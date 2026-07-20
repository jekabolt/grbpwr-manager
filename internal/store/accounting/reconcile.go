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
	return rec, nil
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
func (s *Store) reconRevenue(ctx context.Context, fromStr, toStr string, fromT, toT time.Time, statusIDs []int) (entity.AcctReconBlock, error) {
	ledger, err := s.ledgerLineSum(ctx, []string{"4010", "4020", "4110"}, entity.AcctSideCredit, string(entity.AcctSourceOrderSale), fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "revenue", Ledger: ledger}
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
		                  WHERE e.source_type = 'order_sale' AND e.source_key = CAST(co.uuid AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci)
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
		                  WHERE e.source_type = 'order_sale' AND e.source_key = CAST(co.uuid AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci)`,
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

// reconVat: ledger 2070 output VAT credited by order_sale entries vs the VAT the period's confirmed
// orders imply from their sale-time snapshot (EUR-scaled the same way reconRevenue scales revenue).
// The posting now uses the RESOLVED REGIME rate, so a standing delta versus the snapshot is expected
// where regime and snapshot rate diverge (phase 2, wave 1) — informational, not an error. Items sample
// the order_sale entries whose builder flagged a "vat snapshot mismatch".
func (s *Store) reconVat(ctx context.Context, fromStr, toStr string, fromT, toT time.Time, statusIDs []int) (entity.AcctReconBlock, error) {
	ledger, err := s.ledgerLineSum(ctx, []string{"2070"}, entity.AcctSideCredit, string(entity.AcctSourceOrderSale), fromStr, toStr)
	if err != nil {
		return entity.AcctReconBlock{}, err
	}
	block := entity.AcctReconBlock{Name: "vat", Ledger: ledger}
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
		WHERE source_type = 'order_sale' AND has_caveat = TRUE
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
		WHERE source_type = 'order_sale' AND has_caveat = TRUE
		  AND caveat LIKE '%vat snapshot mismatch%'
		  AND occurred_at >= :from AND occurred_at < :to`,
		map[string]any{"from": fromStr, "to": toStr})
	if err != nil {
		return entity.AcctReconBlock{}, fmt.Errorf("accounting: recon vat mismatch count: %w", err)
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
