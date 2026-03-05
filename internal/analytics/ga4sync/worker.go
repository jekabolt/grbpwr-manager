package ga4sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/circuitbreaker"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"golang.org/x/sync/errgroup"
)

// Config holds configuration for the GA4 sync worker.
type Config struct {
	WorkerInterval       time.Duration `mapstructure:"worker_interval"`
	BQInterval           time.Duration `mapstructure:"bq_interval"`
	LookbackDays         int           `mapstructure:"lookback_days"`
	RetentionDays        int           `mapstructure:"retention_days"`
	MaxBackoffRetries    int           `mapstructure:"max_backoff_retries"`
	InitialBackoff       time.Duration `mapstructure:"initial_backoff"`
	MaxBackoff           time.Duration `mapstructure:"max_backoff"`
	GA4StaleThreshold    time.Duration `mapstructure:"ga4_stale_threshold"`
	BQStaleThreshold     time.Duration `mapstructure:"bq_stale_threshold"`
}

// DefaultConfig returns default configuration values.
func DefaultConfig() Config {
	return Config{
		WorkerInterval:    1 * time.Hour,
		BQInterval:        6 * time.Hour,
		LookbackDays:      7,
		RetentionDays:     90,
		MaxBackoffRetries: 3,
		InitialBackoff:    30 * time.Second,
		MaxBackoff:        5 * time.Minute,
		GA4StaleThreshold: 3 * time.Hour,
		BQStaleThreshold:  12 * time.Hour,
	}
}

// Worker periodically syncs GA4 data to the database.
// Two independent sync tiers:
//   - GA4 API tier (WorkerInterval): daily_metrics, product_page, country, ecommerce.
//     Skips when already synced through yesterday (GA4 Data API has ~24h lag).
//   - BQ tier (BQInterval): 22 precompute queries against events_*.
//     Always runs with [today-lookback, now] since saves are idempotent and
//     intraday data streams continuously.
type Worker struct {
	ga4Client         *ga4.Client
	bqClient          dependency.BQClient
	ga4Data           dependency.GA4DataStore
	bqCache           dependency.BQCacheStore
	syncStatus        dependency.SyncStatusStore
	c                 *Config
	ctx               context.Context
	stop              context.CancelFunc
	runMu             sync.Mutex // protects ctx, stop for Start/Stop
	consecutiveErrors atomic.Int32
	ga4SyncMu         sync.Mutex
	ga4SyncInProgress bool
	bqSyncMu          sync.Mutex
	bqSyncInProgress  bool
}

// New creates a new GA4 sync worker. bqClient may be nil if BQ is disabled.
func New(ga4Client *ga4.Client, bqClient dependency.BQClient, ga4Data dependency.GA4DataStore, bqCache dependency.BQCacheStore, syncStatus dependency.SyncStatusStore, c *Config) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.WorkerInterval == 0 {
		c.WorkerInterval = 1 * time.Hour
	}
	if c.BQInterval == 0 {
		c.BQInterval = 6 * time.Hour
	}
	if c.LookbackDays == 0 {
		c.LookbackDays = 7
	}
	if c.RetentionDays == 0 {
		c.RetentionDays = 90
	}
	if c.MaxBackoffRetries == 0 {
		c.MaxBackoffRetries = 3
	}
	if c.InitialBackoff == 0 {
		c.InitialBackoff = 30 * time.Second
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = 5 * time.Minute
	}
	if c.GA4StaleThreshold == 0 {
		c.GA4StaleThreshold = 3 * time.Hour
	}
	if c.BQStaleThreshold == 0 {
		c.BQStaleThreshold = 12 * time.Hour
	}
	return &Worker{
		ga4Client:  ga4Client,
		bqClient:   bqClient,
		ga4Data:    ga4Data,
		bqCache:    bqCache,
		syncStatus: syncStatus,
		c:          c,
	}
}

// Start starts the worker.
func (w *Worker) Start(ctx context.Context) error {
	w.runMu.Lock()
	defer w.runMu.Unlock()
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("ga4 sync worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	go w.worker(w.ctx)
	return nil
}

// Stop stops the worker gracefully.
func (w *Worker) Stop() error {
	w.runMu.Lock()
	defer w.runMu.Unlock()
	if w.stop == nil {
		return fmt.Errorf("ga4 sync worker already stopped or not started")
	}
	w.stop()
	w.ctx = nil
	w.stop = nil
	return nil
}

func (w *Worker) worker(ctx context.Context) {
	if err := w.runWithBackoff(ctx, "ga4 api", w.syncGA4API); err != nil {
		slog.Default().ErrorContext(ctx, "ga4 api sync failed on startup",
			slog.String("err", err.Error()))
	}
	if err := w.runWithBackoff(ctx, "bq", w.syncBQ); err != nil {
		slog.Default().ErrorContext(ctx, "bq sync failed on startup",
			slog.String("err", err.Error()))
	}

	ga4Ticker := time.NewTicker(w.c.WorkerInterval)
	defer ga4Ticker.Stop()

	bqTicker := time.NewTicker(w.c.BQInterval)
	defer bqTicker.Stop()

	healthTicker := time.NewTicker(5 * time.Minute)
	defer healthTicker.Stop()

	for {
		select {
		case <-ga4Ticker.C:
			w.tryRunAsync(ctx, &w.ga4SyncMu, &w.ga4SyncInProgress, "ga4 api", w.syncGA4API)
		case <-bqTicker.C:
			w.tryRunAsync(ctx, &w.bqSyncMu, &w.bqSyncInProgress, "bq", w.syncBQ)
		case <-healthTicker.C:
			w.logHealthStatus(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// tryRunAsync launches fn in a goroutine if a previous run isn't still in progress.
func (w *Worker) tryRunAsync(ctx context.Context, mu *sync.Mutex, inProgress *bool, name string, fn func(context.Context) error) {
	mu.Lock()
	if *inProgress {
		slog.Default().WarnContext(ctx, name+" sync skipped: previous still in progress")
		mu.Unlock()
		return
	}
	*inProgress = true
	mu.Unlock()

	go func() {
		defer func() {
			mu.Lock()
			*inProgress = false
			mu.Unlock()
		}()
		if err := w.runWithBackoff(ctx, name, fn); err != nil {
			slog.Default().ErrorContext(ctx, name+" sync failed",
				slog.String("err", err.Error()))
		}
	}()
}

// logHealthStatus logs circuit breaker health and data staleness for monitoring/alerting.
func (w *Worker) logHealthStatus(ctx context.Context) {
	ga4State := w.ga4Client.CircuitBreakerState()
	consecutiveErrors := w.consecutiveErrors.Load()
	attrs := []any{
		slog.String("ga4_circuit", ga4State.String()),
		slog.Int("consecutive_errors", int(consecutiveErrors)),
	}

	if w.bqClient != nil {
		bqState := w.bqClient.CircuitBreakerState()
		attrs = append(attrs, slog.String("bq_circuit", bqState.String()))
	}

	statuses, err := w.syncStatus.GetAllSyncStatuses(ctx)
	if err == nil {
		now := time.Now()
		var ga4LastOK, bqLastOK time.Time
		var ga4ErrTypes, bqErrTypes []string

		for _, s := range statuses {
			isGA4 := s.SyncType == "daily_metrics" || s.SyncType == "product_page_metrics" ||
				s.SyncType == "country_metrics" || s.SyncType == "ecommerce" ||
				s.SyncType == "revenue_by_source" || s.SyncType == "product_conversion"

			if s.Success {
				if isGA4 && s.LastSyncAt.After(ga4LastOK) {
					ga4LastOK = s.LastSyncAt
				} else if !isGA4 && s.LastSyncAt.After(bqLastOK) {
					bqLastOK = s.LastSyncAt
				}
			} else {
				if isGA4 {
					ga4ErrTypes = append(ga4ErrTypes, s.SyncType)
				} else {
					bqErrTypes = append(bqErrTypes, s.SyncType)
				}
			}
		}

		if !ga4LastOK.IsZero() {
			age := now.Sub(ga4LastOK)
			attrs = append(attrs, slog.Duration("ga4_cache_age", age))
			if age > w.c.GA4StaleThreshold {
				attrs = append(attrs, slog.Bool("ga4_stale", true))
			}
		}
		if !bqLastOK.IsZero() {
			age := now.Sub(bqLastOK)
			attrs = append(attrs, slog.Duration("bq_cache_age", age))
			if age > w.c.BQStaleThreshold {
				attrs = append(attrs, slog.Bool("bq_stale", true))
			}
		}
		if len(ga4ErrTypes) > 0 {
			attrs = append(attrs, slog.Any("ga4_error_types", ga4ErrTypes))
		}
		if len(bqErrTypes) > 0 {
			attrs = append(attrs, slog.Any("bq_error_types", bqErrTypes))
		}
	}

	circuitOpen := ga4State == circuitbreaker.StateOpen ||
		(w.bqClient != nil && w.bqClient.CircuitBreakerState() == circuitbreaker.StateOpen)

	if circuitOpen {
		slog.Default().ErrorContext(ctx, "ga4sync health check: circuit breaker(s) open", attrs...)
	} else if consecutiveErrors > 0 {
		slog.Default().WarnContext(ctx, "ga4sync health check: recent errors", attrs...)
	} else {
		slog.Default().InfoContext(ctx, "ga4sync health check: ok", attrs...)
	}
}

// runWithBackoff attempts fn with exponential backoff on transient errors.
// Circuit breaker errors skip backoff since the circuit handles retry timing.
func (w *Worker) runWithBackoff(ctx context.Context, name string, fn func(context.Context) error) error {
	backoff := w.c.InitialBackoff

	for attempt := 0; attempt <= w.c.MaxBackoffRetries; attempt++ {
		err := fn(ctx)
		if err == nil {
			w.consecutiveErrors.Store(0)
			return nil
		}

		if errors.Is(err, circuitbreaker.ErrCircuitOpen) {
			slog.Default().WarnContext(ctx, name+" sync skipped: circuit breaker open",
				slog.Int("consecutive_errors", int(w.consecutiveErrors.Load())))
			w.consecutiveErrors.Add(1)
			return err
		}

		w.consecutiveErrors.Add(1)
		if attempt < w.c.MaxBackoffRetries {
			slog.Default().WarnContext(ctx, name+" sync failed, retrying with backoff",
				slog.Int("attempt", attempt+1),
				slog.Int("max_retries", w.c.MaxBackoffRetries),
				slog.Duration("backoff", backoff),
				slog.String("err", err.Error()))

			select {
			case <-time.After(backoff):
				backoff *= 2
				if backoff > w.c.MaxBackoff {
					backoff = w.c.MaxBackoff
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	return fmt.Errorf("%s sync failed after %d retries", name, w.c.MaxBackoffRetries)
}

// syncGA4API fetches from the GA4 Data API (daily_metrics, product_page, country, ecommerce).
// Skips when already synced through yesterday — the GA4 Data API has ~24h lag so there's
// nothing new to fetch until the next daily export lands.
func (w *Worker) syncGA4API(ctx context.Context) error {
	now := time.Now()
	endDate := now.AddDate(0, 0, -1) // yesterday

	startDate := endDate.AddDate(0, 0, -w.c.LookbackDays)
	if lastSync, err := w.syncStatus.GetGA4LastSyncDate(ctx, "daily_metrics"); err == nil && !lastSync.IsZero() {
		candidateStart := lastSync.AddDate(0, 0, 1)
		if candidateStart.After(endDate) {
			slog.Default().InfoContext(ctx, "ga4 api sync skipped: already synced through yesterday")
			return nil
		}
		startDate = candidateStart
	}

	slog.Default().InfoContext(ctx, "starting ga4 api sync",
		slog.String("start_date", startDate.Format("2006-01-02")),
		slog.String("end_date", endDate.Format("2006-01-02")))

	var errs []error
	if err := w.syncDailyMetrics(ctx, startDate, endDate); err != nil {
		slog.Default().ErrorContext(ctx, "failed to sync daily metrics",
			slog.String("err", err.Error()))
		errs = append(errs, err)
	}
	if err := w.syncProductPageMetrics(ctx, startDate, endDate); err != nil {
		slog.Default().ErrorContext(ctx, "failed to sync product page metrics",
			slog.String("err", err.Error()))
		errs = append(errs, err)
	}
	if err := w.syncCountryMetrics(ctx, startDate, endDate); err != nil {
		slog.Default().ErrorContext(ctx, "failed to sync country metrics",
			slog.String("err", err.Error()))
		errs = append(errs, err)
	}
	if err := w.syncEcommerce(ctx, startDate, endDate); err != nil {
		slog.Default().ErrorContext(ctx, "failed to sync ecommerce metrics",
			slog.String("err", err.Error()))
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	slog.Default().InfoContext(ctx, "ga4 api sync completed")
	return nil
}

// syncBQ runs all BQ precompute queries. Unlike GA4 API, BQ always queries
// the full lookback window + today's intraday data. The saves are idempotent
// (UPSERT), so re-processing the same dates is safe and keeps intraday fresh.
// After precompute, purges rows older than RetentionDays from all cache tables.
func (w *Worker) syncBQ(ctx context.Context) error {
	if w.bqClient == nil {
		return nil
	}

	now := time.Now()
	startDate := now.AddDate(0, 0, -w.c.LookbackDays)
	endDate := now // includes today's intraday

	slog.Default().InfoContext(ctx, "starting bq precompute",
		slog.String("start_date", startDate.Format("2006-01-02")),
		slog.String("end_date", endDate.Format("2006-01-02")))

	if err := w.runBQPrecompute(ctx, startDate, endDate); err != nil {
		return err
	}

	w.purgeOldData(ctx, now)
	return nil
}

// purgeOldData deletes rows older than RetentionDays from all analytics cache tables.
// Errors are logged but not propagated — retention is best-effort.
func (w *Worker) purgeOldData(ctx context.Context, now time.Time) {
	cutoff := now.AddDate(0, 0, -w.c.RetentionDays)
	deleted, err := w.syncStatus.DeleteOldAnalyticsData(ctx, cutoff)
	if err != nil {
		slog.Default().ErrorContext(ctx, "retention purge failed",
			slog.String("cutoff", cutoff.Format("2006-01-02")),
			slog.String("err", err.Error()))
		return
	}
	if deleted > 0 {
		slog.Default().InfoContext(ctx, "retention purge completed",
			slog.String("cutoff", cutoff.Format("2006-01-02")),
			slog.Int64("rows_deleted", deleted))
	}
}

func (w *Worker) syncDailyMetrics(ctx context.Context, startDate, endDate time.Time) error {
	metrics, err := w.ga4Client.GetDailyMetrics(ctx, startDate, endDate)
	if err != nil {
		w.syncStatus.UpdateGA4SyncStatus(ctx, "daily_metrics", endDate, false, 0, err.Error())
		return fmt.Errorf("failed to fetch daily metrics: %w", err)
	}

	if err := w.ga4Data.SaveGA4DailyMetrics(ctx, metrics); err != nil {
		w.syncStatus.UpdateGA4SyncStatus(ctx, "daily_metrics", endDate, false, 0, err.Error())
		return fmt.Errorf("failed to save daily metrics: %w", err)
	}

	w.syncStatus.UpdateGA4SyncStatus(ctx, "daily_metrics", endDate, true, len(metrics), "")
	slog.Default().InfoContext(ctx, "synced daily metrics",
		slog.Int("count", len(metrics)))
	return nil
}

func (w *Worker) syncProductPageMetrics(ctx context.Context, startDate, endDate time.Time) error {
	metrics, err := w.ga4Client.GetProductPageMetrics(ctx, startDate, endDate)
	if err != nil {
		w.syncStatus.UpdateGA4SyncStatus(ctx, "product_page_metrics", endDate, false, 0, err.Error())
		return fmt.Errorf("failed to fetch product page metrics: %w", err)
	}

	if err := w.ga4Data.SaveGA4ProductPageMetrics(ctx, metrics); err != nil {
		w.syncStatus.UpdateGA4SyncStatus(ctx, "product_page_metrics", endDate, false, 0, err.Error())
		return fmt.Errorf("failed to save product page metrics: %w", err)
	}

	w.syncStatus.UpdateGA4SyncStatus(ctx, "product_page_metrics", endDate, true, len(metrics), "")
	slog.Default().InfoContext(ctx, "synced product page metrics",
		slog.Int("count", len(metrics)))
	return nil
}

func (w *Worker) syncCountryMetrics(ctx context.Context, startDate, endDate time.Time) error {
	metrics, err := w.ga4Client.GetCountryMetrics(ctx, startDate, endDate)
	if err != nil {
		w.syncStatus.UpdateGA4SyncStatus(ctx, "country_metrics", endDate, false, 0, err.Error())
		return fmt.Errorf("failed to fetch country metrics: %w", err)
	}

	if err := w.ga4Data.SaveGA4CountryMetrics(ctx, metrics); err != nil {
		w.syncStatus.UpdateGA4SyncStatus(ctx, "country_metrics", endDate, false, 0, err.Error())
		return fmt.Errorf("failed to save country metrics: %w", err)
	}

	w.syncStatus.UpdateGA4SyncStatus(ctx, "country_metrics", endDate, true, len(metrics), "")
	slog.Default().InfoContext(ctx, "synced country metrics",
		slog.Int("count", len(metrics)))
	return nil
}

// syncEcommerce fetches GA4 ecommerce batch data and saves the 3 ecommerce tables.
func (w *Worker) syncEcommerce(ctx context.Context, startDate, endDate time.Time) error {
	ecom, revSrc, prodConv, err := w.ga4Client.GetEcommerceBatch(ctx, startDate, endDate)
	if err != nil {
		w.syncStatus.UpdateGA4SyncStatus(ctx, "ecommerce", endDate, false, 0, err.Error())
		return fmt.Errorf("fetch ecommerce batch: %w", err)
	}

	if err := w.ga4Data.SaveGA4EcommerceMetrics(ctx, ecom); err != nil {
		w.syncStatus.UpdateGA4SyncStatus(ctx, "ecommerce", endDate, false, 0, err.Error())
		return fmt.Errorf("save ecommerce metrics: %w", err)
	}
	if err := w.ga4Data.SaveGA4RevenueBySource(ctx, revSrc); err != nil {
		w.syncStatus.UpdateGA4SyncStatus(ctx, "revenue_by_source", endDate, false, 0, err.Error())
		return fmt.Errorf("save revenue by source: %w", err)
	}
	if err := w.ga4Data.SaveGA4ProductConversion(ctx, prodConv); err != nil {
		w.syncStatus.UpdateGA4SyncStatus(ctx, "product_conversion", endDate, false, 0, err.Error())
		return fmt.Errorf("save product conversion: %w", err)
	}

	total := len(ecom) + len(revSrc) + len(prodConv)
	w.syncStatus.UpdateGA4SyncStatus(ctx, "ecommerce", endDate, true, total, "")
	slog.Default().InfoContext(ctx, "synced ecommerce metrics",
		slog.Int("ecom", len(ecom)), slog.Int("rev_src", len(revSrc)), slog.Int("prod_conv", len(prodConv)))
	return nil
}

// runBQPrecompute fetches all BQ analytics and saves to cache tables.
// FIX #7: Gracefully skips when BQ is disabled.
func (w *Worker) runBQPrecompute(ctx context.Context, startDate, endDate time.Time) error {
	if w.bqClient == nil {
		slog.Default().InfoContext(ctx, "bq precompute skipped: bigquery client not configured")
		return nil
	}

	type syncFn struct {
		name string
		fn   func() error
	}

	syncs := []syncFn{
		{"bq_funnel", func() error {
			if err := w.bqCache.DeleteBQFunnelAnalysisByDateRange(ctx, startDate, endDate); err != nil {
				return fmt.Errorf("delete funnel: %w", err)
			}
			var totalRows int
			err := w.bqClient.GetFunnelAnalysisStream(ctx, startDate, endDate, 500, func(batch []entity.DailyFunnel) error {
				totalRows += len(batch)
				return w.bqCache.SaveBQFunnelAnalysis(ctx, batch)
			})
			slog.Default().InfoContext(ctx, "bq_funnel sync completed",
				slog.String("start_date", startDate.Format("2006-01-02")),
				slog.String("end_date", endDate.Format("2006-01-02")),
				slog.Int("rows_synced", totalRows))
			return err
		}},
		{"bq_oos_impact", func() error {
			rows, err := w.bqClient.GetOOSImpact(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch oos: %w", err)
			}
			return w.bqCache.SaveBQOOSImpact(ctx, rows)
		}},
		{"bq_payment_failures", func() error {
			rows, err := w.bqClient.GetPaymentFailures(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch payment failures: %w", err)
			}
			return w.bqCache.SaveBQPaymentFailures(ctx, rows)
		}},
		{"bq_web_vitals", func() error {
			rows, err := w.bqClient.GetWebVitals(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch web vitals: %w", err)
			}
			return w.bqCache.SaveBQWebVitals(ctx, rows)
		}},
		{"bq_user_journeys", func() error {
			rows, err := w.bqClient.GetUserJourneys(ctx, startDate, endDate, 200)
			if err != nil {
				return fmt.Errorf("fetch user journeys: %w", err)
			}
			return w.bqCache.SaveBQUserJourneys(ctx, rows)
		}},
		{"bq_session_duration", func() error {
			rows, err := w.bqClient.GetSessionDuration(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch session duration: %w", err)
			}
			return w.bqCache.SaveBQSessionDuration(ctx, rows)
		}},
		{"bq_size_intent", func() error {
			rows, err := w.bqClient.GetSizeIntent(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch size intent: %w", err)
			}
			return w.bqCache.SaveBQSizeIntent(ctx, rows)
		}},
		{"bq_device_funnel", func() error {
			rows, err := w.bqClient.GetDeviceFunnel(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch device funnel: %w", err)
			}
			return w.bqCache.SaveBQDeviceFunnel(ctx, rows)
		}},
		{"bq_product_engagement", func() error {
			rows, err := w.bqClient.GetProductEngagement(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch product engagement: %w", err)
			}
			return w.bqCache.SaveBQProductEngagement(ctx, rows)
		}},
		{"bq_form_errors", func() error {
			rows, err := w.bqClient.GetFormErrors(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch form errors: %w", err)
			}
			return w.bqCache.SaveBQFormErrors(ctx, rows)
		}},
		{"bq_exceptions", func() error {
			rows, err := w.bqClient.GetExceptions(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch exceptions: %w", err)
			}
			return w.bqCache.SaveBQExceptions(ctx, rows)
		}},
		{"bq_not_found_pages", func() error {
			rows, err := w.bqClient.Get404Pages(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch 404 pages: %w", err)
			}
			return w.bqCache.SaveBQNotFoundPages(ctx, rows)
		}},
		{"bq_hero_funnel", func() error {
			rows, err := w.bqClient.GetHeroFunnel(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch hero funnel: %w", err)
			}
			return w.bqCache.SaveBQHeroFunnel(ctx, rows)
		}},
		{"bq_size_confidence", func() error {
			rows, err := w.bqClient.GetSizeConfidence(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch size confidence: %w", err)
			}
			return w.bqCache.SaveBQSizeConfidence(ctx, rows)
		}},
		{"bq_payment_recovery", func() error {
			rows, err := w.bqClient.GetPaymentRecovery(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch payment recovery: %w", err)
			}
			return w.bqCache.SaveBQPaymentRecovery(ctx, rows)
		}},
		{"bq_checkout_timings", func() error {
			rows, err := w.bqClient.GetCheckoutTimings(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch checkout timings: %w", err)
			}
			return w.bqCache.SaveBQCheckoutTimings(ctx, rows)
		}},
		{"bq_scroll_depth", func() error {
			rows, err := w.bqClient.GetScrollDepth(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch scroll depth: %w", err)
			}
			return w.bqCache.SaveBQScrollDepth(ctx, rows)
		}},
		{"bq_add_to_cart_rate", func() error {
			rows, err := w.bqClient.GetAddToCartRate(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch add to cart rate: %w", err)
			}
			return w.bqCache.SaveBQAddToCartRate(ctx, rows)
		}},
		{"bq_browser_breakdown", func() error {
			rows, err := w.bqClient.GetBrowserBreakdown(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch browser breakdown: %w", err)
			}
			return w.bqCache.SaveBQBrowserBreakdown(ctx, rows)
		}},
		{"bq_newsletter", func() error {
			rows, err := w.bqClient.GetNewsletterSignups(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch newsletter signups: %w", err)
			}
			return w.bqCache.SaveBQNewsletter(ctx, rows)
		}},
		{"bq_abandoned_cart", func() error {
			rows, err := w.bqClient.GetAbandonedCart(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch abandoned cart: %w", err)
			}
			return w.bqCache.SaveBQAbandonedCart(ctx, rows)
		}},
		{"bq_campaign_attribution", func() error {
			rows, err := w.bqClient.GetCampaignAttribution(ctx, startDate, endDate)
			if err != nil {
				return fmt.Errorf("fetch campaign attribution: %w", err)
			}
			return w.bqCache.SaveBQCampaignAttribution(ctx, rows)
		}},
	}

	const concurrency = 5
	g, gCtx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, concurrency)

	var mu sync.Mutex
	var syncErrs []error

	for _, s := range syncs {
		s := s
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-gCtx.Done():
				return gCtx.Err()
			}
			if err := s.fn(); err != nil {
				slog.Default().ErrorContext(ctx, "bq precompute failed",
					slog.String("sync", s.name), slog.String("err", err.Error()))
				w.syncStatus.UpdateGA4SyncStatus(ctx, s.name, endDate, false, 0, err.Error())
				mu.Lock()
				syncErrs = append(syncErrs, fmt.Errorf("%s: %w", s.name, err))
				mu.Unlock()
				return nil
			}
			w.syncStatus.UpdateGA4SyncStatus(ctx, s.name, endDate, true, 0, "")
			slog.Default().InfoContext(ctx, "bq precompute done", slog.String("sync", s.name))
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}
	if len(syncErrs) > 0 {
		return errors.Join(syncErrs...)
	}
	return nil
}
