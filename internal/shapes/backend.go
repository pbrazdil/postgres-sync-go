package shapes

import (
	"context"
	"errors"
	"fmt"
)

const InitialOffset = "0_0"

type SnapshotMode string

const (
	SnapshotModeData         SnapshotMode = "data"
	SnapshotModeValidateOnly SnapshotMode = "validate_only"
)

type ColumnSchema struct {
	Type    string `json:"type"`
	PKIndex *int   `json:"pk_index,omitempty"`
}

type Row map[string]any

type Message struct {
	Headers  map[string]any `json:"headers"`
	Key      string         `json:"key,omitempty"`
	Value    Row            `json:"value,omitempty"`
	OldValue Row            `json:"old_value,omitempty"`
	Offset   string         `json:"offset,omitempty"`
}

type SnapshotMetadata struct {
	SnapshotMark int      `json:"snapshot_mark"`
	DatabaseLSN  string   `json:"database_lsn"`
	XMin         string   `json:"xmin"`
	XMax         string   `json:"xmax"`
	XIPList      []string `json:"xip_list"`
}

type SnapshotRequest struct {
	Definition Definition
	Mode       SnapshotMode
	Metadata   bool
}

type SnapshotResult struct {
	Schema   map[string]ColumnSchema
	Rows     []Row
	Metadata *SnapshotMetadata
}

type Backend interface {
	Snapshot(context.Context, SnapshotRequest) (SnapshotResult, error)
}

var ErrRelationNotFound = errors.New("relation not found")

type RelationNotFoundError struct {
	Relation Relation
}

func (e RelationNotFoundError) Error() string {
	return fmt.Sprintf(
		"Table %q.%q does not exist. If the table name contains capitals or special characters you must quote it.",
		e.Relation.Schema,
		e.Relation.Table,
	)
}

func (e RelationNotFoundError) Unwrap() error {
	return ErrRelationNotFound
}
