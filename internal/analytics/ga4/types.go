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
	BounceRate              float64 // percentage (0-100), not ratio (0-1)
	AvgSessionDuration      float64 // seconds
	UserEngagementSeconds   int64   // total foreground engagement seconds
	PagesPerSession         float64
}

// ProductPageMetrics represents GA4 metrics for a specific product page.
type ProductPageMetrics struct {
	Date       time.Time
	ProductID  string
	PagePath   string
	PageViews  int
	AddToCarts int
	Sessions   int
}

// CountryMetrics represents GA4 country-level session data.
type CountryMetrics struct {
	Date     time.Time
	Country  string
	Sessions int
	Users    int
}

// EcommerceMetrics represents daily ecommerce metrics from GA4 batch API.
type EcommerceMetrics struct {
	Date        time.Time
	Purchases   int
	Revenue     decimal.Decimal
	AddToCarts  int
	Checkouts   int
	ItemsViewed int
}

// RevenueSourceMetrics represents revenue attribution by source/medium/campaign.
type RevenueSourceMetrics struct {
	Date      time.Time
	Source    string
	Medium    string
	Campaign  string
	Sessions  int
	Revenue   decimal.Decimal
	Purchases int
}

// ProductConversionMetrics represents per-product conversion funnel data.
type ProductConversionMetrics struct {
	Date        time.Time
	ProductID   string
	ProductName string
	ItemsViewed int
	AddToCarts  int
	Purchases   int
	Revenue     decimal.Decimal
}

// TimeRange represents a time period for metrics.
type TimeRange struct {
	From time.Time
	To   time.Time
}
