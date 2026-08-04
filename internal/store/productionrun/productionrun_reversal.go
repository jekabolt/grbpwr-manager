package productionrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	acctrules "github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// ReverseProductionRunReceipt undoes ONE receipt of a run in a single transaction (Phase 6,
// plan 05). Effects, in lock order:
//
//  1. run lock FOR UPDATE — every stateful precondition (unreversed receipt, run status, lock
//     version) is decided under it, which is also the idempotency guard: "this receipt is not yet
//     reversed" can hold for exactly one committer;
//  2. the receipt's good units leave product stock (never below zero — EVERY short variant is
//     collected and the whole command refuses with the full list; sold-but-unshipped units
//     already left `quantity` at payment, so they block identically);
//  3. the plan-grid rollups subtract this receipt's counts (they are Σ over live receipts);
//  4. the SCOPED accounting compensation: Dr 1120 WIP / Cr 1130 FG for what the receipt's live
//     entry transferred — the manual/AP capitalisation deliberately stays (the supplier invoice
//     does not vanish because the goods went back to WIP), and the original entry stays LIVE as
//     its record. posted_fg_base is NULLed (the FG claim died); a NULL posted_manual_base is
//     first recovered from the live entry's lines (a deploy-window receipt stamped without
//     amounts would otherwise let the next receipt double-capitalise);
//  5. a reversal row appears in the receipt history (reversal_of → original, original gets
//     reversed_by) — quantity-neutral, posting_status 'posted' (its entry is the inline
//     compensation; the scan never sees either side of the pair);
//  6. cost_price rolls back to the tech-card estimate for products THIS run still claims, unless
//     a live sibling receipt still stocks the product (the run's claim is then still earned) or a
//     later source superseded it (skipped);
//  7. the run recomputes: a live FINAL keeps it received; otherwise partially_received while live
//     receipts remain, in_progress when none — received_at clears with the final; lock_version
//     bumps either way;
//  8. a production_run_event records who/why/what (stock deltas, compensated FG, cost actions).
//
// Materials issued to the run are NOT returned (reversal un-receives garments, it does not un-cut
// fabric); the aux-run refusal lives in the handler (it loads the card anyway).
func (s *Store) ReverseProductionRunReceipt(ctx context.Context, p entity.ReverseProductionRunReceiptParams) (*entity.ReverseProductionRunReceiptResult, error) {
	var res *entity.ReverseProductionRunReceiptResult
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		cur, err := storeutil.QueryNamedOne[struct {
			Status      string `db:"status"`
			LockVersion int    `db:"lock_version"`
		}](ctx, db, `SELECT status, lock_version FROM production_run WHERE id = :id FOR UPDATE`,
			map[string]any{"id": p.RunID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return fmt.Errorf("failed to load production run for reversal: %w", err)
		}
		if cur.Status == string(entity.ProductionRunClosed) {
			return entity.ErrProductionRunReversalClosedRun
		}
		if p.ExpectedLockVersion > 0 && cur.LockVersion != p.ExpectedLockVersion {
			return entity.ErrProductionRunConflict
		}

		rcpt, err := storeutil.QueryNamedOne[struct {
			Id           int                 `db:"id"`
			ReversalOf   sql.NullInt32       `db:"reversal_of"`
			ReversedBy   sql.NullInt32       `db:"reversed_by"`
			Final        bool                `db:"final"`
			PostedManual decimal.NullDecimal `db:"posted_manual_base"`
			PostedFG     decimal.NullDecimal `db:"posted_fg_base"`
		}](ctx, db, `
			SELECT id, reversal_of, reversed_by, final, posted_manual_base, posted_fg_base
			FROM production_run_receipt WHERE id = :id AND run_id = :run_id FOR UPDATE`,
			map[string]any{"id": p.ReceiptID, "run_id": p.RunID})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return entity.ErrProductionRunReceiptNotFound
			}
			return fmt.Errorf("failed to load receipt for reversal: %w", err)
		}
		if rcpt.ReversalOf.Valid {
			return entity.ErrProductionRunReversalOfReversal
		}
		if rcpt.ReversedBy.Valid {
			return entity.ErrProductionRunReceiptAlreadyReversed
		}
		// Reversals unwind newest-first: while a live FINAL exists, reversing a PARTIAL would strand
		// its FG share back on WIP forever — the run stays received, no receipt can ever post again,
		// and no true-up can sweep 1120 (adversarial #3). Reverse the final first; the run returns
		// to partially_received and the partial (and a corrected re-receive) become possible.
		if !rcpt.Final {
			finals, err := storeutil.QueryCountNamed(ctx, db, `
				SELECT COUNT(*) FROM production_run_receipt
				WHERE run_id = :run_id AND id <> :id AND final = 1
				  AND reversal_of IS NULL AND reversed_by IS NULL`,
				map[string]any{"run_id": p.RunID, "id": p.ReceiptID})
			if err != nil {
				return fmt.Errorf("failed to check live final receipts: %w", err)
			}
			if finals > 0 {
				return entity.ErrProductionRunReversalFinalFirst
			}
		}

		lines, err := storeutil.QueryListNamed[entity.ProductionRunReceiptLine](ctx, db, `
			SELECT rl.id, rl.receipt_id, rl.run_line_id, rl.product_id, rl.size_id, rl.good_qty,
			       rl.defect_qty, rl.defect_disposition, '' AS line_key
			FROM production_run_receipt_line rl WHERE rl.receipt_id = :id ORDER BY rl.id`,
			map[string]any{"id": p.ReceiptID})
		if err != nil {
			return fmt.Errorf("failed to load receipt lines for reversal: %w", err)
		}

		// 2. Stock back out, products ascending (the receive path's lock order), collecting EVERY
		// short variant before refusing — the operator fixes the whole problem in one pass.
		perProduct := make(map[int]map[int]int)
		perProductSeconds := make(map[int]map[int]int)
		type stockDelta struct {
			ProductID, SizeID, Qty int
			Grade                  string `json:",omitempty"`
		}
		var stock []stockDelta
		for _, ln := range lines {
			if !ln.ProductId.Valid {
				continue
			}
			pid := int(ln.ProductId.Int32)
			if ln.GoodQty > 0 {
				if perProduct[pid] == nil {
					perProduct[pid] = make(map[int]int)
				}
				perProduct[pid][int(ln.SizeId.Int32)] += ln.GoodQty
			}
			// Seconds went into B-grade stock at receive — the reversal takes them back out under
			// the same never-negative shortfall discipline (a sold B unit blocks identically).
			if ln.DefectQty > 0 && ln.DefectDisposition == entity.DefectDispositionSeconds {
				if perProductSeconds[pid] == nil {
					perProductSeconds[pid] = make(map[int]int)
				}
				perProductSeconds[pid][int(ln.SizeId.Int32)] += ln.DefectQty
			}
		}
		productIDs := make([]int, 0, len(perProduct))
		for pid := range perProduct {
			productIDs = append(productIDs, pid)
		}
		sort.Ints(productIDs)
		var short []entity.ProductionRunReversalShortfallItem
		for _, pid := range productIDs {
			sh, err := rep.Products().ReverseProductionStock(ctx, pid, perProduct[pid], p.ReceiptID, p.Username, p.Reason, entity.VariantGradeA)
			if err != nil {
				return err
			}
			short = append(short, sh...)
			for sizeID, qty := range perProduct[pid] {
				stock = append(stock, stockDelta{ProductID: pid, SizeID: sizeID, Qty: qty})
			}
		}
		secondsIDs := make([]int, 0, len(perProductSeconds))
		for pid := range perProductSeconds {
			secondsIDs = append(secondsIDs, pid)
		}
		sort.Ints(secondsIDs)
		for _, pid := range secondsIDs {
			sh, err := rep.Products().ReverseProductionStock(ctx, pid, perProductSeconds[pid], p.ReceiptID, p.Username, p.Reason, entity.VariantGradeB)
			if err != nil {
				return err
			}
			short = append(short, sh...)
			for sizeID, qty := range perProductSeconds[pid] {
				stock = append(stock, stockDelta{ProductID: pid, SizeID: sizeID, Qty: qty, Grade: entity.VariantGradeB})
			}
		}
		if len(short) > 0 {
			return &entity.ProductionRunReversalShortfallError{Items: short}
		}

		// 3. Rollups subtract (they are Σ over live receipts; the CHECK >= 0 guards corruption).
		for _, ln := range lines {
			if ln.GoodQty == 0 && ln.DefectQty == 0 {
				continue
			}
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE production_run_line
				SET received_qty = COALESCE(received_qty, 0) - :g,
				    defect_qty = COALESCE(defect_qty, 0) - :d
				WHERE id = :id`,
				map[string]any{"g": ln.GoodQty, "d": ln.DefectQty, "id": ln.RunLineId}); err != nil {
				return fmt.Errorf("failed to subtract receipt counts from run line %d: %w", ln.RunLineId, err)
			}
		}

		// 4. Scoped accounting compensation off the receipt's LIVE entry, if any (none on a
		// never-posted receipt, with accounting disabled, or after a generic whole-entry reversal —
		// in each case there is no FG transfer to compensate).
		var compensated decimal.NullDecimal
		entry, err := rep.Accounting().GetLiveProductionReceiveEntry(ctx, p.ReceiptID, p.RunID)
		if err != nil {
			return err
		}
		if entry != nil {
			// Plan 05 v2 semantics: the compensation posts at Now() into the CURRENT open period,
			// referencing the original via source_key — the original's (possibly closed) period is
			// untouched. The FG leg moves between balance-sheet accounts; the write-off recovery
			// (Cr 5040) DOES move P&L across months when the original expensed in a closed one —
			// accepted v1 behaviour (a standard current-period adjustment), noted, not hidden. Today's
			// period being closed is the only refusal, surfaced by CreateJournalEntry below —
			// gating on the ORIGINAL's period would make every receipt permanently un-reversible
			// the day its month closes (adversarial #4).
			fg, manual, writeOff := decimal.Zero, decimal.Zero, decimal.Zero
			for _, l := range entry.Lines {
				switch {
				case l.AccountCode == acctrules.Acc1130 && l.Side == entity.AcctSideDebit:
					fg = fg.Add(l.Amount)
				case l.AccountCode == acctrules.Acc2010 && l.Side == entity.AcctSideCredit:
					manual = manual.Add(l.Amount)
				case l.AccountCode == acctrules.Acc5040 && l.Side == entity.AcctSideDebit:
					// The final receipt's abnormal defect write-off (Phase 7): un-receiving the goods
					// un-does the loss event too — the cost goes back to WIP with the rest.
					writeOff = writeOff.Add(l.Amount)
				}
			}
			if rcpt.PostedFG.Valid && rcpt.PostedFG.Decimal.LessThan(fg) {
				// The stamped claim is what the sibling arithmetic has been reading — never credit
				// 1130 beyond what the LIVE entry actually debited, and never beyond the claim: a
				// version-key collapse can stamp figures the stored entry never booked
				// (adversarial #5). min() of the two is the safe compensation either way.
				fg = rcpt.PostedFG.Decimal
			}
			if fg.IsPositive() || writeOff.IsPositive() {
				desc := fmt.Sprintf("reversal of production receipt %d (run %d)", p.ReceiptID, p.RunID)
				if p.Reason != "" {
					desc = desc + " — " + p.Reason
				}
				compLines := []entity.AcctJournalLineInsert{
					{AccountCode: acctrules.Acc1120, Side: entity.AcctSideDebit, Amount: fg.Add(writeOff)},
				}
				if fg.IsPositive() {
					compLines = append(compLines, entity.AcctJournalLineInsert{AccountCode: acctrules.Acc1130, Side: entity.AcctSideCredit, Amount: fg})
				}
				if writeOff.IsPositive() {
					compLines = append(compLines, entity.AcctJournalLineInsert{AccountCode: acctrules.Acc5040, Side: entity.AcctSideCredit, Amount: writeOff})
				}
				if _, _, err := rep.Accounting().CreateJournalEntry(ctx, entity.AcctJournalEntryInsert{
					OccurredAt:  s.Now(),
					Description: desc,
					SourceType:  entity.AcctSourceProductionReceiveReversal,
					SourceKey:   fmt.Sprintf("receipt:%d", p.ReceiptID),
					CreatedBy:   createdByOrSystem(p.Username),
					Lines:       compLines,
				}); err != nil {
					if errors.Is(err, entity.ErrAcctPeriodClosed) {
						return entity.ErrProductionRunReversalPeriodClosed
					}
					return fmt.Errorf("failed to post reversal compensation: %w", err)
				}
				compensated = decimal.NullDecimal{Decimal: fg, Valid: true}
			}
			// The FG claim died with the compensation; a missing manual claim (a deploy-window
			// receipt stamped without amounts) is recovered from the live entry so the run's
			// capitalise-once arithmetic keeps seeing the invoice that remains payable.
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE production_run_receipt
				SET posted_fg_base = NULL,
				    posted_manual_base = COALESCE(posted_manual_base, :manual)
				WHERE id = :id`,
				map[string]any{"id": p.ReceiptID, "manual": manual}); err != nil {
				return fmt.Errorf("failed to update posted claims on reversed receipt: %w", err)
			}
		}

		// 5. The reversal row + linkage.
		mintedKey, err := entity.MintProductionRunLineKey()
		if err != nil {
			return fmt.Errorf("failed to mint reversal receipt key: %w", err)
		}
		note := p.Reason
		if r := []rune(note); len(r) > 512 {
			note = string(r[:512]) // rune-safe: a byte cut can split UTF-8 and hard-fail the insert
		}
		var adminUser sql.NullString
		if p.Username != "" {
			adminUser = sql.NullString{String: p.Username, Valid: true}
		}
		reversalID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO production_run_receipt
				(run_id, received_at, admin_username, note, idempotency_key, unit_cost_base,
				 base_currency, has_base, posting_status, final, reversal_of)
			VALUES (:run_id, :received_at, :admin_username, :note, :idempotency_key, NULL,
			        NULL, FALSE, 'posted', FALSE, :reversal_of)`,
			map[string]any{
				"run_id": p.RunID, "received_at": s.Now(), "admin_username": adminUser,
				"note":            sql.NullString{String: note, Valid: note != ""},
				"idempotency_key": mintedKey, "reversal_of": p.ReceiptID,
			})
		if err != nil {
			return fmt.Errorf("failed to insert reversal receipt row: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE production_run_receipt SET reversed_by = :rev WHERE id = :id`,
			map[string]any{"rev": reversalID, "id": p.ReceiptID}); err != nil {
			return fmt.Errorf("failed to link reversed receipt: %w", err)
		}

		// 6. cost_price rollback for products this receipt stocked, unless a live sibling still
		// stocks them (the run's claim is then still earned by real goods on the shelf).
		result := &entity.ReverseProductionRunReceiptResult{
			ReversalReceiptID: reversalID,
			CompensatedFGBase: compensated,
		}
		for _, pid := range productIDs {
			stillStocked, err := storeutil.QueryCountNamed(ctx, db, `
				SELECT COUNT(*) FROM production_run_receipt s
				JOIN production_run_receipt_line rl ON rl.receipt_id = s.id
				WHERE s.run_id = :run_id AND s.id <> :id
				  AND s.reversal_of IS NULL AND s.reversed_by IS NULL
				  AND rl.product_id = :pid AND rl.good_qty > 0`,
				map[string]any{"run_id": p.RunID, "id": p.ReceiptID, "pid": pid})
			if err != nil {
				return fmt.Errorf("failed to check sibling stocking of product %d: %w", pid, err)
			}
			if stillStocked > 0 {
				result.CostPriceSkipped = append(result.CostPriceSkipped, pid)
				continue
			}
			est := p.Reseed[pid]
			written, err := rep.Products().ClearProductCostPriceClaimOfRun(ctx, pid, p.RunID, p.CardID, est)
			if err != nil {
				return fmt.Errorf("failed to roll back cost_price of product %d: %w", pid, err)
			}
			switch {
			case !written:
				result.CostPriceSkipped = append(result.CostPriceSkipped, pid)
			case est.Cost.Valid:
				result.CostPriceReseeded = append(result.CostPriceReseeded, pid)
			default:
				result.CostPriceCleared = append(result.CostPriceCleared, pid)
			}
		}

		// 7. Run status from what remains live.
		remain, err := storeutil.QueryNamedOne[struct {
			Total  int `db:"total"`
			Finals int `db:"finals"`
		}](ctx, db, `
			SELECT COUNT(*) AS total, COALESCE(SUM(final), 0) AS finals
			FROM production_run_receipt
			WHERE run_id = :run_id AND reversal_of IS NULL AND reversed_by IS NULL`,
			map[string]any{"run_id": p.RunID})
		if err != nil {
			return fmt.Errorf("failed to count remaining live receipts: %w", err)
		}
		switch {
		case remain.Finals > 0:
			// The final that closed the run is still live — the run stays received; only the
			// lock fence moves.
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE production_run SET lock_version = lock_version + 1, updated_at = NOW()
				WHERE id = :id`, map[string]any{"id": p.RunID}); err != nil {
				return fmt.Errorf("failed to bump run lock version: %w", err)
			}
		case remain.Total > 0:
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE production_run SET status = :status, received_at = NULL,
					lock_version = lock_version + 1, updated_at = NOW()
				WHERE id = :id`,
				map[string]any{"id": p.RunID, "status": string(entity.ProductionRunPartiallyReceived)}); err != nil {
				return fmt.Errorf("failed to move run back to partially received: %w", err)
			}
		default:
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE production_run SET status = :status, received_at = NULL,
					lock_version = lock_version + 1, updated_at = NOW()
				WHERE id = :id`,
				map[string]any{"id": p.RunID, "status": string(entity.ProductionRunInProgress)}); err != nil {
				return fmt.Errorf("failed to move run back to in progress: %w", err)
			}
		}

		// 8. The audit event.
		payload := map[string]any{
			"receipt_id":          p.ReceiptID,
			"reversal_receipt_id": reversalID,
			"compensated_fg_base": nil,
			"stock":               stock,
			"cost_price": map[string][]int{
				"reseeded": result.CostPriceReseeded,
				"cleared":  result.CostPriceCleared,
				"skipped":  result.CostPriceSkipped,
			},
		}
		if compensated.Valid {
			payload["compensated_fg_base"] = compensated.Decimal.String()
		}
		pj, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal reversal event payload: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO production_run_event (run_id, event_type, actor, reason, payload)
			VALUES (:run_id, :event_type, :actor, :reason, :payload)`,
			map[string]any{
				"run_id": p.RunID, "event_type": entity.ProductionRunEventReceiptReversed,
				"actor":   adminUser,
				"reason":  sql.NullString{String: note, Valid: note != ""},
				"payload": string(pj),
			}); err != nil {
			return fmt.Errorf("failed to record reversal event: %w", err)
		}

		res = result
		return nil
	})
	if err != nil {
		var shortErr *entity.ProductionRunReversalShortfallError
		switch {
		case errors.Is(err, sql.ErrNoRows),
			errors.Is(err, entity.ErrProductionRunReceiptNotFound),
			errors.Is(err, entity.ErrProductionRunReceiptAlreadyReversed),
			errors.Is(err, entity.ErrProductionRunReversalOfReversal),
			errors.Is(err, entity.ErrProductionRunReversalClosedRun),
			errors.Is(err, entity.ErrProductionRunReversalPeriodClosed),
			errors.Is(err, entity.ErrProductionRunConflict):
			return nil, err
		case errors.As(err, &shortErr):
			return nil, err
		}
		return nil, fmt.Errorf("can't reverse production run receipt: %w", err)
	}
	return res, nil
}

// createdByOrSystem mirrors the accounting store's convention: entries are attributed to the
// acting admin when known, 'system' otherwise.
func createdByOrSystem(username string) string {
	if strings.TrimSpace(username) != "" {
		return username
	}
	return "system"
}
