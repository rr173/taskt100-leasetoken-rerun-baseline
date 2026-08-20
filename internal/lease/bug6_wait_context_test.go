package lease

import (
	"context"
	"errors"
	"testing"
	"time"

	"task100-leasetoken/internal/model"
)

func TestAcquireWaitContextStopsSleepingWhenCanceled(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.Acquire("wait-context", "owner", 30); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	_, err := m.AcquireWaitContext(ctx, model.AcquireWaitRequest{Resource: "wait-context", Holder: "waiter", TTLSeconds: 10, TimeoutSecs: 10, PollInterval: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cancellation did not interrupt polling sleep: %s", elapsed)
	}
}
