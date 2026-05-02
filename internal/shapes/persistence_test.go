package shapes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pbrazdil/postgres-sync-go/internal/storage"
)

func TestManagerHydratesPersistedDiskState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := storage.NewDiskStore(dir)
	if err != nil {
		t.Fatalf("NewDiskStore() error = %v", err)
	}

	manager, err := NewManager(store)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	definition := Definition{
		Relation: Relation{Schema: "public", Table: "items"},
	}
	initial := SnapshotResult{
		Schema: map[string]ColumnSchema{
			"id":    {Type: "uuid", PKIndex: intPtr(0)},
			"value": {Type: "text"},
		},
		Rows: []Row{{"id": "1", "value": "before"}},
	}

	state, err := manager.UpsertSnapshot(definition, initial)
	if err != nil {
		t.Fatalf("UpsertSnapshot() error = %v", err)
	}
	if _, err := manager.Append(state.Handle, []Message{
		{
			Headers: map[string]any{"operation": "update"},
			Key:     "1",
			Value:   Row{"id": "1", "value": "after"},
		},
	}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := storage.NewDiskStore(dir)
	if err != nil {
		t.Fatalf("NewDiskStore() reopen error = %v", err)
	}
	defer func() {
		_ = reopened.Close(context.Background())
	}()

	hydrated, err := NewManager(reopened)
	if err != nil {
		t.Fatalf("NewManager() hydrated error = %v", err)
	}

	loaded, ok := hydrated.LookupByHandle(state.Handle)
	if !ok {
		t.Fatalf("LookupByHandle(%q) = !ok", state.Handle)
	}
	if loaded.CurrentOffset != "0_1" {
		t.Fatalf("CurrentOffset = %q, want 0_1", loaded.CurrentOffset)
	}
	if got := loaded.Materialized["1"]["value"]; got != "after" {
		t.Fatalf("materialized value = %v, want after", got)
	}
}

func TestManagerCorruptDiskShapeBecomesMustRefetchTombstone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := storage.NewDiskStore(dir)
	if err != nil {
		t.Fatalf("NewDiskStore() error = %v", err)
	}

	manager, err := NewManager(store)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	state, err := manager.UpsertSnapshot(Definition{
		Relation: Relation{Schema: "public", Table: "items"},
	}, SnapshotResult{
		Schema: map[string]ColumnSchema{
			"id": {Type: "uuid", PKIndex: intPtr(0)},
		},
		Rows: []Row{{"id": "1"}},
	})
	if err != nil {
		t.Fatalf("UpsertSnapshot() error = %v", err)
	}
	if _, err := manager.Append(state.Handle, []Message{
		{
			Headers: map[string]any{"operation": "insert"},
			Key:     "1",
			Value:   Row{"id": "1"},
		},
	}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

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

	reopened, err := storage.NewDiskStore(dir)
	if err != nil {
		t.Fatalf("NewDiskStore() reopen error = %v", err)
	}
	defer func() {
		_ = reopened.Close(context.Background())
	}()

	hydrated, err := NewManager(reopened)
	if err != nil {
		t.Fatalf("NewManager() hydrated error = %v", err)
	}

	if _, ok := hydrated.LookupByHandle(state.Handle); ok {
		t.Fatalf("LookupByHandle(%q) = ok, want false", state.Handle)
	}
	if _, _, err := hydrated.Read(state.Handle, InitialOffset); !errors.Is(err, ErrShapeDeleted) {
		t.Fatalf("Read() error = %v, want ErrShapeDeleted", err)
	}
}
