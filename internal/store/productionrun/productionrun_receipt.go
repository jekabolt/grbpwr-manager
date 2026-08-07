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

// PostProductionRunReceipt executes the atomic receiving command (Phase 4 receipt v1; Phase 5 adds
// partials): in ONE transaction it records the immutable receipt + its counted lines, accumulates
// the counts onto the plan-grid rollups, books the good units into product stock (or, for an
// auxiliary run, into its output material — one bucket per colour when the card registered colour
// variants), freezes the run's actual unit cost on the receipt, optionally seeds cost_price,
// transitions the run (partial → partially_received; final → received), and writes the idempotency
// record. The receipt row doubles as the accounting outbox: the posting worker scans receipts
// without a live journal entry and posts by receipt id.
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
			TechCardId  int    `db:"tech_card_id"`
		}](ctx, db, `SELECT status, lock_version, tech_card_id FROM production_run WHERE id = :id FOR UPDATE`,
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
		// Ф6.5: PRESENCE, not magnitude. The version the operator counted against is enforced whenever
		// it was supplied — including 0, which is what a run that has never been edited reports. The
		// old `> 0` test meant a receipt posted against a FRESH run skipped this check entirely, so a
		// planner's edit landing between the count and the post went undetected on exactly the runs
		// most likely to be counted straight after creation. Only an ABSENT token opts out.
		if p.ExpectedLockVersion.Conflicts(cur.LockVersion) {
			return entity.ErrProductionRunConflict
		}
		// An AUX run receives in ONE final receipt (final-review BLOCKER): its output is valued
		// into the material warehouse at unit cost = run total ÷ received units, and partial
		// deliveries against a front-loaded cost would over-relieve WIP on every early receipt
		// (Σ thisGood × total/cumulative > total) — irreparably, since aux receipts cannot be
		// reversed. Mirrors ReverseProductionRunReceipt's aux refusal.
		if p.Aux && !p.Final {
			return entity.ErrProductionRunAuxPartial
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
		// Where an auxiliary run's output lands is resolved HERE, from the card's registry as it
		// stands inside this transaction — never from a snapshot the handler took before the run lock.
		// See auxDestination for why each half of that matters.
		var dest auxDestination
		if p.Aux {
			dest, err = resolveAuxDestination(ctx, db, cur.TechCardId, lines)
			if err != nil {
				return err
			}
		}
		type countedLine struct {
			line *entity.ProductionRunLine
			in   entity.ProductionRunReceiptLineInput
		}
		counted := make([]countedLine, 0, len(p.Lines))
		seen := make(map[string]bool, len(p.Lines))
		totalGood, totalDefect := 0, 0
		// perVariantGood is the colour breakdown of an aux run in VARIANT mode (0253): each colour's
		// good units, booked into that colour's own warehouse bucket further down. The rollups and the
		// valuation stay run-wide on purpose — one run has one blended unit cost across its colours
		// (plan §6.5; separate runs per colour is the SOP escape when that is not good enough).
		perVariantGood := make(map[int]int)
		for _, in := range p.Lines {
			if in.GoodQty < 0 || in.DefectQty < 0 {
				return fmt.Errorf("receipt line %q: negative quantity", in.LineKey)
			}
			if seen[in.LineKey] {
				return fmt.Errorf("receipt line %q submitted twice", in.LineKey)
			}
			seen[in.LineKey] = true
			if !entity.ValidDefectDisposition(in.DefectDisposition) {
				return fmt.Errorf("receipt line %q: unknown defect disposition %q", in.LineKey, in.DefectDisposition)
			}
			if in.DefectDisposition == "" {
				in.DefectDisposition = entity.DefectDispositionScrap
			}
			ln, ok := byKey[in.LineKey]
			if !ok {
				return entity.ErrProductionRunReceiptLineUnknown
			}
			if in.GoodQty == 0 && in.DefectQty == 0 {
				continue // an uncounted line carries no receipt fact
			}
			if in.DefectDisposition == entity.DefectDispositionSeconds && in.DefectQty > 0 {
				// Seconds land in the product's B-grade variant stock — that needs the same product
				// and size a good unit needs, and never exists on an auxiliary run (its output is a
				// material; a failed batch is scrap or an adjustment, not "B-grade fabric").
				if p.Aux {
					return fmt.Errorf("receipt line %q: an auxiliary run cannot receive seconds", in.LineKey)
				}
				if !ln.ProductId.Valid || ln.SizeId == 0 {
					return fmt.Errorf("receipt line %q: seconds need a published product and size to book B-grade stock", in.LineKey)
				}
			}
			if p.Aux {
				if ln.ProductId.Valid {
					// The handler validated a product-free grid; a product appeared since → stale read.
					return entity.ErrProductionRunConcurrentModification
				}
				if dest.variantMode {
					vid := lineVariantID(ln)
					if vid == 0 {
						// A counted COLOURLESS line on a card that produces by colour. The grid predates
						// the colours (they were registered after it was planned) — there is no bucket to
						// book it into, and the scalar path would put every colour's output into
						// output_material_id. A mixed grid can no longer be planned (the plan-time
						// validator refuses it), so this only catches grids that predate the colours.
						return entity.ErrProductionRunConcurrentModification
					}
					if _, ok := dest.variantMaterials[vid]; !ok {
						// The colour is not one of this card's — a genuinely impossible id, or a row that
						// moved out from under the run. RETIREMENT is NOT this case: a retired colour is
						// still the card's and is in the map, because retiring a colour stops new PLANS,
						// not a delivery already in flight (an aux run has no partial, no reversal and no
						// cancel with issued material — refusing here would strand it forever).
						return entity.ErrProductionRunConcurrentModification
					}
					perVariantGood[vid] += in.GoodQty
				}
			} else {
				// Anything that BOOKS stock needs a product linked to the run's card and a size from
				// its grid: good units (A stock) and seconds-dispositioned defects (B stock) alike.
				// A scrap-defect-only count is a recorded fact, not stock — it may land on a
				// still-unpublished (product-less) planning line (adversarial #3: without this,
				// seconds slipped past the link/size gates good units go through).
				booksStock := in.GoodQty > 0 ||
					(in.DefectQty > 0 && in.DefectDisposition == entity.DefectDispositionSeconds)
				if booksStock {
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
						// A line without a size cannot be booked as product_size stock.
						return entity.ErrProductionRunLineSizeUnlinked
					}
				}
			}
			counted = append(counted, countedLine{line: ln, in: in})
			totalGood += in.GoodQty
			totalDefect += in.DefectQty
		}
		if totalGood == 0 && totalDefect == 0 {
			// The one legal empty receipt is a FINAL on a partially received run: the operator
			// declares the series complete without a last delivery ("short-close" — the remainder
			// simply never arrived). Its posting is the true-up carrier. Anything else counted
			// nothing and books nothing — refuse.
			if !(p.Final && cur.Status == string(entity.ProductionRunPartiallyReceived)) {
				return entity.ErrProductionRunNothingReceived
			}
		}

		// Maintain the plan-grid rollups: received_qty/defect_qty are Σ over the run's receipts
		// (Phase 5), so a counted line ACCUMULATES this delivery on top of what earlier receipts
		// booked. On the FINAL receipt every never-counted line is stamped to an explicit 0 — the
		// run is declared complete, so "nothing ever arrived" is a final fact, not an unknowable
		// NULL (the Phase 4 rule, now scoped to the close of the receipt series).
		countsByID := make(map[int]entity.ProductionRunReceiptLineInput, len(counted))
		for _, c := range counted {
			countsByID[c.line.Id] = c.in
		}
		for i := range lines {
			in, countedNow := countsByID[lines[i].Id]
			switch {
			case countedNow:
				// The shim's counts ARE the stored rollups (old-client totals) — accumulating would
				// add them to themselves; the command API always carries deltas.
				rollupSQL := `
					UPDATE production_run_line
					SET received_qty = COALESCE(received_qty, 0) + :g,
					    defect_qty = COALESCE(defect_qty, 0) + :d
					WHERE id = :id`
				if p.LegacyTotals {
					rollupSQL = `
					UPDATE production_run_line
					SET received_qty = :g, defect_qty = :d
					WHERE id = :id`
				}
				if err := storeutil.ExecNamed(ctx, db, rollupSQL,
					map[string]any{"g": in.GoodQty, "d": in.DefectQty, "id": lines[i].Id}); err != nil {
					return fmt.Errorf("failed to stamp receipt counts on run line %d: %w", lines[i].Id, err)
				}
				if p.LegacyTotals {
					lines[i].ReceivedQty = sql.NullInt64{Int64: int64(in.GoodQty), Valid: true}
					lines[i].DefectQty = sql.NullInt64{Int64: int64(in.DefectQty), Valid: true}
				} else {
					lines[i].ReceivedQty = sql.NullInt64{Int64: lines[i].ReceivedQty.Int64 + int64(in.GoodQty), Valid: true}
					lines[i].DefectQty = sql.NullInt64{Int64: lines[i].DefectQty.Int64 + int64(in.DefectQty), Valid: true}
				}
			case p.Final:
				if err := storeutil.ExecNamed(ctx, db, `
					UPDATE production_run_line
					SET received_qty = COALESCE(received_qty, 0), defect_qty = COALESCE(defect_qty, 0)
					WHERE id = :id`, map[string]any{"id": lines[i].Id}); err != nil {
					return fmt.Errorf("failed to finalize receipt counts on run line %d: %w", lines[i].Id, err)
				}
				if !lines[i].ReceivedQty.Valid {
					lines[i].ReceivedQty = sql.NullInt64{Int64: 0, Valid: true}
				}
				if !lines[i].DefectQty.Valid {
					lines[i].DefectQty = sql.NullInt64{Int64: 0, Valid: true}
				}
			}
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
		// Valuation happens ONLY when the series closes (final-review BLOCKER): a partial receipt's
		// "run total ÷ units so far" is inflated whenever costs are front-loaded (materials are
		// issued before the first delivery), and that figure fed cost_price → order_item COGS. A
		// partial receipt therefore freezes NO unit cost and seeds nothing; the FINAL receipt values
		// the whole series at once — minus the abnormal-scrap share the ledger writes off to 5040
		// (final-review HIGH: otherwise the loss is expensed twice, once on 5040 and once inside
		// COGS, and 1130 drains negative).
		var unitCost decimal.NullDecimal
		if p.Final {
			unitCost = runShape.ActualUnitCostBase()
			if unitCost.Valid {
				goodUnits := runShape.NetReceivedQty()
				allReceived := 0
				for i := range lines {
					allReceived += int(lines[i].ReceivedQty.Int64) + int(lines[i].DefectQty.Int64)
				}
				priorScrap, err := storeutil.QueryCountNamed(ctx, db, `
					SELECT COALESCE(SUM(rl.defect_qty), 0) FROM production_run_receipt pr
					JOIN production_run_receipt_line rl ON rl.receipt_id = pr.id
					WHERE pr.run_id = :id AND pr.reversed_by IS NULL AND pr.reversal_of IS NULL
					  AND rl.defect_disposition = 'scrap'`, map[string]any{"id": p.RunID})
				if err != nil {
					return fmt.Errorf("failed to sum prior scrap for valuation: %w", err)
				}
				allScrap := priorScrap
				for _, c := range counted {
					if c.in.DefectQty > 0 && c.in.DefectDisposition != entity.DefectDispositionSeconds {
						allScrap += c.in.DefectQty
					}
				}
				// Aux runs post NO write-off in the ledger (their P1 has no FG side at all), so
				// their valuation must NOT subtract one — M2 relieves the FULL run cost into the
				// output material, defects included, exactly as before.
				rate := p.NormalLossRate
				if rate.LessThanOrEqual(decimal.Zero) || rate.GreaterThanOrEqual(decimal.NewFromInt(1)) {
					rate = decimal.NewFromFloat(0.05) // mirror acctposting's default clamp
				}
				if total, ok := runShape.ActualTotalCostBase(); ok && !p.Aux && goodUnits > 0 && allReceived > 0 && allScrap > 0 {
					// Mirror of BuildProductionReceiveEntry's split: allowance = floor(received ×
					// rate); the abnormal excess share leaves the run's value via the write-off —
					// CAPPED at what is still on WIP after the partials' FG relief, exactly as the
					// builder caps writeOff at `remaining` (fix-verify F2: an over-delivered run's
					// partials may have relieved everything; the ledger then writes off nothing and
					// the valuation must not subtract either).
					fgRow, err := storeutil.QueryNamedOne[struct {
						V decimal.Decimal `db:"v"`
					}](ctx, db, `
						SELECT COALESCE(SUM(posted_fg_base), 0) AS v FROM production_run_receipt
						WHERE run_id = :id`, map[string]any{"id": p.RunID})
					if err != nil {
						return fmt.Errorf("failed to sum posted fg for valuation cap: %w", err)
					}
					allowance := decimal.NewFromInt(int64(allReceived)).Mul(rate).Floor()
					abnormal := decimal.NewFromInt(int64(allScrap)).Sub(allowance)
					if abnormal.IsPositive() {
						writeOffShare := total.Mul(abnormal).Div(decimal.NewFromInt(int64(allReceived)))
						remaining := total.Sub(fgRow.V)
						if remaining.IsNegative() {
							remaining = decimal.Zero
						}
						if writeOffShare.GreaterThan(remaining) {
							writeOffShare = remaining
						}
						adjusted := total.Sub(writeOffShare)
						if adjusted.IsNegative() {
							adjusted = decimal.Zero
						}
						unitCost = decimal.NullDecimal{
							Decimal: adjusted.Div(decimal.NewFromInt(int64(goodUnits))).RoundBank(2),
							Valid:   true,
						}
					}
				}
			}
		}

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
				(run_id, received_at, admin_username, note, idempotency_key, unit_cost_base, base_currency, has_base, posting_status, final)
			VALUES (:run_id, :received_at, :admin_username, :note, :idempotency_key, :unit_cost_base, :base_currency, :has_base, 'pending', :final)`,
			map[string]any{
				"run_id":          p.RunID,
				"received_at":     now,
				"admin_username":  adminUser,
				"note":            note,
				"idempotency_key": p.IdempotencyKey,
				"unit_cost_base":  unitCost,
				"base_currency":   baseCurrency,
				"has_base":        unitCost.Valid,
				"final":           p.Final,
			})
		if err != nil {
			return fmt.Errorf("failed to insert production run receipt: %w", err)
		}
		for _, c := range counted {
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO production_run_receipt_line
					(receipt_id, run_line_id, product_id, size_id, good_qty, defect_qty, defect_disposition)
				VALUES (:receipt_id, :run_line_id, :product_id, :size_id, :good_qty, :defect_qty, :defect_disposition)`,
				map[string]any{
					"receipt_id":         receiptID,
					"run_line_id":        c.line.Id,
					"product_id":         c.line.ProductId,
					"size_id":            nullIfZero(c.line.SizeId),
					"good_qty":           c.in.GoodQty,
					"defect_qty":         c.in.DefectQty,
					"defect_disposition": c.in.DefectDisposition,
				}); err != nil {
				return fmt.Errorf("failed to insert production run receipt line: %w", err)
			}
		}

		// The defect trace (Phase 7, plan 09): every defected unit leaves an auditable event — how
		// many scrapped (their cost is resolved by the posting rule: normal loss absorbed into the
		// good units, abnormal excess written off) and how many went to B-grade seconds. NO phantom
		// stock movement is written for scrap — this event and the ledger entry ARE its trace. The
		// absorbed-cost figure is the receipt's frozen per-unit valuation × scrapped units — an
		// estimate stamped for the operator; the ledger's split is exact at posting time.
		scrapQty, secondsQty := 0, 0
		for _, c := range counted {
			if c.in.DefectQty == 0 {
				continue
			}
			if c.in.DefectDisposition == entity.DefectDispositionSeconds {
				secondsQty += c.in.DefectQty
			} else {
				scrapQty += c.in.DefectQty
			}
		}
		if scrapQty > 0 || secondsQty > 0 {
			payload := map[string]any{
				"receipt_id":  receiptID,
				"scrap_qty":   scrapQty,
				"seconds_qty": secondsQty,
			}
			if unitCost.Valid && scrapQty > 0 {
				payload["absorbed_cost_base_estimate"] = unitCost.Decimal.Mul(decimal.NewFromInt(int64(scrapQty))).Round(2).String()
			}
			pj, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("failed to marshal units_scrapped payload: %w", err)
			}
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO production_run_event (run_id, event_type, actor, reason, payload)
				VALUES (:run_id, :event_type, :actor, NULL, :payload)`,
				map[string]any{
					"run_id": p.RunID, "event_type": entity.ProductionRunEventUnitsScrapped,
					"actor": adminUser, "payload": string(pj),
				}); err != nil {
				return fmt.Errorf("failed to record units_scrapped event: %w", err)
			}
		}

		// Book the good units. Aux → the output material's warehouse (moving average), one bucket per
		// COLOUR in variant mode; garment → each line's own product stock, products ascending for a
		// deterministic lock order.
		costPriceWrites := 0
		var stockTransitions []entity.StockTransition
		if p.Aux {
			switch {
			case dest.variantMode:
				// One landing per colour, each into its OWN bucket: its own moving average, its own
				// material_price production_run point, its own low-stock answer — which is the entire
				// reason colours exist. All of them at the SAME blended unit cost, because a run has one
				// actual cost and nothing in the data says which colour consumed which share of it.
				type variantBooking struct{ variantID, materialID, good int }
				bookings := make([]variantBooking, 0, len(perVariantGood))
				for vid, good := range perVariantGood {
					if good <= 0 {
						continue // a defect-only colour is a recorded fact, not a landing
					}
					// The bucket comes from the IN-TRANSACTION registry read, so a colour re-pointed at
					// another material after the handler's read lands in the material the card produces
					// into NOW — re-pointing is an explicitly supported edit, and booking the old bucket
					// would move stock, its moving average, its price history and the M2 journal entry
					// somewhere the operator can never unwind (an aux receipt has no reversal).
					bookings = append(bookings, variantBooking{variantID: vid, materialID: dest.variantMaterials[vid], good: good})
				}
				// Materials ASCENDING before the per-material FOR UPDATE inside ReceiveInTx — the same
				// deadlock discipline sort.Ints(productIDs) enforces on the sellable side. Two receipts of
				// two runs sharing a colour would otherwise take the row locks in map-iteration order,
				// which Go deliberately randomises, and deadlock intermittently. uniq_tcov_material makes
				// the material ids distinct; the variant id is the tie-break so the order is total.
				sort.Slice(bookings, func(i, j int) bool {
					if bookings[i].materialID != bookings[j].materialID {
						return bookings[i].materialID < bookings[j].materialID
					}
					return bookings[i].variantID < bookings[j].variantID
				})
				for _, b := range bookings {
					if _, err := inventory.ReceiveInTx(ctx, rep, entity.MaterialReceiptInsert{
						MaterialId:      b.materialID,
						Quantity:        decimal.NewFromInt(int64(b.good)),
						UnitCost:        unitCost,
						ProductionRunId: sql.NullInt32{Int32: int32(p.RunID), Valid: true},
						FromProduction:  true,
						AdminUsername:   p.Username,
					}, now); err != nil {
						return err
					}
				}
			case totalGood > 0:
				// Same freshness rule for the legacy single bucket: the run lock does not cover the CARD
				// row, so output_material_id may have moved since the handler validated it.
				if !dest.legacyMaterialID.Valid {
					return entity.ErrProductionRunConcurrentModification
				}
				if _, err := inventory.ReceiveInTx(ctx, rep, entity.MaterialReceiptInsert{
					MaterialId:      int(dest.legacyMaterialID.Int64),
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
			perProductSeconds := make(map[int]map[int]int)
			for _, c := range counted {
				pid := int(c.line.ProductId.Int32)
				if c.in.GoodQty > 0 {
					if perProduct[pid] == nil {
						perProduct[pid] = make(map[int]int)
					}
					perProduct[pid][c.line.SizeId] += c.in.GoodQty
				}
				if c.in.DefectQty > 0 && c.in.DefectDisposition == entity.DefectDispositionSeconds {
					if perProductSeconds[pid] == nil {
						perProductSeconds[pid] = make(map[int]int)
					}
					perProductSeconds[pid][c.line.SizeId] += c.in.DefectQty
				}
			}
			// A stock first, then B — the SAME order the reversal un-books in (A rows, then B rows):
			// two concurrent commands touching one product from different runs would otherwise take
			// (B,A) vs (A,B) and deadlock (adversarial #6; the Tx retry would mask it as latency).
			productIDs := make([]int, 0, len(perProduct))
			for pid := range perProduct {
				productIDs = append(productIDs, pid)
			}
			sort.Ints(productIDs)
			for _, pid := range productIDs {
				tr, err := rep.Products().ReceiveProductionStock(ctx, pid, perProduct[pid], p.RunID, p.Username, entity.VariantGradeA)
				if err != nil {
					return err
				}
				stockTransitions = append(stockTransitions, tr...)
			}
			// cost_price seeds on the FINAL for EVERY product the whole SERIES delivered (the
			// cumulative rollups), not just this receipt's lines — a short-close final counts
			// nothing itself, and a multi-product run may have delivered product A entirely in an
			// earlier partial (fix-verify F1: seeding only `counted` left those products uncosted,
			// so their sales posted NO COGS at all).
			if p.Final && p.UpdateCostPrice && unitCost.Valid {
				seedIDs := make([]int, 0, len(lines))
				seenPid := make(map[int]bool)
				for i := range lines {
					if !lines[i].ProductId.Valid || lines[i].ReceivedQty.Int64 <= 0 {
						continue
					}
					pid := int(lines[i].ProductId.Int32)
					if !seenPid[pid] {
						seenPid[pid] = true
						seedIDs = append(seedIDs, pid)
					}
				}
				sort.Ints(seedIDs)
				for _, pid := range seedIDs {
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
			// Seconds land in the B-grade variant of the SAME (product, size) — journalled exactly
			// like good units, at zero carried cost v1 (the run's whole cost stays with the A units;
			// the B variant is not sellable until its discount pricing is decided).
			secondsIDs := make([]int, 0, len(perProductSeconds))
			for pid := range perProductSeconds {
				secondsIDs = append(secondsIDs, pid)
			}
			sort.Ints(secondsIDs)
			for _, pid := range secondsIDs {
				tr, err := rep.Products().ReceiveProductionStock(ctx, pid, perProductSeconds[pid], p.RunID, p.Username, entity.VariantGradeB)
				if err != nil {
					return err
				}
				stockTransitions = append(stockTransitions, tr...)
			}
		}

		// The FINAL receipt declares the run complete: status=received + received_at. A partial
		// moves the run to partially_received and leaves received_at NULL — the run is not received
		// yet, and downstream consumers of received_at (accounting occurred_at is the RECEIPT's own
		// received_at, not the run's) must not see a half-done run as done.
		// lock_version bumps on BOTH branches (final-review HIGH): a run-edit form opened before
		// this receipt must CONFLICT, not win — a stale save could flip partially_received back to
		// in_progress and re-arm the legacy shim against the cumulative rollups (double booking).
		if p.Final {
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE production_run SET status = :status, received_at = :received_at,
					lock_version = lock_version + 1 WHERE id = :id`,
				map[string]any{"id": p.RunID, "status": string(entity.ProductionRunReceived), "received_at": now}); err != nil {
				return err
			}
		} else {
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE production_run SET status = :status,
					lock_version = lock_version + 1 WHERE id = :id`,
				map[string]any{"id": p.RunID, "status": string(entity.ProductionRunPartiallyReceived)}); err != nil {
				return err
			}
		}
		// The audit trail references the receipt — it never duplicates its data (plan 04: dual
		// truth is the failure mode the event table must not reintroduce).
		statusAfter := string(entity.ProductionRunPartiallyReceived)
		if p.Final {
			statusAfter = string(entity.ProductionRunReceived)
		}
		if err := recordRunEvent(ctx, db, p.RunID, entity.ProductionRunEventReceiptPosted, p.Username, "",
			map[string]any{"receipt_id": receiptID, "final": p.Final, "status_after": statusAfter}); err != nil {
			return err
		}

		result := &entity.PostProductionRunReceiptResult{ReceiptID: receiptID, CostPriceUpdated: costPriceWrites > 0, StockTransitions: stockTransitions}
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
			entity.ErrProductionRunNothingReceived, entity.ErrIdempotencyConflict,
			entity.ErrProductionRunAuxPartial:
			return nil, err
		}
		return nil, fmt.Errorf("can't post production run receipt: %w", err)
	}
	return res, nil
}

// lineVariantID is the colour a plan line produces, or 0 for a colourless one. It treats a Valid-but
// -non-positive id as unset, matching the store boundary that writes such a value as NULL.
func lineVariantID(ln *entity.ProductionRunLine) int {
	if !ln.OutputVariantId.Valid || ln.OutputVariantId.Int32 <= 0 {
		return 0
	}
	return int(ln.OutputVariantId.Int32)
}

// auxDestination is where an auxiliary run's output lands, resolved inside the receipt transaction.
// Either variantMode is set and variantMaterials maps every colour of the card (active OR retired)
// to the bucket it produces into RIGHT NOW, or the run is in legacy single-output mode and
// legacyMaterialID is the card's one bucket (INVALID when the card has none).
type auxDestination struct {
	variantMode      bool
	variantMaterials map[int]int
	legacyMaterialID sql.NullInt64
}

// resolveAuxDestination reads the card's output registry under the run lock and decides how this
// receipt books. It is deliberately the ONLY source of that decision — the handler's read happened
// before the lock and can be arbitrarily stale.
//
// Two rules earn their keep here:
//
//   - RETIRED COLOURS ARE INCLUDED. Retiring a colour means "stop planning this", not "abandon the
//     batch already on the sewing floor". An auxiliary run cannot be received partially, cannot be
//     reversed, and cannot be cancelled while material is issued to it, so refusing its receipt
//     because a colour was retired in the meantime strands the run — and its issued material — with
//     no way out at all. Re-pointing the line at a live colour is not a repair either: it would book
//     the white units into the black bucket. This mirrors the plan-time rule exactly, which lets an
//     existing line keep its colour through a retirement.
//   - VARIANT MODE IS DECIDED BY THE UNION of "the card has an active colour" and "the grid names a
//     colour". The first half catches colours registered after a colourless grid was planned (that
//     grid can no longer be received into one bucket — the counted-line loop says so). The second
//     half catches a run planned on colours whose card has since retired all of them: the grid names
//     buckets, so the buckets are where it lands.
func resolveAuxDestination(ctx context.Context, db dependency.DB, techCardID int, lines []entity.ProductionRunLine) (auxDestination, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Id         int  `db:"id"`
		MaterialId int  `db:"material_id"`
		Active     bool `db:"active"`
	}](ctx, db, `
		SELECT id, material_id, active FROM tech_card_output_variant WHERE tech_card_id = :card`,
		map[string]any{"card": techCardID})
	if err != nil {
		return auxDestination{}, fmt.Errorf("failed to load colour variants of tech card %d for receipt: %w", techCardID, err)
	}
	dest := auxDestination{variantMaterials: make(map[int]int, len(rows))}
	for _, r := range rows {
		dest.variantMaterials[r.Id] = r.MaterialId
		if r.Active {
			dest.variantMode = true
		}
	}
	if !dest.variantMode {
		for i := range lines {
			if lineVariantID(&lines[i]) != 0 {
				dest.variantMode = true
				break
			}
		}
	}
	if dest.variantMode {
		return dest, nil
	}
	// Legacy single-output mode. The run lock covers the run, never the card, so this read is what
	// makes the destination current rather than whatever the handler saw.
	card, err := storeutil.QueryNamedOne[struct {
		OutputMaterialId sql.NullInt64 `db:"output_material_id"`
	}](ctx, db, `SELECT output_material_id FROM tech_card WHERE id = :id`, map[string]any{"id": techCardID})
	if err != nil {
		return auxDestination{}, fmt.Errorf("failed to load output material of tech card %d for receipt: %w", techCardID, err)
	}
	dest.legacyMaterialID = card.OutputMaterialId
	return dest, nil
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

// nullIfNoVariant maps "no colour" onto the nullable output_variant_id column (0253). A non-positive
// id is unset however it was spelled — INVALID, or the {0, Valid: true} a proto3 zero becomes when a
// mapper marks it present — so a caller can never smuggle a 0 past validation into the foreign key.
func nullIfNoVariant(v sql.NullInt32) any {
	if !v.Valid || v.Int32 <= 0 {
		return nil
	}
	return v.Int32
}

// loadRunReceipts returns a run's receipts oldest-first, each with its counted lines (joined with
// the plan line's stable line_key so the client correlates without row ids).
func loadRunReceipts(ctx context.Context, db dependency.DB, runID int) ([]entity.ProductionRunReceipt, error) {
	receipts, err := storeutil.QueryListNamed[entity.ProductionRunReceipt](ctx, db, `
		SELECT id, run_id, received_at, admin_username, note, idempotency_key, unit_cost_base,
		       base_currency, has_base, reversal_of, reversed_by, posting_status, final, created_at
		FROM production_run_receipt WHERE run_id = :run_id ORDER BY received_at, id`,
		map[string]any{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("can't load production run receipts: %w", err)
	}
	for i := range receipts {
		lines, err := storeutil.QueryListNamed[entity.ProductionRunReceiptLine](ctx, db, `
			SELECT rl.id, rl.receipt_id, rl.run_line_id, rl.product_id, rl.size_id, rl.good_qty,
			       rl.defect_qty, rl.defect_disposition, COALESCE(l.line_key, '') AS line_key
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

// CleanupExpiredCommandIdempotency deletes command_idempotency rows older than 90 days (bounded
// per call so a backlog cannot stall the cleanup tick; the remainder goes next tick). The key's
// only job is to replay a client RETRY of the same intent — retries live for minutes, the modal
// session that minted the key for hours — so 90 days is far beyond any legitimate replay window.
// After the purge a replay of a purged key would EXECUTE anew rather than replay; for receipts
// that is a second physical receipt, which is exactly what a three-months-later resubmission of
// the same counts is. There is deliberately NO index on created_at: the table only ever holds a
// few rows per receipt ever posted, and the bounded scan is cheaper than carrying an index on
// every receipt write. CAVEAT for the future: the unindexed DELETE holds gap locks across the
// table for the statement — fine at receipts-only volume, but if a second, higher-volume
// command_type ever joins this table, add the created_at index THEN (the purge would otherwise
// stall concurrent claimIdempotency INSERTs on the hot path).
func (s *Store) CleanupExpiredCommandIdempotency(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx,
		`DELETE FROM command_idempotency WHERE created_at < DATE_SUB(UTC_TIMESTAMP(), INTERVAL 90 DAY) LIMIT 10000`)
	if err != nil {
		return 0, fmt.Errorf("cleanup expired command idempotency: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("cleanup expired command idempotency rows affected: %w", err)
	}
	return n, nil
}
