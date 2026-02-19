package ga4

import (
	"time"

	"github.com/shopspring/decimal"
)

// DailyMetrics represents aggregated GA4 metrics for a single day.
type DailyMetrics struct {
	Date               time.Time
	Sessions           int
	Users              int
	NewUsers           int
	PageViews          int
	BounceRate         float64
	AvgSessionDuration float64
	PagesPerSession    float64
}

// ProductPageMetrics represents GA4 metrics for a specific product page.
type ProductPageMetrics struct {
	Date       time.Time
	ProductID  int
	PagePath   string
	PageViews  int
	AddToCarts int
	Sessions   int
}

// TrafficSourceMetrics represents GA4 traffic source data.
type TrafficSourceMetrics struct {
	Date     time.Time
	Source   string
	Medium   string
	Sessions int
	Users    int
}

// DeviceMetrics represents GA4 device category data.
type DeviceMetrics struct {
	Date           time.Time
	DeviceCategory string // mobile, desktop, tablet
	Sessions       int
	Users          int
}

// CountryMetrics represents GA4 country-level session data.
type CountryMetrics struct {
	Date     time.Time
	Country  string
	Sessions int
	Users    int
}

// CategoryPageMetrics represents GA4 metrics for category pages.
type CategoryPageMetrics struct {
	Date       time.Time
	CategoryID int
	PagePath   string
	PageViews  int
	Sessions   int
}

// MetricsRequest represents a request for GA4 data.
type MetricsRequest struct {
	PropertyID string
	StartDate  time.Time
	EndDate    time.Time
	Dimensions []string
	Metrics    []string
}

// MetricsResponse represents the raw response from GA4 Data API.
type MetricsResponse struct {
	Rows []MetricsRow
}

// MetricsRow represents a single row in the GA4 response.
type MetricsRow struct {
	DimensionValues []string
	MetricValues    []string
}

// AggregatedMetrics represents computed metrics combining GA + DB data.
type AggregatedMetrics struct {
	Period             TimeRange
	Sessions           int
	Users              int
	Orders             int
	ConversionRate     decimal.Decimal
	RevenuePerSession  decimal.Decimal
	CartAbandonmentRate decimal.Decimal
}

// TimeRange represents a time period for metrics.
type TimeRange struct {
	From time.Time
	To   time.Time
}
