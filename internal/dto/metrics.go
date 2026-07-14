package dto

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	shopspring "github.com/shopspring/decimal"
	"google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fractionToPct converts a 0–1 ratio into a 0–100 percentage so it matches the
// convention used by the business KPIs (conversionRate, refundRate, bounceRate).
// The BigQuery funnel rates (add-to-cart rate, cart abandonment) are produced as
// 0–1 fractions (SAFE_DIVIDE / SanitizeRate); this is applied at the serialization
// boundary so the whole API reports rates in 0–100.
func fractionToPct(v float64) float64 {
	return v * 100
}

// ConvertEntityBusinessMetricsToPb maps the flat internal entity into the typed, provenance-
// grouped API message (commerce / margin / traffic / email). The entity stays flat; only the
// wire shape is grouped.
func ConvertEntityBusinessMetricsToPb(m *entity.BusinessMetrics) *pb_admin.BusinessMetrics {
	if m == nil {
		return nil
	}
	pb := &pb_admin.BusinessMetrics{
		Period:   timeRangeToPb(m.Period),
		Commerce: commerceCoreMetricsToPb(m),
		Margin:   marginMetricsToPb(m),
		Traffic:  trafficMetricsToPb(m),
		Email:    emailMetricsToPb(m),
	}
	if m.ComparePeriod != nil && (!m.ComparePeriod.From.IsZero() || !m.ComparePeriod.To.IsZero()) {
		pb.ComparePeriod = timeRangeToPb(*m.ComparePeriod)
	}
	return pb
}

// commerceCoreMetricsToPb builds the DB-trusted commerce view (sales, customers, discounts,
// shipping) with its breakdowns and daily series.
func commerceCoreMetricsToPb(m *entity.BusinessMetrics) *pb_admin.CommerceCoreMetrics {
	return &pb_admin.CommerceCoreMetrics{
		Revenue:              metricWithComparisonToPb(m.Revenue),
		OrdersCount:          metricWithComparisonToPb(m.OrdersCount),
		TotalPlacedOrders:    metricWithComparisonToPb(m.TotalPlacedOrders),
		AvgOrderValue:        metricWithComparisonToPb(m.AvgOrderValue),
		ItemsPerOrder:        metricWithComparisonToPb(m.ItemsPerOrder, false, true), // round to int so "1 vs 1" shows 0%
		RefundRate:           metricWithComparisonToPb(m.RefundRate, true),           // lower is better
		PromoUsageRate:       metricWithComparisonToPb(m.PromoUsageRate),
		GrossRevenue:         metricWithComparisonToPb(m.GrossRevenue),
		TotalRefunded:        metricWithComparisonToPb(m.TotalRefunded, true), // lower is better
		TotalDiscount:        metricWithComparisonToPb(m.TotalDiscount),
		ProductSaleDiscount:  metricWithComparisonToPb(m.ProductSaleDiscount),
		PromoCodeDiscount:    metricWithComparisonToPb(m.PromoCodeDiscount),
		RevenueInclVat:       metricWithComparisonToPb(m.RevenueInclVat),
		VatAmount:            metricWithComparisonToPb(m.VatAmount),
		UniqueCustomers:      metricWithComparisonToPb(m.UniqueCustomers),
		PeakDay:              peakDayToPb(m.PeakDay),
		DiscountRatePct:      metricWithComparisonToPb(m.DiscountRatePct, true), // lower is better
		NewVsReturning:       newVsReturningSplitToPb(m.NewVsReturning),
		NewSubscribers:       metricWithComparisonToPb(m.NewSubscribers),
		NewCustomers:         metricWithComparisonToPb(m.NewCustomers),
		RepeatCustomersRate:  metricWithComparisonToPb(m.RepeatCustomersRate),
		AvgOrdersPerCustomer: metricWithComparisonToPb(m.AvgOrdersPerCustomer),
		AvgDaysBetweenOrders: metricWithComparisonToPb(m.AvgDaysBetweenOrders),
		AvgShippingCost:      metricWithComparisonToPb(m.AvgShippingCost, false, false, int32(2)),
		TotalShippingCost:    metricWithComparisonToPb(m.TotalShippingCost, false, false, int32(2)),
		ClvDistribution:      clvStatsToPb(m.CLVDistribution),

		RevenueByCountry:               geographyMetricsToPb(m.RevenueByCountry),
		RevenueByCity:                  geographyMetricsToPb(m.RevenueByCity),
		RevenueByRegion:                regionMetricsToPb(m.RevenueByRegion),
		AvgOrderByCountry:              geographyMetricsToPb(m.AvgOrderByCountry),
		RevenueByCurrency:              currencyMetricsToPb(m.RevenueByCurrency),
		RevenueByPaymentMethod:         paymentMethodMetricsToPb(m.RevenueByPaymentMethod),
		TopProductsByRevenue:           productMetricsToPb(m.TopProductsByRevenue),
		TopProductsByQuantity:          productMetricsToPb(m.TopProductsByQuantity),
		RevenueByCategory:              categoryMetricsToPb(m.RevenueByCategory),
		CrossSellPairs:                 crossSellPairsToPb(m.CrossSellPairs),
		RevenueByPromo:                 promoMetricsToPb(m.RevenueByPromo),
		OrdersByStatus:                 statusCountsToPb(m.OrdersByStatus),
		RevenueByDay:                   timeSeriesToPb(m.RevenueByDay),
		OrdersByDay:                    timeSeriesToPb(m.OrdersByDay),
		SubscribersByDay:               timeSeriesToPb(m.SubscribersByDay),
		GrossRevenueByDay:              timeSeriesToPb(m.GrossRevenueByDay),
		RefundsByDay:                   timeSeriesToPb(m.RefundsByDay),
		AvgOrderValueByDay:             timeSeriesToPb(m.AvgOrderValueByDay),
		UnitsSoldByDay:                 timeSeriesToPb(m.UnitsSoldByDay),
		NewCustomersByDay:              timeSeriesToPb(m.NewCustomersByDay),
		ReturningCustomersByDay:        timeSeriesToPb(m.ReturningCustomersByDay),
		ShippedByDay:                   timeSeriesToPb(m.ShippedByDay),
		DeliveredByDay:                 timeSeriesToPb(m.DeliveredByDay),
		RevenueByDayCompare:            timeSeriesToPb(m.RevenueByDayCompare),
		OrdersByDayCompare:             timeSeriesToPb(m.OrdersByDayCompare),
		SubscribersByDayCompare:        timeSeriesToPb(m.SubscribersByDayCompare),
		GrossRevenueByDayCompare:       timeSeriesToPb(m.GrossRevenueByDayCompare),
		RefundsByDayCompare:            timeSeriesToPb(m.RefundsByDayCompare),
		AvgOrderValueByDayCompare:      timeSeriesToPb(m.AvgOrderValueByDayCompare),
		UnitsSoldByDayCompare:          timeSeriesToPb(m.UnitsSoldByDayCompare),
		NewCustomersByDayCompare:       timeSeriesToPb(m.NewCustomersByDayCompare),
		ReturningCustomersByDayCompare: timeSeriesToPb(m.ReturningCustomersByDayCompare),
		ShippedByDayCompare:            timeSeriesToPb(m.ShippedByDayCompare),
		DeliveredByDayCompare:          timeSeriesToPb(m.DeliveredByDayCompare),
	}
}

// marginMetricsToPb builds the DB-trusted COGS / margin view. COGS is a volume-scaling cost
// like shipping — neutral, not "lower is better".
func marginMetricsToPb(m *entity.BusinessMetrics) *pb_admin.MarginMetrics {
	return &pb_admin.MarginMetrics{
		RevenueCost:        metricWithComparisonToPb(m.RevenueCost, false, false, int32(2)),
		GrossMargin:        metricWithComparisonToPb(m.GrossMargin, false, false, int32(2)),
		GrossMarginPct:     metricWithComparisonToPb(m.GrossMarginPct, false, false, int32(2)),
		PaymentFees:        metricWithComparisonToPb(m.PaymentFees, false, false, int32(2)),
		ContributionMargin: metricWithComparisonToPb(m.ContributionMargin, false, false, int32(2)),
		CostCoveragePct:    m.CostCoveragePct,
		UncostedProductIds: intsToInt32(m.UncostedProductIds),
	}
}

// trafficMetricsToPb builds the GA4-estimated traffic / engagement view.
func trafficMetricsToPb(m *entity.BusinessMetrics) *pb_admin.TrafficMetrics {
	return &pb_admin.TrafficMetrics{
		Sessions:                   metricWithComparisonToPb(m.Sessions),
		Users:                      metricWithComparisonToPb(m.Users),
		NewUsers:                   metricWithComparisonToPb(m.NewUsers),
		PageViews:                  metricWithComparisonToPb(m.PageViews),
		BounceRate:                 metricWithComparisonToPb(m.BounceRate, true),                           // lower is better
		AvgSessionDuration:         metricWithComparisonToPb(m.AvgSessionDuration, false, false, int32(1)), // 1 decimal place
		PagesPerSession:            metricWithComparisonToPb(m.PagesPerSession),
		ConversionRate:             metricWithComparisonToPb(m.ConversionRate),
		RevenuePerSession:          metricWithComparisonToPb(m.RevenuePerSession),
		SessionsByCountry:          geographySessionMetricsToPb(m.SessionsByCountry),
		TopProductsByViews:         productViewMetricsToPb(m.TopProductsByViews),
		TrafficBySource:            trafficSourceMetricsToPb(m.TrafficBySource),
		TrafficByDevice:            deviceMetricsToPb(m.TrafficByDevice),
		SessionsByDay:              timeSeriesToPb(m.SessionsByDay),
		UsersByDay:                 timeSeriesToPb(m.UsersByDay),
		PageViewsByDay:             timeSeriesToPb(m.PageViewsByDay),
		ConversionRateByDay:        timeSeriesToPb(m.ConversionRateByDay),
		SessionsByDayCompare:       timeSeriesToPb(m.SessionsByDayCompare),
		UsersByDayCompare:          timeSeriesToPb(m.UsersByDayCompare),
		PageViewsByDayCompare:      timeSeriesToPb(m.PageViewsByDayCompare),
		ConversionRateByDayCompare: timeSeriesToPb(m.ConversionRateByDayCompare),
	}
}

// emailMetricsToPb builds the Resend email-delivery view.
func emailMetricsToPb(m *entity.BusinessMetrics) *pb_admin.EmailMetrics {
	return &pb_admin.EmailMetrics{
		EmailsSent:        metricWithComparisonToPb(m.EmailsSent),
		EmailsDelivered:   metricWithComparisonToPb(m.EmailsDelivered),
		EmailDeliveryRate: metricWithComparisonToPb(m.EmailDeliveryRate),
		EmailOpenRate:     metricWithComparisonToPb(m.EmailOpenRate),
		EmailClickRate:    metricWithComparisonToPb(m.EmailClickRate),
		EmailBounceRate:   metricWithComparisonToPb(m.EmailBounceRate, true), // lower is better
	}
}

func timeRangeToPb(tr entity.TimeRange) *pb_admin.TimeRange {
	return &pb_admin.TimeRange{
		From: timestamppb.New(tr.From),
		To:   timestamppb.New(tr.To),
	}
}

// metricWithComparisonToPb converts entity.MetricWithComparison to protobuf.
// Optional parameters (variadic):
//   - opts[0] (bool): lowerIsBetter — when true, negative change is good (e.g., refund rate, bounce rate)
//   - opts[1] (bool): roundToInt — when true, rounds values to 0 decimal places before computing delta
//   - opts[2] (int32): roundToDecimalPlaces — when > 0, rounds values to N decimal places before computing delta (overrides roundToInt)
func metricWithComparisonToPb(m entity.MetricWithComparison, opts ...any) *pb_admin.MetricWithComparison {
	lowerIsBetter := false
	roundToInt := false
	var roundToDecimalPlaces int32 = -1

	if len(opts) > 0 {
		if v, ok := opts[0].(bool); ok {
			lowerIsBetter = v
		}
	}
	if len(opts) > 1 {
		if v, ok := opts[1].(bool); ok {
			roundToInt = v
		}
	}
	if len(opts) > 2 {
		if v, ok := opts[2].(int32); ok {
			roundToDecimalPlaces = v
		}
	}

	// Apply rounding to match display precision
	displayValue := m.Value
	var displayCompareValue *shopspring.Decimal
	if m.CompareValue != nil {
		cv := *m.CompareValue
		displayCompareValue = &cv
	}

	var decimalPlaces int32 = -1
	if roundToDecimalPlaces >= 0 {
		decimalPlaces = roundToDecimalPlaces
		displayValue = displayValue.Round(roundToDecimalPlaces)
		if displayCompareValue != nil {
			rounded := displayCompareValue.Round(roundToDecimalPlaces)
			displayCompareValue = &rounded
		}
	} else if roundToInt {
		decimalPlaces = 0
		displayValue = displayValue.Round(0)
		if displayCompareValue != nil {
			rounded := displayCompareValue.Round(0)
			displayCompareValue = &rounded
		}
	}

	// Always derive changePct from displayValue vs displayCompareValue (ensures consistency with displayed numbers)
	var changePct float64
	var changeAbsolute float64
	if displayCompareValue != nil && !displayCompareValue.IsZero() {
		if pct := computeChangePct(displayValue, *displayCompareValue); pct != nil {
			changePct = *pct
		} else {
			// e.g. Float64 inexact on diff — use store-computed pct
			changePct = ptrFloat64ToVal(m.ChangePct)
		}
		// Compute absolute delta (current - previous) for all metrics
		changeAbsolute = displayValue.Sub(*displayCompareValue).Round(2).InexactFloat64()
	} else {
		changePct = ptrFloat64ToVal(m.ChangePct)
	}

	// Format decimal strings with fixed precision when rounding was applied (preserves trailing zeros)
	var valueStr, compareValueStr string
	if decimalPlaces >= 0 {
		valueStr = displayValue.StringFixed(decimalPlaces)
		if displayCompareValue != nil {
			compareValueStr = displayCompareValue.StringFixed(decimalPlaces)
		}
	} else {
		valueStr = displayValue.String()
		if displayCompareValue != nil {
			compareValueStr = displayCompareValue.String()
		}
	}

	pb := &pb_admin.MetricWithComparison{
		Value:          &decimal.Decimal{Value: valueStr},
		ChangePct:      changePct,
		ChangeAbsolute: changeAbsolute,
	}
	if displayCompareValue != nil {
		pb.CompareValue = &decimal.Decimal{Value: compareValueStr}
	}
	if lowerIsBetter {
		pb.LowerIsBetter = true
	}
	if m.Caveat != "" {
		pb.Caveat = m.Caveat
	}
	if m.SampleSize > 0 {
		pb.SampleSize = int32(m.SampleSize)
	}
	if m.MarginOfError > 0 {
		pb.MarginOfError = m.MarginOfError
	}
	return pb
}

// computeChangePct returns (current - previous) / previous * 100, or nil if previous is zero.
// Uses decimal arithmetic so operands like 5665.96 (inexact as float64) still yield a correct %;
// the rounded result is converted with InexactFloat64 for protobuf display.
func computeChangePct(current, previous shopspring.Decimal) *float64 {
	if previous.IsZero() {
		return nil
	}
	diff := current.Sub(previous).Div(previous).Mul(shopspring.NewFromInt(100))
	f := diff.Round(2).InexactFloat64()
	return &f
}

func geographyMetricsToPb(list []entity.GeographyMetric) []*pb_admin.GeographyMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.GeographyMetric, len(list))
	for i, g := range list {
		pb[i] = &pb_admin.GeographyMetric{
			Country: g.Country,
			Value:   &decimal.Decimal{Value: g.Value.String()},
			Count:   int32(g.Count),
		}
		if g.State != nil {
			pb[i].State = *g.State
		}
		if g.City != nil {
			pb[i].City = *g.City
		}
		if g.CompareValue != nil {
			pb[i].CompareValue = &decimal.Decimal{Value: g.CompareValue.String()}
		}
		if g.CompareCount != nil {
			pb[i].CompareCount = int32(*g.CompareCount)
		}
		if g.SharePct != nil {
			pb[i].SharePct = *g.SharePct
		}
		if g.AvgOrderValue != nil {
			pb[i].AvgOrderValue = &decimal.Decimal{Value: g.AvgOrderValue.String()}
		}
	}
	return pb
}

func regionMetricsToPb(list []entity.RegionMetric) []*pb_admin.RegionMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.RegionMetric, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.RegionMetric{
			Region: r.Region,
			Value:  &decimal.Decimal{Value: r.Value.String()},
			Count:  int32(r.Count),
		}
	}
	return pb
}

func paymentMethodMetricsToPb(list []entity.PaymentMethodMetric) []*pb_admin.PaymentMethodMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.PaymentMethodMetric, len(list))
	for i, p := range list {
		pb[i] = &pb_admin.PaymentMethodMetric{
			PaymentMethod: p.PaymentMethod,
			Value:         &decimal.Decimal{Value: p.Value.String()},
			Count:         int32(p.Count),
		}
	}
	return pb
}

func currencyMetricsToPb(list []entity.CurrencyMetric) []*pb_admin.CurrencyMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.CurrencyMetric, len(list))
	for i, c := range list {
		pb[i] = &pb_admin.CurrencyMetric{
			Currency: c.Currency,
			Value:    &decimal.Decimal{Value: c.Value.String()},
			Count:    int32(c.Count),
		}
	}
	return pb
}

func productMetricsToPb(list []entity.ProductMetric) []*pb_admin.ProductMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.ProductMetric, len(list))
	for i, p := range list {
		pm := &pb_admin.ProductMetric{
			ProductId:   int32(p.ProductId),
			ProductName: p.ProductName,
			Brand:       p.Brand,
			Value:       &decimal.Decimal{Value: p.Value.String()},
			Count:       int32(p.Count),
			HasCost:     p.HasCost,
		}
		// Emit margin only when the product has a cost — otherwise leave the fields unset
		// so the client shows N/A rather than a misleading 100% margin.
		if p.HasCost {
			pm.UnitCost = &decimal.Decimal{Value: p.UnitCost.String()}
			pm.RevenueCost = &decimal.Decimal{Value: p.RevenueCost.String()}
			pm.GrossMargin = &decimal.Decimal{Value: p.GrossMargin.String()}
			pm.GrossMarginPct = p.GrossMarginPct
		}
		pb[i] = pm
	}
	return pb
}

func categoryMetricsToPb(list []entity.CategoryMetric) []*pb_admin.CategoryMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.CategoryMetric, len(list))
	for i, c := range list {
		pb[i] = &pb_admin.CategoryMetric{
			CategoryId:          int32(c.CategoryId),
			CategoryName:        c.CategoryName,
			CategoryDisplayName: c.CategoryDisplayName,
			Value:               &decimal.Decimal{Value: c.Value.String()},
			Count:               int32(c.Count),
			SharePct:            c.SharePct,
		}
	}
	return pb
}

// ConvertRevenueForecastToPb maps the month revenue forecast (analytics-v2 task 06) to the wire.
func ConvertRevenueForecastToPb(f entity.RevenueForecast) *pb_admin.RevenueForecast {
	return &pb_admin.RevenueForecast{
		Month:              timestamppb.New(f.Month),
		MtdActual:          &decimal.Decimal{Value: f.MtdActual.String()},
		Forecast:           &decimal.Decimal{Value: f.Forecast.String()},
		ForecastLow:        &decimal.Decimal{Value: f.ForecastLow.String()},
		ForecastHigh:       &decimal.Decimal{Value: f.ForecastHigh.String()},
		RunRate:            &decimal.Decimal{Value: f.RunRate.String()},
		Method:             f.Method,
		ElapsedDays:        int32(f.ElapsedDays),
		RemainingDays:      int32(f.RemainingDays),
		LastYearMonthTotal: &decimal.Decimal{Value: f.LastYearMonthTotal.String()},
		Caveat:             f.Caveat,
	}
}

// ConvertDeliverySectionToPb maps the fulfilment-speed section (analytics-v2 task 04) to the wire.
func ConvertDeliverySectionToPb(d entity.DeliverySection) *pb_admin.DeliverySection {
	return &pb_admin.DeliverySection{
		AvgDaysPlacedToShipped:      d.AvgDaysPlacedToShipped,
		AvgDaysShippedToDelivered:   d.AvgDaysShippedToDelivered,
		AvgDaysPlacedToDelivered:    d.AvgDaysPlacedToDelivered,
		MedianDaysPlacedToDelivered: d.MedianDaysPlacedToDelivered,
		OnTimeRatePct:               d.OnTimeRatePct,
		OnTimeSample:                int32(d.OnTimeSample),
		EtaCoveragePct:              d.EtaCoveragePct,
		DeliveredCoveragePct:        d.DeliveredCoveragePct,
		DeliveredSample:             int32(d.DeliveredSample),
		ShippedSample:               int32(d.ShippedSample),
		AvgDeliveryDaysByWeek:       timeSeriesToPb(d.AvgDeliveryDaysByWeek),
		Caveat:                      d.Caveat,
	}
}

// ConvertOrderValueBandsToPb maps the fixed order-value histogram (analytics-v2 task 03) to the wire.
func ConvertOrderValueBandsToPb(list []entity.OrderValueBandRow) []*pb_admin.OrderValueBandRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.OrderValueBandRow, len(list))
	for i, b := range list {
		pb[i] = &pb_admin.OrderValueBandRow{
			Label:           b.Label,
			From:            &decimal.Decimal{Value: b.From.String()},
			To:              &decimal.Decimal{Value: b.To.String()},
			Orders:          int32(b.Orders),
			Revenue:         &decimal.Decimal{Value: b.Revenue.String()},
			OrdersSharePct:  b.OrdersSharePct,
			RevenueSharePct: b.RevenueSharePct,
			AvgOrderValue:   &decimal.Decimal{Value: b.AvgOrderValue.String()},
		}
	}
	return pb
}

func crossSellPairsToPb(list []entity.CrossSellPair) []*pb_admin.CrossSellPair {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.CrossSellPair, len(list))
	for i, c := range list {
		pb[i] = &pb_admin.CrossSellPair{
			ProductAId:   int32(c.ProductAId),
			ProductBId:   int32(c.ProductBId),
			ProductAName: c.ProductAName,
			ProductBName: c.ProductBName,
			Count:        int32(c.Count),
			Support:      c.Support,
			Confidence:   c.Confidence,
			Lift:         c.Lift,
		}
	}
	return pb
}

func promoMetricsToPb(list []entity.PromoMetric) []*pb_admin.PromoMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.PromoMetric, len(list))
	for i, p := range list {
		pb[i] = &pb_admin.PromoMetric{
			PromoCode:   p.PromoCode,
			OrdersCount: int32(p.OrdersCount),
			Revenue:     &decimal.Decimal{Value: p.Revenue.String()},
			AvgDiscount: &decimal.Decimal{Value: p.AvgDiscount.String()},
		}
	}
	return pb
}

func statusCountsToPb(list []entity.StatusCount) []*pb_admin.StatusCount {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.StatusCount, len(list))
	for i, s := range list {
		pb[i] = &pb_admin.StatusCount{
			StatusName: s.StatusName,
			Count:      int32(s.Count),
		}
	}
	return pb
}

func clvStatsToPb(c entity.CLVStats) *pb_admin.CLVStats {
	return &pb_admin.CLVStats{
		Mean:       &decimal.Decimal{Value: c.Mean.String()},
		Median:     &decimal.Decimal{Value: c.Median.String()},
		P90:        &decimal.Decimal{Value: c.P90.String()},
		SampleSize: int32(c.SampleSize),
	}
}

func peakDayToPb(p *entity.PeakDay) *pb_admin.PeakDay {
	if p == nil {
		return nil
	}
	return &pb_admin.PeakDay{
		Date:    timestamppb.New(p.Date),
		Revenue: &decimal.Decimal{Value: p.Revenue.String()},
		Orders:  int32(p.Orders),
	}
}

func newVsReturningSplitToPb(s *entity.NewVsReturningSplit) *pb_admin.NewVsReturningSplit {
	if s == nil {
		return nil
	}
	return &pb_admin.NewVsReturningSplit{
		NewOrders:             metricWithComparisonToPb(s.NewOrders),
		NewRevenue:            metricWithComparisonToPb(s.NewRevenue),
		NewAov:                metricWithComparisonToPb(s.NewAOV),
		ReturningOrders:       metricWithComparisonToPb(s.ReturningOrders),
		ReturningRevenue:      metricWithComparisonToPb(s.ReturningRevenue),
		ReturningAov:          metricWithComparisonToPb(s.ReturningAOV),
		NewRevenueSharePct:    s.NewRevenueSharePct,
		NewRevenueByDay:       timeSeriesToPb(s.NewRevenueByDay),
		ReturningRevenueByDay: timeSeriesToPb(s.ReturningByDay),
	}
}

func timeSeriesToPb(list []entity.TimeSeriesPoint) []*pb_admin.TimeSeriesPoint {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.TimeSeriesPoint, len(list))
	for i, t := range list {
		pb[i] = &pb_admin.TimeSeriesPoint{
			Date:  timestamppb.New(t.Date),
			Value: &decimal.Decimal{Value: t.Value.String()},
			Count: int32(t.Count),
		}
	}
	return pb
}

func ptrFloat64ToVal(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func productViewMetricsToPb(list []entity.ProductViewMetric) []*pb_admin.ProductViewMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.ProductViewMetric, len(list))
	for i, p := range list {
		pb[i] = &pb_admin.ProductViewMetric{
			ProductId:      int32(p.ProductId),
			ProductName:    p.ProductName,
			Brand:          p.Brand,
			PageViews:      int32(p.PageViews),
			Sessions:       int32(p.Sessions),
			AddToCarts:     int32(p.AddToCarts),
			Purchases:      int32(p.Purchases),
			ConversionRate: p.ConversionRate,
		}
	}
	return pb
}

func trafficSourceMetricsToPb(list []entity.TrafficSourceMetric) []*pb_admin.TrafficSourceMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.TrafficSourceMetric, len(list))
	for i, t := range list {
		pb[i] = &pb_admin.TrafficSourceMetric{
			Source:   t.Source,
			Medium:   t.Medium,
			Sessions: int32(t.Sessions),
			Users:    int32(t.Users),
			Revenue:  &decimal.Decimal{Value: t.Revenue.String()},
		}
	}
	return pb
}

func deviceMetricsToPb(list []entity.DeviceMetric) []*pb_admin.DeviceMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.DeviceMetric, len(list))
	for i, d := range list {
		pb[i] = &pb_admin.DeviceMetric{
			DeviceCategory: d.DeviceCategory,
			Sessions:       int32(d.Sessions),
			Users:          int32(d.Users),
			ConversionRate: d.ConversionRate,
		}
	}
	return pb
}

func geographySessionMetricsToPb(list []entity.GeographySessionMetric) []*pb_admin.GeographySessionMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.GeographySessionMetric, len(list))
	for i, g := range list {
		pb[i] = &pb_admin.GeographySessionMetric{
			Country:  g.Country,
			Sessions: int32(g.Sessions),
			Users:    int32(g.Users),
		}
		if g.State != nil {
			pb[i].State = *g.State
		}
		if g.City != nil {
			pb[i].City = *g.City
		}
	}
	return pb
}

// ConvertGeographyToPb converts geography metrics and session data to protobuf GeographySection.
func ConvertGeographyToPb(byCountry []entity.GeographyMetric, sessions []entity.GeographySessionMetric) *pb_admin.GeographySection {
	return &pb_admin.GeographySection{
		ByCountry:         geographyMetricsToPb(byCountry),
		SessionsByCountry: geographySessionMetricsToPb(sessions),
	}
}

// --- BQ Analytics DTO converters ---

func ConvertFunnelAggregateToPb(a *entity.FunnelAggregate) *pb_admin.FunnelAggregate {
	if a == nil {
		return nil
	}
	return &pb_admin.FunnelAggregate{
		SessionStartUsers:    a.FunnelSteps.SessionStartUsers,
		ViewItemListUsers:    a.FunnelSteps.ViewItemListUsers,
		SelectItemUsers:      a.FunnelSteps.SelectItemUsers,
		ViewItemUsers:        a.FunnelSteps.ViewItemUsers,
		SizeSelectedUsers:    a.FunnelSteps.SizeSelectedUsers,
		AddToCartUsers:       a.FunnelSteps.AddToCartUsers,
		BeginCheckoutUsers:   a.FunnelSteps.BeginCheckoutUsers,
		AddShippingInfoUsers: a.FunnelSteps.AddShippingInfoUsers,
		AddPaymentInfoUsers:  a.FunnelSteps.AddPaymentInfoUsers,
		PurchaseUsers:        a.FunnelSteps.PurchaseUsers,
	}
}

func ConvertDailyFunnelsToPb(list []entity.DailyFunnel) []*pb_admin.DailyFunnel {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.DailyFunnel, len(list))
	for i, d := range list {
		pb[i] = &pb_admin.DailyFunnel{
			Date:                 timestamppb.New(d.Date),
			SessionStartUsers:    d.FunnelSteps.SessionStartUsers,
			ViewItemListUsers:    d.FunnelSteps.ViewItemListUsers,
			SelectItemUsers:      d.FunnelSteps.SelectItemUsers,
			ViewItemUsers:        d.FunnelSteps.ViewItemUsers,
			SizeSelectedUsers:    d.FunnelSteps.SizeSelectedUsers,
			AddToCartUsers:       d.FunnelSteps.AddToCartUsers,
			BeginCheckoutUsers:   d.FunnelSteps.BeginCheckoutUsers,
			AddShippingInfoUsers: d.FunnelSteps.AddShippingInfoUsers,
			AddPaymentInfoUsers:  d.FunnelSteps.AddPaymentInfoUsers,
			PurchaseUsers:        d.FunnelSteps.PurchaseUsers,
		}
	}
	return pb
}

func ConvertOOSImpactMetricsToPb(list []entity.OOSImpactMetric) []*pb_admin.OOSImpactMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.OOSImpactMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.OOSImpactMetric{
			Date:                 timestamppb.New(m.Date),
			ProductId:            m.ProductID,
			ProductName:          m.ProductName,
			SizeId:               int32(m.SizeID),
			SizeName:             m.SizeName,
			ProductPrice:         &decimal.Decimal{Value: m.ProductPrice.String()},
			Currency:             m.Currency,
			ClickCount:           m.ClickCount,
			EstimatedLostSales:   &decimal.Decimal{Value: m.EstimatedLostSales.String()},
			EstimatedLostRevenue: &decimal.Decimal{Value: m.EstimatedLostRevenue.String()},
		}
	}
	return pb
}

func ConvertPaymentFailureMetricsToPb(list []entity.PaymentFailureMetric) []*pb_admin.PaymentFailureMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.PaymentFailureMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.PaymentFailureMetric{
			Date:                timestamppb.New(m.Date),
			ErrorCode:           m.ErrorCode,
			PaymentType:         m.PaymentType,
			FailureCount:        m.FailureCount,
			TotalFailedValue:    &decimal.Decimal{Value: m.TotalFailedValue.String()},
			AvgFailedOrderValue: &decimal.Decimal{Value: m.AvgFailedOrderValue.String()},
		}
	}
	return pb
}

func ConvertWebVitalMetricsToPb(list []entity.WebVitalMetric) []*pb_admin.WebVitalMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.WebVitalMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.WebVitalMetric{
			Date:           timestamppb.New(m.Date),
			MetricName:     m.MetricName,
			MetricRating:   m.MetricRating,
			SessionCount:   int32(m.SessionCount),
			Conversions:    int32(m.Conversions),
			AvgMetricValue: m.AvgMetricValue,
		}
	}
	return pb
}

func ConvertUserJourneyMetricsToPb(list []entity.UserJourneyMetric) []*pb_admin.UserJourneyMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.UserJourneyMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.UserJourneyMetric{
			Date:         timestamppb.New(m.Date),
			JourneyPath:  m.JourneyPath,
			SessionCount: int32(m.SessionCount),
			Conversions:  int32(m.Conversions),
		}
	}
	return pb
}

func ConvertSessionDurationMetricsToPb(list []entity.SessionDurationMetric) []*pb_admin.SessionDurationMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.SessionDurationMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.SessionDurationMetric{
			Date:                        timestamppb.New(m.Date),
			AvgTimeBetweenEventsSeconds: m.AvgTimeBetweenEventsSeconds,
			MedianTimeBetweenEvents:     m.MedianTimeBetweenEvents,
		}
	}
	return pb
}

func ConvertDeviceFunnelMetricsToPb(list []entity.DeviceFunnelMetric) []*pb_admin.DeviceFunnelMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.DeviceFunnelMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.DeviceFunnelMetric{
			Date:           timestamppb.New(m.Date),
			DeviceCategory: m.DeviceCategory,
			Sessions:       int32(m.Sessions),
			AddToCartUsers: int32(m.AddToCartUsers),
			CheckoutUsers:  int32(m.CheckoutUsers),
			PurchaseUsers:  int32(m.PurchaseUsers),
		}
	}
	return pb
}

func ConvertProductEngagementMetricsToPb(list []entity.ProductEngagementMetric) []*pb_admin.ProductEngagementMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.ProductEngagementMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.ProductEngagementMetric{
			Date:                 timestamppb.New(m.Date),
			ProductId:            m.ProductID,
			ProductName:          m.ProductName,
			ImageViews:           m.ImageViews,
			ZoomEvents:           m.ZoomEvents,
			Scroll_75:            m.Scroll75,
			Scroll_100:           m.Scroll100,
			AvgTimeOnPageSeconds: m.AvgTimeOnPageSeconds,
		}
	}
	return pb
}

func ConvertProductEngagementBubbleMatrixToPb(m *entity.ProductEngagementBubbleMatrix) *pb_admin.ProductEngagementBubbleMatrix {
	if m == nil {
		return nil
	}
	rows := make([]*pb_admin.ProductEngagementBubbleRow, len(m.Rows))
	for i, r := range m.Rows {
		rows[i] = &pb_admin.ProductEngagementBubbleRow{
			ProductId:            r.ProductID,
			ProductName:          r.ProductName,
			TotalImageViews:      r.TotalImageViews,
			TotalZoomEvents:      r.TotalZoomEvents,
			TotalScroll_75:       r.TotalScroll75,
			TotalScroll_100:      r.TotalScroll100,
			ZoomRatePct:          r.ZoomRatePct,
			Scroll_75RatePct:     r.Scroll75RatePct,
			Scroll_100RatePct:    r.Scroll100RatePct,
			AvgTimeOnPageSeconds: r.AvgTimeOnPageSeconds,
		}
	}
	return &pb_admin.ProductEngagementBubbleMatrix{
		Rows: rows,
		Overall: &pb_admin.ProductEngagementMetricsPct{
			AvgZoomRatePct:       m.Overall.AvgZoomRatePct,
			AvgScroll_75RatePct:  m.Overall.AvgScroll75RatePct,
			AvgScroll_100RatePct: m.Overall.AvgScroll100RatePct,
			AvgTimeOnPageSeconds: m.Overall.AvgTimeOnPageSeconds,
		},
	}
}

func ConvertFormErrorMetricsToPb(list []entity.FormErrorMetric) []*pb_admin.FormErrorMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.FormErrorMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.FormErrorMetric{
			Date:       timestamppb.New(m.Date),
			FieldName:  m.FieldName,
			ErrorCount: m.ErrorCount,
		}
	}
	return pb
}

func ConvertExceptionMetricsToPb(list []entity.ExceptionMetric) []*pb_admin.ExceptionMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.ExceptionMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.ExceptionMetric{
			Date:           timestamppb.New(m.Date),
			PagePath:       m.PagePath,
			ExceptionCount: m.ExceptionCount,
			Description:    m.Description,
		}
	}
	return pb
}

func ConvertNotFoundMetricsToPb(list []entity.NotFoundMetric) []*pb_admin.NotFoundMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.NotFoundMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.NotFoundMetric{
			Date:     timestamppb.New(m.Date),
			PagePath: m.PagePath,
			HitCount: m.HitCount,
		}
	}
	return pb
}

func ConvertHeroFunnelMetricsToPb(list []entity.HeroFunnelMetric) []*pb_admin.HeroFunnelMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.HeroFunnelMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.HeroFunnelMetric{
			Date:           timestamppb.New(m.Date),
			HeroClickUsers: m.HeroClickUsers,
			ViewItemUsers:  m.ViewItemUsers,
			PurchaseUsers:  m.PurchaseUsers,
		}
	}
	return pb
}

func ConvertSizeConfidenceMetricsToPb(list []entity.SizeConfidenceMetric) []*pb_admin.SizeConfidenceMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.SizeConfidenceMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.SizeConfidenceMetric{
			Date:           timestamppb.New(m.Date),
			ProductId:      m.ProductID,
			ProductName:    m.ProductName,
			SizeGuideViews: m.SizeGuideViews,
			SizeSelections: m.SizeSelections,
		}
	}
	return pb
}

func ConvertPaymentRecoveryMetricsToPb(list []entity.PaymentRecoveryMetric) []*pb_admin.PaymentRecoveryMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.PaymentRecoveryMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.PaymentRecoveryMetric{
			Date:           timestamppb.New(m.Date),
			FailedUsers:    m.FailedUsers,
			RecoveredUsers: m.RecoveredUsers,
		}
	}
	return pb
}

func ConvertCheckoutTimingMetricsToPb(list []entity.CheckoutTimingMetric) []*pb_admin.CheckoutTimingMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.CheckoutTimingMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.CheckoutTimingMetric{
			Date:                  timestamppb.New(m.Date),
			AvgCheckoutSeconds:    m.AvgCheckoutSeconds,
			MedianCheckoutSeconds: m.MedianCheckoutSeconds,
			SessionCount:          m.SessionCount,
		}
	}
	return pb
}

// --- Retention DTO converters ---

func ConvertCohortRetentionToPb(list []entity.CohortRetentionRow) []*pb_admin.CohortRetentionRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.CohortRetentionRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.CohortRetentionRow{
			CohortMonth: timestamppb.New(r.CohortMonth),
			CohortSize:  r.CohortSize,
			M1:          r.M1,
			M2:          r.M2,
			M3:          r.M3,
			M4:          r.M4,
			M5:          r.M5,
			M6:          r.M6,
			M1Revenue:   &decimal.Decimal{Value: r.M1Revenue.Round(2).String()},
			M2Revenue:   &decimal.Decimal{Value: r.M2Revenue.Round(2).String()},
			M3Revenue:   &decimal.Decimal{Value: r.M3Revenue.Round(2).String()},
			M4Revenue:   &decimal.Decimal{Value: r.M4Revenue.Round(2).String()},
			M5Revenue:   &decimal.Decimal{Value: r.M5Revenue.Round(2).String()},
			M6Revenue:   &decimal.Decimal{Value: r.M6Revenue.Round(2).String()},
		}
	}
	return pb
}

func ConvertOrderSequenceMetricsToPb(list []entity.OrderSequenceMetric) []*pb_admin.OrderSequenceMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.OrderSequenceMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.OrderSequenceMetric{
			OrderNumber:      int32(m.OrderNumber),
			OrderCount:       m.OrderCount,
			AvgOrderValue:    &decimal.Decimal{Value: m.AvgOrderValue.String()},
			AvgDaysSincePrev: m.AvgDaysSincePrev,
		}
	}
	return pb
}

func ConvertEntryProductMetricsToPb(list []entity.EntryProductMetric) []*pb_admin.EntryProductMetric {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.EntryProductMetric, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.EntryProductMetric{
			ProductId:     int32(m.ProductID),
			ProductName:   m.ProductName,
			PurchaseCount: m.PurchaseCount,
			TotalRevenue:  &decimal.Decimal{Value: m.TotalRevenue.String()},
		}
	}
	return pb
}

func ConvertRevenueParetoToPb(list []entity.RevenueParetoRow) []*pb_admin.RevenueParetoRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.RevenueParetoRow, len(list))
	for i, m := range list {
		row := &pb_admin.RevenueParetoRow{
			Rank:          int32(m.Rank),
			ProductId:     int32(m.ProductID),
			ProductName:   m.ProductName,
			Revenue:       &decimal.Decimal{Value: m.Revenue.String()},
			CumulativePct: m.CumulativePct,
			HasCost:       m.HasCost,
		}
		// Emit margin only when the product has a cost — otherwise leave the fields unset so
		// the client shows N/A rather than a misleading 100% margin.
		if m.HasCost {
			row.UnitCost = &decimal.Decimal{Value: m.UnitCost.String()}
			row.RevenueCost = &decimal.Decimal{Value: m.RevenueCost.String()}
			row.GrossMargin = &decimal.Decimal{Value: m.GrossMargin.String()}
			row.GrossMarginPct = m.GrossMarginPct
		}
		pb[i] = row
	}
	return pb
}

func ConvertSpendingCurveToPb(list []entity.SpendingCurvePoint) []*pb_admin.SpendingCurvePoint {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.SpendingCurvePoint, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.SpendingCurvePoint{
			OrderNumber:        int32(m.OrderNumber),
			AvgCumulativeSpend: &decimal.Decimal{Value: m.AvgCumulativeSpend.String()},
			CustomerCount:      m.CustomerCount,
		}
	}
	return pb
}

func ConvertCategoryLoyaltyToPb(list []entity.CategoryLoyaltyRow) []*pb_admin.CategoryLoyaltyRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.CategoryLoyaltyRow, len(list))
	for i, m := range list {
		pb[i] = &pb_admin.CategoryLoyaltyRow{
			FirstCategory:  m.FirstCategory,
			SecondCategory: m.SecondCategory,
			CustomerCount:  m.CustomerCount,
		}
	}
	return pb
}

func ConvertInventoryHealthToPb(list []entity.InventoryHealthRow) []*pb_admin.InventoryHealthRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.InventoryHealthRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.InventoryHealthRow{
			ProductId:       int32(r.ProductID),
			ProductName:     r.ProductName,
			SizeId:          int32(r.SizeID),
			SizeName:        r.SizeName,
			Quantity:        int32(r.Quantity),
			AvgDailySales:   r.AvgDailySales,
			DaysOnHand:      r.DaysOnHand,
			ReorderPoint:    int32(r.ReorderPoint.Int64),
			TargetDaysCover: int32(r.TargetDaysCover.Int64),
			LeadTimeDays:    int32(r.LeadTimeDays.Int64),
			NeedsReorder:    r.NeedsReorder,
			HasTarget:       r.HasTarget,
			IsSelling:       r.IsSelling,
		}
	}
	return pb
}

// ConvertPbInventoryTargetsToEntity maps admin-supplied targets to entity inserts. A 0 on
// any threshold means "unset" and is stored as NULL (no trigger on that dimension).
func ConvertPbInventoryTargetsToEntity(list []*pb_admin.InventoryTargetInsert) []entity.InventoryTargetInsert {
	out := make([]entity.InventoryTargetInsert, 0, len(list))
	for _, t := range list {
		if t == nil {
			continue
		}
		out = append(out, entity.InventoryTargetInsert{
			ProductID:       int(t.ProductId),
			SizeID:          int(t.SizeId),
			ReorderPoint:    nullInt64FromPositive(t.ReorderPoint),
			TargetDaysCover: nullInt64FromPositive(t.TargetDaysCover),
			LeadTimeDays:    nullInt64FromPositive(t.LeadTimeDays),
		})
	}
	return out
}

// nullInt64FromPositive treats a non-positive proto int (the default 0) as "unset" → NULL.
func nullInt64FromPositive(v int32) sql.NullInt64 {
	if v <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(v), Valid: true}
}

func ConvertSizeRunEfficiencyToPb(list []entity.SizeRunEfficiencyRow) []*pb_admin.SizeRunEfficiencyRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.SizeRunEfficiencyRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.SizeRunEfficiencyRow{
			ProductId:        int32(r.ProductID),
			ProductName:      r.ProductName,
			TotalSizes:       int32(r.TotalSizes),
			SoldThroughSizes: int32(r.SoldThroughSizes),
			EfficiencyPct:    r.EfficiencyPct,
			UnitsBought:      r.UnitsBought,
			UnitsSold:        r.UnitsSold,
			SellThroughPct:   r.SellThroughPct,
		}
	}
	return pb
}

func ConvertSellThroughByDropToPb(list []entity.SellThroughByDropRow) []*pb_admin.SellThroughByDropRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.SellThroughByDropRow, len(list))
	for i, r := range list {
		row := &pb_admin.SellThroughByDropRow{
			Collection:     r.Collection,
			ProductCount:   int32(r.ProductCount),
			UnitsSold:      r.UnitsSold,
			UnitsRemaining: r.UnitsRemaining,
			UnitsBought:    r.UnitsBought,
			SellThroughPct: r.SellThroughPct,
			Revenue:        &decimal.Decimal{Value: r.Revenue.String()},
			HasCost:        r.HasCost,
		}
		if r.HasCost {
			row.GrossMargin = &decimal.Decimal{Value: r.GrossMargin.String()}
			row.GrossMarginPct = r.GrossMarginPct
		}
		if r.DaysTo50Pct.Valid {
			v := r.DaysTo50Pct.Int64
			row.DaysTo_50Pct = &v
		}
		pb[i] = row
	}
	return pb
}

// AlertThresholdsToPb / AlertThresholdsFromPb map the operator-tunable alert thresholds.
func AlertThresholdsToPb(t entity.AlertThresholds) *pb_admin.AlertSettings {
	return &pb_admin.AlertSettings{
		CoverageWarnPct:        t.CoverageWarnPct,
		RefundRateWarnPct:      t.RefundRateWarnPct,
		RateFloorN:             int32(t.RateFloorN),
		ContributionTrustPct:   t.ContributionTrustPct,
		Ga4CoverageWarnPct:     t.GA4CoverageWarnPct,
		ProductionRunStaleDays: int32(t.ProductionRunStaleDays),
	}
}

func AlertThresholdsFromPb(s *pb_admin.AlertSettings) entity.AlertThresholds {
	if s == nil {
		return entity.DefaultAlertThresholds()
	}
	return entity.AlertThresholds{
		CoverageWarnPct:        s.CoverageWarnPct,
		RefundRateWarnPct:      s.RefundRateWarnPct,
		RateFloorN:             int(s.RateFloorN),
		ContributionTrustPct:   s.ContributionTrustPct,
		GA4CoverageWarnPct:     s.Ga4CoverageWarnPct,
		ProductionRunStaleDays: int(s.ProductionRunStaleDays),
	}
}

func alertSeverityToPb(s entity.AlertSeverity) pb_admin.AlertSeverity {
	switch s {
	case entity.AlertSeverityInfo:
		return pb_admin.AlertSeverity_ALERT_SEVERITY_INFO
	case entity.AlertSeverityWarning:
		return pb_admin.AlertSeverity_ALERT_SEVERITY_WARNING
	case entity.AlertSeverityCritical:
		return pb_admin.AlertSeverity_ALERT_SEVERITY_CRITICAL
	default:
		return pb_admin.AlertSeverity_ALERT_SEVERITY_UNSPECIFIED
	}
}

// ConvertDashboardToPb maps the decision-grade dashboard payload, reusing the section
// converters for the action lists.
func ConvertDashboardToPb(d *entity.Dashboard) *pb_admin.GetDashboardResponse {
	if d == nil {
		return nil
	}
	resp := &pb_admin.GetDashboardResponse{
		Period:              timeRangeToPb(d.Period),
		Revenue:             &decimal.Decimal{Value: d.Revenue.String()},
		Orders:              int32(d.Orders),
		GrossMargin:         &decimal.Decimal{Value: d.GrossMargin.String()},
		GrossMarginPct:      d.GrossMarginPct,
		ContributionMargin:  &decimal.Decimal{Value: d.ContributionMargin.String()},
		CostCoveragePct:     d.CostCoveragePct,
		Caveat:              d.Caveat,
		UncostedProductIds:  intsToInt32(d.UncostedProductIds),
		Ga4Revenue:          &decimal.Decimal{Value: d.GA4Revenue.String()},
		TrackingCoveragePct: d.TrackingCoveragePct,
		OperatingResult:     &decimal.Decimal{Value: d.OperatingResult.String()},
		OpexTotal:           &decimal.Decimal{Value: d.OpexTotal.String()},
		MarketingSpend:      &decimal.Decimal{Value: d.MarketingSpend.String()},
		OpexCaveat:          d.OpexCaveat,
		TopByMargin:         productMetricsToPb(d.TopByMargin),
		Reorder:             ConvertInventoryHealthToPb(d.Reorder),
		Clear:               ConvertSlowMoversToPb(d.Clear),
		Drops:               ConvertSellThroughByDropToPb(d.Drops),
	}
	if len(d.Alerts) > 0 {
		resp.Alerts = make([]*pb_admin.DashboardAlert, len(d.Alerts))
		for i, a := range d.Alerts {
			resp.Alerts[i] = &pb_admin.DashboardAlert{
				Severity: alertSeverityToPb(a.Severity),
				Code:     a.Code,
				Title:    a.Title,
				Detail:   a.Detail,
			}
		}
	}
	return resp
}

func ConvertSlowMoversToPb(list []entity.SlowMoverRow) []*pb_admin.SlowMoverRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.SlowMoverRow, len(list))
	for i, r := range list {
		row := &pb_admin.SlowMoverRow{
			ProductId:     int32(r.ProductID),
			ProductName:   r.ProductName,
			Revenue:       &decimal.Decimal{Value: r.Revenue.String()},
			UnitsSold:     r.UnitsSold,
			DaysInStock:   r.DaysInStock,
			ProductHidden: r.ProductHidden,
			TotalViews:    r.TotalViews,
			HasCost:       r.HasCost,
		}
		if r.LastSaleDate != nil {
			row.LastSaleDate = timestamppb.New(*r.LastSaleDate)
		}
		// Emit margin only when the product has a cost — otherwise leave the fields unset so
		// the client shows N/A rather than a misleading 100% margin.
		if r.HasCost {
			row.UnitCost = &decimal.Decimal{Value: r.UnitCost.String()}
			row.RevenueCost = &decimal.Decimal{Value: r.RevenueCost.String()}
			row.GrossMargin = &decimal.Decimal{Value: r.GrossMargin.String()}
			row.GrossMarginPct = r.GrossMarginPct
		}
		pb[i] = row
	}
	return pb
}

func ConvertReturnByProductToPb(list []entity.ReturnByProductRow) []*pb_admin.ReturnByProductRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.ReturnByProductRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.ReturnByProductRow{
			ProductName:     r.ProductName,
			TotalReturnRate: r.TotalReturnRate,
			Reasons:         r.Reasons,
		}
	}
	return pb
}

func ConvertReturnBySizeToPb(list []entity.ReturnBySizeRow) []*pb_admin.ReturnBySizeRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.ReturnBySizeRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.ReturnBySizeRow{
			SizeId:        int32(r.SizeID),
			SizeName:      r.SizeName,
			TotalSold:     r.TotalSold,
			TotalReturned: r.TotalReturned,
			ReturnRate:    r.ReturnRate,
		}
	}
	return pb
}

func ConvertSizeAnalyticsToPb(list []entity.SizeAnalyticsRow) []*pb_admin.SizeAnalyticsRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.SizeAnalyticsRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.SizeAnalyticsRow{
			ProductId:    int32(r.ProductID),
			ProductName:  r.ProductName,
			SizeId:       int32(r.SizeID),
			SizeName:     r.SizeName,
			UnitsSold:    r.UnitsSold,
			Revenue:      &decimal.Decimal{Value: r.Revenue.String()},
			PctOfProduct: r.PctOfProduct,
		}
	}
	return pb
}

func ConvertDeadStockToPb(list []entity.DeadStockRow) []*pb_admin.DeadStockRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.DeadStockRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.DeadStockRow{
			ProductId:       int32(r.ProductID),
			ProductName:     r.ProductName,
			SizeId:          int32(r.SizeID),
			SizeName:        r.SizeName,
			Quantity:        int32(r.Quantity),
			DaysWithoutSale: r.DaysWithoutSale,
			StockValue:      &decimal.Decimal{Value: r.StockValue.String()},
		}
	}
	return pb
}

func ConvertProductTrendToPb(list []entity.ProductTrendRow) []*pb_admin.ProductTrendRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.ProductTrendRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.ProductTrendRow{
			ProductId:       int32(r.ProductID),
			ProductName:     r.ProductName,
			CurrentRevenue:  &decimal.Decimal{Value: r.CurrentRevenue.String()},
			PreviousRevenue: &decimal.Decimal{Value: r.PreviousRevenue.String()},
			ChangePct:       r.ChangePct,
			CurrentUnits:    r.CurrentUnits,
			PreviousUnits:   r.PreviousUnits,
		}
	}
	return pb
}

func ConvertTimeOnPageToPb(list []entity.TimeOnPageRow) []*pb_admin.TimeOnPageRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.TimeOnPageRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.TimeOnPageRow{
			Date:                  timestamppb.New(r.Date),
			PagePath:              r.PagePath,
			AvgVisibleTimeSeconds: r.AvgVisibleTimeSeconds,
			AvgTotalTimeSeconds:   r.AvgTotalTimeSeconds,
			AvgEngagementScore:    r.AvgEngagementScore,
			PageViews:             r.PageViews,
		}
	}
	return pb
}

func ConvertProductZoomToPb(list []entity.ProductZoomRow) []*pb_admin.ProductZoomRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.ProductZoomRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.ProductZoomRow{
			Date:        timestamppb.New(r.Date),
			ProductId:   r.ProductID,
			ProductName: r.ProductName,
			ZoomMethod:  r.ZoomMethod,
			ZoomCount:   r.ZoomCount,
		}
	}
	return pb
}

func ConvertImageSwipesToPb(list []entity.ImageSwipeRow) []*pb_admin.ImageSwipeRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.ImageSwipeRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.ImageSwipeRow{
			Date:           timestamppb.New(r.Date),
			ProductId:      r.ProductID,
			ProductName:    r.ProductName,
			SwipeDirection: r.SwipeDirection,
			SwipeCount:     r.SwipeCount,
		}
	}
	return pb
}

func ConvertSizeGuideClicksToPb(list []entity.SizeGuideClickRow) []*pb_admin.SizeGuideClickRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.SizeGuideClickRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.SizeGuideClickRow{
			Date:         timestamppb.New(r.Date),
			ProductId:    r.ProductID,
			ProductName:  r.ProductName,
			PageLocation: r.PageLocation,
			ClickCount:   r.ClickCount,
		}
	}
	return pb
}

func ConvertDetailsExpansionToPb(list []entity.DetailsExpansionRow) []*pb_admin.DetailsExpansionRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.DetailsExpansionRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.DetailsExpansionRow{
			Date:        timestamppb.New(r.Date),
			ProductId:   r.ProductID,
			ProductName: r.ProductName,
			SectionName: r.SectionName,
			ExpandCount: r.ExpandCount,
		}
	}
	return pb
}

func ConvertNotifyMeIntentToPb(list []entity.NotifyMeIntentRow) []*pb_admin.NotifyMeIntentRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.NotifyMeIntentRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.NotifyMeIntentRow{
			Date:           timestamppb.New(r.Date),
			ProductId:      r.ProductID,
			ProductName:    r.ProductName,
			Action:         r.Action,
			Count:          r.Count,
			ConversionRate: r.ConversionRate,
		}
	}
	return pb
}

func ConvertAddToCartRateToPb(list []entity.AddToCartRateRow) []*pb_admin.AddToCartRateRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.AddToCartRateRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.AddToCartRateRow{
			Date:           timestamppb.New(r.Date),
			ProductId:      r.ProductID,
			ProductName:    r.ProductName,
			ViewCount:      r.ViewCount,
			AddToCartCount: r.AddToCartCount,
			CartRate:       fractionToPct(r.CartRate),
		}
	}
	return pb
}

func ConvertAddToCartRateAnalysisToPb(a *entity.AddToCartRateAnalysis) *pb_admin.AddToCartRateAnalysis {
	if a == nil {
		return nil
	}
	return &pb_admin.AddToCartRateAnalysis{
		Products:     convertATCProductRowsToPb(a.Products),
		GlobalTrend:  convertATCGlobalRowsToPb(a.GlobalTrend),
		AvgViewCount: float64(a.AvgViewCount),
		AvgCartRate:  fractionToPct(a.AvgCartRate),
	}
}

func convertATCProductRowsToPb(list []entity.AddToCartRateProductRow) []*pb_admin.AddToCartRateProductRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.AddToCartRateProductRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.AddToCartRateProductRow{
			ProductId:      r.ProductID,
			ProductName:    r.ProductName,
			ViewCount:      r.ViewCount,
			AddToCartCount: r.AddToCartCount,
			CartRate:       fractionToPct(r.CartRate),
		}
	}
	return pb
}

func convertATCGlobalRowsToPb(list []entity.AddToCartRateGlobalRow) []*pb_admin.AddToCartRateGlobalRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.AddToCartRateGlobalRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.AddToCartRateGlobalRow{
			Date:            timestamppb.New(r.Date),
			TotalViews:      r.TotalViews,
			TotalAddToCarts: r.TotalAddToCarts,
			GlobalCartRate:  fractionToPct(r.GlobalCartRate),
		}
	}
	return pb
}

func ConvertBrowserBreakdownToPb(list []entity.BrowserBreakdownRow) []*pb_admin.BrowserBreakdownRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.BrowserBreakdownRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.BrowserBreakdownRow{
			Date:           timestamppb.New(r.Date),
			Browser:        r.Browser,
			Sessions:       int32(r.Sessions),
			Users:          int32(r.Users),
			Conversions:    int32(r.Conversions),
			ConversionRate: r.ConversionRate,
		}
	}
	return pb
}

func ConvertNewsletterToPb(list []entity.NewsletterMetricRow) []*pb_admin.NewsletterMetricRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.NewsletterMetricRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.NewsletterMetricRow{
			Date:        timestamppb.New(r.Date),
			SignupCount: int32(r.SignupCount),
			UniqueUsers: int32(r.UniqueUsers),
		}
	}
	return pb
}

func ConvertAbandonedCartToPb(list []entity.AbandonedCartRow) []*pb_admin.AbandonedCartRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.AbandonedCartRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.AbandonedCartRow{
			Date:                 timestamppb.New(r.Date),
			CartsStarted:         int32(r.CartsStarted),
			CheckoutsStarted:     int32(r.CheckoutsStarted),
			AbandonmentRate:      fractionToPct(r.AbandonmentRate),
			AvgMinutesToCheckout: r.AvgMinutesToCheckout,
			AvgMinutesToAbandon:  r.AvgMinutesToAbandon,
		}
	}
	return pb
}

func ConvertCampaignAttributionToPb(list []entity.CampaignAttributionRow) []*pb_admin.CampaignAttributionRow {
	if len(list) == 0 {
		return nil
	}

	type aggKey struct {
		Source, Medium, Campaign string
	}
	type aggVal struct {
		Sessions, Users, Conversions int64
		Revenue                      shopspring.Decimal
	}
	agg := make(map[aggKey]*aggVal)
	order := make([]aggKey, 0)
	for _, r := range list {
		k := aggKey{r.UTMSource, r.UTMMedium, r.UTMCampaign}
		if v, ok := agg[k]; ok {
			v.Sessions += r.Sessions
			v.Users += r.Users
			v.Conversions += r.Conversions
			v.Revenue = v.Revenue.Add(r.Revenue)
		} else {
			agg[k] = &aggVal{
				Sessions:    r.Sessions,
				Users:       r.Users,
				Conversions: r.Conversions,
				Revenue:     r.Revenue,
			}
			order = append(order, k)
		}
	}

	pb := make([]*pb_admin.CampaignAttributionRow, 0, len(order))
	for _, k := range order {
		v := agg[k]
		var convRate float64
		if v.Sessions > 0 {
			convRate = float64(v.Conversions) / float64(v.Sessions)
		}
		pb = append(pb, &pb_admin.CampaignAttributionRow{
			UtmSource:      k.Source,
			UtmMedium:      k.Medium,
			UtmCampaign:    k.Campaign,
			Sessions:       int32(v.Sessions),
			Users:          int32(v.Users),
			Conversions:    int32(v.Conversions),
			Revenue:        &decimal.Decimal{Value: v.Revenue.String()},
			ConversionRate: convRate,
		})
	}
	return pb
}

func ConvertCampaignAttributionAggregatedToPb(list []entity.CampaignAttributionAggregatedFull) []*pb_admin.CampaignAttributionRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.CampaignAttributionRow, 0, len(list))
	for _, r := range list {
		pb = append(pb, &pb_admin.CampaignAttributionRow{
			UtmSource:      r.UTMSource,
			UtmMedium:      r.UTMMedium,
			UtmCampaign:    r.UTMCampaign,
			Sessions:       int32(r.Sessions),
			Users:          int32(r.Users),
			Conversions:    int32(r.Conversions),
			Revenue:        &decimal.Decimal{Value: r.Revenue.String()},
			ConversionRate: r.ConversionRate,
			Spend:          &decimal.Decimal{Value: r.Spend.String()},
			Roas:           r.ROAS,
			Cac:            r.CAC,
		})
	}
	return pb
}

// ConvertPbChannelSpendToEntity parses admin-supplied marketing spend rows. Date must be
// YYYY-MM-DD and amount must be a non-negative decimal.
func ConvertPbChannelSpendToEntity(list []*pb_admin.ChannelSpendInsert) ([]entity.ChannelSpendInsert, error) {
	out := make([]entity.ChannelSpendInsert, 0, len(list))
	for _, sp := range list {
		if sp == nil {
			continue
		}
		d, err := time.Parse("2006-01-02", sp.Date)
		if err != nil {
			return nil, fmt.Errorf("invalid channel spend date %q: %w", sp.Date, err)
		}
		amount := shopspring.Zero
		if sp.Amount != nil && sp.Amount.Value != "" {
			amount, err = shopspring.NewFromString(sp.Amount.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid channel spend amount %q: %w", sp.Amount.Value, err)
			}
		}
		if amount.IsNegative() {
			return nil, fmt.Errorf("channel spend amount must be >= 0")
		}
		out = append(out, entity.ChannelSpendInsert{
			Date:        d,
			UTMSource:   sp.UtmSource,
			UTMMedium:   sp.UtmMedium,
			UTMCampaign: sp.UtmCampaign,
			Amount:      amount.Round(2),
			Currency:    strings.ToUpper(sp.Currency),
		})
	}
	return out, nil
}

// ConvertPbOpexEntriesToEntity validates and normalises OPEX journal lines (task 22): the month
// is snapped to the first of its month, the category is checked against the closed set, and the
// amount must be a non-negative base-currency figure.
func ConvertPbOpexEntriesToEntity(list []*pb_admin.OpexEntryInsert) ([]entity.OpexEntry, error) {
	out := make([]entity.OpexEntry, 0, len(list))
	for _, e := range list {
		if e == nil {
			continue
		}
		m, err := time.Parse("2006-01-02", e.Month)
		if err != nil {
			return nil, fmt.Errorf("invalid opex month %q: %w", e.Month, err)
		}
		m = time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC) // first of the month
		category := strings.TrimSpace(e.Category)
		if _, ok := entity.ValidOpexCategories[category]; !ok {
			return nil, fmt.Errorf("invalid opex category %q", category)
		}
		amount := shopspring.Zero
		if e.Amount != nil && e.Amount.Value != "" {
			amount, err = shopspring.NewFromString(e.Amount.Value)
			if err != nil {
				return nil, fmt.Errorf("invalid opex amount %q: %w", e.Amount.Value, err)
			}
		}
		if amount.IsNegative() {
			return nil, fmt.Errorf("opex amount must be >= 0")
		}
		note := sql.NullString{}
		if s := strings.TrimSpace(e.Note); s != "" {
			note = sql.NullString{String: s, Valid: true}
		}
		out = append(out, entity.OpexEntry{
			Month:    m,
			Category: category,
			Amount:   amount.Round(2),
			Note:     note,
		})
	}
	return out, nil
}

// ConvertCustomerSegmentationToPb converts AOV customer segments to protobuf.
func ConvertCustomerSegmentationToPb(rows []entity.CustomerSegmentRow) []*pb_admin.CustomerSegmentRow {
	result := make([]*pb_admin.CustomerSegmentRow, len(rows))
	for i, r := range rows {
		result[i] = &pb_admin.CustomerSegmentRow{
			Email:         r.Email,
			OrderCount:    r.OrderCount,
			TotalRevenue:  &decimal.Decimal{Value: r.TotalRevenue.Round(2).String()},
			AvgOrderValue: &decimal.Decimal{Value: r.AvgOrderValue.Round(2).String()},
			Segment:       r.Segment,
		}
	}
	return result
}

// ConvertRFMAnalysisToPb converts RFM analysis results to protobuf.
func ConvertRFMAnalysisToPb(rows []entity.RFMSegmentRow) []*pb_admin.RFMSegmentRow {
	result := make([]*pb_admin.RFMSegmentRow, len(rows))
	for i, r := range rows {
		result[i] = &pb_admin.RFMSegmentRow{
			Email:          r.Email,
			RecencyScore:   int32(r.RecencyScore),
			FrequencyScore: int32(r.FrequencyScore),
			MonetaryScore:  int32(r.MonetaryScore),
			RfmLabel:       r.RFMLabel,
			LastPurchase:   timestamppb.New(r.LastPurchase),
			OrderCount:     r.OrderCount,
			TotalSpent:     &decimal.Decimal{Value: r.TotalSpent.Round(2).String()},
		}
	}
	return result
}

// ConvertMarginByStyleToPb maps per-style margin rows to proto, emitting the margin fields only
// when the style's sold SKUs have a cost (else left unset so the client shows N/A, not 100%).
func ConvertMarginByStyleToPb(list []entity.MarginByStyleRow) []*pb_admin.MarginByStyleRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.MarginByStyleRow, len(list))
	for i, r := range list {
		row := &pb_admin.MarginByStyleRow{
			TechCardId:    int32(r.TechCardID),
			StyleNumber:   r.StyleNumber,
			Name:          r.Name,
			Revenue:       &decimal.Decimal{Value: r.Revenue.String()},
			UnitsSold:     r.UnitsSold,
			ColorwayCount: int32(r.ColorwayCount),
			HasCost:       r.HasCost,
		}
		if r.HasCost {
			row.UnitCost = &decimal.Decimal{Value: r.UnitCost.String()}
			row.RevenueCost = &decimal.Decimal{Value: r.RevenueCost.String()}
			row.GrossMargin = &decimal.Decimal{Value: r.GrossMargin.String()}
			row.GrossMarginPct = r.GrossMarginPct
		}
		pb[i] = row
	}
	return pb
}

// ConvertCogsStructureToPb maps the COGS component rows to proto.
func ConvertCogsStructureToPb(list []entity.CogsStructureRow) []*pb_admin.CogsStructureRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.CogsStructureRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.CogsStructureRow{
			Component: r.Component,
			Amount:    &decimal.Decimal{Value: r.Amount.String()},
			Pct:       r.Pct,
		}
	}
	return pb
}

// ConvertInventoryValuationToPb maps the inventory-valuation summary to proto.
func ConvertInventoryValuationToPb(v *entity.InventoryValuation) *pb_admin.InventoryValuation {
	if v == nil {
		return nil
	}
	return &pb_admin.InventoryValuation{
		TotalStockValue:         &decimal.Decimal{Value: v.TotalStockValue.String()},
		TotalOnHandUnits:        v.TotalOnHandUnits,
		CostedOnHandUnits:       v.CostedOnHandUnits,
		UncostedStockUnits:      v.UncostedStockUnits,
		UncostedStockProducts:   int32(v.UncostedStockProducts),
		CoveragePct:             v.CoveragePct,
		TopByValue:              convertInventoryValuationRows(v.TopByValue),
		DeadStock:               convertInventoryValuationRows(v.DeadStock),
		WriteOffsValue:          &decimal.Decimal{Value: v.WriteOffsValue.String()},
		WriteOffsUnits:          v.WriteOffsUnits,
		RawMaterialsValue:       &decimal.Decimal{Value: v.RawMaterialsValue.String()},
		RawMaterialsCount:       int32(v.RawMaterialsCount),
		RawUncostedCount:        int32(v.RawUncostedCount),
		WipValue:                &decimal.Decimal{Value: v.WipValue.String()},
		WriteoffsMaterialsValue: &decimal.Decimal{Value: v.WriteOffsMaterialsValue.String()},
		TopMaterialsByValue:     convertMaterialValuationRows(v.TopMaterialsByValue),
	}
}

func convertMaterialValuationRows(list []entity.MaterialValuationRow) []*pb_admin.MaterialValuationRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.MaterialValuationRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.MaterialValuationRow{
			MaterialId:      int32(r.MaterialId),
			Name:            r.Name,
			Unit:            r.Unit,
			OnHand:          &decimal.Decimal{Value: r.OnHand.String()},
			AvgUnitCostBase: &decimal.Decimal{Value: r.AvgUnitCostBase.String()},
			Value:           &decimal.Decimal{Value: r.Value.String()},
		}
	}
	return pb
}

func convertInventoryValuationRows(list []entity.InventoryValuationRow) []*pb_admin.InventoryValuationRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.InventoryValuationRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.InventoryValuationRow{
			ProductId:   int32(r.ProductID),
			ProductName: r.ProductName,
			OnHand:      r.OnHand,
			UnitCost:    &decimal.Decimal{Value: r.UnitCost.String()},
			Value:       &decimal.Decimal{Value: r.Value.String()},
			SoldUnits:   r.SoldUnits,
		}
	}
	return pb
}
