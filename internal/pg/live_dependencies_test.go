package pg

import (
	"reflect"
	"testing"

	"github.com/pbrazdil/postgres-sync-go/internal/shapes"
)

func TestDefinitionSupportsTargetedRefreshForSimpleWhere(t *testing.T) {
	t.Parallel()

	definition := shapes.Definition{
		Relation: shapes.Relation{Schema: "public", Table: "items"},
		Where:    "priority >= 2 AND note = 'select from join'",
	}

	if !definitionSupportsTargetedRefresh(definition) {
		t.Fatalf("definitionSupportsTargetedRefresh() = false, want true")
	}

	dependencies := liveDependenciesForDefinition(definition)
	if dependencies.Unsupported {
		t.Fatalf("dependencies.Unsupported = true, want false")
	}
}

func TestLiveDependenciesForDefinitionSubqueryTracksRelatedRelation(t *testing.T) {
	t.Parallel()

	definition := shapes.Definition{
		Relation: shapes.Relation{Schema: "public", Table: "items"},
		Where:    "id IN (SELECT item_id FROM item_flags WHERE enabled = true)",
	}

	if definitionSupportsTargetedRefresh(definition) {
		t.Fatalf("definitionSupportsTargetedRefresh() = true, want false")
	}

	dependencies := liveDependenciesForDefinition(definition)
	if !dependencies.Unsupported {
		t.Fatalf("dependencies.Unsupported = false, want true")
	}
	if dependencies.Wildcard {
		t.Fatalf("dependencies.Wildcard = true, want false")
	}

	expected := []shapes.Relation{
		{Schema: "public", Table: "item_flags"},
		{Schema: "public", Table: "items"},
	}
	if !reflect.DeepEqual(dependencies.Relations, expected) {
		t.Fatalf("dependencies.Relations = %+v, want %+v", dependencies.Relations, expected)
	}

	if !definitionRequiresInvalidationForRelation(
		definition,
		shapes.Relation{Schema: "public", Table: "item_flags_2026"},
		shapes.Relation{Schema: "public", Table: "item_flags"},
	) {
		t.Fatalf("definitionRequiresInvalidationForRelation() = false, want true for dependency root")
	}

	if definitionRequiresInvalidationForRelation(
		definition,
		shapes.Relation{Schema: "public", Table: "audit_log"},
		shapes.Relation{Schema: "public", Table: "audit_log"},
	) {
		t.Fatalf("definitionRequiresInvalidationForRelation() = true, want false for unrelated relation")
	}
}

func TestLiveDependenciesForDefinitionSupportsQuotedSchemaReferences(t *testing.T) {
	t.Parallel()

	definition := shapes.Definition{
		Relation: shapes.Relation{Schema: "public", Table: "child"},
		Where:    `parent_id IN (SELECT "Id" FROM "Custom"."Parent" WHERE "Active" = true)`,
	}

	dependencies := liveDependenciesForDefinition(definition)
	expected := []shapes.Relation{
		{Schema: "Custom", Table: "Parent"},
		{Schema: "public", Table: "child"},
	}
	if !reflect.DeepEqual(dependencies.Relations, expected) {
		t.Fatalf("dependencies.Relations = %+v, want %+v", dependencies.Relations, expected)
	}
}

func TestLiveDependenciesForDefinitionFallsBackToWildcard(t *testing.T) {
	t.Parallel()

	definition := shapes.Definition{
		Relation: shapes.Relation{Schema: "public", Table: "items"},
		Where:    "EXISTS (SELECT 1)",
	}

	dependencies := liveDependenciesForDefinition(definition)
	if !dependencies.Unsupported {
		t.Fatalf("dependencies.Unsupported = false, want true")
	}
	if !dependencies.Wildcard {
		t.Fatalf("dependencies.Wildcard = false, want true")
	}
	if !definitionRequiresInvalidationForRelation(
		definition,
		shapes.Relation{Schema: "public", Table: "anything"},
		shapes.Relation{Schema: "public", Table: "anything"},
	) {
		t.Fatalf("definitionRequiresInvalidationForRelation() = false, want true for wildcard dependency")
	}
}
