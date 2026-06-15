// Package health provides lightweight, dependency-free observability primitives
// for background workers and the runtime. Workers embed a Tracker to record the
// timestamp of their last successful tick; the app aggregates them into a
// Registry that the HTTP status endpoint renders as JSON.
//
// No external dependencies (prometheus/otel) are used: this is intentionally a
// standard-library-only building block.
package health

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/circuitbreaker"
)

// Reporter is implemented by anything that records a last-successful-run
// timestamp (typically a background worker). Name identifies the worker in the
// status output; LastSuccess is the zero value until the first successful tick.
type Reporter interface {
	Name() string
	LastSuccess() time.Time
}

// Tracker is a thread-safe, race-free record of a worker's last successful tick
// and last error. Embed it in a worker struct (as a value) and call
// MarkSuccess() at the END of a successful tick (never on error, so staleness
// reflects real failures). The zero value is ready to use and reports
// LastSuccess() as the zero time ("never ran yet").
type Tracker struct {
	// lastSuccessNano holds the unix-nano timestamp of the last successful tick,
	// 0 when none has happened. Accessed atomically so it is safe under -race
	// without holding errMu.
	lastSuccessNano atomic.Int64

	errMu   sync.Mutex
	lastErr string
}

// MarkSuccess records the current time as the last successful tick.
func (t *Tracker) MarkSuccess() {
	t.lastSuccessNano.Store(time.Now().UnixNano())
	t.errMu.Lock()
	t.lastErr = ""
	t.errMu.Unlock()
}

// MarkError records a human-readable last-error string. It does NOT touch the
// last-success timestamp, so staleness keeps reflecting the last real success.
func (t *Tracker) MarkError(err error) {
	if err == nil {
		return
	}
	t.errMu.Lock()
	t.lastErr = err.Error()
	t.errMu.Unlock()
}

// LastSuccess returns the time of the last successful tick, or the zero time if
// there has not been one yet.
func (t *Tracker) LastSuccess() time.Time {
	n := t.lastSuccessNano.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// LastError returns the last recorded error string (empty if none / cleared on
// the next success).
func (t *Tracker) LastError() string {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.lastErr
}

// DBStatsProvider exposes connection-pool statistics (typically *sql.DB.Stats()).
// Kept as a one-method interface so the store can satisfy it without the http
// package importing the store.
type DBStatsProvider interface {
	Stats() DBStats
}

// DBStats is a stdlib-only mirror of the subset of sql.DBStats reported by the
// status endpoint. The store maps sql.DBStats into this so the health/http
// packages don't need to import database/sql or depend on the store.
type DBStats struct {
	OpenConnections    int
	InUse              int
	Idle               int
	WaitCount          int64
	WaitDuration       time.Duration
	MaxOpenConnections int
}

// BreakerReporter exposes a named circuit-breaker's current state cheaply.
type BreakerReporter struct {
	BreakerName string
	StateFunc   func() circuitbreaker.State
}

// Registry aggregates the runtime/DB/worker/breaker state surfaced by the HTTP
// status endpoint. It is constructed once in app wiring and is read-only
// afterwards; the workers it references update their own Trackers concurrently.
type Registry struct {
	Workers  []Reporter
	DB       DBStatsProvider
	Breakers []BreakerReporter
}
