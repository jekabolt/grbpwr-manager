package ga4

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/circuitbreaker"
	"github.com/jekabolt/grbpwr-manager/internal/slug"
	"github.com/shopspring/decimal"
	analyticsdata "google.golang.org/api/analyticsdata/v1beta"
	"google.golang.org/api/option"
)

// property_id 299206603
// Config holds GA4 client configuration.
type Config struct {
	PropertyID      string                `mapstructure:"property_id"`
	CredentialsJSON string                `mapstructure:"credentials_json"` // path to service account JSON file, or raw JSON (for env vars)
	Enabled         bool                  `mapstructure:"enabled"`
	CircuitBreaker  circuitbreaker.Config `mapstructure:"circuit_breaker"`
}

// Client wraps the GA4 Data API client.
type Client struct {
	service        *analyticsdata.Service
	propertyID     string
	enabled        bool
	circuitBreaker *circuitbreaker.CircuitBreaker
}

// NewClient creates a new GA4 client.
func NewClient(ctx context.Context, cfg *Config) (*Client, error) {
	if cfg == nil || !cfg.Enabled {
		slog.Default().InfoContext(ctx, "GA4 analytics disabled")
		return &Client{enabled: false}, nil
	}

	if cfg.PropertyID == "" {
		return nil, fmt.Errorf("ga4 property_id is required")
	}

	var opts []option.ClientOption
	if cfg.CredentialsJSON != "" {
		jsonBytes := []byte(cfg.CredentialsJSON)
		if len(jsonBytes) > 0 && jsonBytes[0] == '{' {
			opts = append(opts, option.WithAuthCredentialsJSON(option.ServiceAccount, jsonBytes))
		} else {
			opts = append(opts, option.WithAuthCredentialsFile(option.ServiceAccount, cfg.CredentialsJSON))
		}
	}

	service, err := analyticsdata.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GA4 service: %w", err)
	}

	cb := circuitbreaker.New("ga4", cfg.CircuitBreaker, func(from, to circuitbreaker.State, reason string) {
		slog.Default().ErrorContext(ctx, "GA4 circuit breaker state changed",
			slog.String("from", from.String()),
			slog.String("to", to.String()),
			slog.String("reason", reason))
	})

	slog.Default().InfoContext(ctx, "GA4 analytics client initialized",
		slog.String("property_id", cfg.PropertyID))

	return &Client{
		service:        service,
		propertyID:     cfg.PropertyID,
		enabled:        true,
		circuitBreaker: cb,
	}, nil
}

// GetDailyMetrics fetches aggregated daily metrics for the given period.
func (c *Client) GetDailyMetrics(ctx context.Context, startDate, endDate time.Time) ([]DailyMetrics, error) {
	if !c.enabled {
		return nil, nil
	}

	var result []DailyMetrics
	err := c.circuitBreaker.Call(ctx, func(ctx context.Context) error {
		metrics, err := c.getDailyMetrics(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = metrics
		return nil
	})
	return result, err
}

func (c *Client) getDailyMetrics(ctx context.Context, startDate, endDate time.Time) ([]DailyMetrics, error) {
	req := &analyticsdata.RunReportRequest{
		DateRanges: []*analyticsdata.DateRange{
			{
				StartDate: startDate.Format("2006-01-02"),
				EndDate:   endDate.Format("2006-01-02"),
			},
		},
		Dimensions: []*analyticsdata.Dimension{
			{Name: "date"},
		},
		Metrics: []*analyticsdata.Metric{
			{Name: "sessions"},
			{Name: "totalUsers"},
			{Name: "newUsers"},
			{Name: "screenPageViews"},
			{Name: "bounceRate"},
			{Name: "averageSessionDuration"},
			{Name: "screenPageViewsPerSession"},
			{Name: "userEngagementDuration"},
		},
		OrderBys: []*analyticsdata.OrderBy{
			{
				Dimension: &analyticsdata.DimensionOrderBy{DimensionName: "date"},
				Desc:      false,
			},
		},
	}

	resp, err := c.service.Properties.RunReport(fmt.Sprintf("properties/%s", c.propertyID), req).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to run GA4 report: %w", err)
	}

	idx, hasEngagement, ok := resolveGA4DailyIndices(resp.MetricHeaders)
	if !ok {
		return nil, fmt.Errorf("GA4 daily metrics: unexpected metric headers in response")
	}
	mh := resp.MetricHeaders
	maxCol := idx.maxMetricCol(hasEngagement)

	var metrics []DailyMetrics
	for _, row := range resp.Rows {
		if len(row.DimensionValues) == 0 || len(row.MetricValues) <= maxCol {
			continue
		}

		dateStr := row.DimensionValues[0].Value
		date, err := time.Parse("20060102", dateStr)
		if err != nil {
			slog.Default().WarnContext(ctx, "failed to parse GA4 date",
				slog.String("date", dateStr),
				slog.String("err", err.Error()))
			continue
		}

		sessions := parseInt(row.MetricValues[idx.sessions].Value)
		wallSec := metricValueToSeconds(
			row.MetricValues[idx.averageSessionDuration].Value,
			metricHeaderAt(mh, idx.averageSessionDuration),
		)
		var engagementTotalSec float64
		if hasEngagement {
			engagementTotalSec = metricValueToSeconds(
				row.MetricValues[idx.userEngagementDuration].Value,
				metricHeaderAt(mh, idx.userEngagementDuration),
			)
		}
		m := DailyMetrics{
			Date:       date,
			Sessions:   sessions,
			Users:      parseInt(row.MetricValues[idx.totalUsers].Value),
			NewUsers:   parseInt(row.MetricValues[idx.newUsers].Value),
			PageViews:  parseInt(row.MetricValues[idx.screenPageViews].Value),
			BounceRate: parseFloat(row.MetricValues[idx.bounceRate].Value) * 100,
			AvgSessionDuration: avgSessionDurationForStorage(
				sessions, wallSec, engagementTotalSec, hasEngagement,
			),
			UserEngagementSeconds: int64(math.Round(engagementTotalSec)),
			PagesPerSession:       parseFloat(row.MetricValues[idx.screenPageViewsPerSession].Value),
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// GetProductPageMetrics fetches page views and engagement for product pages.
func (c *Client) GetProductPageMetrics(ctx context.Context, startDate, endDate time.Time) ([]ProductPageMetrics, error) {
	if !c.enabled {
		return nil, nil
	}

	var result []ProductPageMetrics
	err := c.circuitBreaker.Call(ctx, func(ctx context.Context) error {
		metrics, err := c.getProductPageMetrics(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = metrics
		return nil
	})
	return result, err
}

func (c *Client) getProductPageMetrics(ctx context.Context, startDate, endDate time.Time) ([]ProductPageMetrics, error) {
	const pageSize = int64(10000)
	sd := startDate.Format("2006-01-02")
	ed := endDate.Format("2006-01-02")

	var metrics []ProductPageMetrics
	// Paginate: a single request returns at most Limit rows, so without an offset loop
	// the low-traffic tail of products is silently dropped (a 7-day window multiplies
	// rows by day). Stop once a short page comes back.
	for offset := int64(0); ; offset += pageSize {
		req := &analyticsdata.RunReportRequest{
			DateRanges: []*analyticsdata.DateRange{
				{StartDate: sd, EndDate: ed},
			},
			Dimensions: []*analyticsdata.Dimension{
				{Name: "date"},
				{Name: "pagePath"},
			},
			Metrics: []*analyticsdata.Metric{
				{Name: "screenPageViews"},
				{Name: "sessions"},
				{Name: "addToCarts"},
			},
			DimensionFilter: &analyticsdata.FilterExpression{
				Filter: &analyticsdata.Filter{
					FieldName: "pagePath",
					StringFilter: &analyticsdata.StringFilter{
						MatchType: "CONTAINS",
						Value:     "/product/", // Adjust based on your URL structure
					},
				},
			},
			OrderBys: []*analyticsdata.OrderBy{
				{
					Metric: &analyticsdata.MetricOrderBy{MetricName: "screenPageViews"},
					Desc:   true,
				},
			},
			Limit:  pageSize,
			Offset: offset,
		}

		resp, err := c.service.Properties.RunReport(fmt.Sprintf("properties/%s", c.propertyID), req).Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("failed to run GA4 product report: %w", err)
		}

		for _, row := range resp.Rows {
			if len(row.DimensionValues) < 2 || len(row.MetricValues) < 3 {
				continue
			}

			dateStr := row.DimensionValues[0].Value
			date, err := time.Parse("20060102", dateStr)
			if err != nil {
				continue
			}

			pagePath := row.DimensionValues[1].Value
			productSKU := extractProductSKUFromPath(pagePath)
			if productSKU == "" {
				continue
			}

			metrics = append(metrics, ProductPageMetrics{
				Date:       date,
				ProductID:  productSKU,
				PagePath:   pagePath,
				PageViews:  parseInt(row.MetricValues[0].Value),
				Sessions:   parseInt(row.MetricValues[1].Value),
				AddToCarts: parseInt(row.MetricValues[2].Value),
			})
		}

		if int64(len(resp.Rows)) < pageSize {
			break
		}
	}

	return metrics, nil
}

// GetCountryMetrics fetches session data by country.
func (c *Client) GetCountryMetrics(ctx context.Context, startDate, endDate time.Time) ([]CountryMetrics, error) {
	if !c.enabled {
		return nil, nil
	}

	var result []CountryMetrics
	err := c.circuitBreaker.Call(ctx, func(ctx context.Context) error {
		metrics, err := c.getCountryMetrics(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		result = metrics
		return nil
	})
	return result, err
}

func (c *Client) getCountryMetrics(ctx context.Context, startDate, endDate time.Time) ([]CountryMetrics, error) {
	const pageSize = int64(10000)
	sd := startDate.Format("2006-01-02")
	ed := endDate.Format("2006-01-02")

	var metrics []CountryMetrics
	// Paginate so country-days beyond the first page are not silently truncated.
	for offset := int64(0); ; offset += pageSize {
		req := &analyticsdata.RunReportRequest{
			DateRanges: []*analyticsdata.DateRange{
				{StartDate: sd, EndDate: ed},
			},
			Dimensions: []*analyticsdata.Dimension{
				{Name: "date"},
				{Name: "country"},
			},
			Metrics: []*analyticsdata.Metric{
				{Name: "sessions"},
				{Name: "totalUsers"},
			},
			OrderBys: []*analyticsdata.OrderBy{
				{
					Metric: &analyticsdata.MetricOrderBy{MetricName: "sessions"},
					Desc:   true,
				},
			},
			Limit:  pageSize,
			Offset: offset,
		}

		resp, err := c.service.Properties.RunReport(fmt.Sprintf("properties/%s", c.propertyID), req).Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("failed to run GA4 country report: %w", err)
		}

		for _, row := range resp.Rows {
			if len(row.DimensionValues) < 2 || len(row.MetricValues) < 2 {
				continue
			}

			dateStr := row.DimensionValues[0].Value
			date, err := time.Parse("20060102", dateStr)
			if err != nil {
				continue
			}

			metrics = append(metrics, CountryMetrics{
				Date:     date,
				Country:  row.DimensionValues[1].Value,
				Sessions: parseInt(row.MetricValues[0].Value),
				Users:    parseInt(row.MetricValues[1].Value),
			})
		}

		if int64(len(resp.Rows)) < pageSize {
			break
		}
	}

	return metrics, nil
}

// ga4DailyMetricIdx holds column indices for RunReport metric columns (by header
// name when present, else fixed positions matching our request order).
type ga4DailyMetricIdx struct {
	sessions, totalUsers, newUsers, screenPageViews                                       int
	bounceRate, averageSessionDuration, screenPageViewsPerSession, userEngagementDuration int
}

func ga4MetricNameIndex(headers []*analyticsdata.MetricHeader) map[string]int {
	m := make(map[string]int, len(headers))
	for i, h := range headers {
		if h != nil && h.Name != "" {
			m[h.Name] = i
		}
	}
	return m
}

func resolveGA4DailyIndices(headers []*analyticsdata.MetricHeader) (idx ga4DailyMetricIdx, hasEngagement bool, ok bool) {
	if len(headers) == 0 {
		return ga4DailyMetricIdx{
			sessions: 0, totalUsers: 1, newUsers: 2, screenPageViews: 3,
			bounceRate: 4, averageSessionDuration: 5, screenPageViewsPerSession: 6, userEngagementDuration: 7,
		}, true, true
	}
	n := ga4MetricNameIndex(headers)
	must := []string{"sessions", "totalUsers", "newUsers", "screenPageViews", "bounceRate", "averageSessionDuration", "screenPageViewsPerSession"}
	for _, name := range must {
		if _, found := n[name]; !found {
			return ga4DailyMetricIdx{}, false, false
		}
	}
	idx = ga4DailyMetricIdx{
		sessions:                  n["sessions"],
		totalUsers:                n["totalUsers"],
		newUsers:                  n["newUsers"],
		screenPageViews:           n["screenPageViews"],
		bounceRate:                n["bounceRate"],
		averageSessionDuration:    n["averageSessionDuration"],
		screenPageViewsPerSession: n["screenPageViewsPerSession"],
	}
	ued, found := n["userEngagementDuration"]
	if found {
		idx.userEngagementDuration = ued
		return idx, true, true
	}
	return idx, false, true
}

func metricHeaderAt(headers []*analyticsdata.MetricHeader, col int) *analyticsdata.MetricHeader {
	if col < 0 || col >= len(headers) {
		return nil
	}
	return headers[col]
}

// metricValueToSeconds converts a metric cell to seconds using MetricHeader.Type.
// GA4 reports TYPE_MILLISECONDS for some duration metrics; the numeric Value is
// then in ms and must be scaled (e.g. 13635 ms → 13.635 s).
func metricValueToSeconds(val string, header *analyticsdata.MetricHeader) float64 {
	v := parseFloat(val)
	if header == nil {
		return v
	}
	switch header.Type {
	case "TYPE_MILLISECONDS":
		return v / 1000.0
	case "TYPE_MINUTES":
		return v * 60
	case "TYPE_HOURS":
		return v * 3600
	default:
		return v
	}
}

func (idx ga4DailyMetricIdx) maxMetricCol(hasEngagement bool) int {
	mx := idx.sessions
	for _, c := range []int{
		idx.totalUsers, idx.newUsers, idx.screenPageViews, idx.bounceRate,
		idx.averageSessionDuration, idx.screenPageViewsPerSession,
	} {
		if c > mx {
			mx = c
		}
	}
	if hasEngagement && idx.userEngagementDuration > mx {
		mx = idx.userEngagementDuration
	}
	return mx
}

// avgSessionDurationForStorage picks a per-session duration (seconds) to persist
// in avg_session_duration. Prefer userEngagementDuration / sessions when that
// metric is present (foreground time; sane for ecommerce). Otherwise use
// averageSessionDuration after metricValueToSeconds (handles TYPE_MILLISECONDS).
func avgSessionDurationForStorage(sessions int, wallClockSec, engagementTotalSec float64, hasEngagement bool) float64 {
	if sessions > 0 && hasEngagement {
		return engagementTotalSec / float64(sessions)
	}
	if sessions > 0 {
		return wallClockSec
	}
	return wallClockSec
}

// Helper functions

func parseInt(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		slog.Warn("GA4 parseInt failed, using 0", "value", s, "err", err)
		return 0
	}
	return v
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		slog.Warn("GA4 parseFloat failed, using 0", "value", s, "err", err)
		return 0
	}
	return v
}

func parseDecimal(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		slog.Warn("GA4 parseDecimal failed, using 0", "value", s, "err", err)
		return decimal.Zero
	}
	return d
}

// extractProductSKUFromPath extracts the uppercased base SKU from a product page path
// (/p/{pretty}-{sku}). Returns "" when the path is not a product path or carries no SKU. It delegates
// to slug.ParseProductTail — the single shared strict parser (problem 031) — after normalizing the
// leading slash GA4 page_path may omit, so analytics and the storefront agree on the grammar.
func extractProductSKUFromPath(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	sku, err := slug.ParseProductTail(path)
	if err != nil {
		return ""
	}
	return sku
}

// CircuitBreakerState returns the current state of the circuit breaker.
func (c *Client) CircuitBreakerState() circuitbreaker.State {
	if c.circuitBreaker == nil {
		return circuitbreaker.StateClosed
	}
	return c.circuitBreaker.State()
}

// GetEcommerceBatch runs the ecommerce reports in a single batched API call.
// Handles pagination per-report individually to avoid dropping rows > 10,000.
// The revenue-by-source report was dropped (analytics-v2 task 11): ga4_revenue_by_source had no
// readers — attribution lives in bq_campaign_attribution + settled ROAS — so fetching it only burned
// GA4 quota.
func (c *Client) GetEcommerceBatch(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]EcommerceMetrics, []ProductConversionMetrics, error) {
	if !c.enabled {
		return nil, nil, nil
	}

	var ecomRes []EcommerceMetrics
	var prodRes []ProductConversionMetrics

	err := c.circuitBreaker.Call(ctx, func(ctx context.Context) error {
		ecom, prod, err := c.getEcommerceBatch(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		ecomRes = ecom
		prodRes = prod
		return nil
	})
	return ecomRes, prodRes, err
}

func (c *Client) getEcommerceBatch(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]EcommerceMetrics, []ProductConversionMetrics, error) {
	var ecomRes []EcommerceMetrics
	// prodAgg collapses the per-itemId rows GA4 returns down to one row per (date, base SKU) — see
	// prodAggregator.
	prodAgg := newProdAggregator()

	const limit = int64(10000)
	var offset0, offset2 int64
	more0, more2 := true, true

	sd := startDate.Format("2006-01-02")
	ed := endDate.Format("2006-01-02")

	for more0 || more2 {
		var requests []*analyticsdata.RunReportRequest

		if more0 {
			requests = append(requests, &analyticsdata.RunReportRequest{
				DateRanges: []*analyticsdata.DateRange{{StartDate: sd, EndDate: ed}},
				Dimensions: []*analyticsdata.Dimension{{Name: "date"}},
				Metrics: []*analyticsdata.Metric{
					{Name: "ecommercePurchases"},
					{Name: "purchaseRevenue"},
					{Name: "addToCarts"},
					{Name: "checkouts"},
					{Name: "itemsViewed"},
				},
				Limit:  limit,
				Offset: offset0,
			})
		}
		if more2 {
			// Use itemsAddedToCart (item-scoped) instead of addToCarts (event-scoped):
			// itemId/itemName are item-scoped dimensions and require item-scoped metrics.
			requests = append(requests, &analyticsdata.RunReportRequest{
				DateRanges: []*analyticsdata.DateRange{{StartDate: sd, EndDate: ed}},
				Dimensions: []*analyticsdata.Dimension{
					{Name: "date"},
					{Name: "itemId"},
					{Name: "itemName"},
				},
				Metrics: []*analyticsdata.Metric{
					{Name: "itemsViewed"},
					{Name: "itemsAddedToCart"},
					{Name: "itemsPurchased"},
					{Name: "itemRevenue"},
				},
				Limit:  limit,
				Offset: offset2,
			})
		}

		req := &analyticsdata.BatchRunReportsRequest{Requests: requests}
		resp, err := c.service.Properties.BatchRunReports(
			"properties/"+c.propertyID, req,
		).Context(ctx).Do()
		if err != nil {
			return nil, nil, fmt.Errorf("BatchRunReports failed: %w", err)
		}

		if len(resp.Reports) != len(requests) {
			return nil, nil, fmt.Errorf(
				"unexpected report count: got %d, want %d",
				len(resp.Reports), len(requests),
			)
		}

		reqIdx := 0

		if more0 {
			r := resp.Reports[reqIdx]
			for _, row := range r.Rows {
				if len(row.DimensionValues) < 1 || len(row.MetricValues) < 5 {
					continue
				}
				date, err := time.Parse("20060102", row.DimensionValues[0].Value)
				if err != nil {
					slog.Default().WarnContext(ctx, "ga4: skipping ecom row with bad date",
						slog.String("value", row.DimensionValues[0].Value),
						slog.String("err", err.Error()))
					continue
				}
				ecomRes = append(ecomRes, EcommerceMetrics{
					Date:        date,
					Purchases:   parseInt(row.MetricValues[0].Value),
					Revenue:     parseDecimal(row.MetricValues[1].Value),
					AddToCarts:  parseInt(row.MetricValues[2].Value),
					Checkouts:   parseInt(row.MetricValues[3].Value),
					ItemsViewed: parseInt(row.MetricValues[4].Value),
				})
			}
			if int64(len(r.Rows)) < limit {
				more0 = false
			} else {
				offset0 += limit
			}
			reqIdx++
		}

		if more2 {
			r := resp.Reports[reqIdx]
			for _, row := range r.Rows {
				if len(row.DimensionValues) < 3 || len(row.MetricValues) < 4 {
					continue
				}
				date, err := time.Parse("20060102", row.DimensionValues[0].Value)
				if err != nil {
					slog.Default().WarnContext(ctx, "ga4: skipping prod row with bad date",
						slog.String("value", row.DimensionValues[0].Value),
						slog.String("err", err.Error()))
					continue
				}
				rawItemID := row.DimensionValues[1].Value
				ok := prodAgg.add(date, rawItemID, row.DimensionValues[2].Value,
					parseInt(row.MetricValues[0].Value),
					parseInt(row.MetricValues[1].Value),
					parseInt(row.MetricValues[2].Value),
					parseDecimal(row.MetricValues[3].Value),
				)
				if !ok {
					slog.Default().WarnContext(ctx, "ga4: skipping prod row with unrecognized item id",
						slog.String("item_id", rawItemID))
				}
			}
			if int64(len(r.Rows)) < limit {
				more2 = false
			} else {
				offset2 += limit
			}
		}
	}

	return ecomRes, prodAgg.result(), nil
}
