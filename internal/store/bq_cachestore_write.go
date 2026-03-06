package store

import (
	"context"
	"crypto/md5"
	"fmt"
	"time"

	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type bqCacheStoreWrite struct {
	*MYSQLStore
}

// funnelAnalysisCols matches bq_funnel_analysis table column order.
var funnelAnalysisCols = []string{"date", "session_start_users", "view_item_list_users", "select_item_users",
	"view_item_users", "size_selected_users", "add_to_cart_users",
	"begin_checkout_users", "add_shipping_info_users", "add_payment_info_users", "purchase_users"}

func funnelAnalysisArgs(rows []entity.DailyFunnel) [][]any {
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.SessionStartUsers, r.ViewItemListUsers, r.SelectItemUsers,
			r.ViewItemUsers, r.SizeSelectedUsers, r.AddToCartUsers,
			r.BeginCheckoutUsers, r.AddShippingInfoUsers, r.AddPaymentInfoUsers, r.PurchaseUsers,
		})
	}
	return args
}

func (s *bqCacheStoreWrite) DeleteBQFunnelAnalysisByDateRange(ctx context.Context, startDate, endDate time.Time) error {
	_, err := s.DB().ExecContext(ctx,
		"DELETE FROM bq_funnel_analysis WHERE date >= ? AND date <= ?",
		startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
	if err != nil {
		return fmt.Errorf("delete bq funnel analysis by date range: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) InsertBQFunnelAnalysisBatch(ctx context.Context, rows []entity.DailyFunnel) error {
	if len(rows) == 0 {
		return nil
	}
	args := funnelAnalysisArgs(rows)
	if err := BulkInsertRows(ctx, s.DB(), "bq_funnel_analysis", funnelAnalysisCols, args); err != nil {
		return fmt.Errorf("insert bq funnel analysis batch: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQFunnelAnalysis(ctx context.Context, rows []entity.DailyFunnel) error {
	if len(rows) == 0 {
		return nil
	}
	args := funnelAnalysisArgs(rows)
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_funnel_analysis", funnelAnalysisCols, args); err != nil {
		return fmt.Errorf("save bq funnel analysis: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQOOSImpact(ctx context.Context, rows []entity.OOSImpactMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "product_id", "product_name", "size_id", "size_name", "product_price", "currency", "click_count", "estimated_lost_sales", "estimated_lost_revenue"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.ProductID, r.ProductName, r.SizeID, r.SizeName,
			r.ProductPrice, r.Currency, r.ClickCount, r.EstimatedLostSales, r.EstimatedLostRevenue,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_oos_impact", cols, args); err != nil {
		return fmt.Errorf("save bq oos impact: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQPaymentFailures(ctx context.Context, rows []entity.PaymentFailureMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "error_code", "payment_type", "failure_count", "total_failed_value", "avg_failed_order_value"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.ErrorCode, r.PaymentType, r.FailureCount,
			r.TotalFailedValue, r.AvgFailedOrderValue,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_payment_failures", cols, args); err != nil {
		return fmt.Errorf("save bq payment failures: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQWebVitals(ctx context.Context, rows []entity.WebVitalMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "metric_name", "metric_rating", "session_count", "conversions", "avg_metric_value"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.MetricName, r.MetricRating, r.SessionCount,
			r.Conversions, r.AvgMetricValue,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_web_vitals", cols, args); err != nil {
		return fmt.Errorf("save bq web vitals: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQUserJourneys(ctx context.Context, rows []entity.UserJourneyMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "journey_path", "session_count", "conversions", "path_hash"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		hash := fmt.Sprintf("%x", md5.Sum([]byte(r.JourneyPath)))
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.JourneyPath, r.SessionCount, r.Conversions, hash,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_user_journeys", cols, args); err != nil {
		return fmt.Errorf("save bq user journeys: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQSessionDuration(ctx context.Context, rows []entity.SessionDurationMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "avg_time_between_events_seconds", "median_time_between_events"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.AvgTimeBetweenEventsSeconds, r.MedianTimeBetweenEvents,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_session_duration", cols, args); err != nil {
		return fmt.Errorf("save bq session duration: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQSizeIntent(ctx context.Context, rows []bq.SizeIntentRow) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "product_id", "size_id", "size_name", "size_clicks"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.ProductID, r.SizeID, r.SizeName, r.SizeClicks,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_size_intent", cols, args); err != nil {
		return fmt.Errorf("save bq size intent: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQDeviceFunnel(ctx context.Context, rows []entity.DeviceFunnelMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "device_category", "sessions", "add_to_cart_users", "checkout_users", "purchase_users"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.DeviceCategory, r.Sessions,
			r.AddToCartUsers, r.CheckoutUsers, r.PurchaseUsers,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_device_funnel", cols, args); err != nil {
		return fmt.Errorf("save bq device funnel: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQProductEngagement(ctx context.Context, rows []entity.ProductEngagementMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "product_id", "product_name", "image_views", "zoom_events", "scroll_75", "scroll_100"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.ProductID, r.ProductName, r.ImageViews,
			r.ZoomEvents, r.Scroll75, r.Scroll100,
		})
	}
	keyCols := []string{"date", "product_id"}
	if err := BulkUpsertByDate(ctx, s.DB(), "bq_product_engagement", cols, keyCols, args); err != nil {
		return fmt.Errorf("save bq product engagement: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQFormErrors(ctx context.Context, rows []entity.FormErrorMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "field_name", "error_count"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{r.Date.Format("2006-01-02"), r.FieldName, r.ErrorCount})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_form_errors", cols, args); err != nil {
		return fmt.Errorf("save bq form errors: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQExceptions(ctx context.Context, rows []entity.ExceptionMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "page_path", "exception_count", "description", "path_desc_hash"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		path := truncatePagePath(r.PagePath)
		hash := fmt.Sprintf("%x", md5.Sum([]byte(path+"\x00"+r.Description)))
		args = append(args, []any{
			r.Date.Format("2006-01-02"), path, r.ExceptionCount, r.Description, hash,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_exceptions", cols, args); err != nil {
		return fmt.Errorf("save bq exceptions: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQNotFoundPages(ctx context.Context, rows []entity.NotFoundMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "page_path", "path_hash", "hit_count"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		path := truncatePagePath(r.PagePath)
		hash := fmt.Sprintf("%x", md5.Sum([]byte(path)))
		args = append(args, []any{r.Date.Format("2006-01-02"), path, hash, r.HitCount})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_not_found_pages", cols, args); err != nil {
		return fmt.Errorf("save bq not found pages: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQHeroFunnel(ctx context.Context, rows []entity.HeroFunnelMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "hero_click_users", "view_item_users", "purchase_users"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.HeroClickUsers, r.ViewItemUsers, r.PurchaseUsers,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_hero_funnel", cols, args); err != nil {
		return fmt.Errorf("save bq hero funnel: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQSizeConfidence(ctx context.Context, rows []entity.SizeConfidenceMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "product_id", "size_guide_views", "size_selections"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.ProductID, r.SizeGuideViews, r.SizeSelections,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_size_confidence", cols, args); err != nil {
		return fmt.Errorf("save bq size confidence: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQPaymentRecovery(ctx context.Context, rows []entity.PaymentRecoveryMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "failed_users", "recovered_users"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{r.Date.Format("2006-01-02"), r.FailedUsers, r.RecoveredUsers})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_payment_recovery", cols, args); err != nil {
		return fmt.Errorf("save bq payment recovery: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQCheckoutTimings(ctx context.Context, rows []entity.CheckoutTimingMetric) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "avg_checkout_seconds", "median_checkout_seconds", "session_count"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.AvgCheckoutSeconds, r.MedianCheckoutSeconds, r.SessionCount,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_checkout_timings", cols, args); err != nil {
		return fmt.Errorf("save bq checkout timings: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQScrollDepth(ctx context.Context, rows []entity.ScrollDepthRow) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "page_type", "scroll_25", "scroll_50", "scroll_75", "scroll_100", "total_users"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.PageType, r.Scroll25, r.Scroll50,
			r.Scroll75, r.Scroll100, r.TotalUsers,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_scroll_depth", cols, args); err != nil {
		return fmt.Errorf("save bq scroll depth: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQAddToCartRate(ctx context.Context, rows []entity.AddToCartRateRow) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "product_id", "product_name", "view_count", "add_to_cart_count", "cart_rate"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.ProductID, r.ProductName, r.ViewCount,
			r.AddToCartCount, r.CartRate,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_add_to_cart_rate", cols, args); err != nil {
		return fmt.Errorf("save bq add to cart rate: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQBrowserBreakdown(ctx context.Context, rows []entity.BrowserBreakdownRow) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "browser", "sessions", "users", "conversions", "conversion_rate"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.Browser, r.Sessions, r.Users,
			r.Conversions, r.ConversionRate,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_browser_breakdown", cols, args); err != nil {
		return fmt.Errorf("save bq browser breakdown: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQNewsletter(ctx context.Context, rows []entity.NewsletterMetricRow) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "signup_count", "unique_users"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{r.Date.Format("2006-01-02"), r.SignupCount, r.UniqueUsers})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_newsletter", cols, args); err != nil {
		return fmt.Errorf("save bq newsletter: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQAbandonedCart(ctx context.Context, rows []entity.AbandonedCartRow) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "carts_started", "checkouts_started", "abandonment_rate", "avg_minutes_to_checkout", "avg_minutes_to_abandon"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.CartsStarted, r.CheckoutsStarted,
			r.AbandonmentRate, r.AvgMinutesToCheckout, r.AvgMinutesToAbandon,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_abandoned_cart", cols, args); err != nil {
		return fmt.Errorf("save bq abandoned cart: %w", err)
	}
	return nil
}

func (s *bqCacheStoreWrite) SaveBQCampaignAttribution(ctx context.Context, rows []entity.CampaignAttributionRow) error {
	if len(rows) == 0 {
		return nil
	}
	cols := []string{"date", "utm_source", "utm_medium", "utm_campaign", "sessions", "users", "conversions", "revenue", "conversion_rate"}
	args := make([][]any, 0, len(rows))
	for _, r := range rows {
		args = append(args, []any{
			r.Date.Format("2006-01-02"), r.UTMSource, r.UTMMedium, r.UTMCampaign,
			r.Sessions, r.Users, r.Conversions, r.Revenue, r.ConversionRate,
		})
	}
	if err := BulkReplaceByDate(ctx, s.DB(), "bq_campaign_attribution", cols, args); err != nil {
		return fmt.Errorf("save bq campaign attribution: %w", err)
	}
	return nil
}
