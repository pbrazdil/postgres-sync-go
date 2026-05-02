package storage

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

type PersistedShape struct {
	Handle        string
	Hash          string
	Definition    json.RawMessage
	Schema        json.RawMessage
	Snapshot      json.RawMessage
	Materialized  json.RawMessage
	CurrentOffset string
	LastAccess    time.Time
	Generation    uint64
	Deleted       bool
	Changes       []json.RawMessage
}

type RuntimeCheckpoint struct {
	SlotName         string
	LastConfirmedLSN string
	SystemID         string
	Timeline         int32
	DBName           string
}

type StoreStats struct {
	Kind              string
	ShapeCount        int
	ActiveShapeCount  int
	DeletedShapeCount int
	ChunkCount        int
	ChangeCount       int
	MetadataBytes     int64
	ChunkBytes        int64
	TotalBytes        int64
	HasCheckpoint     bool
	Checkpoint        RuntimeCheckpoint
}

type CompactionResult struct {
	RemovedChunks int
	RemovedBytes  int64
}

type Store interface {
	Kind() string
	LoadShapes(context.Context) ([]PersistedShape, error)
	SaveShape(context.Context, PersistedShape) error
	LoadRuntimeCheckpoint(context.Context) (RuntimeCheckpoint, bool, error)
	SaveRuntimeCheckpoint(context.Context, RuntimeCheckpoint) error
	Stats(context.Context) (StoreStats, error)
	Compact(context.Context) (CompactionResult, error)
	Close(context.Context) error
}

type MemoryStore struct {
	mu         sync.RWMutex
	shapes     map[string]PersistedShape
	checkpoint *RuntimeCheckpoint
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		shapes: map[string]PersistedShape{},
	}
}

func (s *MemoryStore) Kind() string {
	return "memory"
}

func (s *MemoryStore) LoadShapes(context.Context) ([]PersistedShape, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	handles := make([]string, 0, len(s.shapes))
	for handle := range s.shapes {
		handles = append(handles, handle)
	}
	sort.Strings(handles)

	shapes := make([]PersistedShape, 0, len(handles))
	for _, handle := range handles {
		shapes = append(shapes, clonePersistedShape(s.shapes[handle]))
	}
	return shapes, nil
}

func (s *MemoryStore) SaveShape(_ context.Context, shape PersistedShape) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.shapes[shape.Handle] = clonePersistedShape(shape)
	return nil
}

func (s *MemoryStore) LoadRuntimeCheckpoint(context.Context) (RuntimeCheckpoint, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.checkpoint == nil {
		return RuntimeCheckpoint{}, false, nil
	}

	checkpoint := *s.checkpoint
	return checkpoint, true, nil
}

func (s *MemoryStore) SaveRuntimeCheckpoint(_ context.Context, checkpoint RuntimeCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cloned := checkpoint
	s.checkpoint = &cloned
	return nil
}

func (s *MemoryStore) Stats(context.Context) (StoreStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := StoreStats{
		Kind:       s.Kind(),
		ShapeCount: len(s.shapes),
	}
	for _, shape := range s.shapes {
		if shape.Deleted {
			stats.DeletedShapeCount++
		} else {
			stats.ActiveShapeCount++
		}
		stats.ChangeCount += len(shape.Changes)
	}
	if s.checkpoint != nil {
		stats.HasCheckpoint = true
		stats.Checkpoint = *s.checkpoint
	}
	return stats, nil
}

func (s *MemoryStore) Compact(context.Context) (CompactionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := CompactionResult{}
	for handle, shape := range s.shapes {
		if !shape.Deleted || len(shape.Changes) == 0 {
			continue
		}
		result.RemovedChunks++
		shape.Changes = nil
		s.shapes[handle] = shape
	}
	return result, nil
}

func (s *MemoryStore) Close(context.Context) error {
	return nil
}

func clonePersistedShape(shape PersistedShape) PersistedShape {
	cloned := PersistedShape{
		Handle:        shape.Handle,
		Hash:          shape.Hash,
		Definition:    cloneRawMessage(shape.Definition),
		Schema:        cloneRawMessage(shape.Schema),
		Snapshot:      cloneRawMessage(shape.Snapshot),
		Materialized:  cloneRawMessage(shape.Materialized),
		CurrentOffset: shape.CurrentOffset,
		LastAccess:    shape.LastAccess,
		Generation:    shape.Generation,
		Deleted:       shape.Deleted,
		Changes:       make([]json.RawMessage, 0, len(shape.Changes)),
	}

	for _, change := range shape.Changes {
		cloned.Changes = append(cloned.Changes, cloneRawMessage(change))
	}
	return cloned
}

func cloneRawMessage(message json.RawMessage) json.RawMessage {
	if len(message) == 0 {
		return nil
	}

	cloned := make(json.RawMessage, len(message))
	copy(cloned, message)
	return cloned
}
