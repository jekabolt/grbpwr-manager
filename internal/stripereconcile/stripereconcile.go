package stripereconcile

import (
	"context"
	"fmt"
	"time"
)

// PreOrderPICleaner cleans up orphaned pre-order PaymentIntents from Stripe.
type PreOrderPICleaner interface {
	CleanupOrphanedPreOrderPaymentIntents(ctx context.Context, olderThan time.Time) error
}

// Config holds configuration for the Stripe pre-order PI reconciliation worker.
type Config struct {
	WorkerInterval   time.Duration `mapstructure:"worker_interval"`
	PreOrderThreshold time.Duration `mapstructure:"pre_order_threshold"` // e.g. 24h - cancel pre_order PIs older than this
}

// DefaultConfig returns default configuration values.
func DefaultConfig() Config {
	return Config{
		WorkerInterval:    15 * time.Minute,
		PreOrderThreshold: 24 * time.Hour,
	}
}

// Worker cancels orphaned pre-order PaymentIntents on Stripe (created but never converted to orders).
type Worker struct {
	cleaners []PreOrderPICleaner
	c        *Config
	ctx      context.Context
	stop     context.CancelFunc
}

// New creates a new Stripe reconciliation worker.
func New(c *Config, cleaners ...PreOrderPICleaner) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.PreOrderThreshold == 0 {
		c.PreOrderThreshold = 24 * time.Hour
	}
	if c.WorkerInterval == 0 {
		c.WorkerInterval = 15 * time.Minute
	}
	return &Worker{
		cleaners: cleaners,
		c:        c,
	}
}

// Start starts the worker.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("stripe reconcile worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	go w.worker(w.ctx)
	return nil
}

// Stop stops the worker gracefully.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("stripe reconcile worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	return nil
}
