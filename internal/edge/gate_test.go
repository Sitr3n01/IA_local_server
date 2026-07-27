package edge

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGateBoundsActiveQueueAndTimeout(t *testing.T) {
	gate := newGate(1, 1, 20*time.Millisecond)
	release, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	waiting := make(chan error, 1)
	go func() {
		_, acquireErr := gate.acquire(context.Background())
		waiting <- acquireErr
	}()
	deadline := time.Now().Add(time.Second)
	for gate.snapshot().Queued != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if gate.snapshot().Queued != 1 {
		t.Fatal("request did not enter bounded queue")
	}
	if _, err := gate.acquire(context.Background()); !errors.Is(err, errQueueFull) {
		t.Fatalf("third acquire error = %v, want queue full", err)
	}
	if err := <-waiting; !errors.Is(err, errQueueTimeout) {
		t.Fatalf("queued acquire error = %v, want queue timeout", err)
	}
	snapshot := gate.snapshot()
	if snapshot.Active != 1 || snapshot.Queued != 0 || snapshot.Rejected != 1 || snapshot.TimedOut != 1 {
		t.Fatalf("unexpected gate snapshot: %+v", snapshot)
	}
}

func TestGatePropagatesQueuedCancellation(t *testing.T) {
	gate := newGate(1, 1, time.Second)
	release, err := gate.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gate.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire error = %v, want context canceled", err)
	}
	if gate.snapshot().Queued != 0 {
		t.Fatalf("canceled request remained queued: %+v", gate.snapshot())
	}
}
