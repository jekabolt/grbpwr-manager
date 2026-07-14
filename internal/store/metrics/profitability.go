package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// profitRaw is the flat set of period figures the profitability tab is assembled from. It is computed
// once per period (current, and the compare period when requested) by getProfitabilityRaw, then folded
// into MetricWithComparison. Every figure comes from the SAME builder the dashboard uses, so the shared
// numbers (gross/contribution margin, opex, operating result) tie out with GetDashboard on one period.
type profitRaw struct {
	grossMargin        decimal.Decimal
	grossMarginPct     decimal.Decimal
	costCoveragePct    decimal.Decimal
	totalDiscount      decimal.Decimal
	productSale        decimal.Decimal
	promoCode          decimal.Decimal
	discountRatePct    decimal.Decimal
	contributionMargin decimal.Decimal

	cpo                    decimal.Decimal
	blendedCAC             decimal.Decimal
	hasSpend               bool
	ltv                    decimal.Decimal
	fulfilmentCostPerOrder decimal.Decimal

	refundRate    decimal.Decimal
	totalRefunded decimal.Decimal
	opexTotal     decimal.Decimal
	marketing     decimal.Decimal
	operating     decimal.Decimal
	opexCaveat    string

	orders       int
	placedOrders int
	newCustomers int
}

// GetProfitability assembles the "Profitability" tab (analytics-v2 task 07): margin and its erosion,
// acquisition economics (CPO / blended CAC / LTV / LTV·CAC), fulfilment cost per order, returns and the
// operating-result roll-up. It orchestrates existing builders — nothing is recomputed from scratch —
// so the shared figures agree with the dashboard. When comparePeriod is set every MetricWithComparison
// carries the prior-period value and change.
func (s *Store) GetProfitability(ctx context.Context, period, comparePeriod entity.TimeRange) (entity.ProfitabilitySection, error) {
	cur, err := s.getProfitabilityRaw(ctx, period.From, period.To)
	if err != nil {
		return entity.ProfitabilitySection{}, err
	}
	hasCompare := !comparePeriod.From.IsZero() && !comparePeriod.To.IsZero()
	var prev profitRaw
	if hasCompare {
		prev, err = s.getProfitabilityRaw(ctx, comparePeriod.From, comparePeriod.To)
		if err != nil {
			return entity.ProfitabilitySection{}, err
		}
	}

	// mwc folds a current/previous decimal pair into a comparison metric; the compare side is filled
	// only when a compare period was requested (otherwise the change would divide by a phantom zero).
	mwc := func(c, p decimal.Decimal) entity.MetricWithComparison {
		m := entity.MetricWithComparison{Value: c}
		if hasCompare {
			m.CompareValue = ptr(p)
			m.ChangePct = changePct(c, p)
		}
		return m
	}

	sec := entity.ProfitabilitySection{
		GrossMargin:         mwc(cur.grossMargin, prev.grossMargin),
		GrossMarginPct:      mwc(cur.grossMarginPct, prev.grossMarginPct),
		CostCoveragePct:     cur.costCoveragePct.InexactFloat64(),
		TotalDiscount:       mwc(cur.totalDiscount, prev.totalDiscount),
		ProductSaleDiscount: mwc(cur.productSale, prev.productSale),
		PromoCodeDiscount:   mwc(cur.promoCode, prev.promoCode),
		DiscountRatePct:     mwc(cur.discountRatePct, prev.discountRatePct),
		ContributionMargin:  mwc(cur.contributionMargin, prev.contributionMargin),

		CPO:                    mwc(cur.cpo, prev.cpo),
		BlendedCAC:             mwc(cur.blendedCAC, prev.blendedCAC),
		HasSpend:               cur.hasSpend,
		LTV:                    cur.ltv,
		FulfilmentCostPerOrder: mwc(cur.fulfilmentCostPerOrder, prev.fulfilmentCostPerOrder),

		RefundRate:      mwc(cur.refundRate, prev.refundRate),
		TotalRefunded:   mwc(cur.totalRefunded, prev.totalRefunded),
		OpexTotal:       cur.opexTotal,
		MarketingSpend:  cur.marketing,
		OperatingResult: cur.operating,
		OpexCaveat:      cur.opexCaveat,
		Caveat:          profitabilityCaveat(cur),
	}

	// Sample sizes so the UI can gate a derived metric on n rather than an arbitrary display floor.
	sec.DiscountRatePct.SampleSize = cur.placedOrders
	sec.RefundRate.SampleSize = cur.placedOrders
	sec.CPO.SampleSize = cur.orders
	sec.BlendedCAC.SampleSize = cur.newCustomers

	// LTV·CAC = historical LTV ÷ blended CAC, only when CAC is a real number (spend entered AND at
	// least one new customer). Left at 0 (= N/A) otherwise so a "free" divide-by-zero can't read as ∞.
	if cur.hasSpend && cur.blendedCAC.GreaterThan(decimal.Zero) {
		sec.LTVCACRatio = cur.ltv.Div(cur.blendedCAC).Round(2).InexactFloat64()
	}
	return sec, nil
}

// getProfitabilityRaw runs every underlying builder for one period and derives the flat figures. It is
// deliberately the same set of calls (and the same operating-result arithmetic) the dashboard uses.
func (s *Store) getProfitabilityRaw(ctx context.Context, from, to time.Time) (profitRaw, error) {
	var r profitRaw
	hundred := decimal.NewFromInt(100)

	_, _, _, orders, _, err := s.getCoreSalesMetrics(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability core sales: %w", err)
	}
	r.orders = orders

	costedRev, cogs, totalItemRev, err := s.getMarginMetrics(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability margin: %w", err)
	}
	r.grossMargin = costedRev.Sub(cogs).Round(2)
	if costedRev.GreaterThan(decimal.Zero) {
		r.grossMarginPct = r.grossMargin.Div(costedRev).Mul(hundred).Round(2)
	}
	if totalItemRev.GreaterThan(decimal.Zero) {
		r.costCoveragePct = costedRev.Div(totalItemRev).Mul(hundred).Round(2)
	}

	_, totalShip, err := s.getShippingCostMetrics(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability shipping: %w", err)
	}
	fees, _, err := s.getPaymentFees(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability fees: %w", err)
	}
	r.contributionMargin = r.grossMargin.Sub(totalShip).Sub(fees).Round(2)
	if orders > 0 {
		r.fulfilmentCostPerOrder = totalShip.Add(fees).Div(decimal.NewFromInt(int64(orders))).Round(2)
	}

	productSale, promoCode, err := s.getDiscountComponents(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability discounts: %w", err)
	}
	r.productSale = productSale.Round(2)
	r.promoCode = promoCode.Round(2)
	r.totalDiscount = productSale.Add(promoCode).Round(2)

	grossRev, err := s.getGrossRevenueTotal(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability gross revenue: %w", err)
	}
	if grossRev.GreaterThan(decimal.Zero) {
		r.discountRatePct = r.totalDiscount.Div(grossRev).Mul(hundred).Round(2)
	}

	revRefund, _, err := s.getRefundMetrics(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability refunds: %w", err)
	}
	r.totalRefunded = revRefund.Round(2)
	if grossRev.GreaterThan(decimal.Zero) {
		r.refundRate = revRefund.Div(grossRev).Mul(hundred).Round(2)
	}

	r.placedOrders, err = s.getPlacedOrdersCount(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability placed orders: %w", err)
	}

	// New customers over the period — the same definition (and builder) as the headline new-customers
	// KPI, so the two never disagree. Bucket granularity is irrelevant to the total, so use day.
	dayExpr, _ := granularitySQL(entity.MetricsGranularityDay)
	newSeries, _, err := s.getNewVsReturningCustomersByPeriod(ctx, from, to, dayExpr)
	if err != nil {
		return r, fmt.Errorf("profitability new customers: %w", err)
	}
	for _, p := range newSeries {
		r.newCustomers += p.Count
	}

	r.marketing, err = s.getChannelSpendTotal(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability marketing spend: %w", err)
	}
	// CPO/CAC only when media spend was actually entered — a zero divisor would read as "0 cost per
	// order", the best possible number, which is a lie. has_spend=false → the UI shows N/A.
	r.hasSpend = r.marketing.GreaterThan(decimal.Zero)
	if r.hasSpend {
		if orders > 0 {
			r.cpo = r.marketing.Div(decimal.NewFromInt(int64(orders))).Round(2)
		}
		if r.newCustomers > 0 {
			r.blendedCAC = r.marketing.Div(decimal.NewFromInt(int64(r.newCustomers))).Round(2)
		}
	}

	clv, err := s.getCLVStats(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability clv: %w", err)
	}
	r.ltv = clv.Mean.Round(2)

	opex, err := s.getOpexForPeriod(ctx, from, to)
	if err != nil {
		return r, fmt.Errorf("profitability opex: %w", err)
	}
	r.opexTotal = opex.Total
	// Operating result — identical arithmetic to GetDashboard: contribution less fixed costs less
	// media spend (spend is subtracted HERE, not in contribution, and not double-counted vs ROAS).
	r.operating = r.contributionMargin.Sub(opex.Total).Sub(r.marketing).Round(2)
	if !opex.Complete {
		r.opexCaveat = "OPEX is missing or uncosted for one or more months in this period — operating result excludes those fixed costs and is incomplete."
	} else if opex.DoubleCountRisk {
		r.opexCaveat = "OPEX may be double-counted: a month in this period has both an aggregate figure and itemised lines for the same category — remove the aggregate once the category is itemised."
	}
	return r, nil
}

// profitabilityCaveat states the two standing caveats of this tab: media spend is entered by hand (so
// CPO/CAC/LTV·CAC are N/A without it), and LTV is a historical realized figure, not a prediction.
func profitabilityCaveat(r profitRaw) string {
	if !r.hasSpend {
		return "No marketing spend entered for this period, so CPO, blended CAC and LTV·CAC are N/A. " +
			"LTV is the historical mean realized revenue per customer this period, not a prediction."
	}
	return "CPO, CAC and LTV·CAC use manually-entered media spend (channel_spend) — read blended CAC on " +
		"monthly+ ranges. LTV is the historical mean realized revenue per customer this period, not a prediction."
}
