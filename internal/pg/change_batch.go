package pg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pglogrepl"

	"github.com/petrbrazdil/pulsesync/internal/shapes"
)

type ChangeOperation string

const (
	ChangeInsert ChangeOperation = "insert"
	ChangeUpdate ChangeOperation = "update"
	ChangeDelete ChangeOperation = "delete"
)

type ChangeRecord struct {
	Relation   shapes.Relation
	Operation  ChangeOperation
	PrimaryKey shapes.Row
	OldTuple   shapes.Row
	NewTuple   shapes.Row
	CommitLSN  pglogrepl.LSN
	XID        uint32
}

type ChangeBatch struct {
	XID         uint32
	CommitLSN   pglogrepl.LSN
	byRelation  map[uint32]map[string]ChangeRecord
	relationIDs []uint32
}

func (b *ChangeBatch) Reset(xid uint32) {
	b.XID = xid
	b.CommitLSN = 0
	b.byRelation = map[uint32]map[string]ChangeRecord{}
	b.relationIDs = b.relationIDs[:0]
}

func (b *ChangeBatch) Add(relationID uint32, change ChangeRecord) {
	if b.byRelation == nil {
		b.byRelation = map[uint32]map[string]ChangeRecord{}
	}
	if b.byRelation[relationID] == nil {
		b.byRelation[relationID] = map[string]ChangeRecord{}
		b.relationIDs = append(b.relationIDs, relationID)
	}

	key := primaryKeySignature(change.PrimaryKey)
	if key == "" {
		return
	}
	b.byRelation[relationID][key] = change
}

func (b *ChangeBatch) RelationIDs() []uint32 {
	if len(b.relationIDs) == 0 {
		return nil
	}

	ids := append([]uint32(nil), b.relationIDs...)
	sort.Slice(ids, func(i, j int) bool {
		return ids[i] < ids[j]
	})
	return ids
}

func (b *ChangeBatch) ChangesForRelation(relationID uint32) []ChangeRecord {
	changesByKey := b.byRelation[relationID]
	if len(changesByKey) == 0 {
		return nil
	}

	keys := make([]string, 0, len(changesByKey))
	for key := range changesByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	changes := make([]ChangeRecord, 0, len(keys))
	for _, key := range keys {
		change := changesByKey[key]
		change.CommitLSN = b.CommitLSN
		change.XID = b.XID
		changes = append(changes, change)
	}
	return changes
}

func primaryKeySignature(row shapes.Row) string {
	if len(row) == 0 {
		return ""
	}

	keys := make([]string, 0, len(row))
	for key := range row {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, row[key]))
	}
	return strings.Join(parts, "|")
}
