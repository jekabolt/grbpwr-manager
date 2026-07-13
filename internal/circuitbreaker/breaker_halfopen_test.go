package circuitbreaker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestBreakerHalfOpenSingleProbe verifies the transitioning probe is counted and
// only one probe is admitted in half-open at a time.
func TestBreakerHalfOpenSingleProbe(t *testing.T) {
	cb := New("t", Config{MaxFailures: 1, OpenTimeout: 20 * time.Millisecond, HalfOpenMaxRetries: 3}, nil)
	ctx := context.Background()
	boom := func(context.Context) error { return errors.New("boom") }

	// One failure opens the circuit (MaxFailures=1).
	_ = cb.Call(ctx, boom)
	if cb.State() != StateOpen {
		t.Fatalf("expected Open after failure, got %v", cb.State())
	}
	// Within the open timeout, calls are rejected.
	if err := cb.beforeCall(); err != ErrCircuitOpen {
		t.Fatalf("expected ErrCircuitOpen within timeout, got %v", err)
	}

	time.Sleep(40 * time.Millisecond) // past OpenTimeout

	// First beforeCall transitions to half-open and admits one probe.
	if err := cb.beforeCall(); err != nil {
		t.Fatalf("first probe should be admitted, got %v", err)
	}
	if cb.State() != StateHalfOpen {
		t.Fatalf("expected HalfOpen, got %v", cb.State())
	}
	// A second probe while the first is still in flight is rejected.
	if err := cb.beforeCall(); err != ErrTooManyRequests {
		t.Fatalf("second concurrent probe should be rejected, got %v", err)
	}
}

// TestBreakerRecoversOnSuccessfulProbe verifies a successful half-open probe closes.
func TestBreakerRecoversOnSuccessfulProbe(t *testing.T) {
	cb := New("t", Config{MaxFailures: 1, OpenTimeout: 20 * time.Millisecond, HalfOpenMaxRetries: 3}, nil)
	ctx := context.Background()
	_ = cb.Call(ctx, func(context.Context) error { return errors.New("boom") })

	time.Sleep(40 * time.Millisecond)

	if err := cb.Call(ctx, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("probe call returned error: %v", err)
	}
	if cb.State() != StateClosed {
		t.Fatalf("expected Closed after successful probe, got %v", cb.State())
	}
}
