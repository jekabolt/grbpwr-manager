// Package marketingaggregate periodically rebuilds the per-account order
// aggregate consumed by the email-segmentation compiler.
package marketingaggregate

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

const tickTimeout = 30 * time.Second

const (
	backoffBase = 30 * time.Second
	backoffMax  = 5 * time.Minute
)

// Config configures the marketing aggregate refresh worker.
type Config struct {
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
}

// DefaultConfig refreshes the aggregate hourly.
func DefaultConfig() Config {
	return Config{WorkerInterval: time.Hour}
}

// Worker periodically rebuilds marketing_account_aggregate.
type Worker struct {
	repo    dependency.Repository
	c       *Config
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "marketingaggregate" }

// LastSuccess implements health.Reporter.
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// New constructs a marketing aggregate worker.
func New(c *Config, repo dependency.Repository) *Worker {
	if c == nil {
		defaults := DefaultConfig()
		c = &defaults
	}
	if c.WorkerInterval <= 0 {
		c.WorkerInterval = DefaultConfig().WorkerInterval
	}
	return &Worker{repo: repo, c: c}
}

// Start launches the initial backfill and periodic refresh loop.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("marketing aggregate worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() {
		w.run(w.ctx)
	})
	return nil
}

// Stop cancels the worker and waits for any in-flight refresh to finish.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("marketing aggregate worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}

func (w *Worker) run(ctx context.Context) {
	// Backfill immediately so segment previews do not wait for the first interval
	// after a deployment.
	w.runOnce(ctx)

	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

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
			slog.WarnContext(ctx, "marketing aggregate: backing off after failed tick",
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

// runOnce rebuilds the aggregate under a bounded context and reports success.
func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "marketingaggregate")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	rows, err := w.repo.Campaigns().RefreshMarketingAggregate(ctx)
	if err != nil {
		w.tracker.MarkError(err)
		slog.ErrorContext(ctx, "marketing aggregate refresh failed", slog.String("err", err.Error()))
		return false
	}
	w.tracker.MarkSuccess()
	slog.InfoContext(ctx, "marketing aggregate refreshed", slog.Int64("account_count", rows))
	return true
}
