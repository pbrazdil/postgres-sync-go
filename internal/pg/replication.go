package pg

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pbrazdil/postgres-sync-go/internal/shapes"
	"github.com/pbrazdil/postgres-sync-go/internal/storage"
)

const replicationStandbyTimeout = 10 * time.Second

func (r *Runtime) ensureReplication(ctx context.Context) error {
	r.mu.RLock()
	if r.replicationConn != nil {
		r.mu.RUnlock()
		return nil
	}
	r.mu.RUnlock()

	r.startupMu.Lock()
	defer r.startupMu.Unlock()

	r.mu.RLock()
	if r.replicationConn != nil {
		r.mu.RUnlock()
		return nil
	}
	pool := r.queryPool
	r.mu.RUnlock()
	if pool == nil {
		return errors.New("postgres-sync-go engine has not been started")
	}

	if err := r.ensurePublication(ctx, pool); err != nil {
		return err
	}

	replConfig, err := pgconn.ParseConfig(r.cfg.DatabaseURL)
	if err != nil {
		return err
	}
	replConfig.RuntimeParams["replication"] = "database"

	conn, err := pgconn.ConnectConfig(ctx, replConfig)
	if err != nil {
		return err
	}

	sysident, err := pglogrepl.IdentifySystem(ctx, conn)
	if err != nil {
		_ = conn.Close(context.Background())
		return err
	}

	slotName, startLSN, err := r.prepareReplicationStart(ctx, pool, conn, sysident)
	if err != nil {
		_ = conn.Close(context.Background())
		return err
	}

	if err := pglogrepl.StartReplication(
		ctx,
		conn,
		slotName,
		startLSN,
		pglogrepl.StartReplicationOptions{
			Mode: pglogrepl.LogicalReplication,
			PluginArgs: []string{
				"proto_version '1'",
				fmt.Sprintf("publication_names '%s'", r.publicationName()),
			},
		},
	); err != nil {
		_ = conn.Close(context.Background())
		return err
	}

	replCtx, cancel := context.WithCancel(context.Background())

	r.mu.Lock()
	r.replicationConn = conn
	r.replicationCancel = cancel
	r.replicationSlot = slotName
	r.systemIdentity = sysident
	r.relationCache = map[uint32]relationMetadata{}
	r.status = StatusActive
	r.mu.Unlock()

	r.replicationWG.Add(1)
	go func() {
		defer r.replicationWG.Done()
		r.replicationLoop(replCtx, conn, startLSN)
	}()

	if err := r.saveRuntimeCheckpoint(context.Background(), slotName, startLSN, sysident); err != nil {
		return err
	}

	return nil
}

func (r *Runtime) prepareReplicationStart(
	ctx context.Context,
	pool *pgxpool.Pool,
	conn *pgconn.PgConn,
	sysident pglogrepl.IdentifySystemResult,
) (string, pglogrepl.LSN, error) {
	slotName := r.replicationSlotName()
	startLSN := sysident.XLogPos

	checkpoint, hasCheckpoint, err := r.store.LoadRuntimeCheckpoint(ctx)
	if err != nil {
		return "", 0, err
	}

	slotExists, err := r.replicationSlotExists(ctx, pool, slotName)
	if err != nil {
		return "", 0, err
	}

	validCheckpoint := false
	if r.store.Kind() == "memory" {
		hasCheckpoint = false
	}

	if r.store.Kind() == "disk" && hasCheckpoint {
		validCheckpoint = checkpointCompatible(checkpoint, slotName, sysident, currentDatabaseName(r.cfg.DatabaseURL))
		if !validCheckpoint {
			if _, err := r.shapes.InvalidateAll(); err != nil {
				return "", 0, err
			}
		}
	}

	if r.store.Kind() == "disk" && validCheckpoint && !slotExists {
		if _, err := r.shapes.InvalidateAll(); err != nil {
			return "", 0, err
		}
		validCheckpoint = false
	}

	if !slotExists {
		slot, err := pglogrepl.CreateReplicationSlot(
			ctx,
			conn,
			slotName,
			"pgoutput",
			pglogrepl.CreateReplicationSlotOptions{
				Temporary: r.store.Kind() != "disk",
				Mode:      pglogrepl.LogicalReplication,
			},
		)
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return "", 0, err
		}
		if slot.ConsistentPoint != "" {
			if consistentPoint, parseErr := pglogrepl.ParseLSN(slot.ConsistentPoint); parseErr == nil {
				startLSN = consistentPoint
			}
		}
	} else if validCheckpoint {
		parsed, err := pglogrepl.ParseLSN(checkpoint.LastConfirmedLSN)
		if err == nil {
			startLSN = parsed
		}
	}

	return slotName, startLSN, nil
}

func checkpointCompatible(checkpoint storage.RuntimeCheckpoint, slotName string, sysident pglogrepl.IdentifySystemResult, dbName string) bool {
	return checkpoint.SlotName == slotName &&
		checkpoint.SystemID == sysident.SystemID &&
		checkpoint.Timeline == sysident.Timeline &&
		checkpoint.DBName == dbName &&
		strings.TrimSpace(checkpoint.LastConfirmedLSN) != ""
}

func currentDatabaseName(databaseURL string) string {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(parsed.Path, "/")
}

func (r *Runtime) saveRuntimeCheckpoint(ctx context.Context, slotName string, lsn pglogrepl.LSN, sysident pglogrepl.IdentifySystemResult) error {
	return r.store.SaveRuntimeCheckpoint(ctx, storage.RuntimeCheckpoint{
		SlotName:         slotName,
		LastConfirmedLSN: lsn.String(),
		SystemID:         sysident.SystemID,
		Timeline:         sysident.Timeline,
		DBName:           currentDatabaseName(r.cfg.DatabaseURL),
	})
}

func (r *Runtime) replicationSlotExists(ctx context.Context, pool *pgxpool.Pool, slotName string) (bool, error) {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = $1)`, slotName).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (r *Runtime) ensurePublication(ctx context.Context, pool *pgxpool.Pool) error {
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = $1)", r.publicationName()).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err := pool.Exec(ctx, "CREATE PUBLICATION "+r.publicationName()+" FOR ALL TABLES")
	return err
}

func (r *Runtime) replicationLoop(ctx context.Context, conn *pgconn.PgConn, clientXLogPos pglogrepl.LSN) {
	nextStandbyMessageDeadline := time.Now().Add(replicationStandbyTimeout)
	batch := ChangeBatch{}

	for {
		if time.Now().After(nextStandbyMessageDeadline) {
			if err := pglogrepl.SendStandbyStatusUpdate(
				context.Background(),
				conn,
				pglogrepl.StandbyStatusUpdate{WALWritePosition: clientXLogPos},
			); err != nil {
				r.handleReplicationError(err, conn)
				return
			}
			nextStandbyMessageDeadline = time.Now().Add(replicationStandbyTimeout)
		}

		receiveCtx, cancel := context.WithDeadline(ctx, nextStandbyMessageDeadline)
		rawMsg, err := conn.ReceiveMessage(receiveCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if pgconn.Timeout(err) {
				continue
			}
			r.handleReplicationError(err, conn)
			return
		}

		if errMsg, ok := rawMsg.(*pgproto3.ErrorResponse); ok {
			r.handleReplicationError(fmt.Errorf("received Postgres WAL error: %s", errMsg.Message), conn)
			return
		}

		msg, ok := rawMsg.(*pgproto3.CopyData)
		if !ok {
			continue
		}

		switch msg.Data[0] {
		case pglogrepl.PrimaryKeepaliveMessageByteID:
			pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(msg.Data[1:])
			if err != nil {
				r.handleReplicationError(err, conn)
				return
			}
			if pkm.ServerWALEnd > clientXLogPos {
				clientXLogPos = pkm.ServerWALEnd
			}
			if pkm.ReplyRequested {
				nextStandbyMessageDeadline = time.Time{}
			}

		case pglogrepl.XLogDataByteID:
			xld, err := pglogrepl.ParseXLogData(msg.Data[1:])
			if err != nil {
				r.handleReplicationError(err, conn)
				return
			}

			commitLSN, err := r.handleLogicalMessage(ctx, xld.WALData, &batch)
			if err != nil {
				r.handleReplicationError(err, conn)
				return
			}
			if commitLSN > clientXLogPos {
				clientXLogPos = commitLSN
			}
			if xld.WALStart > clientXLogPos {
				clientXLogPos = xld.WALStart
			}
		}
	}
}

func (r *Runtime) handleLogicalMessage(ctx context.Context, walData []byte, batch *ChangeBatch) (pglogrepl.LSN, error) {
	logicalMsg, err := pglogrepl.Parse(walData)
	if err != nil {
		return 0, err
	}

	switch msg := logicalMsg.(type) {
	case *pglogrepl.RelationMessage:
		if _, err := r.cacheRelationMetadata(ctx, msg.RelationID, msg); err != nil {
			return 0, err
		}
	case *pglogrepl.BeginMessage:
		batch.Reset(msg.Xid)
	case *pglogrepl.InsertMessage:
		if err := r.recordInsert(batch, msg); err != nil {
			return 0, err
		}
	case *pglogrepl.UpdateMessage:
		if err := r.recordUpdate(batch, msg); err != nil {
			return 0, err
		}
	case *pglogrepl.DeleteMessage:
		if err := r.recordDelete(batch, msg); err != nil {
			return 0, err
		}
	case *pglogrepl.TruncateMessage:
		for _, relationID := range msg.RelationIDs {
			metadata, ok := r.relationMetadata(relationID)
			if !ok {
				continue
			}
			if err := r.invalidateDependentShapesForRelation(metadata); err != nil {
				return 0, err
			}
			if _, err := r.shapes.InvalidateByRelation(metadata.Relation); err != nil {
				return 0, err
			}
			if metadata.RootRelation != metadata.Relation {
				if _, err := r.shapes.InvalidateByRelation(metadata.RootRelation); err != nil {
					return 0, err
				}
			}
		}
	case *pglogrepl.CommitMessage:
		batch.CommitLSN = msg.CommitLSN
		if err := r.applyChangeBatch(ctx, *batch); err != nil {
			return msg.TransactionEndLSN, err
		}
		batch.Reset(0)
		return msg.TransactionEndLSN, nil
	}

	return 0, nil
}

func (r *Runtime) recordInsert(batch *ChangeBatch, message *pglogrepl.InsertMessage) error {
	metadata, ok := r.relationMetadata(message.RelationID)
	if !ok {
		return nil
	}

	newTuple := decodeTupleRow(metadata.Columns, message.Tuple)
	batch.Add(message.RelationID, ChangeRecord{
		Relation:   metadata.Relation,
		Operation:  ChangeInsert,
		PrimaryKey: primaryKeyRowForColumns(metadata.PKColumns, newTuple),
		NewTuple:   newTuple,
	})
	return nil
}

func (r *Runtime) recordUpdate(batch *ChangeBatch, message *pglogrepl.UpdateMessage) error {
	metadata, ok := r.relationMetadata(message.RelationID)
	if !ok {
		return nil
	}

	oldTupleColumns := metadata.Columns
	if message.OldTupleType == pglogrepl.UpdateMessageTupleTypeKey {
		oldTupleColumns = metadata.PKColumns
	}
	oldTuple := decodeTupleRow(oldTupleColumns, message.OldTuple)
	newTuple := decodeTupleRow(metadata.Columns, message.NewTuple)

	if key := primaryKeyRowForColumns(metadata.PKColumns, oldTuple); len(key) > 0 {
		batch.Add(message.RelationID, ChangeRecord{
			Relation:   metadata.Relation,
			Operation:  ChangeUpdate,
			PrimaryKey: key,
			OldTuple:   oldTuple,
			NewTuple:   newTuple,
		})
	}
	if key := primaryKeyRowForColumns(metadata.PKColumns, newTuple); len(key) > 0 {
		batch.Add(message.RelationID, ChangeRecord{
			Relation:   metadata.Relation,
			Operation:  ChangeUpdate,
			PrimaryKey: key,
			OldTuple:   oldTuple,
			NewTuple:   newTuple,
		})
	}
	return nil
}

func (r *Runtime) recordDelete(batch *ChangeBatch, message *pglogrepl.DeleteMessage) error {
	metadata, ok := r.relationMetadata(message.RelationID)
	if !ok {
		return nil
	}

	oldTupleColumns := metadata.Columns
	if message.OldTupleType == pglogrepl.DeleteMessageTupleTypeKey {
		oldTupleColumns = metadata.PKColumns
	}
	oldTuple := decodeTupleRow(oldTupleColumns, message.OldTuple)
	batch.Add(message.RelationID, ChangeRecord{
		Relation:   metadata.Relation,
		Operation:  ChangeDelete,
		PrimaryKey: primaryKeyRowForColumns(metadata.PKColumns, oldTuple),
		OldTuple:   oldTuple,
	})
	return nil
}

func (r *Runtime) applyChangeBatch(ctx context.Context, batch ChangeBatch) error {
	refreshedDependentShapes := map[string]struct{}{}

	for _, relationID := range batch.RelationIDs() {
		metadata, ok := r.relationMetadata(relationID)
		if !ok {
			continue
		}

		changes := batch.ChangesForRelation(relationID)
		if len(changes) == 0 {
			continue
		}

		keyRows := make([]shapes.Row, 0, len(changes))
		for _, change := range changes {
			if len(change.PrimaryKey) == 0 {
				continue
			}
			keyRows = append(keyRows, change.PrimaryKey)
		}
		if len(keyRows) == 0 {
			continue
		}

		for _, state := range r.candidateShapes(metadata) {
			if !definitionSupportsTargetedRefresh(state.Definition) {
				continue
			}

			snapshot, err := r.snapshotChangedKeys(ctx, state.Definition, metadata, keyRows)
			if err != nil {
				var missing shapes.RelationNotFoundError
				if errors.As(err, &missing) {
					if _, invalidateErr := r.shapes.InvalidateByRelation(state.Definition.Relation); invalidateErr != nil {
						return invalidateErr
					}
					continue
				}
				return err
			}

			if _, _, err := r.shapes.RefreshKeysWithMetadata(state.Handle, snapshot, keyRows, shapes.ChangeMetadata{
				KeyRelation:   metadata.Relation,
				CommitLSN:     uint64(batch.CommitLSN),
				TransactionID: batch.XID,
			}); err != nil && !errors.Is(err, shapes.ErrShapeDeleted) && !errors.Is(err, shapes.ErrShapeNotFound) {
				return err
			}
		}

		if err := r.refreshDependentShapesForRelation(ctx, metadata, batch, refreshedDependentShapes); err != nil {
			return err
		}
	}

	if batch.CommitLSN > 0 {
		r.mu.RLock()
		slotName := r.replicationSlot
		sysident := r.systemIdentity
		r.mu.RUnlock()
		if slotName == "" {
			slotName = r.replicationSlotName()
		}
		return r.saveRuntimeCheckpoint(ctx, slotName, batch.CommitLSN, sysident)
	}
	return nil
}

func (r *Runtime) refreshDependentShapesForRelation(ctx context.Context, metadata relationMetadata, batch ChangeBatch, refreshed map[string]struct{}) error {
	for _, state := range r.shapes.ActiveStates() {
		if definitionSupportsTargetedRefresh(state.Definition) {
			continue
		}
		if !definitionRequiresInvalidationForRelation(state.Definition, metadata.Relation, metadata.RootRelation) {
			continue
		}
		if _, ok := refreshed[state.Handle]; ok {
			continue
		}

		snapshot, err := r.Snapshot(ctx, shapes.SnapshotRequest{
			Definition: state.Definition,
			Mode:       shapes.SnapshotModeData,
		})
		if err != nil {
			var missing shapes.RelationNotFoundError
			if errors.As(err, &missing) {
				if _, deleteErr := r.shapes.Delete(state.Handle); deleteErr != nil && !errors.Is(deleteErr, shapes.ErrShapeNotFound) && !errors.Is(deleteErr, shapes.ErrShapeDeleted) {
					return deleteErr
				}
				refreshed[state.Handle] = struct{}{}
				continue
			}
			return err
		}

		if _, _, err := r.shapes.RefreshWithMetadata(state.Handle, snapshot, shapes.ChangeMetadata{
			KeyRelation:      metadata.Relation,
			CommitLSN:        uint64(batch.CommitLSN),
			TransactionID:    batch.XID,
			DependentRefresh: true,
		}); err != nil && !errors.Is(err, shapes.ErrShapeDeleted) && !errors.Is(err, shapes.ErrShapeNotFound) {
			return err
		}
		refreshed[state.Handle] = struct{}{}
	}
	return nil
}

func (r *Runtime) invalidateDependentShapesForRelation(metadata relationMetadata) error {
	for _, state := range r.shapes.ActiveStates() {
		if definitionSupportsTargetedRefresh(state.Definition) {
			continue
		}
		if !definitionRequiresInvalidationForRelation(state.Definition, metadata.Relation, metadata.RootRelation) {
			continue
		}
		if _, err := r.shapes.Delete(state.Handle); err != nil && !errors.Is(err, shapes.ErrShapeNotFound) && !errors.Is(err, shapes.ErrShapeDeleted) {
			return err
		}
	}
	return nil
}

func (r *Runtime) candidateShapes(metadata relationMetadata) []shapes.State {
	states := append([]shapes.State{}, r.shapes.ActiveByRelation(metadata.Relation)...)
	if metadata.RootRelation != metadata.Relation {
		states = append(states, r.shapes.ActiveByRelation(metadata.RootRelation)...)
	}

	sort.Slice(states, func(i, j int) bool {
		return states[i].Handle < states[j].Handle
	})

	deduped := make([]shapes.State, 0, len(states))
	seen := map[string]struct{}{}
	for _, state := range states {
		if _, ok := seen[state.Handle]; ok {
			continue
		}
		seen[state.Handle] = struct{}{}
		deduped = append(deduped, state)
	}
	return deduped
}

func (r *Runtime) snapshotChangedKeys(ctx context.Context, def shapes.Definition, metadata relationMetadata, keyRows []shapes.Row) (shapes.SnapshotResult, error) {
	pool, err := r.ensurePool(ctx)
	if err != nil {
		return shapes.SnapshotResult{}, err
	}

	result := shapes.SnapshotResult{
		Schema: map[string]shapes.ColumnSchema{},
	}
	selectedColumns := targetedSnapshotColumns(metadata, def.Columns)
	for _, column := range selectedColumns {
		result.Schema[column.Name] = shapes.ColumnSchema{
			Type:    column.Type,
			NotNull: column.NotNull,
			PKIndex: column.PKIndex,
		}
	}

	query, args, err := buildTargetedSnapshotQuery(def, selectedColumns, metadata.PKColumns, keyRows)
	if err != nil {
		return shapes.SnapshotResult{}, err
	}

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return shapes.SnapshotResult{}, shapes.RelationNotFoundError{Relation: def.Relation}
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

func (r *Runtime) handleReplicationError(err error, conn *pgconn.PgConn) {
	_ = conn.Close(context.Background())

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.replicationConn == conn {
		r.replicationConn = nil
		r.replicationCancel = nil
		r.replicationSlot = ""
		r.relationCache = map[uint32]relationMetadata{}
		if r.status != StatusStopped {
			r.status = StatusWaiting
		}
	}
	_ = err
}

func (r *Runtime) publicationName() string {
	return compactIdentifier("postgres_sync_go_"+r.cfg.ReplicationStreamID+"_pub", 63)
}

func (r *Runtime) replicationSlotName() string {
	if r.store != nil && r.store.Kind() == "disk" {
		return compactIdentifier("postgres_sync_go_"+r.cfg.ReplicationStreamID+"_slot", 63)
	}

	base := compactIdentifier("postgres_sync_go_"+r.cfg.ReplicationStreamID+"_slot", 48)
	return compactIdentifier(fmt.Sprintf("%s_%x", base, time.Now().UnixNano()), 63)
}

func compactIdentifier(value string, maxLen int) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
		default:
			builder.WriteRune('_')
		}
	}

	compact := builder.String()
	compact = strings.Trim(compact, "_")
	if compact == "" {
		compact = "postgres_sync_go"
	}
	if len(compact) > maxLen {
		compact = compact[:maxLen]
	}
	return compact
}
