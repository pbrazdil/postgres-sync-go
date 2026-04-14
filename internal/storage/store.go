package storage

import "context"

type Store interface {
	Kind() string
	Close(context.Context) error
}

type MemoryStore struct{}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (s *MemoryStore) Kind() string {
	return "memory"
}

func (s *MemoryStore) Close(context.Context) error {
	return nil
}
