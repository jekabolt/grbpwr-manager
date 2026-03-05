package circuitbreaker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

var (
	ErrCircuitOpen     = errors.New("circuit breaker is open")
	ErrTooManyRequests = errors.New("circuit breaker: too many requests in half-open state")
)

// State represents the circuit breaker state.
type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Config holds circuit breaker configuration.
type Config struct {
	MaxFailures        int           `mapstructure:"max_failures"`
	OpenTimeout        time.Duration `mapstructure:"open_timeout"`
	HalfOpenMaxRetries int           `mapstructure:"half_open_max_retries"`
}

// DefaultConfig returns sensible defaults for a circuit breaker.
func DefaultConfig() Config {
	return Config{
		MaxFailures:        5,
		OpenTimeout:        5 * time.Minute,
		HalfOpenMaxRetries: 3,
	}
}

// CircuitBreaker implements the circuit breaker pattern.
type CircuitBreaker struct {
	name   string
	config Config

	mu              sync.RWMutex
	state           State
	failures        int
	lastFailureTime time.Time
	halfOpenRetries int

	onStateChange func(from, to State, reason string)
}

// New creates a new circuit breaker with the given name and config.
func New(name string, config Config, onStateChange func(from, to State, reason string)) *CircuitBreaker {
	if config.MaxFailures == 0 {
		config.MaxFailures = DefaultConfig().MaxFailures
	}
	if config.OpenTimeout == 0 {
		config.OpenTimeout = DefaultConfig().OpenTimeout
	}
	if config.HalfOpenMaxRetries == 0 {
		config.HalfOpenMaxRetries = DefaultConfig().HalfOpenMaxRetries
	}

	return &CircuitBreaker{
		name:          name,
		config:        config,
		state:         StateClosed,
		onStateChange: onStateChange,
	}
}

// Call executes the given function if the circuit is closed or half-open.
// Returns ErrCircuitOpen if the circuit is open.
func (cb *CircuitBreaker) Call(ctx context.Context, fn func(context.Context) error) error {
	if err := cb.beforeCall(); err != nil {
		return err
	}

	err := fn(ctx)
	cb.afterCall(err)
	return err
}

// beforeCall checks if the call is allowed based on the current state.
func (cb *CircuitBreaker) beforeCall() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		return nil
	case StateOpen:
		if time.Since(cb.lastFailureTime) > cb.config.OpenTimeout {
			cb.transitionTo(StateHalfOpen, "open timeout expired, attempting recovery")
			cb.halfOpenRetries = 0
			return nil
		}
		return ErrCircuitOpen
	case StateHalfOpen:
		if cb.halfOpenRetries >= cb.config.HalfOpenMaxRetries {
			return ErrTooManyRequests
		}
		cb.halfOpenRetries++
		return nil
	}
	return nil
}

// afterCall records the result of the call and updates the circuit state.
func (cb *CircuitBreaker) afterCall(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err == nil {
		cb.onSuccess()
		return
	}

	cb.onFailure(err)
}

func (cb *CircuitBreaker) onSuccess() {
	switch cb.state {
	case StateClosed:
		cb.failures = 0
	case StateHalfOpen:
		cb.transitionTo(StateClosed, "recovery successful")
		cb.failures = 0
		cb.halfOpenRetries = 0
	}
}

func (cb *CircuitBreaker) onFailure(err error) {
	cb.failures++
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case StateClosed:
		if cb.failures >= cb.config.MaxFailures {
			cb.transitionTo(StateOpen, fmt.Sprintf("max failures reached (%d): %v", cb.failures, err))
		}
	case StateHalfOpen:
		cb.transitionTo(StateOpen, fmt.Sprintf("recovery attempt failed: %v", err))
		cb.halfOpenRetries = 0
	}
}

func (cb *CircuitBreaker) transitionTo(newState State, reason string) {
	oldState := cb.state
	if oldState == newState {
		return
	}

	cb.state = newState
	slog.Default().WarnContext(context.Background(),
		"circuit breaker state transition",
		slog.String("breaker", cb.name),
		slog.String("from", oldState.String()),
		slog.String("to", newState.String()),
		slog.String("reason", reason),
		slog.Int("failures", cb.failures))

	if cb.onStateChange != nil {
		cb.onStateChange(oldState, newState, reason)
	}
}

// State returns the current state of the circuit breaker.
func (cb *CircuitBreaker) State() State {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Failures returns the current failure count.
func (cb *CircuitBreaker) Failures() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.failures
}

// Reset manually resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state != StateClosed {
		cb.transitionTo(StateClosed, "manual reset")
	}
	cb.failures = 0
	cb.halfOpenRetries = 0
}
