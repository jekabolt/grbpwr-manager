package entity

import (
	"time"

	"github.com/shopspring/decimal"
)

// SyncSourceStatus represents the sync status of a single data source (GA4 API or BQ query).
type SyncSourceStatus struct {
	SyncType      string
	LastSyncAt    time.Time
	LastSyncDate  time.Time
	Success       bool // true = sync succeeded, false = error
	ErrorMessage  string
	RecordsSynced int
	StaleSince    *time.Time // non-nil when data is stale (last success > threshold)
}

// DataFreshness summarizes cache freshness across all sync sources.
// Included in BusinessMetrics so the admin dashboard can warn when serving old data.
type DataFreshness struct {
	GA4APILastSuccess *time.Time // most recent successful GA4 API sync
	BQLastSuccess     *time.Time // most recent successful BQ sync
	GA4Stale          bool       // true when GA4 cache exceeds staleness threshold
	BQStale           bool       // true when BQ cache exceeds staleness threshold
	Sources           []SyncSourceStatus
}

// BusinessMetrics contains all computed metrics for a reporting period.
type BusinessMetrics struct {
	Period        TimeRange
	ComparePeriod *TimeRange
	Freshness     *DataFreshness

	// Core sales
	Revenue        MetricWithComparison
	OrdersCount    MetricWithComparison
	AvgOrderValue  MetricWithComparison
	ItemsPerOrder  MetricWithComparison
	RefundRate     MetricWithComparison
	PromoUsageRate MetricWithComparison
	GrossRevenue   MetricWithComparison
	TotalRefunded  MetricWithComparison
	TotalDiscount  MetricWithComparison
	// ProductSaleDiscount is sum of list-price reductions from order_item.product_sale_percentage.
	// PromoCodeDiscount is promo_code percentage applied to post–product-sale subtotal.
	// TotalDiscount.Value == ProductSaleDiscount + PromoCodeDiscount.
	ProductSaleDiscount MetricWithComparison
	PromoCodeDiscount   MetricWithComparison

	// GA4 Traffic & Engagement
	Sessions           MetricWithComparison
	Users              MetricWithComparison
	NewUsers           MetricWithComparison
	PageViews          MetricWithComparison
	BounceRate         MetricWithComparison
	AvgSessionDuration MetricWithComparison
	PagesPerSession    MetricWithComparison
	ConversionRate     MetricWithComparison // orders / sessions
	RevenuePerSession  MetricWithComparison // revenue / sessions

	// Geography
	RevenueByCountry  []GeographyMetric
	RevenueByCity     []GeographyMetric
	RevenueByRegion   []RegionMetric
	AvgOrderByCountry []GeographyMetric
	SessionsByCountry []GeographySessionMetric

	// Currency
	RevenueByCurrency []CurrencyMetric

	// Payment
	RevenueByPaymentMethod []PaymentMethodMetric

	// Products
	TopProductsByRevenue  []ProductMetric
	TopProductsByQuantity []ProductMetric
	TopProductsByViews    []ProductViewMetric
	RevenueByCategory     []CategoryMetric
	CrossSellPairs        []CrossSellPair

	// Traffic Sources
	TrafficBySource []TrafficSourceMetric
	TrafficByDevice []DeviceMetric

	// Customers
	NewSubscribers       MetricWithComparison
	RepeatCustomersRate  MetricWithComparison
	AvgOrdersPerCustomer MetricWithComparison
	AvgDaysBetweenOrders MetricWithComparison
	CLVDistribution      CLVStats

	// Promo
	RevenueByPromo []PromoMetric

	// Order status funnel
	OrdersByStatus []StatusCount

	// Time series for charts
	RevenueByDay            []TimeSeriesPoint
	OrdersByDay             []TimeSeriesPoint
	SubscribersByDay        []TimeSeriesPoint
	GrossRevenueByDay       []TimeSeriesPoint
	RefundsByDay            []TimeSeriesPoint
	AvgOrderValueByDay      []TimeSeriesPoint
	UnitsSoldByDay          []TimeSeriesPoint
	NewCustomersByDay       []TimeSeriesPoint
	ReturningCustomersByDay []TimeSeriesPoint
	ShippedByDay            []TimeSeriesPoint
	DeliveredByDay          []TimeSeriesPoint
	SessionsByDay           []TimeSeriesPoint
	UsersByDay              []TimeSeriesPoint
	PageViewsByDay          []TimeSeriesPoint
	ConversionRateByDay     []TimeSeriesPoint

	// Email delivery metrics
	EmailDeliveryRate MetricWithComparison
	EmailOpenRate     MetricWithComparison
	EmailClickRate    MetricWithComparison
	EmailBounceRate   MetricWithComparison
	EmailsSent        MetricWithComparison
	EmailsDelivered   MetricWithComparison

	// Comparison period time series (overlay previous period on charts)
	RevenueByDayCompare            []TimeSeriesPoint
	OrdersByDayCompare             []TimeSeriesPoint
	SubscribersByDayCompare        []TimeSeriesPoint
	GrossRevenueByDayCompare       []TimeSeriesPoint
	RefundsByDayCompare            []TimeSeriesPoint
	AvgOrderValueByDayCompare      []TimeSeriesPoint
	UnitsSoldByDayCompare          []TimeSeriesPoint
	NewCustomersByDayCompare       []TimeSeriesPoint
	ReturningCustomersByDayCompare []TimeSeriesPoint
	ShippedByDayCompare            []TimeSeriesPoint
	DeliveredByDayCompare          []TimeSeriesPoint
	SessionsByDayCompare           []TimeSeriesPoint
	UsersByDayCompare              []TimeSeriesPoint
	PageViewsByDayCompare          []TimeSeriesPoint
	ConversionRateByDayCompare     []TimeSeriesPoint
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
	Value        decimal.Decimal
	Count        int
}

type CrossSellPair struct {
	ProductAId   int
	ProductBId   int
	ProductAName string
	ProductBName string
	Count        int
}

type PromoMetric struct {
	PromoCode   string
	OrdersCount int
	Revenue     decimal.Decimal
	AvgDiscount decimal.Decimal
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

// ProductViewMetric represents product page performance with GA4 data.
type ProductViewMetric struct {
	ProductId      int
	ProductName    string
	Brand          string
	PageViews      int
	Sessions       int
	AddToCarts     int
	Purchases      int
	ConversionRate float64 // purchases / page_views
}

// TrafficSourceMetric represents traffic source/medium breakdown.
type TrafficSourceMetric struct {
	Source   string
	Medium   string
	Sessions int
	Users    int
	Revenue  decimal.Decimal
}

// DeviceMetric represents device category breakdown.
type DeviceMetric struct {
	DeviceCategory string
	Sessions       int
	Users          int
	ConversionRate float64
}

// GeographySessionMetric represents session data by geography.
type GeographySessionMetric struct {
	Country  string
	State    *string
	City     *string
	Sessions int
	Users    int
}

// --- BQ Analytics entity types ---

// FunnelSteps holds the 10-step ecommerce funnel counts (shared by daily and aggregate).
type FunnelSteps struct {
	SessionStartUsers    int64 `db:"session_start_users"`
	ViewItemListUsers    int64 `db:"view_item_list_users"`
	SelectItemUsers      int64 `db:"select_item_users"`
	ViewItemUsers        int64 `db:"view_item_users"`
	SizeSelectedUsers    int64 `db:"size_selected_users"`
	AddToCartUsers       int64 `db:"add_to_cart_users"`
	BeginCheckoutUsers   int64 `db:"begin_checkout_users"`
	AddShippingInfoUsers int64 `db:"add_shipping_info_users"`
	AddPaymentInfoUsers  int64 `db:"add_payment_info_users"`
	PurchaseUsers        int64 `db:"purchase_users"`
}

// FunnelAggregate is the sum of DailyFunnel over a date range.
type FunnelAggregate struct {
	FunnelSteps
}

// DailyFunnel represents one day of the full 10-step ecommerce funnel.
type DailyFunnel struct {
	Date time.Time
	FunnelSteps
}

type DeviceFunnelMetric struct {
	Date           time.Time
	DeviceCategory string
	Sessions       int64
	AddToCartUsers int64
	CheckoutUsers  int64
	PurchaseUsers  int64
}

type ProductEngagementMetric struct {
	Date                  time.Time
	ProductID             string
	ProductName           string
	ImageViews            int64
	ZoomEvents            int64
	Scroll75              int64
	Scroll100             int64
	AvgTimeOnPageSeconds  float64
}

type ProductEngagementBubbleRow struct {
	ProductID             string
	ProductName           string
	TotalImageViews       int64
	TotalZoomEvents       int64
	TotalScroll75         int64
	TotalScroll100        int64
	ZoomRatePct           float64
	Scroll75RatePct       float64
	Scroll100RatePct      float64
	AvgTimeOnPageSeconds  float64
}

type ProductEngagementMetricsPct struct {
	AvgZoomRatePct        float64
	AvgScroll75RatePct    float64
	AvgScroll100RatePct   float64
	AvgTimeOnPageSeconds  float64
}

type ProductEngagementBubbleMatrix struct {
	Rows    []ProductEngagementBubbleRow
	Overall ProductEngagementMetricsPct
}

type FormErrorMetric struct {
	Date       time.Time
	FieldName  string
	ErrorCount int64
}

type ExceptionMetric struct {
	Date           time.Time
	PagePath       string
	ExceptionCount int64
	Description    string
}

type NotFoundMetric struct {
	Date     time.Time
	PagePath string
	HitCount int64
}

type HeroFunnelMetric struct {
	Date           time.Time
	HeroClickUsers int64
	ViewItemUsers  int64
	PurchaseUsers  int64
}

type SizeConfidenceMetric struct {
	Date           time.Time
	ProductID      string
	ProductName    string
	SizeGuideViews int64
	SizeSelections int64
}

type PaymentRecoveryMetric struct {
	Date           time.Time
	FailedUsers    int64
	RecoveredUsers int64
}

type CheckoutTimingMetric struct {
	Date                  time.Time
	AvgCheckoutSeconds    float64
	MedianCheckoutSeconds float64
	SessionCount          int64
}

type OOSImpactMetric struct {
	Date                 time.Time
	ProductID            string
	ProductName          string
	SizeID               int
	SizeName             string
	ProductPrice         decimal.Decimal
	Currency             string
	ClickCount           int64
	EstimatedLostSales   decimal.Decimal
	EstimatedLostRevenue decimal.Decimal
}

type PaymentFailureMetric struct {
	Date                time.Time
	ErrorCode           string
	PaymentType         string
	FailureCount        int64
	TotalFailedValue    decimal.Decimal
	AvgFailedOrderValue decimal.Decimal
}

type WebVitalMetric struct {
	Date           time.Time
	MetricName     string
	MetricRating   string
	SessionCount   int64
	Conversions    int64
	AvgMetricValue float64
}

type UserJourneyMetric struct {
	Date         time.Time
	JourneyPath  string
	SessionCount int64
	Conversions  int64
}

type SessionDurationMetric struct {
	Date                        time.Time
	AvgTimeBetweenEventsSeconds float64
	MedianTimeBetweenEvents     float64
}

// --- Retention & Cohort entity types ---

type CohortRetentionRow struct {
	CohortMonth time.Time
	CohortSize  int64 `db:"cohort_size"`
	M1          int64 `db:"m1"`
	M2          int64 `db:"m2"`
	M3          int64 `db:"m3"`
	M4          int64 `db:"m4"`
	M5          int64 `db:"m5"`
	M6          int64 `db:"m6"`
}

type OrderSequenceMetric struct {
	OrderNumber      int             `db:"order_num"`
	OrderCount       int64           `db:"order_count"`
	AvgOrderValue    decimal.Decimal `db:"avg_order_value"`
	AvgDaysSincePrev float64         `db:"avg_days_since_prev"`
}

type EntryProductMetric struct {
	ProductID     int             `db:"product_id"`
	ProductName   string          `db:"product_name"`
	PurchaseCount int64           `db:"purchase_count"`
	TotalRevenue  decimal.Decimal `db:"total_revenue"`
}

type RevenueParetoRow struct {
	Rank          int             `db:"rank_num"`
	ProductID     int             `db:"product_id"`
	ProductName   string          `db:"product_name"`
	Revenue       decimal.Decimal `db:"revenue"`
	CumulativePct float64         `db:"cumulative_pct"`
}

type SpendingCurvePoint struct {
	OrderNumber        int             `db:"order_num"`
	AvgCumulativeSpend decimal.Decimal `db:"avg_cumulative_spend"`
	CustomerCount      int64           `db:"customer_count"`
}

type CategoryLoyaltyRow struct {
	FirstCategory  string `db:"first_category"`
	SecondCategory string `db:"second_category"`
	CustomerCount  int64  `db:"customer_count"`
}

// --- Inventory Health entity types ---

type InventoryHealthRow struct {
	ProductID     int     `db:"product_id"`
	ProductName   string  `db:"product_name"`
	SizeID        int     `db:"size_id"`
	SizeName      string  `db:"size_name"`
	Quantity      int     `db:"quantity"`
	AvgDailySales float64 `db:"avg_daily_sales"`
	DaysOnHand    float64 `db:"days_on_hand"`
}

type SizeRunEfficiencyRow struct {
	ProductID        int     `db:"product_id"`
	ProductName      string  `db:"product_name"`
	TotalSizes       int     `db:"total_sizes"`
	SoldThroughSizes int     `db:"sold_through_sizes"`
	EfficiencyPct    float64 `db:"efficiency_pct"`
}

// --- Slow Movers ---

type SlowMoverRow struct {
	ProductID    int             `db:"product_id"`
	ProductName  string          `db:"product_name"`
	Revenue      decimal.Decimal `db:"revenue"`
	UnitsSold    int64           `db:"units_sold"`
	DaysInStock  float64         `db:"days_in_stock"`
	LastSaleDate *time.Time      `db:"last_sale_date"`
}

// --- Return Analysis ---

// ReturnByProductRow holds return rate by product with refund reason breakdown for horizontal stacked bar chart.
type ReturnByProductRow struct {
	ProductName     string             `json:"product_name"`
	TotalReturnRate float64            `json:"total_return_rate"`
	Reasons         map[string]float64 `json:"reasons"` // wrong_size, not_as_described, defective, changed_mind, other
}

type ReturnBySizeRow struct {
	SizeID        int     `db:"size_id"`
	SizeName      string  `db:"size_name"`
	TotalSold     int64   `db:"total_sold"`
	TotalReturned int64   `db:"total_returned"`
	ReturnRate    float64 `db:"return_rate"`
}

// --- Size Analytics ---

type SizeAnalyticsRow struct {
	ProductID    int             `db:"product_id"`
	ProductName  string          `db:"product_name"`
	SizeID       int             `db:"size_id"`
	SizeName     string          `db:"size_name"`
	UnitsSold    int64           `db:"units_sold"`
	Revenue      decimal.Decimal `db:"revenue"`
	PctOfProduct float64         `db:"pct_of_product"`
}

// --- Dead Stock ---

type DeadStockRow struct {
	ProductID       int             `db:"product_id"`
	ProductName     string          `db:"product_name"`
	SizeID          int             `db:"size_id"`
	SizeName        string          `db:"size_name"`
	Quantity        int             `db:"quantity"`
	DaysWithoutSale float64         `db:"days_without_sale"`
	StockValue      decimal.Decimal `db:"stock_value"`
}

// --- Product Trend ---

type ProductTrendRow struct {
	ProductID       int             `db:"product_id"`
	ProductName     string          `db:"product_name"`
	CurrentRevenue  decimal.Decimal `db:"current_revenue"`
	PreviousRevenue decimal.Decimal `db:"previous_revenue"`
	ChangePct       float64         `db:"change_pct"`
	CurrentUnits    int64           `db:"current_units"`
	PreviousUnits   int64           `db:"previous_units"`
}

// --- Time on Page (BQ) ---

type TimeOnPageRow struct {
	Date                  time.Time
	PagePath              string
	AvgVisibleTimeSeconds float64
	AvgTotalTimeSeconds   float64
	AvgEngagementScore    float64
	PageViews             int64
}

// --- Product Zoom (BQ) ---

type ProductZoomRow struct {
	Date        time.Time
	ProductID   string
	ProductName string
	ZoomMethod  string // double_click, pinch
	ZoomCount   int64
}

// --- Image Swipes (BQ) ---

type ImageSwipeRow struct {
	Date           time.Time
	ProductID      string
	ProductName    string
	SwipeDirection string // next, previous
	SwipeCount     int64
}

// --- Size Guide Clicks (BQ) ---

type SizeGuideClickRow struct {
	Date         time.Time
	ProductID    string
	ProductName  string
	PageLocation string // desktop, mobile
	ClickCount   int64
}

// --- Details Expansion (BQ) ---

type DetailsExpansionRow struct {
	Date        time.Time
	ProductID   string
	ProductName string
	SectionName string // description, composition, care
	ExpandCount int64
}

// --- Notify Me Intent (BQ) ---

type NotifyMeIntentRow struct {
	Date           time.Time
	ProductID      string
	ProductName    string
	Action         string // opened, submitted, closed_without_submit
	Count          int64
	ConversionRate float64 // submitted / opened
}

// --- Add-to-Cart Rate (BQ) ---

type AddToCartRateRow struct {
	Date           time.Time
	ProductID      string
	ProductName    string
	ViewCount      int64
	AddToCartCount int64
	CartRate       float64
}

// TrendGranularity defines time bucket size for trend analysis
type TrendGranularity int

const (
	TrendGranularityDaily   TrendGranularity = 0
	TrendGranularityWeekly  TrendGranularity = 1
	TrendGranularityMonthly TrendGranularity = 2
)

// AddToCartRateAnalysis contains both per-product aggregate data for scatter plot
// and store-wide trend data for time series visualization
type AddToCartRateAnalysis struct {
	Products     []AddToCartRateProductRow
	GlobalTrend  []AddToCartRateGlobalRow
	AvgViewCount int64
	AvgCartRate  float64
}

// AddToCartRateProductRow represents per-product aggregate metrics for scatter plot matrix
type AddToCartRateProductRow struct {
	ProductID      string
	ProductName    string
	ViewCount      int64
	AddToCartCount int64
	CartRate       float64
}

// AddToCartRateGlobalRow represents store-wide daily/weekly/monthly ATC rate for trend line
type AddToCartRateGlobalRow struct {
	Date            time.Time
	TotalViews      int64
	TotalAddToCarts int64
	GlobalCartRate  float64
}

// --- Browser Breakdown (BQ) ---

type BrowserBreakdownRow struct {
	Date           time.Time
	Browser        string
	Sessions       int64
	Users          int64
	Conversions    int64
	ConversionRate float64
}

// --- Newsletter (BQ) ---

type NewsletterMetricRow struct {
	Date        time.Time
	SignupCount int64
	UniqueUsers int64
}

// --- Abandoned Cart (BQ) ---

type AbandonedCartRow struct {
	Date                 time.Time
	CartsStarted         int64
	CheckoutsStarted     int64
	AbandonmentRate      float64
	AvgMinutesToCheckout float64
	AvgMinutesToAbandon  float64
}

// --- Campaign Attribution (BQ) ---

type CampaignAttributionRow struct {
	Date           time.Time
	UTMSource      string
	UTMMedium      string
	UTMCampaign    string
	Sessions       int64
	Users          int64
	Conversions    int64
	Revenue        decimal.Decimal
	ConversionRate float64
}

type CampaignAttributionAggregated struct {
	UTMSource string
	UTMMedium string
	Sessions  int64
	Users     int64
	Revenue   decimal.Decimal
}

type CampaignAttributionAggregatedFull struct {
	UTMSource      string
	UTMMedium      string
	UTMCampaign    string
	Sessions       int64
	Users          int64
	Conversions    int64
	Revenue        decimal.Decimal
	ConversionRate float64
}
