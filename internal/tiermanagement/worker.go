package tiermanagement

import (
	"context"
	"fmt"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
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
	repo   dependency.Repository
	mailer dependency.Mailer
	c      *Config
	ctx    context.Context
	stop   context.CancelFunc
}

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
	go w.run(w.ctx)
	return nil
}

// Stop signals the worker to exit.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("tier management worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	return nil
}

func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.runOnce(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	e := NewEngine(w.repo, w.mailer)
	if n, err := e.RunDailyTierReview(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "tier review failed", slog.String("err", err.Error()))
	} else if n > 0 {
		slog.Default().InfoContext(ctx, "tier review downgraded members", slog.Int("count", n))
	}
	if n, err := e.RunDowngradeReminders(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "downgrade reminders failed", slog.String("err", err.Error()))
	} else if n > 0 {
		slog.Default().InfoContext(ctx, "downgrade reminders queued", slog.Int("count", n))
	}
	if n, err := e.RunBirthdayGifts(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "birthday gifts failed", slog.String("err", err.Error()))
	} else if n > 0 {
		slog.Default().InfoContext(ctx, "birthday gifts queued", slog.Int("count", n))
	}
}
