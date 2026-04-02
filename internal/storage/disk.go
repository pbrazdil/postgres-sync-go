package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DiskStore struct {
	dir      string
	chunkDir string
	db       *sql.DB
}

func NewDiskStore(dir string) (*DiskStore, error) {
	if strings.TrimSpace(dir) == "" {
		dir = ".pulsesync"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	chunkDir := filepath.Join(dir, "chunks")
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, "metadata.sqlite"))
	if err != nil {
		return nil, err
	}
	if err := initDiskSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &DiskStore{
		dir:      dir,
		chunkDir: chunkDir,
		db:       db,
	}, nil
}

func (s *DiskStore) Kind() string {
	return "disk"
}

func (s *DiskStore) LoadShapes(ctx context.Context) ([]PersistedShape, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT handle, hash, definition_json, schema_json, snapshot_json, materialized_json, current_offset, last_access_unix, generation, deleted
		 FROM shapes
		 ORDER BY handle`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shapes []PersistedShape
	for rows.Next() {
		var (
			shape          PersistedShape
			lastAccessUnix int64
			deleted        int
		)
		if err := rows.Scan(
			&shape.Handle,
			&shape.Hash,
			&shape.Definition,
			&shape.Schema,
			&shape.Snapshot,
			&shape.Materialized,
			&shape.CurrentOffset,
			&lastAccessUnix,
			&shape.Generation,
			&deleted,
		); err != nil {
			return nil, err
		}
		shape.LastAccess = time.Unix(lastAccessUnix, 0).UTC()
		shape.Deleted = deleted == 1

		changes, err := s.loadShapeChanges(ctx, shape.Handle)
		if err != nil {
			shape.Changes = []json.RawMessage{json.RawMessage(`{invalid`)}
			shapes = append(shapes, shape)
			continue
		}
		shape.Changes = changes
		shapes = append(shapes, shape)
	}

	return shapes, rows.Err()
}

func (s *DiskStore) SaveShape(ctx context.Context, shape PersistedShape) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	existingCount, nextSeq, existingChunks, err := s.loadChunkMetadata(ctx, tx, shape.Handle)
	if err != nil {
		return err
	}

	if len(shape.Changes) < existingCount {
		if err := s.removeChunks(existingChunks); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE handle = ?`, shape.Handle); err != nil {
			return err
		}
		existingCount = 0
		nextSeq = 0
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO shapes(handle, hash, definition_json, schema_json, snapshot_json, materialized_json, current_offset, last_access_unix, generation, deleted, change_count)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(handle) DO UPDATE SET
		   hash = excluded.hash,
		   definition_json = excluded.definition_json,
		   schema_json = excluded.schema_json,
		   snapshot_json = excluded.snapshot_json,
		   materialized_json = excluded.materialized_json,
		   current_offset = excluded.current_offset,
		   last_access_unix = excluded.last_access_unix,
		   generation = excluded.generation,
		   deleted = excluded.deleted,
		   change_count = excluded.change_count`,
		shape.Handle,
		shape.Hash,
		shape.Definition,
		shape.Schema,
		shape.Snapshot,
		shape.Materialized,
		shape.CurrentOffset,
		shape.LastAccess.UTC().Unix(),
		shape.Generation,
		boolToInt(shape.Deleted),
		len(shape.Changes),
	); err != nil {
		return err
	}

	if len(shape.Changes) > existingCount {
		chunkPath, err := s.writeChunk(shape.Handle, nextSeq, shape.Changes[existingCount:])
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO chunks(handle, seq, path, message_count) VALUES(?, ?, ?, ?)`,
			shape.Handle,
			nextSeq,
			chunkPath,
			len(shape.Changes)-existingCount,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *DiskStore) LoadRuntimeCheckpoint(ctx context.Context) (RuntimeCheckpoint, bool, error) {
	var checkpoint RuntimeCheckpoint
	err := s.db.QueryRowContext(
		ctx,
		`SELECT slot_name, last_confirmed_lsn, system_id, timeline, db_name
		 FROM runtime_checkpoint
		 WHERE id = 1`,
	).Scan(
		&checkpoint.SlotName,
		&checkpoint.LastConfirmedLSN,
		&checkpoint.SystemID,
		&checkpoint.Timeline,
		&checkpoint.DBName,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return RuntimeCheckpoint{}, false, nil
	case err != nil:
		return RuntimeCheckpoint{}, false, err
	default:
		return checkpoint, true, nil
	}
}

func (s *DiskStore) SaveRuntimeCheckpoint(ctx context.Context, checkpoint RuntimeCheckpoint) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO runtime_checkpoint(id, slot_name, last_confirmed_lsn, system_id, timeline, db_name)
		 VALUES(1, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   slot_name = excluded.slot_name,
		   last_confirmed_lsn = excluded.last_confirmed_lsn,
		   system_id = excluded.system_id,
		   timeline = excluded.timeline,
		   db_name = excluded.db_name`,
		checkpoint.SlotName,
		checkpoint.LastConfirmedLSN,
		checkpoint.SystemID,
		checkpoint.Timeline,
		checkpoint.DBName,
	)
	return err
}

func (s *DiskStore) Close(context.Context) error {
	return s.db.Close()
}

func initDiskSchema(db *sql.DB) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS shapes(
			handle TEXT PRIMARY KEY,
			hash TEXT NOT NULL,
			definition_json BLOB NOT NULL,
			schema_json BLOB NOT NULL,
			snapshot_json BLOB NOT NULL,
			materialized_json BLOB NOT NULL,
			current_offset TEXT NOT NULL,
			last_access_unix INTEGER NOT NULL,
			generation INTEGER NOT NULL,
			deleted INTEGER NOT NULL,
			change_count INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS chunks(
			handle TEXT NOT NULL,
			seq INTEGER NOT NULL,
			path TEXT NOT NULL,
			message_count INTEGER NOT NULL,
			PRIMARY KEY(handle, seq),
			FOREIGN KEY(handle) REFERENCES shapes(handle) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS runtime_checkpoint(
			id INTEGER PRIMARY KEY CHECK(id = 1),
			slot_name TEXT NOT NULL,
			last_confirmed_lsn TEXT NOT NULL,
			system_id TEXT NOT NULL,
			timeline INTEGER NOT NULL,
			db_name TEXT NOT NULL
		)`,
	}

	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *DiskStore) loadChunkMetadata(ctx context.Context, tx *sql.Tx, handle string) (int, int, []string, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT change_count FROM shapes WHERE handle = ?`, handle).Scan(&count)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		count = 0
	case err != nil:
		return 0, 0, nil, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT seq, path FROM chunks WHERE handle = ? ORDER BY seq`, handle)
	if err != nil {
		return 0, 0, nil, err
	}
	defer rows.Close()

	nextSeq := 0
	var paths []string
	for rows.Next() {
		var (
			seq  int
			path string
		)
		if err := rows.Scan(&seq, &path); err != nil {
			return 0, 0, nil, err
		}
		paths = append(paths, path)
		if seq >= nextSeq {
			nextSeq = seq + 1
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, nil, err
	}

	return count, nextSeq, paths, nil
}

func (s *DiskStore) writeChunk(handle string, seq int, changes []json.RawMessage) (string, error) {
	handleDir := filepath.Join(s.chunkDir, sanitizePath(handle))
	if err := os.MkdirAll(handleDir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(handleDir, fmt.Sprintf("%06d.json", seq))
	tempPath := path + ".tmp"

	encoded, err := json.Marshal(changes)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *DiskStore) loadShapeChanges(ctx context.Context, handle string) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM chunks WHERE handle = ? ORDER BY seq`, handle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var changes []json.RawMessage
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var chunk []json.RawMessage
		if err := json.Unmarshal(data, &chunk); err != nil {
			return nil, err
		}
		changes = append(changes, chunk...)
	}
	return changes, nil
}

func (s *DiskStore) removeChunks(paths []string) error {
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func sanitizePath(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, value)
	value = strings.Trim(value, "_")
	if value == "" {
		return "shape"
	}
	return value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
