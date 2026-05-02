package pgsync

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"

	internalconfig "github.com/pbrazdil/postgres-sync-go/internal/config"
	"github.com/pbrazdil/postgres-sync-go/internal/httpapi"
	"github.com/pbrazdil/postgres-sync-go/internal/pg"
	"github.com/pbrazdil/postgres-sync-go/internal/protocol"
	"github.com/pbrazdil/postgres-sync-go/internal/shapes"
	"github.com/pbrazdil/postgres-sync-go/internal/storage"
	"github.com/pbrazdil/postgres-sync-go/internal/telemetry"
)

type Status = pg.ServiceStatus

type Engine struct {
	cfg       internalconfig.Config
	store     storage.Store
	runtime   *pg.Runtime
	shapes    *shapes.Manager
	telemetry *telemetry.Provider
	protocol  *protocol.Service
	router    *httpapi.Router

	startOnce sync.Once
	closeOnce sync.Once
	startErr  error
	closeErr  error
}

func New(cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	var storeImpl storage.Store
	switch cfg.Storage.Mode {
	case "", internalconfig.StorageModeMemory:
		storeImpl = storage.NewMemoryStore()
	case internalconfig.StorageModeDisk:
		dir := cfg.Storage.Dir
		if dir == "" {
			dir = filepath.Join(".", ".postgres-sync-go")
		}

		diskStore, err := storage.NewDiskStore(dir)
		if err != nil {
			return nil, err
		}
		storeImpl = diskStore
	default:
		return nil, errors.New("unsupported storage mode")
	}
	if _, err := storeImpl.Compact(context.Background()); err != nil {
		_ = storeImpl.Close(context.Background())
		return nil, err
	}

	shapeManager, err := shapes.NewManager(storeImpl)
	if err != nil {
		return nil, err
	}
	telemetryProvider := telemetry.NewProvider(Version, cfg.Telemetry, cfg.MaxConcurrentRequests)
	runtime := pg.NewRuntime(cfg, shapeManager, storeImpl)
	telemetryProvider.AttachStore(storeImpl)
	telemetryProvider.AttachRuntimeMetrics(func() telemetry.RuntimeMetrics {
		snapshot := runtime.MetricsSnapshot()
		return telemetry.RuntimeMetrics{
			Status:               string(snapshot.Status),
			ReplicationConnected: snapshot.ReplicationConnected,
			ReplicationSlot:      snapshot.ReplicationSlot,
			LastConfirmedLSN:     snapshot.LastConfirmedLSN,
			LastConfirmedBytes:   snapshot.LastConfirmedBytes,
			LastReceivedLSN:      snapshot.LastReceivedLSN,
			LastReceivedBytes:    snapshot.LastReceivedBytes,
			ServerWALEnd:         snapshot.ServerWALEnd,
			ServerWALEndBytes:    snapshot.ServerWALEndBytes,
			WALRetainedBytes:     snapshot.WALRetainedBytes,
			Reconnects:           snapshot.Reconnects,
			ReplicationErrors:    snapshot.ReplicationErrors,
			ChangeBatches:        snapshot.ChangeBatches,
			ChangeRecords:        snapshot.ChangeRecords,
			Invalidations:        snapshot.Invalidations,
			LastReplicationError: snapshot.LastReplicationError,
		}
	})
	protocolService := protocol.NewService(cfg, shapeManager, runtime, telemetryProvider)
	router := httpapi.NewRouter("postgres-sync-go/"+Version, protocolService, telemetryProvider, runtime)

	return &Engine{
		cfg:       cfg,
		store:     storeImpl,
		runtime:   runtime,
		shapes:    shapeManager,
		telemetry: telemetryProvider,
		protocol:  protocolService,
		router:    router,
	}, nil
}

func (e *Engine) Start(ctx context.Context) error {
	e.startOnce.Do(func() {
		e.startErr = e.runtime.Start(ctx)
	})

	return e.startErr
}

func (e *Engine) Handler() http.Handler {
	return e.router
}

func (e *Engine) Status() Status {
	return e.runtime.Status()
}

func (e *Engine) Close(ctx context.Context) error {
	e.closeOnce.Do(func() {
		if err := e.runtime.Close(ctx); err != nil {
			e.closeErr = err
			return
		}

		if err := e.telemetry.Close(ctx); err != nil {
			e.closeErr = err
			return
		}

		e.closeErr = e.store.Close(ctx)
	})

	return e.closeErr
}
