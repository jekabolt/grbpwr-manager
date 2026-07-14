// Package deliverysync runs the background worker that moves shipped orders to delivered. It polls
// AfterShip for a real Delivered signal (reconciling anything the webhook missed) and, as a safety
// net, silently delivers orders whose carrier has no tracking API or whose tracking is stuck past a
// per-carrier window. Mirrors the ordercleanup worker's shape.
package deliverysync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/health"
)

// Config holds configuration for the delivery-sync worker.
type Config struct {
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
	// FallbackDefault is the timer safety-net window used when a carrier has no explicit
	// auto_deliver_after_hours override: how long after shipment to silently mark an order
	// delivered when no real Delivered signal arrived.
	FallbackDefault time.Duration `mapstructure:"fallback_default"`
}

// DefaultConfig returns default configuration values.
func DefaultConfig() Config {
	return Config{
		WorkerInterval:  15 * time.Minute,
		FallbackDefault: 14 * 24 * time.Hour, // 336h
	}
}

// Worker delivers shipped orders: a real AfterShip signal (with the delivered email) or, as a
// safety net, a silent timer-based delivery for uncovered/stuck shipments.
type Worker struct {
	repo    dependency.Repository
	tracker dependency.Tracker
	mailer  dependency.Mailer
	c       *Config
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	ht      health.Tracker
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "deliverysync" }

// LastSuccess implements health.Reporter (zero time until the first clean tick).
func (w *Worker) LastSuccess() time.Time { return w.ht.LastSuccess() }

// New creates a new delivery-sync worker. tracker may be a disabled no-op (when AfterShip is not
// configured), in which case delivery falls back entirely to the per-carrier timer safety net.
func New(c *Config, repo dependency.Repository, tracker dependency.Tracker, mailer dependency.Mailer) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.WorkerInterval == 0 {
		c.WorkerInterval = 15 * time.Minute
	}
	if c.FallbackDefault == 0 {
		c.FallbackDefault = 14 * 24 * time.Hour
	}
	return &Worker{
		repo:    repo,
		tracker: tracker,
		mailer:  mailer,
		c:       c,
	}
}

// Start starts the worker.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("delivery sync worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() {
		w.worker(w.ctx)
	})
	return nil
}

// Stop signals the worker to stop and waits for its goroutine to exit, so the caller can safely
// close shared resources (e.g. the DB) afterwards.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("delivery sync worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}
