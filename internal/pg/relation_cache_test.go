package pg

import (
	"testing"

	"github.com/pbrazdil/postgres-sync-go/internal/shapes"
)

func TestShapeSchemasEqualDetectsSchemaChanges(t *testing.T) {
	t.Parallel()

	pk := 0
	base := map[string]shapes.ColumnSchema{
		"id":    {Type: "uuid", NotNull: true, PKIndex: &pk},
		"value": {Type: "text", NotNull: true},
	}
	same := map[string]shapes.ColumnSchema{
		"id":    {Type: "uuid", NotNull: true, PKIndex: &pk},
		"value": {Type: "text", NotNull: true},
	}
	changed := map[string]shapes.ColumnSchema{
		"id":      {Type: "uuid", NotNull: true, PKIndex: &pk},
		"value":   {Type: "text", NotNull: true},
		"new_col": {Type: "text", NotNull: false},
	}

	if !shapeSchemasEqual(base, same) {
		t.Fatalf("shapeSchemasEqual() = false, want true for identical schema")
	}
	if shapeSchemasEqual(base, changed) {
		t.Fatalf("shapeSchemasEqual() = true, want false for added column")
	}
}
