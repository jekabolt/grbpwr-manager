// Package ga4data implements GA4 Data API persistence and sync status tracking.
package ga4data

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// TxFunc is a function that executes f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.GA4DataStore.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new GA4 data store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// SaveGA4DailyMetrics saves or updates GA4 daily metrics.
func (s *Store) SaveGA4DailyMetrics(ctx context.Context, metrics []ga4.DailyMetrics) error {
	if len(metrics) == 0 {
		return nil
	}
	cols := []string{"date", "sessions", "users", "new_users", "page_views", "bounce_rate", "avg_session_duration", "pages_per_session"}
	updCols := cols[1:]
	args := make([][]any, 0, len(metrics))
	for _, m := range metrics {
		args = append(args, []any{
			m.Date.Format("2006-01-02"),
			m.Sessions, m.Users, m.NewUsers, m.PageViews,
			m.BounceRate, m.AvgSessionDuration, m.PagesPerSession,
		})
	}
	if err := storeutil.BulkUpsert(ctx, s.DB, "ga4_daily_metrics", cols, updCols, args); err != nil {
		return fmt.Errorf("save ga4 daily metrics: %w", err)
	}
	return nil
}

// SaveGA4ProductPageMetrics saves or updates GA4 product page metrics.
func (s *Store) SaveGA4ProductPageMetrics(ctx context.Context, metrics []ga4.ProductPageMetrics) error {
	if len(metrics) == 0 {
		return nil
	}
	cols := []string{"date", "product_id", "page_path", "page_views", "add_to_carts", "sessions"}
	updCols := []string{"page_path", "page_views", "add_to_carts", "sessions"}
	args := make([][]any, 0, len(metrics))
	for _, m := range metrics {
		if m.ProductID == "" || m.ProductID == "0" {
			continue
		}
		args = append(args, []any{
			m.Date.Format("2006-01-02"),
			m.ProductID, m.PagePath, m.PageViews, m.AddToCarts, m.Sessions,
		})
	}
	if err := storeutil.BulkUpsert(ctx, s.DB, "ga4_product_page_metrics", cols, updCols, args); err != nil {
		return fmt.Errorf("save ga4 product page metrics: %w", err)
	}
	return nil
}

// SaveGA4CountryMetrics saves or updates GA4 country metrics.
func (s *Store) SaveGA4CountryMetrics(ctx context.Context, metrics []ga4.CountryMetrics) error {
	if len(metrics) == 0 {
		return nil
	}
	cols := []string{"date", "country", "sessions", "users"}
	updCols := []string{"sessions", "users"}
	args := make([][]any, 0, len(metrics))
	for _, m := range metrics {
		args = append(args, []any{
			m.Date.Format("2006-01-02"), m.Country, m.Sessions, m.Users,
		})
	}
	if err := storeutil.BulkUpsert(ctx, s.DB, "ga4_country_metrics", cols, updCols, args); err != nil {
		return fmt.Errorf("save ga4 country metrics: %w", err)
	}
	return nil
}

// SaveGA4EcommerceMetrics saves or updates GA4 ecommerce metrics.
func (s *Store) SaveGA4EcommerceMetrics(ctx context.Context, metrics []ga4.EcommerceMetrics) error {
	if len(metrics) == 0 {
		return nil
	}
	cols := []string{"date", "purchases", "revenue", "add_to_carts", "checkouts", "items_viewed"}
	updCols := cols[1:]
	args := make([][]any, 0, len(metrics))
	for _, m := range metrics {
		args = append(args, []any{
			m.Date.Format("2006-01-02"),
			m.Purchases, m.Revenue.String(), m.AddToCarts, m.Checkouts, m.ItemsViewed,
		})
	}
	if err := storeutil.BulkUpsert(ctx, s.DB, "ga4_ecommerce_metrics", cols, updCols, args); err != nil {
		return fmt.Errorf("save ga4 ecommerce metrics: %w", err)
	}
	return nil
}

// SaveGA4RevenueBySource saves or updates GA4 revenue by source metrics.
func (s *Store) SaveGA4RevenueBySource(ctx context.Context, metrics []ga4.RevenueSourceMetrics) error {
	if len(metrics) == 0 {
		return nil
	}
	cols := []string{"date", "source", "medium", "campaign", "sessions", "revenue", "purchases"}
	updCols := []string{"sessions", "revenue", "purchases"}
	args := make([][]any, 0, len(metrics))
	for _, m := range metrics {
		args = append(args, []any{
			m.Date.Format("2006-01-02"),
			m.Source, m.Medium, m.Campaign, m.Sessions, m.Revenue.String(), m.Purchases,
		})
	}
	if err := storeutil.BulkUpsert(ctx, s.DB, "ga4_revenue_by_source", cols, updCols, args); err != nil {
		return fmt.Errorf("save ga4 revenue by source: %w", err)
	}
	return nil
}

// SaveGA4ProductConversion saves or updates GA4 product conversion metrics.
func (s *Store) SaveGA4ProductConversion(ctx context.Context, metrics []ga4.ProductConversionMetrics) error {
	if len(metrics) == 0 {
		return nil
	}
	cols := []string{"date", "product_id", "product_name", "items_viewed", "add_to_carts", "purchases", "revenue"}
	updCols := []string{"product_name", "items_viewed", "add_to_carts", "purchases", "revenue"}
	args := make([][]any, 0, len(metrics))
	for _, m := range metrics {
		args = append(args, []any{
			m.Date.Format("2006-01-02"),
			m.ProductID, m.ProductName, m.ItemsViewed, m.AddToCarts, m.Purchases, m.Revenue.String(),
		})
	}
	if err := storeutil.BulkUpsert(ctx, s.DB, "ga4_product_conversion", cols, updCols, args); err != nil {
		return fmt.Errorf("save ga4 product conversion: %w", err)
	}
	return nil
}

// GetGA4DailyMetrics retrieves GA4 daily metrics for a date range.
func (s *Store) GetGA4DailyMetrics(ctx context.Context, from, to time.Time) ([]ga4.DailyMetrics, error) {
	query := `
		SELECT
			date, sessions, users, new_users, page_views,
			bounce_rate, avg_session_duration, pages_per_session
		FROM ga4_daily_metrics
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC
	`
	type row struct {
		Date               string  `db:"date"`
		Sessions           int     `db:"sessions"`
		Users              int     `db:"users"`
		NewUsers           int     `db:"new_users"`
		PageViews          int     `db:"page_views"`
		BounceRate         float64 `db:"bounce_rate"`
		AvgSessionDuration float64 `db:"avg_session_duration"`
		PagesPerSession    float64 `db:"pages_per_session"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := storeutil.QueryListNamed[row](ctx, s.DB, query, params)
	if err != nil {
		return nil, err
	}
	metrics := make([]ga4.DailyMetrics, 0, len(rows))
	for _, r := range rows {
		date, err := storeutil.ParseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		metrics = append(metrics, ga4.DailyMetrics{
			Date: date, Sessions: r.Sessions, Users: r.Users, NewUsers: r.NewUsers,
			PageViews: r.PageViews, BounceRate: r.BounceRate,
			AvgSessionDuration: r.AvgSessionDuration, PagesPerSession: r.PagesPerSession,
		})
	}
	return metrics, nil
}

// GetGA4ProductPageMetrics retrieves top products by page views with conversion data.
func (s *Store) GetGA4ProductPageMetrics(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductViewMetric, error) {
	query := `
		SELECT
			p.id AS product_id,
			COALESCE((SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1), p.brand) AS product_name,
			p.brand,
			ga.total_page_views,
			ga.total_sessions,
			ga.total_add_to_carts,
			COALESCE(orders.total_purchases, 0) AS total_purchases
		FROM (
			SELECT
				product_id,
				SUM(page_views) AS total_page_views,
				SUM(sessions) AS total_sessions,
				SUM(add_to_carts) AS total_add_to_carts
			FROM ga4_product_page_metrics
			WHERE date >= :fromDate AND date <= :toDate
			GROUP BY product_id
		) ga
		JOIN product p ON p.id = CAST(ga.product_id AS UNSIGNED)
		LEFT JOIN (
			SELECT
				oi.product_id,
				SUM(oi.quantity) AS total_purchases
			FROM order_item oi
			JOIN customer_order co ON oi.order_id = co.id
				AND co.placed >= :placedFrom AND co.placed < :placedTo
				AND co.order_status_id IN (` + storeutil.JoinInts(cache.OrderStatusIDsForNetRevenue()) + `)
			GROUP BY oi.product_id
		) orders ON orders.product_id = CAST(ga.product_id AS UNSIGNED)
		ORDER BY ga.total_page_views DESC
		LIMIT :limit
	`

	type row struct {
		ProductId   int    `db:"product_id"`
		ProductName string `db:"product_name"`
		Brand       string `db:"brand"`
		PageViews   int    `db:"total_page_views"`
		Sessions    int    `db:"total_sessions"`
		AddToCarts  int    `db:"total_add_to_carts"`
		Purchases   int    `db:"total_purchases"`
	}
	params := map[string]any{
		"placedFrom": from,
		"placedTo":   to,
		"fromDate":   from.Format("2006-01-02"),
		"toDate":     to.Format("2006-01-02"),
		"limit":      limit,
	}
	rows, err := storeutil.QueryListNamed[row](ctx, s.DB, query, params)
	if err != nil {
		return nil, err
	}
	metrics := make([]entity.ProductViewMetric, 0, len(rows))
	for _, r := range rows {
		m := entity.ProductViewMetric{
			ProductId: r.ProductId, ProductName: r.ProductName, Brand: r.Brand,
			PageViews: r.PageViews, Sessions: r.Sessions, AddToCarts: r.AddToCarts, Purchases: r.Purchases,
		}
		if m.PageViews > 0 {
			m.ConversionRate = float64(m.Purchases) / float64(m.PageViews)
		}
		metrics = append(metrics, m)
	}
	return metrics, nil
}

// GetGA4SessionsByCountry retrieves session data by country.
func (s *Store) GetGA4SessionsByCountry(ctx context.Context, from, to time.Time, limit int) ([]entity.GeographySessionMetric, error) {
	query := `
		SELECT
			country,
			SUM(sessions) AS total_sessions,
			SUM(users) AS total_users
		FROM ga4_country_metrics
		WHERE date >= :fromDate AND date <= :toDate
		GROUP BY country
		ORDER BY total_sessions DESC
		LIMIT :limit
	`
	type row struct {
		Country  string `db:"country"`
		Sessions int    `db:"total_sessions"`
		Users    int    `db:"total_users"`
	}
	params := map[string]any{
		"fromDate": from.Format("2006-01-02"),
		"toDate":   to.Format("2006-01-02"),
		"limit":    limit,
	}
	rows, err := storeutil.QueryListNamed[row](ctx, s.DB, query, params)
	if err != nil {
		return nil, err
	}
	metrics := make([]entity.GeographySessionMetric, 0, len(rows))
	for _, r := range rows {
		metrics = append(metrics, entity.GeographySessionMetric{
			Country: r.Country, Sessions: r.Sessions, Users: r.Users,
		})
	}
	return metrics, nil
}
