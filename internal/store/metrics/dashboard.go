package metrics

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
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

	rev, grossInclVat, _, orders, _, err := s.getCoreSalesMetrics(ctx, from, to)
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
	ga4Rev, err := s.getGA4Revenue(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard ga4 revenue: %w", err)
	}
	opex, err := s.getOpexForPeriod(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard opex: %w", err)
	}
	marketingSpend, err := s.getChannelSpendTotal(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("dashboard marketing spend: %w", err)
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
		GA4Revenue:         ga4Rev,
		Clear:              slow,
		Drops:              drops,
	}
	if costedRev.GreaterThan(decimal.Zero) {
		d.GrossMarginPct = grossMargin.Div(costedRev).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
	}
	// GA4-vs-DB revenue coverage (task 20): what fraction of real DB revenue GA4 saw. The
	// denominator must be LIKE-FOR-LIKE with GA4's purchase `value` = what the customer actually
	// paid (order.totalPrice): post-discount, shipping- AND VAT-inclusive. That is grossInclVat —
	// NOT the pre-discount list-price grossRev (which would understate coverage on any discounted
	// order) and NOT the net-of-VAT headline revenue (which would understate it by the VAT rate).
	if grossInclVat.GreaterThan(decimal.Zero) {
		d.TrackingCoveragePct = ga4Rev.Div(grossInclVat).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
	}
	// Operating result (task 22): the honest total under contribution margin. Marketing spend is
	// subtracted HERE (not in contribution — it isn't variable per order), which also avoids
	// double-counting it against the ROAS report.
	d.OpexTotal = opex.Total
	d.MarketingSpend = marketingSpend
	d.OperatingResult = d.ContributionMargin.Sub(opex.Total).Sub(marketingSpend).Round(2)
	if !opex.Complete {
		// Covers "no OPEX at all", "some months recorded, others missing", and "a line whose currency
		// had no FX rate (uncosted, excluded from the total)" — any of these understates fixed costs.
		d.OpexCaveat = "OPEX is missing or uncosted for one or more months in this period — operating result excludes those fixed costs and is incomplete."
	} else if opex.DoubleCountRisk {
		// The opposite failure: a month carries both a migrated '(aggregate)' lump and itemised lines
		// of the same category, so its OPEX is counted twice and the operating result is overstated.
		d.OpexCaveat = "OPEX may be double-counted: a month in this period has both an aggregate figure and itemised lines for the same category — remove the aggregate once the category is itemised."
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
	lowStockNames, lowStockCount, err := s.GetLowStockMaterials(ctx, dashboardLowStockNamesLimit)
	if err != nil {
		return nil, fmt.Errorf("dashboard low material stock: %w", err)
	}
	staleRuns, err := s.GetStaleOpenRunCount(ctx, thresholds.ProductionRunStaleDays)
	if err != nil {
		return nil, fmt.Errorf("dashboard stale runs: %w", err)
	}
	d.Alerts = buildDashboardAlerts(d, thresholds, refundRatePct, placedOrders, len(reorder), totalItemRev,
		lowStockNames, lowStockCount, staleRuns)

	return d, nil
}

// dashboardLowStockNamesLimit caps how many material names the low_material_stock alert lists.
const dashboardLowStockNamesLimit = 5

// getGA4Revenue sums the GA4-reported ecommerce revenue over the period from the
// ga4_ecommerce_metrics cache (populated daily by the ga4sync worker; re-syncable — see the
// analytics-cache note). Rows are keyed by calendar date (whole-day buckets); a day [d, d+1) is
// counted iff it OVERLAPS the half-open request window [from, to) — matching the DB revenue
// interval (`placed >= from AND placed < to`). The old `date <= DATE(:to)` counted the boundary
// day the DB excludes, inflating coverage by a full day on a midnight-aligned `to`. COALESCE keeps
// an empty (not-yet-synced) window at zero rather than NULL.
func (s *Store) getGA4Revenue(ctx context.Context, from, to time.Time) (decimal.Decimal, error) {
	row, err := storeutil.QueryNamedOne[struct {
		Revenue decimal.Decimal `db:"revenue"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(revenue), 0) AS revenue
		FROM ga4_ecommerce_metrics
		WHERE date < :to AND date + INTERVAL 1 DAY > :from`,
		map[string]any{"from": from, "to": to})
	if err != nil {
		return decimal.Zero, fmt.Errorf("ga4 revenue: %w", err)
	}
	return row.Revenue, nil
}

// GetLowStockMaterials returns the names of active materials whose on-hand is below their configured
// minimum (NF-02), ordered by shortfall (most-below first) and capped at `limit`, plus the total
// count of below-min materials (the count reflects all, not just the returned top-N). Materials with
// no min_stock set are never low-stock. Feeds the low_material_stock dashboard alert (NF-09).
// Exported (not on the dependency.Metrics interface) so the store integration suite can exercise the
// query directly — the metrics package has no live-DB harness of its own (nf09-07).
//
// LEFT JOIN + COALESCE(on_hand, 0), symmetric with the warehouse list (ListMaterialStock BelowMinOnly):
// a material_stock row is created lazily on the first movement, so a just-created material with a
// min_stock and zero movements has NO stock row. An INNER JOIN would drop exactly that material —
// the most blatant «we have none of this» case — making the dashboard alert disagree with the
// warehouse screen (nf09-01).
func (s *Store) GetLowStockMaterials(ctx context.Context, limit int) ([]string, int, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Name string `db:"name"`
	}](ctx, s.DB, `
		SELECT m.name
		FROM material m
		LEFT JOIN material_stock st ON st.material_id = m.id
		WHERE m.archived = FALSE AND m.min_stock IS NOT NULL AND COALESCE(st.on_hand, 0) < m.min_stock
		ORDER BY (m.min_stock - COALESCE(st.on_hand, 0)) DESC, m.name`, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("get low stock materials: %w", err)
	}
	names := make([]string, 0, len(rows))
	for i, r := range rows {
		if limit > 0 && i >= limit {
			break
		}
		names = append(names, r.Name)
	}
	return names, len(rows), nil
}

// GetStaleOpenRunCount counts production runs still open (planned/in_progress) whose created_at is
// older than staleDays — forgotten runs that keep their issued materials pinned in WIP (NF-09).
// staleDays <= 0 disables the check. Exported for the same integration-coverage reason as
// GetLowStockMaterials (nf09-07).
func (s *Store) GetStaleOpenRunCount(ctx context.Context, staleDays int) (int, error) {
	if staleDays <= 0 {
		return 0, nil
	}
	cutoff := s.Now().AddDate(0, 0, -staleDays)
	return storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM production_run
		WHERE status IN (:planned, :inprogress) AND created_at < :cutoff`,
		map[string]any{
			"planned":    string(entity.ProductionRunPlanned),
			"inprogress": string(entity.ProductionRunInProgress),
			"cutoff":     cutoff,
		})
}

// buildDashboardAlerts derives the server-side alert list from the headline figures using the
// operator-tunable thresholds. Rate-based alerts (refund rate) are gated on t.RateFloorN so
// they never fire on a statistically meaningless handful of orders.
func buildDashboardAlerts(d *entity.Dashboard, t entity.AlertThresholds, refundRatePct float64, placedOrders, reorderCount int, totalItemRev decimal.Decimal, lowStockNames []string, lowStockCount, staleRunCount int) []entity.DashboardAlert {
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
	// GA4 tracking-coverage alert (task 20): GA4 saw materially less revenue than the DB.
	// Gated on the significance floor, on GA4 having synced *some* revenue (a flat zero is "not
	// synced yet", not "0% coverage", so a fresh window doesn't cry wolf), and on the DB actually
	// having revenue (a zero denominator leaves coverage at 0 spuriously). We deliberately do NOT
	// also require TrackingCoveragePct > 0: a positive-but-tiny GA4 figure rounds coverage to 0.00,
	// which is the WORST case (near-total tracking loss) and must still alarm — the old > 0 guard
	// silently suppressed exactly that.
	if placedOrders >= t.RateFloorN && d.GA4Revenue.IsPositive() && d.Revenue.IsPositive() &&
		d.TrackingCoveragePct < t.GA4CoverageWarnPct {
		out = append(out, entity.DashboardAlert{
			Severity: entity.AlertSeverityWarning,
			Code:     "low_ga4_tracking_coverage",
			Title:    "Low GA4 tracking coverage",
			Detail: fmt.Sprintf("GA4 saw only %.0f%% of DB revenue this period — check tracking (consent, ad-blockers, bots). Read ROAS as ≈ shown / coverage.",
				d.TrackingCoveragePct),
		})
	}
	// Low material stock (NF-09): materials below their configured minimum. The alert names the
	// most-below few; the full list is on the warehouse screen (ListMaterialStock, below-min filter).
	if lowStockCount > 0 {
		detail := fmt.Sprintf("%d material(s) below minimum stock.", lowStockCount)
		if len(lowStockNames) > 0 {
			detail = fmt.Sprintf("%d material(s) below minimum stock: %s.", lowStockCount, strings.Join(lowStockNames, ", "))
		}
		out = append(out, entity.DashboardAlert{
			Severity: entity.AlertSeverityWarning,
			Code:     "low_material_stock",
			Title:    "Low material stock",
			Detail:   detail,
		})
	}
	// Stale open production runs (NF-09): planned/in_progress runs older than the threshold — likely
	// forgotten, and their issued materials stay pinned in WIP, distorting the inventory valuation.
	if staleRunCount > 0 {
		out = append(out, entity.DashboardAlert{
			Severity: entity.AlertSeverityWarning,
			Code:     "stale_open_production_run",
			Title:    "Stale production runs",
			Detail:   fmt.Sprintf("%d production run(s) have been open longer than %d days.", staleRunCount, t.ProductionRunStaleDays),
		})
	}
	return out
}
