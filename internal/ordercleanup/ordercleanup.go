package ordercleanup

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

// Config holds configuration for the order cleanup worker.
type Config struct {
	WorkerInterval   time.Duration `mapstructure:"worker_interval"`
	PlacedThreshold  time.Duration `mapstructure:"placed_threshold"` // e.g. 24h - cancel orders in Placed older than this
}

// DefaultConfig returns default configuration values.
func DefaultConfig() Config {
	return Config{
		WorkerInterval:  15 * time.Minute,
		PlacedThreshold: 24 * time.Hour,
	}
}

// Worker cancels orders stuck in Placed status (never reached InsertFiatInvoice)
// and AwaitingPayment orders past their expired_at (safety net when monitors fail).
type Worker struct {
	repo dependency.Repository
	c    *Config
	ctx  context.Context
	stop context.CancelFunc
}

// New creates a new order cleanup worker.
func New(c *Config, repo dependency.Repository) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.PlacedThreshold == 0 {
		c.PlacedThreshold = 24 * time.Hour
	}
	if c.WorkerInterval == 0 {
		c.WorkerInterval = 15 * time.Minute
	}
	return &Worker{
		repo: repo,
		c:    c,
	}
}

// Start starts the worker.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("order cleanup worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	go w.worker(w.ctx)
	return nil
}

// Stop stops the worker gracefully.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("order cleanup worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	return nil
}
