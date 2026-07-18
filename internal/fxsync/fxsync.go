// Package fxsync runs a periodic job that fetches external FX reference rates (ECB euro reference
// rates) and upserts them into costing_fx_rate, so the base-currency fold used for tech-card
// costing and the admin per-currency margin view stays current without anyone maintaining rates by
// hand. Rates are dated to the ECB reference day and keyed on (currency, valid_from), so the
// as-of-today read (store.GetCostingFxRatesToBase) naturally picks the latest published rate; a
// manual row for a (currency, date) the fetcher hasn't published still wins for that date.
package fxsync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// tickTimeout bounds the fetch+upsert of a single tick so a stuck request can't block the loop or
// stall graceful shutdown.
const tickTimeout = 30 * time.Second

// Backoff bounds for consecutive-failure backoff (base * 2^(n-1), capped at backoffMax); a
// successful tick resets it. Mirrors opexmaterialize/stripereconcile.
const (
	backoffBase = 1 * time.Minute
	backoffMax  = 30 * time.Minute
)

// Config configures the external FX-rate sync worker.
type Config struct {
	// Enabled gates the worker entirely (off unless explicitly enabled), matching the analytics
	// workers. When false the worker is never constructed.
	Enabled bool `mapstructure:"enabled"`
	// SourceURL is the ECB daily reference-rates XML feed. Empty falls back to the ECB default.
	SourceURL string `mapstructure:"source_url"`
	// RefreshInterval is how often rates are refetched. The ECB publishes once per working day, so a
	// sub-day interval only means the day's rate lands sooner after a restart; upserts are idempotent.
	RefreshInterval time.Duration `mapstructure:"refresh_interval"`
	// HTTPTimeout bounds a single outbound fetch. Empty falls back to the client default.
	HTTPTimeout time.Duration `mapstructure:"http_timeout"`
}

// DefaultConfig returns sane defaults (disabled; refresh twice a day).
func DefaultConfig() Config {
	return Config{
		Enabled:         false,
		SourceURL:       defaultSourceURL,
		RefreshInterval: 12 * time.Hour,
		HTTPTimeout:     defaultHTTPTimeout,
	}
}

// ratesSource fetches external rates already expressed as base-currency-per-unit for a given base.
type ratesSource interface {
	RatesToBase(ctx context.Context, base string) ([]entity.CostingFxRate, error)
}

// ratesStore upserts costing FX rates (satisfied by dependency.TechCards).
type ratesStore interface {
	UpsertCostingFxRates(ctx context.Context, rates []entity.CostingFxRate) error
}

// Worker periodically syncs external FX rates into costing_fx_rate.
type Worker struct {
	c       *Config
	src     ratesSource
	store   ratesStore
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "fxsync" }

// LastSuccess implements health.Reporter (zero time until the first clean tick).
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// New constructs an FX-sync worker backed by the ECB reference-rates feed.
func New(c *Config, store ratesStore) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.RefreshInterval <= 0 {
		c.RefreshInterval = DefaultConfig().RefreshInterval
	}
	return &Worker{
		c:     c,
		src:   newECBClient(c.SourceURL, c.HTTPTimeout),
		store: store,
	}
}

// Start launches the worker goroutine.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("fxsync worker already started")
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
		return fmt.Errorf("fxsync worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}

func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(w.c.RefreshInterval)
	defer ticker.Stop()

	// Fetch once at startup so a fresh boot doesn't wait a full interval for the current rates.
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
			slog.Default().WarnContext(ctx, "fxsync: backing off after failed tick",
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

// baseCurrency is the configured base (EUR by default); rates are expressed relative to it.
func baseCurrency() string {
	if b := cache.GetBaseCurrency(); b != "" {
		return b
	}
	return "EUR"
}

// runOnce fetches the external rates and upserts them. Returns whether the tick succeeded; a
// transient outage just retries on the next tick (nothing is written on failure).
func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "fxsync")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	rates, err := w.src.RatesToBase(ctx, baseCurrency())
	if err != nil {
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "fxsync: fetch failed", slog.String("err", err.Error()))
		return false
	}
	if len(rates) == 0 {
		// A successful fetch with no rows is unexpected; skip the upsert (empty is a store no-op) but
		// don't mark the tick failed — there's nothing transient to retry.
		w.tracker.MarkSuccess()
		return true
	}
	if err := w.store.UpsertCostingFxRates(ctx, rates); err != nil {
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "fxsync: upsert failed", slog.String("err", err.Error()))
		return false
	}
	slog.Default().InfoContext(ctx, "fxsync: upserted external fx rates", slog.Int("count", len(rates)))
	w.tracker.MarkSuccess()
	return true
}
