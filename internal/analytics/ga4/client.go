package ga4

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/circuitbreaker"
	"github.com/shopspring/decimal"
	analyticsdata "google.golang.org/api/analyticsdata/v1beta"
	"google.golang.org/api/option"
)

// property_id 299206603
// Config holds GA4 client configuration.
type Config struct {
	PropertyID      string                   `mapstructure:"property_id"`
	CredentialsJSON string                   `mapstructure:"credentials_json"` // path to service account JSON file, or raw JSON (for env vars)
	Enabled         bool                     `mapstructure:"enabled"`
	CircuitBreaker  circuitbreaker.Config    `mapstructure:"circuit_breaker"`
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

	var metrics []DailyMetrics
	for _, row := range resp.Rows {
		if len(row.DimensionValues) == 0 || len(row.MetricValues) < 7 {
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

		m := DailyMetrics{
			Date:               date,
			Sessions:           parseInt(row.MetricValues[0].Value),
			Users:              parseInt(row.MetricValues[1].Value),
			NewUsers:           parseInt(row.MetricValues[2].Value),
			PageViews:          parseInt(row.MetricValues[3].Value),
			BounceRate:         parseFloat(row.MetricValues[4].Value),
			AvgSessionDuration: parseFloat(row.MetricValues[5].Value),
			PagesPerSession:    parseFloat(row.MetricValues[6].Value),
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
	req := &analyticsdata.RunReportRequest{
		DateRanges: []*analyticsdata.DateRange{
			{
				StartDate: startDate.Format("2006-01-02"),
				EndDate:   endDate.Format("2006-01-02"),
			},
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
		Limit: 1000,
	}

	resp, err := c.service.Properties.RunReport(fmt.Sprintf("properties/%s", c.propertyID), req).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to run GA4 product report: %w", err)
	}

	var metrics []ProductPageMetrics
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
		productID := extractProductIDFromPath(pagePath)
		if productID == 0 {
			continue
		}

		m := ProductPageMetrics{
			Date:       date,
			ProductID:  strconv.Itoa(productID),
			PagePath:   pagePath,
			PageViews:  parseInt(row.MetricValues[0].Value),
			Sessions:   parseInt(row.MetricValues[1].Value),
			AddToCarts: parseInt(row.MetricValues[2].Value),
		}
		metrics = append(metrics, m)
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
	req := &analyticsdata.RunReportRequest{
		DateRanges: []*analyticsdata.DateRange{
			{
				StartDate: startDate.Format("2006-01-02"),
				EndDate:   endDate.Format("2006-01-02"),
			},
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
		Limit: 500,
	}

	resp, err := c.service.Properties.RunReport(fmt.Sprintf("properties/%s", c.propertyID), req).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to run GA4 country report: %w", err)
	}

	var metrics []CountryMetrics
	for _, row := range resp.Rows {
		if len(row.DimensionValues) < 2 || len(row.MetricValues) < 2 {
			continue
		}

		dateStr := row.DimensionValues[0].Value
		date, err := time.Parse("20060102", dateStr)
		if err != nil {
			continue
		}

		m := CountryMetrics{
			Date:     date,
			Country:  row.DimensionValues[1].Value,
			Sessions: parseInt(row.MetricValues[0].Value),
			Users:    parseInt(row.MetricValues[1].Value),
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
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

// extractProductIDFromPath extracts product ID from URL path.
// Handles both /product/42 and /product/{gender}/{brand}/{name}/{id} (see dto.GetProductSlug).
// Returns 0 if path is not a product path or last segment is not numeric.
func extractProductIDFromPath(path string) int {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || parts[0] != "product" {
		return 0
	}
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0
	}
	return id
}

// CircuitBreakerState returns the current state of the circuit breaker.
func (c *Client) CircuitBreakerState() circuitbreaker.State {
	if c.circuitBreaker == nil {
		return circuitbreaker.StateClosed
	}
	return c.circuitBreaker.State()
}

// GetEcommerceBatch runs 3 ecommerce reports in a single batched API call.
// Handles pagination per-report individually to avoid dropping rows > 10,000.
func (c *Client) GetEcommerceBatch(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]EcommerceMetrics, []RevenueSourceMetrics, []ProductConversionMetrics, error) {
	if !c.enabled {
		return nil, nil, nil, nil
	}

	var ecomRes []EcommerceMetrics
	var revRes []RevenueSourceMetrics
	var prodRes []ProductConversionMetrics

	err := c.circuitBreaker.Call(ctx, func(ctx context.Context) error {
		ecom, rev, prod, err := c.getEcommerceBatch(ctx, startDate, endDate)
		if err != nil {
			return err
		}
		ecomRes = ecom
		revRes = rev
		prodRes = prod
		return nil
	})
	return ecomRes, revRes, prodRes, err
}

func (c *Client) getEcommerceBatch(
	ctx context.Context,
	startDate, endDate time.Time,
) ([]EcommerceMetrics, []RevenueSourceMetrics, []ProductConversionMetrics, error) {
	var ecomRes []EcommerceMetrics
	var revRes []RevenueSourceMetrics
	var prodRes []ProductConversionMetrics

	const limit = int64(10000)
	var offset0, offset1, offset2 int64
	more0, more1, more2 := true, true, true

	sd := startDate.Format("2006-01-02")
	ed := endDate.Format("2006-01-02")

	for more0 || more1 || more2 {
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
		if more1 {
			requests = append(requests, &analyticsdata.RunReportRequest{
				DateRanges: []*analyticsdata.DateRange{{StartDate: sd, EndDate: ed}},
				Dimensions: []*analyticsdata.Dimension{
					{Name: "date"},
					{Name: "sessionSource"},
					{Name: "sessionMedium"},
					{Name: "sessionCampaignName"},
				},
				Metrics: []*analyticsdata.Metric{
					{Name: "sessions"},
					{Name: "purchaseRevenue"},
					{Name: "ecommercePurchases"},
				},
				Limit:  limit,
				Offset: offset1,
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
			return nil, nil, nil, fmt.Errorf("BatchRunReports failed: %w", err)
		}

		if len(resp.Reports) != len(requests) {
			return nil, nil, nil, fmt.Errorf(
				"unexpected report count: got %d, want %d",
				len(resp.Reports), len(requests),
			)
		}

		reqIdx := 0

		if more0 {
			r := resp.Reports[reqIdx]
			for _, row := range r.Rows {
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

		if more1 {
			r := resp.Reports[reqIdx]
			for _, row := range r.Rows {
				date, err := time.Parse("20060102", row.DimensionValues[0].Value)
				if err != nil {
					slog.Default().WarnContext(ctx, "ga4: skipping rev row with bad date",
						slog.String("value", row.DimensionValues[0].Value),
						slog.String("err", err.Error()))
					continue
				}
				campaign := row.DimensionValues[3].Value
				if campaign == "(not set)" {
					campaign = ""
				}
				revRes = append(revRes, RevenueSourceMetrics{
					Date:      date,
					Source:    row.DimensionValues[1].Value,
					Medium:    row.DimensionValues[2].Value,
					Campaign:  campaign,
					Sessions:  parseInt(row.MetricValues[0].Value),
					Revenue:   parseDecimal(row.MetricValues[1].Value),
					Purchases: parseInt(row.MetricValues[2].Value),
				})
			}
			if int64(len(r.Rows)) < limit {
				more1 = false
			} else {
				offset1 += limit
			}
			reqIdx++
		}

		if more2 {
			r := resp.Reports[reqIdx]
			for _, row := range r.Rows {
				date, err := time.Parse("20060102", row.DimensionValues[0].Value)
				if err != nil {
					slog.Default().WarnContext(ctx, "ga4: skipping prod row with bad date",
						slog.String("value", row.DimensionValues[0].Value),
						slog.String("err", err.Error()))
					continue
				}
				prodRes = append(prodRes, ProductConversionMetrics{
					Date:        date,
					ProductID:   row.DimensionValues[1].Value,
					ProductName: row.DimensionValues[2].Value,
					ItemsViewed: parseInt(row.MetricValues[0].Value),
					AddToCarts:  parseInt(row.MetricValues[1].Value),
					Purchases:   parseInt(row.MetricValues[2].Value),
					Revenue:     parseDecimal(row.MetricValues[3].Value),
				})
			}
			if int64(len(r.Rows)) < limit {
				more2 = false
			} else {
				offset2 += limit
			}
		}
	}

	return ecomRes, revRes, prodRes, nil
}
