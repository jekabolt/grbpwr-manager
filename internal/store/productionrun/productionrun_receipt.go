package productionrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/go-sql-driver/mysql"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/inventory"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// PostProductionRunReceipt executes the atomic receiving command (Phase 4, receipt v1, final-only):
// in ONE transaction it records the immutable receipt + its counted lines, stamps the counts onto
// the plan grid, books the good units into product stock (or the output material for an auxiliary
// run), freezes the run's actual unit cost on the receipt, optionally seeds cost_price, transitions
// the run to received, and writes the idempotency record. The receipt row doubles as the accounting
// outbox: the posting worker scans receipts without a live journal entry and posts by receipt id.
//
// Ordering inside the transaction is load-bearing:
//  1. the run lock comes FIRST, so two concurrent commands with the same idempotency key serialize
//     here and the loser's replay check (step 2) sees the winner's committed record — a locking
//     read returns the latest committed row, and the read view of this SERIALIZABLE tx is only
//     established by its first read, which is this lock;
//  2. the replay check runs BEFORE the status guards: a genuine retry of a command that already
//     received the run must replay the original success, not die on "already received".
//
// Idempotency = replay, not reject (plan 05, amendment 4): same key + same RequestHash → the stored
// result, Replayed=true; same key + different hash → entity.ErrIdempotencyConflict.
func (s *Store) PostProductionRunReceipt(ctx context.Context, p entity.PostProductionRunReceiptParams) (*entity.PostProductionRunReceiptResult, error) {
	var res *entity.PostProductionRunReceiptResult
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
			return fmt.Errorf("failed to load production run for receipt: %w", err)
		}

		claimed, stored, err := claimIdempotency(ctx, db, entity.CommandTypeProductionRunReceipt, p.IdempotencyKey, p.RequestHash)
		if err != nil {
			return err
		}
		if !claimed {
			if stored.RequestHash != p.RequestHash {
				return entity.ErrIdempotencyConflict
			}
			replayed, err := replayReceiptResult(stored.Response)
			if err != nil {
				return err
			}
			res = replayed
			return nil
		}

		switch cur.Status {
		case string(entity.ProductionRunReceived), string(entity.ProductionRunClosed):
			return entity.ErrProductionRunAlreadyReceived
		case string(entity.ProductionRunCancelled):
			return entity.ErrProductionRunCancelledReceive
		}
		if p.ExpectedLockVersion > 0 && cur.LockVersion != p.ExpectedLockVersion {
			return entity.ErrProductionRunConflict
		}

		// Resolve the submitted counts against the FRESH plan lines under the lock — by line_key,
		// never by (product, size). The plan line's product/size are re-validated here against the
		// handler's card-derived sets, so a line edit racing the command cannot smuggle stock into a
		// product the handler never saw.
		lines, err := loadRunLines(ctx, db, p.RunID)
		if err != nil {
			return err
		}
		byKey := make(map[string]*entity.ProductionRunLine, len(lines))
		for i := range lines {
			byKey[lines[i].LineKey] = &lines[i]
		}
		type countedLine struct {
			line *entity.ProductionRunLine
			in   entity.ProductionRunReceiptLineInput
		}
		counted := make([]countedLine, 0, len(p.Lines))
		seen := make(map[string]bool, len(p.Lines))
		totalGood, totalDefect := 0, 0
		for _, in := range p.Lines {
			if in.GoodQty < 0 || in.DefectQty < 0 {
				return fmt.Errorf("receipt line %q: negative quantity", in.LineKey)
			}
			if seen[in.LineKey] {
				return fmt.Errorf("receipt line %q submitted twice", in.LineKey)
			}
			seen[in.LineKey] = true
			ln, ok := byKey[in.LineKey]
			if !ok {
				return entity.ErrProductionRunReceiptLineUnknown
			}
			if in.GoodQty == 0 && in.DefectQty == 0 {
				continue // an uncounted line carries no receipt fact
			}
			if p.Aux {
				if ln.ProductId.Valid {
					// The handler validated a product-free grid; a product appeared since → stale read.
					return entity.ErrProductionRunConcurrentModification
				}
			} else {
				// Good units are posted to sellable stock, so they need a product linked to the run's
				// card and a size from its grid. A defect-only count is a recorded fact, not stock —
				// it may land on a still-unpublished (product-less) planning line.
				if in.GoodQty > 0 {
					if !ln.ProductId.Valid {
						return entity.ErrProductionRunLineProductMissing
					}
					if len(p.ValidProducts) > 0 && !p.ValidProducts[int(ln.ProductId.Int32)] {
						return entity.ErrProductionRunLineProductUnlinked
					}
					if len(p.ValidSizes) > 0 && ln.SizeId > 0 && !p.ValidSizes[ln.SizeId] {
						return entity.ErrProductionRunLineSizeUnlinked
					}
					if ln.SizeId == 0 {
						// A garment line without a size cannot be booked as product_size stock.
						return entity.ErrProductionRunLineSizeUnlinked
					}
				}
			}
			counted = append(counted, countedLine{line: ln, in: in})
			totalGood += in.GoodQty
			totalDefect += in.DefectQty
		}
		if totalGood == 0 && totalDefect == 0 {
			return entity.ErrProductionRunNothingReceived
		}

		// Stamp the counts onto the plan grid. Final-only semantics: a submitted count is the line's
		// final fact; a line NOT submitted (or submitted at 0/0) received nothing — an explicit 0,
		// not an unknowable NULL.
		countsByID := make(map[int]entity.ProductionRunReceiptLineInput, len(counted))
		for _, c := range counted {
			countsByID[c.line.Id] = c.in
		}
		for i := range lines {
			in := countsByID[lines[i].Id] // zero value for unsubmitted lines
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE production_run_line SET received_qty = :g, defect_qty = :d WHERE id = :id`,
				map[string]any{"g": in.GoodQty, "d": in.DefectQty, "id": lines[i].Id}); err != nil {
				return fmt.Errorf("failed to stamp receipt counts on run line %d: %w", lines[i].Id, err)
			}
			lines[i].ReceivedQty = sql.NullInt64{Int64: int64(in.GoodQty), Valid: true}
			lines[i].DefectQty = sql.NullInt64{Int64: int64(in.DefectQty), Valid: true}
		}

		// Freeze the valuation: the run's actual unit cost over THIS receipt's good units, computed
		// from the freshly-read costs and movements inside the lock (a material issue racing the
		// command is either included or serialized behind the run lock).
		costs, err := loadRunCosts(ctx, db, p.RunID)
		if err != nil {
			return err
		}
		movements, err := loadRunMovements(ctx, db, p.RunID)
		if err != nil {
			return err
		}
		runShape := &entity.ProductionRun{
			ProductionRunInsert: entity.ProductionRunInsert{Lines: lines, Costs: costs},
			MaterialMovements:   movements,
		}
		unitCost := runShape.ActualUnitCostBase()

		now := s.Now()
		var baseCurrency sql.NullString
		if unitCost.Valid && p.BaseCurrency != "" {
			baseCurrency = sql.NullString{String: p.BaseCurrency, Valid: true}
		}
		var adminUser sql.NullString
		if p.Username != "" {
			adminUser = sql.NullString{String: p.Username, Valid: true}
		}
		var note sql.NullString
		if p.Note != "" {
			note = sql.NullString{String: p.Note, Valid: true}
		}
		receiptID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO production_run_receipt
				(run_id, received_at, admin_username, note, idempotency_key, unit_cost_base, base_currency, has_base, posting_status)
			VALUES (:run_id, :received_at, :admin_username, :note, :idempotency_key, :unit_cost_base, :base_currency, :has_base, 'pending')`,
			map[string]any{
				"run_id":          p.RunID,
				"received_at":     now,
				"admin_username":  adminUser,
				"note":            note,
				"idempotency_key": p.IdempotencyKey,
				"unit_cost_base":  unitCost,
				"base_currency":   baseCurrency,
				"has_base":        unitCost.Valid,
			})
		if err != nil {
			return fmt.Errorf("failed to insert production run receipt: %w", err)
		}
		for _, c := range counted {
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO production_run_receipt_line
					(receipt_id, run_line_id, product_id, size_id, good_qty, defect_qty)
				VALUES (:receipt_id, :run_line_id, :product_id, :size_id, :good_qty, :defect_qty)`,
				map[string]any{
					"receipt_id":  receiptID,
					"run_line_id": c.line.Id,
					"product_id":  c.line.ProductId,
					"size_id":     nullIfZero(c.line.SizeId),
					"good_qty":    c.in.GoodQty,
					"defect_qty":  c.in.DefectQty,
				}); err != nil {
				return fmt.Errorf("failed to insert production run receipt line: %w", err)
			}
		}

		// Book the good units. Aux → the output material's warehouse (moving average); garment →
		// each line's own product stock, products ascending for a deterministic lock order.
		costPriceWrites := 0
		if p.Aux {
			if totalGood > 0 {
				if _, err := inventory.ReceiveInTx(ctx, rep, entity.MaterialReceiptInsert{
					MaterialId:      p.OutputMaterialID,
					Quantity:        decimal.NewFromInt(int64(totalGood)),
					UnitCost:        unitCost,
					ProductionRunId: sql.NullInt32{Int32: int32(p.RunID), Valid: true},
					FromProduction:  true,
					AdminUsername:   p.Username,
				}, now); err != nil {
					return err
				}
			}
		} else {
			perProduct := make(map[int]map[int]int)
			for _, c := range counted {
				if c.in.GoodQty == 0 {
					continue
				}
				pid := int(c.line.ProductId.Int32)
				if perProduct[pid] == nil {
					perProduct[pid] = make(map[int]int)
				}
				perProduct[pid][c.line.SizeId] += c.in.GoodQty
			}
			productIDs := make([]int, 0, len(perProduct))
			for pid := range perProduct {
				productIDs = append(productIDs, pid)
			}
			sort.Ints(productIDs)
			for _, pid := range productIDs {
				if err := rep.Products().ReceiveProductionStock(ctx, pid, perProduct[pid], p.RunID, p.Username); err != nil {
					return err
				}
				if p.UpdateCostPrice && unitCost.Valid {
					written, err := rep.Products().SetProductCostPriceFromProductionRun(ctx, pid, p.RunID, unitCost.Decimal)
					if err != nil {
						return err
					}
					if written {
						costPriceWrites++
					} else {
						slog.Default().InfoContext(ctx, "production receipt did not claim cost_price (manual source, missing product, or unchanged)",
							slog.Int("run_id", p.RunID), slog.Int("product_id", pid))
					}
				}
			}
		}

		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE production_run SET status = :status, received_at = :received_at WHERE id = :id`,
			map[string]any{"id": p.RunID, "status": string(entity.ProductionRunReceived), "received_at": now}); err != nil {
			return err
		}

		result := &entity.PostProductionRunReceiptResult{ReceiptID: receiptID, CostPriceUpdated: costPriceWrites > 0}
		response, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("failed to marshal receipt result: %w", err)
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE command_idempotency SET status = 'succeeded', result_ids = :result_ids, response = :response
			WHERE command_type = :command_type AND idempotency_key = :idempotency_key`,
			map[string]any{
				"command_type":    entity.CommandTypeProductionRunReceipt,
				"idempotency_key": p.IdempotencyKey,
				"result_ids":      fmt.Sprintf("receipt:%d", receiptID),
				"response":        string(response),
			}); err != nil {
			return fmt.Errorf("failed to record receipt idempotency: %w", err)
		}
		res = result
		return nil
	})
	if err != nil {
		switch err {
		case sql.ErrNoRows, entity.ErrProductionRunAlreadyReceived, entity.ErrProductionRunCancelledReceive,
			entity.ErrProductionRunConflict, entity.ErrProductionRunReceiptLineUnknown,
			entity.ErrProductionRunConcurrentModification, entity.ErrProductionRunLineProductMissing,
			entity.ErrProductionRunLineProductUnlinked, entity.ErrProductionRunLineSizeUnlinked,
			entity.ErrProductionRunNothingReceived, entity.ErrIdempotencyConflict:
			return nil, err
		}
		return nil, fmt.Errorf("can't post production run receipt: %w", err)
	}
	return res, nil
}

// idempotencyRecord is the replay-relevant slice of a command_idempotency row.
type idempotencyRecord struct {
	Status      string `db:"status"`
	RequestHash string `db:"request_hash"`
	Response    string `db:"response"`
}

// claimIdempotency is the CLAIM-FIRST replay check: it INSERTs the command's 'in_progress' marker
// and reports claimed=true for a fresh command (the caller executes and later flips the row to
// 'succeeded' — or rolls the whole claim back with the failed transaction, leaving no trace). A
// duplicate key (1062) means the command already ran: the stored row comes back for replay/conflict.
//
// Insert-first is not a style choice. The previous shape — a plain SELECT probing for the key, then
// an INSERT at commit time — takes a shared GAP lock on uq_cmdidem under SERIALIZABLE, and two
// concurrent commands of DIFFERENT runs (which share no run lock) both upgrade that gap to an
// insert intention: the classic S→X deadlock storm (measured: 13 of 16 concurrent receives
// deadlocked at least once). An INSERT's intention lock conflicts only with gap locks, and this
// function is now the only toucher of the index — different keys in the same gap no longer block
// each other, and the same key serializes on the winner's record lock (the loser blocks in the
// uniqueness check until the winner commits, then reads the committed row).
func claimIdempotency(ctx context.Context, db dependency.DB, commandType, key, requestHash string) (bool, *idempotencyRecord, error) {
	err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO command_idempotency (command_type, idempotency_key, request_hash, status)
		VALUES (:command_type, :idempotency_key, :request_hash, 'in_progress')`,
		map[string]any{"command_type": commandType, "idempotency_key": key, "request_hash": requestHash})
	if err == nil {
		return true, nil, nil
	}
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1062 {
		return false, nil, fmt.Errorf("failed to claim idempotency record: %w", err)
	}
	rec, err := storeutil.QueryNamedOne[idempotencyRecord](ctx, db, `
		SELECT status, request_hash, COALESCE(response, '') AS response
		FROM command_idempotency WHERE command_type = :t AND idempotency_key = :k`,
		map[string]any{"t": commandType, "k": key})
	if err != nil {
		return false, nil, fmt.Errorf("failed to load idempotency record after duplicate claim: %w", err)
	}
	if rec.Status != "succeeded" {
		// Unreachable for a well-behaved retry: a failed command rolls its claim back, and a
		// concurrent same-key command commits 'succeeded' before its row is visible. A non-succeeded
		// row therefore means the key was reused across distinct intents (or a crashed manual edit) —
		// refuse rather than replay a response that does not exist.
		return false, nil, entity.ErrIdempotencyConflict
	}
	return false, &rec, nil
}

// replayReceiptResult reconstructs the original command result from the stored response JSON.
func replayReceiptResult(response string) (*entity.PostProductionRunReceiptResult, error) {
	var r entity.PostProductionRunReceiptResult
	if err := json.Unmarshal([]byte(response), &r); err != nil {
		return nil, fmt.Errorf("failed to replay receipt result: %w", err)
	}
	r.Replayed = true
	return &r, nil
}

// nullIfZero maps the entity's "0 = no size" convention onto the nullable size_id column (0236).
func nullIfZero(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// loadRunReceipts returns a run's receipts oldest-first, each with its counted lines (joined with
// the plan line's stable line_key so the client correlates without row ids).
func loadRunReceipts(ctx context.Context, db dependency.DB, runID int) ([]entity.ProductionRunReceipt, error) {
	receipts, err := storeutil.QueryListNamed[entity.ProductionRunReceipt](ctx, db, `
		SELECT id, run_id, received_at, admin_username, note, idempotency_key, unit_cost_base,
		       base_currency, has_base, reversal_of, reversed_by, posting_status, created_at
		FROM production_run_receipt WHERE run_id = :run_id ORDER BY received_at, id`,
		map[string]any{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("can't load production run receipts: %w", err)
	}
	for i := range receipts {
		lines, err := storeutil.QueryListNamed[entity.ProductionRunReceiptLine](ctx, db, `
			SELECT rl.id, rl.receipt_id, rl.run_line_id, rl.product_id, rl.size_id, rl.good_qty,
			       rl.defect_qty, COALESCE(l.line_key, '') AS line_key
			FROM production_run_receipt_line rl
			JOIN production_run_line l ON l.id = rl.run_line_id
			WHERE rl.receipt_id = :id ORDER BY rl.id`,
			map[string]any{"id": receipts[i].Id})
		if err != nil {
			return nil, fmt.Errorf("can't load production run receipt lines: %w", err)
		}
		receipts[i].Lines = lines
	}
	return receipts, nil
}
