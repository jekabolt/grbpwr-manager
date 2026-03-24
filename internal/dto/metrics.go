package dto

import (
	"math"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	shopspring "github.com/shopspring/decimal"
	"google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func ConvertEntityBusinessMetricsToPb(m *entity.BusinessMetrics) *pb_admin.BusinessMetrics {
	if m == nil {
		return nil
	}
	pb := &pb_admin.BusinessMetrics{
		Period:                         timeRangeToPb(m.Period),
		Revenue:                        metricWithComparisonToPb(m.Revenue),
		OrdersCount:                    metricWithComparisonToPb(m.OrdersCount),
		AvgOrderValue:                  metricWithComparisonToPb(m.AvgOrderValue),
		ItemsPerOrder:                  metricWithComparisonToPb(m.ItemsPerOrder, false, true), // round to int so "1 vs 1" shows 0%
		RefundRate:                     metricWithComparisonToPb(m.RefundRate, true),           // lower is better
		PromoUsageRate:                 metricWithComparisonToPb(m.PromoUsageRate),
		GrossRevenue:                   metricWithComparisonToPb(m.GrossRevenue),
		TotalRefunded:                  metricWithComparisonToPb(m.TotalRefunded, true), // lower is better
		TotalDiscount:                  metricWithComparisonToPb(m.TotalDiscount),
		Sessions:                       metricWithComparisonToPb(m.Sessions),
		Users:                          metricWithComparisonToPb(m.Users),
		NewUsers:                       metricWithComparisonToPb(m.NewUsers),
		PageViews:                      metricWithComparisonToPb(m.PageViews),
		BounceRate:                     metricWithComparisonToPb(m.BounceRate, true), // lower is better
		AvgSessionDuration:             metricWithComparisonToPb(m.AvgSessionDuration),
		PagesPerSession:                metricWithComparisonToPb(m.PagesPerSession),
		ConversionRate:                 metricWithComparisonToPb(m.ConversionRate),
		RevenuePerSession:              metricWithComparisonToPb(m.RevenuePerSession),
		NewSubscribers:                 metricWithComparisonToPb(m.NewSubscribers),
		RepeatCustomersRate:            metricWithComparisonToPb(m.RepeatCustomersRate),
		AvgOrdersPerCustomer:           metricWithComparisonToPb(m.AvgOrdersPerCustomer),
		AvgDaysBetweenOrders:           metricWithComparisonToPb(m.AvgDaysBetweenOrders),
		ClvDistribution:                clvStatsToPb(m.CLVDistribution),
		RevenueByCountry:               geographyMetricsToPb(m.RevenueByCountry),
		RevenueByCity:                  geographyMetricsToPb(m.RevenueByCity),
		RevenueByRegion:                regionMetricsToPb(m.RevenueByRegion),
		AvgOrderByCountry:              geographyMetricsToPb(m.AvgOrderByCountry),
		SessionsByCountry:              geographySessionMetricsToPb(m.SessionsByCountry),
		RevenueByCurrency:              currencyMetricsToPb(m.RevenueByCurrency),
		RevenueByPaymentMethod:         paymentMethodMetricsToPb(m.RevenueByPaymentMethod),
		TopProductsByRevenue:           productMetricsToPb(m.TopProductsByRevenue),
		TopProductsByQuantity:          productMetricsToPb(m.TopProductsByQuantity),
		TopProductsByViews:             productViewMetricsToPb(m.TopProductsByViews),
		RevenueByCategory:              categoryMetricsToPb(m.RevenueByCategory),
		CrossSellPairs:                 crossSellPairsToPb(m.CrossSellPairs),
		TrafficBySource:                trafficSourceMetricsToPb(m.TrafficBySource),
		TrafficByDevice:                deviceMetricsToPb(m.TrafficByDevice),
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
		SessionsByDay:                  timeSeriesToPb(m.SessionsByDay),
		UsersByDay:                     timeSeriesToPb(m.UsersByDay),
		PageViewsByDay:                 timeSeriesToPb(m.PageViewsByDay),
		ConversionRateByDay:            timeSeriesToPb(m.ConversionRateByDay),
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
		SessionsByDayCompare:           timeSeriesToPb(m.SessionsByDayCompare),
		UsersByDayCompare:              timeSeriesToPb(m.UsersByDayCompare),
		PageViewsByDayCompare:          timeSeriesToPb(m.PageViewsByDayCompare),
		ConversionRateByDayCompare:     timeSeriesToPb(m.ConversionRateByDayCompare),
		EmailsSent:                     metricWithComparisonToPb(m.EmailsSent),
		EmailsDelivered:                metricWithComparisonToPb(m.EmailsDelivered),
		EmailDeliveryRate:              metricWithComparisonToPb(m.EmailDeliveryRate),
		EmailOpenRate:                  metricWithComparisonToPb(m.EmailOpenRate),
		EmailClickRate:                 metricWithComparisonToPb(m.EmailClickRate),
		EmailBounceRate:                metricWithComparisonToPb(m.EmailBounceRate, true), // lower is better
	}
	if m.ComparePeriod != nil && (!m.ComparePeriod.From.IsZero() || !m.ComparePeriod.To.IsZero()) {
		pb.ComparePeriod = timeRangeToPb(*m.ComparePeriod)
	}
	return pb
}

func timeRangeToPb(tr entity.TimeRange) *pb_admin.TimeRange {
	return &pb_admin.TimeRange{
		From: timestamppb.New(tr.From),
		To:   timestamppb.New(tr.To),
	}
}

func metricWithComparisonToPb(m entity.MetricWithComparison, opts ...bool) *pb_admin.MetricWithComparison {
	lowerIsBetter := len(opts) > 0 && opts[0]
	roundToInt := len(opts) > 1 && opts[1]
	// Always derive changePct from Value vs CompareValue when we have both (single source of truth)
	var changePct float64
	if m.CompareValue != nil && !m.CompareValue.IsZero() {
		curr, prev := m.Value, *m.CompareValue
		if roundToInt {
			curr = curr.Round(0)
			prev = prev.Round(0)
		}
		if pct := computeChangePct(curr, prev); pct != nil {
			changePct = *pct
		}
	} else {
		changePct = ptrFloat64ToVal(m.ChangePct)
	}
	pb := &pb_admin.MetricWithComparison{
		Value:     &decimal.Decimal{Value: m.Value.String()},
		ChangePct: changePct,
	}
	if m.CompareValue != nil {
		pb.CompareValue = &decimal.Decimal{Value: m.CompareValue.String()}
	}
	if lowerIsBetter {
		pb.LowerIsBetter = true
	}
	return pb
}

// computeChangePct returns (current - previous) / previous * 100, or nil if previous is zero.
// Float64() returns (f, exact); we check exact=false due to binary float representation,
// but the value is acceptable for % display. Returns nil if conversion fails.
func computeChangePct(current, previous shopspring.Decimal) *float64 {
	if previous.IsZero() {
		return nil
	}
	curr, exactCurr := current.Float64()
	prev, exactPrev := previous.Float64()
	if !exactCurr || !exactPrev {
		return nil
	}
	if prev == 0 {
		return nil
	}
	pct := (curr - prev) / prev * 100
	pct = math.Round(pct*100) / 100
	return &pct
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
		pb[i] = &pb_admin.ProductMetric{
			ProductId:   int32(p.ProductId),
			ProductName: p.ProductName,
			Brand:       p.Brand,
			Value:       &decimal.Decimal{Value: p.Value.String()},
			Count:       int32(p.Count),
		}
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
			CategoryId:   int32(c.CategoryId),
			CategoryName: c.CategoryName,
			Value:        &decimal.Decimal{Value: c.Value.String()},
			Count:        int32(c.Count),
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
		Mean:   &decimal.Decimal{Value: c.Mean.String()},
		Median: &decimal.Decimal{Value: c.Median.String()},
		P90:    &decimal.Decimal{Value: c.P90.String()},
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
			Date:        timestamppb.New(m.Date),
			ProductId:   m.ProductID,
			ProductName: m.ProductName,
			ImageViews:  m.ImageViews,
			ZoomEvents:  m.ZoomEvents,
			Scroll_75:   m.Scroll75,
			Scroll_100:  m.Scroll100,
		}
	}
	return pb
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
		pb[i] = &pb_admin.RevenueParetoRow{
			Rank:          int32(m.Rank),
			ProductId:     int32(m.ProductID),
			ProductName:   m.ProductName,
			Revenue:       &decimal.Decimal{Value: m.Revenue.String()},
			CumulativePct: m.CumulativePct,
		}
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
			ProductId:     int32(r.ProductID),
			ProductName:   r.ProductName,
			SizeId:        int32(r.SizeID),
			SizeName:      r.SizeName,
			Quantity:      int32(r.Quantity),
			AvgDailySales: r.AvgDailySales,
			DaysOnHand:    r.DaysOnHand,
		}
	}
	return pb
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
		}
	}
	return pb
}

func ConvertSlowMoversToPb(list []entity.SlowMoverRow) []*pb_admin.SlowMoverRow {
	if len(list) == 0 {
		return nil
	}
	pb := make([]*pb_admin.SlowMoverRow, len(list))
	for i, r := range list {
		pb[i] = &pb_admin.SlowMoverRow{
			ProductId:   int32(r.ProductID),
			ProductName: r.ProductName,
			Revenue:     &decimal.Decimal{Value: r.Revenue.String()},
			UnitsSold:   r.UnitsSold,
			DaysInStock: r.DaysInStock,
		}
		if r.LastSaleDate != nil {
			pb[i].LastSaleDate = timestamppb.New(*r.LastSaleDate)
		}
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
			CartRate:       r.CartRate,
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
		AvgCartRate:  a.AvgCartRate,
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
			CartRate:       r.CartRate,
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
			GlobalCartRate:  r.GlobalCartRate,
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
			AbandonmentRate:      r.AbandonmentRate,
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
		})
	}
	return pb
}
