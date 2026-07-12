package metrics

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// dashboardReorderScanLimit bounds how many inventory rows we scan to collect the reorder
// list. The alert thresholds themselves are operator-tunable and loaded from alert_setting
// (see settings.go / entity.AlertThresholds); rate-based alerts are still gated on a minimum
// order count so a 1-order period can't trip a "100% refund rate" alarm.
const dashboardReorderScanLimit = 500 // inventory rows scanned to collect the reorder list

// GetDashboard assembles the decision-grade dashboard payload: a small set of DB-trusted
// headline figures, server-computed alerts, and the short action lists (top products by
// margin, SKUs to reorder, slow movers to clear, drop sell-through). It deliberately does NOT
// build the ~90-field BusinessMetrics god-object — each figure comes from a targeted query,
// so the dashboard stays cheap. Use the sectioned GetMetrics for deep drill-down.
func (s *Store) GetDashboard(ctx context.Context, from, to time.Time, limit int) (*entity.Dashboard, error) {
	if limit <= 0 {
		limit = 10
	}

	rev, orders, _, err := s.getCoreSalesMetrics(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard core sales: %w", err)
	}
	costedRev, cogs, totalItemRev, err := s.getMarginMetrics(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard margin: %w", err)
	}
	_, totalShip, err := s.getShippingCostMetrics(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard shipping: %w", err)
	}
	fees, _, err := s.getPaymentFees(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard fees: %w", err)
	}
	placedOrders, err := s.getPlacedOrdersCount(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard placed orders: %w", err)
	}
	grossRev, err := s.getGrossRevenueTotal(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard gross revenue: %w", err)
	}
	revRefund, _, err := s.getRefundMetrics(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard refunds: %w", err)
	}
	uncosted, err := s.getUncostedSoldProductIDs(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard uncosted: %w", err)
	}
	topProducts, err := s.getTopProductsByRevenue(ctx, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("dashboard top products: %w", err)
	}
	health, err := s.inventory().GetInventoryHealth(ctx, from, to, dashboardReorderScanLimit)
	if err != nil {
		return nil, fmt.Errorf("dashboard inventory: %w", err)
	}
	slow, err := s.analytics().GetSlowMovers(ctx, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("dashboard slow movers: %w", err)
	}
	drops, err := s.inventory().GetSellThroughByDrop(ctx, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("dashboard drops: %w", err)
	}
	thresholds, err := s.GetAlertThresholds(ctx)
	if err != nil {
		return nil, fmt.Errorf("dashboard alert thresholds: %w", err)
	}

	grossMargin := costedRev.Sub(cogs).Round(2)
	d := &entity.Dashboard{
		Period:             entity.TimeRange{From: from, To: to},
		Revenue:            rev,
		Orders:             orders,
		GrossMargin:        grossMargin,
		ContributionMargin: grossMargin.Sub(totalShip).Sub(fees).Round(2),
		UncostedProductIds: uncosted,
		Clear:              slow,
		Drops:              drops,
	}
	if costedRev.GreaterThan(decimal.Zero) {
		d.GrossMarginPct = grossMargin.Div(costedRev).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
	}
	if totalItemRev.GreaterThan(decimal.Zero) {
		d.CostCoveragePct = costedRev.Div(totalItemRev).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
	}
	if d.CostCoveragePct > 0 && d.CostCoveragePct < 99.5 {
		d.Caveat = fmt.Sprintf("Margins cover %.0f%% of product revenue (only items with a cost set).", d.CostCoveragePct)
	} else if totalItemRev.GreaterThan(decimal.Zero) && costedRev.LessThanOrEqual(decimal.Zero) {
		d.Caveat = "No product cost data in this period — set product cost_price to compute margins."
	}

	// Top by margin €: the highest-revenue products re-ranked by gross margin €, keeping only
	// costed ones (margins are N/A without a cost). Shows where the € margin actually is.
	topByMargin := make([]entity.ProductMetric, 0, len(topProducts))
	for _, p := range topProducts {
		if p.HasCost {
			topByMargin = append(topByMargin, p)
		}
	}
	sort.SliceStable(topByMargin, func(i, j int) bool {
		return topByMargin[i].GrossMargin.GreaterThan(topByMargin[j].GrossMargin)
	})
	d.TopByMargin = topByMargin

	// Reorder list: SKUs the server flagged (needs_reorder), most urgent first (GetInventoryHealth
	// already orders by days-on-hand asc), capped at limit.
	reorder := make([]entity.InventoryHealthRow, 0, limit)
	for _, r := range health {
		if r.NeedsReorder {
			reorder = append(reorder, r)
			if len(reorder) >= limit {
				break
			}
		}
	}
	d.Reorder = reorder

	var refundRatePct float64
	if grossRev.GreaterThan(decimal.Zero) {
		refundRatePct = revRefund.Div(grossRev).Mul(decimal.NewFromInt(100)).InexactFloat64()
	}
	d.Alerts = buildDashboardAlerts(d, thresholds, refundRatePct, placedOrders, len(reorder), totalItemRev)

	return d, nil
}

// buildDashboardAlerts derives the server-side alert list from the headline figures using the
// operator-tunable thresholds. Rate-based alerts (refund rate) are gated on t.RateFloorN so
// they never fire on a statistically meaningless handful of orders.
func buildDashboardAlerts(d *entity.Dashboard, t entity.AlertThresholds, refundRatePct float64, placedOrders, reorderCount int, totalItemRev decimal.Decimal) []entity.DashboardAlert {
	var out []entity.DashboardAlert

	if d.CostCoveragePct >= t.ContributionTrustPct && d.ContributionMargin.IsNegative() {
		out = append(out, entity.DashboardAlert{
			Severity: entity.AlertSeverityCritical,
			Code:     "negative_contribution_margin",
			Title:    "Negative contribution margin",
			Detail:   fmt.Sprintf("Contribution margin is %s after COGS, shipping and payment fees.", d.ContributionMargin.StringFixed(2)),
		})
	}
	if totalItemRev.GreaterThan(decimal.Zero) && d.CostCoveragePct > 0 && d.CostCoveragePct < t.CoverageWarnPct {
		out = append(out, entity.DashboardAlert{
			Severity: entity.AlertSeverityWarning,
			Code:     "low_cost_coverage",
			Title:    "Low cost coverage",
			Detail:   fmt.Sprintf("Margins cover only %.0f%% of product revenue; set costs to see true margin.", d.CostCoveragePct),
		})
	}
	if n := len(d.UncostedProductIds); n > 0 {
		out = append(out, entity.DashboardAlert{
			Severity: entity.AlertSeverityWarning,
			Code:     "uncosted_products",
			Title:    "Products missing cost",
			Detail:   fmt.Sprintf("%d product(s) sold this period have no cost set.", n),
		})
	}
	if reorderCount > 0 {
		out = append(out, entity.DashboardAlert{
			Severity: entity.AlertSeverityWarning,
			Code:     "reorder_needed",
			Title:    "Reorder needed",
			Detail:   fmt.Sprintf("%d SKU(s) at or below their reorder point.", reorderCount),
		})
	}
	if placedOrders >= t.RateFloorN && refundRatePct >= t.RefundRateWarnPct {
		out = append(out, entity.DashboardAlert{
			Severity: entity.AlertSeverityWarning,
			Code:     "high_refund_rate",
			Title:    "High refund rate",
			Detail:   fmt.Sprintf("Refund rate is %.1f%% over %d orders.", refundRatePct, placedOrders),
		})
	}
	return out
}
