package entity

import (
	"time"

	"github.com/shopspring/decimal"
)

// BusinessMetrics contains all computed metrics for a reporting period.
type BusinessMetrics struct {
	Period        TimeRange
	ComparePeriod *TimeRange

	// Core sales
	Revenue          MetricWithComparison
	OrdersCount      MetricWithComparison
	AvgOrderValue    MetricWithComparison
	ItemsPerOrder    MetricWithComparison
	RefundRate       MetricWithComparison
	PromoUsageRate   MetricWithComparison
	GrossRevenue     MetricWithComparison
	TotalRefunded    MetricWithComparison
	TotalDiscount    MetricWithComparison

	// Geography
	RevenueByCountry  []GeographyMetric
	RevenueByCity     []GeographyMetric
	RevenueByRegion   []RegionMetric
	AvgOrderByCountry []GeographyMetric

	// Currency
	RevenueByCurrency []CurrencyMetric

	// Payment
	RevenueByPaymentMethod []PaymentMethodMetric

	// Products
	TopProductsByRevenue   []ProductMetric
	TopProductsByQuantity  []ProductMetric
	RevenueByCategory      []CategoryMetric
	CrossSellPairs         []CrossSellPair

	// Customers
	NewSubscribers      MetricWithComparison
	RepeatCustomersRate MetricWithComparison
	AvgOrdersPerCustomer MetricWithComparison
	AvgDaysBetweenOrders MetricWithComparison
	CLVDistribution     CLVStats

	// Promo
	RevenueByPromo []PromoMetric

	// Order status funnel
	OrdersByStatus []StatusCount

	// Time series for charts
	RevenueByDay        []TimeSeriesPoint
	OrdersByDay         []TimeSeriesPoint
	SubscribersByDay    []TimeSeriesPoint
	GrossRevenueByDay  []TimeSeriesPoint
	RefundsByDay       []TimeSeriesPoint
	AvgOrderValueByDay []TimeSeriesPoint
	UnitsSoldByDay         []TimeSeriesPoint
	NewCustomersByDay      []TimeSeriesPoint
	ReturningCustomersByDay []TimeSeriesPoint
	ShippedByDay           []TimeSeriesPoint
	DeliveredByDay         []TimeSeriesPoint

	// Comparison period time series (overlay previous period on charts)
	RevenueByDayCompare        []TimeSeriesPoint
	OrdersByDayCompare         []TimeSeriesPoint
	SubscribersByDayCompare    []TimeSeriesPoint
	GrossRevenueByDayCompare   []TimeSeriesPoint
	RefundsByDayCompare        []TimeSeriesPoint
	AvgOrderValueByDayCompare   []TimeSeriesPoint
	UnitsSoldByDayCompare       []TimeSeriesPoint
	NewCustomersByDayCompare    []TimeSeriesPoint
	ReturningCustomersByDayCompare []TimeSeriesPoint
	ShippedByDayCompare        []TimeSeriesPoint
	DeliveredByDayCompare      []TimeSeriesPoint
}

// PaymentMethodMetric aggregates revenue by payment method (card, PayPal, etc.)
type PaymentMethodMetric struct {
	PaymentMethod string
	Value         decimal.Decimal
	Count         int
}

// MetricsGranularity controls time bucket size for time series (day, week, month).
type MetricsGranularity int

const (
	MetricsGranularityDay   MetricsGranularity = 1
	MetricsGranularityWeek  MetricsGranularity = 2
	MetricsGranularityMonth MetricsGranularity = 3
)

type TimeRange struct {
	From time.Time
	To   time.Time
}

type MetricWithComparison struct {
	Value        decimal.Decimal
	CompareValue *decimal.Decimal
	ChangePct    *float64
}

type GeographyMetric struct {
	Country      string
	State        *string
	City         *string
	Value        decimal.Decimal
	CompareValue *decimal.Decimal
	Count        int
	CompareCount *int // optional, for comparison period
}

// RegionMetric aggregates by shipping region (AFRICA, AMERICAS, EUROPE, etc.)
type RegionMetric struct {
	Region string
	Value  decimal.Decimal
	Count  int
}

// CurrencyMetric aggregates revenue by order currency
type CurrencyMetric struct {
	Currency string
	Value    decimal.Decimal
	Count    int
}

type ProductMetric struct {
	ProductId   int
	ProductName string
	Brand       string
	Value       decimal.Decimal
	Count       int
}

type CategoryMetric struct {
	CategoryId   int
	CategoryName string
	Value       decimal.Decimal
	Count       int
}

type CrossSellPair struct {
	ProductAId   int
	ProductBId   int
	ProductAName string
	ProductBName string
	Count        int
}

type PromoMetric struct {
	PromoCode    string
	OrdersCount  int
	Revenue      decimal.Decimal
	AvgDiscount  decimal.Decimal
}

type StatusCount struct {
	StatusName string
	Count      int
}

type CLVStats struct {
	Mean   decimal.Decimal
	Median decimal.Decimal
	P90    decimal.Decimal
}

type TimeSeriesPoint struct {
	Date  time.Time
	Value decimal.Decimal
	Count int
}
