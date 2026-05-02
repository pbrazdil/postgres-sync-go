package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pbrazdil/postgres-sync-go/pkg/pgsync"
)

func main() {
	cfg, err := pgsync.LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	engine, err := pgsync.New(cfg)
	if err != nil {
		log.Fatalf("create engine: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := engine.Start(ctx); err != nil {
		log.Fatalf("start engine: %v", err)
	}

	server := &http.Server{
		Addr:              cfg.ListenAddress(),
		Handler:           engine.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErrCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve http: %v", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown http server: %v", err)
	}

	if err := engine.Close(shutdownCtx); err != nil {
		log.Printf("shutdown engine: %v", err)
	}

	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.Canceled) {
		os.Exit(1)
	}
}
