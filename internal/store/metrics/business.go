package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"
)

func (s *Store) GetBusinessMetrics(ctx context.Context, period, comparePeriod entity.TimeRange, granularity entity.MetricsGranularity) (*entity.BusinessMetrics, error) {
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
		promoOrders, cPromoOrders     int
		newSubs, cNewSubs              int
		repeatRate, avgOrders, avgDays decimal.Decimal
		emailSummary, cEmailSummary    *entity.EmailMetricsSummary
	)

	g, gctx := errgroup.WithContext(ctx)

	var ga4Daily, ga4DailyCompare []ga4.DailyMetrics

	// --- HOT: Core sales + time series (dashboard charts, refreshed every few seconds) ---
	// Core sales (period)
	g.Go(func() error {
		var err error
		rev, orders, aov, err = s.getCoreSalesMetrics(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		itemsPerOrder, err = s.getItemsPerOrder(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		revRefund, _, err = s.getRefundMetrics(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		totalDiscount, err = s.getTotalDiscount(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		promoOrders, err = s.getPromoUsageCount(gctx, period.From, period.To)
		return err
	})

	// Core sales (compare)
	if hasCompare {
		g.Go(func() error {
			var err error
			cRev, cOrders, cAov, err = s.getCoreSalesMetrics(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
		g.Go(func() error {
			var err error
			cItemsPerOrder, err = s.getItemsPerOrder(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
		g.Go(func() error {
			var err error
			cRevRefund, _, err = s.getRefundMetrics(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
		g.Go(func() error {
			var err error
			cTotalDiscount, err = s.getTotalDiscount(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
		g.Go(func() error {
			var err error
			cPromoOrders, err = s.getPromoUsageCount(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
	}

	// --- COLD: Breakdowns (geography, products, customers; can be lazy-loaded or cached longer) ---
	// Geography
	g.Go(func() error {
		var err error
		m.RevenueByCountry, err = s.getRevenueByGeography(gctx, period.From, period.To, "country", nil, nil)
		return err
	})
	g.Go(func() error {
		var err error
		m.RevenueByCity, err = s.getRevenueByGeography(gctx, period.From, period.To, "city", nil, nil)
		return err
	})
	g.Go(func() error {
		var err error
		m.AvgOrderByCountry, err = s.getAvgOrderByGeography(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		m.SessionsByCountry, err = s.repo.GA4Data().GetGA4SessionsByCountry(gctx, period.From, period.To, 50)
		return err
	})

	// Currency + payment
	g.Go(func() error {
		var err error
		m.RevenueByCurrency, err = s.getRevenueByCurrency(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		m.RevenueByPaymentMethod, err = s.getRevenueByPaymentMethod(gctx, period.From, period.To)
		return err
	})

	// Products
	g.Go(func() error {
		var err error
		m.TopProductsByRevenue, err = s.getTopProductsByRevenue(gctx, period.From, period.To, 20)
		return err
	})
	g.Go(func() error {
		var err error
		m.TopProductsByQuantity, err = s.getTopProductsByQuantity(gctx, period.From, period.To, 20)
		return err
	})
	g.Go(func() error {
		var err error
		m.TopProductsByViews, err = s.repo.GA4Data().GetGA4ProductPageMetrics(gctx, period.From, period.To, 20)
		return err
	})
	g.Go(func() error {
		var err error
		m.RevenueByCategory, err = s.getRevenueByCategory(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		m.CrossSellPairs, err = s.getCrossSellPairs(gctx, period.From, period.To, 15)
		return err
	})

	// Traffic Sources (from BQ campaign attribution — single source of truth)
	g.Go(func() error {
		rows, err := s.repo.BQCache().GetBQCampaignAttribution(gctx, period.From, period.To, 10000, 0)
		if err != nil {
			return err
		}
		seen := make(map[string]int)
		for _, r := range rows {
			key := r.UTMSource + "|" + r.UTMMedium
			if idx, ok := seen[key]; ok {
				m.TrafficBySource[idx].Sessions += int(r.Sessions)
				m.TrafficBySource[idx].Users += int(r.Users)
				m.TrafficBySource[idx].Revenue = m.TrafficBySource[idx].Revenue.Add(r.Revenue)
				continue
			}
			seen[key] = len(m.TrafficBySource)
			m.TrafficBySource = append(m.TrafficBySource, entity.TrafficSourceMetric{
				Source:   r.UTMSource,
				Medium:   r.UTMMedium,
				Sessions: int(r.Sessions),
				Users:    int(r.Users),
				Revenue:  r.Revenue,
			})
		}
		sort.Slice(m.TrafficBySource, func(i, j int) bool {
			return m.TrafficBySource[i].Sessions > m.TrafficBySource[j].Sessions
		})
		if len(m.TrafficBySource) > 20 {
			m.TrafficBySource = m.TrafficBySource[:20]
		}
		return nil
	})
	// Device metrics (from BQ device funnel — single source of truth)
	g.Go(func() error {
		rows, err := s.repo.BQCache().GetBQDeviceFunnel(gctx, period.From, period.To)
		if err != nil {
			return err
		}
		agg := make(map[string]*entity.DeviceMetric)
		for _, r := range rows {
			if dm, ok := agg[r.DeviceCategory]; ok {
				dm.Sessions += int(r.Sessions)
				dm.Users += int(r.PurchaseUsers)
			} else {
				agg[r.DeviceCategory] = &entity.DeviceMetric{
					DeviceCategory: r.DeviceCategory,
					Sessions:       int(r.Sessions),
					Users:          int(r.PurchaseUsers),
				}
			}
		}
		for _, dm := range agg {
			if dm.Sessions > 0 {
				dm.ConversionRate = float64(dm.Users) / float64(dm.Sessions)
			}
			m.TrafficByDevice = append(m.TrafficByDevice, *dm)
		}
		sort.Slice(m.TrafficByDevice, func(i, j int) bool {
			return m.TrafficByDevice[i].Sessions > m.TrafficByDevice[j].Sessions
		})
		return nil
	})

	// Customers
	g.Go(func() error {
		var err error
		newSubs, err = s.repo.Subscribers().GetNewSubscribersCount(gctx, period.From, period.To)
		return err
	})
	if hasCompare {
		g.Go(func() error {
			var err error
			cNewSubs, err = s.repo.Subscribers().GetNewSubscribersCount(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
	}
	g.Go(func() error {
		var err error
		repeatRate, avgOrders, avgDays, err = s.getRepeatCustomerMetrics(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		emailSummary, err = s.GetEmailMetricsSummary(gctx, period.From, period.To)
		return err
	})
	if hasCompare {
		g.Go(func() error {
			var err error
			cEmailSummary, err = s.GetEmailMetricsSummary(gctx, comparePeriod.From, comparePeriod.To)
			return err
		})
	}
	g.Go(func() error {
		var err error
		m.CLVDistribution, err = s.getCLVStats(gctx, period.From, period.To)
		return err
	})

	// Promo + order status
	g.Go(func() error {
		var err error
		m.RevenueByPromo, err = s.getRevenueByPromo(gctx, period.From, period.To)
		return err
	})
	g.Go(func() error {
		var err error
		m.OrdersByStatus, err = s.getOrdersByStatus(gctx, period.From, period.To)
		return err
	})

	// --- HOT: Time series (period) ---
	g.Go(func() error {
		var err error
		m.RevenueByDay, err = s.getRevenueByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.OrdersByDay, err = s.getOrdersByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.SubscribersByDay, err = s.getSubscribersByPeriod(gctx, period.From, period.To, subDateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.GrossRevenueByDay, err = s.getGrossRevenueByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.RefundsByDay, err = s.getRefundsByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.AvgOrderValueByDay, err = s.getAvgOrderValueByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.UnitsSoldByDay, err = s.getUnitsSoldByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.NewCustomersByDay, m.ReturningCustomersByDay, err = s.getNewVsReturningCustomersByPeriod(gctx, period.From, period.To, dateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.ShippedByDay, err = s.getShippedByPeriod(gctx, period.From, period.To, shippedDateExpr)
		return err
	})
	g.Go(func() error {
		var err error
		m.DeliveredByDay, err = s.getDeliveredByPeriod(gctx, period.From, period.To, deliveredDateExpr)
		return err
	})

	// --- GA4: Time series (period) ---
	g.Go(func() error {
		var err error
		ga4Daily, err = s.repo.GA4Data().GetGA4DailyMetrics(gctx, period.From, period.To)
		if err != nil {
			return err
		}
		m.SessionsByDay = make([]entity.TimeSeriesPoint, len(ga4Daily))
		m.UsersByDay = make([]entity.TimeSeriesPoint, len(ga4Daily))
		m.PageViewsByDay = make([]entity.TimeSeriesPoint, len(ga4Daily))
		for i, ga := range ga4Daily {
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
			m.RevenueByDayCompare, err = s.getRevenueByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.OrdersByDayCompare, err = s.getOrdersByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.SubscribersByDayCompare, err = s.getSubscribersByPeriod(gctx, comparePeriod.From, comparePeriod.To, subDateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.GrossRevenueByDayCompare, err = s.getGrossRevenueByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.RefundsByDayCompare, err = s.getRefundsByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.AvgOrderValueByDayCompare, err = s.getAvgOrderValueByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.UnitsSoldByDayCompare, err = s.getUnitsSoldByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.NewCustomersByDayCompare, m.ReturningCustomersByDayCompare, err = s.getNewVsReturningCustomersByPeriod(gctx, comparePeriod.From, comparePeriod.To, dateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.ShippedByDayCompare, err = s.getShippedByPeriod(gctx, comparePeriod.From, comparePeriod.To, shippedDateExpr)
			return err
		})
		g.Go(func() error {
			var err error
			m.DeliveredByDayCompare, err = s.getDeliveredByPeriod(gctx, comparePeriod.From, comparePeriod.To, deliveredDateExpr)
			return err
		})

		// --- GA4: Time series (compare) ---
		g.Go(func() error {
			var err error
			ga4DailyCompare, err = s.repo.GA4Data().GetGA4DailyMetrics(gctx, comparePeriod.From, comparePeriod.To)
			if err != nil {
				return err
			}
			m.SessionsByDayCompare = make([]entity.TimeSeriesPoint, len(ga4DailyCompare))
			m.UsersByDayCompare = make([]entity.TimeSeriesPoint, len(ga4DailyCompare))
			m.PageViewsByDayCompare = make([]entity.TimeSeriesPoint, len(ga4DailyCompare))
			for i, ga := range ga4DailyCompare {
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

	// Data freshness: fetch sync statuses in parallel with everything else
	var syncStatuses []entity.SyncSourceStatus
	g.Go(func() error {
		var err error
		syncStatuses, err = s.repo.SyncStatus().GetAllSyncStatuses(gctx)
		return err
	})

	if err := g.Wait(); err != nil {
		slog.Default().ErrorContext(ctx, "GetBusinessMetrics: metrics query failed", slog.String("err", err.Error()))
		return nil, err
	}

	m.Freshness = buildDataFreshness(syncStatuses, 3*time.Hour, 12*time.Hour)

	// GA4 aggregate metrics for period (sum from errgroup fetch above)
	var totalSessions, totalUsers, totalNewUsers, totalPageViews int
	var totalBounceRate, totalAvgSessionDuration, totalPagesPerSession float64
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

		// GA4 compare metrics (sum from errgroup fetch above)
		var cTotalSessions, cTotalUsers, cTotalNewUsers, cTotalPageViews int
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

	// Email delivery metrics
	if emailSummary != nil {
		m.EmailsSent.Value = decimal.NewFromInt(int64(emailSummary.TotalSent))
		m.EmailsDelivered.Value = decimal.NewFromInt(int64(emailSummary.TotalDelivered))
		m.EmailDeliveryRate.Value = decimal.NewFromFloat(emailSummary.DeliveryRate)
		m.EmailOpenRate.Value = decimal.NewFromFloat(emailSummary.OpenRate)
		m.EmailClickRate.Value = decimal.NewFromFloat(emailSummary.ClickRate)
		m.EmailBounceRate.Value = decimal.NewFromFloat(emailSummary.BounceRate)
	}
	if hasCompare && cEmailSummary != nil {
		cEmailsSent := decimal.NewFromInt(int64(cEmailSummary.TotalSent))
		cEmailsDelivered := decimal.NewFromInt(int64(cEmailSummary.TotalDelivered))
		cDeliveryRate := decimal.NewFromFloat(cEmailSummary.DeliveryRate)
		cOpenRate := decimal.NewFromFloat(cEmailSummary.OpenRate)
		cClickRate := decimal.NewFromFloat(cEmailSummary.ClickRate)
		cBounceRate := decimal.NewFromFloat(cEmailSummary.BounceRate)
		m.EmailsSent.CompareValue = &cEmailsSent
		m.EmailsSent.ChangePct = changePct(m.EmailsSent.Value, cEmailsSent)
		m.EmailsDelivered.CompareValue = &cEmailsDelivered
		m.EmailsDelivered.ChangePct = changePct(m.EmailsDelivered.Value, cEmailsDelivered)
		m.EmailDeliveryRate.CompareValue = &cDeliveryRate
		m.EmailDeliveryRate.ChangePct = changePct(m.EmailDeliveryRate.Value, cDeliveryRate)
		m.EmailOpenRate.CompareValue = &cOpenRate
		m.EmailOpenRate.ChangePct = changePct(m.EmailOpenRate.Value, cOpenRate)
		m.EmailClickRate.CompareValue = &cClickRate
		m.EmailClickRate.ChangePct = changePct(m.EmailClickRate.Value, cClickRate)
		m.EmailBounceRate.CompareValue = &cBounceRate
		m.EmailBounceRate.ChangePct = changePct(m.EmailBounceRate.Value, cBounceRate)
	}

	// Region depends on country (run after parallel wait)
	var err error
	m.RevenueByRegion, err = s.getRevenueByRegion(m.RevenueByCountry)
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
