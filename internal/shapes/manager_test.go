package shapes

import (
	"testing"

	"github.com/pbrazdil/postgres-sync-go/internal/storage"
)

func TestManagerChangesOnlyRefreshProducesUpdate(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(storage.NewMemoryStore())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
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

	state, err := manager.UpsertSnapshot(definition, initial)
	if err != nil {
		t.Fatalf("UpsertSnapshot() error = %v", err)
	}
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

	manager, err := NewManager(storage.NewMemoryStore())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
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

	state, err := manager.UpsertSnapshot(definition, initial)
	if err != nil {
		t.Fatalf("UpsertSnapshot() error = %v", err)
	}
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

func TestManagerDependentRefreshProducesMoveEvents(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(storage.NewMemoryStore())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	definition := Definition{
		Relation: Relation{Schema: "public", Table: "child"},
		Where:    "parent_id IN (SELECT id FROM parent WHERE value = 1)",
	}

	initial := SnapshotResult{
		Schema: map[string]ColumnSchema{
			"id":        {Type: "int4", PKIndex: intPtr(0)},
			"parent_id": {Type: "int4"},
			"value":     {Type: "text"},
		},
		Rows: []Row{
			{"id": "1", "parent_id": "1", "value": "before"},
		},
	}

	state, err := manager.UpsertSnapshot(definition, initial)
	if err != nil {
		t.Fatalf("UpsertSnapshot() error = %v", err)
	}
	if tags, ok := state.Snapshot[0].Headers["tags"].([]string); !ok || len(tags) != 1 {
		t.Fatalf("snapshot tags = %+v", state.Snapshot[0].Headers["tags"])
	}

	updated := SnapshotResult{
		Schema: initial.Schema,
		Rows: []Row{
			{"id": "2", "parent_id": "2", "value": "after"},
		},
	}

	state, messages, err := manager.RefreshWithMetadata(state.Handle, updated, ChangeMetadata{
		KeyRelation:      Relation{Schema: "public", Table: "parent"},
		CommitLSN:        42,
		TransactionID:    7,
		DependentRefresh: true,
	})
	if err != nil {
		t.Fatalf("RefreshWithMetadata() error = %v", err)
	}

	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3: %+v", len(messages), messages)
	}
	if messages[0].Headers["event"] != "move-out" {
		t.Fatalf("first message = %+v", messages[0])
	}
	if messages[1].Headers["operation"] != "insert" || messages[1].Headers["is_move_in"] != true {
		t.Fatalf("second message = %+v", messages[1])
	}
	if tags, ok := messages[1].Headers["tags"].([]string); !ok || len(tags) != 1 {
		t.Fatalf("move-in tags = %+v", messages[1].Headers["tags"])
	}
	if messages[2].Headers["control"] != "snapshot-end" {
		t.Fatalf("third message = %+v", messages[2])
	}
	if _, ok := state.Materialized["1"]; ok {
		t.Fatalf("old row still materialized: %+v", state.Materialized)
	}
	if got := state.Materialized["2"]["value"]; got != "after" {
		t.Fatalf("new materialized value = %v", got)
	}
}

func TestManagerInvalidateByRelation(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(storage.NewMemoryStore())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	definition := Definition{
		Relation: Relation{Schema: "public", Table: "items"},
	}

	state, err := manager.UpsertSnapshot(definition, SnapshotResult{
		Schema: map[string]ColumnSchema{
			"id": {Type: "uuid", PKIndex: intPtr(0)},
		},
		Rows: []Row{{"id": "1"}},
	})
	if err != nil {
		t.Fatalf("UpsertSnapshot() error = %v", err)
	}

	invalidated, err := manager.InvalidateByRelation(definition.Relation)
	if err != nil {
		t.Fatalf("InvalidateByRelation() error = %v", err)
	}
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
