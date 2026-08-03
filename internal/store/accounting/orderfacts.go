package accounting

import (
	"context"
	"fmt"
	"strings"
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
	// Phase 2, wave 1: the buyer→shipping-address JOIN supplies the VAT destination (country_code with
	// a fallback to country, 07 §7.4.1) and buyer_vat_id / vat_regime are read from the order. LEFT
	// joins keep a buyer-less / address-less order resolvable (dest_country '' → export + caveat).
	facts, err := storeutil.QueryNamedOne[entity.AcctOrderFacts](ctx, s.DB, `
		SELECT co.id, co.uuid, co.placed, co.total_price, co.currency,
		       co.total_settled_base, co.payment_fee, co.vat_amount, co.vat_rate_pct,
		       co.buyer_vat_id, co.vat_regime, co.promo_discount_pct,
		       p.payment_method_id,
		       s.cost AS shipment_cost, s.free_shipping,
		       COALESCE(NULLIF(a.country_code, ''), a.country, '') AS dest_country
		FROM customer_order co
		JOIN payment p ON p.order_id = co.id
		LEFT JOIN shipment s ON s.order_id = co.id
		LEFT JOIN buyer b ON b.order_id = co.id
		LEFT JOIN address a ON a.id = b.shipping_address_id
		WHERE co.uuid = :uuid
		ORDER BY p.id, b.id, a.id
		LIMIT 1`, map[string]any{"uuid": orderUUID})
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
	// C-8: LEFT JOIN so a sold line is never silently dropped if its product row were hard-deleted.
	// The line keeps its sale-time cost snapshot (cost_price_at_sale) when present; only a legacy line
	// with no snapshot AND a missing product yields a NULL unit_cost, which the builder already treats
	// as uncosted (excluded from COGS, named in the entry caveat) rather than vanishing.
	items, err := storeutil.QueryListNamed[entity.AcctOrderItemFact](ctx, s.DB, `
		SELECT oi.id, oi.product_id, oi.quantity,
		       COALESCE(oi.cost_price_at_sale, pr.cost_price) AS unit_cost
		FROM order_item oi
		LEFT JOIN product pr ON pr.id = oi.product_id
		WHERE oi.order_id = :order_id`, map[string]any{"order_id": facts.Id})
	if err != nil {
		return nil, fmt.Errorf("accounting: get order item facts %s: %w", orderUUID, err)
	}
	facts.Items = items
	return &facts, nil
}

// GetVatRatesFor returns the standard VAT rate (percent) for each of the given ISO alpha-2 country
// codes present in vat_rate (phase 2, wave 1). Codes are upper-cased and non-2-letter ones dropped;
// absent countries are simply not in the map, letting the worker skip an order with a "vat rate
// missing" alert (07 §7.4.14) rather than post a zero rate. An empty input yields an empty map.
func (s *Store) GetVatRatesFor(ctx context.Context, codes []string) (map[string]decimal.Decimal, error) {
	out := make(map[string]decimal.Decimal, len(codes))
	seen := make(map[string]struct{}, len(codes))
	norm := make([]string, 0, len(codes))
	for _, c := range codes {
		c = strings.ToUpper(strings.TrimSpace(c))
		if len(c) != 2 {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		norm = append(norm, c)
	}
	if len(norm) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[struct {
		Code string          `db:"country_code"`
		Rate decimal.Decimal `db:"rate_pct"`
	}](ctx, s.DB, `SELECT country_code, rate_pct FROM vat_rate WHERE country_code IN (:codes)`,
		map[string]any{"codes": norm})
	if err != nil {
		return nil, fmt.Errorf("accounting: get vat rates %v: %w", norm, err)
	}
	for _, r := range rows {
		out[strings.ToUpper(strings.TrimSpace(r.Code))] = r.Rate
	}
	return out, nil
}

// SetOrderVatRegime snapshots the resolved VAT regime onto an order (customer_order.vat_regime). The
// worker calls it in the SAME tx as the order-sale entry (§1.3), so the regime and the posting commit
// together. Idempotent — re-running with the same regime is a no-op UPDATE.
func (s *Store) SetOrderVatRegime(ctx context.Context, orderUUID, regime string) error {
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE customer_order SET vat_regime = :regime WHERE uuid = :uuid`,
		map[string]any{"regime": regime, "uuid": orderUUID}); err != nil {
		return fmt.Errorf("accounting: set order vat regime %s: %w", orderUUID, err)
	}
	return nil
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

// ListUnpostedReceipts returns production receipts (Phase 4: the receipt is the accounting unit of
// a receive) received on/after startDate, not dead-lettered and with no reversal linkage, that
// have no live production_receive journal entry under EITHER key family — 'receipt:<id>' (current)
// or the legacy '<run_id>' (pre-0235 entries; belt-and-suspenders after the migration rewrote them).
// Oldest first, up to limit. Dead-lettered receipts are deliberately excluded: they already alerted,
// and re-scanning them every tick is exactly the queue-clogging the dead-letter state exists to
// stop — ClosePeriod still counts them, so the money cannot silently vanish.
//
// The status filter is <> 'dead_letter', NOT = 'pending': the live-entry NOT EXISTS is the real
// gate, exactly as on ClosePeriod and the recon block. A 'pending' filter would wedge the ledger
// the day an operator reverses a posted receipt's entry (a normal accounting operation): the
// receipt stays 'posted', the scan never re-sees it, yet ClosePeriod counts its missing live entry
// forever. With the entry-existence gate the worker re-posts the next version (':vN', exactly what
// loadProductionReceiveVersions exists for) and re-marks the receipt — self-healing, as before.
func (s *Store) ListUnpostedReceipts(ctx context.Context, startDate time.Time, limit int) ([]entity.AcctReceiptRef, error) {
	if limit <= 0 {
		limit = defaultScanBatch
	}
	rows, err := storeutil.QueryListNamed[entity.AcctReceiptRef](ctx, s.DB, `
		SELECT pr.id AS receipt_id, pr.run_id AS run_id FROM production_run_receipt pr
		WHERE pr.posting_status <> 'dead_letter' AND pr.reversal_of IS NULL AND pr.reversed_by IS NULL
		  AND pr.received_at >= :start_date
		  AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
		                  WHERE e.source_type = 'production_receive'
		                    AND (e.source_key = CONCAT('receipt', CHAR(58), CAST(pr.id AS CHAR CHARACTER SET utf8mb4)) COLLATE utf8mb4_unicode_ci
		                         OR e.source_key LIKE CONCAT('receipt', CHAR(58), CAST(pr.id AS CHAR CHARACTER SET utf8mb4), CHAR(58), 'v%') COLLATE utf8mb4_unicode_ci
		                         OR e.source_key = CAST(pr.run_id AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci
		                         OR e.source_key LIKE CONCAT(CAST(pr.run_id AS CHAR CHARACTER SET utf8mb4), CHAR(58), 'v%') COLLATE utf8mb4_unicode_ci)
		                    AND e.reversed_by IS NULL)
		ORDER BY pr.received_at, pr.id
		LIMIT :limit`, map[string]any{"start_date": startDate.UTC(), "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("accounting: list unposted receipts: %w", err)
	}
	return rows, nil
}

// GetReceiptFactsForPosting assembles the production-receive fact set for one receipt (P1): the
// receipt's own received_at and quantity totals, the RUN's manual cost articles and material
// issue/return movements, and the Phase 5 pro-rata basis — run-wide unit aggregates over
// non-reversed receipts, the plan total, and what sibling receipts' live entries already
// capitalised/relieved (posted_manual_base / posted_fg_base, written in the same tx as each entry).
// LEDGER_WIP (Σ costed issue_production − return_production, with the pre-cutover exclusion) is
// derived from Issues by the caller, which knows accounting.start_date.
func (s *Store) GetReceiptFactsForPosting(ctx context.Context, receiptID int) (*entity.AcctRunFacts, error) {
	hdr, err := storeutil.QueryNamedOne[struct {
		Id           int             `db:"id"`
		RunId        int             `db:"run_id"`
		ReceivedAt   time.Time       `db:"received_at"`
		Final        bool            `db:"final"`
		TechCardName string          `db:"tech_card_name"`
		GoodQty      int             `db:"good_qty"`
		DefectQty    int             `db:"defect_qty"`
		AllGood      int             `db:"all_good"`
		AllReceived  int             `db:"all_received"`
		PlannedTotal int             `db:"planned_total"`
		OtherManual  decimal.Decimal `db:"other_manual"`
		OtherFG      decimal.Decimal `db:"other_fg"`
	}](ctx, s.DB, `
		SELECT pr.id, pr.run_id, pr.received_at, pr.final, tc.name AS tech_card_name,
		       COALESCE((SELECT SUM(rl.good_qty) FROM production_run_receipt_line rl WHERE rl.receipt_id = pr.id), 0) AS good_qty,
		       COALESCE((SELECT SUM(rl.defect_qty) FROM production_run_receipt_line rl WHERE rl.receipt_id = pr.id), 0) AS defect_qty,
		       COALESCE((SELECT SUM(rl.good_qty) FROM production_run_receipt s
		                 JOIN production_run_receipt_line rl ON rl.receipt_id = s.id
		                 WHERE s.run_id = pr.run_id AND s.reversed_by IS NULL AND s.reversal_of IS NULL), 0) AS all_good,
		       COALESCE((SELECT SUM(rl.good_qty + rl.defect_qty) FROM production_run_receipt s
		                 JOIN production_run_receipt_line rl ON rl.receipt_id = s.id
		                 WHERE s.run_id = pr.run_id AND s.reversed_by IS NULL AND s.reversal_of IS NULL), 0) AS all_received,
		       COALESCE((SELECT SUM(l.planned_qty) FROM production_run_line l WHERE l.run_id = pr.run_id), 0) AS planned_total,
		       COALESCE((SELECT SUM(s.posted_manual_base) FROM production_run_receipt s
		                 WHERE s.run_id = pr.run_id AND s.id <> pr.id AND s.reversed_by IS NULL), 0) AS other_manual,
		       COALESCE((SELECT SUM(s.posted_fg_base) FROM production_run_receipt s
		                 WHERE s.run_id = pr.run_id AND s.id <> pr.id AND s.reversed_by IS NULL), 0) AS other_fg
		FROM production_run_receipt pr
		JOIN production_run r ON r.id = pr.run_id
		JOIN tech_card tc ON tc.id = r.tech_card_id
		WHERE pr.id = :id`, map[string]any{"id": receiptID})
	if err != nil {
		return nil, fmt.Errorf("accounting: get receipt header %d: %w", receiptID, err)
	}
	costs, err := storeutil.QueryListNamed[entity.ProductionRunCost](ctx, s.DB, `
		SELECT id, run_id, kind, description, amount, currency, amount_base, incurred_at
		FROM production_run_cost WHERE run_id = :id ORDER BY id`, map[string]any{"id": hdr.RunId})
	if err != nil {
		return nil, fmt.Errorf("accounting: get run costs %d: %w", hdr.RunId, err)
	}
	issues, err := storeutil.QueryListNamed[entity.AcctRunIssueFact](ctx, s.DB, `
		SELECT movement_type, quantity, unit_cost_base, created_at
		FROM material_stock_movement
		WHERE production_run_id = :id
		  AND movement_type IN ('issue_production','return_production')
		ORDER BY id`, map[string]any{"id": hdr.RunId})
	if err != nil {
		return nil, fmt.Errorf("accounting: get run issues %d: %w", hdr.RunId, err)
	}
	rf := &entity.AcctRunFacts{
		RunID:                 hdr.RunId,
		ReceivedAt:            hdr.ReceivedAt,
		TechCardName:          hdr.TechCardName,
		Costs:                 costs,
		Issues:                issues,
		ReceiptID:             hdr.Id,
		GoodQtyTotal:          hdr.GoodQty,
		DefectQtyTotal:        hdr.DefectQty,
		IsFinal:               hdr.Final,
		AllGoodQty:            hdr.AllGood,
		AllReceivedQty:        hdr.AllReceived,
		PlannedQtyTotal:       hdr.PlannedTotal,
		OtherPostedManualBase: hdr.OtherManual,
		OtherPostedFGBase:     hdr.OtherFG,
	}
	return rf, nil
}

// MarkReceiptPosted stamps a receipt's posting_status='posted' together with what its live entry
// actually capitalised (Cr 2010) and relieved (Dr 1130) — the sibling aggregates Phase 5's
// pro-rata/true-up arithmetic reads. Called in the same transaction as the journal-entry insert so
// "entry exists", "receipt posted" and the amounts are one fact.
func (s *Store) MarkReceiptPosted(ctx context.Context, receiptID int, manualBase, fgBase decimal.Decimal) error {
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE production_run_receipt SET posting_status = 'posted', last_posting_error = NULL,
			posted_manual_base = :manual_base, posted_fg_base = :fg_base
		WHERE id = :id`, map[string]any{
		"id": receiptID, "manual_base": manualBase, "fg_base": fgBase,
	}); err != nil {
		return fmt.Errorf("accounting: mark receipt %d posted: %w", receiptID, err)
	}
	return nil
}

// MarkReceiptPostedFromEntry marks a receipt posted with amounts RECOVERED from an existing live
// entry's lines — the worker's raced path (an entry exists but the receipt row was never marked,
// e.g. the mark crashed after the entry committed in an older binary). One statement so the
// recovery is atomic with the mark.
func (s *Store) MarkReceiptPostedFromEntry(ctx context.Context, receiptID, entryID int) error {
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE production_run_receipt SET posting_status = 'posted', last_posting_error = NULL,
			posted_manual_base = COALESCE((SELECT SUM(l.amount) FROM acct_journal_line l
			                               JOIN acct_account a ON a.id = l.account_id
			                               WHERE l.entry_id = :entry_id AND a.code = '2010' AND l.side = 'credit'), 0),
			posted_fg_base = COALESCE((SELECT SUM(l.amount) FROM acct_journal_line l
			                           JOIN acct_account a ON a.id = l.account_id
			                           WHERE l.entry_id = :entry_id AND a.code = '1130' AND l.side = 'debit'), 0)
		WHERE id = :id`, map[string]any{"id": receiptID, "entry_id": entryID}); err != nil {
		return fmt.Errorf("accounting: mark receipt %d posted from entry %d: %w", receiptID, entryID, err)
	}
	return nil
}

// RecordReceiptPostingFailure increments the receipt's attempt counter, stores the error text, and
// dead-letters it once attempts reach maxAttempts. Runs on the pool (NOT in the failed tx — that
// rolled back). The error text is bounded to the column width.
func (s *Store) RecordReceiptPostingFailure(ctx context.Context, receiptID int, errMsg string, maxAttempts int) (bool, error) {
	msg := []rune(errMsg)
	if len(msg) > 512 {
		msg = msg[:512]
	}
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE production_run_receipt SET
			posting_attempts = posting_attempts + 1,
			last_posting_error = :msg,
			posting_status = IF(posting_attempts >= :max_attempts, 'dead_letter', posting_status)
		WHERE id = :id AND posting_status = 'pending'`,
		map[string]any{"id": receiptID, "msg": string(msg), "max_attempts": maxAttempts}); err != nil {
		return false, fmt.Errorf("accounting: record receipt %d posting failure: %w", receiptID, err)
	}
	row, err := storeutil.QueryNamedOne[struct {
		Status string `db:"posting_status"`
	}](ctx, s.DB, `SELECT posting_status FROM production_run_receipt WHERE id = :id`,
		map[string]any{"id": receiptID})
	if err != nil {
		return false, fmt.Errorf("accounting: read receipt %d posting status: %w", receiptID, err)
	}
	return row.Status == entity.ReceiptPostingDeadLetter, nil
}

// CountReceiptPostingBacklog reports how many pending receipts received in [startDate, olderThan)
// exist (work the worker should long have drained) and how many receipts are dead-lettered. The
// startDate bound matters: 0231 backfills a 'pending' receipt for every pre-cutover legacy receive,
// and the scan (bounded by the same startDate) is designed never to touch those — counting them
// would WARN about a backlog no one can drain, every tick, forever.
func (s *Store) CountReceiptPostingBacklog(ctx context.Context, startDate, olderThan time.Time) (int, int, error) {
	pending, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM production_run_receipt
		WHERE posting_status = 'pending' AND reversal_of IS NULL AND reversed_by IS NULL
		  AND received_at >= :start_date AND received_at < :older_than`,
		map[string]any{"start_date": startDate.UTC(), "older_than": olderThan.UTC()})
	if err != nil {
		return 0, 0, fmt.Errorf("accounting: count pending receipts: %w", err)
	}
	dead, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM production_run_receipt WHERE posting_status = 'dead_letter'`, map[string]any{})
	if err != nil {
		return 0, 0, fmt.Errorf("accounting: count dead-lettered receipts: %w", err)
	}
	return pending, dead, nil
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
		SELECT category, COALESCE(SUM(amount_base), 0) AS amount_base,
		       COALESCE(SUM(vat_amount_base), 0) AS vat_base
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

// ListChangedShipmentsForActualCost returns shipments whose actual carrier cost was set/changed after
// afterTS (the shipping_actual checkpoint) and that carry a cost value (actual_cost OR
// return_shipping_cost NOT NULL), oldest change first (phase 2, wave 3, feature 3.1). The worker reposts
// each per shipment.updated_at (mutable source, like opex) and clamps its checkpoint before the scan;
// pre-cutover shipments are filtered by the worker on occurred_at, not here. Reading on the pool (not
// inside a Tx) obeys the lock rule (07). A shipment whose cost is later CLEARED to NULL drops out of this
// scan; the residual is surfaced by the shipping reconciliation block, not auto-reversed.
func (s *Store) ListChangedShipmentsForActualCost(ctx context.Context, afterTS, startDate time.Time) ([]entity.AcctShipmentCostFacts, error) {
	rows, err := storeutil.QueryListNamed[entity.AcctShipmentCostFacts](ctx, s.DB, `
		SELECT sh.id AS shipment_id, co.uuid AS order_uuid,
		       sh.actual_cost, sh.return_shipping_cost, sh.shipping_date, sh.updated_at
		FROM shipment sh
		JOIN customer_order co ON co.id = sh.order_id
		WHERE sh.updated_at > :after_ts
		  AND (sh.actual_cost IS NOT NULL OR sh.return_shipping_cost IS NOT NULL)
		ORDER BY sh.updated_at, sh.id`,
		map[string]any{"after_ts": afterTS.UTC()})
	if err != nil {
		return nil, fmt.Errorf("accounting: list changed shipments for actual cost: %w", err)
	}
	return rows, nil
}

// ListDevExpensesForPosting returns every tech_card_dev_expense row created on/after startDate (the
// cutover), joined with its tech card's name (phase 2, wave 3, feature 3.2). tech_card_dev_expense has
// no updated_at column and a DeleteTechCardDevExpense RPC exists, so the worker reconciles the FULL set
// each tick (dev expenses are few, like production runs): it posts new costed rows, reposts changed
// amounts, and reverses rows that vanished (deleted) or lost their costing. Uncosted rows (amount_base
// NULL) are returned too so the worker can skip them with a caveat.
func (s *Store) ListDevExpensesForPosting(ctx context.Context, startDate time.Time) ([]entity.AcctDevExpenseFacts, error) {
	rows, err := storeutil.QueryListNamed[entity.AcctDevExpenseFacts](ctx, s.DB, `
		SELECT de.id, de.tech_card_id, tc.name AS tech_card_name, de.kind,
		       de.description, de.amount_base, de.incurred_at, de.created_at
		FROM tech_card_dev_expense de
		JOIN tech_card tc ON tc.id = de.tech_card_id
		WHERE de.created_at >= :start_date
		ORDER BY de.id`,
		map[string]any{"start_date": startDate.UTC()})
	if err != nil {
		return nil, fmt.Errorf("accounting: list dev expenses for posting: %w", err)
	}
	return rows, nil
}
