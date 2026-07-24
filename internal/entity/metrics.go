package entity

import (
	"database/sql"
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
	Revenue MetricWithComparison
	// OrdersCount counts only net-revenue orders (confirmed/shipped/delivered/partially_refunded).
	OrdersCount MetricWithComparison
	// TotalPlacedOrders counts every order placed in the period regardless of status
	// (sum of OrdersByStatus). Use it as the denominator for status shares.
	TotalPlacedOrders MetricWithComparison
	AvgOrderValue     MetricWithComparison
	ItemsPerOrder     MetricWithComparison
	RefundRate        MetricWithComparison
	PromoUsageRate    MetricWithComparison
	// PeakDay is the highest-net-revenue calendar day in the period (nil when there is no
	// revenue). Computed from a dedicated daily rollup, independent of the chart granularity.
	PeakDay *PeakDay
	// GrossRevenue is revenue at list prices (before any discounts or refunds) + shipping.
	// Revenue = GrossRevenue - ProductSaleDiscount - PromoCodeDiscount - TotalRefunded.
	GrossRevenue  MetricWithComparison
	TotalRefunded MetricWithComparison
	TotalDiscount MetricWithComparison
	// ProductSaleDiscount is sum of list-price reductions from order_item.product_sale_percentage.
	// PromoCodeDiscount is promo_code percentage applied to post–product-sale subtotal.
	// TotalDiscount.Value == ProductSaleDiscount + PromoCodeDiscount.
	ProductSaleDiscount MetricWithComparison
	PromoCodeDiscount   MetricWithComparison
	// DiscountRatePct is TotalDiscount / GrossRevenue × 100 — the share of list revenue given
	// away as discounts (lower is better). Numerator is over net-revenue statuses, denominator
	// (GrossRevenue) includes fully-refunded orders at list price, so they differ marginally.
	DiscountRatePct MetricWithComparison
	// RevenueInclVat is post-discount/refund revenue BEFORE removing VAT — what the company
	// actually collected from customers. Revenue (headline) is RevenueInclVat net of VAT, and
	// VatAmount = RevenueInclVat - Revenue. VAT is resolved per order from the destination
	// country and is 0 for export / pre-feature orders. All margins are computed on net Revenue.
	RevenueInclVat MetricWithComparison
	VatAmount      MetricWithComparison

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
	NewCustomers         MetricWithComparison // Aggregate of new_customers_by_day series
	// UniqueCustomers is the count of distinct buyer emails with a net-revenue order in the
	// period (the "покупатели" KPI) — distinct from NewCustomers (first-ever order in period).
	UniqueCustomers MetricWithComparison
	// NewVsReturning splits revenue / orders / AOV by new vs returning buyers (nil until assembled).
	NewVsReturning  *NewVsReturningSplit
	CLVDistribution CLVStats

	// Shipping / logistics metrics
	AvgShippingCost   MetricWithComparison
	TotalShippingCost MetricWithComparison

	// Margin (COGS from product.cost_price, base currency). Computed over the "costed"
	// revenue subset — line items whose product has a cost set. CostCoveragePct is that
	// subset's share of net product revenue, so a low coverage flags that the margins
	// only describe part of sales. COGS is refund-adjusted but not reduced by discounts.
	RevenueCost        MetricWithComparison // COGS: Σ(cost × qty), refund-adjusted
	GrossMargin        MetricWithComparison // costed net revenue − COGS
	GrossMarginPct     MetricWithComparison // GrossMargin / costed net revenue × 100
	PaymentFees        MetricWithComparison // Σ Stripe processing fees (base ccy), not refund-adjusted
	ContributionMargin MetricWithComparison // GrossMargin − TotalShippingCost − PaymentFees
	CostCoveragePct    float64              // % of net product revenue with a cost set
	// Product IDs sold in the period with no cost_price set, ranked by period revenue desc —
	// the products darkening the margins above. Empty when cost coverage is 100%.
	UncostedProductIds []int

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
	Caveat       string
	// SampleSize is the number of observations behind the metric (orders, sessions, customers…).
	// 0 = not populated for this metric. Lets the UI gate a metric on n instead of an arbitrary
	// display floor. MarginOfError is the 95% CI half-width in the metric's own units, populated
	// only for true count-proportions (e.g. conversion, promo usage); 0 = not computed.
	SampleSize    int
	MarginOfError float64
}

type GeographyMetric struct {
	Country       string
	State         *string
	City          *string
	Value         decimal.Decimal
	CompareValue  *decimal.Decimal
	Count         int
	CompareCount  *int             // optional, for comparison period
	SharePct      *float64         // percentage of total revenue
	AvgOrderValue *decimal.Decimal // average order value for this geography
	ChangePct     float64          // task 10: period-over-period revenue growth %, set only with a compare period
}

// RegionMetric aggregates by shipping region (AFRICA, AMERICAS, EUROPE, etc.)
type RegionMetric struct {
	Region string
	Value  decimal.Decimal
	Count  int
}

// CountryEconomicsRow is per-country profitability (analytics-v2 task 08): margin and its inputs,
// contribution, profit per order and average customer LTV. Revenue/Orders reconcile with the
// by_country breakdown (same shipping-address attribution). Margin covers only items with a cost
// snapshot, so CostCoveragePct/Orders gate its trust. Money fields except Revenue/TotalDiscount/LtvAvg
// are confidential cost data (stripped without costing:read).
type CountryEconomicsRow struct {
	Country            string
	Revenue            decimal.Decimal
	Orders             int
	RevenueCost        decimal.Decimal
	GrossMargin        decimal.Decimal
	GrossMarginPct     float64
	CostCoveragePct    float64
	ShippingCost       decimal.Decimal
	PaymentFees        decimal.Decimal
	ContributionMargin decimal.Decimal
	ProfitPerOrder     decimal.Decimal
	TotalDiscount      decimal.Decimal
	LtvAvg             decimal.Decimal
	LtvSample          int
}

// CountryLogisticsRow is per-country fulfilment and returns (analytics-v2 task 09). Durations are for
// orders placed in the period; delivered-based figures are gated by DeliveredSample. No confidential
// cost fields (AvgShippingCost is logistics, not COGS).
type CountryLogisticsRow struct {
	Country                  string
	AvgDaysPlacedToDelivered float64
	AvgDaysPlacedToShipped   float64
	OnTimeRatePct            float64
	DeliveredSample          int
	AvgShippingCost          decimal.Decimal
	RefundRatePct            float64
	RefundOrders             int
}

// CountryDemandRow is per-country demand mix (analytics-v2 task 09): conversion, new-vs-returning and
// top categories. Conversion is directional (geo-IP sessions vs shipping-address orders, undercounted
// by consent/ad-block) — comparable across countries, not against external benchmarks. Country is ISO-2
// (or "(unmatched)" for a GA4 country name with no ISO mapping).
type CountryDemandRow struct {
	Country            string
	Sessions           int
	Orders             int
	ConversionRatePct  float64
	AOV                decimal.Decimal
	NewCustomers       int
	ReturningCustomers int
	NewSharePct        float64
	TopCategories      []CategoryMetric
	Caveat             string
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
	// Margin fields, populated only for revenue/quantity product breakdowns (left zero
	// for view-based metrics). When HasCost is false the product's cost is unknown, so
	// consumers should render margins as N/A rather than as 100%.
	HasCost        bool            // product has a cost_price set
	UnitCost       decimal.Decimal // current per-unit COGS (base currency)
	RevenueCost    decimal.Decimal // Σ(cost × net qty), refund-adjusted
	GrossMargin    decimal.Decimal // Value − RevenueCost
	GrossMarginPct float64         // GrossMargin / Value × 100
}

type CategoryMetric struct {
	CategoryId          int
	CategoryName        string
	Value               decimal.Decimal
	Count               int
	CategoryDisplayName string
	// SharePct is this category's Value as a percentage of total category revenue in the period.
	SharePct float64
}

// PeakDay is the highest-net-revenue calendar day in a reporting period.
type PeakDay struct {
	Date    time.Time
	Revenue decimal.Decimal // net revenue (base currency) on that day
	Orders  int             // net-revenue orders placed that day
}

// RevenueForecast projects net revenue for the calendar month containing the period end, blending a
// day-of-week run-rate with a seasonal ratio-to-date when prior-year data exists. DB-only.
type RevenueForecast struct {
	Month              time.Time
	MtdActual          decimal.Decimal
	Forecast           decimal.Decimal
	ForecastLow        decimal.Decimal
	ForecastHigh       decimal.Decimal
	RunRate            decimal.Decimal
	Method             string
	ElapsedDays        int
	RemainingDays      int
	LastYearMonthTotal decimal.Decimal
	Caveat             string
}

// DeliverySection reports fulfilment speed for orders placed in the period (placed-cohort).
// Delivered-based figures are gated by DeliveredCoveragePct/DeliveredSample because the delivered
// status is operator-flipped and may be missing.
type DeliverySection struct {
	AvgDaysPlacedToShipped      float64
	AvgDaysShippedToDelivered   float64
	AvgDaysPlacedToDelivered    float64
	MedianDaysPlacedToDelivered float64
	OnTimeRatePct               float64
	OnTimeSample                int
	EtaCoveragePct              float64
	DeliveredCoveragePct        float64
	DeliveredSample             int
	ShippedSample               int
	AvgDeliveryDaysByWeek       []TimeSeriesPoint
	Caveat                      string
}

// OrderValueBandRow is one fixed order-value bucket (net-revenue basis) with its order/revenue
// shares and in-band AOV. Bands are fixed EUR ranges (not quantile) so they compare across periods.
type OrderValueBandRow struct {
	Label           string
	From            decimal.Decimal
	To              decimal.Decimal // zero = no upper bound
	Orders          int
	Revenue         decimal.Decimal
	OrdersSharePct  float64
	RevenueSharePct float64
	AvgOrderValue   decimal.Decimal
}

// ProfitabilitySection is the assembled "Profitability" tab (analytics-v2 task 07): margin and its
// erosion, acquisition economics (CPO / blended CAC / LTV / LTV·CAC), fulfilment cost per order,
// returns and the operating-result roll-up. Built from the same helpers as the dashboard so shared
// figures tie out. CPO/CAC/LTV·CAC divide by manually-entered media spend (channel_spend): HasSpend
// is false and they are zero when no spend is entered (N/A, not "free").
type ProfitabilitySection struct {
	GrossMargin         MetricWithComparison
	GrossMarginPct      MetricWithComparison
	CostCoveragePct     float64
	TotalDiscount       MetricWithComparison
	ProductSaleDiscount MetricWithComparison
	PromoCodeDiscount   MetricWithComparison
	DiscountRatePct     MetricWithComparison
	ContributionMargin  MetricWithComparison

	CPO                    MetricWithComparison
	BlendedCAC             MetricWithComparison
	HasSpend               bool
	LTV                    decimal.Decimal
	LTVCACRatio            float64
	FulfilmentCostPerOrder MetricWithComparison

	RefundRate      MetricWithComparison
	TotalRefunded   MetricWithComparison
	OpexTotal       decimal.Decimal
	MarketingSpend  decimal.Decimal
	OperatingResult decimal.Decimal
	OpexCaveat      string
	Caveat          string
}

// NewVsReturningSplit splits a period's net revenue, orders and AOV by whether the buyer's
// first-ever order (any status) falls in the period. new+returning revenue reconciles with
// headline Revenue. Daily series carry revenue in Value and order count in Count.
type NewVsReturningSplit struct {
	NewOrders          MetricWithComparison
	NewRevenue         MetricWithComparison
	NewAOV             MetricWithComparison
	ReturningOrders    MetricWithComparison
	ReturningRevenue   MetricWithComparison
	ReturningAOV       MetricWithComparison
	NewRevenueSharePct float64
	NewRevenueByDay    []TimeSeriesPoint
	ReturningByDay     []TimeSeriesPoint
}

type CrossSellPair struct {
	ProductAId   int
	ProductBId   int
	ProductAName string
	ProductBName string
	Count        int     // orders containing both A and B (distinct orders)
	Support      float64 // P(A∧B): Count / total orders
	Confidence   float64 // P(B|A): Count / orders containing A
	Lift         float64 // Support / (P(A)·P(B)); >1 ⇒ bought together more than chance
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
	Mean       decimal.Decimal
	Median     decimal.Decimal
	P90        decimal.Decimal
	SampleSize int
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
	Date                 time.Time
	ProductID            string
	ProductName          string
	ImageViews           int64
	ZoomEvents           int64
	Scroll75             int64
	Scroll100            int64
	AvgTimeOnPageSeconds float64
}

type ProductEngagementBubbleRow struct {
	ProductID            string
	ProductName          string
	TotalImageViews      int64
	TotalZoomEvents      int64
	TotalScroll75        int64
	TotalScroll100       int64
	ZoomRatePct          float64
	Scroll75RatePct      float64
	Scroll100RatePct     float64
	AvgTimeOnPageSeconds float64
}

type ProductEngagementMetricsPct struct {
	AvgZoomRatePct       float64
	AvgScroll75RatePct   float64
	AvgScroll100RatePct  float64
	AvgTimeOnPageSeconds float64
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

type HeroFunnelAggregate struct {
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
	CohortSize  int64           `db:"cohort_size"`
	M1          int64           `db:"m1"`
	M2          int64           `db:"m2"`
	M3          int64           `db:"m3"`
	M4          int64           `db:"m4"`
	M5          int64           `db:"m5"`
	M6          int64           `db:"m6"`
	M1Revenue   decimal.Decimal `db:"m1_revenue"`
	M2Revenue   decimal.Decimal `db:"m2_revenue"`
	M3Revenue   decimal.Decimal `db:"m3_revenue"`
	M4Revenue   decimal.Decimal `db:"m4_revenue"`
	M5Revenue   decimal.Decimal `db:"m5_revenue"`
	M6Revenue   decimal.Decimal `db:"m6_revenue"`
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
	// Margin fields (mirror ProductMetric), computed in Go after the query — not scanned.
	// HasCost=false ⇒ no cost_price, so margins are N/A rather than a misleading 100%.
	HasCost        bool            `db:"-"`
	UnitCost       decimal.Decimal `db:"-"`
	RevenueCost    decimal.Decimal `db:"-"`
	GrossMargin    decimal.Decimal `db:"-"`
	GrossMarginPct float64         `db:"-"`
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

// CustomerSegmentRow represents AOV-based customer value segmentation.
type CustomerSegmentRow struct {
	Email         string
	OrderCount    int64
	TotalRevenue  decimal.Decimal
	AvgOrderValue decimal.Decimal
	Segment       string // "high", "medium", "low"
}

// RFMSegmentRow represents RFM (Recency, Frequency, Monetary) analysis results.
type RFMSegmentRow struct {
	Email          string
	RecencyScore   int    // 1-5 (5 = purchased recently)
	FrequencyScore int    // 1-5 (5 = frequent buyer)
	MonetaryScore  int    // 1-5 (5 = high spender)
	RFMLabel       string // "Champions", "Loyal", "At-Risk", "Lost", etc.
	LastPurchase   time.Time
	OrderCount     int64
	TotalSpent     decimal.Decimal
}

// --- Inventory Health entity types ---

type InventoryHealthRow struct {
	ProductID     int     `db:"product_id"`
	ProductName   string  `db:"product_name"`
	SizeID        int     `db:"size_id"`
	SizeName      string  `db:"size_name"`
	Quantity      int     `db:"quantity"`
	AvgDailySales float64 `db:"avg_daily_sales"`
	DaysOnHand    float64 `db:"days_on_hand"` // days of cover at the current sales rate
	// Optional per-SKU targets (from inventory_target); NULL when unset.
	ReorderPoint    sql.NullInt64 `db:"reorder_point"`
	TargetDaysCover sql.NullInt64 `db:"target_days_cover"`
	LeadTimeDays    sql.NullInt64 `db:"lead_time_days"`
	// Server-side decision, computed after the query.
	HasTarget    bool `db:"-"` // any target is set for this SKU
	NeedsReorder bool `db:"-"` // stock at/below reorder point, or cover below lead time / target
	IsSelling    bool `db:"-"` // sold >0 units in the window; days_on_hand is a sentinel when false
}

// InventoryTargetInsert is an admin-supplied per-SKU reorder target. A nil field leaves
// that threshold unset (no trigger on that dimension).
type InventoryTargetInsert struct {
	ProductID       int           `db:"product_id"`
	SizeID          int           `db:"size_id"`
	ReorderPoint    sql.NullInt64 `db:"reorder_point"`
	TargetDaysCover sql.NullInt64 `db:"target_days_cover"`
	LeadTimeDays    sql.NullInt64 `db:"lead_time_days"`
}

type SizeRunEfficiencyRow struct {
	ProductID        int     `db:"product_id"`
	ProductName      string  `db:"product_name"`
	TotalSizes       int     `db:"total_sizes"`
	SoldThroughSizes int     `db:"sold_through_sizes"`
	EfficiencyPct    float64 `db:"efficiency_pct"` // size-run coverage: % of sizes with any sale
	// True unit sell-through (distinct from the size-coverage EfficiencyPct above).
	UnitsBought    int64   `db:"units_bought"`     // Σ initial stock across sizes (on-hand + net sold)
	UnitsSold      int64   `db:"units_sold"`       // Σ net units sold across sizes
	SellThroughPct float64 `db:"sell_through_pct"` // UnitsSold / UnitsBought × 100
}

// SellThroughByDropRow rolls a release/drop cohort (product.collection) into decision-grade
// totals. Lifetime (current-state), not windowed: sell-through is inherently cumulative.
type SellThroughByDropRow struct {
	Collection     string          `db:"collection"`
	ProductCount   int             `db:"product_count"`
	UnitsSold      int64           `db:"units_sold"`
	UnitsRemaining int64           `db:"units_remaining"`
	UnitsBought    int64           `db:"units_bought"` // units_sold + units_remaining (initial-stock proxy)
	SellThroughPct float64         `db:"sell_through_pct"`
	Revenue        decimal.Decimal `db:"revenue"`
	// Margin over the costed subset (products with a cost_price). RevenueCost/CostedRevenue are
	// scanned; HasCost/GrossMargin/GrossMarginPct are derived in Go (mirrors SlowMoverRow). When
	// nothing in the drop has a cost, HasCost=false and the margins are N/A rather than a
	// misleading 0/100.
	RevenueCost    decimal.Decimal `db:"revenue_cost"`   // Σ(cost_price × units_sold) over costed products
	CostedRevenue  decimal.Decimal `db:"costed_revenue"` // Σ(revenue) over costed products
	HasCost        bool            `db:"-"`
	GrossMargin    decimal.Decimal `db:"-"`
	GrossMarginPct float64         `db:"-"`
	// DaysTo50Pct is the whole days from the drop's first sale to 50% sell-through; invalid when
	// the drop hasn't reached 50% yet (or has no sales).
	DaysTo50Pct sql.NullInt64 `db:"-"`
}

// --- Dashboard (decision-grade summary) ---

// AlertSeverity ranks a dashboard alert. Kept as small string codes so the store can emit
// them without importing the proto enum.
type AlertSeverity string

const (
	AlertSeverityInfo     AlertSeverity = "info"
	AlertSeverityWarning  AlertSeverity = "warning"
	AlertSeverityCritical AlertSeverity = "critical"
)

// AlertThresholds are the operator-tunable thresholds behind the dashboard alerts, loaded
// from the alert_setting table (with DefaultAlertThresholds as the fallback).
type AlertThresholds struct {
	CoverageWarnPct        float64 // warn when cost coverage (% of revenue with a cost) is below this
	RefundRateWarnPct      float64 // warn when refund rate is at/above this
	RateFloorN             int     // min orders before any rate-based alert fires (significance floor)
	ContributionTrustPct   float64 // only trust the contribution-margin sign at/above this coverage
	GA4CoverageWarnPct     float64 // warn when GA4 tracking coverage (% of DB revenue GA4 sees) is below this (task 20)
	ProductionRunStaleDays int     // warn when an open production run is older than this many days (NF-09)
	// AcctPostingLagHours warns when the accounting module has unprocessed acct_event rows or
	// material movements stuck behind the acctposting worker's checkpoint older than this many hours
	// (accounting Step 8 / 07-worker-config.md "Health-алерты"). It is carried by the AlertSettings
	// proto/DTO round-trip (field 7) and is editable end-to-end via UpsertAlertSettings, the same as
	// every other threshold in this struct. Unlike ProductionRunStaleDays, <= 0 does NOT disable the
	// check — it falls back to the default (24) as a guard, so an unset/zeroed value (e.g. a client
	// build that predates this field) never silently goes dark with no operator intent behind it (see
	// metrics.Store.GetAcctPostingLag).
	AcctPostingLagHours int
}

// DefaultAlertThresholds returns the built-in defaults (also the seed values of alert_setting).
func DefaultAlertThresholds() AlertThresholds {
	return AlertThresholds{
		CoverageWarnPct:        70,
		RefundRateWarnPct:      10,
		RateFloorN:             30,
		ContributionTrustPct:   50,
		GA4CoverageWarnPct:     80,
		ProductionRunStaleDays: 60,
		AcctPostingLagHours:    24,
	}
}

// DashboardAlert is a server-computed, threshold-driven alert for the dashboard.
type DashboardAlert struct {
	Severity AlertSeverity
	Code     string // stable machine key, e.g. "low_cost_coverage"
	Title    string
	Detail   string
}

// Dashboard is the small, DB-trusted decision payload: headline figures + server alerts +
// short action lists. It is intentionally cheap (no ~90-field BusinessMetrics god-object).
type Dashboard struct {
	Period             TimeRange
	Revenue            decimal.Decimal
	Orders             int
	GrossMargin        decimal.Decimal
	GrossMarginPct     float64
	ContributionMargin decimal.Decimal
	CostCoveragePct    float64
	Caveat             string
	UncostedProductIds []int
	// GA4Revenue is the GA4-reported revenue for the period (analytics cache), and
	// TrackingCoveragePct is 100 * GA4Revenue / DB gross revenue — the share of real revenue
	// the client-side tracking saw (task 20 step 1). 0 when DB revenue is 0 / unknown.
	GA4Revenue          decimal.Decimal
	TrackingCoveragePct float64
	// Operating result (task 22): the "honest" total under the contribution margin.
	// OperatingResult = ContributionMargin − OpexTotal − MarketingSpend. OpexTotal is the
	// day-pro-rated fixed-cost journal for the period; MarketingSpend is channel_spend for the
	// period (subtracted here, not in contribution — ad spend isn't variable per order, and this
	// avoids double-counting with ROAS). OpexCaveat is set when no OPEX is recorded for the
	// period's months, so the operating result is understood as incomplete (coverage honesty).
	OperatingResult decimal.Decimal
	OpexTotal       decimal.Decimal
	MarketingSpend  decimal.Decimal
	OpexCaveat      string
	Alerts          []DashboardAlert
	TopByMargin     []ProductMetric      // top revenue products re-ranked by gross margin €
	Reorder         []InventoryHealthRow // SKUs flagged needs_reorder, most urgent first
	Clear           []SlowMoverRow       // slow movers to clear
	Drops           []SellThroughByDropRow
	// Compare, when non-nil, is the headline snapshot over ComparePeriod (previous period or same
	// period last year), set by the handler from GetDashboardHeadline when the request asked for a
	// comparison. Only the six higher-is-better headline scalars are recomputed for the compare
	// window — not the action lists or alerts. The change percentages are derived at DTO time.
	ComparePeriod TimeRange
	Compare       *DashboardHeadline
}

// DashboardHeadline is the six higher-is-better decision figures of the dashboard, computed
// cheaply for a single window. It is the full payload of the comparison period
// (Metrics.GetDashboardHeadline) and a subset of the primary Dashboard.
type DashboardHeadline struct {
	Revenue            decimal.Decimal
	Orders             int
	GrossMargin        decimal.Decimal
	GrossMarginPct     float64
	ContributionMargin decimal.Decimal
	OperatingResult    decimal.Decimal
}

// --- Slow Movers ---

type SlowMoverRow struct {
	ProductID     int             `db:"product_id"`
	ProductName   string          `db:"product_name"`
	Revenue       decimal.Decimal `db:"revenue"`
	UnitsSold     int64           `db:"units_sold"`
	DaysInStock   float64         `db:"days_in_stock"`
	LastSaleDate  *time.Time      `db:"last_sale_date"`
	ProductHidden bool            `db:"product_hidden"`
	TotalViews    int64           `db:"total_views"`
	// Margin fields (mirror ProductMetric), computed in Go after the query — not scanned.
	// HasCost=false ⇒ no cost_price, so margins are N/A rather than a misleading 100%.
	HasCost        bool            `db:"-"`
	UnitCost       decimal.Decimal `db:"-"`
	RevenueCost    decimal.Decimal `db:"-"`
	GrossMargin    decimal.Decimal `db:"-"`
	GrossMarginPct float64         `db:"-"`
}

// --- Margin by Style (task 15) ---

// MarginByStyleRow rolls per-SKU sales up to the STYLE (tech card) a product's
// primary_tech_card_id points at, so a style with several colourway SKUs is ONE row instead
// of many uncorrelated ones. Products with no primary tech card collapse into a single
// "no style" row (TechCardID = 0), the same uncosted-honesty as products without a cost.
// Margin fields mirror ProductMetric (N/A when the sold SKUs carry no cost).
type MarginByStyleRow struct {
	TechCardID    int             `db:"tech_card_id"` // 0 = products with no primary style
	StyleNumber   string          `db:"style_number"`
	Name          string          `db:"name"`
	Revenue       decimal.Decimal `db:"revenue"`
	UnitsSold     int64           `db:"units_sold"`
	ColorwayCount int             `db:"colorway_count"` // distinct products that sold under this style
	// Margin fields (mirror ProductMetric), computed in Go after the query — not scanned.
	HasCost        bool            `db:"-"`
	UnitCost       decimal.Decimal `db:"-"`
	RevenueCost    decimal.Decimal `db:"-"`
	GrossMargin    decimal.Decimal `db:"-"`
	GrossMarginPct float64         `db:"-"`
}

// --- COGS structure (task 15) ---

// CogsStructureRow is one component of the cost of goods SOLD in a period, attributed from
// each product's cost_breakdown snapshot (materials / cmt / hardware / packaging / logistics
// / overhead). The actual line COGS (cost_price_at_sale × net qty) is split by the breakdown's
// component proportions, so the components always sum to the reported COGS; sold units whose
// product has no breakdown (manual cost, or seeded before the column existed) collect in a
// single "unattributed" component so coverage stays honest.
type CogsStructureRow struct {
	Component string          `db:"component"` // materials|cmt|hardware|packaging|logistics|overhead|unattributed
	Amount    decimal.Decimal `db:"amount"`    // base-currency (EUR) Σ over the period, refund-adjusted
	Pct       float64         `db:"-"`         // share of total COGS, filled in Go
}

// --- Inventory valuation (task 16) ---

// InventoryValuationRow is one product's frozen stock value: on-hand units × current per-unit
// cost. SoldUnits is the product's net units sold in the reporting window (0 ⇒ dead stock — it
// is sitting in the warehouse as unsold cost).
type InventoryValuationRow struct {
	ProductID   int             `db:"product_id"`
	ProductName string          `db:"product_name"`
	OnHand      int64           `db:"on_hand"`
	UnitCost    decimal.Decimal `db:"-"`
	Value       decimal.Decimal `db:"-"`
	SoldUnits   int64           `db:"-"`
}

// InventoryValuation is the money view of the warehouse: how much cost is frozen in stock, how
// much of it is dead (unsold in the window), and how much was written off in the period. Stock
// is valued at the CURRENT plan cost_price (v1 — the only cost available); products without a
// cost are counted honestly as uncosted (value unknown), never as zero.
type InventoryValuation struct {
	TotalStockValue       decimal.Decimal         // Σ cost_price × on_hand over COSTED products, base EUR
	TotalOnHandUnits      int64                   // Σ on_hand over ALL in-stock products
	CostedOnHandUnits     int64                   // on_hand of products that HAVE a cost
	UncostedStockUnits    int64                   // on_hand of products with NO cost (value unknown)
	UncostedStockProducts int                     // distinct in-stock products with no cost
	CoveragePct           float64                 // costed_on_hand_units / total_on_hand_units × 100
	TopByValue            []InventoryValuationRow // costed products ranked by frozen value
	DeadStock             []InventoryValuationRow // costed, in-stock, unsold in window — by value
	WriteOffsValue        decimal.Decimal         // Σ |Δqty| × cost_price for damage/loss in the period
	WriteOffsUnits        int64                   // units written off (damage/loss) in the period

	// Raw-material warehouse (NF-09 valuation v2). Materials are valued at their moving-average
	// unit cost in base currency; materials with stock but no average are counted, not valued.
	RawMaterialsValue       decimal.Decimal        // Σ on_hand × avg_unit_cost_base over costed materials with stock, base EUR
	RawMaterialsCount       int                    // materials with on_hand > 0
	RawUncostedCount        int                    // materials with stock but no average (value unknown)
	WipValue                decimal.Decimal        // work-in-progress: materials issued into OPEN runs and not returned, base EUR
	WriteOffsMaterialsValue decimal.Decimal        // material write-offs (damage/loss/defect) in the period, base EUR
	TopMaterialsByValue     []MaterialValuationRow // costed in-stock materials ranked by frozen value
}

// StyleSampleSummary counts a style's samples and the warehouse-material cost they consumed
// (NF-09, informational — sample materials are R&D spend, not folded into the style's sales net).
// HasUncosted is set when a sample material issue had no unit cost, so the figure understates.
type StyleSampleSummary struct {
	Count             int             `db:"count"`
	MaterialsCostBase decimal.Decimal `db:"materials_cost_base"`
	HasUncosted       bool            `db:"has_uncosted"`
}

// StyleMaterialsFromStock is the net warehouse-material cost issued into a style's production runs
// (NF-09), from the material ledger — the actuals side of production cost. HasUncosted flags issues
// with no unit cost (the value understates).
type StyleMaterialsFromStock struct {
	Base        decimal.Decimal `db:"base"`
	HasUncosted bool            `db:"has_uncosted"`
}

// MaterialValuationRow is one raw-material line in the warehouse money view (NF-09).
type MaterialValuationRow struct {
	MaterialId      int             `db:"material_id"`
	Name            string          `db:"name"`
	Unit            string          `db:"unit"`
	OnHand          decimal.Decimal `db:"on_hand"`
	AvgUnitCostBase decimal.Decimal `db:"avg_unit_cost_base"`
	Value           decimal.Decimal `db:"value"`
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
	PageLocation string // device category: desktop, mobile, tablet, unknown
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
	// Spend and ROAS are enriched from channel_spend (operator-entered, base currency).
	// Spend is zero when none is recorded; ROAS (revenue / spend) is set only when spend > 0.
	Spend decimal.Decimal
	ROAS  float64
	// CAC = spend / conversions (cost to acquire a converting customer), set only when spend
	// and conversions are both > 0. Note: attribution-based, so directional for an
	// organic-heavy channel mix rather than an exact acquisition cost.
	CAC float64
}

// ChannelSpendInsert is an operator-entered marketing spend row for one channel on one day.
type ChannelSpendInsert struct {
	Date        time.Time       `db:"date"`
	UTMSource   string          `db:"utm_source"`
	UTMMedium   string          `db:"utm_medium"`
	UTMCampaign string          `db:"utm_campaign"`
	Amount      decimal.Decimal `db:"amount"`
	Currency    string          `db:"currency"`
}

// ChannelSpendRow is spend aggregated by channel over a period (base currency), used to
// compute ROAS against campaign-attribution revenue.
type ChannelSpendRow struct {
	UTMSource   string          `db:"utm_source"`
	UTMMedium   string          `db:"utm_medium"`
	UTMCampaign string          `db:"utm_campaign"`
	Spend       decimal.Decimal `db:"spend"`
}

// OrderChannelRow maps a GA4 client_id (= customer_order.ga_client_id) to its last non-direct UTM
// channel (task 20 step 2). Produced by the BQ precompute, cached in bq_order_channel, and joined to
// orders for server-side, settled-revenue attribution. Date is the session date of that last touch.
type OrderChannelRow struct {
	ClientID    string    `db:"client_id"`
	Date        time.Time `db:"date"`
	UTMSource   string    `db:"utm_source"`
	UTMMedium   string    `db:"utm_medium"`
	UTMCampaign string    `db:"utm_campaign"`
}

// ChannelSettledRow is one channel's SETTLED-revenue attribution over a period (task 20 step 2):
// orders whose GA client_id maps to this UTM triple, their settled base revenue, and how many were
// placed by first-time customers. Spend/ROAS/CAC are layered on in the handler from channel_spend.
type ChannelSettledRow struct {
	UTMSource      string          `db:"utm_source"`
	UTMMedium      string          `db:"utm_medium"`
	UTMCampaign    string          `db:"utm_campaign"`
	SettledRevenue decimal.Decimal `db:"settled_revenue"`
	Orders         int64           `db:"orders"`
	NewCustomers   int64           `db:"new_customers"`
}

// ValidOpexCategories is the closed set of OPEX categories (validated in dto rather than a DB
// CHECK, so the set can evolve without a migration). marketing spend is deliberately NOT here —
// it lives in channel_spend and is subtracted separately, so ROAS and the operating result
// don't double-count it.
var ValidOpexCategories = map[string]struct{}{
	"salaries":           {},
	"rent":               {},
	"software":           {},
	"marketing_other":    {},
	"production_content": {},
	// NF-08 additions (the set is dto-validated, so it extends without a migration):
	"taxes":                 {},
	"bank_fees":             {},
	"professional_services": {}, // accountant / lawyer
	"logistics_office":      {}, // office/ops logistics (not order shipping)
	"employer_social":       {}, // employer-side social contributions (ZUS/NI) — 6335, split from salaries
	"other":                 {},
}
