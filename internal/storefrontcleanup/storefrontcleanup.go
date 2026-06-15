package storefrontcleanup

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

// tickTimeout bounds the DB work done in a single cleanup tick, so one stuck
// query can't block the loop forever or stall graceful shutdown.
const tickTimeout = 30 * time.Second

// Config holds configuration for the storefront cleanup worker.
type Config struct {
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
}

// DefaultConfig returns default configuration values.
func DefaultConfig() Config {
	return Config{
		WorkerInterval: 1 * time.Hour,
	}
}

// Worker deletes expired rows from storefront auth tables (JTI denylist, login challenges, refresh tokens).
type Worker struct {
	repo    dependency.Repository
	c       *Config
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "storefrontcleanup" }

// LastSuccess implements health.Reporter (zero time until the first clean tick).
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// New creates a new storefront cleanup worker.
func New(c *Config, repo dependency.Repository) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.WorkerInterval == 0 {
		c.WorkerInterval = 24 * time.Hour
	}
	return &Worker{
		repo: repo,
		c:    c,
	}
}

// Start starts the worker.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("storefront cleanup worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() {
		w.worker(w.ctx)
	})
	return nil
}

// Stop signals the worker to stop and waits for its goroutine to exit, so the
// caller can safely close shared resources (e.g. the DB) afterwards.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("storefront cleanup worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}

func (w *Worker) worker(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.runCleanup(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) runCleanup(ctx context.Context) {
	defer saferun.Recover(ctx, "storefrontcleanup")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	sa := w.repo.StorefrontAccount()

	ok := true

	jtiN, err := sa.CleanupExpiredJtiDenylist(ctx)
	if err != nil {
		ok = false
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "storefront cleanup: jti denylist failed", slog.String("err", err.Error()))
	} else if jtiN > 0 {
		slog.Default().InfoContext(ctx, "storefront cleanup: expired jti denylist removed", slog.Int64("count", jtiN))
	}

	challengeN, err := sa.CleanupExpiredLoginChallenges(ctx)
	if err != nil {
		ok = false
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "storefront cleanup: login challenges failed", slog.String("err", err.Error()))
	} else if challengeN > 0 {
		slog.Default().InfoContext(ctx, "storefront cleanup: expired login challenges removed", slog.Int64("count", challengeN))
	}

	refreshN, err := sa.CleanupExpiredRefreshTokens(ctx)
	if err != nil {
		ok = false
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "storefront cleanup: refresh tokens failed", slog.String("err", err.Error()))
	} else if refreshN > 0 {
		slog.Default().InfoContext(ctx, "storefront cleanup: expired refresh tokens removed", slog.Int64("count", refreshN))
	}

	// Record success only when every sub-cleanup completed without error.
	if ok {
		w.tracker.MarkSuccess()
	}
}
