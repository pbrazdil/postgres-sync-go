package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiskStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDiskStore() error = %v", err)
	}
	defer func() {
		_ = store.Close(context.Background())
	}()

	shape := PersistedShape{
		Handle:        "shape-1",
		Hash:          "hash-1",
		Definition:    mustJSON(t, map[string]any{"table": "items"}),
		Schema:        mustJSON(t, map[string]any{"id": map[string]any{"type": "uuid", "pk_index": 0}}),
		Snapshot:      mustJSON(t, []any{map[string]any{"headers": map[string]any{"operation": "insert"}, "key": "1"}}),
		Materialized:  mustJSON(t, map[string]any{"1": map[string]any{"id": "1"}}),
		CurrentOffset: "0_1",
		LastAccess:    time.Unix(1_700_000_000, 0).UTC(),
		Generation:    7,
		Changes: []json.RawMessage{
			mustJSON(t, map[string]any{"headers": map[string]any{"operation": "insert"}, "key": "1", "offset": "0_1"}),
		},
	}
	checkpoint := RuntimeCheckpoint{
		SlotName:         "slot-1",
		LastConfirmedLSN: "0/16B6C50",
		SystemID:         "system-1",
		Timeline:         1,
		DBName:           "postgres_sync_go",
	}

	if err := store.SaveShape(context.Background(), shape); err != nil {
		t.Fatalf("SaveShape() error = %v", err)
	}
	if err := store.SaveRuntimeCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatalf("SaveRuntimeCheckpoint() error = %v", err)
	}

	loadedShapes, err := store.LoadShapes(context.Background())
	if err != nil {
		t.Fatalf("LoadShapes() error = %v", err)
	}
	if len(loadedShapes) != 1 {
		t.Fatalf("LoadShapes() length = %d, want 1", len(loadedShapes))
	}
	if loadedShapes[0].Handle != shape.Handle || loadedShapes[0].CurrentOffset != shape.CurrentOffset {
		t.Fatalf("loaded shape = %+v", loadedShapes[0])
	}
	if len(loadedShapes[0].Changes) != 1 {
		t.Fatalf("loaded changes length = %d, want 1", len(loadedShapes[0].Changes))
	}

	loadedCheckpoint, ok, err := store.LoadRuntimeCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("LoadRuntimeCheckpoint() error = %v", err)
	}
	if !ok {
		t.Fatalf("LoadRuntimeCheckpoint() ok = false, want true")
	}
	if loadedCheckpoint != checkpoint {
		t.Fatalf("loaded checkpoint = %+v, want %+v", loadedCheckpoint, checkpoint)
	}
}

func TestDiskStoreCorruptShapeDoesNotAbortCatalogLoad(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewDiskStore(dir)
	if err != nil {
		t.Fatalf("NewDiskStore() error = %v", err)
	}

	shape := PersistedShape{
		Handle:        "shape-1",
		Hash:          "hash-1",
		Definition:    mustJSON(t, map[string]any{"table": "items"}),
		Schema:        mustJSON(t, map[string]any{"id": map[string]any{"type": "uuid", "pk_index": 0}}),
		Snapshot:      mustJSON(t, []any{}),
		Materialized:  mustJSON(t, map[string]any{}),
		CurrentOffset: "0_0",
		LastAccess:    time.Unix(1_700_000_000, 0).UTC(),
		Generation:    1,
		Changes: []json.RawMessage{
			mustJSON(t, map[string]any{"headers": map[string]any{"operation": "insert"}, "key": "1"}),
		},
	}
	if err := store.SaveShape(context.Background(), shape); err != nil {
		t.Fatalf("SaveShape() error = %v", err)
	}
	_ = store.Close(context.Background())

	paths, err := filepath.Glob(filepath.Join(dir, "chunks", "*", "*.json"))
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("chunk paths = %+v, want 1 entry", paths)
	}
	chunkPath := paths[0]
	if err := os.WriteFile(chunkPath, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	reopened, err := NewDiskStore(dir)
	if err != nil {
		t.Fatalf("NewDiskStore() reopen error = %v", err)
	}
	defer func() {
		_ = reopened.Close(context.Background())
	}()

	loadedShapes, err := reopened.LoadShapes(context.Background())
	if err != nil {
		t.Fatalf("LoadShapes() error = %v", err)
	}
	if len(loadedShapes) != 1 {
		t.Fatalf("LoadShapes() length = %d, want 1", len(loadedShapes))
	}
	if string(loadedShapes[0].Changes[0]) != "{invalid" {
		t.Fatalf("corrupt shape marker = %q", string(loadedShapes[0].Changes[0]))
	}
}

func TestDiskStoreCompactsDeletedShapeChunks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewDiskStore(dir)
	if err != nil {
		t.Fatalf("NewDiskStore() error = %v", err)
	}
	defer func() {
		_ = store.Close(context.Background())
	}()

	shape := PersistedShape{
		Handle:        "shape-1",
		Hash:          "hash-1",
		Definition:    mustJSON(t, map[string]any{"table": "items"}),
		Schema:        mustJSON(t, map[string]any{"id": map[string]any{"type": "uuid", "pk_index": 0}}),
		Snapshot:      mustJSON(t, []any{}),
		Materialized:  mustJSON(t, map[string]any{}),
		CurrentOffset: "0_0",
		LastAccess:    time.Unix(1_700_000_000, 0).UTC(),
		Generation:    1,
		Changes: []json.RawMessage{
			mustJSON(t, map[string]any{"headers": map[string]any{"operation": "insert"}, "key": "1"}),
		},
	}
	if err := store.SaveShape(context.Background(), shape); err != nil {
		t.Fatalf("SaveShape() error = %v", err)
	}

	stats, err := store.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.ChunkCount == 0 || stats.ChangeCount == 0 || stats.ChunkBytes == 0 {
		t.Fatalf("stats before delete = %+v, want persisted chunk data", stats)
	}

	shape.Deleted = true
	if err := store.SaveShape(context.Background(), shape); err != nil {
		t.Fatalf("SaveShape(deleted) error = %v", err)
	}

	stats, err = store.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() after delete error = %v", err)
	}
	if stats.ChunkCount != 0 || stats.ChangeCount != 0 || stats.DeletedShapeCount != 1 {
		t.Fatalf("stats after delete = %+v, want deleted shape with no chunks", stats)
	}

	orphanDir := filepath.Join(dir, "chunks", "orphan")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	orphanPath := filepath.Join(orphanDir, "000000.json")
	if err := os.WriteFile(orphanPath, []byte(`[{}]`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, err := store.Compact(context.Background())
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if result.RemovedChunks != 1 || result.RemovedBytes == 0 {
		t.Fatalf("Compact() = %+v, want orphan chunk removal", result)
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Fatalf("orphan chunk still exists, stat err = %v", err)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return encoded
}
