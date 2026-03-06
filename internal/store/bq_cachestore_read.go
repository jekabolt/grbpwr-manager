package store

import (
	"context"
	"fmt"
	"time"

	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

type bqCacheStoreRead struct {
	*MYSQLStore
}

func (s *bqCacheStoreRead) SumBQFunnelAnalysis(ctx context.Context, from, to time.Time) (*entity.FunnelAggregate, error) {
	query := `
		SELECT
			COALESCE(SUM(session_start_users), 0) AS session_start_users,
			COALESCE(SUM(view_item_list_users), 0) AS view_item_list_users,
			COALESCE(SUM(select_item_users), 0) AS select_item_users,
			COALESCE(SUM(view_item_users), 0) AS view_item_users,
			COALESCE(SUM(size_selected_users), 0) AS size_selected_users,
			COALESCE(SUM(add_to_cart_users), 0) AS add_to_cart_users,
			COALESCE(SUM(begin_checkout_users), 0) AS begin_checkout_users,
			COALESCE(SUM(add_shipping_info_users), 0) AS add_shipping_info_users,
			COALESCE(SUM(add_payment_info_users), 0) AS add_payment_info_users,
			COALESCE(SUM(purchase_users), 0) AS purchase_users
		FROM bq_funnel_analysis
		WHERE date >= :fromDate AND date <= :toDate
	`
	agg, err := QueryNamedOne[entity.FunnelAggregate](ctx, s.DB(), query, map[string]any{
		"fromDate": from.Format("2006-01-02"),
		"toDate":   to.Format("2006-01-02"),
	})
	if err != nil {
		return nil, fmt.Errorf("sum bq funnel analysis: %w", err)
	}
	return &agg, nil
}

func (s *bqCacheStoreRead) GetDailyBQFunnelAnalysis(ctx context.Context, from, to time.Time) ([]entity.DailyFunnel, error) {
	query := `
		SELECT date, session_start_users, view_item_list_users, select_item_users,
			view_item_users, size_selected_users, add_to_cart_users,
			begin_checkout_users, add_shipping_info_users, add_payment_info_users, purchase_users
		FROM bq_funnel_analysis
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC
	`
	type row struct {
		Date string `db:"date"`
		entity.FunnelSteps
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.DailyFunnel, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.DailyFunnel{Date: date, FunnelSteps: r.FunnelSteps})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQOOSImpact(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.OOSImpactMetric, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, product_id, product_name, size_id, size_name, product_price, currency,
			click_count, estimated_lost_sales, estimated_lost_revenue
		FROM bq_oos_impact
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY estimated_lost_revenue DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date                 string          `db:"date"`
		ProductID            string          `db:"product_id"`
		ProductName          string          `db:"product_name"`
		SizeID               int             `db:"size_id"`
		SizeName             string          `db:"size_name"`
		ProductPrice         decimal.Decimal  `db:"product_price"`
		Currency             string          `db:"currency"`
		ClickCount           int64           `db:"click_count"`
		EstimatedLostSales   decimal.Decimal  `db:"estimated_lost_sales"`
		EstimatedLostRevenue decimal.Decimal `db:"estimated_lost_revenue"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.OOSImpactMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.OOSImpactMetric{
			Date: date, ProductID: r.ProductID, ProductName: r.ProductName, SizeID: r.SizeID, SizeName: r.SizeName,
			ProductPrice: r.ProductPrice, Currency: r.Currency, ClickCount: r.ClickCount,
			EstimatedLostSales: r.EstimatedLostSales, EstimatedLostRevenue: r.EstimatedLostRevenue,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQPaymentFailures(ctx context.Context, from, to time.Time) ([]entity.PaymentFailureMetric, error) {
	query := `
		SELECT date, error_code, payment_type, failure_count, total_failed_value, avg_failed_order_value
		FROM bq_payment_failures
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY total_failed_value DESC
	`
	type row struct {
		Date                string          `db:"date"`
		ErrorCode           string          `db:"error_code"`
		PaymentType         string          `db:"payment_type"`
		FailureCount        int64           `db:"failure_count"`
		TotalFailedValue    decimal.Decimal `db:"total_failed_value"`
		AvgFailedOrderValue decimal.Decimal `db:"avg_failed_order_value"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.PaymentFailureMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.PaymentFailureMetric{
			Date: date, ErrorCode: r.ErrorCode, PaymentType: r.PaymentType, FailureCount: r.FailureCount,
			TotalFailedValue: r.TotalFailedValue, AvgFailedOrderValue: r.AvgFailedOrderValue,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQWebVitals(ctx context.Context, from, to time.Time) ([]entity.WebVitalMetric, error) {
	query := `
		SELECT date, metric_name, metric_rating, session_count, conversions, avg_metric_value
		FROM bq_web_vitals
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC, metric_name, metric_rating
	`
	type row struct {
		Date           string  `db:"date"`
		MetricName     string  `db:"metric_name"`
		MetricRating   string  `db:"metric_rating"`
		SessionCount   int64   `db:"session_count"`
		Conversions    int64   `db:"conversions"`
		AvgMetricValue float64 `db:"avg_metric_value"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.WebVitalMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.WebVitalMetric{
			Date: date, MetricName: r.MetricName, MetricRating: r.MetricRating,
			SessionCount: r.SessionCount, Conversions: r.Conversions, AvgMetricValue: r.AvgMetricValue,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQUserJourneys(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.UserJourneyMetric, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, journey_path, session_count, conversions
		FROM bq_user_journeys
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY session_count DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date         string `db:"date"`
		JourneyPath  string `db:"journey_path"`
		SessionCount int64  `db:"session_count"`
		Conversions  int64  `db:"conversions"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.UserJourneyMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.UserJourneyMetric{
			Date: date, JourneyPath: r.JourneyPath, SessionCount: r.SessionCount, Conversions: r.Conversions,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQSessionDuration(ctx context.Context, from, to time.Time) ([]entity.SessionDurationMetric, error) {
	query := `
		SELECT date, avg_time_between_events_seconds, median_time_between_events
		FROM bq_session_duration
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC
	`
	type row struct {
		Date                        string  `db:"date"`
		AvgTimeBetweenEventsSeconds float64 `db:"avg_time_between_events_seconds"`
		MedianTimeBetweenEvents    float64 `db:"median_time_between_events"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.SessionDurationMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.SessionDurationMetric{
			Date: date, AvgTimeBetweenEventsSeconds: r.AvgTimeBetweenEventsSeconds, MedianTimeBetweenEvents: r.MedianTimeBetweenEvents,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQSizeIntent(ctx context.Context, from, to time.Time, limit, offset int) ([]bq.SizeIntentRow, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, product_id, size_id, size_name, size_clicks
		FROM bq_size_intent
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC, size_clicks DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date       string `db:"date"`
		ProductID  string `db:"product_id"`
		SizeID     int    `db:"size_id"`
		SizeName   string `db:"size_name"`
		SizeClicks int64  `db:"size_clicks"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]bq.SizeIntentRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, bq.SizeIntentRow{
			Date: date, ProductID: r.ProductID, SizeID: r.SizeID, SizeName: r.SizeName, SizeClicks: r.SizeClicks,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQDeviceFunnel(ctx context.Context, from, to time.Time) ([]entity.DeviceFunnelMetric, error) {
	query := `
		SELECT date, device_category, sessions, add_to_cart_users, checkout_users, purchase_users
		FROM bq_device_funnel
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC, sessions DESC
	`
	type row struct {
		Date           string `db:"date"`
		DeviceCategory string `db:"device_category"`
		Sessions       int64  `db:"sessions"`
		AddToCartUsers int64  `db:"add_to_cart_users"`
		CheckoutUsers  int64  `db:"checkout_users"`
		PurchaseUsers  int64  `db:"purchase_users"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.DeviceFunnelMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.DeviceFunnelMetric{
			Date: date, DeviceCategory: r.DeviceCategory, Sessions: r.Sessions,
			AddToCartUsers: r.AddToCartUsers, CheckoutUsers: r.CheckoutUsers, PurchaseUsers: r.PurchaseUsers,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQProductEngagement(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.ProductEngagementMetric, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, product_id, product_name, image_views, zoom_events, scroll_75, scroll_100
		FROM bq_product_engagement
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY image_views DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date        string `db:"date"`
		ProductID   string `db:"product_id"`
		ProductName string `db:"product_name"`
		ImageViews  int64  `db:"image_views"`
		ZoomEvents  int64  `db:"zoom_events"`
		Scroll75    int64  `db:"scroll_75"`
		Scroll100   int64  `db:"scroll_100"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.ProductEngagementMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.ProductEngagementMetric{
			Date: date, ProductID: r.ProductID, ProductName: r.ProductName,
			ImageViews: r.ImageViews, ZoomEvents: r.ZoomEvents, Scroll75: r.Scroll75, Scroll100: r.Scroll100,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQFormErrors(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.FormErrorMetric, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, field_name, error_count
		FROM bq_form_errors
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY error_count DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date       string `db:"date"`
		FieldName  string `db:"field_name"`
		ErrorCount int64  `db:"error_count"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.FormErrorMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.FormErrorMetric{Date: date, FieldName: r.FieldName, ErrorCount: r.ErrorCount})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQExceptions(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.ExceptionMetric, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, page_path, exception_count, description
		FROM bq_exceptions
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY exception_count DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date           string `db:"date"`
		PagePath       string `db:"page_path"`
		ExceptionCount int64  `db:"exception_count"`
		Description    string `db:"description"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.ExceptionMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.ExceptionMetric{Date: date, PagePath: r.PagePath, ExceptionCount: r.ExceptionCount, Description: r.Description})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQNotFoundPages(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.NotFoundMetric, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, page_path, hit_count
		FROM bq_not_found_pages
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY hit_count DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date     string `db:"date"`
		PagePath string `db:"page_path"`
		HitCount int64  `db:"hit_count"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.NotFoundMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.NotFoundMetric{Date: date, PagePath: r.PagePath, HitCount: r.HitCount})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQHeroFunnel(ctx context.Context, from, to time.Time) ([]entity.HeroFunnelMetric, error) {
	query := `
		SELECT date, hero_click_users, view_item_users, purchase_users
		FROM bq_hero_funnel
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC
	`
	type row struct {
		Date           string `db:"date"`
		HeroClickUsers int64  `db:"hero_click_users"`
		ViewItemUsers  int64  `db:"view_item_users"`
		PurchaseUsers  int64  `db:"purchase_users"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.HeroFunnelMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.HeroFunnelMetric{Date: date, HeroClickUsers: r.HeroClickUsers, ViewItemUsers: r.ViewItemUsers, PurchaseUsers: r.PurchaseUsers})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQSizeConfidence(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.SizeConfidenceMetric, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, product_id, size_guide_views, size_selections
		FROM bq_size_confidence
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY size_guide_views DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date           string `db:"date"`
		ProductID      string `db:"product_id"`
		SizeGuideViews int64  `db:"size_guide_views"`
		SizeSelections int64  `db:"size_selections"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.SizeConfidenceMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.SizeConfidenceMetric{Date: date, ProductID: r.ProductID, SizeGuideViews: r.SizeGuideViews, SizeSelections: r.SizeSelections})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQPaymentRecovery(ctx context.Context, from, to time.Time) ([]entity.PaymentRecoveryMetric, error) {
	query := `
		SELECT date, failed_users, recovered_users
		FROM bq_payment_recovery
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC
	`
	type row struct {
		Date           string `db:"date"`
		FailedUsers    int64  `db:"failed_users"`
		RecoveredUsers int64  `db:"recovered_users"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.PaymentRecoveryMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.PaymentRecoveryMetric{Date: date, FailedUsers: r.FailedUsers, RecoveredUsers: r.RecoveredUsers})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQCheckoutTimings(ctx context.Context, from, to time.Time) ([]entity.CheckoutTimingMetric, error) {
	query := `
		SELECT date, avg_checkout_seconds, median_checkout_seconds, session_count
		FROM bq_checkout_timings
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC
	`
	type row struct {
		Date                  string  `db:"date"`
		AvgCheckoutSeconds    float64 `db:"avg_checkout_seconds"`
		MedianCheckoutSeconds float64 `db:"median_checkout_seconds"`
		SessionCount          int64   `db:"session_count"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.CheckoutTimingMetric, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.CheckoutTimingMetric{
			Date: date, AvgCheckoutSeconds: r.AvgCheckoutSeconds, MedianCheckoutSeconds: r.MedianCheckoutSeconds, SessionCount: r.SessionCount,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQTimeOnPage(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.TimeOnPageRow, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, page_path, avg_visible_time_seconds, avg_total_time_seconds, avg_engagement_score, page_views
		FROM bq_time_on_page
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY page_views DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date                  string  `db:"date"`
		PagePath              string  `db:"page_path"`
		AvgVisibleTimeSeconds float64 `db:"avg_visible_time_seconds"`
		AvgTotalTimeSeconds   float64 `db:"avg_total_time_seconds"`
		AvgEngagementScore    float64 `db:"avg_engagement_score"`
		PageViews             int64   `db:"page_views"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeOnPageRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.TimeOnPageRow{
			Date:                  date,
			PagePath:              r.PagePath,
			AvgVisibleTimeSeconds: r.AvgVisibleTimeSeconds,
			AvgTotalTimeSeconds:   r.AvgTotalTimeSeconds,
			AvgEngagementScore:    r.AvgEngagementScore,
			PageViews:             r.PageViews,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQProductZoom(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.ProductZoomRow, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, product_id, product_name, zoom_method, zoom_count
		FROM bq_product_zoom
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY zoom_count DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date        string `db:"date"`
		ProductID   string `db:"product_id"`
		ProductName string `db:"product_name"`
		ZoomMethod  string `db:"zoom_method"`
		ZoomCount   int64  `db:"zoom_count"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.ProductZoomRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.ProductZoomRow{
			Date:        date,
			ProductID:   r.ProductID,
			ProductName: r.ProductName,
			ZoomMethod:  r.ZoomMethod,
			ZoomCount:   r.ZoomCount,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQImageSwipes(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.ImageSwipeRow, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, product_id, product_name, swipe_direction, swipe_count
		FROM bq_image_swipes
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY swipe_count DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date           string `db:"date"`
		ProductID      string `db:"product_id"`
		ProductName    string `db:"product_name"`
		SwipeDirection string `db:"swipe_direction"`
		SwipeCount     int64  `db:"swipe_count"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.ImageSwipeRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.ImageSwipeRow{
			Date:           date,
			ProductID:      r.ProductID,
			ProductName:    r.ProductName,
			SwipeDirection: r.SwipeDirection,
			SwipeCount:     r.SwipeCount,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQSizeGuideClicks(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.SizeGuideClickRow, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, product_id, product_name, page_location, click_count
		FROM bq_size_guide_clicks
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY click_count DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date         string `db:"date"`
		ProductID    string `db:"product_id"`
		ProductName  string `db:"product_name"`
		PageLocation string `db:"page_location"`
		ClickCount   int64  `db:"click_count"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.SizeGuideClickRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.SizeGuideClickRow{
			Date:         date,
			ProductID:    r.ProductID,
			ProductName:  r.ProductName,
			PageLocation: r.PageLocation,
			ClickCount:   r.ClickCount,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQDetailsExpansion(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.DetailsExpansionRow, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, product_id, product_name, section_name, expand_count
		FROM bq_details_expansion
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY expand_count DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date        string `db:"date"`
		ProductID   string `db:"product_id"`
		ProductName string `db:"product_name"`
		SectionName string `db:"section_name"`
		ExpandCount int64  `db:"expand_count"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.DetailsExpansionRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.DetailsExpansionRow{
			Date:        date,
			ProductID:   r.ProductID,
			ProductName: r.ProductName,
			SectionName: r.SectionName,
			ExpandCount: r.ExpandCount,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQNotifyMeIntent(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.NotifyMeIntentRow, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, product_id, product_name, action, count, conversion_rate
		FROM bq_notify_me_intent
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY count DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date           string  `db:"date"`
		ProductID      string  `db:"product_id"`
		ProductName    string  `db:"product_name"`
		Action         string  `db:"action"`
		Count          int64   `db:"count"`
		ConversionRate float64 `db:"conversion_rate"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.NotifyMeIntentRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.NotifyMeIntentRow{
			Date:           date,
			ProductID:      r.ProductID,
			ProductName:    r.ProductName,
			Action:         r.Action,
			Count:          r.Count,
			ConversionRate: r.ConversionRate,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQAddToCartRate(ctx context.Context, from, to time.Time, granularity entity.TrendGranularity, limit, offset int) (*entity.AddToCartRateAnalysis, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	
	// Query 1: Per-product aggregates for scatter plot
	productQuery := `
		SELECT
			product_id,
			ANY_VALUE(product_name) AS product_name,
			SUM(view_count) AS view_count,
			SUM(add_to_cart_count) AS add_to_cart_count,
			CASE WHEN SUM(view_count) > 0 THEN SUM(add_to_cart_count) / SUM(view_count) ELSE 0 END AS cart_rate
		FROM bq_add_to_cart_rate
		WHERE date >= :fromDate AND date <= :toDate
		GROUP BY product_id
		HAVING SUM(view_count) > 0
		ORDER BY view_count DESC, cart_rate DESC
		LIMIT :limit OFFSET :offset
	`
	type productRow struct {
		ProductID      string  `db:"product_id"`
		ProductName    string  `db:"product_name"`
		ViewCount      int64   `db:"view_count"`
		AddToCartCount int64   `db:"add_to_cart_count"`
		CartRate       float64 `db:"cart_rate"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	productRows, err := QueryListNamed[productRow](ctx, s.DB(), productQuery, params)
	if err != nil {
		return nil, fmt.Errorf("get product aggregates: %w", err)
	}
	
	products := make([]entity.AddToCartRateProductRow, 0, len(productRows))
	var totalViews int64
	var totalAddToCarts int64
	for _, r := range productRows {
		products = append(products, entity.AddToCartRateProductRow{
			ProductID:      r.ProductID,
			ProductName:    r.ProductName,
			ViewCount:      r.ViewCount,
			AddToCartCount: r.AddToCartCount,
			CartRate:       r.CartRate,
		})
		totalViews += r.ViewCount
		totalAddToCarts += r.AddToCartCount
	}
	
	// Calculate store averages for quadrant dividing lines
	avgViewCount := int64(0)
	avgCartRate := 0.0
	if len(products) > 0 {
		avgViewCount = totalViews / int64(len(products))
		if totalViews > 0 {
			avgCartRate = float64(totalAddToCarts) / float64(totalViews)
		}
	}
	
	// Query 2: Global trend data for time series
	var trendQuery string
	switch granularity {
	case entity.TrendGranularityWeekly:
		trendQuery = `
			SELECT
				DATE_ADD(date, INTERVAL -WEEKDAY(date) DAY) AS trend_date,
				SUM(view_count) AS total_views,
				SUM(add_to_cart_count) AS total_add_to_carts,
				CASE WHEN SUM(view_count) > 0 THEN SUM(add_to_cart_count) / SUM(view_count) ELSE 0 END AS global_cart_rate
			FROM bq_add_to_cart_rate
			WHERE date >= :fromDate AND date <= :toDate
			GROUP BY trend_date
			ORDER BY trend_date
		`
	case entity.TrendGranularityMonthly:
		trendQuery = `
			SELECT
				DATE_FORMAT(date, '%Y-%m-01') AS trend_date,
				SUM(view_count) AS total_views,
				SUM(add_to_cart_count) AS total_add_to_carts,
				CASE WHEN SUM(view_count) > 0 THEN SUM(add_to_cart_count) / SUM(view_count) ELSE 0 END AS global_cart_rate
			FROM bq_add_to_cart_rate
			WHERE date >= :fromDate AND date <= :toDate
			GROUP BY trend_date
			ORDER BY trend_date
		`
	default: // Daily
		trendQuery = `
			SELECT
				date AS trend_date,
				SUM(view_count) AS total_views,
				SUM(add_to_cart_count) AS total_add_to_carts,
				CASE WHEN SUM(view_count) > 0 THEN SUM(add_to_cart_count) / SUM(view_count) ELSE 0 END AS global_cart_rate
			FROM bq_add_to_cart_rate
			WHERE date >= :fromDate AND date <= :toDate
			GROUP BY date
			ORDER BY date
		`
	}
	
	type trendRow struct {
		TrendDate       string  `db:"trend_date"`
		TotalViews      int64   `db:"total_views"`
		TotalAddToCarts int64   `db:"total_add_to_carts"`
		GlobalCartRate  float64 `db:"global_cart_rate"`
	}
	trendParams := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	trendRows, err := QueryListNamed[trendRow](ctx, s.DB(), trendQuery, trendParams)
	if err != nil {
		return nil, fmt.Errorf("get trend data: %w", err)
	}
	
	globalTrend := make([]entity.AddToCartRateGlobalRow, 0, len(trendRows))
	for _, r := range trendRows {
		date, err := parseDateStr(r.TrendDate)
		if err != nil {
			return nil, fmt.Errorf("parse trend date: %w", err)
		}
		globalTrend = append(globalTrend, entity.AddToCartRateGlobalRow{
			Date:            date,
			TotalViews:      r.TotalViews,
			TotalAddToCarts: r.TotalAddToCarts,
			GlobalCartRate:  r.GlobalCartRate,
		})
	}
	
	return &entity.AddToCartRateAnalysis{
		Products:     products,
		GlobalTrend:  globalTrend,
		AvgViewCount: avgViewCount,
		AvgCartRate:  avgCartRate,
	}, nil
}

func (s *bqCacheStoreRead) GetBQBrowserBreakdown(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.BrowserBreakdownRow, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, browser, sessions, users, conversions, conversion_rate
		FROM bq_browser_breakdown
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY sessions DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date           string  `db:"date"`
		Browser        string  `db:"browser"`
		Sessions       int64   `db:"sessions"`
		Users          int64   `db:"users"`
		Conversions    int64   `db:"conversions"`
		ConversionRate float64 `db:"conversion_rate"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.BrowserBreakdownRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.BrowserBreakdownRow{
			Date: date, Browser: r.Browser, Sessions: r.Sessions, Users: r.Users,
			Conversions: r.Conversions, ConversionRate: r.ConversionRate,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQNewsletter(ctx context.Context, from, to time.Time) ([]entity.NewsletterMetricRow, error) {
	query := `
		SELECT date, signup_count, unique_users
		FROM bq_newsletter
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC
	`
	type row struct {
		Date        string `db:"date"`
		SignupCount int64  `db:"signup_count"`
		UniqueUsers int64  `db:"unique_users"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.NewsletterMetricRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.NewsletterMetricRow{Date: date, SignupCount: r.SignupCount, UniqueUsers: r.UniqueUsers})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQAbandonedCart(ctx context.Context, from, to time.Time) ([]entity.AbandonedCartRow, error) {
	query := `
		SELECT date, carts_started, checkouts_started, abandonment_rate, avg_minutes_to_checkout, avg_minutes_to_abandon
		FROM bq_abandoned_cart
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date ASC
	`
	type row struct {
		Date                 string  `db:"date"`
		CartsStarted         int64   `db:"carts_started"`
		CheckoutsStarted     int64   `db:"checkouts_started"`
		AbandonmentRate      float64 `db:"abandonment_rate"`
		AvgMinutesToCheckout float64 `db:"avg_minutes_to_checkout"`
		AvgMinutesToAbandon  float64 `db:"avg_minutes_to_abandon"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02")}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.AbandonedCartRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.AbandonedCartRow{
			Date: date, CartsStarted: r.CartsStarted, CheckoutsStarted: r.CheckoutsStarted,
			AbandonmentRate: r.AbandonmentRate, AvgMinutesToCheckout: r.AvgMinutesToCheckout, AvgMinutesToAbandon: r.AvgMinutesToAbandon,
		})
	}
	return result, nil
}

func (s *bqCacheStoreRead) GetBQCampaignAttribution(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.CampaignAttributionRow, error) {
	page := BQPageParams{Limit: limit, Offset: offset}
	query := `
		SELECT date, utm_source, utm_medium, utm_campaign, sessions, users, conversions, revenue, conversion_rate
		FROM bq_campaign_attribution
		WHERE date >= :fromDate AND date <= :toDate
		ORDER BY date DESC, sessions DESC
		LIMIT :limit OFFSET :offset
	`
	type row struct {
		Date           string          `db:"date"`
		UTMSource      string          `db:"utm_source"`
		UTMMedium      string          `db:"utm_medium"`
		UTMCampaign    string          `db:"utm_campaign"`
		Sessions       int64           `db:"sessions"`
		Users          int64           `db:"users"`
		Conversions    int64           `db:"conversions"`
		Revenue        decimal.Decimal `db:"revenue"`
		ConversionRate float64         `db:"conversion_rate"`
	}
	params := map[string]any{"fromDate": from.Format("2006-01-02"), "toDate": to.Format("2006-01-02"), "limit": page.effectiveLimit(), "offset": page.effectiveOffset()}
	rows, err := QueryListNamed[row](ctx, s.DB(), query, params)
	if err != nil {
		return nil, err
	}
	result := make([]entity.CampaignAttributionRow, 0, len(rows))
	for _, r := range rows {
		date, err := parseDateStr(r.Date)
		if err != nil {
			return nil, err
		}
		result = append(result, entity.CampaignAttributionRow{
			Date: date, UTMSource: r.UTMSource, UTMMedium: r.UTMMedium, UTMCampaign: r.UTMCampaign,
			Sessions: r.Sessions, Users: r.Users, Conversions: r.Conversions, Revenue: r.Revenue, ConversionRate: r.ConversionRate,
		})
	}
	return result, nil
}
