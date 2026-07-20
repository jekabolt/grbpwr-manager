package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
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

	// Accounting-module health alerts (Step 8, docs/plan-accounting/07-worker-config.md
	// "Health-алерты"): posting lag, manual-entry backlog, revenue reconciliation drift. Appended
	// rather than folded into buildDashboardAlerts' own parameter list so that function's existing
	// signature — and its unit tests in dashboard_test.go — stay untouched; buildAcctDashboardAlerts
	// follows the exact same pure-function/precomputed-inputs shape. Silently empty until the
	// accounting module has posted its first entry (see GetAcctModuleActive).
	acctAlerts, err := s.acctDashboardAlerts(ctx, thresholds)
	if err != nil {
		return nil, fmt.Errorf("dashboard accounting alerts: %w", err)
	}
	d.Alerts = append(d.Alerts, acctAlerts...)

	return d, nil
}

// GetDashboardHeadline computes only the six higher-is-better headline figures (revenue, orders,
// gross margin €/%, contribution margin, operating result) for a single window, using the SAME
// arithmetic as GetDashboard so a period-over-period delta is apples-to-apples. It runs the six
// targeted sales/cost queries but, unlike GetDashboard, builds no action lists or alerts — cheap
// enough to call for the comparison window on every dashboard render. The handler diffs its result
// against the primary dashboard to populate the comparison; the deltas themselves are shaped at DTO.
func (s *Store) GetDashboardHeadline(ctx context.Context, from, to time.Time) (*entity.DashboardHeadline, error) {
	rev, _, _, orders, _, err := s.getCoreSalesMetrics(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("headline core sales: %w", err)
	}
	costedRev, cogs, _, err := s.getMarginMetrics(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("headline margin: %w", err)
	}
	_, totalShip, err := s.getShippingCostMetrics(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("headline shipping: %w", err)
	}
	fees, _, err := s.getPaymentFees(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("headline fees: %w", err)
	}
	opex, err := s.getOpexForPeriod(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("headline opex: %w", err)
	}
	marketingSpend, err := s.getChannelSpendTotal(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("headline marketing spend: %w", err)
	}

	grossMargin := costedRev.Sub(cogs).Round(2)
	contribution := grossMargin.Sub(totalShip).Sub(fees).Round(2)
	h := &entity.DashboardHeadline{
		Revenue:            rev,
		Orders:             orders,
		GrossMargin:        grossMargin,
		ContributionMargin: contribution,
		OperatingResult:    contribution.Sub(opex.Total).Sub(marketingSpend).Round(2),
	}
	if costedRev.GreaterThan(decimal.Zero) {
		h.GrossMarginPct = grossMargin.Div(costedRev).Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
	}
	return h, nil
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

// --- Accounting module health alerts (Step 8, docs/plan-accounting/07-worker-config.md
// "Health-алерты") ---
//
// The queries below read acct_journal_entry / acct_event / acct_checkpoint / material_stock_movement /
// customer_order directly via s.DB — this package has no dependency on internal/store/accounting (same
// cross-domain-read precedent as the rest of this file, and the same tables/formulas the accounting
// store's own reconcile.go uses, trimmed to what the dashboard needs). They are exported (not on
// dependency.Metrics) for the same integration-coverage reason as GetLowStockMaterials /
// GetStaleOpenRunCount: the metrics package has no live-DB test harness of its own (nf09-07).

// acctReconciliationDriftPct is the fractional (not %) revenue-reconciliation drift threshold for the
// acct_reconciliation_drift alert ("delta above 1% of the ledger value").
const acctReconciliationDriftPct = 0.01

// acctManualEntryLookbackDays bounds the acct_manual_entry_required alert to recent skips ("over the
// last 30 days") so a resolved backlog eventually ages out of the dashboard.
const acctManualEntryLookbackDays = 30

// acctManualEntryWarnFloor is the count at/above which acct_manual_entry_required escalates from info
// to warning. A handful of non-EUR/non-Stripe orders needing a manual entry is business-as-usual for
// an international store; a growing pile signals bookkeeping is falling behind.
const acctManualEntryWarnFloor = 5

// GetAcctModuleActive reports whether the accounting module has posted at least one journal entry — a
// cheap EXISTS gate so the three acct_* dashboard alerts stay silent until the module is actually in
// use (compiled in but never enabled, or enabled but not yet ticked once).
func (s *Store) GetAcctModuleActive(ctx context.Context) (bool, error) {
	n, err := storeutil.QueryCountNamed(ctx, s.DB, `SELECT EXISTS(SELECT 1 FROM acct_journal_entry) AS active`, nil)
	if err != nil {
		return false, fmt.Errorf("get accounting module active: %w", err)
	}
	return n > 0, nil
}

// GetAcctPostingLag returns the count and max age (hours) of the accounting posting backlog:
// unprocessed acct_event rows (processed_at IS NULL) and material_stock_movement rows stuck past the
// acctposting worker's material_movement checkpoint, both older than lagHours. lagHours is the
// operator-tunable AlertSettings.acct_posting_lag_hours (proto field 7; internal/dto/metrics.go
// AlertThresholdsToPb/FromPb), editable end-to-end via GetAlertSettings/UpsertAlertSettings — a normal
// saved threshold like ProductionRunStaleDays, not a special case. lagHours <= 0 still falls back to
// entity.DefaultAlertThresholds().AcctPostingLagHours (24h) rather than disabling the check outright
// (unlike GetStaleOpenRunCount's staleDays <= 0 == off): that guard exists purely so an unset/zeroed
// value never goes dark with no operator intent behind it, not to route around a missing wire-up. The
// movements side is INNER JOINed to acct_checkpoint on purpose: a store whose movements phase has never
// posted anything yet (no checkpoint row) contributes zero rather than mistaking its entire
// pre-accounting movement history for backlog.
func (s *Store) GetAcctPostingLag(ctx context.Context, lagHours int) (count int, maxAgeHours float64, err error) {
	if lagHours <= 0 {
		lagHours = entity.DefaultAlertThresholds().AcctPostingLagHours
	}
	cutoff := s.Now().Add(-time.Duration(lagHours) * time.Hour)

	events, err := storeutil.QueryNamedOne[struct {
		Cnt    int          `db:"cnt"`
		Oldest sql.NullTime `db:"oldest"`
	}](ctx, s.DB, `
		SELECT COUNT(*) AS cnt, MIN(occurred_at) AS oldest
		FROM acct_event
		WHERE processed_at IS NULL AND occurred_at < :cutoff`,
		map[string]any{"cutoff": cutoff})
	if err != nil {
		return 0, 0, fmt.Errorf("acct posting lag events: %w", err)
	}

	movements, err := storeutil.QueryNamedOne[struct {
		Cnt    int          `db:"cnt"`
		Oldest sql.NullTime `db:"oldest"`
	}](ctx, s.DB, `
		SELECT COUNT(*) AS cnt, MIN(m.created_at) AS oldest
		FROM material_stock_movement m
		JOIN acct_checkpoint cp ON cp.source = 'material_movement'
		WHERE m.id > cp.last_id AND m.created_at < :cutoff`,
		map[string]any{"cutoff": cutoff})
	if err != nil {
		return 0, 0, fmt.Errorf("acct posting lag movements: %w", err)
	}

	count = events.Cnt + movements.Cnt
	if count == 0 {
		return 0, 0, nil
	}
	oldest := events.Oldest
	if movements.Oldest.Valid && (!oldest.Valid || movements.Oldest.Time.Before(oldest.Time)) {
		oldest = movements.Oldest
	}
	if oldest.Valid {
		maxAgeHours = s.Now().Sub(oldest.Time).Hours()
	}
	return count, maxAgeHours, nil
}

// GetAcctManualEntryRequiredCount counts acct_event rows the acctposting worker could not post
// automatically and flagged for manual bookkeeping (last_error LIKE '%manual entry required%' — see
// accounting.ErrSkipNonEUR and acctposting/outbox.go skipEvent, e.g. "non-eur non-stripe order, manual
// entry required") whose business date (occurred_at) falls in the last acctManualEntryLookbackDays days.
func (s *Store) GetAcctManualEntryRequiredCount(ctx context.Context) (int, error) {
	cutoff := s.Now().AddDate(0, 0, -acctManualEntryLookbackDays)
	n, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM acct_event
		WHERE processed_at IS NOT NULL
		  AND last_error LIKE '%manual entry required%'
		  AND occurred_at >= :cutoff`,
		map[string]any{"cutoff": cutoff})
	if err != nil {
		return 0, fmt.Errorf("get acct manual entry required count: %w", err)
	}
	return n, nil
}

// GetAcctOpenDisputeCount counts Stripe chargebacks that are still open — an order_dispute outbox event
// keyed 'dispute:<id>:open' with no matching 'dispute:<id>:close' event yet (phase 2, wave 4 — §4.3).
// Feeds the acct_dispute_open dashboard alert: an open dispute has a response deadline, and the funds are
// already withheld from the Stripe balance, so the operator must act.
func (s *Store) GetAcctOpenDisputeCount(ctx context.Context) (int, error) {
	n, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM acct_event o
		WHERE o.event_type = 'order_dispute'
		  AND o.source_key LIKE 'dispute:%:open'
		  AND NOT EXISTS (
		      SELECT 1 FROM acct_event c
		      WHERE c.event_type = 'order_dispute'
		        AND c.source_key = REPLACE(o.source_key, ':open', ':close'))`, nil)
	if err != nil {
		return 0, fmt.Errorf("get acct open dispute count: %w", err)
	}
	return n, nil
}

// GetAcctRevenueReconForMonth computes the revenue-block ledger and operational figures for the
// calendar month containing `now`, trimmed from accounting.Store's reconRevenue
// (internal/store/accounting/reconcile.go) to the two sums the acct_reconciliation_drift alert needs.
// Ledger is Σ acct_journal_line.amount credited to the NET/SHIP/other-revenue accounts (4010/4020/4110)
// by order_sale entries; operational is Σ(settled − VAT share) over the month's net-revenue-status
// orders — the same formula reconcile.go uses. Returns (ledger, zero) when the order-status cache is
// empty (a cache-less harness — production always loads it in app.Start; same guard reconcile.go uses)
// so an empty `IN ()` never reaches the DB.
func (s *Store) GetAcctRevenueReconForMonth(ctx context.Context, now time.Time) (ledger, operational decimal.Decimal, err error) {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)

	ledgerRow, err := storeutil.QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(l.amount), 0) AS v
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE a.code IN (:codes)
		  AND l.side = 'credit'
		  AND e.source_type = 'order_sale'
		  AND e.occurred_at >= :from AND e.occurred_at < :to`,
		map[string]any{
			"codes": []string{"4010", "4020", "4110"},
			"from":  monthStart.Format("2006-01-02"),
			"to":    monthEnd.Format("2006-01-02"),
		})
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("acct revenue recon ledger: %w", err)
	}
	ledger = ledgerRow.V

	statusIDs := cache.OrderStatusIDsForNetRevenue()
	if len(statusIDs) == 0 {
		return ledger, decimal.Zero, nil
	}
	opRow, err := storeutil.QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(
			CASE WHEN co.total_price > 0 THEN
				COALESCE(co.total_settled_base, co.total_price)
				- COALESCE(co.vat_amount, 0) * (COALESCE(co.total_settled_base, co.total_price) / co.total_price)
			ELSE 0 END), 0) AS v
		FROM customer_order co
		WHERE co.order_status_id IN (:statusIds)
		  AND co.placed >= :from AND co.placed < :to
		  AND (co.total_settled_base IS NOT NULL OR co.currency = :base)`,
		map[string]any{
			"statusIds": statusIDs,
			"from":      monthStart,
			"to":        monthEnd,
			"base":      cache.GetBaseCurrency(),
		})
	if err != nil {
		return decimal.Zero, decimal.Zero, fmt.Errorf("acct revenue recon operational: %w", err)
	}
	operational = opRow.V.Round(2)
	return ledger, operational, nil
}

// acctDashboardAlertInputs bundles the precomputed inputs buildAcctDashboardAlerts needs, mirroring how
// buildDashboardAlerts takes its own figures as plain parameters — computed once in acctDashboardAlerts
// so the builder stays a pure function of already-fetched data. The zero value (module inactive, or an
// individual figure that came back empty) yields no alerts.
type acctDashboardAlertInputs struct {
	PostingLagCount       int
	PostingLagMaxAgeHours float64
	// PostingLagThresholdHours is the threshold GetAcctPostingLag actually applied (after its own
	// <= 0 → default fallback), so the alert text always names the cutoff that produced the count
	// rather than a possibly-stale/zeroed t.AcctPostingLagHours.
	PostingLagThresholdHours int
	ManualEntryCount         int
	OpenDisputeCount         int
	ReconLedger              decimal.Decimal
	ReconOperational         decimal.Decimal
}

// acctDashboardAlerts assembles acctDashboardAlertInputs and turns them into the accounting-module
// dashboard alerts, gated on GetAcctModuleActive so a store that has not turned accounting on (no
// acct_journal_entry row posted yet) sees none of this — "не показываем шум до включения модуля".
func (s *Store) acctDashboardAlerts(ctx context.Context, t entity.AlertThresholds) ([]entity.DashboardAlert, error) {
	active, err := s.GetAcctModuleActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("acct dashboard alerts: %w", err)
	}
	if !active {
		return nil, nil
	}

	var in acctDashboardAlertInputs
	in.PostingLagThresholdHours = t.AcctPostingLagHours
	if in.PostingLagThresholdHours <= 0 {
		in.PostingLagThresholdHours = entity.DefaultAlertThresholds().AcctPostingLagHours
	}
	in.PostingLagCount, in.PostingLagMaxAgeHours, err = s.GetAcctPostingLag(ctx, in.PostingLagThresholdHours)
	if err != nil {
		return nil, fmt.Errorf("acct dashboard alerts: %w", err)
	}
	in.ManualEntryCount, err = s.GetAcctManualEntryRequiredCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("acct dashboard alerts: %w", err)
	}
	in.OpenDisputeCount, err = s.GetAcctOpenDisputeCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("acct dashboard alerts: %w", err)
	}
	in.ReconLedger, in.ReconOperational, err = s.GetAcctRevenueReconForMonth(ctx, s.Now())
	if err != nil {
		return nil, fmt.Errorf("acct dashboard alerts: %w", err)
	}
	return buildAcctDashboardAlerts(in), nil
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

// buildAcctDashboardAlerts derives the three accounting-module alerts (Step 8) from precomputed
// inputs, by the same pattern as buildDashboardAlerts (severity + stable code + title + detail with a
// counter). Kept as a sibling function rather than folded into buildDashboardAlerts itself so that
// function's existing signature — and its unit tests in dashboard_test.go — are untouched. It takes no
// entity.AlertThresholds: acctDashboardAlerts already resolves the one configurable figure
// (PostingLagThresholdHours) into `in`, and the other two alerts' thresholds are fixed constants
// (acctManualEntryLookbackDays/-WarnFloor, acctReconciliationDriftPct) — see their doc comments for
// why they are not operator-tunable settings. Callers must only pass it inputs from an
// acctDashboardAlerts call that already confirmed the module is active (GetAcctModuleActive); on the
// zero value it correctly yields no alerts either way.
func buildAcctDashboardAlerts(in acctDashboardAlertInputs) []entity.DashboardAlert {
	var out []entity.DashboardAlert

	// acct_posting_lag: unprocessed acct_event rows or material movements stuck behind the
	// acctposting worker's checkpoint, both older than PostingLagThresholdHours — signals the worker
	// is stalled or falling behind.
	if in.PostingLagCount > 0 {
		out = append(out, entity.DashboardAlert{
			Severity: entity.AlertSeverityWarning,
			Code:     "acct_posting_lag",
			Title:    "Accounting posting lag",
			Detail: fmt.Sprintf("%d accounting event(s)/movement(s) have been unposted for more than %d hours (oldest %.0fh).",
				in.PostingLagCount, in.PostingLagThresholdHours, in.PostingLagMaxAgeHours),
		})
	}
	// acct_manual_entry_required: orders the worker could not post automatically (non-Stripe, non-EUR
	// — no FX rate to convert to base) in the last 30 days. A handful is business-as-usual for an
	// international store; a growing pile means bookkeeping is falling behind on manual entries.
	if in.ManualEntryCount > 0 {
		sev := entity.AlertSeverityInfo
		if in.ManualEntryCount >= acctManualEntryWarnFloor {
			sev = entity.AlertSeverityWarning
		}
		out = append(out, entity.DashboardAlert{
			Severity: sev,
			Code:     "acct_manual_entry_required",
			Title:    "Manual accounting entries required",
			Detail:   fmt.Sprintf("%d order(s) in the last 30 days need a manual accounting entry (non-EUR, non-Stripe).", in.ManualEntryCount),
		})
	}
	// acct_dispute_open: Stripe chargebacks that are opened but not yet closed (phase 2, wave 4). The
	// funds are already withheld from the Stripe balance and each dispute has a response deadline, so any
	// open dispute is actionable.
	if in.OpenDisputeCount > 0 {
		out = append(out, entity.DashboardAlert{
			Severity: entity.AlertSeverityWarning,
			Code:     "acct_dispute_open",
			Title:    "Open Stripe disputes",
			Detail:   fmt.Sprintf("%d Stripe dispute(s) are open — respond before the deadline or the withheld funds are lost.", in.OpenDisputeCount),
		})
	}
	// acct_reconciliation_drift: the ledger's posted revenue for the current month has drifted from
	// the operational (customer_order) figure by more than acctReconciliationDriftPct. Guarded on
	// ledger > 0 (a % of zero is meaningless, and there is nothing posted yet to have drifted).
	if in.ReconLedger.GreaterThan(decimal.Zero) {
		delta := in.ReconLedger.Sub(in.ReconOperational)
		driftPct := delta.Abs().Div(in.ReconLedger)
		if driftPct.GreaterThan(decimal.NewFromFloat(acctReconciliationDriftPct)) {
			out = append(out, entity.DashboardAlert{
				Severity: entity.AlertSeverityWarning,
				Code:     "acct_reconciliation_drift",
				Title:    "Accounting reconciliation drift",
				Detail: fmt.Sprintf("Ledger revenue (%s) is %.1f%% off the operational figure (%s) this month — check the reconciliation report.",
					in.ReconLedger.StringFixed(2), driftPct.Mul(decimal.NewFromInt(100)).InexactFloat64(), in.ReconOperational.StringFixed(2)),
			})
		}
	}

	return out
}
