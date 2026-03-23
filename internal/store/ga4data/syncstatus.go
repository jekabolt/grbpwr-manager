package ga4data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// SyncStatusStore implements dependency.SyncStatusStore.
type SyncStatusStore struct {
	storeutil.Base
	txFunc TxFunc
}

// NewSyncStatus creates a new sync status store.
func NewSyncStatus(base storeutil.Base, txFunc TxFunc) *SyncStatusStore {
	return &SyncStatusStore{Base: base, txFunc: txFunc}
}

// UpdateGA4SyncStatus updates the sync status for a given sync type.
func (s *SyncStatusStore) UpdateGA4SyncStatus(ctx context.Context, syncType string, lastSyncDate time.Time, success bool, recordsSynced int, errorMsg string) error {
	query := `
		INSERT INTO ga4_sync_status (
			sync_type, last_sync_date, last_sync_at, success, error_message, records_synced
		) VALUES (:syncType, :lastSyncDate, NOW(), :success, :errorMessage, :recordsSynced)
		ON DUPLICATE KEY UPDATE
			last_sync_date = VALUES(last_sync_date),
			last_sync_at = NOW(),
			success = VALUES(success),
			error_message = VALUES(error_message),
			records_synced = VALUES(records_synced),
			updated_at = CURRENT_TIMESTAMP
	`
	params := map[string]any{
		"syncType":      syncType,
		"lastSyncDate":  lastSyncDate.Format("2006-01-02"),
		"success":       success,
		"errorMessage":  errorMsg,
		"recordsSynced": recordsSynced,
	}
	return storeutil.ExecNamed(ctx, s.DB, query, params)
}

// GetGA4LastSyncDate returns the last successful sync date for a given sync type.
func (s *SyncStatusStore) GetGA4LastSyncDate(ctx context.Context, syncType string) (time.Time, error) {
	query := `
		SELECT last_sync_date
		FROM ga4_sync_status
		WHERE sync_type = :syncType AND success = 1
		ORDER BY last_sync_at DESC
		LIMIT 1
	`
	type row struct {
		LastSyncDate string `db:"last_sync_date"`
	}
	r, err := storeutil.QueryNamedOne[row](ctx, s.DB, query, map[string]any{"syncType": syncType})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	return storeutil.ParseDateStr(r.LastSyncDate)
}

// GetGA4MinLastSyncDate returns the minimum last successful sync date across all sync types.
func (s *SyncStatusStore) GetGA4MinLastSyncDate(ctx context.Context) (time.Time, error) {
	query := `
		SELECT MIN(last_sync_date) AS min_date
		FROM ga4_sync_status
		WHERE success = 1
	`
	type row struct {
		MinDate sql.NullString `db:"min_date"`
	}
	r, err := storeutil.QueryNamedOne[row](ctx, s.DB, query, nil)
	if err != nil {
		return time.Time{}, err
	}
	if !r.MinDate.Valid || r.MinDate.String == "" {
		return time.Time{}, nil
	}
	return storeutil.ParseDateStr(r.MinDate.String)
}

// GetAllSyncStatuses returns the latest sync status for every sync_type.
func (s *SyncStatusStore) GetAllSyncStatuses(ctx context.Context) ([]entity.SyncSourceStatus, error) {
	query := `
		SELECT sync_type, last_sync_at, last_sync_date, success,
			COALESCE(error_message, '') AS error_message, records_synced
		FROM ga4_sync_status
		ORDER BY last_sync_at DESC
	`
	type syncStatusRow struct {
		SyncType      string    `db:"sync_type"`
		LastSyncAt    time.Time `db:"last_sync_at"`
		LastSyncDate  string    `db:"last_sync_date"`
		Success       bool      `db:"success"`
		ErrorMessage  string    `db:"error_message"`
		RecordsSynced int       `db:"records_synced"`
	}
	rows, err := storeutil.QueryListNamed[syncStatusRow](ctx, s.DB, query, nil)
	if err != nil {
		return nil, fmt.Errorf("get all sync statuses: %w", err)
	}
	result := make([]entity.SyncSourceStatus, 0, len(rows))
	for _, row := range rows {
		date, err := storeutil.ParseDateStr(row.LastSyncDate)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.SyncSourceStatus{
			SyncType:      row.SyncType,
			LastSyncAt:    row.LastSyncAt,
			LastSyncDate:  date,
			Success:       row.Success,
			ErrorMessage:  row.ErrorMessage,
			RecordsSynced: row.RecordsSynced,
		})
	}
	return result, nil
}

// DeleteOldAnalyticsData removes rows with date < olderThan from all GA4 and BQ cache tables.
// Returns total rows deleted across all tables. Executes within a transaction to ensure atomicity.
func (s *SyncStatusStore) DeleteOldAnalyticsData(ctx context.Context, olderThan time.Time) (int64, error) {
	tables := []string{
		// GA4 tables
		"ga4_daily_metrics",
		"ga4_product_page_metrics",
		"ga4_country_metrics",
		"ga4_ecommerce_metrics",
		"ga4_revenue_by_source",
		"ga4_product_conversion",
		// BQ cache tables
		"bq_funnel_analysis",
		"bq_oos_impact",
		"bq_payment_failures",
		"bq_web_vitals",
		"bq_user_journeys",
		"bq_session_duration",
		"bq_size_intent",
		"bq_device_funnel",
		"bq_product_engagement",
		"bq_form_errors",
		"bq_exceptions",
		"bq_not_found_pages",
		"bq_hero_funnel",
		"bq_size_confidence",
		"bq_payment_recovery",
		"bq_checkout_timings",
		"bq_time_on_page",
		"bq_product_zoom",
		"bq_image_swipes",
		"bq_size_guide_clicks",
		"bq_details_expansion",
		"bq_notify_me_intent",
		"bq_add_to_cart_rate",
		"bq_browser_breakdown",
		"bq_newsletter",
		"bq_abandoned_cart",
		"bq_campaign_attribution",
	}

	cutoff := olderThan.Format("2006-01-02")

	var total int64
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		for _, t := range tables {
			res, err := rep.DB().ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE date < ?", t), cutoff)
			if err != nil {
				return fmt.Errorf("delete old data from %s: %w", t, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				slog.Warn("failed to get rows affected", "table", t, "err", err)
			} else {
				total += n
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
