package accounting

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// defaultScanBatch bounds a pull-source batch when the caller passes a non-positive limit
// (docs/plan-accounting/03 uses batches of ~200 for movements).
const defaultScanBatch = 200

// GetOrderFactsForPosting assembles the flat fact set for an order sale/refund (09.2). The header
// comes from customer_order JOIN payment LEFT JOIN shipment; Items are the COGS lines with the
// snapshot-first cost fallback (COALESCE(oi.cost_price_at_sale, product.cost_price)). A missing order
// surfaces as sql.ErrNoRows (wrapped). This reads other domains' tables directly (the
// internal/store/metrics precedent).
func (s *Store) GetOrderFactsForPosting(ctx context.Context, orderUUID string) (*entity.AcctOrderFacts, error) {
	facts, err := storeutil.QueryNamedOne[entity.AcctOrderFacts](ctx, s.DB, `
		SELECT co.id, co.uuid, co.placed, co.total_price, co.currency,
		       co.total_settled_base, co.payment_fee, co.vat_amount, co.vat_rate_pct,
		       p.payment_method_id,
		       s.cost AS shipment_cost, s.free_shipping
		FROM customer_order co
		JOIN payment p ON p.order_id = co.id
		LEFT JOIN shipment s ON s.order_id = co.id
		WHERE co.uuid = :uuid`, map[string]any{"uuid": orderUUID})
	if err != nil {
		return nil, fmt.Errorf("accounting: get order facts %s: %w", orderUUID, err)
	}
	pm, ok := cache.GetPaymentMethodById(facts.PaymentMethodId)
	if !ok {
		return nil, fmt.Errorf("accounting: payment method %d not found in cache for order %s", facts.PaymentMethodId, orderUUID)
	}
	facts.PaymentMethodName = pm.Method.Name
	facts.FeePct = pm.Method.FeePct
	facts.FeeFixed = pm.Method.FeeFixed
	items, err := storeutil.QueryListNamed[entity.AcctOrderItemFact](ctx, s.DB, `
		SELECT oi.id, oi.product_id, oi.quantity,
		       COALESCE(oi.cost_price_at_sale, pr.cost_price) AS unit_cost
		FROM order_item oi
		JOIN product pr ON pr.id = oi.product_id
		WHERE oi.order_id = :order_id`, map[string]any{"order_id": facts.Id})
	if err != nil {
		return nil, fmt.Errorf("accounting: get order item facts %s: %w", orderUUID, err)
	}
	facts.Items = items
	return &facts, nil
}

// ListUnpostedMovements returns material_stock_movement rows (joined with the material name) with
// id > afterID and created_at >= startDate, oldest first, up to limit. The worker posts each per the
// M1–M8 rules and advances the checkpoint; uncosted rows are skipped by the builder but still move
// the cursor. Reading here (not inside the worker Tx) obeys the "facts on the pool" lock rule (07).
func (s *Store) ListUnpostedMovements(ctx context.Context, afterID int64, startDate time.Time, limit int) ([]entity.AcctMovementFacts, error) {
	if limit <= 0 {
		limit = defaultScanBatch
	}
	movements, err := storeutil.QueryListNamed[entity.AcctMovementFacts](ctx, s.DB, `
		SELECT m.*, mat.name AS material_name
		FROM material_stock_movement m
		JOIN material mat ON mat.id = m.material_id
		WHERE m.id > :after_id AND m.created_at >= :start_date
		ORDER BY m.id
		LIMIT :limit`,
		map[string]any{"after_id": afterID, "start_date": startDate.UTC(), "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("accounting: list unposted movements: %w", err)
	}
	return movements, nil
}

// ListUnpostedReceivedRuns returns ids of production runs received on/after startDate that have no
// production_receive journal entry yet (idempotency IS the checkpoint here — runs are few), oldest
// receive first, up to limit.
func (s *Store) ListUnpostedReceivedRuns(ctx context.Context, startDate time.Time, limit int) ([]int, error) {
	if limit <= 0 {
		limit = defaultScanBatch
	}
	rows, err := storeutil.QueryListNamed[struct {
		Id int `db:"id"`
	}](ctx, s.DB, `
		SELECT r.id FROM production_run r
		WHERE r.received_at IS NOT NULL AND r.received_at >= :start_date
		  AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
		                  WHERE e.source_type = 'production_receive' AND e.source_key = CAST(r.id AS CHAR) COLLATE utf8mb4_unicode_ci)
		ORDER BY r.received_at, r.id
		LIMIT :limit`, map[string]any{"start_date": startDate.UTC(), "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("accounting: list unposted received runs: %w", err)
	}
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.Id)
	}
	return ids, nil
}

// GetRunFactsForPosting assembles the production-receive fact set (P1): the run's received_at, its
// manual cost articles (production_run_cost), and its material issue/return movements. LEDGER_WIP
// (Σ costed issue_production − return_production, with the pre-cutover exclusion) is derived from
// Issues by the caller, which knows accounting.start_date — this method has no start-date argument.
func (s *Store) GetRunFactsForPosting(ctx context.Context, runID int) (*entity.AcctRunFacts, error) {
	hdr, err := storeutil.QueryNamedOne[struct {
		Id           int          `db:"id"`
		ReceivedAt   sql.NullTime `db:"received_at"`
		TechCardName string       `db:"tech_card_name"`
	}](ctx, s.DB, `
		SELECT r.id, r.received_at, tc.name AS tech_card_name
		FROM production_run r
		JOIN tech_card tc ON tc.id = r.tech_card_id
		WHERE r.id = :id`, map[string]any{"id": runID})
	if err != nil {
		return nil, fmt.Errorf("accounting: get run header %d: %w", runID, err)
	}
	if !hdr.ReceivedAt.Valid {
		return nil, fmt.Errorf("accounting: run %d is not received (received_at is null)", runID)
	}
	costs, err := storeutil.QueryListNamed[entity.ProductionRunCost](ctx, s.DB, `
		SELECT id, run_id, kind, description, amount, currency, amount_base, incurred_at
		FROM production_run_cost WHERE run_id = :id ORDER BY id`, map[string]any{"id": runID})
	if err != nil {
		return nil, fmt.Errorf("accounting: get run costs %d: %w", runID, err)
	}
	issues, err := storeutil.QueryListNamed[entity.AcctRunIssueFact](ctx, s.DB, `
		SELECT movement_type, quantity, unit_cost_base, created_at
		FROM material_stock_movement
		WHERE production_run_id = :id
		  AND movement_type IN ('issue_production','return_production')
		ORDER BY id`, map[string]any{"id": runID})
	if err != nil {
		return nil, fmt.Errorf("accounting: get run issues %d: %w", runID, err)
	}
	rf := &entity.AcctRunFacts{
		RunID:        runID,
		ReceivedAt:   hdr.ReceivedAt.Time,
		TechCardName: hdr.TechCardName,
		Costs:        costs,
		Issues:       issues,
	}
	return rf, nil
}

// ListChangedOpexMonths returns the distinct opex_line months whose rows changed after afterTS
// (oldest first). The worker filters out pre-cutover months (month >= start_month) — that bound
// depends on accounting.start_date, which lives in the worker's config, not this method.
func (s *Store) ListChangedOpexMonths(ctx context.Context, afterTS time.Time) ([]time.Time, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Month time.Time `db:"month"`
	}](ctx, s.DB, `
		SELECT DISTINCT month FROM opex_line
		WHERE updated_at > :after_ts
		ORDER BY month`, map[string]any{"after_ts": afterTS.UTC()})
	if err != nil {
		return nil, fmt.Errorf("accounting: list changed opex months: %w", err)
	}
	months := make([]time.Time, 0, len(rows))
	for _, r := range rows {
		months = append(months, r.Month)
	}
	return months, nil
}

// GetOpexMonthFacts returns one month's costed OPEX totals grouped by category (amount_base NOT NULL;
// unconverted lines are excluded and surface as a builder caveat). Category is one of
// entity.ValidOpexCategories.
func (s *Store) GetOpexMonthFacts(ctx context.Context, month time.Time) ([]entity.AcctOpexCategorySum, error) {
	m := firstOfMonthUTC(month).Format(dateLayout)
	sums, err := storeutil.QueryListNamed[entity.AcctOpexCategorySum](ctx, s.DB, `
		SELECT category, COALESCE(SUM(amount_base), 0) AS amount_base
		FROM opex_line
		WHERE month = :m AND amount_base IS NOT NULL
		GROUP BY category
		ORDER BY category`, map[string]any{"m": m})
	if err != nil {
		return nil, fmt.Errorf("accounting: get opex month facts %s: %w", m, err)
	}

	uncosted, err := storeutil.QueryListNamed[struct {
		Category string `db:"category"`
		Label    string `db:"label"`
	}](ctx, s.DB, `
		SELECT category, label FROM opex_line
		WHERE month = :m AND amount_base IS NULL
		ORDER BY category, label`, map[string]any{"m": m})
	if err != nil {
		return nil, fmt.Errorf("accounting: get opex month uncosted labels %s: %w", m, err)
	}

	// Merge the uncosted labels into the costed sums: an existing category gets its labels appended;
	// a category with ONLY uncosted lines never made the GROUP BY above, so it gets a zero-amount
	// placeholder here purely so the builder's caveat still names it (entity.AcctOpexCategorySum
	// doc-comment).
	byCategory := make(map[string]int, len(sums))
	for i, cs := range sums {
		byCategory[cs.Category] = i
	}
	for _, u := range uncosted {
		if i, ok := byCategory[u.Category]; ok {
			sums[i].UncostedLabels = append(sums[i].UncostedLabels, u.Label)
			continue
		}
		byCategory[u.Category] = len(sums)
		sums = append(sums, entity.AcctOpexCategorySum{
			Category:       u.Category,
			AmountBase:     decimal.Zero,
			UncostedLabels: []string{u.Label},
		})
	}
	return sums, nil
}
