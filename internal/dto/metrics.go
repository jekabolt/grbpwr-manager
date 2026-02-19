package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
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
		ItemsPerOrder:                  metricWithComparisonToPb(m.ItemsPerOrder),
		RefundRate:                     metricWithComparisonToPb(m.RefundRate),
		PromoUsageRate:                 metricWithComparisonToPb(m.PromoUsageRate),
		GrossRevenue:                   metricWithComparisonToPb(m.GrossRevenue),
		TotalRefunded:                  metricWithComparisonToPb(m.TotalRefunded),
		TotalDiscount:                  metricWithComparisonToPb(m.TotalDiscount),
		Sessions:                       metricWithComparisonToPb(m.Sessions),
		Users:                          metricWithComparisonToPb(m.Users),
		NewUsers:                       metricWithComparisonToPb(m.NewUsers),
		PageViews:                      metricWithComparisonToPb(m.PageViews),
		BounceRate:                     metricWithComparisonToPb(m.BounceRate),
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

func metricWithComparisonToPb(m entity.MetricWithComparison) *pb_admin.MetricWithComparison {
	pb := &pb_admin.MetricWithComparison{
		Value:     &decimal.Decimal{Value: m.Value.String()},
		ChangePct: ptrFloat64ToVal(m.ChangePct),
	}
	if m.CompareValue != nil {
		pb.CompareValue = &decimal.Decimal{Value: m.CompareValue.String()}
	}
	return pb
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
