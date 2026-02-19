package store

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"
)

type metricsStore struct {
	*MYSQLStore
}

// Metrics returns an object implementing the Metrics interface.
func (ms *MYSQLStore) Metrics() dependency.Metrics {
	return &metricsStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) GetBusinessMetrics(ctx context.Context, period, comparePeriod entity.TimeRange, granularity entity.MetricsGranularity) (*entity.BusinessMetrics, error) {
	if granularity == 0 {
		granularity = entity.MetricsGranularityDay
	}
	dateExpr, subDateExpr := granularitySQL(granularity)
	shippedDateExpr := granularityDateExpr(granularity, "first_shipped.shipped_at")
	deliveredDateExpr := granularityDateExpr(granularity, "first_delivered.delivered_at")

	m := &entity.BusinessMetrics{Period: period}
	if !comparePeriod.From.IsZero() || !comparePeriod.To.IsZero() {
		m.ComparePeriod = &comparePeriod
	}
	hasCompare := !comparePeriod.From.IsZero() && !comparePeriod.To.IsZero()

	var (
		rev, cRev                      decimal.Decimal
		orders, cOrders                int
		aov, cAov                      decimal.Decimal
		itemsPerOrder, cItemsPerOrder  decimal.Decimal
		revRefund, cRevRefund          decimal.Decimal
		totalDiscount, cTotalDiscount  decimal.Decimal
		promoOrders, cPromoOrders      int
		newSubs, cNewSubs              int
		repeatRate, avgOrders, avgDays decimal.Decimal
	)

	g, gctx := errgroup.WithContext(ctx)

	// --- HOT: Core sales + time series (dashboard charts, refreshed every few seconds) ---
	// Core sales (period)
	g.Go(func() error {
		var err error
		rev, orders, aov, err = ms.getCoreSalesMetrics(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		itemsPerOrder, err = ms.getItemsPerOrder(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		revRefund, _, err = ms.getRefundMetrics(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		totalDiscount, err = ms.getTotalDiscount(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		promoOrders, err = ms.getPromoUsageCount(gctx, period.From, period.To)
		return err
	})

	// Core sales (compare)
	if hasCompare {
		g.Go(func() error {
			var err error
			cRev, cOrders, cAov, err = ms.getCoreSalesMetrics(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
		g.Go(func() error {
			var err error
			cItemsPerOrder, err = ms.getItemsPerOrder(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
		g.Go(func() error {
			var err error
			cRevRefund, _, err = ms.getRefundMetrics(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
		g.Go(func() error {
			var err error
			cTotalDiscount, err = ms.getTotalDiscount(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
		g.Go(func() error {
			var err error
			cPromoOrders, err = ms.getPromoUsageCount(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
	}

	// --- COLD: Breakdowns (geography, products, customers; can be lazy-loaded or cached longer) ---
	// Geography
	g.Go(func() error {
		var err error
		m.RevenueByCountry, err = ms.getRevenueByGeography(gctx, period.From, period.To, "country", nil, nil)
		return err
	})
	g.Go(func() error {
		var err error
		m.RevenueByCity, err = ms.getRevenueByGeography(gctx, period.From, period.To, "city", nil, nil)
		return err
	})
	g.Go(func() error {
		var err error
		m.AvgOrderByCountry, err = ms.getAvgOrderByGeography(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		m.SessionsByCountry, err = ms.GA4().GetGA4SessionsByCountry(gctx, period.From, period.To, 50)
		return err
	})

	// Currency + payment
	g.Go(func() error {
		var err error
		m.RevenueByCurrency, err = ms.getRevenueByCurrency(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		m.RevenueByPaymentMethod, err = ms.getRevenueByPaymentMethod(gctx, period.From, period.To)
		return err
	})

	// Products
	g.Go(func() error {
		var err error
		m.TopProductsByRevenue, err = ms.getTopProductsByRevenue(gctx, period.From, period.To, 20)
		return err
	})
	g.Go(func() error {
		var err error
		m.TopProductsByQuantity, err = ms.getTopProductsByQuantity(gctx, period.From, period.To, 20)
		return err
	})
	g.Go(func() error {
		var err error
		m.TopProductsByViews, err = ms.GA4().GetGA4ProductPageMetrics(gctx, period.From, period.To, 20)
		return err
	})
	g.Go(func() error {
		var err error
		m.RevenueByCategory, err = ms.getRevenueByCategory(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		m.CrossSellPairs, err = ms.getCrossSellPairs(gctx, period.From, period.To, 15)
		return err
	})

	// Traffic Sources
	g.Go(func() error {
		var err error
		m.TrafficBySource, err = ms.GA4().GetGA4TrafficSourceMetrics(gctx, period.From, period.To, 20)
		return err
	})
	g.Go(func() error {
		var err error
		m.TrafficByDevice, err = ms.GA4().GetGA4DeviceMetrics(gctx, period.From, period.To)
		return err
	})

	// Customers
	g.Go(func() error {
		var err error
		newSubs, err = ms.GetNewSubscribersCount(gctx, period.From, period.To)
		return err
	})
	if hasCompare {
		g.Go(func() error {
			var err error
			cNewSubs, err = ms.GetNewSubscribersCount(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
	}
	g.Go(func() error {
		var err error
		repeatRate, avgOrders, avgDays, err = ms.getRepeatCustomerMetrics(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		m.CLVDistribution, err = ms.getCLVStats(gctx, period.From, period.To)
		return err
	})

	// Promo + order status
	g.Go(func() error {
		var err error
		m.RevenueByPromo, err = ms.getRevenueByPromo(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		m.OrdersByStatus, err = ms.getOrdersByStatus(gctx, period.From, period.To)
		return err
	})

	// --- HOT: Time series (period) ---
	g.Go(func() error {
		var err error
		m.RevenueByDay, err = ms.getRevenueByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.OrdersByDay, err = ms.getOrdersByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.SubscribersByDay, err = ms.getSubscribersByPeriod(gctx, period.From, period.To, subDateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.GrossRevenueByDay, err = ms.getGrossRevenueByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.RefundsByDay, err = ms.getRefundsByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.AvgOrderValueByDay, err = ms.getAvgOrderValueByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.UnitsSoldByDay, err = ms.getUnitsSoldByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.NewCustomersByDay, m.ReturningCustomersByDay, err = ms.getNewVsReturningCustomersByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.ShippedByDay, err = ms.getShippedByPeriod(gctx, period.From, period.To, shippedDateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.DeliveredByDay, err = ms.getDeliveredByPeriod(gctx, period.From, period.To, deliveredDateExpr)
		return err
	})

	// --- GA4: Time series (period) ---
	g.Go(func() error {
		var err error
		ga4Metrics, err := ms.GA4().GetGA4DailyMetrics(gctx, period.From, period.To)
		if err != nil {
			return err
		}
		m.SessionsByDay = make([]entity.TimeSeriesPoint, len(ga4Metrics))
		m.UsersByDay = make([]entity.TimeSeriesPoint, len(ga4Metrics))
		m.PageViewsByDay = make([]entity.TimeSeriesPoint, len(ga4Metrics))
		for i, ga := range ga4Metrics {
			m.SessionsByDay[i] = entity.TimeSeriesPoint{
				Date:  ga.Date,
				Value: decimal.NewFromInt(int64(ga.Sessions)),
				Count: ga.Sessions,
			}
			m.UsersByDay[i] = entity.TimeSeriesPoint{
				Date:  ga.Date,
				Value: decimal.NewFromInt(int64(ga.Users)),
				Count: ga.Users,
			}
			m.PageViewsByDay[i] = entity.TimeSeriesPoint{
				Date:  ga.Date,
				Value: decimal.NewFromInt(int64(ga.PageViews)),
				Count: ga.PageViews,
			}
		}
		return nil
	})

	// --- HOT: Time series (compare) ---
	if hasCompare {
		g.Go(func() error {
			var err error
			m.RevenueByDayCompare, err = ms.getRevenueByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.OrdersByDayCompare, err = ms.getOrdersByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.SubscribersByDayCompare, err = ms.getSubscribersByPeriod(gctx, comparePeriod.From, comparePeriod.To, subDateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.GrossRevenueByDayCompare, err = ms.getGrossRevenueByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.RefundsByDayCompare, err = ms.getRefundsByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.AvgOrderValueByDayCompare, err = ms.getAvgOrderValueByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.UnitsSoldByDayCompare, err = ms.getUnitsSoldByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.NewCustomersByDayCompare, m.ReturningCustomersByDayCompare, err = ms.getNewVsReturningCustomersByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.ShippedByDayCompare, err = ms.getShippedByPeriod(gctx, comparePeriod.From, comparePeriod.To, shippedDateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.DeliveredByDayCompare, err = ms.getDeliveredByPeriod(gctx, comparePeriod.From, comparePeriod.To, deliveredDateExpr)
			return err
		})

		// --- GA4: Time series (compare) ---
		g.Go(func() error {
			var err error
			ga4Metrics, err := ms.GA4().GetGA4DailyMetrics(gctx, comparePeriod.From, comparePeriod.To)
			if err != nil {
				return err
			}
			m.SessionsByDayCompare = make([]entity.TimeSeriesPoint, len(ga4Metrics))
			m.UsersByDayCompare = make([]entity.TimeSeriesPoint, len(ga4Metrics))
			m.PageViewsByDayCompare = make([]entity.TimeSeriesPoint, len(ga4Metrics))
			for i, ga := range ga4Metrics {
				m.SessionsByDayCompare[i] = entity.TimeSeriesPoint{
					Date:  ga.Date,
					Value: decimal.NewFromInt(int64(ga.Sessions)),
					Count: ga.Sessions,
				}
				m.UsersByDayCompare[i] = entity.TimeSeriesPoint{
					Date:  ga.Date,
					Value: decimal.NewFromInt(int64(ga.Users)),
					Count: ga.Users,
				}
				m.PageViewsByDayCompare[i] = entity.TimeSeriesPoint{
					Date:  ga.Date,
					Value: decimal.NewFromInt(int64(ga.PageViews)),
					Count: ga.PageViews,
				}
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		slog.Default().ErrorContext(ctx, "GetBusinessMetrics: metrics query failed", slog.String("err", err.Error()))
		return nil, err
	}

	// GA4 aggregate metrics for period (needed for compare block and final values)
	var totalSessions, totalUsers, totalNewUsers, totalPageViews int
	var totalBounceRate, totalAvgSessionDuration, totalPagesPerSession float64
	ga4Daily, _ := ms.GA4().GetGA4DailyMetrics(ctx, period.From, period.To)
	for _, d := range ga4Daily {
		totalSessions += d.Sessions
		totalUsers += d.Users
		totalNewUsers += d.NewUsers
		totalPageViews += d.PageViews
		totalBounceRate += d.BounceRate
		totalAvgSessionDuration += d.AvgSessionDuration
		totalPagesPerSession += d.PagesPerSession
	}

	if hasCompare {
		m.NewSubscribers.CompareValue = ptr(decimal.NewFromInt(int64(cNewSubs)))
		m.NewSubscribers.ChangePct = changePctInt(newSubs, cNewSubs)

		// GA4 compare metrics
		var cTotalSessions, cTotalUsers, cTotalNewUsers, cTotalPageViews int
		ga4DailyCompare, _ := ms.GA4().GetGA4DailyMetrics(ctx, comparePeriod.From, comparePeriod.To)
		for _, d := range ga4DailyCompare {
			cTotalSessions += d.Sessions
			cTotalUsers += d.Users
			cTotalNewUsers += d.NewUsers
			cTotalPageViews += d.PageViews
		}
		m.Sessions.CompareValue = ptr(decimal.NewFromInt(int64(cTotalSessions)))
		m.Sessions.ChangePct = changePctInt(totalSessions, cTotalSessions)
		m.Users.CompareValue = ptr(decimal.NewFromInt(int64(cTotalUsers)))
		m.Users.ChangePct = changePctInt(totalUsers, cTotalUsers)
		m.NewUsers.CompareValue = ptr(decimal.NewFromInt(int64(cTotalNewUsers)))
		m.NewUsers.ChangePct = changePctInt(totalNewUsers, cTotalNewUsers)
		m.PageViews.CompareValue = ptr(decimal.NewFromInt(int64(cTotalPageViews)))
		m.PageViews.ChangePct = changePctInt(totalPageViews, cTotalPageViews)

		// Conversion rate compare
		if cTotalSessions > 0 {
			cConvRate := decimal.NewFromInt(int64(cOrders)).Div(decimal.NewFromInt(int64(cTotalSessions))).Mul(decimal.NewFromInt(100))
			m.ConversionRate.CompareValue = &cConvRate
			if !m.ConversionRate.Value.IsZero() {
				changePct := m.ConversionRate.Value.Sub(cConvRate).Div(cConvRate).Mul(decimal.NewFromInt(100))
				f, _ := changePct.Float64()
				m.ConversionRate.ChangePct = &f
			}

			cRevPerSession := cRev.Div(decimal.NewFromInt(int64(cTotalSessions)))
			m.RevenuePerSession.CompareValue = &cRevPerSession
			if !m.RevenuePerSession.Value.IsZero() {
				changePct := m.RevenuePerSession.Value.Sub(cRevPerSession).Div(cRevPerSession).Mul(decimal.NewFromInt(100))
				f, _ := changePct.Float64()
				m.RevenuePerSession.ChangePct = &f
			}
		}

		// Conversion rate by day compare
		m.ConversionRateByDayCompare = make([]entity.TimeSeriesPoint, 0)
		ordersByDayCompareMap := make(map[string]int)
		for _, o := range m.OrdersByDayCompare {
			ordersByDayCompareMap[o.Date.Format("2006-01-02")] = o.Count
		}
		for _, s := range m.SessionsByDayCompare {
			dateKey := s.Date.Format("2006-01-02")
			ordersCount := ordersByDayCompareMap[dateKey]
			convRate := decimal.Zero
			if s.Count > 0 {
				convRate = decimal.NewFromInt(int64(ordersCount)).Div(decimal.NewFromInt(int64(s.Count))).Mul(decimal.NewFromInt(100))
			}
			m.ConversionRateByDayCompare = append(m.ConversionRateByDayCompare, entity.TimeSeriesPoint{
				Date:  s.Date,
				Value: convRate,
				Count: ordersCount,
			})
		}
	}

	// Derived values from core sales
	m.Revenue.Value = rev
	m.OrdersCount.Value = decimal.NewFromInt(int64(orders))
	m.AvgOrderValue.Value = aov
	m.ItemsPerOrder.Value = itemsPerOrder
	grossRev := rev.Add(revRefund)
	if grossRev.GreaterThan(decimal.Zero) {
		m.RefundRate.Value = revRefund.Div(grossRev).Mul(decimal.NewFromInt(100))
	}
	m.GrossRevenue.Value = grossRev
	m.TotalRefunded.Value = revRefund
	m.TotalDiscount.Value = totalDiscount
	if orders > 0 {
		m.PromoUsageRate.Value = decimal.NewFromInt(int64(promoOrders)).Div(decimal.NewFromInt(int64(orders))).Mul(decimal.NewFromInt(100))
	}
	m.NewSubscribers.Value = decimal.NewFromInt(int64(newSubs))
	m.RepeatCustomersRate.Value = repeatRate
	m.AvgOrdersPerCustomer.Value = avgOrders
	m.AvgDaysBetweenOrders.Value = avgDays

	// GA4 aggregate metrics (totalSessions etc. computed above)
	m.Sessions.Value = decimal.NewFromInt(int64(totalSessions))
	m.Users.Value = decimal.NewFromInt(int64(totalUsers))
	m.NewUsers.Value = decimal.NewFromInt(int64(totalNewUsers))
	m.PageViews.Value = decimal.NewFromInt(int64(totalPageViews))
	if len(ga4Daily) > 0 {
		m.BounceRate.Value = decimal.NewFromFloat(totalBounceRate / float64(len(ga4Daily)))
		m.AvgSessionDuration.Value = decimal.NewFromFloat(totalAvgSessionDuration / float64(len(ga4Daily)))
		m.PagesPerSession.Value = decimal.NewFromFloat(totalPagesPerSession / float64(len(ga4Daily)))
	}
	
	// Conversion rate = orders / sessions
	if totalSessions > 0 {
		m.ConversionRate.Value = decimal.NewFromInt(int64(orders)).Div(decimal.NewFromInt(int64(totalSessions))).Mul(decimal.NewFromInt(100))
		m.RevenuePerSession.Value = rev.Div(decimal.NewFromInt(int64(totalSessions)))
	}

	// Compute conversion rate by day
	m.ConversionRateByDay = make([]entity.TimeSeriesPoint, 0)
	ordersByDayMap := make(map[string]int)
	for _, o := range m.OrdersByDay {
		ordersByDayMap[o.Date.Format("2006-01-02")] = o.Count
	}
	for _, s := range m.SessionsByDay {
		dateKey := s.Date.Format("2006-01-02")
		ordersCount := ordersByDayMap[dateKey]
		convRate := decimal.Zero
		if s.Count > 0 {
			convRate = decimal.NewFromInt(int64(ordersCount)).Div(decimal.NewFromInt(int64(s.Count))).Mul(decimal.NewFromInt(100))
		}
		m.ConversionRateByDay = append(m.ConversionRateByDay, entity.TimeSeriesPoint{
			Date:  s.Date,
			Value: convRate,
			Count: ordersCount,
		})
	}

	if hasCompare {
		m.Revenue.CompareValue = &cRev
		m.OrdersCount.CompareValue = ptr(decimal.NewFromInt(int64(cOrders)))
		m.AvgOrderValue.CompareValue = &cAov
		m.Revenue.ChangePct = changePct(rev, cRev)
		m.OrdersCount.ChangePct = changePctInt(orders, cOrders)
		m.AvgOrderValue.ChangePct = changePct(aov, cAov)
		m.ItemsPerOrder.CompareValue = &cItemsPerOrder
		m.ItemsPerOrder.ChangePct = changePct(itemsPerOrder, cItemsPerOrder)
		cGross := cRev.Add(cRevRefund)
		if cGross.GreaterThan(decimal.Zero) {
			cRefundRate := cRevRefund.Div(cGross).Mul(decimal.NewFromInt(100))
			m.RefundRate.CompareValue = &cRefundRate
			m.RefundRate.ChangePct = changePct(m.RefundRate.Value, cRefundRate)
		}
		if cOrders > 0 {
			cPromoRate := decimal.NewFromInt(int64(cPromoOrders)).Div(decimal.NewFromInt(int64(cOrders))).Mul(decimal.NewFromInt(100))
			m.PromoUsageRate.CompareValue = &cPromoRate
			m.PromoUsageRate.ChangePct = changePct(m.PromoUsageRate.Value, cPromoRate)
		}
		m.GrossRevenue.CompareValue = ptr(cRev.Add(cRevRefund))
		m.TotalRefunded.CompareValue = &cRevRefund
		m.GrossRevenue.ChangePct = changePct(grossRev, cRev.Add(cRevRefund))
		m.TotalRefunded.ChangePct = changePct(revRefund, cRevRefund)
		m.TotalDiscount.CompareValue = &cTotalDiscount
		m.TotalDiscount.ChangePct = changePct(totalDiscount, cTotalDiscount)
	}

	// Region depends on country (run after parallel wait)
	var err error
	m.RevenueByRegion, err = ms.getRevenueByRegion(m.RevenueByCountry)
	if err != nil {
		return nil, fmt.Errorf("revenue by region: %w", err)
	}

	// Gap-fill time series (data already fetched in parallel)
	m.RevenueByDay = fillTimeSeriesGaps(m.RevenueByDay, period.From, period.To, granularity)
	m.OrdersByDay = fillTimeSeriesGaps(m.OrdersByDay, period.From, period.To, granularity)
	m.SubscribersByDay = fillTimeSeriesGaps(m.SubscribersByDay, period.From, period.To, granularity)
	m.GrossRevenueByDay = fillTimeSeriesGaps(m.GrossRevenueByDay, period.From, period.To, granularity)
	m.RefundsByDay = fillTimeSeriesGaps(m.RefundsByDay, period.From, period.To, granularity)
	m.AvgOrderValueByDay = fillTimeSeriesGaps(m.AvgOrderValueByDay, period.From, period.To, granularity)
	m.UnitsSoldByDay = fillTimeSeriesGaps(m.UnitsSoldByDay, period.From, period.To, granularity)
	m.NewCustomersByDay = fillTimeSeriesGaps(m.NewCustomersByDay, period.From, period.To, granularity)
	m.ReturningCustomersByDay = fillTimeSeriesGaps(m.ReturningCustomersByDay, period.From, period.To, granularity)
	m.ShippedByDay = fillTimeSeriesGaps(m.ShippedByDay, period.From, period.To, granularity)
	m.DeliveredByDay = fillTimeSeriesGaps(m.DeliveredByDay, period.From, period.To, granularity)
	if hasCompare {
		m.RevenueByDayCompare = fillTimeSeriesGaps(m.RevenueByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.OrdersByDayCompare = fillTimeSeriesGaps(m.OrdersByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.SubscribersByDayCompare = fillTimeSeriesGaps(m.SubscribersByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.GrossRevenueByDayCompare = fillTimeSeriesGaps(m.GrossRevenueByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.RefundsByDayCompare = fillTimeSeriesGaps(m.RefundsByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.AvgOrderValueByDayCompare = fillTimeSeriesGaps(m.AvgOrderValueByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.UnitsSoldByDayCompare = fillTimeSeriesGaps(m.UnitsSoldByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.NewCustomersByDayCompare = fillTimeSeriesGaps(m.NewCustomersByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.ReturningCustomersByDayCompare = fillTimeSeriesGaps(m.ReturningCustomersByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.ShippedByDayCompare = fillTimeSeriesGaps(m.ShippedByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.DeliveredByDayCompare = fillTimeSeriesGaps(m.DeliveredByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
	}

	return m, nil
}

func (ms *MYSQLStore) getCoreSalesMetrics(ctx context.Context, from, to time.Time) (revenue decimal.Decimal, orders int, aov decimal.Decimal, err error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	type row struct {
		Revenue decimal.Decimal `db:"revenue"`
		Orders  int             `db:"orders"`
	}
	query := `
		WITH order_base AS (
			SELECT co.id,
				COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
				COALESCE(MAX(scp.price), 0) AS shipment_base,
				COALESCE(MAX(pc.discount), 0) AS discount,
				COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
				co.total_price,
				COALESCE(co.refunded_amount, 0) AS refunded_amount
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment s ON co.id = s.order_id
			LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
			GROUP BY co.id, co.total_price, co.refunded_amount
		)
		SELECT
			COALESCE(SUM(
				(items_base * (100 - discount) / 100 + CASE WHEN free_shipping THEN 0 ELSE shipment_base END)
				* (total_price - refunded_amount) / NULLIF(total_price, 0)
			), 0) AS revenue,
			COUNT(*) AS orders
		FROM order_base
	`
	r, err := QueryNamedOne[row](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return decimal.Zero, 0, decimal.Zero, err
	}
	revenue = r.Revenue
	orders = r.Orders
	if orders > 0 {
		aov = revenue.Div(decimal.NewFromInt(int64(orders))).Round(2)
	}
	return revenue, orders, aov, nil
}

func (ms *MYSQLStore) getItemsPerOrder(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
	type row struct {
		TotalItems int `db:"total_items"`
		Orders     int `db:"orders"`
	}
	query := `
		SELECT COALESCE(SUM(item_count), 0) AS total_items, COUNT(*) AS orders
		FROM (
			SELECT co.id, SUM(oi.quantity) AS item_count
			FROM customer_order co
			JOIN order_item oi ON co.id = oi.order_id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
			GROUP BY co.id
		) AS order_items
	`
	r, err := QueryNamedOne[row](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return decimal.Zero, err
	}
	if r.Orders == 0 {
		return decimal.Zero, nil
	}
	return decimal.NewFromInt(int64(r.TotalItems)).Div(decimal.NewFromInt(int64(r.Orders))).Round(2), nil
}

func (ms *MYSQLStore) getRefundMetrics(ctx context.Context, from, to time.Time) (refundedAmount decimal.Decimal, refundedOrders int, err error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	type row struct {
		Amount decimal.Decimal `db:"amount"`
		Count  int             `db:"cnt"`
	}
	query := `
		WITH order_base AS (
			SELECT co.id,
				COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
				COALESCE(MAX(scp.price), 0) AS shipment_base,
				COALESCE(MAX(pc.discount), 0) AS discount,
				COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
				co.total_price,
				COALESCE(co.refunded_amount, 0) AS refunded_amount
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment s ON co.id = s.order_id
			LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds) AND (co.refunded_amount IS NOT NULL AND co.refunded_amount > 0)
			GROUP BY co.id, co.total_price, co.refunded_amount
		)
		SELECT
			COALESCE(SUM(
				refunded_amount * (items_base * (100 - discount) / 100 + CASE WHEN free_shipping THEN 0 ELSE shipment_base END) / NULLIF(total_price, 0)
			), 0) AS amount,
			COUNT(*) AS cnt
		FROM order_base
	`
	r, err := QueryNamedOne[row](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForRefund()})
	if err != nil {
		return decimal.Zero, 0, err
	}
	return r.Amount, r.Count, nil
}

func (ms *MYSQLStore) getTotalDiscount(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	params := map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()}
	productDiscount, err := QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, ms.DB(), `
		SELECT COALESCE(SUM(pp_base.price * COALESCE(oi.product_sale_percentage, 0) / 100 * oi.quantity), 0) AS v
		FROM customer_order co
		JOIN order_item oi ON co.id = oi.order_id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
	`, params)
	if err != nil {
		return decimal.Zero, err
	}
	promoDiscount, err := QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, ms.DB(), `
		WITH order_items_base AS (
			SELECT co.id,
				COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
				COALESCE(pc.discount, 0) AS discount
			FROM customer_order co
			LEFT JOIN order_item oi ON co.id = oi.order_id
			LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON co.promo_id = pc.id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds) AND co.promo_id IS NOT NULL
			GROUP BY co.id, pc.discount
		)
		SELECT COALESCE(SUM(items_base * discount / 100), 0) AS v
		FROM order_items_base
	`, params)
	if err != nil {
		return decimal.Zero, err
	}
	return productDiscount.V.Add(promoDiscount.V), nil
}

func (ms *MYSQLStore) getRevenueByPaymentMethod(ctx context.Context, from, to time.Time) ([]entity.PaymentMethodMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.total_price, co.refunded_amount
			) ob
		)
		SELECT COALESCE(p.payment_method_type, pm.name) AS payment_method,
			COALESCE(SUM(ob.revenue_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base ob
		JOIN payment p ON p.order_id = ob.id
		JOIN payment_method pm ON p.payment_method_id = pm.id
		GROUP BY COALESCE(p.payment_method_type, pm.name)
		ORDER BY value DESC
	`
	rows, err := QueryListNamed[struct {
		PaymentMethod string          `db:"payment_method"`
		Value         decimal.Decimal `db:"value"`
		Count         int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.PaymentMethodMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.PaymentMethodMetric{PaymentMethod: r.PaymentMethod, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getPromoUsageCount(ctx context.Context, from, to time.Time) (int, error) {
	type row struct {
		N int `db:"n"`
	}
	query := `
		SELECT COUNT(*) AS n FROM customer_order
		WHERE placed >= :from AND placed < :to
		AND order_status_id IN (:statusIds) AND promo_id IS NOT NULL
	`
	r, err := QueryNamedOne[row](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return 0, err
	}
	return r.N, nil
}

func (ms *MYSQLStore) getRevenueByGeography(ctx context.Context, from, to time.Time, groupBy string, country, city *string) ([]entity.GeographyMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	var groupCol, selectCol string
	if groupBy == "city" {
		groupCol = "a.country, a.state, a.city"
		selectCol = "a.country, a.state, a.city"
	} else {
		groupCol = "a.country"
		selectCol = "a.country AS country, NULL AS state, NULL AS city"
	}
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.total_price, co.refunded_amount
			) ob
		)
		SELECT %s,
			COALESCE(SUM(ob.revenue_base), 0) AS value,
			COUNT(DISTINCT ob.id) AS cnt
		FROM order_base ob
		JOIN buyer b ON ob.id = b.order_id
		JOIN address a ON b.shipping_address_id = a.id
		WHERE (:country IS NULL OR a.country = :country)
		AND (:city IS NULL OR a.city = :city)
		GROUP BY %s
		ORDER BY value DESC
		LIMIT 50
	`, selectCol, groupCol)

	params := map[string]any{"from": from, "to": to, "country": country, "city": city, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()}
	rows, err := QueryListNamed[struct {
		Country string          `db:"country"`
		State   *string         `db:"state"`
		City    *string         `db:"city"`
		Value   decimal.Decimal `db:"value"`
		Count   int             `db:"cnt"`
	}](ctx, ms.DB(), query, params)
	if err != nil {
		return nil, err
	}

	result := make([]entity.GeographyMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.GeographyMetric{
			Country: r.Country,
			State:   r.State,
			City:    r.City,
			Value:   r.Value,
			Count:   r.Count,
		}
	}
	return result, nil
}

func (ms *MYSQLStore) getAvgOrderByGeography(ctx context.Context, from, to time.Time) ([]entity.GeographyMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.total_price, co.refunded_amount
			) ob
		)
		SELECT a.country,
			COALESCE(AVG(ob.revenue_base), 0) AS value,
			COUNT(DISTINCT ob.id) AS cnt
		FROM order_base ob
		JOIN buyer b ON ob.id = b.order_id
		JOIN address a ON b.shipping_address_id = a.id
		GROUP BY a.country
		ORDER BY value DESC
		LIMIT 30
	`
	rows, err := QueryListNamed[struct {
		Country string          `db:"country"`
		Value   decimal.Decimal `db:"value"`
		Count   int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.GeographyMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.GeographyMetric{Country: r.Country, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getRevenueByRegion(byCountry []entity.GeographyMetric) ([]entity.RegionMetric, error) {
	regionAgg := make(map[string]struct {
		value decimal.Decimal
		count int
	})
	for _, g := range byCountry {
		cc := strings.ToUpper(strings.TrimSpace(g.Country))
		region, ok := entity.CountryToRegion(cc)
		regionKey := "OTHER"
		if ok {
			regionKey = string(region)
		}
		agg := regionAgg[regionKey]
		agg.value = agg.value.Add(g.Value)
		agg.count += g.Count
		regionAgg[regionKey] = agg
	}
	result := make([]entity.RegionMetric, 0, len(regionAgg))
	for region, agg := range regionAgg {
		result = append(result, entity.RegionMetric{Region: region, Value: agg.value, Count: agg.count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Value.GreaterThan(result[j].Value) })
	return result, nil
}

func (ms *MYSQLStore) getRevenueByCurrency(ctx context.Context, from, to time.Time) ([]entity.CurrencyMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id, ob.currency,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id, co.currency,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.currency, co.total_price, co.refunded_amount
			) ob
		)
		SELECT currency,
			COALESCE(SUM(revenue_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY currency
		ORDER BY value DESC
	`
	rows, err := QueryListNamed[struct {
		Currency string          `db:"currency"`
		Value    decimal.Decimal `db:"value"`
		Count    int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.CurrencyMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.CurrencyMetric{Currency: r.Currency, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getTopProductsByRevenue(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT oi.product_id, p.brand,
			(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1) AS product_name,
			COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS value,
			SUM(oi.quantity) AS cnt
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		JOIN customer_order co ON oi.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY oi.product_id, p.brand
		ORDER BY value DESC
		LIMIT :limit
	`
	rows, err := QueryListNamed[struct {
		ProductId   int             `db:"product_id"`
		Brand       string          `db:"brand"`
		ProductName string          `db:"product_name"`
		Value       decimal.Decimal `db:"value"`
		Count       int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "limit": limit, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.ProductMetric, len(rows))
	for i, r := range rows {
		productName := r.ProductName
		if productName == "" {
			productName = r.Brand
		}
		result[i] = entity.ProductMetric{ProductId: r.ProductId, ProductName: productName, Brand: r.Brand, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getTopProductsByQuantity(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT oi.product_id, p.brand,
			(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1) AS product_name,
			SUM(oi.quantity) AS cnt,
			COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS value
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		JOIN customer_order co ON oi.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY oi.product_id, p.brand
		ORDER BY cnt DESC
		LIMIT :limit
	`
	rows, err := QueryListNamed[struct {
		ProductId   int             `db:"product_id"`
		Brand       string          `db:"brand"`
		ProductName string          `db:"product_name"`
		Count       int             `db:"cnt"`
		Value       decimal.Decimal `db:"value"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "limit": limit, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.ProductMetric, len(rows))
	for i, r := range rows {
		productName := r.ProductName
		if productName == "" {
			productName = r.Brand
		}
		result[i] = entity.ProductMetric{ProductId: r.ProductId, ProductName: productName, Brand: r.Brand, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getRevenueByCategory(ctx context.Context, from, to time.Time) ([]entity.CategoryMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT p.top_category_id AS category_id, c.name AS category_name,
			COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS value,
			SUM(oi.quantity) AS cnt
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		JOIN category c ON p.top_category_id = c.id
		JOIN customer_order co ON oi.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY p.top_category_id, c.name
		ORDER BY value DESC
		LIMIT 30
	`
	rows, err := QueryListNamed[struct {
		CategoryId   int             `db:"category_id"`
		CategoryName string          `db:"category_name"`
		Value        decimal.Decimal `db:"value"`
		Count        int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.CategoryMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.CategoryMetric{CategoryId: r.CategoryId, CategoryName: r.CategoryName, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getCrossSellPairs(ctx context.Context, from, to time.Time, limit int) ([]entity.CrossSellPair, error) {
	query := `
		SELECT oi1.product_id AS product_a_id, oi2.product_id AS product_b_id,
			COALESCE((SELECT pt.name FROM product_translation pt WHERE pt.product_id = p1.id ORDER BY pt.language_id LIMIT 1), p1.brand) AS product_a_name,
			COALESCE((SELECT pt.name FROM product_translation pt WHERE pt.product_id = p2.id ORDER BY pt.language_id LIMIT 1), p2.brand) AS product_b_name,
			COUNT(*) AS cnt
		FROM order_item oi1
		JOIN order_item oi2 ON oi1.order_id = oi2.order_id AND oi1.product_id < oi2.product_id
		JOIN product p1 ON oi1.product_id = p1.id
		JOIN product p2 ON oi2.product_id = p2.id
		JOIN customer_order co ON oi1.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY oi1.product_id, oi2.product_id, p1.brand, p2.brand
		ORDER BY cnt DESC
		LIMIT :limit
	`
	rows, err := QueryListNamed[struct {
		ProductAId   int    `db:"product_a_id"`
		ProductBId   int    `db:"product_b_id"`
		ProductAName string `db:"product_a_name"`
		ProductBName string `db:"product_b_name"`
		Count        int    `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "limit": limit, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.CrossSellPair, len(rows))
	for i, r := range rows {
		result[i] = entity.CrossSellPair{
			ProductAId:   r.ProductAId,
			ProductBId:   r.ProductBId,
			ProductAName: r.ProductAName,
			ProductBName: r.ProductBName,
			Count:        r.Count,
		}
	}
	return result, nil
}

func (ms *MYSQLStore) getRepeatCustomerMetrics(ctx context.Context, from, to time.Time) (repeatRate, avgOrders, avgDays decimal.Decimal, err error) {
	type emailOrders struct {
		Email  string `db:"email"`
		Orders int    `db:"orders"`
	}
	query := `
		SELECT b.email, COUNT(*) AS orders
		FROM customer_order co
		JOIN buyer b ON co.id = b.order_id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY b.email
	`
	rows, err := QueryListNamed[emailOrders](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, err
	}

	var repeatCount int
	var totalOrders int
	for _, r := range rows {
		totalOrders += r.Orders
		if r.Orders > 1 {
			repeatCount++
		}
	}

	totalCustomers := len(rows)
	if totalCustomers == 0 {
		return decimal.Zero, decimal.Zero, decimal.Zero, nil
	}
	repeatRate = decimal.NewFromInt(int64(repeatCount)).Div(decimal.NewFromInt(int64(totalCustomers))).Mul(decimal.NewFromInt(100))
	avgOrders = decimal.NewFromInt(int64(totalOrders)).Div(decimal.NewFromInt(int64(totalCustomers)))

	// Avg days between orders for repeat buyers — computed in SQL with LAG, no row materialization
	q2 := `
		SELECT AVG(gap_days) AS avg_days
		FROM (
			SELECT DATEDIFF(co.placed, LAG(co.placed) OVER (PARTITION BY b.email ORDER BY co.placed)) AS gap_days
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (:statusIds)
		) t
		WHERE gap_days IS NOT NULL
	`
	params := map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()}
	avgDaysRow, err := QueryNamedOne[struct {
		AvgDays *float64 `db:"avg_days"`
	}](ctx, ms.DB(), q2, params)
	if err != nil {
		return repeatRate, avgOrders, decimal.Zero, err
	}
	if avgDaysRow.AvgDays != nil {
		avgDays = decimal.NewFromFloat(*avgDaysRow.AvgDays)
	}
	return repeatRate, avgOrders, avgDays, nil
}

func (ms *MYSQLStore) getCLVStats(ctx context.Context, from, to time.Time) (entity.CLVStats, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id, b.email,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.total_price, co.refunded_amount
			) ob
			JOIN customer_order co ON ob.id = co.id
			JOIN buyer b ON co.id = b.order_id
		)
		SELECT email, COALESCE(SUM(revenue_base), 0) AS clv
		FROM order_base
		GROUP BY email
	`
	rows, err := QueryListNamed[struct {
		Email string          `db:"email"`
		CLV   decimal.Decimal `db:"clv"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return entity.CLVStats{}, err
	}

	if len(rows) == 0 {
		return entity.CLVStats{}, nil
	}

	clvs := make([]float64, 0, len(rows))
	for _, r := range rows {
		f, ok := r.CLV.Float64()
		if !ok {
			continue
		}
		clvs = append(clvs, f)
	}
	sort.Float64s(clvs)

	if len(clvs) == 0 {
		return entity.CLVStats{}, nil
	}
	mean := 0.0
	for _, v := range clvs {
		mean += v
	}
	mean /= float64(len(clvs))

	median := 0.0
	if len(clvs)%2 == 1 {
		median = clvs[len(clvs)/2]
	} else {
		median = (clvs[len(clvs)/2-1] + clvs[len(clvs)/2]) / 2
	}

	p90Idx := int(math.Ceil(float64(len(clvs))*0.9)) - 1
	if p90Idx < 0 {
		p90Idx = 0
	}
	p90 := clvs[p90Idx]

	return entity.CLVStats{
		Mean:   decimal.NewFromFloat(mean).Round(2),
		Median: decimal.NewFromFloat(median).Round(2),
		P90:    decimal.NewFromFloat(p90).Round(2),
	}, nil
}

func (ms *MYSQLStore) getRevenueByPromo(ctx context.Context, from, to time.Time) ([]entity.PromoMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id, ob.promo_id, ob.code, ob.discount,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id, pc.id AS promo_id, pc.code, pc.discount,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.total_price, co.refunded_amount, pc.id, pc.code, pc.discount
			) ob
		)
		SELECT code, COUNT(*) AS orders_count,
			COALESCE(SUM(revenue_base), 0) AS revenue,
			COALESCE(AVG(discount), 0) AS avg_discount
		FROM order_base
		GROUP BY promo_id, code
		ORDER BY revenue DESC
		LIMIT 20
	`
	rows, err := QueryListNamed[struct {
		Code        string          `db:"code"`
		OrdersCount int             `db:"orders_count"`
		Revenue     decimal.Decimal `db:"revenue"`
		AvgDiscount decimal.Decimal `db:"avg_discount"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.PromoMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.PromoMetric{
			PromoCode:   r.Code,
			OrdersCount: r.OrdersCount,
			Revenue:     r.Revenue,
			AvgDiscount: r.AvgDiscount,
		}
	}
	return result, nil
}

func (ms *MYSQLStore) getOrdersByStatus(ctx context.Context, from, to time.Time) ([]entity.StatusCount, error) {
	query := `
		SELECT os.name, COUNT(*) AS cnt
		FROM customer_order co
		JOIN order_status os ON co.order_status_id = os.id
		WHERE co.placed >= :from AND co.placed < :to
		GROUP BY co.order_status_id, os.name
		ORDER BY cnt DESC
	`
	rows, err := QueryListNamed[struct {
		Name  string `db:"name"`
		Count int    `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.StatusCount, len(rows))
	for i, r := range rows {
		result[i] = entity.StatusCount{StatusName: r.Name, Count: r.Count}
	}
	return result, nil
}

// granularitySQL returns date expression for ORDER BY/SELECT and GROUP BY.
// dateExpr for order tables (co.placed), subDateExpr for subscriber (created_at).
// Uses CONVERT_TZ to UTC so date bucketing matches Go's bucketStart (UTC).
func granularitySQL(g entity.MetricsGranularity) (dateExpr, subDateExpr string) {
	// CONVERT_TZ(col, @@session.time_zone, '+00:00') ensures DATE() uses UTC regardless of MySQL server timezone
	placedUTC := "CONVERT_TZ(co.placed, @@session.time_zone, '+00:00')"
	createdUTC := "CONVERT_TZ(created_at, @@session.time_zone, '+00:00')"
	switch g {
	case entity.MetricsGranularityWeek:
		return fmt.Sprintf("DATE(DATE_SUB(%s, INTERVAL WEEKDAY(%s) DAY))", placedUTC, placedUTC),
			fmt.Sprintf("DATE(DATE_SUB(%s, INTERVAL WEEKDAY(%s) DAY))", createdUTC, createdUTC)
	case entity.MetricsGranularityMonth:
		return fmt.Sprintf("DATE(DATE_FORMAT(%s, '%%Y-%%m-01'))", placedUTC),
			fmt.Sprintf("DATE(DATE_FORMAT(%s, '%%Y-%%m-01'))", createdUTC)
	default:
		return fmt.Sprintf("DATE(%s)", placedUTC), fmt.Sprintf("DATE(%s)", createdUTC)
	}
}

// granularityDateExpr returns date expression for a given column (e.g. osh.changed_at).
// Uses CONVERT_TZ to UTC for alignment with Go bucketStart.
func granularityDateExpr(g entity.MetricsGranularity, col string) string {
	colUTC := fmt.Sprintf("CONVERT_TZ(%s, @@session.time_zone, '+00:00')", col)
	switch g {
	case entity.MetricsGranularityWeek:
		return fmt.Sprintf("DATE(DATE_SUB(%s, INTERVAL WEEKDAY(%s) DAY))", colUTC, colUTC)
	case entity.MetricsGranularityMonth:
		return fmt.Sprintf("DATE(DATE_FORMAT(%s, '%%Y-%%m-01'))", colUTC)
	default:
		return fmt.Sprintf("DATE(%s)", colUTC)
	}
}

func (ms *MYSQLStore) getRevenueByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id, co.placed,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.placed, co.total_price, co.refunded_amount
			) ob
		)
		SELECT %s AS d,
			COALESCE(SUM(revenue_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getOrdersByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d, COUNT(*) AS cnt, 0 AS value
		FROM customer_order co
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Count int             `db:"cnt"`
		Value decimal.Decimal `db:"value"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getSubscribersByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d, COUNT(*) AS cnt
		FROM subscriber
		WHERE created_at IS NOT NULL AND created_at >= :from AND created_at < :to
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int       `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Count: r.Count, Value: decimal.NewFromInt(int64(r.Count))}
	}
	return result, nil
}

func (ms *MYSQLStore) getGrossRevenueByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) AS gross_revenue_base
			FROM (
				SELECT co.id, co.placed,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.placed
			) ob
		)
		SELECT %s AS d,
			COALESCE(SUM(gross_revenue_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForRefund()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getRefundsByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				refunded_amount * (ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) / NULLIF(ob.total_price, 0) AS refunded_base
			FROM (
				SELECT co.id, co.placed, COALESCE(co.refunded_amount, 0) AS refunded_amount,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIdsRefund)
				AND COALESCE(co.refunded_amount, 0) > 0
				GROUP BY co.id, co.placed, co.refunded_amount, co.total_price
			) ob
		)
		SELECT %s AS d,
			COALESCE(SUM(refunded_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIdsRefund": cache.OrderStatusIDsForRefund()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getAvgOrderValueByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := fmt.Sprintf(`
		WITH order_base AS (
			SELECT ob.id, ob.placed,
				(ob.items_base * (100 - ob.discount) / 100 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id, co.placed,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.placed, co.total_price, co.refunded_amount
			) ob
		)
		SELECT %s AS d,
			COALESCE(SUM(revenue_base), 0) / NULLIF(COUNT(*), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		val := r.Value
		if r.Count == 0 {
			val = decimal.Zero
		}
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: val.Round(2), Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getUnitsSoldByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d,
			COALESCE(SUM(oi.quantity), 0) AS value,
			COUNT(DISTINCT co.id) AS cnt
		FROM customer_order co
		JOIN order_item oi ON co.id = oi.order_id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getNewVsReturningCustomersByPeriod(ctx context.Context, from, to time.Time, dateExpr string) (newCustomers, returningCustomers []entity.TimeSeriesPoint, err error) {
	query := fmt.Sprintf(`
		WITH ranked AS (
			SELECT co.placed,
				%s AS bucket,
				ROW_NUMBER() OVER (PARTITION BY b.email ORDER BY co.placed) AS rn
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			WHERE co.order_status_id IN (:statusIds)
		)
		SELECT bucket AS d,
			SUM(CASE WHEN rn = 1 THEN 1 ELSE 0 END) AS new_cnt,
			SUM(CASE WHEN rn > 1 THEN 1 ELSE 0 END) AS ret_cnt
		FROM ranked
		WHERE placed >= :from AND placed < :to
		GROUP BY bucket
		ORDER BY d
	`, dateExpr)
	rows, err := QueryListNamed[struct {
		D      time.Time `db:"d"`
		NewCnt int       `db:"new_cnt"`
		RetCnt int       `db:"ret_cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, nil, err
	}
	newCustomers = make([]entity.TimeSeriesPoint, len(rows))
	returningCustomers = make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		newCustomers[i] = entity.TimeSeriesPoint{Date: r.D, Value: decimal.NewFromInt(int64(r.NewCnt)), Count: r.NewCnt}
		returningCustomers[i] = entity.TimeSeriesPoint{Date: r.D, Value: decimal.NewFromInt(int64(r.RetCnt)), Count: r.RetCnt}
	}
	return newCustomers, returningCustomers, nil
}

func (ms *MYSQLStore) getShippedByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d, COUNT(*) AS cnt
		FROM (
			SELECT osh.order_id, MIN(osh.changed_at) AS shipped_at
			FROM order_status_history osh
			WHERE osh.order_status_id = :shippedStatusId
			GROUP BY osh.order_id
		) AS first_shipped
		WHERE shipped_at >= :from AND shipped_at < :to
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int       `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "shippedStatusId": cache.OrderStatusShipped.Status.Id})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: decimal.NewFromInt(int64(r.Count)), Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getDeliveredByPeriod(ctx context.Context, from, to time.Time, dateExpr string) ([]entity.TimeSeriesPoint, error) {
	query := fmt.Sprintf(`
		SELECT %s AS d, COUNT(*) AS cnt
		FROM (
			SELECT osh.order_id, MIN(osh.changed_at) AS delivered_at
			FROM order_status_history osh
			WHERE osh.order_status_id = :deliveredStatusId
			GROUP BY osh.order_id
		) AS first_delivered
		WHERE delivered_at >= :from AND delivered_at < :to
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int       `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "deliveredStatusId": cache.OrderStatusDelivered.Status.Id})
	if err != nil {
		return nil, err
	}
	result := make([]entity.TimeSeriesPoint, len(rows))
	for i, r := range rows {
		result[i] = entity.TimeSeriesPoint{Date: r.D, Value: decimal.NewFromInt(int64(r.Count)), Count: r.Count}
	}
	return result, nil
}

// fillTimeSeriesGaps ensures continuous date range for charts; fills missing buckets with zeros.
func fillTimeSeriesGaps(points []entity.TimeSeriesPoint, from, to time.Time, granularity entity.MetricsGranularity) []entity.TimeSeriesPoint {
	pointMap := make(map[string]entity.TimeSeriesPoint)
	for _, p := range points {
		key := p.Date.Format("2006-01-02")
		pointMap[key] = p
	}
	var result []entity.TimeSeriesPoint
	cur := bucketStart(from, granularity)
	end := bucketStart(to, granularity)
	for !cur.After(end) {
		key := cur.Format("2006-01-02")
		if p, ok := pointMap[key]; ok {
			result = append(result, p)
		} else {
			result = append(result, entity.TimeSeriesPoint{Date: cur, Value: decimal.Zero, Count: 0})
		}
		cur = bucketNext(cur, granularity)
	}
	return result
}

// bucketStart returns the start of the bucket containing t. Uses UTC to align with MySQL
// CONVERT_TZ(..., '+00:00') in granularitySQL; avoids timezone mismatch between Go and MySQL.
func bucketStart(t time.Time, g entity.MetricsGranularity) time.Time {
	t = t.UTC()
	loc := time.UTC
	switch g {
	case entity.MetricsGranularityWeek:
		// Monday 00:00 (align with MySQL WEEKDAY: 0=Mon, 6=Sun; Go: 0=Sun, 1=Mon)
		weekday := int(t.Weekday())
		daysBack := (weekday + 6) % 7
		return time.Date(t.Year(), t.Month(), t.Day()-daysBack, 0, 0, 0, 0, loc)
	case entity.MetricsGranularityMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	default:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
}

func bucketNext(t time.Time, g entity.MetricsGranularity) time.Time {
	switch g {
	case entity.MetricsGranularityWeek:
		return t.AddDate(0, 0, 7)
	case entity.MetricsGranularityMonth:
		return t.AddDate(0, 1, 0)
	default:
		return t.AddDate(0, 0, 1)
	}
}

func changePct(current, previous decimal.Decimal) *float64 {
	if previous.IsZero() {
		return nil
	}
	diff := current.Sub(previous).Div(previous).Mul(decimal.NewFromInt(100))
	f, ok := diff.Float64()
	if !ok {
		return nil
	}
	return &f
}

func changePctInt(current, previous int) *float64 {
	if previous == 0 {
		return nil
	}
	f := (float64(current-previous) / float64(previous)) * 100
	return &f
}

func ptr(d decimal.Decimal) *decimal.Decimal {
	return &d
}
