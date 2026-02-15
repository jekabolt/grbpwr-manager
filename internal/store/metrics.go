package store

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

type metricsStore struct {
	*MYSQLStore
}

func (ms *MYSQLStore) Metrics() dependency.Metrics {
	return &metricsStore{MYSQLStore: ms}
}

func (ms *MYSQLStore) GetBusinessMetrics(ctx context.Context, period, comparePeriod entity.TimeRange, granularity entity.MetricsGranularity) (*entity.BusinessMetrics, error) {
	if granularity == 0 {
		granularity = entity.MetricsGranularityDay
	}
	dateExpr, subDateExpr := granularitySQL(granularity)
	m := &entity.BusinessMetrics{
		Period: period,
	}
	if !comparePeriod.From.IsZero() || !comparePeriod.To.IsZero() {
		m.ComparePeriod = &comparePeriod
	}

	// Core sales metrics
	rev, orders, aov, err := ms.getCoreSalesMetrics(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("core sales: %w", err)
	}
	m.Revenue.Value = rev
	m.OrdersCount.Value = decimal.NewFromInt(int64(orders))
	m.AvgOrderValue.Value = aov

	itemsPerOrder, err := ms.getItemsPerOrder(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("items per order: %w", err)
	}
	m.ItemsPerOrder.Value = itemsPerOrder

	revRefund, _, err := ms.getRefundMetrics(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("refund: %w", err)
	}
	grossRev := rev.Add(revRefund)
	if grossRev.GreaterThan(decimal.Zero) {
		m.RefundRate.Value = revRefund.Div(grossRev).Mul(decimal.NewFromInt(100))
	}
	m.GrossRevenue.Value = grossRev
	m.TotalRefunded.Value = revRefund

	totalDiscount, err := ms.getTotalDiscount(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("total discount: %w", err)
	}
	m.TotalDiscount.Value = totalDiscount

	promoOrders, err := ms.getPromoUsageCount(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("promo usage: %w", err)
	}
	if orders > 0 {
		m.PromoUsageRate.Value = decimal.NewFromInt(int64(promoOrders)).Div(decimal.NewFromInt(int64(orders))).Mul(decimal.NewFromInt(100))
	}

	// Compare period
	if !comparePeriod.From.IsZero() && !comparePeriod.To.IsZero() {
		cRev, cOrders, cAov, err := ms.getCoreSalesMetrics(ctx, comparePeriod.From, comparePeriod.To)
		if err == nil {
			m.Revenue.CompareValue = &cRev
			m.OrdersCount.CompareValue = ptr(decimal.NewFromInt(int64(cOrders)))
			m.AvgOrderValue.CompareValue = &cAov
			m.Revenue.ChangePct = changePct(rev, cRev)
			m.OrdersCount.ChangePct = changePctInt(orders, cOrders)
			m.AvgOrderValue.ChangePct = changePct(aov, cAov)
		}
		cItemsPerOrder, _ := ms.getItemsPerOrder(ctx, comparePeriod.From, comparePeriod.To)
		m.ItemsPerOrder.CompareValue = &cItemsPerOrder
		m.ItemsPerOrder.ChangePct = changePct(itemsPerOrder, cItemsPerOrder)
		cRevRefund, _, _ := ms.getRefundMetrics(ctx, comparePeriod.From, comparePeriod.To)
		cRevTotal, _, _, _ := ms.getCoreSalesMetrics(ctx, comparePeriod.From, comparePeriod.To)
		cGross := cRevTotal.Add(cRevRefund)
		if cGross.GreaterThan(decimal.Zero) {
			cRefundRate := cRevRefund.Div(cGross).Mul(decimal.NewFromInt(100))
			m.RefundRate.CompareValue = &cRefundRate
			m.RefundRate.ChangePct = changePct(m.RefundRate.Value, cRefundRate)
		}
		cPromoOrders, _ := ms.getPromoUsageCount(ctx, comparePeriod.From, comparePeriod.To)
		if cOrders > 0 {
			cPromoRate := decimal.NewFromInt(int64(cPromoOrders)).Div(decimal.NewFromInt(int64(cOrders))).Mul(decimal.NewFromInt(100))
			m.PromoUsageRate.CompareValue = &cPromoRate
			m.PromoUsageRate.ChangePct = changePct(m.PromoUsageRate.Value, cPromoRate)
		}
		cGrossRev := cRevTotal.Add(cRevRefund)
		m.GrossRevenue.CompareValue = &cGrossRev
		m.TotalRefunded.CompareValue = &cRevRefund
		m.GrossRevenue.ChangePct = changePct(grossRev, cGrossRev)
		m.TotalRefunded.ChangePct = changePct(revRefund, cRevRefund)
		cTotalDiscount, _ := ms.getTotalDiscount(ctx, comparePeriod.From, comparePeriod.To)
		m.TotalDiscount.CompareValue = &cTotalDiscount
		m.TotalDiscount.ChangePct = changePct(totalDiscount, cTotalDiscount)
	}

	// Geography
	m.RevenueByCountry, err = ms.getRevenueByGeography(ctx, period.From, period.To, "country", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("revenue by country: %w", err)
	}
	m.RevenueByCity, err = ms.getRevenueByGeography(ctx, period.From, period.To, "city", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("revenue by city: %w", err)
	}
	m.AvgOrderByCountry, err = ms.getAvgOrderByGeography(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("avg order by country: %w", err)
	}
	m.RevenueByRegion, err = ms.getRevenueByRegion(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("revenue by region: %w", err)
	}

	// Currency
	m.RevenueByCurrency, err = ms.getRevenueByCurrency(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("revenue by currency: %w", err)
	}

	// Payment method
	m.RevenueByPaymentMethod, err = ms.getRevenueByPaymentMethod(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("revenue by payment method: %w", err)
	}

	// Products
	m.TopProductsByRevenue, err = ms.getTopProductsByRevenue(ctx, period.From, period.To, 20)
	if err != nil {
		return nil, fmt.Errorf("top products revenue: %w", err)
	}
	m.TopProductsByQuantity, err = ms.getTopProductsByQuantity(ctx, period.From, period.To, 20)
	if err != nil {
		return nil, fmt.Errorf("top products quantity: %w", err)
	}
	m.RevenueByCategory, err = ms.getRevenueByCategory(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("revenue by category: %w", err)
	}
	m.CrossSellPairs, err = ms.getCrossSellPairs(ctx, period.From, period.To, 15)
	if err != nil {
		return nil, fmt.Errorf("cross sell: %w", err)
	}

	// Customers
	newSubs, err := ms.GetNewSubscribersCount(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("new subscribers: %w", err)
	}
	m.NewSubscribers.Value = decimal.NewFromInt(int64(newSubs))
	if !comparePeriod.From.IsZero() && !comparePeriod.To.IsZero() {
		cNewSubs, _ := ms.GetNewSubscribersCount(ctx, comparePeriod.From, comparePeriod.To)
		m.NewSubscribers.CompareValue = ptr(decimal.NewFromInt(int64(cNewSubs)))
		m.NewSubscribers.ChangePct = changePctInt(newSubs, cNewSubs)
	}

	repeatRate, avgOrders, avgDays, err := ms.getRepeatCustomerMetrics(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("repeat customers: %w", err)
	}
	m.RepeatCustomersRate.Value = repeatRate
	m.AvgOrdersPerCustomer.Value = avgOrders
	m.AvgDaysBetweenOrders.Value = avgDays

	m.CLVDistribution, err = ms.getCLVStats(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("clv: %w", err)
	}

	// Promo
	m.RevenueByPromo, err = ms.getRevenueByPromo(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("revenue by promo: %w", err)
	}

	// Order status funnel
	m.OrdersByStatus, err = ms.getOrdersByStatus(ctx, period.From, period.To)
	if err != nil {
		return nil, fmt.Errorf("orders by status: %w", err)
	}

	// Time series (with gap-filling for continuous charts)
	m.RevenueByDay, err = ms.getRevenueByPeriod(ctx, period.From, period.To, dateExpr)
	if err != nil {
		return nil, fmt.Errorf("revenue by period: %w", err)
	}
	m.RevenueByDay = fillTimeSeriesGaps(m.RevenueByDay, period.From, period.To, granularity)

	m.OrdersByDay, err = ms.getOrdersByPeriod(ctx, period.From, period.To, dateExpr)
	if err != nil {
		return nil, fmt.Errorf("orders by period: %w", err)
	}
	m.OrdersByDay = fillTimeSeriesGaps(m.OrdersByDay, period.From, period.To, granularity)

	m.SubscribersByDay, err = ms.getSubscribersByPeriod(ctx, period.From, period.To, subDateExpr)
	if err != nil {
		return nil, fmt.Errorf("subscribers by period: %w", err)
	}
	m.SubscribersByDay = fillTimeSeriesGaps(m.SubscribersByDay, period.From, period.To, granularity)

	m.GrossRevenueByDay, err = ms.getGrossRevenueByPeriod(ctx, period.From, period.To, dateExpr)
	if err != nil {
		return nil, fmt.Errorf("gross revenue by period: %w", err)
	}
	m.GrossRevenueByDay = fillTimeSeriesGaps(m.GrossRevenueByDay, period.From, period.To, granularity)

	m.RefundsByDay, err = ms.getRefundsByPeriod(ctx, period.From, period.To, dateExpr)
	if err != nil {
		return nil, fmt.Errorf("refunds by period: %w", err)
	}
	m.RefundsByDay = fillTimeSeriesGaps(m.RefundsByDay, period.From, period.To, granularity)

	m.AvgOrderValueByDay, err = ms.getAvgOrderValueByPeriod(ctx, period.From, period.To, dateExpr)
	if err != nil {
		return nil, fmt.Errorf("avg order value by period: %w", err)
	}
	m.AvgOrderValueByDay = fillTimeSeriesGaps(m.AvgOrderValueByDay, period.From, period.To, granularity)

	m.UnitsSoldByDay, err = ms.getUnitsSoldByPeriod(ctx, period.From, period.To, dateExpr)
	if err != nil {
		return nil, fmt.Errorf("units sold by period: %w", err)
	}
	m.UnitsSoldByDay = fillTimeSeriesGaps(m.UnitsSoldByDay, period.From, period.To, granularity)

	m.NewCustomersByDay, m.ReturningCustomersByDay, err = ms.getNewVsReturningCustomersByPeriod(ctx, period.From, period.To, dateExpr)
	if err != nil {
		return nil, fmt.Errorf("new vs returning customers: %w", err)
	}
	m.NewCustomersByDay = fillTimeSeriesGaps(m.NewCustomersByDay, period.From, period.To, granularity)
	m.ReturningCustomersByDay = fillTimeSeriesGaps(m.ReturningCustomersByDay, period.From, period.To, granularity)

	shippedDateExpr := granularityDateExpr(granularity, "first_shipped.shipped_at")
	deliveredDateExpr := granularityDateExpr(granularity, "first_delivered.delivered_at")
	m.ShippedByDay, err = ms.getShippedByPeriod(ctx, period.From, period.To, shippedDateExpr)
	if err != nil {
		return nil, fmt.Errorf("shipped by period: %w", err)
	}
	m.ShippedByDay = fillTimeSeriesGaps(m.ShippedByDay, period.From, period.To, granularity)

	m.DeliveredByDay, err = ms.getDeliveredByPeriod(ctx, period.From, period.To, deliveredDateExpr)
	if err != nil {
		return nil, fmt.Errorf("delivered by period: %w", err)
	}
	m.DeliveredByDay = fillTimeSeriesGaps(m.DeliveredByDay, period.From, period.To, granularity)

	// Comparison period time series
	if !comparePeriod.From.IsZero() && !comparePeriod.To.IsZero() {
		m.RevenueByDayCompare, _ = ms.getRevenueByPeriod(ctx, comparePeriod.From, comparePeriod.To, dateExpr)
		m.RevenueByDayCompare = fillTimeSeriesGaps(m.RevenueByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.OrdersByDayCompare, _ = ms.getOrdersByPeriod(ctx, comparePeriod.From, comparePeriod.To, dateExpr)
		m.OrdersByDayCompare = fillTimeSeriesGaps(m.OrdersByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.SubscribersByDayCompare, _ = ms.getSubscribersByPeriod(ctx, comparePeriod.From, comparePeriod.To, subDateExpr)
		m.SubscribersByDayCompare = fillTimeSeriesGaps(m.SubscribersByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.GrossRevenueByDayCompare, _ = ms.getGrossRevenueByPeriod(ctx, comparePeriod.From, comparePeriod.To, dateExpr)
		m.GrossRevenueByDayCompare = fillTimeSeriesGaps(m.GrossRevenueByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.RefundsByDayCompare, _ = ms.getRefundsByPeriod(ctx, comparePeriod.From, comparePeriod.To, dateExpr)
		m.RefundsByDayCompare = fillTimeSeriesGaps(m.RefundsByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.AvgOrderValueByDayCompare, _ = ms.getAvgOrderValueByPeriod(ctx, comparePeriod.From, comparePeriod.To, dateExpr)
		m.AvgOrderValueByDayCompare = fillTimeSeriesGaps(m.AvgOrderValueByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.UnitsSoldByDayCompare, _ = ms.getUnitsSoldByPeriod(ctx, comparePeriod.From, comparePeriod.To, dateExpr)
		m.UnitsSoldByDayCompare = fillTimeSeriesGaps(m.UnitsSoldByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.NewCustomersByDayCompare, m.ReturningCustomersByDayCompare, _ = ms.getNewVsReturningCustomersByPeriod(ctx, comparePeriod.From, comparePeriod.To, dateExpr)
		m.NewCustomersByDayCompare = fillTimeSeriesGaps(m.NewCustomersByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.ReturningCustomersByDayCompare = fillTimeSeriesGaps(m.ReturningCustomersByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.ShippedByDayCompare, _ = ms.getShippedByPeriod(ctx, comparePeriod.From, comparePeriod.To, shippedDateExpr)
		m.ShippedByDayCompare = fillTimeSeriesGaps(m.ShippedByDayCompare, comparePeriod.From, comparePeriod.To, granularity)
		m.DeliveredByDayCompare, _ = ms.getDeliveredByPeriod(ctx, comparePeriod.From, comparePeriod.To, deliveredDateExpr)
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
			AND co.order_status_id IN (3, 4, 5, 10)
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
	r, err := QueryNamedOne[row](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
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
			AND co.order_status_id IN (3, 4, 5, 10)
			GROUP BY co.id
		) AS order_items
	`
	r, err := QueryNamedOne[row](ctx, ms.DB(), query, map[string]any{"from": from, "to": to})
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
			AND co.order_status_id IN (7, 10) AND (co.refunded_amount IS NOT NULL AND co.refunded_amount > 0)
			GROUP BY co.id, co.total_price, co.refunded_amount
		)
		SELECT
			COALESCE(SUM(
				refunded_amount * (items_base * (100 - discount) / 100 + CASE WHEN free_shipping THEN 0 ELSE shipment_base END) / NULLIF(total_price, 0)
			), 0) AS amount,
			COUNT(*) AS cnt
		FROM order_base
	`
	r, err := QueryNamedOne[row](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
	if err != nil {
		return decimal.Zero, 0, err
	}
	return r.Amount, r.Count, nil
}

func (ms *MYSQLStore) getTotalDiscount(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	params := map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency}
	productDiscount, err := QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, ms.DB(), `
		SELECT COALESCE(SUM(pp_base.price * COALESCE(oi.product_sale_percentage, 0) / 100 * oi.quantity), 0) AS v
		FROM customer_order co
		JOIN order_item oi ON co.id = oi.order_id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (3, 4, 5, 10)
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
			AND co.order_status_id IN (3, 4, 5, 10) AND co.promo_id IS NOT NULL
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
				AND co.order_status_id IN (3, 4, 5, 10)
				GROUP BY co.id, co.total_price, co.refunded_amount
			) ob
		)
		SELECT pm.name AS payment_method,
			COALESCE(SUM(ob.revenue_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base ob
		JOIN payment p ON p.order_id = ob.id
		JOIN payment_method pm ON p.payment_method_id = pm.id
		GROUP BY pm.id, pm.name
		ORDER BY value DESC
	`
	rows, err := QueryListNamed[struct {
		PaymentMethod string          `db:"payment_method"`
		Value         decimal.Decimal `db:"value"`
		Count         int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
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
		AND order_status_id IN (3, 4, 5, 10) AND promo_id IS NOT NULL
	`
	r, err := QueryNamedOne[row](ctx, ms.DB(), query, map[string]any{"from": from, "to": to})
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
				AND co.order_status_id IN (3, 4, 5, 10)
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

	params := map[string]any{"from": from, "to": to, "country": country, "city": city, "baseCurrency": baseCurrency}
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
				AND co.order_status_id IN (3, 4, 5, 10)
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
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
	if err != nil {
		return nil, err
	}
	result := make([]entity.GeographyMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.GeographyMetric{Country: r.Country, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getRevenueByRegion(ctx context.Context, from, to time.Time) ([]entity.RegionMetric, error) {
	byCountry, err := ms.getRevenueByGeography(ctx, from, to, "country", nil, nil)
	if err != nil {
		return nil, err
	}
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
	query := `
		SELECT co.currency,
			COALESCE(SUM(co.total_price - COALESCE(co.refunded_amount, 0)), 0) AS value,
			COUNT(*) AS cnt
		FROM customer_order co
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (3, 4, 5, 10)
		GROUP BY co.currency
		ORDER BY value DESC
	`
	rows, err := QueryListNamed[struct {
		Currency string          `db:"currency"`
		Value    decimal.Decimal `db:"value"`
		Count    int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to})
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
			COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS value,
			SUM(oi.quantity) AS cnt
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		JOIN customer_order co ON oi.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (3, 4, 5, 10)
		GROUP BY oi.product_id, p.brand
		ORDER BY value DESC
		LIMIT :limit
	`
	rows, err := QueryListNamed[struct {
		ProductId int             `db:"product_id"`
		Brand     string          `db:"brand"`
		Value     decimal.Decimal `db:"value"`
		Count     int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "limit": limit, "baseCurrency": baseCurrency})
	if err != nil {
		return nil, err
	}
	result := make([]entity.ProductMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.ProductMetric{ProductId: r.ProductId, ProductName: r.Brand, Brand: r.Brand, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (ms *MYSQLStore) getTopProductsByQuantity(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT oi.product_id, p.brand, SUM(oi.quantity) AS cnt,
			COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) * oi.quantity), 0) AS value
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		JOIN customer_order co ON oi.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (3, 4, 5, 10)
		GROUP BY oi.product_id, p.brand
		ORDER BY cnt DESC
		LIMIT :limit
	`
	rows, err := QueryListNamed[struct {
		ProductId int             `db:"product_id"`
		Brand     string          `db:"brand"`
		Count     int             `db:"cnt"`
		Value     decimal.Decimal `db:"value"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "limit": limit, "baseCurrency": baseCurrency})
	if err != nil {
		return nil, err
	}
	result := make([]entity.ProductMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.ProductMetric{ProductId: r.ProductId, ProductName: r.Brand, Brand: r.Brand, Value: r.Value, Count: r.Count}
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
		AND co.order_status_id IN (3, 4, 5, 10)
		GROUP BY p.top_category_id, c.name
		ORDER BY value DESC
		LIMIT 30
	`
	rows, err := QueryListNamed[struct {
		CategoryId   int             `db:"category_id"`
		CategoryName string          `db:"category_name"`
		Value        decimal.Decimal `db:"value"`
		Count        int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
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
			p1.brand AS product_a_name, p2.brand AS product_b_name,
			COUNT(*) AS cnt
		FROM order_item oi1
		JOIN order_item oi2 ON oi1.order_id = oi2.order_id AND oi1.product_id < oi2.product_id
		JOIN product p1 ON oi1.product_id = p1.id
		JOIN product p2 ON oi2.product_id = p2.id
		JOIN customer_order co ON oi1.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (3, 4, 5, 10)
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
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "limit": limit})
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
		AND co.order_status_id IN (3, 4, 5, 10)
		GROUP BY b.email
	`
	rows, err := QueryListNamed[emailOrders](ctx, ms.DB(), query, map[string]any{"from": from, "to": to})
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

	// Avg days between orders for repeat buyers
	type orderDate struct {
		Email string    `db:"email"`
		Placed time.Time `db:"placed"`
	}
	q2 := `
		SELECT b.email, co.placed
		FROM customer_order co
		JOIN buyer b ON co.id = b.order_id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (3, 4, 5, 10)
		ORDER BY b.email, co.placed
	`
	orderRows, err := QueryListNamed[orderDate](ctx, ms.DB(), q2, map[string]any{"from": from, "to": to})
	if err != nil {
		return repeatRate, avgOrders, decimal.Zero, nil
	}

	// Group by email, compute gaps
	emailToDates := make(map[string][]time.Time)
	for _, r := range orderRows {
		emailToDates[r.Email] = append(emailToDates[r.Email], r.Placed)
	}
	var totalDays int
	var gapCount int
	for _, dates := range emailToDates {
		sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
		for i := 1; i < len(dates); i++ {
			d := int(dates[i].Sub(dates[i-1]).Hours() / 24)
			totalDays += d
			gapCount++
		}
	}
	if gapCount > 0 {
		avgDays = decimal.NewFromInt(int64(totalDays)).Div(decimal.NewFromInt(int64(gapCount)))
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
				AND co.order_status_id IN (3, 4, 5, 10)
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
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
	if err != nil {
		return entity.CLVStats{}, err
	}

	if len(rows) == 0 {
		return entity.CLVStats{}, nil
	}

	clvs := make([]float64, len(rows))
	for i, r := range rows {
		f, _ := r.CLV.Float64()
		clvs[i] = f
	}
	sort.Float64s(clvs)

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

	p90Idx := int(math.Ceil(float64(len(clvs)) * 0.9)) - 1
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
				AND co.order_status_id IN (3, 4, 5, 10)
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
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
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
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to})
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
func granularitySQL(g entity.MetricsGranularity) (dateExpr, subDateExpr string) {
	switch g {
	case entity.MetricsGranularityWeek:
		return "DATE(DATE_SUB(co.placed, INTERVAL WEEKDAY(co.placed) DAY))",
			"DATE(DATE_SUB(created_at, INTERVAL WEEKDAY(created_at) DAY))"
	case entity.MetricsGranularityMonth:
		return "DATE(DATE_FORMAT(co.placed, '%Y-%m-01'))",
			"DATE(DATE_FORMAT(created_at, '%Y-%m-01'))"
	default:
		return "DATE(co.placed)", "DATE(created_at)"
	}
}

// granularityDateExpr returns date expression for a given column (e.g. osh.changed_at).
func granularityDateExpr(g entity.MetricsGranularity, col string) string {
	switch g {
	case entity.MetricsGranularityWeek:
		return fmt.Sprintf("DATE(DATE_SUB(%s, INTERVAL WEEKDAY(%s) DAY))", col, col)
	case entity.MetricsGranularityMonth:
		return fmt.Sprintf("DATE(DATE_FORMAT(%s, '%%Y-%%m-01'))", col)
	default:
		return fmt.Sprintf("DATE(%s)", col)
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
				AND co.order_status_id IN (3, 4, 5, 10)
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
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
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
				AND co.order_status_id IN (3, 4, 5, 10)
				GROUP BY co.id, co.placed, co.total_price, co.refunded_amount
			) ob
		)
		SELECT %s AS d, COUNT(*) AS cnt, COALESCE(SUM(revenue_base), 0) AS value
		FROM order_base
		GROUP BY %s
		ORDER BY d
	`, strings.ReplaceAll(dateExpr, "co.placed", "placed"), strings.ReplaceAll(dateExpr, "co.placed", "placed"))
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Count int             `db:"cnt"`
		Value decimal.Decimal `db:"value"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
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
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to})
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
				AND co.order_status_id IN (3, 4, 5, 10)
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
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
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
				AND co.order_status_id IN (3, 4, 5, 7, 10)
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
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
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
				AND co.order_status_id IN (3, 4, 5, 10)
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
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency})
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
		AND co.order_status_id IN (3, 4, 5, 10)
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := QueryListNamed[struct {
		D     time.Time       `db:"d"`
		Value decimal.Decimal `db:"value"`
		Count int             `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to})
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
		SELECT bucket AS d,
			SUM(CASE WHEN prior_orders = 0 THEN 1 ELSE 0 END) AS new_cnt,
			SUM(CASE WHEN prior_orders > 0 THEN 1 ELSE 0 END) AS ret_cnt
		FROM (
			SELECT %s AS bucket,
				(SELECT COUNT(*) FROM customer_order co2
				 JOIN buyer b2 ON co2.id = b2.order_id
				 WHERE b2.email = b.email AND co2.placed < co.placed
				 AND co2.order_status_id IN (3, 4, 5, 10)) AS prior_orders
			FROM customer_order co
			JOIN buyer b ON co.id = b.order_id
			WHERE co.placed >= :from AND co.placed < :to
			AND co.order_status_id IN (3, 4, 5, 10)
		) AS cust
		GROUP BY bucket
		ORDER BY d
	`, dateExpr)
	rows, err := QueryListNamed[struct {
		D       time.Time `db:"d"`
		NewCnt  int       `db:"new_cnt"`
		RetCnt  int       `db:"ret_cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to})
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
			WHERE osh.order_status_id = 4
			GROUP BY osh.order_id
		) AS first_shipped
		WHERE shipped_at >= :from AND shipped_at < :to
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int      `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to})
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
			WHERE osh.order_status_id = 5
			GROUP BY osh.order_id
		) AS first_delivered
		WHERE delivered_at >= :from AND delivered_at < :to
		GROUP BY %s
		ORDER BY d
	`, dateExpr, dateExpr)
	rows, err := QueryListNamed[struct {
		D     time.Time `db:"d"`
		Count int      `db:"cnt"`
	}](ctx, ms.DB(), query, map[string]any{"from": from, "to": to})
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

func bucketStart(t time.Time, g entity.MetricsGranularity) time.Time {
	loc := t.Location()
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
	f, _ := diff.Float64()
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
