package pg

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/petrbrazdil/pulsesync/internal/config"
	"github.com/petrbrazdil/pulsesync/internal/shapes"
)

type ServiceStatus string

const (
	StatusStarting ServiceStatus = "starting"
	StatusWaiting  ServiceStatus = "waiting"
	StatusActive   ServiceStatus = "active"
	StatusSleeping ServiceStatus = "sleeping"
	StatusStopped  ServiceStatus = "stopped"
)

type Runtime struct {
	cfg               config.Config
	mu                sync.RWMutex
	startupMu         sync.Mutex
	status            ServiceStatus
	queryPoolConfig   *pgxpool.Config
	queryPool         *pgxpool.Pool
	systemIdentity    pglogrepl.IdentifySystemResult
	shapes            *shapes.Manager
	replicationConn   *pgconn.PgConn
	replicationCancel context.CancelFunc
	replicationWG     sync.WaitGroup
}

func NewRuntime(cfg config.Config, manager *shapes.Manager) *Runtime {
	return &Runtime{
		cfg:    cfg,
		status: StatusStarting,
		shapes: manager,
	}
}

func (r *Runtime) Start(context.Context) error {
	poolConfig, err := pgxpool.ParseConfig(r.cfg.PooledDatabaseURL)
	if err != nil {
		return err
	}

	poolConfig.MaxConns = int32(r.cfg.DBPoolSize)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.queryPoolConfig = poolConfig
	r.status = StatusActive

	return nil
}

func (r *Runtime) Status() ServiceStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.status
}

func (r *Runtime) Close(context.Context) error {
	r.mu.Lock()
	if r.replicationCancel != nil {
		r.replicationCancel()
		r.replicationCancel = nil
	}
	replConn := r.replicationConn
	r.replicationConn = nil
	r.mu.Unlock()
	if replConn != nil {
		_ = replConn.Close(context.Background())
	}
	r.replicationWG.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.queryPool != nil {
		r.queryPool.Close()
		r.queryPool = nil
	}
	r.status = StatusStopped
	return nil
}

func (r *Runtime) Snapshot(ctx context.Context, request shapes.SnapshotRequest) (shapes.SnapshotResult, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return shapes.SnapshotResult{}, err
	}

	if request.Metadata {
		return r.snapshotWithMetadata(ctx, pool, request)
	}

	columns, err := describeRelation(ctx, pool, request.Definition.Relation)
	if err != nil {
		return shapes.SnapshotResult{}, err
	}

	result := shapes.SnapshotResult{
		Schema: map[string]shapes.ColumnSchema{},
	}

	for _, column := range columns {
		result.Schema[column.Name] = shapes.ColumnSchema{
			Type:    column.Type,
			PKIndex: column.PKIndex,
		}
	}

	if request.Mode == shapes.SnapshotModeValidateOnly {
		return result, nil
	}

	selectedColumns := projectedColumns(columns, request.Definition.Columns)
	query, args := buildSnapshotQuery(request.Definition, selectedColumns)
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return shapes.SnapshotResult{}, shapes.RelationNotFoundError{Relation: request.Definition.Relation}
		}
		return shapes.SnapshotResult{}, err
	}
	defer rows.Close()

	fieldDescriptions := rows.FieldDescriptions()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return shapes.SnapshotResult{}, err
		}

		row := shapes.Row{}
		for index, field := range fieldDescriptions {
			if values[index] == nil {
				row[string(field.Name)] = nil
				continue
			}
			row[string(field.Name)] = fmt.Sprint(values[index])
		}
		result.Rows = append(result.Rows, row)
	}

	return result, rows.Err()
}

func (r *Runtime) snapshotWithMetadata(ctx context.Context, pool *pgxpool.Pool, request shapes.SnapshotRequest) (shapes.SnapshotResult, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return shapes.SnapshotResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	columns, err := describeRelation(ctx, tx, request.Definition.Relation)
	if err != nil {
		return shapes.SnapshotResult{}, err
	}

	result := shapes.SnapshotResult{
		Schema: map[string]shapes.ColumnSchema{},
	}
	for _, column := range columns {
		result.Schema[column.Name] = shapes.ColumnSchema{
			Type:    column.Type,
			PKIndex: column.PKIndex,
		}
	}

	metadata, err := loadSnapshotMetadata(ctx, tx)
	if err != nil {
		return shapes.SnapshotResult{}, err
	}
	result.Metadata = metadata

	if request.Mode == shapes.SnapshotModeValidateOnly {
		if err := tx.Commit(ctx); err != nil {
			return shapes.SnapshotResult{}, err
		}
		return result, nil
	}

	selectedColumns := projectedColumns(columns, request.Definition.Columns)
	query, args := buildSnapshotQuery(request.Definition, selectedColumns)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return shapes.SnapshotResult{}, shapes.RelationNotFoundError{Relation: request.Definition.Relation}
		}
		return shapes.SnapshotResult{}, err
	}
	defer rows.Close()

	fieldDescriptions := rows.FieldDescriptions()
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return shapes.SnapshotResult{}, err
		}

		row := shapes.Row{}
		for index, field := range fieldDescriptions {
			if values[index] == nil {
				row[string(field.Name)] = nil
				continue
			}
			row[string(field.Name)] = fmt.Sprint(values[index])
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return shapes.SnapshotResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return shapes.SnapshotResult{}, err
	}

	return result, nil
}

type relationQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *Runtime) ensurePool(ctx context.Context) (*pgxpool.Pool, error) {
	r.mu.RLock()
	if r.queryPool != nil {
		defer r.mu.RUnlock()
		return r.queryPool, nil
	}
	if r.queryPoolConfig == nil {
		r.mu.RUnlock()
		return nil, errors.New("PulseSync engine has not been started")
	}
	configCopy := r.queryPoolConfig.Copy()
	r.mu.RUnlock()

	pool, err := pgxpool.NewWithConfig(ctx, configCopy)
	if err != nil {
		return nil, err
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	r.mu.Lock()
	if r.queryPool == nil {
		r.queryPool = pool
	} else {
		pool.Close()
	}
	queryPool := r.queryPool
	r.mu.Unlock()

	if err := r.ensureReplication(context.Background()); err != nil {
		return nil, err
	}
	return queryPool, nil
}

type describedColumn struct {
	Name    string
	Type    string
	PKIndex *int
}

func describeRelation(ctx context.Context, pool relationQueryer, relation shapes.Relation) ([]describedColumn, error) {
	const query = `
SELECT
	a.attname,
	format_type(a.atttypid, a.atttypmod) AS type_name,
	CASE
		WHEN i.indisprimary THEN array_position(i.indkey::smallint[], a.attnum) - 1
		ELSE NULL
	END AS pk_index
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
LEFT JOIN pg_index i ON i.indrelid = c.oid AND i.indisprimary
WHERE n.nspname = $1 AND c.relname = $2
ORDER BY a.attnum`

	rows, err := pool.Query(ctx, query, relation.Schema, relation.Table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := []describedColumn{}
	for rows.Next() {
		var (
			name     string
			typeName string
			pkIndex  *int
		)
		if err := rows.Scan(&name, &typeName, &pkIndex); err != nil {
			return nil, err
		}
		columns = append(columns, describedColumn{Name: name, Type: typeName, PKIndex: pkIndex})
	}

	if len(columns) == 0 {
		return nil, shapes.RelationNotFoundError{Relation: relation}
	}

	return columns, rows.Err()
}

func loadSnapshotMetadata(ctx context.Context, tx pgx.Tx) (*shapes.SnapshotMetadata, error) {
	var snapshotText string
	if err := tx.QueryRow(ctx, "SELECT txid_current_snapshot()::text").Scan(&snapshotText); err != nil {
		return nil, err
	}

	var databaseLSN string
	if err := tx.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&databaseLSN); err != nil {
		return nil, err
	}

	xmin, xmax, xipList := parseSnapshotText(snapshotText)
	return &shapes.SnapshotMetadata{
		SnapshotMark: rand.Intn(1_000_000_000),
		DatabaseLSN:  databaseLSN,
		XMin:         xmin,
		XMax:         xmax,
		XIPList:      xipList,
	}, nil
}

func parseSnapshotText(value string) (string, string, []string) {
	parts := strings.SplitN(strings.TrimSpace(value), ":", 3)
	if len(parts) < 2 {
		return "0", "0", nil
	}

	var xipList []string
	if len(parts) == 3 && strings.TrimSpace(parts[2]) != "" {
		xipList = strings.Split(parts[2], ",")
	}
	return parts[0], parts[1], xipList
}

func projectedColumns(described []describedColumn, explicit []string) []describedColumn {
	if len(explicit) == 0 {
		return described
	}

	seen := map[string]bool{}
	columns := make([]describedColumn, 0, len(explicit))

	for _, name := range explicit {
		for _, column := range described {
			if column.Name == name && !seen[name] {
				columns = append(columns, column)
				seen[name] = true
			}
		}
	}

	for _, column := range described {
		if column.PKIndex != nil && !seen[column.Name] {
			columns = append(columns, column)
			seen[column.Name] = true
		}
	}

	return columns
}

func buildSnapshotQuery(def shapes.Definition, columns []describedColumn) (string, []any) {
	selectParts := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted := quoteIdentifier(column.Name)
		selectParts = append(selectParts, fmt.Sprintf("%s::text AS %s", quoted, quoted))
	}

	query := fmt.Sprintf(
		"SELECT %s FROM %s.%s",
		strings.Join(selectParts, ", "),
		quoteIdentifier(def.Relation.Schema),
		quoteIdentifier(def.Relation.Table),
	)

	args := paramsAsArgs(def.Params)
	whereClauses := make([]string, 0, 2)
	if def.Where != "" {
		whereClauses = append(whereClauses, "("+def.Where+")")
	}
	if def.Subset != nil && def.Subset.Where != "" {
		whereClauses = append(whereClauses, "("+def.Subset.Where+")")
		for _, value := range paramsAsArgs(def.Subset.Params) {
			args = append(args, value)
		}
	}
	if len(whereClauses) > 0 {
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	if def.Subset != nil && def.Subset.OrderBy != "" {
		query += " ORDER BY " + def.Subset.OrderBy
	}
	if def.Subset != nil && def.Subset.Limit != nil {
		query += fmt.Sprintf(" LIMIT %d", *def.Subset.Limit)
	}
	if def.Subset != nil && def.Subset.Offset != nil {
		query += fmt.Sprintf(" OFFSET %d", *def.Subset.Offset)
	}

	return query, args
}

func paramsAsArgs(values map[string]string) []any {
	if len(values) == 0 {
		return nil
	}

	keys := make([]int, 0, len(values))
	for key := range values {
		parsed, err := strconv.Atoi(key)
		if err != nil {
			continue
		}
		keys = append(keys, parsed)
	}
	sort.Ints(keys)

	args := make([]any, 0, len(keys))
	for _, key := range keys {
		args = append(args, values[strconv.Itoa(key)])
	}
	return args
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
