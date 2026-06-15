package tiermanagement

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

// tickTimeout bounds the DB work done in a single maintenance tick, so one stuck
// query can't block the loop forever or stall graceful shutdown.
const tickTimeout = 30 * time.Second

// Backoff bounds for consecutive-failure backoff. On a failed tick an extra
// delay (base * 2^(n-1), capped at backoffMax) is waited before the next
// iteration so a persistently failing dependency (DB / mailer) isn't hammered
// every WorkerInterval. A successful tick resets the backoff. See ga4sync for
// the established pattern in this codebase.
const (
	backoffBase = 30 * time.Second
	backoffMax  = 5 * time.Minute
)

// Config configures the tier maintenance worker.
type Config struct {
	// WorkerInterval is how often the daily jobs are evaluated. The jobs are
	// idempotent within a day, so a sub-day interval is safe.
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
}

// DefaultConfig returns sane defaults (run every 6 hours).
func DefaultConfig() Config {
	return Config{WorkerInterval: 6 * time.Hour}
}

// Worker runs the periodic tier review, downgrade reminders, and birthday gifts.
type Worker struct {
	repo    dependency.Repository
	mailer  dependency.Mailer
	c       *Config
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "tiermanagement" }

// LastSuccess implements health.Reporter (zero time until the first clean tick).
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// New constructs a tier maintenance worker.
func New(c *Config, repo dependency.Repository, mailer dependency.Mailer) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.WorkerInterval <= 0 {
		c.WorkerInterval = DefaultConfig().WorkerInterval
	}
	return &Worker{repo: repo, mailer: mailer, c: c}
}

// Start launches the worker goroutine.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("tier management worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() {
		w.run(w.ctx)
	})
	return nil
}

// Stop signals the worker to exit and waits for its goroutine to return, so the
// caller can safely close shared resources (e.g. the DB) afterwards.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("tier management worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}

func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

	// consecutiveFailures drives the extra backoff delay applied after a failed
	// tick. Reset to 0 on the first successful tick.
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
			slog.Default().WarnContext(ctx, "tiermanagement: backing off after failed tick",
				slog.Int("consecutive_failures", consecutiveFailures),
				slog.Duration("delay", delay),
			)
			// Wait the extra backoff on top of the ticker interval, but stay
			// responsive to shutdown — never time.Sleep blindly.
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

// backoffDelay returns the extra inter-iteration delay for the given number of
// consecutive failures: base * 2^(n-1), capped at backoffMax.
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

// runOnce performs a single maintenance tick and reports whether every daily job
// completed without error.
func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "tiermanagement")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	ok := true
	e := NewEngine(w.repo, w.mailer)
	if n, err := e.RunDailyTierReview(ctx); err != nil {
		ok = false
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "tier review failed", slog.String("err", err.Error()))
	} else if n > 0 {
		slog.Default().InfoContext(ctx, "tier review downgraded members", slog.Int("count", n))
	}
	if n, err := e.RunDowngradeReminders(ctx); err != nil {
		ok = false
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "downgrade reminders failed", slog.String("err", err.Error()))
	} else if n > 0 {
		slog.Default().InfoContext(ctx, "downgrade reminders queued", slog.Int("count", n))
	}
	if n, err := e.RunBirthdayGifts(ctx); err != nil {
		ok = false
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "birthday gifts failed", slog.String("err", err.Error()))
	} else if n > 0 {
		slog.Default().InfoContext(ctx, "birthday gifts queued", slog.Int("count", n))
	}

	// Record success only when every daily job completed without error.
	if ok {
		w.tracker.MarkSuccess()
	}
	return ok
}
