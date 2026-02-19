package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

type ga4Store struct {
	*MYSQLStore
}

// GA4 returns an object implementing the GA4 interface.
func (ms *MYSQLStore) GA4() dependency.GA4 {
	return &ga4Store{
		MYSQLStore: ms,
	}
}

// SaveGA4DailyMetrics saves or updates GA4 daily metrics.
func (ms *MYSQLStore) SaveGA4DailyMetrics(ctx context.Context, metrics []ga4.DailyMetrics) error {
	if len(metrics) == 0 {
		return nil
	}

	query := `
		INSERT INTO ga4_daily_metrics (
			date, sessions, users, new_users, page_views,
			bounce_rate, avg_session_duration, pages_per_session
		) VALUES (:date, :sessions, :users, :newUsers, :pageViews, :bounceRate, :avgSessionDuration, :pagesPerSession)
		ON DUPLICATE KEY UPDATE
			sessions = VALUES(sessions),
			users = VALUES(users),
			new_users = VALUES(new_users),
			page_views = VALUES(page_views),
			bounce_rate = VALUES(bounce_rate),
			avg_session_duration = VALUES(avg_session_duration),
			pages_per_session = VALUES(pages_per_session),
			updated_at = CURRENT_TIMESTAMP
	`

	for _, m := range metrics {
		params := map[string]any{
			"date":                m.Date.Format("2006-01-02"),
			"sessions":            m.Sessions,
			"users":               m.Users,
			"newUsers":            m.NewUsers,
			"pageViews":           m.PageViews,
			"bounceRate":          m.BounceRate,
			"avgSessionDuration":  m.AvgSessionDuration,
			"pagesPerSession":    m.PagesPerSession,
		}
		if err := ExecNamed(ctx, ms.db, query, params); err != nil {
			return fmt.Errorf("failed to save GA4 daily metrics for %s: %w", m.Date.Format("2006-01-02"), err)
		}
	}

	return nil
}

// SaveGA4ProductPageMetrics saves or updates GA4 product page metrics.
func (ms *MYSQLStore) SaveGA4ProductPageMetrics(ctx context.Context, metrics []ga4.ProductPageMetrics) error {
	if len(metrics) == 0 {
		return nil
	}

	query := `
		INSERT INTO ga4_product_page_metrics (
			date, product_id, page_path, page_views, add_to_carts, sessions
		) VALUES (:date, :productId, :pagePath, :pageViews, :addToCarts, :sessions)
		ON DUPLICATE KEY UPDATE
			page_path = VALUES(page_path),
			page_views = VALUES(page_views),
			add_to_carts = VALUES(add_to_carts),
			sessions = VALUES(sessions),
			updated_at = CURRENT_TIMESTAMP
	`

	for _, m := range metrics {
		if m.ProductID == 0 {
			continue
		}
		params := map[string]any{
			"date":       m.Date.Format("2006-01-02"),
			"productId":  m.ProductID,
			"pagePath":   m.PagePath,
			"pageViews":  m.PageViews,
			"addToCarts": m.AddToCarts,
			"sessions":   m.Sessions,
		}
		if err := ExecNamed(ctx, ms.db, query, params); err != nil {
			return fmt.Errorf("failed to save GA4 product metrics for product %d: %w", m.ProductID, err)
		}
	}

	return nil
}

// SaveGA4TrafficSourceMetrics saves or updates GA4 traffic source metrics.
func (ms *MYSQLStore) SaveGA4TrafficSourceMetrics(ctx context.Context, metrics []ga4.TrafficSourceMetrics) error {
	if len(metrics) == 0 {
		return nil
	}

	query := `
		INSERT INTO ga4_traffic_source_metrics (
			date, source, medium, sessions, users
		) VALUES (:date, :source, :medium, :sessions, :users)
		ON DUPLICATE KEY UPDATE
			sessions = VALUES(sessions),
			users = VALUES(users),
			updated_at = CURRENT_TIMESTAMP
	`

	for _, m := range metrics {
		params := map[string]any{
			"date":     m.Date.Format("2006-01-02"),
			"source":   m.Source,
			"medium":   m.Medium,
			"sessions": m.Sessions,
			"users":    m.Users,
		}
		if err := ExecNamed(ctx, ms.db, query, params); err != nil {
			return fmt.Errorf("failed to save GA4 traffic source metrics: %w", err)
		}
	}

	return nil
}

// SaveGA4DeviceMetrics saves or updates GA4 device metrics.
func (ms *MYSQLStore) SaveGA4DeviceMetrics(ctx context.Context, metrics []ga4.DeviceMetrics) error {
	if len(metrics) == 0 {
		return nil
	}

	query := `
		INSERT INTO ga4_device_metrics (
			date, device_category, sessions, users
		) VALUES (:date, :deviceCategory, :sessions, :users)
		ON DUPLICATE KEY UPDATE
			sessions = VALUES(sessions),
			users = VALUES(users),
			updated_at = CURRENT_TIMESTAMP
	`

	for _, m := range metrics {
		params := map[string]any{
			"date":           m.Date.Format("2006-01-02"),
			"deviceCategory": m.DeviceCategory,
			"sessions":       m.Sessions,
			"users":          m.Users,
		}
		if err := ExecNamed(ctx, ms.db, query, params); err != nil {
			return fmt.Errorf("failed to save GA4 device metrics: %w", err)
		}
	}

	return nil
}

// SaveGA4CountryMetrics saves or updates GA4 country metrics.
func (ms *MYSQLStore) SaveGA4CountryMetrics(ctx context.Context, metrics []ga4.CountryMetrics) error {
	if len(metrics) == 0 {
		return nil
	}

	query := `
		INSERT INTO ga4_country_metrics (
			date, country, sessions, users
		) VALUES (:date, :country, :sessions, :users)
		ON DUPLICATE KEY UPDATE
			sessions = VALUES(sessions),
			users = VALUES(users),
			updated_at = CURRENT_TIMESTAMP
	`

	for _, m := range metrics {
		params := map[string]any{
			"date":     m.Date.Format("2006-01-02"),
			"country":  m.Country,
			"sessions": m.Sessions,
			"users":    m.Users,
		}
		if err := ExecNamed(ctx, ms.db, query, params); err != nil {
			return fmt.Errorf("failed to save GA4 country metrics: %w", err)
		}
	}

	return nil
}

// UpdateGA4SyncStatus updates the sync status for a given sync type.
func (ms *MYSQLStore) UpdateGA4SyncStatus(ctx context.Context, syncType string, lastSyncDate time.Time, status string, recordsSynced int, errorMsg string) error {
	query := `
		INSERT INTO ga4_sync_status (
			sync_type, last_sync_date, last_sync_at, status, error_message, records_synced
		) VALUES (:syncType, :lastSyncDate, NOW(), :status, :errorMessage, :recordsSynced)
		ON DUPLICATE KEY UPDATE
			last_sync_date = VALUES(last_sync_date),
			last_sync_at = NOW(),
			status = VALUES(status),
			error_message = VALUES(error_message),
			records_synced = VALUES(records_synced),
			updated_at = CURRENT_TIMESTAMP
	`

	params := map[string]any{
		"syncType":      syncType,
		"lastSyncDate":  lastSyncDate.Format("2006-01-02"),
		"status":        status,
		"errorMessage":  errorMsg,
		"recordsSynced": recordsSynced,
	}
	return ExecNamed(ctx, ms.db, query, params)
}

// GetGA4LastSyncDate returns the last successful sync date for a given sync type.
func (ms *MYSQLStore) GetGA4LastSyncDate(ctx context.Context, syncType string) (time.Time, error) {
	query := `
		SELECT last_sync_date
		FROM ga4_sync_status
		WHERE sync_type = ? AND status = 'success'
		ORDER BY last_sync_at DESC
		LIMIT 1
	`

	var dateStr string
	err := ms.db.GetContext(ctx, &dateStr, query, syncType)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}

	return time.Parse("2006-01-02", dateStr)
}

// GetGA4DailyMetrics retrieves GA4 daily metrics for a date range.
func (ms *MYSQLStore) GetGA4DailyMetrics(ctx context.Context, from, to time.Time) ([]ga4.DailyMetrics, error) {
	query := `
		SELECT
			date, sessions, users, new_users, page_views,
			bounce_rate, avg_session_duration, pages_per_session
		FROM ga4_daily_metrics
		WHERE date >= ? AND date <= ?
		ORDER BY date ASC
	`

	rows, err := ms.db.QueryxContext(ctx, query, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []ga4.DailyMetrics
	for rows.Next() {
		var m ga4.DailyMetrics
		var dateStr string
		err := rows.Scan(
			&dateStr,
			&m.Sessions,
			&m.Users,
			&m.NewUsers,
			&m.PageViews,
			&m.BounceRate,
			&m.AvgSessionDuration,
			&m.PagesPerSession,
		)
		if err != nil {
			return nil, err
		}
		m.Date, _ = time.Parse("2006-01-02", dateStr)
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// joinInts returns a comma-separated string of ints for SQL IN clause.
func joinInts(ids []int) string {
	if len(ids) == 0 {
		return "0"
	}
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(s, ",")
}

// GetGA4ProductPageMetrics retrieves top products by page views with conversion data.
func (ms *MYSQLStore) GetGA4ProductPageMetrics(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductViewMetric, error) {
	query := `
		SELECT
			ga.product_id,
			COALESCE((SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1), p.brand) AS product_name,
			p.brand,
			SUM(ga.page_views) AS total_page_views,
			SUM(ga.sessions) AS total_sessions,
			SUM(ga.add_to_carts) AS total_add_to_carts,
			COALESCE(SUM(CASE WHEN co.id IS NOT NULL THEN oi.quantity ELSE 0 END), 0) AS total_purchases
		FROM ga4_product_page_metrics ga
		JOIN product p ON ga.product_id = p.id
		LEFT JOIN order_item oi ON oi.product_id = ga.product_id
		LEFT JOIN customer_order co ON oi.order_id = co.id
			AND co.placed >= ? AND co.placed < ?
			AND co.order_status_id IN (` + joinInts(cache.OrderStatusIDsForNetRevenue()) + `)
		WHERE ga.date >= ? AND ga.date <= ?
		GROUP BY ga.product_id, p.brand
		ORDER BY total_page_views DESC
		LIMIT ?
	`

	rows, err := ms.db.QueryxContext(ctx, query,
		from, to,
		from.Format("2006-01-02"), to.Format("2006-01-02"),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []entity.ProductViewMetric
	for rows.Next() {
		var m entity.ProductViewMetric
		err := rows.Scan(
			&m.ProductId,
			&m.ProductName,
			&m.Brand,
			&m.PageViews,
			&m.Sessions,
			&m.AddToCarts,
			&m.Purchases,
		)
		if err != nil {
			return nil, err
		}
		if m.PageViews > 0 {
			m.ConversionRate = float64(m.Purchases) / float64(m.PageViews)
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// GetGA4TrafficSourceMetrics retrieves traffic source breakdown with revenue.
func (ms *MYSQLStore) GetGA4TrafficSourceMetrics(ctx context.Context, from, to time.Time, limit int) ([]entity.TrafficSourceMetric, error) {
	query := `
		SELECT
			source,
			medium,
			SUM(sessions) AS total_sessions,
			SUM(users) AS total_users
		FROM ga4_traffic_source_metrics
		WHERE date >= ? AND date <= ?
		GROUP BY source, medium
		ORDER BY total_sessions DESC
		LIMIT ?
	`

	rows, err := ms.db.QueryxContext(ctx, query, from.Format("2006-01-02"), to.Format("2006-01-02"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []entity.TrafficSourceMetric
	for rows.Next() {
		var m entity.TrafficSourceMetric
		err := rows.Scan(&m.Source, &m.Medium, &m.Sessions, &m.Users)
		if err != nil {
			return nil, err
		}
		m.Revenue = decimal.Zero
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// GetGA4DeviceMetrics retrieves device category breakdown with conversion rates.
func (ms *MYSQLStore) GetGA4DeviceMetrics(ctx context.Context, from, to time.Time) ([]entity.DeviceMetric, error) {
	query := `
		SELECT
			device_category,
			SUM(sessions) AS total_sessions,
			SUM(users) AS total_users
		FROM ga4_device_metrics
		WHERE date >= ? AND date <= ?
		GROUP BY device_category
		ORDER BY total_sessions DESC
	`

	rows, err := ms.db.QueryxContext(ctx, query, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []entity.DeviceMetric
	for rows.Next() {
		var m entity.DeviceMetric
		err := rows.Scan(&m.DeviceCategory, &m.Sessions, &m.Users)
		if err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// GetGA4SessionsByCountry retrieves session data by country.
func (ms *MYSQLStore) GetGA4SessionsByCountry(ctx context.Context, from, to time.Time, limit int) ([]entity.GeographySessionMetric, error) {
	query := `
		SELECT
			country,
			SUM(sessions) AS total_sessions,
			SUM(users) AS total_users
		FROM ga4_country_metrics
		WHERE date >= ? AND date <= ?
		GROUP BY country
		ORDER BY total_sessions DESC
		LIMIT ?
	`

	rows, err := ms.db.QueryxContext(ctx, query, from.Format("2006-01-02"), to.Format("2006-01-02"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []entity.GeographySessionMetric
	for rows.Next() {
		var m entity.GeographySessionMetric
		err := rows.Scan(&m.Country, &m.Sessions, &m.Users)
		if err != nil {
			return nil, err
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
}
