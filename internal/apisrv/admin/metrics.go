package admin

import (
	"context"
	"database/sql"
	"errors"
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
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
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

	if want(pb_admin.MetricsSection_METRICS_SECTION_SELL_THROUGH_BY_DROP) {
		items, err := s.repo.Metrics().GetSellThroughByDrop(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get sell-through by drop", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get sell-through by drop")
		}
		resp.SellThroughByDrop = dto.ConvertSellThroughByDropToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_MARGIN_BY_STYLE) {
		items, err := s.repo.Metrics().GetMarginByStyle(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get margin by style", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get margin by style")
		}
		resp.MarginByStyle = dto.ConvertMarginByStyleToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_COGS_STRUCTURE) {
		items, err := s.repo.Metrics().GetCogsStructure(ctx, from, to)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get cogs structure", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get cogs structure")
		}
		resp.CogsStructure = dto.ConvertCogsStructureToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_INVENTORY_VALUATION) {
		v, err := s.repo.Metrics().GetInventoryValuation(ctx, from, to, limit)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get inventory valuation", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get inventory valuation")
		}
		resp.InventoryValuation = dto.ConvertInventoryValuationToPb(v)
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
		// Enrich with operator-entered spend + ROAS (non-fatal: attribution still renders
		// without spend if the join fails, e.g. before the channel_spend migration).
		if err := s.enrichCampaignSpend(ctx, from, to, items); err != nil {
			slog.Default().WarnContext(ctx, "can't enrich campaign spend", slog.String("err", err.Error()))
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

	if want(pb_admin.MetricsSection_METRICS_SECTION_ORDER_VALUE_BANDS) {
		items, err := s.repo.Metrics().GetOrderValueBands(ctx, from, to)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "can't get order value bands")
		}
		resp.OrderValueBands = dto.ConvertOrderValueBandsToPb(items)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_DELIVERY) {
		d, err := s.repo.Metrics().GetDeliveryMetrics(ctx, from, to)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "can't get delivery metrics")
		}
		resp.Delivery = dto.ConvertDeliverySectionToPb(d)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_FORECAST) {
		// Anchored to the calendar month of the period end (to), not the requested range.
		fc, err := s.repo.Metrics().GetRevenueForecast(ctx, to)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "can't get revenue forecast")
		}
		resp.RevenueForecast = dto.ConvertRevenueForecastToPb(fc)
	}

	if want(pb_admin.MetricsSection_METRICS_SECTION_PROFITABILITY) {
		period := entity.TimeRange{From: periodFrom, To: periodTo}
		prof, err := s.repo.Metrics().GetProfitability(ctx, period, comparePeriod)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get profitability", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get profitability")
		}
		resp.Profitability = dto.ConvertProfitabilitySectionToPb(prof)
	}

	// Redact confidential cost/margin sections for accounts without costing:read (task 19).
	// Commerce, traffic and email metrics remain visible.
	if read, _ := s.costingAccess(ctx); !read {
		stripMetricsCosting(resp)
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
// Metric-period parsers, compiled once at package init rather than per call.
var (
	metricsPeriodDaysRe    = regexp.MustCompile(`^(\d+)[dD]$`)
	metricsPeriodISODaysRe = regexp.MustCompile(`^[pP](\d+)[dD]$`)
)

func parseMetricsPeriod(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	sl := strings.ToLower(s)

	// Special: today or "to date" periods — handled in GetMetrics via computePeriodBounds
	// This branch is unreachable from GetMetrics but preserved for direct callers.
	if sl == "today" || sl == "1d" || sl == "wtd" || sl == "mtd" || sl == "qtd" || sl == "ytd" {
		return 0, nil // signal: use computePeriodBounds
	}

	// Shorthand: Nd
	if m := metricsPeriodDaysRe.FindStringSubmatch(s); len(m) == 2 {
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
	if m := metricsPeriodISODaysRe.FindStringSubmatch(s); len(m) == 2 {
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

// UpsertInventoryTargets sets per-SKU reorder targets used by the inventory-health metrics.
func (s *Server) UpsertInventoryTargets(ctx context.Context, req *pb_admin.UpsertInventoryTargetsRequest) (*pb_admin.UpsertInventoryTargetsResponse, error) {
	for _, t := range req.GetTargets() {
		if t.GetProductId() <= 0 || t.GetSizeId() <= 0 {
			return nil, status.Error(codes.InvalidArgument, "each inventory target needs a positive product_id and size_id")
		}
	}
	targets := dto.ConvertPbInventoryTargetsToEntity(req.GetTargets())
	if len(targets) == 0 {
		return &pb_admin.UpsertInventoryTargetsResponse{}, nil
	}
	if err := s.repo.Metrics().UpsertInventoryTargets(ctx, targets); err != nil {
		slog.Default().ErrorContext(ctx, "can't upsert inventory targets", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't upsert inventory targets")
	}
	return &pb_admin.UpsertInventoryTargetsResponse{}, nil
}

// UpsertChannelSpend records operator-entered marketing spend per channel per day.
func (s *Server) UpsertChannelSpend(ctx context.Context, req *pb_admin.UpsertChannelSpendRequest) (*pb_admin.UpsertChannelSpendResponse, error) {
	rows, err := dto.ConvertPbChannelSpendToEntity(req.GetSpend())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if len(rows) == 0 {
		return &pb_admin.UpsertChannelSpendResponse{}, nil
	}
	if err := s.repo.BQCache().UpsertChannelSpend(ctx, rows); err != nil {
		slog.Default().ErrorContext(ctx, "can't upsert channel spend", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't upsert channel spend")
	}
	return &pb_admin.UpsertChannelSpendResponse{}, nil
}

// UpsertOpexEntries records the fixed-cost (OPEX) journal that feeds the dashboard operating
// result (task 22). Month/category/amount are validated and normalised in dto; upsert is on
// (month, category).
func (s *Server) UpsertOpexEntries(ctx context.Context, req *pb_admin.UpsertOpexEntriesRequest) (*pb_admin.UpsertOpexEntriesResponse, error) {
	rows, err := dto.ConvertPbOpexEntriesToEntity(req.GetEntries())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if len(rows) == 0 {
		return &pb_admin.UpsertOpexEntriesResponse{}, nil
	}
	if err := s.repo.Metrics().UpsertOpexEntries(ctx, rows); err != nil {
		slog.Default().ErrorContext(ctx, "can't upsert opex entries", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't upsert opex entries")
	}
	return &pb_admin.UpsertOpexEntriesResponse{}, nil
}

// UpsertOpexLines writes OPEX line items (NF-08), folding each amount into base currency via the
// costing FX before storage. OPEX figures are confidential cost data, so — like dev expenses — the
// detailed line API is gated by costing:write on top of the analytics section (the legacy aggregate
// UpsertOpexEntries stays analytics-only for backward compatibility).
func (s *Server) UpsertOpexLines(ctx context.Context, req *pb_admin.UpsertOpexLinesRequest) (*pb_admin.UpsertOpexLinesResponse, error) {
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to edit OPEX lines")
	}
	lines, err := dto.ConvertPbOpexLinesToEntity(req.GetLines())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if len(lines) == 0 {
		return &pb_admin.UpsertOpexLinesResponse{}, nil
	}
	dto.FoldOpexLinesToBase(lines, s.costingFx(ctx))
	if err := s.repo.Metrics().UpsertOpexLines(ctx, lines); err != nil {
		slog.Default().ErrorContext(ctx, "can't upsert opex lines", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't upsert opex lines")
	}
	return &pb_admin.UpsertOpexLinesResponse{}, nil
}

// DeleteOpexLine removes one OPEX line by id.
func (s *Server) DeleteOpexLine(ctx context.Context, req *pb_admin.DeleteOpexLineRequest) (*pb_admin.DeleteOpexLineResponse, error) {
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to delete an OPEX line")
	}
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.Metrics().DeleteOpexLine(ctx, int(req.GetId())); err != nil {
		if errors.Is(err, entity.ErrOpexLineMaterialised) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't delete opex line", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete opex line")
	}
	return &pb_admin.DeleteOpexLineResponse{}, nil
}

// ListOpexLines returns OPEX lines in a month range. The whole response is confidential cost data,
// so without costing:read it is denied outright (PermissionDenied) rather than shaped to an empty
// success — an empty journal is indistinguishable from "no costs entered" and would bait an
// analytics-only operator into re-entering data that only fails on write (nf08-06).
func (s *Server) ListOpexLines(ctx context.Context, req *pb_admin.ListOpexLinesRequest) (*pb_admin.ListOpexLinesResponse, error) {
	if read, _ := s.costingAccess(ctx); !read {
		return nil, status.Error(codes.PermissionDenied, "costing:read is required to view OPEX lines")
	}
	filter, err := dto.ConvertOpexLineFilter(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	lines, err := s.repo.Metrics().ListOpexLines(ctx, filter)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list opex lines", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list opex lines")
	}
	return &pb_admin.ListOpexLinesResponse{Lines: dto.OpexLinesToPb(lines)}, nil
}

// UpsertOpexRecurring inserts (id==0) or updates a recurring OPEX template.
func (s *Server) UpsertOpexRecurring(ctx context.Context, req *pb_admin.UpsertOpexRecurringRequest) (*pb_admin.UpsertOpexRecurringResponse, error) {
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to edit recurring OPEX")
	}
	ins, err := dto.ConvertPbOpexRecurringToEntity(req.GetRecurring())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	id, err := s.repo.Metrics().UpsertOpexRecurring(ctx, ins, int(req.GetId()))
	if err != nil {
		// a bogus employee_id fails the fk_opex_rec_employee FK — a caller-fixable input (g25-10).
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "employee_id does not reference an existing employee")
		}
		slog.Default().ErrorContext(ctx, "can't upsert opex recurring", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't upsert opex recurring")
	}
	return &pb_admin.UpsertOpexRecurringResponse{Id: int32(id)}, nil
}

// ArchiveOpexRecurring stops a recurring template from materialising further months.
func (s *Server) ArchiveOpexRecurring(ctx context.Context, req *pb_admin.ArchiveOpexRecurringRequest) (*pb_admin.ArchiveOpexRecurringResponse, error) {
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to archive recurring OPEX")
	}
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.Metrics().ArchiveOpexRecurring(ctx, int(req.GetId())); err != nil {
		slog.Default().ErrorContext(ctx, "can't archive opex recurring", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't archive opex recurring")
	}
	return &pb_admin.ArchiveOpexRecurringResponse{}, nil
}

// ListOpexRecurring returns recurring OPEX templates (active-only unless include_archived). Gated
// like ListOpexLines — PermissionDenied without costing:read (nf08-06).
func (s *Server) ListOpexRecurring(ctx context.Context, req *pb_admin.ListOpexRecurringRequest) (*pb_admin.ListOpexRecurringResponse, error) {
	if read, _ := s.costingAccess(ctx); !read {
		return nil, status.Error(codes.PermissionDenied, "costing:read is required to view recurring OPEX")
	}
	list, err := s.repo.Metrics().ListOpexRecurring(ctx, req.GetIncludeArchived())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list opex recurring", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list opex recurring")
	}
	return &pb_admin.ListOpexRecurringResponse{Recurring: dto.OpexRecurringListToPb(list)}, nil
}

// UpsertEmployee inserts (id==0) or updates an employee-registry row (gap-07 v2 A). Gated on
// costing:write like recurring OPEX — salary registry data is confidential.
func (s *Server) UpsertEmployee(ctx context.Context, req *pb_admin.UpsertEmployeeRequest) (*pb_admin.UpsertEmployeeResponse, error) {
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to edit the employee registry")
	}
	ins, err := dto.ConvertPbEmployeeToEntity(req.GetEmployee())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	id, err := s.repo.Metrics().UpsertEmployee(ctx, ins, int(req.GetId()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "employee not found")
		}
		slog.Default().ErrorContext(ctx, "can't upsert employee", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't upsert employee")
	}
	return &pb_admin.UpsertEmployeeResponse{Id: int32(id)}, nil
}

// ArchiveEmployee soft-archives an employee; linked recurring templates keep their employee_id.
func (s *Server) ArchiveEmployee(ctx context.Context, req *pb_admin.ArchiveEmployeeRequest) (*pb_admin.ArchiveEmployeeResponse, error) {
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to archive an employee")
	}
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.Metrics().ArchiveEmployee(ctx, int(req.GetId())); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "employee not found")
		}
		slog.Default().ErrorContext(ctx, "can't archive employee", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't archive employee")
	}
	return &pb_admin.ArchiveEmployeeResponse{}, nil
}

// ListEmployees returns registry rows (active-only unless include_archived). Gated on costing:read.
func (s *Server) ListEmployees(ctx context.Context, req *pb_admin.ListEmployeesRequest) (*pb_admin.ListEmployeesResponse, error) {
	if read, _ := s.costingAccess(ctx); !read {
		return nil, status.Error(codes.PermissionDenied, "costing:read is required to view the employee registry")
	}
	list, err := s.repo.Metrics().ListEmployees(ctx, req.GetIncludeArchived())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list employees", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list employees")
	}
	return &pb_admin.ListEmployeesResponse{Employees: dto.EmployeeListToPb(list)}, nil
}

// channelKey joins the three UTM dimensions into a single map key (NUL-separated so empty
// segments can't collide, e.g. "a|" vs "|a").
func channelKey(source, medium, campaign string) string {
	return source + "\x00" + medium + "\x00" + campaign
}

// GetDashboard returns the small, DB-trusted decision payload. It reuses the GetMetrics
// period grammar but computes only the headline figures, alerts and short action lists.
func (s *Server) GetDashboard(ctx context.Context, req *pb_admin.GetDashboardRequest) (*pb_admin.GetDashboardResponse, error) {
	if strings.TrimSpace(req.Period) == "" {
		return nil, status.Errorf(codes.InvalidArgument, "period is required (e.g. 7d, 30d, 90d, today, WTD, MTD, QTD, YTD)")
	}
	endAt := time.Now().UTC()
	if req.EndAt != nil {
		endAt = req.EndAt.AsTime().UTC()
	}
	from, to, isSpecial := computePeriodBounds(req.Period, endAt)
	if !isSpecial {
		dur, err := parseMetricsPeriod(req.Period)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid period %q: %v", req.Period, err)
		}
		to = endAt
		from = endAt.Add(-dur)
	}
	d, err := s.repo.Metrics().GetDashboard(ctx, from, to, int(req.Limit))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get dashboard", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get dashboard")
	}
	resp := dto.ConvertDashboardToPb(d)
	// Redact margin figures for accounts without costing:read (task 19); revenue, order
	// count, inventory action lists and alerts remain.
	if read, _ := s.costingAccess(ctx); !read {
		stripDashboardCosting(resp)
	}
	return resp, nil
}

// GetAlertSettings returns the operator-tunable dashboard alert thresholds.
func (s *Server) GetAlertSettings(ctx context.Context, _ *pb_admin.GetAlertSettingsRequest) (*pb_admin.GetAlertSettingsResponse, error) {
	t, err := s.repo.Metrics().GetAlertThresholds(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get alert settings", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get alert settings")
	}
	return &pb_admin.GetAlertSettingsResponse{Settings: dto.AlertThresholdsToPb(t)}, nil
}

// UpsertAlertSettings updates the dashboard alert thresholds after validating their ranges.
func (s *Server) UpsertAlertSettings(ctx context.Context, req *pb_admin.UpsertAlertSettingsRequest) (*pb_admin.UpsertAlertSettingsResponse, error) {
	if req.Settings == nil {
		return nil, status.Errorf(codes.InvalidArgument, "settings is required")
	}
	t := dto.AlertThresholdsFromPb(req.Settings)
	if t.CoverageWarnPct < 0 || t.CoverageWarnPct > 100 ||
		t.RefundRateWarnPct < 0 || t.RefundRateWarnPct > 100 ||
		t.ContributionTrustPct < 0 || t.ContributionTrustPct > 100 ||
		t.GA4CoverageWarnPct < 0 || t.GA4CoverageWarnPct > 100 ||
		t.RateFloorN < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "percentages must be within [0,100] and rate_floor_n >= 0")
	}
	// A negative stale-days would silently disable the stale-open-run alert (GetStaleOpenRunCount
	// treats <= 0 as "off") while GetAlertSettings still reports the stored value as configured —
	// reject it so the setting can't be quietly turned off (nf09-06). 0 means "disabled" explicitly.
	if t.ProductionRunStaleDays < 0 {
		return nil, status.Errorf(codes.InvalidArgument, "production_run_stale_days must be >= 0 (0 disables the alert)")
	}
	if err := s.repo.Metrics().UpsertAlertThresholds(ctx, t); err != nil {
		slog.Default().ErrorContext(ctx, "can't upsert alert settings", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't upsert alert settings")
	}
	return &pb_admin.UpsertAlertSettingsResponse{}, nil
}

// enrichCampaignSpend fills Spend and ROAS on the attribution rows from channel_spend,
// matching by the UTM triple over the same period. Mutates rows in place.
func (s *Server) enrichCampaignSpend(ctx context.Context, from, to time.Time, rows []entity.CampaignAttributionAggregatedFull) error {
	if len(rows) == 0 {
		return nil
	}
	spend, err := s.repo.BQCache().GetChannelSpendByCampaign(ctx, from, to)
	if err != nil {
		return err
	}
	if len(spend) == 0 {
		return nil
	}
	byChannel := make(map[string]decimal.Decimal, len(spend))
	for _, sp := range spend {
		byChannel[channelKey(sp.UTMSource, sp.UTMMedium, sp.UTMCampaign)] = sp.Spend
	}
	for i := range rows {
		sp, ok := byChannel[channelKey(rows[i].UTMSource, rows[i].UTMMedium, rows[i].UTMCampaign)]
		if !ok || !sp.IsPositive() {
			continue
		}
		rows[i].Spend = sp
		rows[i].ROAS = rows[i].Revenue.Div(sp).InexactFloat64()
		if rows[i].Conversions > 0 {
			rows[i].CAC = sp.Div(decimal.NewFromInt(rows[i].Conversions)).InexactFloat64()
		}
	}
	return nil
}

// GetVatRates returns the configured destination-country VAT rates for the admin management
// surface (net-of-VAT revenue configuration).
func (s *Server) GetVatRates(ctx context.Context, _ *pb_admin.GetVatRatesRequest) (*pb_admin.GetVatRatesResponse, error) {
	rates, err := s.repo.Metrics().ListVatRates(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list vat rates", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list vat rates")
	}
	out := make([]*pb_admin.VatRate, 0, len(rates))
	for _, r := range rates {
		out = append(out, &pb_admin.VatRate{
			CountryCode: r.CountryCode,
			RatePct:     &pb_decimal.Decimal{Value: r.RatePct.String()},
			ValidFrom:   timestamppb.New(r.ValidFrom),
		})
	}
	return &pb_admin.GetVatRatesResponse{Rates: out}, nil
}

// UpsertVatRates inserts or updates the standard VAT rate per destination country (ISO alpha-2).
// Rates must be in [0, 100); a country absent from the table is treated as 0% (export).
func (s *Server) UpsertVatRates(ctx context.Context, req *pb_admin.UpsertVatRatesRequest) (*pb_admin.UpsertVatRatesResponse, error) {
	if req == nil || len(req.Rates) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one vat rate is required")
	}
	ents := make([]entity.VatRate, 0, len(req.Rates))
	for _, r := range req.Rates {
		cc := strings.ToUpper(strings.TrimSpace(r.CountryCode))
		if len(cc) != 2 {
			return nil, status.Errorf(codes.InvalidArgument, "country_code must be a 2-letter ISO code, got %q", r.CountryCode)
		}
		if r.RatePct == nil {
			return nil, status.Errorf(codes.InvalidArgument, "rate_pct is required for %s", cc)
		}
		rate, err := decimal.NewFromString(r.RatePct.Value)
		if err != nil || rate.IsNegative() || rate.GreaterThanOrEqual(decimal.NewFromInt(100)) {
			return nil, status.Errorf(codes.InvalidArgument, "rate_pct for %s must be a number in [0, 100)", cc)
		}
		vr := entity.VatRate{CountryCode: cc, RatePct: rate}
		if r.ValidFrom != nil {
			vr.ValidFrom = r.ValidFrom.AsTime().UTC()
		}
		ents = append(ents, vr)
	}
	if err := s.repo.Metrics().UpsertVatRates(ctx, ents); err != nil {
		slog.Default().ErrorContext(ctx, "can't upsert vat rates", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't upsert vat rates")
	}
	return &pb_admin.UpsertVatRatesResponse{}, nil
}
