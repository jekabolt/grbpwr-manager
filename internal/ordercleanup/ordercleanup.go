package ordercleanup

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/health"
)

// Config holds configuration for the order cleanup worker.
type Config struct {
	WorkerInterval  time.Duration `mapstructure:"worker_interval"`
	PlacedThreshold time.Duration `mapstructure:"placed_threshold"` // e.g. 24h - cancel orders in Placed older than this
}

// DefaultConfig returns default configuration values.
func DefaultConfig() Config {
	return Config{
		WorkerInterval:  15 * time.Minute,
		PlacedThreshold: 24 * time.Hour,
	}
}

// PaymentExpirer verifies an order's payment with the provider (e.g. Stripe)
// before expiring it. Implementations MUST confirm the order when the payment
// actually succeeded instead of cancelling it, so the safety-net never cancels
// a paid order whose in-process monitor was lost (e.g. on deploy).
type PaymentExpirer interface {
	ExpireOrderPayment(ctx context.Context, orderUUID string) error
}

// Worker cancels orders stuck in Placed status (never reached InsertFiatInvoice)
// and AwaitingPayment orders past their expired_at (safety net when monitors fail).
type Worker struct {
	repo           dependency.Repository
	reservationMgr dependency.StockReservationManager
	expirer        PaymentExpirer
	c              *Config
	ctx            context.Context
	stop           context.CancelFunc
	wg             sync.WaitGroup
	tracker        health.Tracker
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "ordercleanup" }

// LastSuccess implements health.Reporter (zero time until the first clean tick).
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// New creates a new order cleanup worker. expirer may be nil, in which case
// expiry falls back to the store-level path (which does not verify the provider).
func New(c *Config, repo dependency.Repository, reservationMgr dependency.StockReservationManager, expirer PaymentExpirer) *Worker {
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
		repo:           repo,
		reservationMgr: reservationMgr,
		expirer:        expirer,
		c:              c,
	}
}

// Start starts the worker.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("order cleanup worker already started")
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
		return fmt.Errorf("order cleanup worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}
