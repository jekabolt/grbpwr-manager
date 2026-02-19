package ga4sync

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
)

// Store defines the interface for GA4 data persistence.
type Store interface {
	SaveGA4DailyMetrics(ctx context.Context, metrics []ga4.DailyMetrics) error
	SaveGA4ProductPageMetrics(ctx context.Context, metrics []ga4.ProductPageMetrics) error
	SaveGA4TrafficSourceMetrics(ctx context.Context, metrics []ga4.TrafficSourceMetrics) error
	SaveGA4DeviceMetrics(ctx context.Context, metrics []ga4.DeviceMetrics) error
	SaveGA4CountryMetrics(ctx context.Context, metrics []ga4.CountryMetrics) error
	UpdateGA4SyncStatus(ctx context.Context, syncType string, lastSyncDate time.Time, status string, recordsSynced int, errorMsg string) error
	GetGA4LastSyncDate(ctx context.Context, syncType string) (time.Time, error)
}

// Config holds configuration for the GA4 sync worker.
type Config struct {
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
	LookbackDays   int           `mapstructure:"lookback_days"` // how many days back to sync on each run
}

// DefaultConfig returns default configuration values.
func DefaultConfig() Config {
	return Config{
		WorkerInterval: 1 * time.Hour,
		LookbackDays:   7,
	}
}

// Worker periodically syncs GA4 data to the database.
type Worker struct {
	ga4Client *ga4.Client
	store     Store
	c         *Config
	ctx       context.Context
	stop      context.CancelFunc
}

// New creates a new GA4 sync worker.
func New(ga4Client *ga4.Client, store Store, c *Config) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.WorkerInterval == 0 {
		c.WorkerInterval = 1 * time.Hour
	}
	if c.LookbackDays == 0 {
		c.LookbackDays = 7
	}
	return &Worker{
		ga4Client: ga4Client,
		store:     store,
		c:         c,
	}
}

// Start starts the worker.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("ga4 sync worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	go w.worker(w.ctx)
	return nil
}

// Stop stops the worker gracefully.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("ga4 sync worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	return nil
}

func (w *Worker) worker(ctx context.Context) {
	// Run immediately on startup
	if err := w.syncAll(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "ga4 sync failed on startup",
			slog.String("err", err.Error()))
	}

	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := w.syncAll(ctx); err != nil {
				slog.Default().ErrorContext(ctx, "ga4 sync failed",
					slog.String("err", err.Error()))
			}
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) syncAll(ctx context.Context) error {
	now := time.Now()
	endDate := now.AddDate(0, 0, -1) // yesterday (GA4 data has ~24h delay)
	startDate := endDate.AddDate(0, 0, -w.c.LookbackDays)

	slog.Default().InfoContext(ctx, "starting ga4 sync",
		slog.String("start_date", startDate.Format("2006-01-02")),
		slog.String("end_date", endDate.Format("2006-01-02")))

	// Sync daily metrics
	if err := w.syncDailyMetrics(ctx, startDate, endDate); err != nil {
		slog.Default().ErrorContext(ctx, "failed to sync daily metrics",
			slog.String("err", err.Error()))
	}

	// Sync product page metrics
	if err := w.syncProductPageMetrics(ctx, startDate, endDate); err != nil {
		slog.Default().ErrorContext(ctx, "failed to sync product page metrics",
			slog.String("err", err.Error()))
	}

	// Sync traffic source metrics
	if err := w.syncTrafficSourceMetrics(ctx, startDate, endDate); err != nil {
		slog.Default().ErrorContext(ctx, "failed to sync traffic source metrics",
			slog.String("err", err.Error()))
	}

	// Sync device metrics
	if err := w.syncDeviceMetrics(ctx, startDate, endDate); err != nil {
		slog.Default().ErrorContext(ctx, "failed to sync device metrics",
			slog.String("err", err.Error()))
	}

	// Sync country metrics
	if err := w.syncCountryMetrics(ctx, startDate, endDate); err != nil {
		slog.Default().ErrorContext(ctx, "failed to sync country metrics",
			slog.String("err", err.Error()))
	}

	slog.Default().InfoContext(ctx, "ga4 sync completed successfully")
	return nil
}

func (w *Worker) syncDailyMetrics(ctx context.Context, startDate, endDate time.Time) error {
	metrics, err := w.ga4Client.GetDailyMetrics(ctx, startDate, endDate)
	if err != nil {
		w.store.UpdateGA4SyncStatus(ctx, "daily_metrics", endDate, "error", 0, err.Error())
		return fmt.Errorf("failed to fetch daily metrics: %w", err)
	}

	if err := w.store.SaveGA4DailyMetrics(ctx, metrics); err != nil {
		w.store.UpdateGA4SyncStatus(ctx, "daily_metrics", endDate, "error", 0, err.Error())
		return fmt.Errorf("failed to save daily metrics: %w", err)
	}

	w.store.UpdateGA4SyncStatus(ctx, "daily_metrics", endDate, "success", len(metrics), "")
	slog.Default().InfoContext(ctx, "synced daily metrics",
		slog.Int("count", len(metrics)))
	return nil
}

func (w *Worker) syncProductPageMetrics(ctx context.Context, startDate, endDate time.Time) error {
	metrics, err := w.ga4Client.GetProductPageMetrics(ctx, startDate, endDate)
	if err != nil {
		w.store.UpdateGA4SyncStatus(ctx, "product_page_metrics", endDate, "error", 0, err.Error())
		return fmt.Errorf("failed to fetch product page metrics: %w", err)
	}

	if err := w.store.SaveGA4ProductPageMetrics(ctx, metrics); err != nil {
		w.store.UpdateGA4SyncStatus(ctx, "product_page_metrics", endDate, "error", 0, err.Error())
		return fmt.Errorf("failed to save product page metrics: %w", err)
	}

	w.store.UpdateGA4SyncStatus(ctx, "product_page_metrics", endDate, "success", len(metrics), "")
	slog.Default().InfoContext(ctx, "synced product page metrics",
		slog.Int("count", len(metrics)))
	return nil
}

func (w *Worker) syncTrafficSourceMetrics(ctx context.Context, startDate, endDate time.Time) error {
	metrics, err := w.ga4Client.GetTrafficSourceMetrics(ctx, startDate, endDate)
	if err != nil {
		w.store.UpdateGA4SyncStatus(ctx, "traffic_source_metrics", endDate, "error", 0, err.Error())
		return fmt.Errorf("failed to fetch traffic source metrics: %w", err)
	}

	if err := w.store.SaveGA4TrafficSourceMetrics(ctx, metrics); err != nil {
		w.store.UpdateGA4SyncStatus(ctx, "traffic_source_metrics", endDate, "error", 0, err.Error())
		return fmt.Errorf("failed to save traffic source metrics: %w", err)
	}

	w.store.UpdateGA4SyncStatus(ctx, "traffic_source_metrics", endDate, "success", len(metrics), "")
	slog.Default().InfoContext(ctx, "synced traffic source metrics",
		slog.Int("count", len(metrics)))
	return nil
}

func (w *Worker) syncDeviceMetrics(ctx context.Context, startDate, endDate time.Time) error {
	metrics, err := w.ga4Client.GetDeviceMetrics(ctx, startDate, endDate)
	if err != nil {
		w.store.UpdateGA4SyncStatus(ctx, "device_metrics", endDate, "error", 0, err.Error())
		return fmt.Errorf("failed to fetch device metrics: %w", err)
	}

	if err := w.store.SaveGA4DeviceMetrics(ctx, metrics); err != nil {
		w.store.UpdateGA4SyncStatus(ctx, "device_metrics", endDate, "error", 0, err.Error())
		return fmt.Errorf("failed to save device metrics: %w", err)
	}

	w.store.UpdateGA4SyncStatus(ctx, "device_metrics", endDate, "success", len(metrics), "")
	slog.Default().InfoContext(ctx, "synced device metrics",
		slog.Int("count", len(metrics)))
	return nil
}

func (w *Worker) syncCountryMetrics(ctx context.Context, startDate, endDate time.Time) error {
	metrics, err := w.ga4Client.GetCountryMetrics(ctx, startDate, endDate)
	if err != nil {
		w.store.UpdateGA4SyncStatus(ctx, "country_metrics", endDate, "error", 0, err.Error())
		return fmt.Errorf("failed to fetch country metrics: %w", err)
	}

	if err := w.store.SaveGA4CountryMetrics(ctx, metrics); err != nil {
		w.store.UpdateGA4SyncStatus(ctx, "country_metrics", endDate, "error", 0, err.Error())
		return fmt.Errorf("failed to save country metrics: %w", err)
	}

	w.store.UpdateGA4SyncStatus(ctx, "country_metrics", endDate, "success", len(metrics), "")
	slog.Default().InfoContext(ctx, "synced country metrics",
		slog.Int("count", len(metrics)))
	return nil
}
