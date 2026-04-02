package pulsesync

import (
	"context"
	"errors"
	"net/http"
	"sync"

	internalconfig "github.com/petrbrazdil/pulsesync/internal/config"
	"github.com/petrbrazdil/pulsesync/internal/httpapi"
	"github.com/petrbrazdil/pulsesync/internal/pg"
	"github.com/petrbrazdil/pulsesync/internal/protocol"
	"github.com/petrbrazdil/pulsesync/internal/shapes"
	"github.com/petrbrazdil/pulsesync/internal/storage"
	"github.com/petrbrazdil/pulsesync/internal/telemetry"
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
		return nil, errors.New("disk storage mode is not implemented yet")
	default:
		return nil, errors.New("unsupported storage mode")
	}

	shapeManager := shapes.NewManager(storeImpl)
	runtime := pg.NewRuntime(cfg, shapeManager)
	telemetryProvider := telemetry.NewProvider(Version, cfg.Telemetry)
	protocolService := protocol.NewService(cfg, shapeManager, runtime)
	router := httpapi.NewRouter("PulseSync/"+Version, protocolService, telemetryProvider, runtime)

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
