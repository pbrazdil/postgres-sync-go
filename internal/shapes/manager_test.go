package shapes

import (
	"testing"

	"github.com/petrbrazdil/pulsesync/internal/storage"
)

func TestManagerChangesOnlyRefreshProducesUpdate(t *testing.T) {
	t.Parallel()

	manager := NewManager(storage.NewMemoryStore())
	definition := Definition{
		Relation: Relation{Schema: "public", Table: "items"},
		Log:      "changes_only",
	}

	initial := SnapshotResult{
		Schema: map[string]ColumnSchema{
			"id":    {Type: "uuid", PKIndex: intPtr(0)},
			"value": {Type: "text"},
		},
		Rows: []Row{
			{"id": "1", "value": "before"},
		},
	}

	state := manager.UpsertSnapshot(definition, initial)
	if len(state.Snapshot) != 0 {
		t.Fatalf("Snapshot length = %d, want 0", len(state.Snapshot))
	}

	updated := SnapshotResult{
		Schema: initial.Schema,
		Rows: []Row{
			{"id": "1", "value": "after"},
		},
	}

	state, messages, err := manager.Refresh(state.Handle, updated)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("messages length = %d, want 1", len(messages))
	}
	if messages[0].Headers["operation"] != "update" {
		t.Fatalf("operation = %v", messages[0].Headers["operation"])
	}
	if messages[0].Offset != "0_1" {
		t.Fatalf("offset = %q", messages[0].Offset)
	}
	if messages[0].Value["id"] != "1" || messages[0].Value["value"] != "after" {
		t.Fatalf("value = %+v", messages[0].Value)
	}
	if state.CurrentOffset != "0_1" {
		t.Fatalf("CurrentOffset = %q", state.CurrentOffset)
	}
	if got := state.Materialized["1"]["value"]; got != "after" {
		t.Fatalf("materialized value = %v", got)
	}
}

func TestManagerRefreshFullReplicaDiffsRows(t *testing.T) {
	t.Parallel()

	manager := NewManager(storage.NewMemoryStore())
	definition := Definition{
		Relation: Relation{Schema: "public", Table: "items"},
		Replica:  "full",
	}

	initial := SnapshotResult{
		Schema: map[string]ColumnSchema{
			"id":    {Type: "uuid", PKIndex: intPtr(0)},
			"value": {Type: "text"},
		},
		Rows: []Row{
			{"id": "1", "value": "before"},
			{"id": "2", "value": "removed"},
		},
	}

	state := manager.UpsertSnapshot(definition, initial)
	updated := SnapshotResult{
		Schema: initial.Schema,
		Rows: []Row{
			{"id": "1", "value": "after"},
			{"id": "3", "value": "inserted"},
		},
	}

	state, messages, err := manager.Refresh(state.Handle, updated)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3", len(messages))
	}

	if messages[0].Headers["operation"] != "update" || messages[0].OldValue["value"] != "before" {
		t.Fatalf("first message = %+v", messages[0])
	}
	if messages[1].Headers["operation"] != "delete" || messages[1].Value["value"] != "removed" {
		t.Fatalf("second message = %+v", messages[1])
	}
	if messages[2].Headers["operation"] != "insert" || messages[2].Value["value"] != "inserted" {
		t.Fatalf("third message = %+v", messages[2])
	}
	if state.CurrentOffset != "0_3" {
		t.Fatalf("CurrentOffset = %q", state.CurrentOffset)
	}
}

func TestManagerInvalidateByRelation(t *testing.T) {
	t.Parallel()

	manager := NewManager(storage.NewMemoryStore())
	definition := Definition{
		Relation: Relation{Schema: "public", Table: "items"},
	}

	state := manager.UpsertSnapshot(definition, SnapshotResult{
		Schema: map[string]ColumnSchema{
			"id": {Type: "uuid", PKIndex: intPtr(0)},
		},
		Rows: []Row{{"id": "1"}},
	})

	invalidated := manager.InvalidateByRelation(definition.Relation)
	if len(invalidated) != 1 || invalidated[0] != state.Handle {
		t.Fatalf("invalidated = %+v", invalidated)
	}

	if _, ok := manager.LookupByHandle(state.Handle); ok {
		t.Fatalf("shape %q still active after invalidation", state.Handle)
	}
}

func intPtr(value int) *int {
	return &value
}
