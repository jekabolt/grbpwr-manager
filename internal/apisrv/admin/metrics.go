package admin

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetMetrics(ctx context.Context, req *pb_admin.GetMetricsRequest) (*pb_admin.GetMetricsResponse, error) {
	if strings.TrimSpace(req.Period) == "" {
		return nil, status.Errorf(codes.InvalidArgument, "period is required (e.g. 7d, 30d, 90d, today, WTD, MTD, QTD, YTD)")
	}
	
	endAt := time.Now().UTC()
	if req.EndAt != nil {
		endAt = req.EndAt.AsTime().UTC()
	}
	
	// Check if period is a special "to date" format first
	periodFrom, periodTo, isSpecial := computePeriodBounds(req.Period, endAt)
	var dur time.Duration
	
	if !isSpecial {
		// Parse as duration (7d, 30d, P7D, etc.)
		var err error
		dur, err = parseMetricsPeriod(req.Period)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid period %q: %v", req.Period, err)
		}
		periodTo = endAt
		periodFrom = endAt.Add(-dur)
	} else {
		// For special periods (today, WTD, etc.), compute duration for comparison periods
		dur = periodTo.Sub(periodFrom)
	}

	from, to := periodFrom, periodTo

	comparePeriod := entity.TimeRange{}
	switch req.CompareMode {
	case pb_admin.CompareMode_COMPARE_MODE_PREVIOUS_PERIOD:
		comparePeriod = entity.TimeRange{
			From: periodFrom.Add(-dur),
			To:   periodFrom,
		}
	case pb_admin.CompareMode_COMPARE_MODE_SAME_PERIOD_LAST_YEAR:
		comparePeriod = entity.TimeRange{
			From: periodFrom.AddDate(-1, 0, 0),
			To:   periodTo.AddDate(-1, 0, 0),
		}
	}

	limit := int(req.Limit)
	if limit <= 0 {
		limit = 50
	}

	sections := req.Sections
	wantAll := len(sections) == 0
	want := func(sec pb_admin.MetricsSection) bool {
		if wantAll {
			return sec == pb_admin.MetricsSection_METRICS_SECTION_BUSINESS
		}
		for _, s := range sections {
			if s == sec {
				return true
			}
		}
		return false
	}

	resp := &pb_admin.GetMetricsResponse{}

	if wantAll || want(pb_admin.MetricsSection_METRICS_SECTION_BUSINESS) {
		period := entity.TimeRange{From: periodFrom, To: periodTo}
		granularity := inferMetricsGranularity(dur)
		metrics, err := s.repo.Metrics().GetBusinessMetrics(ctx, period, comparePeriod, granularity)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get business metrics", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get business metrics")
		}
		resp.Business = dto.ConvertEntityBusinessMetricsToPb(metrics)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_FUNNEL) {
		agg, err := s.repo.BQCache().SumBQFunnelAnalysis(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get funnel aggregate", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get funnel aggregate")
		}
		daily, err := s.repo.BQCache().GetDailyBQFunnelAnalysis(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get daily funnel", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get daily funnel")
		}

		dbOrders, ga4Sessions := funnelReconciliation(ctx, s.repo, from, to)

		var caveat string
		if agg != nil && (dbOrders > int64(agg.PurchaseUsers) || ga4Sessions > 0 && ga4Sessions != agg.SessionStartUsers) {
			caveat = "Funnel purchase count is GA4 session-scoped (requires all sequential steps); " +
				"DB orders count all placed orders. Session start counts BQ raw events which may " +
				"differ from GA4 API sessions due to bot filtering and event deduplication."
		}

		resp.Funnel = &pb_admin.FunnelSection{
			Aggregate:     dto.ConvertFunnelAggregateToPb(agg),
			Daily:         dto.ConvertDailyFunnelsToPb(daily),
			DbOrdersCount: dbOrders,
			Ga4Sessions:   ga4Sessions,
			Caveat:        caveat,
		}
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_OOS_IMPACT) {
		items, err := s.repo.BQCache().GetBQOOSImpact(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get OOS impact", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get OOS impact")
		}
		resp.OosImpact = dto.ConvertOOSImpactMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_PAYMENT_FAILURES) {
		items, err := s.repo.BQCache().GetBQPaymentFailures(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get payment failures", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get payment failures")
		}
		resp.PaymentFailures = dto.ConvertPaymentFailureMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_WEB_VITALS) {
		items, err := s.repo.BQCache().GetBQWebVitals(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get web vitals", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get web vitals")
		}
		resp.WebVitals = dto.ConvertWebVitalMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_USER_JOURNEYS) {
		items, err := s.repo.BQCache().GetBQUserJourneys(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get user journeys", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get user journeys")
		}
		resp.UserJourneys = dto.ConvertUserJourneyMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_SESSION_DURATION) {
		items, err := s.repo.BQCache().GetBQSessionDuration(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get session duration", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get session duration")
		}
		resp.SessionDuration = dto.ConvertSessionDurationMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_DEVICE_FUNNEL) {
		items, err := s.repo.BQCache().GetBQDeviceFunnel(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get device funnel", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get device funnel")
		}
		resp.DeviceFunnel = dto.ConvertDeviceFunnelMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_PRODUCT_ENGAGEMENT) {
		items, err := s.repo.BQCache().GetBQProductEngagement(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get product engagement", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get product engagement")
		}
		resp.ProductEngagement = dto.ConvertProductEngagementMetricsToPb(items)
		resp.ProductEngagementBubbleMatrix = dto.ConvertProductEngagementBubbleMatrixToPb(
			buildBubbleMatrix(items),
		)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_FORM_ERRORS) {
		items, err := s.repo.BQCache().GetBQFormErrors(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get form errors", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get form errors")
		}
		resp.FormErrors = dto.ConvertFormErrorMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_EXCEPTIONS) {
		items, err := s.repo.BQCache().GetBQExceptions(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get exceptions", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get exceptions")
		}
		resp.Exceptions = dto.ConvertExceptionMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_NOT_FOUND) {
		items, err := s.repo.BQCache().GetBQNotFoundPages(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get 404 pages", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get 404 pages")
		}
		resp.NotFound = dto.ConvertNotFoundMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_HERO_FUNNEL) {
		items, err := s.repo.BQCache().GetBQHeroFunnel(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get hero funnel", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get hero funnel")
		}
		resp.HeroFunnel = dto.ConvertHeroFunnelMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_SIZE_CONFIDENCE) {
		items, err := s.repo.BQCache().GetBQSizeConfidence(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get size confidence", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get size confidence")
		}
		resp.SizeConfidence = dto.ConvertSizeConfidenceMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_PAYMENT_RECOVERY) {
		items, err := s.repo.BQCache().GetBQPaymentRecovery(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get payment recovery", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get payment recovery")
		}
		resp.PaymentRecovery = dto.ConvertPaymentRecoveryMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_CHECKOUT_TIMINGS) {
		items, err := s.repo.BQCache().GetBQCheckoutTimings(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get checkout timings", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get checkout timings")
		}
		resp.CheckoutTimings = dto.ConvertCheckoutTimingMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_COHORT_RETENTION) {
		items, err := s.repo.Metrics().GetCohortRetention(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get cohort retention", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get cohort retention")
		}
		resp.CohortRetention = dto.ConvertCohortRetentionToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_ORDER_SEQUENCE) {
		items, err := s.repo.Metrics().GetOrderSequenceMetrics(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get order sequence metrics", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get order sequence metrics")
		}
		resp.OrderSequence = dto.ConvertOrderSequenceMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_ENTRY_PRODUCTS) {
		items, err := s.repo.Metrics().GetEntryProducts(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get entry products", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get entry products")
		}
		resp.EntryProducts = dto.ConvertEntryProductMetricsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_REVENUE_PARETO) {
		items, err := s.repo.Metrics().GetRevenuePareto(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get revenue pareto", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get revenue pareto")
		}
		resp.RevenuePareto = dto.ConvertRevenueParetoToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_SPENDING_CURVE) {
		items, err := s.repo.Metrics().GetCustomerSpendingCurve(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get spending curve", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get spending curve")
		}
		resp.SpendingCurve = dto.ConvertSpendingCurveToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_CATEGORY_LOYALTY) {
		items, err := s.repo.Metrics().GetCategoryLoyalty(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get category loyalty", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get category loyalty")
		}
		resp.CategoryLoyalty = dto.ConvertCategoryLoyaltyToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_INVENTORY_HEALTH) {
		items, err := s.repo.Metrics().GetInventoryHealth(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get inventory health", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get inventory health")
		}
		resp.InventoryHealth = dto.ConvertInventoryHealthToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_SIZE_RUN_EFFICIENCY) {
		items, err := s.repo.Metrics().GetSizeRunEfficiency(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get size run efficiency", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get size run efficiency")
		}
		slog.Default().DebugContext(ctx, "size run efficiency query",
			slog.Time("from", from),
			slog.Time("to", to),
			slog.Int("limit", limit),
			slog.Int("result_count", len(items)))
		resp.SizeRunEfficiency = dto.ConvertSizeRunEfficiencyToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_SLOW_MOVERS) {
		items, err := s.repo.Metrics().GetSlowMovers(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get slow movers", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get slow movers")
		}
		resp.SlowMovers = dto.ConvertSlowMoversToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_RETURN_ANALYSIS) {
		byProduct, err := s.repo.Metrics().GetReturnByProduct(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get return by product", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get return by product")
		}
		resp.ReturnByProduct = dto.ConvertReturnByProductToPb(byProduct)

		bySize, err := s.repo.Metrics().GetReturnBySize(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get return by size", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get return by size")
		}
		resp.ReturnBySize = dto.ConvertReturnBySizeToPb(bySize)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_SIZE_ANALYTICS) {
		items, err := s.repo.Metrics().GetSizeAnalytics(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get size analytics", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get size analytics")
		}
		resp.SizeAnalytics = dto.ConvertSizeAnalyticsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_DEAD_STOCK) {
		items, err := s.repo.Metrics().GetDeadStock(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get dead stock", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get dead stock")
		}
		resp.DeadStock = dto.ConvertDeadStockToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_PRODUCT_TREND) {
		items, err := s.repo.Metrics().GetProductTrend(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get product trend", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get product trend")
		}
		resp.ProductTrend = dto.ConvertProductTrendToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_TIME_ON_PAGE) {
		items, err := s.repo.BQCache().GetBQTimeOnPage(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get time on page", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get time on page")
		}
		resp.TimeOnPage = dto.ConvertTimeOnPageToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_PRODUCT_ZOOM) {
		items, err := s.repo.BQCache().GetBQProductZoom(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get product zoom", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get product zoom")
		}
		resp.ProductZoom = dto.ConvertProductZoomToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_IMAGE_SWIPES) {
		items, err := s.repo.BQCache().GetBQImageSwipes(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get image swipes", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get image swipes")
		}
		resp.ImageSwipes = dto.ConvertImageSwipesToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_SIZE_GUIDE_CLICKS) {
		items, err := s.repo.BQCache().GetBQSizeGuideClicks(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get size guide clicks", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get size guide clicks")
		}
		resp.SizeGuideClicks = dto.ConvertSizeGuideClicksToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_DETAILS_EXPANSION) {
		items, err := s.repo.BQCache().GetBQDetailsExpansion(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get details expansion", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get details expansion")
		}
		resp.DetailsExpansion = dto.ConvertDetailsExpansionToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_NOTIFY_ME_INTENT) {
		items, err := s.repo.BQCache().GetBQNotifyMeIntent(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get notify me intent", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get notify me intent")
		}
		resp.NotifyMeIntent = dto.ConvertNotifyMeIntentToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_GEOGRAPHY) {
		byCountry, err := s.repo.Metrics().GetRevenueByCountry(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get geography revenue by country", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get geography metrics")
		}
		sessions, err := s.repo.GA4Data().GetGA4SessionsByCountry(ctx, from, to, 50)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get geography sessions by country", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get geography metrics")
		}
		resp.Geography = dto.ConvertGeographyToPb(byCountry, sessions)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_ADD_TO_CART_RATE) {
		// Convert protobuf granularity to entity granularity
		granularity := entity.TrendGranularityDaily
		switch req.TrendGranularity {
		case pb_admin.TrendGranularity_TREND_GRANULARITY_WEEKLY:
			granularity = entity.TrendGranularityWeekly
		case pb_admin.TrendGranularity_TREND_GRANULARITY_MONTHLY:
			granularity = entity.TrendGranularityMonthly
		}

		analysis, err := s.repo.BQCache().GetBQAddToCartRate(ctx, from, to, granularity, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get add to cart rate", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get add to cart rate")
		}
		resp.AddToCartRateAnalysis = dto.ConvertAddToCartRateAnalysisToPb(analysis)

		// For backwards compatibility, also populate the old field with product data
		if analysis != nil && len(analysis.Products) > 0 {
			legacyRows := make([]entity.AddToCartRateRow, 0, len(analysis.Products))
			for _, p := range analysis.Products {
				legacyRows = append(legacyRows, entity.AddToCartRateRow{
					Date:           from,
					ProductID:      p.ProductID,
					ProductName:    p.ProductName,
					ViewCount:      p.ViewCount,
					AddToCartCount: p.AddToCartCount,
					CartRate:       p.CartRate,
				})
			}
			resp.AddToCartRate = dto.ConvertAddToCartRateToPb(legacyRows)
		}
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_BROWSER_BREAKDOWN) {
		items, err := s.repo.BQCache().GetBQBrowserBreakdown(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get browser breakdown", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get browser breakdown")
		}
		resp.BrowserBreakdown = dto.ConvertBrowserBreakdownToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_NEWSLETTER) {
		items, err := s.repo.BQCache().GetBQNewsletter(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get newsletter metrics", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get newsletter metrics")
		}
		resp.Newsletter = dto.ConvertNewsletterToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_ABANDONED_CART) {
		items, err := s.repo.BQCache().GetBQAbandonedCart(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get abandoned cart", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get abandoned cart")
		}
		resp.AbandonedCart = dto.ConvertAbandonedCartToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_CAMPAIGN_ATTRIBUTION) {
		items, err := s.repo.BQCache().GetBQCampaignAttributionAggregated(ctx, from, to, limit, 0)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get campaign attribution", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get campaign attribution")
		}
		resp.CampaignAttribution = dto.ConvertCampaignAttributionAggregatedToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_CUSTOMER_SEGMENTATION) {
		items, err := s.repo.Metrics().GetCustomerSegmentation(ctx, from, to)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "can't get customer segmentation")
		}
		resp.CustomerSegments = dto.ConvertCustomerSegmentationToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_RFM) {
		items, err := s.repo.Metrics().GetRFMAnalysis(ctx, from, to)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "can't get RFM analysis")
		}
		resp.RfmAnalysis = dto.ConvertRFMAnalysisToPb(items)
	}

	return resp, nil
}

// buildBubbleMatrix aggregates per-product engagement rows into a bubble-chart matrix
// with percentage rates and an overall summary.
func buildBubbleMatrix(items []entity.ProductEngagementMetric) *entity.ProductEngagementBubbleMatrix {
	if len(items) == 0 {
		return nil
	}

	type agg struct {
		name       string
		views      int64
		zooms      int64
		scroll75   int64
		scroll100  int64
		timeOnPage float64
		timeCount  int
	}

	byProduct := make(map[string]*agg)
	for _, m := range items {
		a, ok := byProduct[m.ProductID]
		if !ok {
			a = &agg{name: m.ProductName}
			byProduct[m.ProductID] = a
		}
		a.views += m.ImageViews
		a.zooms += m.ZoomEvents
		a.scroll75 += m.Scroll75
		a.scroll100 += m.Scroll100
		if m.AvgTimeOnPageSeconds > 0 {
			a.timeOnPage += m.AvgTimeOnPageSeconds
			a.timeCount++
		}
	}

	pctSafe := func(part, total int64) float64 {
		if total == 0 {
			return 0
		}
		return float64(part) / float64(total) * 100
	}

	rows := make([]entity.ProductEngagementBubbleRow, 0, len(byProduct))
	var totalViews, totalZooms, totalS75, totalS100 int64
	var totalTime float64
	var timeProducts int

	for pid, a := range byProduct {
		avgTime := 0.0
		if a.timeCount > 0 {
			avgTime = a.timeOnPage / float64(a.timeCount)
		}
		rows = append(rows, entity.ProductEngagementBubbleRow{
			ProductID:            pid,
			ProductName:          a.name,
			TotalImageViews:      a.views,
			TotalZoomEvents:      a.zooms,
			TotalScroll75:        a.scroll75,
			TotalScroll100:       a.scroll100,
			ZoomRatePct:          pctSafe(a.zooms, a.views),
			Scroll75RatePct:      pctSafe(a.scroll75, a.views),
			Scroll100RatePct:     pctSafe(a.scroll100, a.views),
			AvgTimeOnPageSeconds: avgTime,
		})
		totalViews += a.views
		totalZooms += a.zooms
		totalS75 += a.scroll75
		totalS100 += a.scroll100
		if avgTime > 0 {
			totalTime += avgTime
			timeProducts++
		}
	}

	avgOverallTime := 0.0
	if timeProducts > 0 {
		avgOverallTime = totalTime / float64(timeProducts)
	}

	return &entity.ProductEngagementBubbleMatrix{
		Rows: rows,
		Overall: entity.ProductEngagementMetricsPct{
			AvgZoomRatePct:       pctSafe(totalZooms, totalViews),
			AvgScroll75RatePct:   pctSafe(totalS75, totalViews),
			AvgScroll100RatePct:  pctSafe(totalS100, totalViews),
			AvgTimeOnPageSeconds: avgOverallTime,
		},
	}
}

// funnelReconciliation fetches DB order count and GA4 session count for the funnel
// period so the UI can reconcile with BQ-sourced funnel metrics. Errors are logged
// but non-fatal — zero values are returned on failure.
func funnelReconciliation(ctx context.Context, repo dependency.Repository, from, to time.Time) (dbOrders, ga4Sessions int64) {
	if cnt, err := repo.Metrics().GetPeriodOrderCount(ctx, from, to); err != nil {
		slog.Default().WarnContext(ctx, "funnel: failed to get DB order count", slog.String("err", err.Error()))
	} else {
		dbOrders = int64(cnt)
	}

	ga4Daily, err := repo.GA4Data().GetGA4DailyMetrics(ctx, from, to)
	if err != nil {
		slog.Default().WarnContext(ctx, "funnel: failed to get GA4 daily metrics", slog.String("err", err.Error()))
	} else {
		for _, d := range ga4Daily {
			ga4Sessions += int64(d.Sessions)
		}
	}
	return dbOrders, ga4Sessions
}

// parseMetricsPeriod parses period strings into time.Duration or special flags.
// Supports:
// - Shorthand: "7d", "30d", "90d" (N days back from end)
// - ISO8601: "P7D", "P30D" (N days back)
// - Today: "today", "1d" (start of today to now)
// - WTD/MTD/QTD/YTD: "WTD", "MTD", "QTD", "YTD" (week/month/quarter/year to date)
//
// Note: GetMetrics calls computePeriodBounds first, so the special-period branch below
// is unreachable in normal flows. Kept as a safety net for direct callers.
func parseMetricsPeriod(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	sl := strings.ToLower(s)
	
	// Special: today or "to date" periods — handled in GetMetrics via computePeriodBounds
	// This branch is unreachable from GetMetrics but preserved for direct callers.
	if sl == "today" || sl == "1d" || sl == "wtd" || sl == "mtd" || sl == "qtd" || sl == "ytd" {
		return 0, nil // signal: use computePeriodBounds
	}
	
	// Shorthand: Nd
	if m := regexp.MustCompile(`^(\d+)[dD]$`).FindStringSubmatch(s); len(m) == 2 {
		days, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("invalid days value: %w", err)
		}
		if days < 1 || days > 365 {
			return 0, fmt.Errorf("days must be 1-365")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	// ISO8601: P{n}D
	if m := regexp.MustCompile(`^[pP](\d+)[dD]$`).FindStringSubmatch(s); len(m) == 2 {
		days, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("invalid days value: %w", err)
		}
		if days < 1 || days > 365 {
			return 0, fmt.Errorf("days must be 1-365")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("expected format: 7d, 30d, today, WTD, MTD, QTD, YTD, or P7D, P30D")
}

// computePeriodBounds returns (from, to) for special period strings like "today", "WTD", "MTD", "QTD", "YTD".
// Returns (zero, zero, false) if the period is not a special case.
func computePeriodBounds(period string, endAt time.Time) (from, to time.Time, isSpecial bool) {
	pl := strings.ToLower(strings.TrimSpace(period))
	now := endAt
	
	switch pl {
	case "today", "1d":
		// Start of today (00:00:00) to now
		from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		to = now
		return from, to, true
		
	case "wtd": // Week to date (Monday 00:00 to now)
		weekday := now.Weekday()
		daysFromMonday := int(weekday - time.Monday)
		if daysFromMonday < 0 {
			daysFromMonday += 7 // Sunday is 0, Monday is 1
		}
		from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -daysFromMonday)
		to = now
		return from, to, true
		
	case "mtd": // Month to date (1st of month 00:00 to now)
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		to = now
		return from, to, true
		
	case "qtd": // Quarter to date (start of Q1/Q2/Q3/Q4 to now)
		quarter := int((now.Month()-1)/3) + 1
		quarterStartMonth := time.Month((quarter-1)*3 + 1)
		from = time.Date(now.Year(), quarterStartMonth, 1, 0, 0, 0, 0, time.UTC)
		to = now
		return from, to, true
		
	case "ytd": // Year to date (Jan 1 00:00 to now)
		from = time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
		to = now
		return from, to, true
	}
	
	return time.Time{}, time.Time{}, false
}

// inferMetricsGranularity derives granularity from period length: <=14d→day, 15-90d→week, >90d→month.
func inferMetricsGranularity(dur time.Duration) entity.MetricsGranularity {
	days := int(dur / (24 * time.Hour))
	if days <= 14 {
		return entity.MetricsGranularityDay
	}
	if days <= 90 {
		return entity.MetricsGranularityWeek
	}
	return entity.MetricsGranularityMonth
}
