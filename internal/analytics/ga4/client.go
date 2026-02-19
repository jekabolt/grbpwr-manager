package ga4

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	analyticsdata "google.golang.org/api/analyticsdata/v1beta"
	"google.golang.org/api/option"
)

// property_id 299206603
// Config holds GA4 client configuration.
type Config struct {
	PropertyID      string `mapstructure:"property_id"`
	CredentialsJSON string `mapstructure:"credentials_json"` // path to service account JSON file, or raw JSON (for env vars)
	Enabled         bool   `mapstructure:"enabled"`
}

// Client wraps the GA4 Data API client.
type Client struct {
	service    *analyticsdata.Service
	propertyID string
	enabled    bool
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
			opts = append(opts, option.WithCredentialsJSON(jsonBytes))
		} else {
			opts = append(opts, option.WithCredentialsFile(cfg.CredentialsJSON))
		}
	}

	service, err := analyticsdata.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GA4 service: %w", err)
	}

	slog.Default().InfoContext(ctx, "GA4 analytics client initialized",
		slog.String("property_id", cfg.PropertyID))

	return &Client{
		service:    service,
		propertyID: cfg.PropertyID,
		enabled:    true,
	}, nil
}

// GetDailyMetrics fetches aggregated daily metrics for the given period.
func (c *Client) GetDailyMetrics(ctx context.Context, startDate, endDate time.Time) ([]DailyMetrics, error) {
	if !c.enabled {
		return nil, nil
	}

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
		if len(row.DimensionValues) < 2 || len(row.MetricValues) < 2 {
			continue
		}

		dateStr := row.DimensionValues[0].Value
		date, err := time.Parse("20060102", dateStr)
		if err != nil {
			continue
		}

		pagePath := row.DimensionValues[1].Value
		productID := extractProductIDFromPath(pagePath)

		m := ProductPageMetrics{
			Date:      date,
			ProductID: productID,
			PagePath:  pagePath,
			PageViews: parseInt(row.MetricValues[0].Value),
			Sessions:  parseInt(row.MetricValues[1].Value),
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// GetTrafficSourceMetrics fetches traffic source/medium data.
func (c *Client) GetTrafficSourceMetrics(ctx context.Context, startDate, endDate time.Time) ([]TrafficSourceMetrics, error) {
	if !c.enabled {
		return nil, nil
	}

	req := &analyticsdata.RunReportRequest{
		DateRanges: []*analyticsdata.DateRange{
			{
				StartDate: startDate.Format("2006-01-02"),
				EndDate:   endDate.Format("2006-01-02"),
			},
		},
		Dimensions: []*analyticsdata.Dimension{
			{Name: "date"},
			{Name: "sessionSource"},
			{Name: "sessionMedium"},
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
		return nil, fmt.Errorf("failed to run GA4 traffic source report: %w", err)
	}

	var metrics []TrafficSourceMetrics
	for _, row := range resp.Rows {
		if len(row.DimensionValues) < 3 || len(row.MetricValues) < 2 {
			continue
		}

		dateStr := row.DimensionValues[0].Value
		date, err := time.Parse("20060102", dateStr)
		if err != nil {
			continue
		}

		m := TrafficSourceMetrics{
			Date:     date,
			Source:   row.DimensionValues[1].Value,
			Medium:   row.DimensionValues[2].Value,
			Sessions: parseInt(row.MetricValues[0].Value),
			Users:    parseInt(row.MetricValues[1].Value),
		}
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// GetDeviceMetrics fetches device category breakdown.
func (c *Client) GetDeviceMetrics(ctx context.Context, startDate, endDate time.Time) ([]DeviceMetrics, error) {
	if !c.enabled {
		return nil, nil
	}

	req := &analyticsdata.RunReportRequest{
		DateRanges: []*analyticsdata.DateRange{
			{
				StartDate: startDate.Format("2006-01-02"),
				EndDate:   endDate.Format("2006-01-02"),
			},
		},
		Dimensions: []*analyticsdata.Dimension{
			{Name: "date"},
			{Name: "deviceCategory"},
		},
		Metrics: []*analyticsdata.Metric{
			{Name: "sessions"},
			{Name: "totalUsers"},
		},
	}

	resp, err := c.service.Properties.RunReport(fmt.Sprintf("properties/%s", c.propertyID), req).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to run GA4 device report: %w", err)
	}

	var metrics []DeviceMetrics
	for _, row := range resp.Rows {
		if len(row.DimensionValues) < 2 || len(row.MetricValues) < 2 {
			continue
		}

		dateStr := row.DimensionValues[0].Value
		date, err := time.Parse("20060102", dateStr)
		if err != nil {
			continue
		}

		m := DeviceMetrics{
			Date:           date,
			DeviceCategory: row.DimensionValues[1].Value,
			Sessions:       parseInt(row.MetricValues[0].Value),
			Users:          parseInt(row.MetricValues[1].Value),
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
	v, _ := strconv.Atoi(s)
	return v
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// extractProductIDFromPath extracts product ID from URL path.
// Adjust this based on your actual URL structure.
// Example: /product/123 -> 123
func extractProductIDFromPath(path string) int {
	// Simple extraction - customize based on your URL pattern
	var id int
	fmt.Sscanf(path, "/product/%d", &id)
	return id
}
