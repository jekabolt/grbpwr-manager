package circuitbreaker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCircuitBreaker_ClosedToOpen(t *testing.T) {
	cfg := Config{
		MaxFailures:        3,
		OpenTimeout:        100 * time.Millisecond,
		HalfOpenMaxRetries: 2,
	}

	stateChanges := []State{}
	cb := New("test", cfg, func(from, to State, reason string) {
		stateChanges = append(stateChanges, to)
	})

	if cb.State() != StateClosed {
		t.Fatalf("expected initial state closed, got %v", cb.State())
	}

	testErr := errors.New("test error")
	for i := 0; i < 3; i++ {
		err := cb.Call(context.Background(), func(ctx context.Context) error {
			return testErr
		})
		if err != testErr {
			t.Fatalf("expected test error, got %v", err)
		}
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected state open after 3 failures, got %v", cb.State())
	}

	if len(stateChanges) != 1 || stateChanges[0] != StateOpen {
		t.Fatalf("expected one state change to open, got %v", stateChanges)
	}

	err := cb.Call(context.Background(), func(ctx context.Context) error {
		return nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
}

func TestCircuitBreaker_OpenToHalfOpen(t *testing.T) {
	cfg := Config{
		MaxFailures:        2,
		OpenTimeout:        50 * time.Millisecond,
		HalfOpenMaxRetries: 2,
	}

	cb := New("test", cfg, nil)

	testErr := errors.New("test error")
	for i := 0; i < 2; i++ {
		cb.Call(context.Background(), func(ctx context.Context) error {
			return testErr
		})
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected state open, got %v", cb.State())
	}

	time.Sleep(60 * time.Millisecond)

	err := cb.Call(context.Background(), func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected success in half-open, got %v", err)
	}

	if cb.State() != StateClosed {
		t.Fatalf("expected state closed after successful half-open call, got %v", cb.State())
	}
}

func TestCircuitBreaker_HalfOpenToOpen(t *testing.T) {
	cfg := Config{
		MaxFailures:        2,
		OpenTimeout:        50 * time.Millisecond,
		HalfOpenMaxRetries: 2,
	}

	cb := New("test", cfg, nil)

	testErr := errors.New("test error")
	for i := 0; i < 2; i++ {
		cb.Call(context.Background(), func(ctx context.Context) error {
			return testErr
		})
	}

	time.Sleep(60 * time.Millisecond)

	err := cb.Call(context.Background(), func(ctx context.Context) error {
		return testErr
	})
	if err != testErr {
		t.Fatalf("expected test error, got %v", err)
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected state open after failed half-open call, got %v", cb.State())
	}
}

func TestCircuitBreaker_Reset(t *testing.T) {
	cfg := Config{
		MaxFailures:        2,
		OpenTimeout:        1 * time.Minute,
		HalfOpenMaxRetries: 2,
	}

	cb := New("test", cfg, nil)

	testErr := errors.New("test error")
	for i := 0; i < 2; i++ {
		cb.Call(context.Background(), func(ctx context.Context) error {
			return testErr
		})
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected state open, got %v", cb.State())
	}

	cb.Reset()

	if cb.State() != StateClosed {
		t.Fatalf("expected state closed after reset, got %v", cb.State())
	}

	if cb.Failures() != 0 {
		t.Fatalf("expected 0 failures after reset, got %d", cb.Failures())
	}

	err := cb.Call(context.Background(), func(ctx context.Context) error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after reset, got %v", err)
	}
}
