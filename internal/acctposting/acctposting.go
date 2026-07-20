// Package acctposting runs the phase-1 accounting posting worker (docs/plan-accounting/07): it
// drains the order outbox (acct_event) and the pull sources (material movements, production
// receives, OPEX months) into the double-entry ledger via internal/store/accounting, using the pure
// posting rules in internal/accounting. It is the single writer of automated journal entries.
//
// Lock discipline (docs/plan-accounting/07, "правило локов"): repo.Tx runs at SERIALIZABLE, whose
// next-key locks would block the hot material/order write paths if the worker scanned source tables
// inside a Tx. Therefore every unit of work follows the same shape:
//
//  1. read the facts on the pool, OUTSIDE any Tx (GetOrderFactsForPosting, ListUnpostedMovements,
//     GetRunFactsForPosting, GetOpexMonthFacts, ListJournalEntries, ...);
//  2. build the entry with a pure builder (internal/accounting) — no DB, no clock;
//  3. write only the entry (+ checkpoint / event mark) in a short Tx.
//
// Idempotency on (source_type, source_key) makes a replay after a crash between read and write a
// no-op, so the read-then-write split is safe against races on the append-only/immutable sources.
package acctposting

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/health"
)

// Defaults applied by New when a field is unset (docs/plan-accounting/07).
const (
	defaultWorkerInterval = time.Minute
	defaultBatchSize      = 200
	defaultSettledWaitMax = 48 * time.Hour

	// startDateLayout is the ACCOUNTING_START_DATE format: a UTC calendar date (the cutover); every
	// fact before it stays out of the ledger ("start from zero").
	startDateLayout = "2006-01-02"
)

// Config configures the accounting posting worker.
type Config struct {
	// Enabled gates the worker (and its start-date requirement). Producers enqueue events regardless
	// of this flag; enabling the worker later just drains the accumulated queue from the cutover.
	Enabled bool `mapstructure:"enabled"`
	// WorkerInterval is how often a posting pass runs (default 1m).
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
	// BatchSize bounds each pull-source scan and the outbox pull per tick (default 200).
	BatchSize int `mapstructure:"batch_size"`
	// StartDate is the 'YYYY-MM-DD' cutover; required when Enabled. Parsed in UTC.
	StartDate string `mapstructure:"start_date"`
	// DeliveredRecognitionFrom is the 'YYYY-MM-DD' cutover (UTC) for delivered revenue recognition
	// (phase 2, wave 2): empty ⇒ every order keeps the old order_sale policy; when set, Stripe orders
	// paid on/after this date use the delivered chain (order_transit / order_delivered_sale).
	DeliveredRecognitionFrom string `mapstructure:"delivered_recognition_from"`
	// SettledWaitMax is how long a Stripe order_paid event may sit waiting for total_settled_base
	// before the worker emits a health warning (default 48h). It never auto-posts by fallback — a
	// larger gap means a broken capture pipeline, which must be seen, not masked.
	SettledWaitMax time.Duration `mapstructure:"settled_wait_max"`
	// OriginCountry is the ship-from country (ISO 3166-1 alpha-2) used by the VAT resolver (phase 2,
	// wave 1). It is NOT read from accounting.* config: app.go sets it from
	// cfg.ShippingLabel.ShipFromAddress().CountryISO2 before constructing the worker (07 §7.1). Empty is
	// tolerated — the resolver then classifies purely by destination / payment method.
	OriginCountry string `mapstructure:"-"`
}

// DefaultConfig returns the worker defaults (disabled; the cutover must be set explicitly).
func DefaultConfig() Config {
	return Config{
		Enabled:        false,
		WorkerInterval: defaultWorkerInterval,
		BatchSize:      defaultBatchSize,
		SettledWaitMax: defaultSettledWaitMax,
	}
}

// Validate checks the config when the worker is enabled: StartDate must parse as YYYY-MM-DD and not
// be in the future (posting before the cutover, or from an unset cutover, is refused). Called from
// config.Config.Validate at startup so a misconfiguration fails fast with an actionable message.
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	sd, err := parseStartDate(c.StartDate)
	if err != nil {
		return err
	}
	if sd.After(time.Now().UTC()) {
		return fmt.Errorf("accounting.start_date %q is in the future", c.StartDate)
	}
	drf, err := parseDeliveredRecognitionFrom(c.DeliveredRecognitionFrom)
	if err != nil {
		return err
	}
	if !drf.IsZero() && drf.Before(sd) {
		return fmt.Errorf("accounting.delivered_recognition_from %q precedes accounting.start_date %q", c.DeliveredRecognitionFrom, c.StartDate)
	}
	return nil
}

// parseStartDate parses a YYYY-MM-DD cutover date as midnight UTC.
func parseStartDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("accounting.start_date is required when accounting is enabled (set ACCOUNTING_START_DATE=YYYY-MM-DD)")
	}
	t, err := time.ParseInLocation(startDateLayout, s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("accounting.start_date %q: want YYYY-MM-DD: %w", s, err)
	}
	return t, nil
}

// parseDeliveredRecognitionFrom parses the YYYY-MM-DD delivered-recognition cutover as midnight UTC.
// Empty is valid (feature off) and returns the zero time.
func parseDeliveredRecognitionFrom(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation(startDateLayout, s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("accounting.delivered_recognition_from %q: want YYYY-MM-DD: %w", s, err)
	}
	return t, nil
}

// Worker posts operational facts into the ledger on a ticker. It reads facts on the pool and writes
// in short transactions (see the package doc).
type Worker struct {
	repo dependency.Repository
	c    *Config

	// startDate is the parsed cutover (midnight UTC). startDateErr holds a parse failure so a
	// misconfigured-but-started worker fails every tick loudly instead of posting from the epoch.
	startDate    time.Time
	startDateErr error

	// deliveredRecognitionFrom is the parsed delivered-recognition cutover (midnight UTC), zero when
	// the feature is off. deliveredRecognitionFromErr mirrors startDateErr: a parse failure fails
	// every tick loudly instead of silently keeping the old policy.
	deliveredRecognitionFrom    time.Time
	deliveredRecognitionFromErr error

	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "acctposting" }

// LastSuccess implements health.Reporter (zero time until the first clean tick).
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// New constructs the worker, applying defaults and parsing the cutover date. It never returns an
// error (matching the other workers); a bad/absent start date is captured and surfaced as a per-tick
// failure via RunOnce, while config.Config.Validate already rejects it at boot when Enabled.
func New(c *Config, repo dependency.Repository) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.WorkerInterval <= 0 {
		c.WorkerInterval = defaultWorkerInterval
	}
	if c.BatchSize <= 0 {
		c.BatchSize = defaultBatchSize
	}
	if c.SettledWaitMax <= 0 {
		c.SettledWaitMax = defaultSettledWaitMax
	}
	w := &Worker{repo: repo, c: c}
	if sd, err := parseStartDate(c.StartDate); err != nil {
		w.startDateErr = err
	} else {
		w.startDate = sd
	}
	if drf, err := parseDeliveredRecognitionFrom(c.DeliveredRecognitionFrom); err != nil {
		w.deliveredRecognitionFromErr = err
	} else {
		w.deliveredRecognitionFrom = drf
	}
	return w
}

// Start launches the worker goroutine.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("acctposting worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() {
		w.worker(w.ctx)
	})
	return nil
}

// Stop signals the worker to exit and waits for its goroutine to return, so the caller can safely
// close the DB afterwards.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("acctposting worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}
