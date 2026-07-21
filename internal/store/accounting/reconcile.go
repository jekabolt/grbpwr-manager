package accounting

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// Step 7 reconciliation (docs/plan-accounting/06-reports.md §"Сверка"). The ledger is a DERIVED
// projection; reconciliation proves it did not drift from the operational source tables it is
// derived from, and surfaces what is deliberately (or accidentally) unposted. Each block reports a
// ledger figure, the operational figure it should mirror, their delta and a bounded item sample.
// This is both an admin report and the source for the acctposting worker's health alerts (07).
//
// The block reads other domains' tables directly (customer_order, order_item, product,
// material_stock_movement, production_run, opex_line) — the internal/store/metrics precedent. The
// order-status set comes from the shared cache (cache.OrderStatusIDsForNetRevenue, the same
// confirmed/shipped/delivered/partially_refunded set metrics uses); when that cache is empty (only
// in a cache-less unit harness — production always loads it in app.Start) the operational figures
// that depend on it are reported as zero rather than erroring on an empty IN () list.

// reconTopN bounds the item sample each reconciliation block returns (the block's TotalCount carries
// the full count).
const reconTopN = 20

// GetReconciliation assembles the per-dimension reconciliation blocks over [from, to). Ledger
// figures are read from the journal; operational figures from the source tables. See the block
// helpers below for each dimension's rule.
func (s *Store) GetReconciliation(ctx context.Context, from, to time.Time) (*entity.AcctReconciliation, error) {
	fromStr := from.UTC().Format(dateLayout)
	toStr := to.UTC().Format(dateLayout)
	fromT := from.UTC()
	toT := to.UTC()
	statusIDs := cache.OrderStatusIDsForNetRevenue()

	rec := &entity.AcctReconciliation{From: from, To: to}

	var err error
	if rec.Revenue, err = s.reconRevenue(ctx, fromStr, toStr, fromT, toT, statusIDs); err != nil {
		return nil, err
	}
	if rec.Fees, err = s.reconFees(ctx, fromStr, toStr, fromT, toT, statusIDs); err != nil {
		return nil, err
	}
	if rec.COGS, err = s.reconCOGS(ctx, fromStr, toStr, fromT, toT, statusIDs); err != nil {
		return nil, err
	}
	if rec.Materials, err = s.reconMaterials(ctx, fromT, toT); err != nil {
		return nil, err
	}
	if rec.FinishedGoods, err = s.reconFinishedGoods(ctx, from, to, statusIDs); err != nil {
		return nil, err
	}
	if rec.Pending, err = s.reconPending(ctx, fromStr, toStr, fromT, toT); err != nil {
		return nil, err
	}
	if rec.UnpostedMovements, err = s.reconUnpostedMovements(ctx, fromT, toT); err != nil {
		return nil, err
	}
	vat, err := s.reconVat(ctx, fromStr, toStr, fromT, toT, statusIDs)
	if err != nil {
		return nil, err
	}
	rec.Vat = &vat
	prepayments, err := s.reconPrepayments(ctx, fromT, toT)
	if err != nil {
		return nil, err
	}
	rec.Prepayments = &prepayments
	shipping, err := s.reconShipping(ctx, fromStr, toStr, fromT, toT)
	if err != nil {
		return nil, err
	}
	rec.Shipping = &shipping
	bank, err := s.reconBank(ctx, fromT, toT)
	if err != nil {
		return nil, err
	}
	rec.Bank = &bank
	return rec, nil
}

// reconBank: the 1010 Cash-Bank ledger balance as of the period end vs the Revolut inbox that feeds it
// (phase 2, wave 4 — §4.1). There is no external base-currency bank-balance feed (the inbox is multi-
// currency and posts via the FX-fold mechanic), so this block does not assert a delta; instead it surfaces
// the ACTIONABLE backlog — statement lines booked in the period that are still unmatched (awaiting a
// post/ignore decision) — so 1010 is not trusted while lines remain unposted. Ledger/Operational carry the
// 1010 balance; Items sample the unposted lines with their signed amounts (own currency, informational).
func (s *Store) reconBank(ctx context.Context, fromT, toT time.Time) (entity.AcctReconBlock, error) {
	ledger, err := s.accountBalanceBefore(ctx, "1010", toT)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "bank", Ledger: ledger, Operational: ledger}

	rows, err := storeutil.QueryListNamed[struct {
		Ref      string          `db:"ref"`
		Currency string          `db:"currency"`
		Amount   decimal.Decimal `db:"amount"`
	}](ctx, s.DB, `
		SELECT external_id AS ref, currency, amount
		FROM acct_bank_txn
		WHERE state = 'unmatched' AND booked_at >= :from AND booked_at < :to
		ORDER BY booked_at
		LIMIT :topN`,
		map[string]any{"from": fromT, "to": toT, "topN": reconTopN})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon bank unmatched: %w", err)
	}
	for _, r := range rows {
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    r.Ref,
			Label:  "unposted bank line (" + r.Currency + "); post or ignore to reconcile 1010",
			Amount: r.Amount,
		})
	}
	block.TotalCount, err = storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM acct_bank_txn
		WHERE state = 'unmatched' AND booked_at >= :from AND booked_at < :to`,
		map[string]any{"from": fromT, "to": toT})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon bank unmatched count: %w", err)
	}
	return block, nil
}

// --- shared ledger helpers ---

// ledgerLineSum sums acct_journal_line.amount over lines on the given account codes and side within
// [from, to), optionally restricted to a source_type. Used for turnover-style ledger figures
// (revenue NET+SHIP credits, 6050 fee debits, 5010 COGS debits).
func (s *Store) ledgerLineSum(ctx context.Context, codes []string, side entity.AcctSide, sourceType, fromStr, toStr string) (decimal.Decimal, error) {
	conds := []string{"a.code IN (:codes)", "l.side = :side", "e.occurred_at >= :from", "e.occurred_at < :to"}
	params := map[string]any{"codes": codes, "side": string(side), "from": fromStr, "to": toStr}
	if sourceType != "" {
		conds = append(conds, "e.source_type = :st")
		params["st"] = sourceType
	}
	row, err := storeutil.QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(l.amount), 0) AS v
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE `+strings.Join(conds, " AND "), params)
	if err != nil {
		return decimal.Zero, fmt.Errorf("accounting: ledger line sum %v/%s: %w", codes, side, err)
	}
	return row.V, nil
}

// accountBalanceBefore is one account's signed cumulative balance for lines whose entry occurred
// strictly before `before` (the period end for a period-scoped reconciliation). The LEFT joins keep
// the single account row (0/0) even with no lines; the e.id gate applies the date filter.
func (s *Store) accountBalanceBefore(ctx context.Context, code string, before time.Time) (decimal.Decimal, error) {
	row, err := storeutil.QueryNamedOne[struct {
		Section entity.AcctSection `db:"section"`
		Debit   decimal.Decimal    `db:"dr"`
		Credit  decimal.Decimal    `db:"cr"`
	}](ctx, s.DB, `
		SELECT a.section,
		       COALESCE(SUM(CASE WHEN e.id IS NOT NULL AND l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS dr,
		       COALESCE(SUM(CASE WHEN e.id IS NOT NULL AND l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS cr
		FROM acct_account a
		LEFT JOIN acct_journal_line l ON l.account_id = a.id
		LEFT JOIN acct_journal_entry e ON e.id = l.entry_id AND e.occurred_at < :before
		WHERE a.code = :code
		GROUP BY a.id, a.section`,
		map[string]any{"code": code, "before": before.UTC().Format(dateLayout)})
	if err != nil {
		return decimal.Zero, fmt.Errorf("accounting: account balance %s: %w", code, err)
	}
	return sectionBalance(row.Section, row.Debit, row.Credit), nil
}

// --- blocks ---

// reconRevenue: ledger NET+SHIP credited by order_sale entries vs the recognised revenue on the
// period's confirmed orders (G − VAT share, where G = total_settled_base, or total_price for a
// non-Stripe EUR order). Items are confirmed orders in the period with no order_sale entry, labelled
// with the outbox last_error (the skip / stuck reason).
//
// C-4: the operational side subtracts the sale-time vat_amount SNAPSHOT (co.vat_amount, the 0094
// address-based backfill), while the ledger NET reflects the RESOLVED REGIME VAT (phase-2 wave 1). For
// an order whose regime differs from that snapshot — cash / uk_stock_domestic (20% vs a 23% address
// snapshot), or wdt / export (0% vs a non-zero snapshot) — a small delta here is EXPECTED and advisory,
// NOT a ledger error, and this block is not a hard alert. (A fully drift-free recon would recompute the
// operational VAT from vat_regime + vat_rate, which re-derives the posting; deferred.)
//
// WAVE-2 (extends C-4): the ledger credit now also counts order_delivered_sale — new-chain revenue is
// recognised at DELIVERY, not payment. The operational side still counts every confirmed order's
// revenue at placement (this pass does not rebase recognition timing), so a new-chain order that is
// paid-but-not-yet-delivered contributes to Operational while its revenue has not yet reached the
// ledger — an EXPECTED timing drift, not a ledger error. Its outstanding amount is proved by the
// prepayments block (2090). Advisory, not a hard alert.
func (s *Store) reconRevenue(ctx context.Context, fromStr, toStr string, fromT, toT time.Time, statusIDs []int) (entity.AcctReconBlock, error) {
	// 4310 (B2B / wholesale revenue) belongs here: a B2B sale credits net revenue to 4310, and the
	// operational side below counts every confirmed order's recognised revenue regardless of channel —
	// omitting 4310 makes every B2B order a false negative drift. (4050 B2B trade-discount contra is a
	// debit and is added once discount postings exist.)
	revAccts := []string{"4010", "4020", "4110", "4310"}
	// Ledger revenue credits from BOTH chains: legacy order_sale (all revenue at payment) plus the wave-2
	// order_delivered_sale (NET + SHIP recognised at delivery). order_prepayment credits no revenue
	// account (it parks the net on 2090), so it is deliberately absent here.
	saleRev, err := s.ledgerLineSum(ctx, revAccts, entity.AcctSideCredit, string(entity.AcctSourceOrderSale), fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	deliveredRev, err := s.ledgerLineSum(ctx, revAccts, entity.AcctSideCredit, string(entity.AcctSourceOrderDeliveredSale), fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	// 4030 Discounts is a contra-revenue DEBIT (phase 2, wave 3): a promo order credits the full-price
	// revenue and debits the reconstructed discount to 4030, so net ledger revenue = credits − 4030. The
	// operational side counts revenue net of the discount (G already reflects the discounted price), so
	// subtracting 4030 keeps the two sides comparable. Source-agnostic: both order_sale and
	// order_delivered_sale can carry the split (mirrors the "added once discount postings exist" note).
	discountDr, err := s.ledgerLineSum(ctx, []string{"4030"}, entity.AcctSideDebit, "", fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "revenue", Ledger: saleRev.Add(deliveredRev).Sub(discountDr)}
	if len(statusIDs) == 0 {
		block.Delta = block.Ledger.Sub(block.Operational)
		return block, nil
	}

	op, err := storeutil.QueryNamedOne[struct {
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
		map[string]any{"statusIds": statusIDs, "from": fromT, "to": toT, "base": cache.GetBaseCurrency()})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon revenue operational: %w", err)
	}
	block.Operational = op.V.Round(2)
	block.Delta = block.Ledger.Sub(block.Operational)

	// Wave-2 advisory: new-chain orders paid-but-not-yet-delivered recognise revenue at delivery
	// (order_delivered_sale), so their revenue is in Operational (counted at placement) but not yet in
	// Ledger — an expected timing drift, reconciled by the prepayments block, not a hard alert.
	block.Items = append(block.Items, entity.AcctReconItem{
		Ref:    "note",
		Label:  "new-chain orders paid but not yet delivered recognise revenue at delivery; outstanding amount reconciled by the prepayments block",
		Amount: decimal.Zero,
	})

	missing, err := storeutil.QueryListNamed[struct {
		Ref    string          `db:"ref"`
		Reason sql.NullString  `db:"reason"`
		Amount decimal.Decimal `db:"amount"`
	}](ctx, s.DB, `
		SELECT co.uuid AS ref, ev.last_error AS reason,
		       COALESCE(co.total_settled_base, co.total_price) AS amount
		FROM customer_order co
		LEFT JOIN acct_event ev ON ev.event_type = 'order_paid' AND ev.source_key = CAST(co.uuid AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci
		WHERE co.order_status_id IN (:statusIds)
		  AND co.placed >= :from AND co.placed < :to
		  AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
		                  WHERE e.source_type IN ('order_sale','order_prepayment') AND e.source_key = CAST(co.uuid AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci)
		ORDER BY co.placed
		LIMIT :topN`,
		map[string]any{"statusIds": statusIDs, "from": fromT, "to": toT, "topN": reconTopN})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon revenue unposted orders: %w", err)
	}
	for _, m := range missing {
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    m.Ref,
			Label:  reasonOrDefault(m.Reason, "no order_paid event or awaiting posting"),
			Amount: m.Amount,
		})
	}
	block.TotalCount, err = storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM customer_order co
		WHERE co.order_status_id IN (:statusIds)
		  AND co.placed >= :from AND co.placed < :to
		  AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
		                  WHERE e.source_type IN ('order_sale','order_prepayment') AND e.source_key = CAST(co.uuid AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci)`,
		map[string]any{"statusIds": statusIDs, "from": fromT, "to": toT})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon revenue unposted count: %w", err)
	}
	return block, nil
}

// reconFees: ledger 6050 acquirer-fee debits vs Σ payment_fee on the period's confirmed orders.
func (s *Store) reconFees(ctx context.Context, fromStr, toStr string, fromT, toT time.Time, statusIDs []int) (entity.AcctReconBlock, error) {
	ledger, err := s.ledgerLineSum(ctx, []string{"6050"}, entity.AcctSideDebit, "", fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "fees", Ledger: ledger}
	if len(statusIDs) > 0 {
		op, err := storeutil.QueryNamedOne[struct {
			V decimal.Decimal `db:"v"`
		}](ctx, s.DB, `
			SELECT COALESCE(SUM(co.payment_fee), 0) AS v
			FROM customer_order co
			WHERE co.order_status_id IN (:statusIds)
			  AND co.placed >= :from AND co.placed < :to`,
			map[string]any{"statusIds": statusIDs, "from": fromT, "to": toT})
		if err != nil {
			return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon fees operational: %w", err)
		}
		block.Operational = op.V.Round(2)
	}
	block.Delta = block.Ledger.Sub(block.Operational)
	return block, nil
}

// reconCOGS: ledger 5010 COGS debits vs Σ COALESCE(cost_price_at_sale, product.cost_price) × qty
// over the period's confirmed orders (the same snapshot-first cost fallback as metrics/margin.go and
// the sale builder).
//
// WAVE-2: the ledger sum is source-agnostic (sourceType ""), so it ALREADY recognises the delivered
// chain — the only sources debiting 5010 are order_sale (COGS at payment) and order_delivered_sale
// (COGS at delivery). As with revenue, the operational side still counts COGS at placement, so a
// new-chain order paid-but-not-yet-delivered is an expected timing drift here (advisory), not an error.
func (s *Store) reconCOGS(ctx context.Context, fromStr, toStr string, fromT, toT time.Time, statusIDs []int) (entity.AcctReconBlock, error) {
	ledger, err := s.ledgerLineSum(ctx, []string{"5010"}, entity.AcctSideDebit, "", fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "cogs", Ledger: ledger}
	if len(statusIDs) > 0 {
		op, err := storeutil.QueryNamedOne[struct {
			V decimal.Decimal `db:"v"`
		}](ctx, s.DB, `
			SELECT COALESCE(SUM(COALESCE(oi.cost_price_at_sale, pr.cost_price) * oi.quantity), 0) AS v
			FROM order_item oi
			JOIN customer_order co ON co.id = oi.order_id
			JOIN product pr ON pr.id = oi.product_id
			WHERE co.order_status_id IN (:statusIds)
			  AND co.placed >= :from AND co.placed < :to`,
			map[string]any{"statusIds": statusIDs, "from": fromT, "to": toT})
		if err != nil {
			return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon cogs operational: %w", err)
		}
		block.Operational = op.V.Round(2)
	}
	block.Delta = block.Ledger.Sub(block.Operational)
	return block, nil
}

// reconMaterials: ledger 1110 Materials balance (as of the period end) vs the net signed value of
// costed material movements to date (the M1–M8 effect on 1110), plus a count of uncosted movements
// in the period the posting rules skipped. A standing delta is normal — the ledger only holds
// post-cutover, costed movements, while the direct sum spans all history.
func (s *Store) reconMaterials(ctx context.Context, fromT, toT time.Time) (entity.AcctReconBlock, error) {
	ledger, err := s.accountBalanceBefore(ctx, "1110", toT)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "materials", Ledger: ledger}

	// Net signed value of costed movements that touch 1110 (movement-type literals mirror the
	// entity.MaterialMovement* constants and the M1–M8 rules in 04-posting-rules.md).
	op, err := storeutil.QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(
			CASE m.movement_type
				WHEN 'receipt'            THEN  m.quantity * m.unit_cost_base
				WHEN 'receipt_production' THEN  m.quantity * m.unit_cost_base
				WHEN 'issue_production'   THEN -m.quantity * m.unit_cost_base
				WHEN 'issue_sample'       THEN -m.quantity * m.unit_cost_base
				WHEN 'return_production'  THEN  m.quantity * m.unit_cost_base
				WHEN 'return_sample'      THEN  m.quantity * m.unit_cost_base
				WHEN 'writeoff'           THEN -m.quantity * m.unit_cost_base
				WHEN 'adjustment'         THEN CASE WHEN m.on_hand_after >= m.on_hand_before
				                                    THEN  m.quantity * m.unit_cost_base
				                                    ELSE -m.quantity * m.unit_cost_base END
				ELSE 0
			END), 0) AS v
		FROM material_stock_movement m
		WHERE m.unit_cost_base IS NOT NULL AND m.created_at < :to`,
		map[string]any{"to": toT})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon materials operational: %w", err)
	}
	block.Operational = op.V.Round(2)
	block.Delta = block.Ledger.Sub(block.Operational)

	block.TotalCount, err = storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM material_stock_movement m
		WHERE m.unit_cost_base IS NULL AND m.created_at >= :from AND m.created_at < :to`,
		map[string]any{"from": fromT, "to": toT})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon materials uncosted count: %w", err)
	}
	if block.TotalCount > 0 {
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    "uncosted",
			Label:  fmt.Sprintf("%d uncosted material movement(s) in the period (skipped by posting)", block.TotalCount),
			Amount: decimal.Zero,
		})
	}
	return block, nil
}

// reconFinishedGoods: ledger 1130 Finished Goods balance vs the finished-goods stock valuation
// (repo.Metrics().GetInventoryValuation(...).TotalStockValue). Drift here is STANDARD, not an error:
// product.cost_price mutates after sale, manual stock adjustments bypass the ledger, and the ledger
// carries only post-cutover receives — so the delta is informational.
func (s *Store) reconFinishedGoods(ctx context.Context, from, to time.Time, statusIDs []int) (entity.AcctReconBlock, error) {
	ledger, err := s.accountBalanceBefore(ctx, "1130", to)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "finished_goods", Ledger: ledger}
	// GetInventoryValuation's dead-stock CTE filters on the order-status cache; skip it (operational
	// stays zero) when that cache is empty, so a cache-less harness does not fail on an empty IN ().
	if len(statusIDs) > 0 {
		val, err := s.repo.Metrics().GetInventoryValuation(ctx, from, to, 5)
		if err != nil {
			return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon finished goods valuation: %w", err)
		}
		block.Operational = val.TotalStockValue
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    "note",
			Label:  "drift is expected: cost_price mutates post-sale, manual stock adjustments and pre-cutover receives are not in the ledger",
			Amount: decimal.Zero,
		})
	}
	block.Delta = block.Ledger.Sub(block.Operational)
	return block, nil
}

// reconPending surfaces what is deliberately or accidentally unposted: pending outbox events (with
// their last_error reason), production runs received-but-unposted, and opex months with costed lines
// but no live opex_month entry. Ledger/Operational are zero (this block counts work, not money);
// TotalCount is the full backlog, Items a bounded sample.
func (s *Store) reconPending(ctx context.Context, fromStr, toStr string, fromT, toT time.Time) (entity.AcctReconBlock, error) {
	block := entity.AcctReconBlock{Name: "pending"}

	events, err := storeutil.QueryListNamed[struct {
		Ref       string         `db:"ref"`
		EventType string         `db:"event_type"`
		LastError sql.NullString `db:"last_error"`
	}](ctx, s.DB, `
		SELECT source_key AS ref, event_type, last_error
		FROM acct_event
		WHERE processed_at IS NULL AND occurred_at >= :from AND occurred_at < :to
		ORDER BY occurred_at, id
		LIMIT :topN`,
		map[string]any{"from": fromT, "to": toT, "topN": reconTopN})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon pending events: %w", err)
	}
	for _, ev := range events {
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    ev.Ref,
			Label:  ev.EventType + ": " + reasonOrDefault(ev.LastError, "pending, not yet processed"),
			Amount: decimal.Zero,
		})
	}
	eventCount, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM acct_event
		WHERE processed_at IS NULL AND occurred_at >= :from AND occurred_at < :to`,
		map[string]any{"from": fromT, "to": toT})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon pending event count: %w", err)
	}

	runs, err := storeutil.QueryListNamed[struct {
		Id int `db:"id"`
	}](ctx, s.DB, `
		SELECT r.id FROM production_run r
		WHERE r.received_at >= :from AND r.received_at < :to
		  AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
		                  WHERE e.source_type = 'production_receive' AND e.source_key = CAST(r.id AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci)
		ORDER BY r.received_at, r.id
		LIMIT :topN`,
		map[string]any{"from": fromT, "to": toT, "topN": reconTopN})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon pending runs: %w", err)
	}
	for _, r := range runs {
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    strconv.Itoa(r.Id),
			Label:  "production run received but not posted",
			Amount: decimal.Zero,
		})
	}
	runCount, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM production_run r
		WHERE r.received_at >= :from AND r.received_at < :to
		  AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
		                  WHERE e.source_type = 'production_receive' AND e.source_key = CAST(r.id AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci)`,
		map[string]any{"from": fromT, "to": toT})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon pending run count: %w", err)
	}

	months, err := storeutil.QueryListNamed[struct {
		Month time.Time `db:"month"`
	}](ctx, s.DB, `
		SELECT DISTINCT ol.month AS month
		FROM opex_line ol
		WHERE ol.amount_base IS NOT NULL AND ol.month >= :from AND ol.month < :to
		  AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
		                  WHERE e.source_type = 'opex_month'
		                    AND e.source_key LIKE CONCAT(DATE_FORMAT(ol.month, '%Y-%m'), '%')
		                    AND e.reversed_by IS NULL)
		ORDER BY month
		LIMIT :topN`,
		map[string]any{"from": fromStr, "to": toStr, "topN": reconTopN})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon pending opex months: %w", err)
	}
	for _, m := range months {
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    m.Month.Format("2006-01"),
			Label:  "opex month has costed lines but no posted entry",
			Amount: decimal.Zero,
		})
	}
	// opex month backlog is small; the sampled slice is the full count (LIMIT reconTopN is generous).
	block.TotalCount = eventCount + runCount + len(months)
	return block, nil
}

// reconUnpostedMovements lists uncosted material movements in the period (unit_cost_base NULL — the
// M1–M8 rules skip these and the value never reaches the ledger). Items carry the material name,
// movement type and quantity; TotalCount is the full count.
func (s *Store) reconUnpostedMovements(ctx context.Context, fromT, toT time.Time) (entity.AcctReconBlock, error) {
	block := entity.AcctReconBlock{Name: "unposted_movements"}

	rows, err := storeutil.QueryListNamed[struct {
		Id           int64           `db:"id"`
		MaterialName string          `db:"material_name"`
		MovementType string          `db:"movement_type"`
		Quantity     decimal.Decimal `db:"quantity"`
	}](ctx, s.DB, `
		SELECT m.id AS id, mat.name AS material_name, m.movement_type AS movement_type, m.quantity AS quantity
		FROM material_stock_movement m
		JOIN material mat ON mat.id = m.material_id
		WHERE m.unit_cost_base IS NULL AND m.created_at >= :from AND m.created_at < :to
		ORDER BY m.id
		LIMIT :topN`,
		map[string]any{"from": fromT, "to": toT, "topN": reconTopN})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon unposted movements: %w", err)
	}
	for _, r := range rows {
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    strconv.FormatInt(r.Id, 10),
			Label:  r.MaterialName + " (" + r.MovementType + ")",
			Amount: r.Quantity,
		})
	}
	block.TotalCount, err = storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM material_stock_movement m
		WHERE m.unit_cost_base IS NULL AND m.created_at >= :from AND m.created_at < :to`,
		map[string]any{"from": fromT, "to": toT})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon unposted movements count: %w", err)
	}
	return block, nil
}

// reconVat: ledger 2070 output VAT credited by order entries vs the VAT the period's confirmed orders
// imply from their sale-time snapshot (EUR-scaled the same way reconRevenue scales revenue). The
// posting now uses the RESOLVED REGIME rate, so a standing delta versus the snapshot is expected where
// regime and snapshot rate diverge (phase 2, wave 1) — informational, not an error. Items sample the
// order entries whose builder flagged a "vat snapshot mismatch".
//
// WAVE-2: post-cutover VAT is credited on order_prepayment (at payment), NOT order_sale, so the ledger
// sum reads BOTH sources — mirroring vatreturn.go's 2070 output list and avoiding false drift. The VAT
// tax point stays at payment, so the operational side (VAT the period's orders imply at payment) is
// unchanged; order_delivered_sale never touches VAT and is deliberately absent.
func (s *Store) reconVat(ctx context.Context, fromStr, toStr string, fromT, toT time.Time, statusIDs []int) (entity.AcctReconBlock, error) {
	saleVat, err := s.ledgerLineSum(ctx, []string{"2070"}, entity.AcctSideCredit, string(entity.AcctSourceOrderSale), fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	prepayVat, err := s.ledgerLineSum(ctx, []string{"2070"}, entity.AcctSideCredit, string(entity.AcctSourceOrderPrepayment), fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "vat", Ledger: saleVat.Add(prepayVat)}
	if len(statusIDs) > 0 {
		op, err := storeutil.QueryNamedOne[struct {
			V decimal.Decimal `db:"v"`
		}](ctx, s.DB, `
			SELECT COALESCE(SUM(
				CASE WHEN co.total_price > 0 THEN
					COALESCE(co.vat_amount, 0) * (COALESCE(co.total_settled_base, co.total_price) / co.total_price)
				ELSE 0 END), 0) AS v
			FROM customer_order co
			WHERE co.order_status_id IN (:statusIds)
			  AND co.placed >= :from AND co.placed < :to
			  AND (co.total_settled_base IS NOT NULL OR co.currency = :base)`,
			map[string]any{"statusIds": statusIDs, "from": fromT, "to": toT, "base": cache.GetBaseCurrency()})
		if err != nil {
			return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon vat operational: %w", err)
		}
		block.Operational = op.V.Round(2)
	}
	block.Delta = block.Ledger.Sub(block.Operational)

	mismatches, err := storeutil.QueryListNamed[struct {
		Ref string `db:"ref"`
	}](ctx, s.DB, `
		SELECT source_key AS ref FROM acct_journal_entry
		WHERE source_type IN ('order_sale','order_prepayment') AND has_caveat = TRUE
		  AND caveat LIKE '%vat snapshot mismatch%'
		  AND occurred_at >= :from AND occurred_at < :to
		ORDER BY occurred_at LIMIT :topN`,
		map[string]any{"from": fromStr, "to": toStr, "topN": reconTopN})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon vat mismatches: %w", err)
	}
	for _, m := range mismatches {
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    m.Ref,
			Label:  "regime VAT differs from sale-time snapshot",
			Amount: decimal.Zero,
		})
	}
	block.TotalCount, err = storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM acct_journal_entry
		WHERE source_type IN ('order_sale','order_prepayment') AND has_caveat = TRUE
		  AND caveat LIKE '%vat snapshot mismatch%'
		  AND occurred_at >= :from AND occurred_at < :to`,
		map[string]any{"from": fromStr, "to": toStr})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon vat mismatch count: %w", err)
	}
	return block, nil
}

// reconPrepayments: ledger 2090 Customer-Prepayments balance AS OF the period end vs the outstanding
// customer prepayment implied by the delivered chain — orders that are paid (an order_prepayment entry
// exists) but not yet delivered (no order_delivered_sale entry) as of `to` (phase 2, wave 2). A
// post-cutover Stripe order parks G−VAT on 2090 at payment and drains the exact remaining balance at
// delivery, so between those moments 2090 carries the live obligation this block proves. It is an
// AS-OF balance like reconMaterials / reconFinishedGoods, so it is cumulative (not period-scoped) and
// keyed off ledger-entry existence, not order status — no status-cache guard is needed.
//
// The operational figure sums G − VAT_snapshot·k − refunded·k at k = G/total_price (the ratio the
// prepayment builder uses; G = total_settled_base, or total_price for a non-Stripe EUR order) — the
// revenue block's per-order EUR expression, less the refunded share. It subtracts the GROSS
// refunded_amount while the ledger reduced 2090 by only the refund's NET share, so a small positive
// delta from partial pre-delivery refunds (and rounding) is EXPECTED and advisory, not a hard error —
// the same stance as the revenue and finished-goods blocks. `from` is unused (as-of `to` only).
func (s *Store) reconPrepayments(ctx context.Context, from, to time.Time) (entity.AcctReconBlock, error) {
	ledger, err := s.accountBalanceBefore(ctx, "2090", to)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "prepayments", Ledger: ledger}

	// toStr uses dateLayout so the operational gate's occurred_at boundary is IDENTICAL to the as-of
	// ledger balance (accountBalanceBefore formats the same way) — the two sides count the same entries.
	toStr := to.UTC().Format(dateLayout)

	// gate selects the delivered-chain orders that are paid (order_prepayment) but not yet delivered (no
	// order_delivered_sale) as of `to`. The CAST/COLLATE lands the utf8mb4 acct source_key in the
	// operational tables' utf8mb3 collation (prod), exactly as the revenue block joins order uuids; both
	// source_keys are the bare order uuid, so a plain equality (no SUBSTRING_INDEX) is correct.
	const gate = `EXISTS (SELECT 1 FROM acct_journal_entry e
	              WHERE e.source_type = 'order_prepayment' AND e.occurred_at < :to
	                AND e.source_key = CAST(co.uuid AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci)
	          AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
	                          WHERE e.source_type = 'order_delivered_sale' AND e.occurred_at < :to
	                            AND e.source_key = CAST(co.uuid AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci)`
	// outstanding = live customer prepayment on 2090 (G − VAT_snapshot·k − refunded·k).
	const outstanding = `COALESCE(co.total_settled_base, co.total_price)
	    - COALESCE(co.vat_amount, 0)      * (COALESCE(co.total_settled_base, co.total_price) / co.total_price)
	    - COALESCE(co.refunded_amount, 0) * (COALESCE(co.total_settled_base, co.total_price) / co.total_price)`

	op, err := storeutil.QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(CASE WHEN co.total_price > 0 THEN `+outstanding+` ELSE 0 END), 0) AS v
		FROM customer_order co
		WHERE `+gate,
		map[string]any{"to": toStr})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon prepayments operational: %w", err)
	}
	block.Operational = op.V.Round(2)
	block.Delta = block.Ledger.Sub(block.Operational)

	items, err := storeutil.QueryListNamed[struct {
		Ref    string          `db:"ref"`
		Amount decimal.Decimal `db:"amount"`
	}](ctx, s.DB, `
		SELECT co.uuid AS ref, (`+outstanding+`) AS amount
		FROM customer_order co
		WHERE co.total_price > 0 AND `+gate+`
		ORDER BY co.placed
		LIMIT :topN`,
		map[string]any{"to": toStr, "topN": reconTopN})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon prepayments items: %w", err)
	}
	for _, it := range items {
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    it.Ref,
			Label:  "paid on the delivered chain, not yet delivered (outstanding prepayment)",
			Amount: it.Amount.Round(2),
		})
	}

	block.TotalCount, err = storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM customer_order co
		WHERE `+gate,
		map[string]any{"to": toStr})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon prepayments count: %w", err)
	}
	return block, nil
}

// reconShipping: ledger 6030 Shipping & Fulfillment debits over [from, to) vs Σ shipment.actual_cost +
// return_shipping_cost of the shipments whose shipping_date falls in the period (phase 2, wave 3, 3.1).
// The shipping_actual pull posts each shipment's cost to 6030 with occurred_at = shipping_date, so the
// two sides count the same window. A shipment whose actual_cost was cleared to NULL after posting leaves
// a positive delta here (the pull does not auto-reverse an un-set cost) — advisory, not a hard error,
// the same stance as the other as-of/timing blocks. Items sample the period's costed shipments.
func (s *Store) reconShipping(ctx context.Context, fromStr, toStr string, fromT, toT time.Time) (entity.AcctReconBlock, error) {
	// NET 6030 (debit − credit), NOT debit-only: an actual-cost correction reposts as reverse v1
	// (Cr 6030) + create v2 (Dr 6030), so a debit-only sum would double-count the superseded version
	// (v1 + v2). Netting the reversal credit leaves the active-version total, matching the P&L (which
	// also nets dr − cr) and the operational figure. Reposts are the expected workflow here.
	dr, err := s.ledgerLineSum(ctx, []string{"6030"}, entity.AcctSideDebit, "", fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	cr, err := s.ledgerLineSum(ctx, []string{"6030"}, entity.AcctSideCredit, "", fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "shipping", Ledger: dr.Sub(cr)}

	op, err := storeutil.QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(COALESCE(sh.actual_cost, 0) + COALESCE(sh.return_shipping_cost, 0)), 0) AS v
		FROM shipment sh
		WHERE sh.shipping_date >= :from AND sh.shipping_date < :to
		  AND (sh.actual_cost IS NOT NULL OR sh.return_shipping_cost IS NOT NULL)`,
		map[string]any{"from": fromT, "to": toT})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon shipping operational: %w", err)
	}
	block.Operational = op.V.Round(2)
	block.Delta = block.Ledger.Sub(block.Operational)

	items, err := storeutil.QueryListNamed[struct {
		Ref    string          `db:"ref"`
		Amount decimal.Decimal `db:"amount"`
	}](ctx, s.DB, `
		SELECT co.uuid AS ref, (COALESCE(sh.actual_cost, 0) + COALESCE(sh.return_shipping_cost, 0)) AS amount
		FROM shipment sh
		JOIN customer_order co ON co.id = sh.order_id
		WHERE sh.shipping_date >= :from AND sh.shipping_date < :to
		  AND (sh.actual_cost IS NOT NULL OR sh.return_shipping_cost IS NOT NULL)
		ORDER BY sh.shipping_date
		LIMIT :topN`,
		map[string]any{"from": fromT, "to": toT, "topN": reconTopN})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon shipping items: %w", err)
	}
	for _, it := range items {
		block.Items = append(block.Items, entity.AcctReconItem{
			Ref:    it.Ref,
			Label:  "actual carrier cost booked to 6030",
			Amount: it.Amount.Round(2),
		})
	}
	block.TotalCount, err = storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM shipment sh
		WHERE sh.shipping_date >= :from AND sh.shipping_date < :to
		  AND (sh.actual_cost IS NOT NULL OR sh.return_shipping_cost IS NOT NULL)`,
		map[string]any{"from": fromT, "to": toT})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon shipping count: %w", err)
	}
	return block, nil
}

// reasonOrDefault returns the outbox last_error text, or a default when it is NULL/empty.
func reasonOrDefault(s sql.NullString, def string) string {
	if s.Valid && strings.TrimSpace(s.String) != "" {
		return s.String
	}
	return def
}
