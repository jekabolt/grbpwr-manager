// Package opexmaterialize runs a periodic job that books recurring OPEX templates
// (salaries, subscriptions, rent — opex_recurring) into concrete monthly opex_line
// rows, so the dashboard operating result reflects fixed costs without the operator
// re-entering them every month. Materialisation is INSERT-only and idempotent
// (see metrics.Store.MaterializeOpexRecurring): a month already booked is never
// rewritten, so re-running is safe and editing a template only affects future months.
package opexmaterialize

import (
	"context"
	"fmt"
	"sync"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// tickTimeout bounds the DB work done in a single tick, so one stuck query can't
// block the loop forever or stall graceful shutdown.
const tickTimeout = 30 * time.Second

// Backoff bounds for consecutive-failure backoff (base * 2^(n-1), capped at
// backoffMax); a successful tick resets it. Mirrors tiermanagement/ordercleanup.
const (
	backoffBase = 30 * time.Second
	backoffMax  = 5 * time.Minute
)

// Config configures the OPEX materialisation worker.
type Config struct {
	// WorkerInterval is how often templates are materialised. Materialisation is
	// idempotent, so a sub-day interval is safe; the natural cadence is daily.
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
}

// DefaultConfig returns sane defaults (run once a day).
func DefaultConfig() Config {
	return Config{WorkerInterval: 24 * time.Hour}
}

// Worker periodically materialises recurring OPEX templates into monthly lines.
type Worker struct {
	repo    dependency.Repository
	c       *Config
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "opexmaterialize" }

// LastSuccess implements health.Reporter (zero time until the first clean tick).
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// New constructs an OPEX materialisation worker.
func New(c *Config, repo dependency.Repository) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.WorkerInterval <= 0 {
		c.WorkerInterval = DefaultConfig().WorkerInterval
	}
	return &Worker{repo: repo, c: c}
}

// Start launches the worker goroutine.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("opex materialize worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() {
		w.run(w.ctx)
	})
	return nil
}

// Stop signals the worker to exit and waits for its goroutine to return.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("opex materialize worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}

func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

	// Materialise once at startup so a fresh boot doesn't wait a full interval for
	// the current month's fixed costs to appear.
	w.runOnce(ctx)

	var consecutiveFailures int
	for {
		select {
		case <-ticker.C:
			if w.runOnce(ctx) {
				consecutiveFailures = 0
				continue
			}
			consecutiveFailures++
			delay := backoffDelay(consecutiveFailures)
			slog.Default().WarnContext(ctx, "opexmaterialize: backing off after failed tick",
				slog.Int("consecutive_failures", consecutiveFailures),
				slog.Duration("delay", delay),
			)
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// backoffDelay returns base * 2^(n-1), capped at backoffMax.
func backoffDelay(consecutiveFailures int) time.Duration {
	delay := backoffBase
	for i := 1; i < consecutiveFailures; i++ {
		delay *= 2
		if delay >= backoffMax {
			return backoffMax
		}
	}
	return delay
}

// runOnce materialises all active recurring templates up to the current month, folding each month at
// its own effective FX rate. Returns whether the tick succeeded. Materialisation loads the FX
// history itself and fails the tick if it can't (rather than booking uncosted lines that insert-only
// storage would freeze in place) — a transient outage just retries on the next tick.
func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "opexmaterialize")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	n, err := w.repo.Metrics().MaterializeOpexRecurring(ctx, time.Now())
	if err != nil {
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "opexmaterialize: materialize failed", slog.String("err", err.Error()))
		return false
	}
	if n > 0 {
		slog.Default().InfoContext(ctx, "opexmaterialize: booked recurring OPEX lines", slog.Int("count", n))
	}
	w.tracker.MarkSuccess()
	return true
}
