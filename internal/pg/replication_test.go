package pg

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestApplyWithPeriodicKeepaliveDuringBlockedApply(t *testing.T) {
	t.Parallel()

	applyDone := make(chan struct{})
	applyStarted := make(chan struct{})
	var keepalives atomic.Int32

	errCh := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go func() {
		errCh <- applyWithPeriodicKeepalive(
			ctx,
			10*time.Millisecond,
			func() error {
				close(applyStarted)
				<-applyDone
				return nil
			},
			func() error {
				keepalives.Add(1)
				return nil
			},
		)
	}()

	select {
	case <-applyStarted:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("apply function did not start")
	}

	deadline := time.After(200 * time.Millisecond)
	for keepalives.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("keepalives = %d, want at least 2 while apply is blocked", keepalives.Load())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	close(applyDone)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("applyWithPeriodicKeepalive() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for applyWithPeriodicKeepalive")
	}
}
