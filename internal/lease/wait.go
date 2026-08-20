package lease

import (
	"context"
	"errors"
	"time"

	"task100-leasetoken/internal/model"
)

// AcquireWait polls Acquire until it succeeds or the deadline elapses. It is a
// cooperative polling loop (the storage layer has no blocking watch), bounded
// by timeoutSeconds and pacing with pollIntervalSeconds. Every failed attempt
// that is not ErrConflict aborts immediately; only ErrConflict (resource held)
// is retried.
//
// pollInterval must be >= 1 second to avoid hot-spinning; timeoutSeconds must
// be >= pollInterval. Defaults are applied when zero.
func (m *Manager) AcquireWait(req model.AcquireWaitRequest) (model.AcquireResponse, error) {
	return m.AcquireWaitContext(context.Background(), req)
}

// AcquireWaitContext is the cancellable form of AcquireWait. A caller that
// abandons an HTTP request must be able to stop waiting without leaving a
// sleeping poll behind.
func (m *Manager) AcquireWaitContext(ctx context.Context, req model.AcquireWaitRequest) (model.AcquireResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAcquire(req.Resource, req.Holder, req.TTLSeconds); err != nil {
		return model.AcquireResponse{}, err
	}
	timeout := req.TimeoutSecs
	if timeout <= 0 {
		timeout = 30
	}
	interval := req.PollInterval
	if interval <= 0 {
		interval = 1
	}
	if interval > timeout {
		interval = timeout
	}

	deadline := m.now() + timeout
	for {
		if err := ctx.Err(); err != nil {
			return model.AcquireResponse{}, err
		}
		resp, err := m.Acquire(req.Resource, req.Holder, req.TTLSeconds)
		if err == nil {
			return resp, nil
		}
		if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrResourceLocked) {
			return resp, err
		}
		if m.now() >= deadline {
			return resp, ErrTimeout
		}
		// Poll on the wall clock. The fake clock in tests advances Now() in
		// steps, so the deadline check remains deterministic without real-time
		// sleeps.
		time.Sleep(time.Duration(interval) * time.Second)
	}
}
