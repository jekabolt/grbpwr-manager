// Package productionrun implements production-run (партия) management: the run header, its
// per-size planned/received/defect grid, and (later phases) actual costs and stock integration.
// A run snapshots its planned unit cost at plan time so it stops tracking edits to the tech card.
package productionrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// Pagination bounds for the list endpoint.
const (
	defaultPageLimit = 50
	maxPageLimit     = 100
)

// TxFunc runs f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.ProductionRuns.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new production-run store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// received_at is deliberately absent from the write columns: it is stamped only by the receive flow,
// in the same transaction as the stock it books, and nothing else may set or clear it.
// planned_start_at / promised_at ARE written here, unlike received_at: they are planning intent
// the operator owns, not a stamped fact behind which stock moved.
const runColumns = `tech_card_id, release_id, status, started_at, planned_start_at, promised_at,
	planned_unit_cost, planned_currency, marker_efficiency_pct, marker_notes, actual_wastage_percent, notes, supplier_id`

const runValues = `:tech_card_id, :release_id, :status, :started_at, :planned_start_at, :promised_at,
	:planned_unit_cost, :planned_currency, :marker_efficiency_pct, :marker_notes, :actual_wastage_percent, :notes, :supplier_id`

func runParams(r *entity.ProductionRunInsert) map[string]any {
	return map[string]any{
		"tech_card_id":           r.TechCardId,
		"release_id":             r.ReleaseId,
		"status":                 string(r.Status),
		"started_at":             r.StartedAt,
		"planned_start_at":       r.PlannedStartAt,
		"promised_at":            r.PromisedAt,
		"planned_unit_cost":      r.PlannedUnitCost,
		"planned_currency":       r.PlannedCurrency,
		"marker_efficiency_pct":  r.MarkerEfficiencyPct,
		"marker_notes":           r.MarkerNotes,
		"actual_wastage_percent": r.ActualWastagePercent,
		"notes":                  r.Notes,
		"supplier_id":            r.SupplierId,
	}
}

// recordRunEvent appends one row to the run's audit trail (production_run_event, Phase 8) on the
// caller's connection/tx. payload may be nil; it is marshalled here so writers stay one-liners.
func recordRunEvent(ctx context.Context, db dependency.DB, runID int, eventType, actor, reason string, payload map[string]any) error {
	var pj sql.NullString
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal %s event payload: %w", eventType, err)
		}
		pj = sql.NullString{String: string(b), Valid: true}
	}
	var actorNS, reasonNS sql.NullString
	if actor != "" {
		actorNS = sql.NullString{String: actor, Valid: true}
	}
	if reason != "" {
		reasonNS = sql.NullString{String: reason, Valid: true}
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO production_run_event (run_id, event_type, actor, reason, payload)
		VALUES (:run_id, :event_type, :actor, :reason, :payload)`,
		map[string]any{"run_id": runID, "event_type": eventType, "actor": actorNS, "reason": reasonNS, "payload": pj}); err != nil {
		return fmt.Errorf("failed to record %s run event: %w", eventType, err)
	}
	return nil
}

// CreateProductionRun inserts a run and its size grid, returning the new id. PlannedUnitCost/
// PlannedCurrency are expected to be already snapshotted by the caller.
func (s *Store) CreateProductionRun(ctx context.Context, r *entity.ProductionRunInsert) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Every colour the grid names must be one of THIS card's, and one it still makes. Nothing is
		// grandfathered on a create — every line here is new.
		if err := validateRunLineVariants(ctx, rep.DB(), 0, r.TechCardId, r.Lines); err != nil {
			return err
		}
		var err error
		id, err = storeutil.ExecNamedLastId(ctx, rep.DB(),
			fmt.Sprintf(`INSERT INTO production_run (%s) VALUES (%s)`, runColumns, runValues),
			runParams(r))
		if err != nil {
			return fmt.Errorf("failed to insert production run: %w", err)
		}
		if err := insertRunLines(ctx, rep.DB(), id, r.Lines); err != nil {
			return err
		}
		if err := insertRunCosts(ctx, rep.DB(), id, r.Costs); err != nil {
			return err
		}
		return recordRunEvent(ctx, rep.DB(), id, entity.ProductionRunEventCreated, r.Actor, "",
			map[string]any{"status": string(r.Status)})
	})
	if err != nil {
		// The colour-linkage refusals are caller-fixable preconditions and are returned bare, so the
		// handler's message is the one the operator reads.
		if errors.Is(err, entity.ErrProductionRunLineVariantUnlinked) ||
			errors.Is(err, entity.ErrProductionRunLineVariantRetired) ||
			errors.Is(err, entity.ErrProductionRunLineVariantMixedGrid) {
			return 0, err
		}
		return 0, fmt.Errorf("can't create production run: %w", err)
	}
	return id, nil
}

// UpdateProductionRun updates a run's header, diffs its line grid by line_key (0230 — matched rows
// are updated in place so their ids survive; see upsertRunLines) and full-replaces its cost
// articles. The planned-cost snapshot (planned_unit_cost/planned_currency) is intentionally NOT
// written here — it is frozen at plan time. It first locks the run FOR UPDATE and enforces the
// status invariants that keep received facts and warehouse WIP honest:
//   - a received/closed run is immutable (its booked stock and seeded cost_price are applied facts,
//     exactly as DeleteProductionRun refuses) → ErrProductionRunReceivedImmutable;
//   - status=received cannot be set here (only ReceiveProductionRun books the stock behind it) →
//     ErrProductionRunReceiveViaUpdate;
//   - moving an open run to a terminal state (cancelled/closed) while material is still issued to it
//     would drop that material out of WIP with no receive or write-off → ErrProductionRunHasOpenIssues.
//
// Existence is established by the FOR UPDATE read (not by rows-affected, which is 0 for a no-op
// header edit and would spuriously read as NotFound — the receive-v2 flow only touches line rows).
// Returns sql.ErrNoRows when no run exists.
// The incoming cost articles must arrive UNFOLDED (amount_base unset unless the client supplied a
// deliberate override): folding happens inside the transaction, after the stored bases have been
// carried over, so an unchanged article keeps the base it was booked at instead of being re-valued
// at today's rate. fx is the rate set to fold the genuinely new/changed articles with.
func (s *Store) UpdateProductionRun(ctx context.Context, id int, r *entity.ProductionRunInsert, expectedLockVersion entity.LockGuard, fx dto.CostingFx) error {
	return s.updateProductionRun(ctx, id, r, expectedLockVersion, false, fx)
}

// UpdateProductionRunPreservingCosts reloads the stored cost articles after locking the run and
// carries them through the full-replace. It is the cost-blind update path: any preservation read
// failure aborts the same transaction before destructive child deletes can run.
func (s *Store) UpdateProductionRunPreservingCosts(ctx context.Context, id int, r *entity.ProductionRunInsert, expectedLockVersion entity.LockGuard) error {
	return s.updateProductionRun(ctx, id, r, expectedLockVersion, true, dto.CostingFx{})
}

func (s *Store) updateProductionRun(ctx context.Context, id int, r *entity.ProductionRunInsert,
	expectedLockVersion entity.LockGuard, preserveCosts bool, fx dto.CostingFx) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			Status      string `db:"status"`
			TechCardId  int    `db:"tech_card_id"`
			LockVersion int    `db:"lock_version"`
		}](ctx, rep.DB(), `SELECT status, tech_card_id, lock_version FROM production_run WHERE id = :id FOR UPDATE`, map[string]any{"id": id})
		if err != nil {
			if err == sql.ErrNoRows {
				return sql.ErrNoRows
			}
			return fmt.Errorf("failed to load production run for update: %w", err)
		}
		if cur.Status == string(entity.ProductionRunReceived) || cur.Status == string(entity.ProductionRunClosed) {
			// received → closed is the ONE legal edit of a received run (plan 05: closing was a dead
			// end). Status-only by construction: nothing else of the immutable run is written.
			if cur.Status == string(entity.ProductionRunReceived) && r.Status == entity.ProductionRunClosed {
				if expectedLockVersion.Conflicts(cur.LockVersion) {
					return entity.ErrProductionRunConflict
				}
				net, err := netIssuedToRun(ctx, rep.DB(), id)
				if err != nil {
					return err
				}
				if net.GreaterThan(decimal.Zero) {
					return entity.ErrProductionRunHasOpenIssues
				}
				if err := storeutil.ExecNamed(ctx, rep.DB(), `
					UPDATE production_run SET status = :status, lock_version = lock_version + 1
					WHERE id = :id`, map[string]any{"id": id, "status": string(entity.ProductionRunClosed)}); err != nil {
					return fmt.Errorf("failed to close production run: %w", err)
				}
				return recordRunEvent(ctx, rep.DB(), id, entity.ProductionRunEventClosed, r.Actor, "",
					map[string]any{"from": cur.Status, "to": string(entity.ProductionRunClosed), "lock_version_after": cur.LockVersion + 1})
			}
			return entity.ErrProductionRunReceivedImmutable
		}
		if r.Status == entity.ProductionRunReceived {
			return entity.ErrProductionRunReceiveViaUpdate
		}
		// partially_received is receipt-owned exactly like received: an update may only ECHO it (a
		// round-tripping form), never introduce it — and a partially received run cannot jump to a
		// terminal state either: its receipts booked real stock; post the final receipt (or reverse
		// in Phase 6) instead of cancelling the facts away.
		if r.Status == entity.ProductionRunPartiallyReceived && cur.Status != string(entity.ProductionRunPartiallyReceived) {
			return entity.ErrProductionRunReceiveViaUpdate
		}
		if cur.Status == string(entity.ProductionRunPartiallyReceived) &&
			(r.Status == entity.ProductionRunCancelled || r.Status == entity.ProductionRunClosed) {
			return entity.ErrProductionRunPartialTerminal
		}
		// Optimistic lock (#9, presence-corrected in Ф6.5): a SUPPLIED expected version that no longer
		// matches means the run was edited concurrently — reject rather than clobber the other writer's
		// full-replace. What opts out is now the ABSENCE of a token, never its magnitude: a fresh run
		// is born at lock_version 0, so the old `> 0` test left every run's first save unguarded and
		// two tabs both echoing that 0 silently overwrote each other (see entity.LockGuard). The FOR
		// UPDATE read above serialises this against a concurrent update, so the in-Go check is
		// authoritative; the WHERE guard on the UPDATE is belt-and-suspenders (mirrors UpdateTechCard).
		if expectedLockVersion.Conflicts(cur.LockVersion) {
			return entity.ErrProductionRunConflict
		}
		// The run's style is fixed at creation: the planned-cost snapshot, the movements' denormalised
		// tech_card_id and the style roll-ups are all anchored to it (g25-13).
		if r.TechCardId != cur.TechCardId {
			return entity.ErrProductionRunCardChange
		}
		// Colour linkage against the STORED card (the run cannot move, per the check above). A colour
		// the run already references survives a retirement; a colour it does not is planned fresh and
		// must still be one this card makes.
		if err := validateRunLineVariants(ctx, rep.DB(), id, cur.TechCardId, r.Lines); err != nil {
			return err
		}
		// The stored articles are read under the run lock either way: the cost-blind path carries them
		// through wholesale, the cost-writing path uses them to keep each unchanged article's already
		// folded amount_base instead of re-folding it at today's FX rate. Preserve MUST run before the
		// fold: folding first marks every incoming base Valid and leaves Preserve nothing to fill —
		// which is exactly how the handler-side fold made this whole mechanism inert.
		storedCosts, err := loadRunCosts(ctx, rep.DB(), id)
		if err != nil {
			return fmt.Errorf("load stored production run %d costs: %w", id, err)
		}
		if preserveCosts {
			r.Costs = storedCosts
		} else {
			dto.PreserveProductionRunCostBases(r.Costs, storedCosts)
			dto.FoldProductionRunCostsToBase(r.Costs, fx)
		}
		// Receipt-owned counters (Phase 5): once the run has a live receipt, received_qty/defect_qty
		// are Σ over receipts, maintained by the receipt command — a form echo (possibly read before
		// another delivery landed) must not clobber them. Overlay the STORED counters by line_key;
		// a line new to this payload has no stored fact and keeps whatever the payload says (the DTO
		// zeroes counts on new lines anyway).
		receiptProbe, err := storeutil.QueryNamedOne[struct {
			E bool `db:"e"`
		}](ctx, rep.DB(), `
			SELECT EXISTS(SELECT 1 FROM production_run_receipt
			              WHERE run_id = :id AND reversed_by IS NULL AND reversal_of IS NULL) AS e`,
			map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to probe run receipts: %w", err)
		}
		if receiptProbe.E {
			stored, err := loadRunLines(ctx, rep.DB(), id)
			if err != nil {
				return err
			}
			counters := make(map[string]entity.ProductionRunLine, len(stored))
			for i := range stored {
				counters[stored[i].LineKey] = stored[i]
			}
			for i := range r.Lines {
				if st, ok := counters[strings.TrimSpace(r.Lines[i].LineKey)]; ok {
					r.Lines[i].ReceivedQty = st.ReceivedQty
					r.Lines[i].DefectQty = st.DefectQty
				}
			}
		}
		if r.Status == entity.ProductionRunCancelled || r.Status == entity.ProductionRunClosed {
			net, err := netIssuedToRun(ctx, rep.DB(), id)
			if err != nil {
				return err
			}
			if net.GreaterThan(decimal.Zero) {
				return entity.ErrProductionRunHasOpenIssues
			}
		}
		params := runParams(r)
		params["id"] = id
		params["expected_lock_version"] = expectedLockVersion.Version()
		lockGuard := ""
		if expectedLockVersion.Present() {
			lockGuard = " AND lock_version = :expected_lock_version"
		}
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE production_run SET
				lock_version = lock_version + 1,
				tech_card_id = :tech_card_id, release_id = :release_id, status = :status,
				started_at = :started_at,
				planned_start_at = :planned_start_at, promised_at = :promised_at,
				supplier_id = :supplier_id,
				marker_efficiency_pct = :marker_efficiency_pct, marker_notes = :marker_notes,
				actual_wastage_percent = :actual_wastage_percent, notes = :notes
			WHERE id = :id`+lockGuard, params)
		if err != nil {
			return fmt.Errorf("failed to update production run: %w", err)
		}
		// The row provably exists (loaded above). With the lock guard present, 0 rows means the version
		// moved under us — make the WHERE guard load-bearing, not just the in-Go check. This is NOT the
		// RowsAffected-counts-changed-rows trap: the statement always writes lock_version + 1, so a
		// matched row can never report 0 changed rows however byte-identical the rest of the save is.
		if expectedLockVersion.Present() && rows == 0 {
			return entity.ErrProductionRunConflict
		}
		// Status transitions leave an attributed audit fact (Phase 8): started (→ in_progress),
		// cancelled, closed. received/partially_received never pass this path (receipt-owned).
		if string(r.Status) != cur.Status {
			var evType string
			switch r.Status {
			case entity.ProductionRunInProgress:
				evType = entity.ProductionRunEventStarted
			case entity.ProductionRunCancelled:
				evType = entity.ProductionRunEventCancelled
			case entity.ProductionRunClosed:
				evType = entity.ProductionRunEventClosed
			}
			if evType != "" {
				if err := recordRunEvent(ctx, rep.DB(), id, evType, r.Actor, "",
					map[string]any{"from": cur.Status, "to": string(r.Status), "lock_version_after": cur.LockVersion + 1}); err != nil {
					return err
				}
			}
		}
		// Costs are still full-replaced: they have no wire identity yet (Phase 5 gives receipts the
		// durable money records). Lines are NOT replaced — they are diffed by line_key so their ids
		// survive (0230). Markers are gone entirely (Phase 2 review cut: the surface was write-only).
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM production_run_cost WHERE run_id = :id`, map[string]any{"id": id}); err != nil {
			return fmt.Errorf("failed to clear production_run_cost: %w", err)
		}
		if err := upsertRunLines(ctx, rep.DB(), id, r.Lines); err != nil {
			return err
		}
		return insertRunCosts(ctx, rep.DB(), id, r.Costs)
	})
	if err != nil {
		switch err {
		case sql.ErrNoRows, entity.ErrProductionRunReceivedImmutable,
			entity.ErrProductionRunReceiveViaUpdate, entity.ErrProductionRunHasOpenIssues,
			entity.ErrProductionRunCardChange, entity.ErrProductionRunConflict,
			entity.ErrProductionRunPartialTerminal:
			return err
		}
		// Wrapped (they carry the offending colour's id), so they are matched by identity, not equality.
		if errors.Is(err, entity.ErrProductionRunLineVariantUnlinked) ||
			errors.Is(err, entity.ErrProductionRunLineVariantRetired) ||
			errors.Is(err, entity.ErrProductionRunLineVariantMixedGrid) {
			return err
		}
		return fmt.Errorf("can't update production run: %w", err)
	}
	return nil
}

// netIssuedToRun returns the net quantity of material currently issued to a run (issue_production
// minus return_production). A positive value means material is still out on the run.
func netIssuedToRun(ctx context.Context, db dependency.DB, runID int) (decimal.Decimal, error) {
	net, err := storeutil.QueryNamedOne[struct {
		Net decimal.Decimal `db:"net"`
	}](ctx, db, `
		SELECT COALESCE(SUM(CASE
			WHEN movement_type = 'issue_production'  THEN quantity
			WHEN movement_type = 'return_production' THEN -quantity
			ELSE 0 END), 0) AS net
		FROM material_stock_movement WHERE production_run_id = :id`, map[string]any{"id": runID})
	if err != nil {
		return decimal.Zero, fmt.Errorf("failed to sum net issued material for run %d: %w", runID, err)
	}
	return net.Net, nil
}

// DeleteProductionRun deletes a run by id (size grid cascades). It refuses to delete a run that has
// already been received/closed: that run's stock increment and any cost_price it seeded are applied
// facts, and dropping the run would orphan them. Returns entity.ErrProductionRunReceivedImmutable in
// that case and sql.ErrNoRows when the run does not exist. Load-then-guard is sufficient here
// (admin-only, low concurrency; a received run never transitions back to deletable).
func (s *Store) DeleteProductionRun(ctx context.Context, id int) error {
	cur, err := storeutil.QueryNamedOne[struct {
		Status string `db:"status"`
	}](ctx, s.DB, `SELECT status FROM production_run WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if err == sql.ErrNoRows {
			return sql.ErrNoRows
		}
		return fmt.Errorf("failed to load production run for delete: %w", err)
	}
	if cur.Status == string(entity.ProductionRunReceived) || cur.Status == string(entity.ProductionRunClosed) ||
		cur.Status == string(entity.ProductionRunPartiallyReceived) {
		return entity.ErrProductionRunReceivedImmutable
	}
	// A fully-reversed run is back to in_progress, but its receipt HISTORY (reversed originals +
	// reversal rows) remains and the receipt FK is RESTRICT by design — refuse with the real
	// reason instead of surfacing an opaque FK error (adversarial #8).
	receipts, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM production_run_receipt WHERE run_id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("failed to count run receipts for delete: %w", err)
	}
	if receipts > 0 {
		return entity.ErrProductionRunHasReceiptHistory
	}
	// Refuse if material was issued to the run — those movements are applied stock facts (the FK is
	// ON DELETE SET NULL, so a delete would orphan them). Return the material first.
	moved, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM material_stock_movement WHERE production_run_id = :id`, map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("failed to check production run material movements: %w", err)
	}
	if moved > 0 {
		return entity.ErrProductionRunHasMovements
	}
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM production_run WHERE id = :id`, map[string]any{"id": id}); err != nil {
		return fmt.Errorf("failed to delete production run: %w", err)
	}
	return nil
}

// GetProductionRun returns a run with its size grid, or sql.ErrNoRows when none exists.
func (s *Store) GetProductionRun(ctx context.Context, id int) (*entity.ProductionRun, error) {
	run, err := storeutil.QueryNamedOne[entity.ProductionRun](ctx, s.DB,
		`SELECT * FROM production_run WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("can't get production run: %w", err)
	}
	lines, err := s.runLines(ctx, id)
	if err != nil {
		return nil, err
	}
	run.Lines = lines
	costs, err := s.runCosts(ctx, id)
	if err != nil {
		return nil, err
	}
	run.Costs = costs
	movements, err := s.runMaterialMovements(ctx, id)
	if err != nil {
		return nil, err
	}
	run.MaterialMovements = movements
	receipts, err := loadRunReceipts(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	run.Receipts = receipts
	events, err := storeutil.QueryListNamed[entity.ProductionRunEvent](ctx, s.DB, `
		SELECT id, run_id, event_type, actor, reason, payload, created_at
		FROM production_run_event WHERE run_id = :id ORDER BY id`, map[string]any{"id": id})
	if err != nil {
		return nil, fmt.Errorf("can't load production run events: %w", err)
	}
	run.Events = events
	// The recon checks are advisory: a failure to COMPUTE them must degrade to "checks
	// unavailable", never 500 the whole run screen (adversarial #5).
	recon, err := s.runReconciliation(ctx, &run)
	if err != nil {
		slog.Default().WarnContext(ctx, "can't compute run reconciliation; run served without checks",
			slog.Int("run_id", id), slog.String("err", err.Error()))
	} else {
		run.Recon = recon
	}
	return &run, nil
}

// runReconciliation cross-checks the run's derived state against its journals (plan 04 §4.2 /
// 12.5): every figure on the run screen must trace to a journal row, and the checks below say OUT
// LOUD when they do not. Three typed checks, computed on read, nothing stored:
//
//  1. units_receipts_vs_stock_journal — Σ good units over live receipts == net product-stock
//     journal for this run's reference family (production_received − production_reversed, A grade;
//     B seconds are checked within the same sums via their own journalled rows).
//  2. money_posted_vs_entries — every live receipt marked 'posted' still has a LIVE
//     production_receive entry (a reversed-entry receipt is re-posted by the worker; 'posted' with
//     no live entry and no pending re-post means the ledger lost money).
//  3. costs_capitalised — Σ costed manual articles vs Σ live posted_manual_base claims: a shortfall
//     is pending capitalisation (worker lag or skip), an excess means costs shrank after posting.
func (s *Store) runReconciliation(ctx context.Context, run *entity.ProductionRun) ([]entity.ProductionRunReconCheck, error) {
	id := run.Id
	row, err := storeutil.QueryNamedOne[struct {
		IsAux          bool            `db:"is_aux"`
		ReceiptGood    int             `db:"receipt_good"`
		ReceiptSeconds int             `db:"receipt_seconds"`
		JournalNet     decimal.Decimal `db:"journal_net"`
		PostedNoEntry  int             `db:"posted_no_entry"`
		LiveReceipts   int             `db:"live_receipts"`
		ManualCosted   decimal.Decimal `db:"manual_costed"`
		ManualClaimed  decimal.Decimal `db:"manual_claimed"`
	}](ctx, s.DB, `
		SELECT
		  COALESCE((SELECT tc.purpose = 'auxiliary' FROM production_run r
		            JOIN tech_card tc ON tc.id = r.tech_card_id WHERE r.id = :id), FALSE) AS is_aux,
		  COALESCE((SELECT SUM(rl.good_qty) FROM production_run_receipt pr
		            JOIN production_run_receipt_line rl ON rl.receipt_id = pr.id
		            WHERE pr.run_id = :id AND pr.reversed_by IS NULL AND pr.reversal_of IS NULL), 0) AS receipt_good,
		  COALESCE((SELECT SUM(rl.defect_qty) FROM production_run_receipt pr
		            JOIN production_run_receipt_line rl ON rl.receipt_id = pr.id
		            WHERE pr.run_id = :id AND pr.reversed_by IS NULL AND pr.reversal_of IS NULL
		              AND rl.defect_disposition = 'seconds'), 0) AS receipt_seconds,
		  COALESCE((SELECT SUM(h.quantity_delta) FROM product_stock_change_history h
		            WHERE h.source IN ('production_received', 'production_reversed')
		              AND (h.reference_id = :run_ref
		                   OR h.reference_id IN (SELECT CONCAT('receipt', CHAR(58), CAST(r2.id AS CHAR CHARACTER SET utf8mb4)) COLLATE utf8mb4_unicode_ci
		                                         FROM production_run_receipt r2 WHERE r2.run_id = :id))), 0) AS journal_net,
		  COALESCE((SELECT COUNT(*) FROM production_run_receipt pr
		            WHERE pr.run_id = :id AND pr.reversed_by IS NULL AND pr.reversal_of IS NULL
		              AND pr.posting_status IN ('posted', 'dead_letter')
		              AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
		                              WHERE e.source_type = 'production_receive' AND e.reversed_by IS NULL
		                                AND (e.source_key = CONCAT('receipt', CHAR(58), CAST(pr.id AS CHAR CHARACTER SET utf8mb4)) COLLATE utf8mb4_unicode_ci
		                                     OR e.source_key LIKE CONCAT('receipt', CHAR(58), CAST(pr.id AS CHAR CHARACTER SET utf8mb4), CHAR(58), 'v%') COLLATE utf8mb4_unicode_ci
		                                     OR ((SELECT COUNT(*) FROM production_run_receipt c WHERE c.run_id = pr.run_id) = 1
		                                         AND (e.source_key = CAST(pr.run_id AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci
		                                              OR e.source_key LIKE CONCAT(CAST(pr.run_id AS CHAR CHARACTER SET utf8mb4), CHAR(58), 'v%') COLLATE utf8mb4_unicode_ci))))), 0) AS posted_no_entry,
		  COALESCE((SELECT COUNT(*) FROM production_run_receipt pr
		            WHERE pr.run_id = :id AND pr.reversed_by IS NULL AND pr.reversal_of IS NULL), 0) AS live_receipts,
		  COALESCE((SELECT SUM(c.amount_base) FROM production_run_cost c
		            WHERE c.run_id = :id AND c.amount_base IS NOT NULL), 0) AS manual_costed,
		  COALESCE((SELECT SUM(pr.posted_manual_base) FROM production_run_receipt pr
		            WHERE pr.run_id = :id), 0) AS manual_claimed`,
		// run_ref is bound from Go so the common arm stays sargable on idx_reference_id and no
		// colon ever enters the SQL text (adversarial #5) — bound VALUES may contain colons freely.
		map[string]any{"id": id, "run_ref": fmt.Sprintf("production_run:%d", id)})
	if err != nil {
		return nil, fmt.Errorf("can't compute run %d reconciliation: %w", id, err)
	}
	// Units: journal net counts BOTH grades (A good units + B seconds rows) — the receipts side
	// must therefore include seconds too.
	expectedUnits := row.ReceiptGood + row.ReceiptSeconds
	units := entity.ProductionRunReconCheck{
		Key:      "units_receipts_vs_stock_journal",
		Expected: fmt.Sprintf("%d", expectedUnits),
		Actual:   row.JournalNet.String(),
		Ok:       row.JournalNet.Equal(decimal.NewFromInt(int64(expectedUnits))),
	}
	if row.IsAux {
		// An auxiliary run's good units land in the MATERIAL warehouse (M2), never in the
		// product-stock journal — comparing against it would be red on every aux run forever,
		// training the operator to ignore the panel (adversarial #2).
		units.Ok = true
		units.Actual = units.Expected
		units.Detail = "auxiliary run: output units live in the material warehouse, not product stock"
	} else if !units.Ok {
		units.Detail = "live receipts and the product-stock journal disagree on units for this run — check reversed receipts and manual stock edits"
	}
	entries := entity.ProductionRunReconCheck{
		Key:      "money_posted_vs_entries",
		Expected: "0",
		Actual:   fmt.Sprintf("%d", row.PostedNoEntry),
		Ok:       row.PostedNoEntry == 0,
	}
	if !entries.Ok {
		entries.Detail = "receipt(s) marked posted have no live ledger entry — the worker will re-post; if this persists, see the accounting dead-letter queue"
	}
	costs := entity.ProductionRunReconCheck{
		Key:      "costs_capitalised",
		Expected: row.ManualCosted.Round(2).String(),
		Actual:   row.ManualClaimed.Round(2).String(),
		Ok:       row.ManualCosted.Round(2).Equal(row.ManualClaimed.Round(2)),
	}
	if !costs.Ok {
		if row.LiveReceipts == 0 {
			costs.Detail = "manual costs exist but nothing was received yet — they capitalise with the first receipt"
			costs.Ok = true // not a discrepancy: capitalisation simply has not started
		} else if row.ManualClaimed.LessThan(row.ManualCosted) {
			costs.Detail = "costed articles exceed what receipts capitalised — the delta posts with the next receipt or worker tick"
		} else {
			costs.Detail = "capitalised total exceeds the current cost articles — costs shrank after posting (see the shrunken-manual caveat on the ledger entry)"
		}
	}
	return []entity.ProductionRunReconCheck{units, entries, costs}, nil
}

// runMaterialMovements loads the material stock ledger rows booked to this run (issues/returns to
// production), ordered by id. It feeds the run's materials-from-stock actual cost and the material
// plan's issued column.
func (s *Store) runMaterialMovements(ctx context.Context, runID int) ([]entity.MaterialMovement, error) {
	return loadRunMovements(ctx, s.DB, runID)
}

const movementColumns = `id, material_id, movement_type, quantity, on_hand_before, on_hand_after,
	unit_cost, currency, unit_cost_base, production_run_id, sample_id, tech_card_id, product_id,
	lot, lot_id, supplier_doc, expected_at, reason, comment, admin_username, occurred_at, created_at`

// loadRunMovements loads a run's material movement ledger on the given db (pool or tx).
func loadRunMovements(ctx context.Context, db dependency.DB, runID int) ([]entity.MaterialMovement, error) {
	mv, err := storeutil.QueryListNamed[entity.MaterialMovement](ctx, db,
		fmt.Sprintf(`SELECT %s FROM material_stock_movement WHERE production_run_id = :run_id ORDER BY id`, movementColumns),
		map[string]any{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("can't load production run material movements: %w", err)
	}
	return mv, nil
}

// attachMovements loads the material movements for a page of runs in one query and attaches them.
// The list must carry the same movements the detail does: materials_from_stock_base, the actual
// total (and therefore actual cost per unit), mixed_materials_sources and has_uncosted_issues are
// ALL derived from them. Without them a warehouse-sourced run listed a fabric-free cost per unit and
// reported both provenance flags as false — the list and the detail disagreed about the same run.
func (s *Store) attachMovements(ctx context.Context, runs []entity.ProductionRun) error {
	if len(runs) == 0 {
		return nil
	}
	ids := make([]int, len(runs))
	idx := make(map[int]int, len(runs))
	for i := range runs {
		ids[i] = runs[i].Id
		idx[runs[i].Id] = i
	}
	rows, err := storeutil.QueryListNamed[entity.MaterialMovement](ctx, s.DB,
		fmt.Sprintf(`SELECT %s FROM material_stock_movement WHERE production_run_id IN (:ids)
		 ORDER BY production_run_id, id`, movementColumns),
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load production run material movements: %w", err)
	}
	for _, m := range rows {
		if !m.ProductionRunId.Valid {
			continue
		}
		if i, ok := idx[int(m.ProductionRunId.Int32)]; ok {
			runs[i].MaterialMovements = append(runs[i].MaterialMovements, m)
		}
	}
	return nil
}

// ListProductionRuns returns runs (header + size grid) matching the filter, newest-first, with
// the total count ignoring pagination.
func (s *Store) ListProductionRuns(ctx context.Context, limit, offset int, filter entity.ProductionRunListFilter) ([]entity.ProductionRun, int, error) {
	limit, offset = clampPagination(limit, offset)

	params := map[string]any{}
	where := ""
	if filter.TechCardId > 0 {
		where += " AND tech_card_id = :tech_card_id"
		params["tech_card_id"] = filter.TechCardId
	}
	if filter.Status != "" {
		where += " AND status = :status"
		params["status"] = string(filter.Status)
	}
	// Stale attention filter (#10): still-open runs older than N days — the same rule as the
	// stale_open_production_run dashboard alert (GetStaleOpenRunCount). Combined with an explicit
	// status filter it just intersects (e.g. status=received + stale_days>0 yields nothing).
	if filter.StaleDays > 0 {
		where += " AND status IN (:staleOpenPlanned, :staleOpenInProgress) AND created_at < :staleCutoff"
		params["staleOpenPlanned"] = string(entity.ProductionRunPlanned)
		params["staleOpenInProgress"] = string(entity.ProductionRunInProgress)
		params["staleCutoff"] = s.Now().AddDate(0, 0, -filter.StaleDays)
	}
	// Overdue filter (production cockpit): still-open runs whose promised delivery date has passed.
	// A run with no promised_at was never promised anything, so it is never overdue — the IS NOT NULL
	// is load-bearing, not decorative. The cutoff is TODAY'S UTC MIDNIGHT, not the current instant:
	// promised_at is entered as a calendar date and stored at UTC midnight, and a batch promised
	// TODAY is not late yet — the client's overdueDays predicate counts whole UTC days the same way,
	// so the filter and the "опаздывает N дн" badge agree for the entire promised day.
	if filter.OverdueOnly {
		now := s.Now().UTC()
		where += " AND promised_at IS NOT NULL AND promised_at < :overdueCutoff" +
			" AND status IN (:overduePlanned, :overdueInProgress)"
		params["overdueCutoff"] = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		params["overduePlanned"] = string(entity.ProductionRunPlanned)
		params["overdueInProgress"] = string(entity.ProductionRunInProgress)
	}

	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		fmt.Sprintf(`SELECT COUNT(*) FROM production_run WHERE 1=1%s`, where), params)
	if err != nil {
		return nil, 0, fmt.Errorf("can't count production runs: %w", err)
	}

	params["limit"] = limit
	params["offset"] = offset
	runs, err := storeutil.QueryListNamed[entity.ProductionRun](ctx, s.DB, fmt.Sprintf(`
		SELECT * FROM production_run
		WHERE 1=1%s
		ORDER BY id DESC
		LIMIT :limit OFFSET :offset`, where), params)
	if err != nil {
		return nil, 0, fmt.Errorf("can't list production runs: %w", err)
	}
	if err := s.attachLines(ctx, runs); err != nil {
		return nil, 0, err
	}
	if err := s.attachCosts(ctx, runs); err != nil {
		return nil, 0, err
	}
	if err := s.attachMovements(ctx, runs); err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}

// runLines loads one run's colour-model × size lines, ordered by product then size (NULL product
// first, so planning lines lead) for a stable display.
func (s *Store) runLines(ctx context.Context, runID int) ([]entity.ProductionRunLine, error) {
	return loadRunLines(ctx, s.DB, runID)
}

// loadRunLines loads a run's colour-model × size lines on the given db (pool or tx).
func loadRunLines(ctx context.Context, db dependency.DB, runID int) ([]entity.ProductionRunLine, error) {
	lines, err := storeutil.QueryListNamed[entity.ProductionRunLine](ctx, db,
		`SELECT id, COALESCE(line_key, '') AS line_key, product_id, output_variant_id,
		        COALESCE(size_id, 0) AS size_id, planned_qty, received_qty, defect_qty
		 FROM production_run_line WHERE run_id = :run_id
		 ORDER BY product_id IS NOT NULL, product_id, output_variant_id, size_id`,
		map[string]any{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("can't load production run lines: %w", err)
	}
	return lines, nil
}

// lineRow scans a run line together with its run_id for the batched list attach.
type lineRow struct {
	RunID int `db:"run_id"`
	entity.ProductionRunLine
}

// attachLines loads the lines for a page of runs in one query and attaches them.
func (s *Store) attachLines(ctx context.Context, runs []entity.ProductionRun) error {
	if len(runs) == 0 {
		return nil
	}
	ids := make([]int, len(runs))
	idx := make(map[int]int, len(runs))
	for i := range runs {
		ids[i] = runs[i].Id
		idx[runs[i].Id] = i
	}
	rows, err := storeutil.QueryListNamed[lineRow](ctx, s.DB,
		`SELECT run_id, id, COALESCE(line_key, '') AS line_key, product_id, output_variant_id,
		        COALESCE(size_id, 0) AS size_id, planned_qty, received_qty, defect_qty
		 FROM production_run_line WHERE run_id IN (:ids)
		 ORDER BY run_id, product_id IS NOT NULL, product_id, output_variant_id, size_id`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load production run lines: %w", err)
	}
	for _, r := range rows {
		if i, ok := idx[r.RunID]; ok {
			runs[i].Lines = append(runs[i].Lines, r.ProductionRunLine)
		}
	}
	return nil
}

// runCosts loads one run's actual cost articles ordered by id (insertion order).
func (s *Store) runCosts(ctx context.Context, runID int) ([]entity.ProductionRunCost, error) {
	return loadRunCosts(ctx, s.DB, runID)
}

// loadRunCosts loads a run's actual cost articles on the given db (pool or tx).
func loadRunCosts(ctx context.Context, db dependency.DB, runID int) ([]entity.ProductionRunCost, error) {
	costs, err := storeutil.QueryListNamed[entity.ProductionRunCost](ctx, db,
		`SELECT id, run_id, kind, description, amount, currency, amount_base, incurred_at,
		        supplier_id, document_ref, vat_rate, vat_amount, ap_status
		 FROM production_run_cost WHERE run_id = :run_id ORDER BY id`,
		map[string]any{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("can't load production run costs: %w", err)
	}
	return costs, nil
}

// attachCosts loads the cost articles for a page of runs in one query and attaches them.
func (s *Store) attachCosts(ctx context.Context, runs []entity.ProductionRun) error {
	if len(runs) == 0 {
		return nil
	}
	ids := make([]int, len(runs))
	idx := make(map[int]int, len(runs))
	for i := range runs {
		ids[i] = runs[i].Id
		idx[runs[i].Id] = i
	}
	costs, err := storeutil.QueryListNamed[entity.ProductionRunCost](ctx, s.DB,
		`SELECT id, run_id, kind, description, amount, currency, amount_base, incurred_at,
		        supplier_id, document_ref, vat_rate, vat_amount, ap_status
		 FROM production_run_cost WHERE run_id IN (:ids) ORDER BY run_id, id`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load production run costs: %w", err)
	}
	for _, c := range costs {
		if i, ok := idx[c.RunId]; ok {
			runs[i].Costs = append(runs[i].Costs, c)
		}
	}
	return nil
}

func insertRunCosts(ctx context.Context, db dependency.DB, runID int, costs []entity.ProductionRunCost) error {
	if len(costs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(costs))
	for _, c := range costs {
		rows = append(rows, map[string]any{
			"run_id":      runID,
			"kind":        string(c.Kind),
			"description": c.Description,
			"amount":      c.Amount,
			"currency":    c.Currency,
			"amount_base": c.AmountBase,
			"incurred_at": c.IncurredAt,
			// Cost-document fields (0234 → Phase 9 read/write): honest AP provenance per article.
			"supplier_id":  c.SupplierId,
			"document_ref": c.DocumentRef,
			"vat_rate":     c.VatRate,
			"vat_amount":   c.VatAmount,
			"ap_status":    c.ApStatus,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "production_run_cost", rows); err != nil {
		return fmt.Errorf("failed to insert production run costs: %w", err)
	}
	return nil
}

func insertRunLines(ctx context.Context, db dependency.DB, runID int, lines []entity.ProductionRunLine) error {
	if len(lines) == 0 {
		return nil
	}
	keys, err := resolveRunLineKeys(lines)
	if err != nil {
		return err
	}
	rows := make([]map[string]any, 0, len(lines))
	for i := range lines {
		rows = append(rows, runLineParams(runID, &lines[i], keys[i]))
	}
	if err := storeutil.BulkInsert(ctx, db, "production_run_line", rows); err != nil {
		return fmt.Errorf("failed to insert production run lines: %w", err)
	}
	return nil
}

// runLineParams maps a plan line to the named params of both the insert and the keyed update. It is
// the single definition of the line's column set, so the diff cannot silently drop a column the way
// a hand-written UPDATE list would (received_qty/defect_qty in particular: the receive modal writes
// counted quantities through an ordinary section save, and losing them would erase counted facts).
func runLineParams(runID int, ln *entity.ProductionRunLine, lineKey string) map[string]any {
	return map[string]any{
		"run_id":     runID,
		"line_key":   lineKey,
		"product_id": ln.ProductId,
		// The colour a product-less aux line produces (0253). NULL for every sellable line and for the
		// single output line of a legacy single-output aux card — chk_prl_variant_xor holds either way,
		// including while the diff parks a mover at product_id = NULL.
		//
		// nullIfNoVariant, not the raw NullInt32: a caller that builds {Int32: 0, Valid: true} — the
		// shape a proto3 zero takes on the way through any hand-written mapper — would otherwise reach
		// the FK as a literal 0 and die as a raw 1452 naming a constraint instead of a colour. 0 means
		// unset here exactly as it does for size_id, and every reader agrees (lineVariantID).
		"output_variant_id": nullIfNoVariant(ln.OutputVariantId),
		"size_id":           nullIfZero(ln.SizeId),
		"planned_qty":       ln.PlannedQty,
		"received_qty":      ln.ReceivedQty,
		"defect_qty":        ln.DefectQty,
	}
}

// validateRunLineVariants enforces, at PLAN time, everything about a run grid's colours that the
// database cannot: that the grid is not half-coloured, that every colour it names is a colour of the
// run's OWN tech card, and that a colour it names FRESH is one the card still makes (0252/0253). It
// runs inside the write transaction, so the registry it reads is the registry the lines commit
// against.
//
// Three rules, and the asymmetry between them is the point:
//
//   - NO MIXED GRID. Once any line names a colour, every product-less line must. A grid that mixes
//     colour lines with the old colourless aux line is plannable but UNRECEIVABLE — the receipt has
//     one bucket per colour and nowhere at all to put the colourless line's units — so allowing it
//     only defers the refusal to the one moment an auxiliary run can no longer be unwound.
//   - BELONGS TO THIS CARD is absolute. A colour of another card (or a stale id, or a colour on a
//     run whose card is sellable and therefore has no colours at all) would send this run's output
//     into someone else's warehouse bucket, blending two physically different articles into one
//     moving average. There is no grandfathering for it.
//   - ACTIVE is required only of a colour a line does not ALREADY carry. Retiring a colour means "we
//     no longer make this", which must stop new plans — but a line planned while the colour was live
//     keeps it, and every later save of that run (a note, a promised date, a round-tripped grid) must
//     keep working. The exemption is per LINE, not per run: a run that already produces black may not
//     use that fact to add a fresh line on a retired white.
//
// storedRunID is 0 on create (nothing is grandfathered) and the run's id on update. The common case
// — a sellable grid or a legacy single-output aux line — costs ZERO extra queries.
func validateRunLineVariants(ctx context.Context, db dependency.DB, storedRunID, techCardID int, lines []entity.ProductionRunLine) error {
	referenced := make([]int, 0, len(lines))
	seen := make(map[int]bool, len(lines))
	for i := range lines {
		id := lineVariantID(&lines[i])
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		referenced = append(referenced, id)
	}
	if len(referenced) == 0 {
		return nil
	}
	// The mixed-grid rule is a pure payload rule, so it is answered before any round trip.
	for i := range lines {
		if lines[i].ProductId.Valid || lineVariantID(&lines[i]) != 0 {
			continue
		}
		return fmt.Errorf("%w: line %q produces no colour while the rest of the grid does",
			entity.ErrProductionRunLineVariantMixedGrid, lines[i].LineKey)
	}
	sort.Ints(referenced)
	rows, err := storeutil.QueryListNamed[struct {
		Id     int  `db:"id"`
		Active bool `db:"active"`
	}](ctx, db, `
		SELECT id, active FROM tech_card_output_variant
		WHERE tech_card_id = :card AND id IN (:ids)`,
		map[string]any{"card": techCardID, "ids": referenced})
	if err != nil {
		return fmt.Errorf("failed to load colour variants of tech card %d for a run grid: %w", techCardID, err)
	}
	active := make(map[int]bool, len(rows))
	for _, r := range rows {
		active[r.Id] = r.Active
	}
	for _, id := range referenced {
		if _, ours := active[id]; !ours {
			return fmt.Errorf("%w: colour variant %d is not a colour of tech card %d",
				entity.ErrProductionRunLineVariantUnlinked, id, techCardID)
		}
	}
	retiredLines := make([]int, 0, len(lines))
	for i := range lines {
		if id := lineVariantID(&lines[i]); id != 0 && !active[id] {
			retiredLines = append(retiredLines, i)
		}
	}
	if len(retiredLines) == 0 {
		return nil
	}
	// Only now is the grandfathering read worth its round trip. It is keyed by LINE identity: the
	// exemption belongs to the row that already carried the colour, not to the run it sits on.
	priorByKey := make(map[string]int)
	if storedRunID > 0 {
		prior, err := storeutil.QueryListNamed[struct {
			LineKey string `db:"line_key"`
			Id      int    `db:"output_variant_id"`
		}](ctx, db, `
			SELECT COALESCE(line_key, '') AS line_key, output_variant_id FROM production_run_line
			WHERE run_id = :run_id AND output_variant_id IS NOT NULL`,
			map[string]any{"run_id": storedRunID})
		if err != nil {
			return fmt.Errorf("failed to load run %d stored colour variants: %w", storedRunID, err)
		}
		for _, p := range prior {
			if p.LineKey != "" {
				priorByKey[p.LineKey] = p.Id
			}
		}
	}
	for _, i := range retiredLines {
		// A keyless line is a NEW line (the DTO mints keys for every RPC payload), so it is never
		// grandfathered — and a keyed line is exempt only when the stored row behind that key already
		// carried this very colour.
		key := strings.TrimSpace(lines[i].LineKey)
		if key == "" || priorByKey[key] != lineVariantID(&lines[i]) {
			return fmt.Errorf("%w: colour variant %d", entity.ErrProductionRunLineVariantRetired, lineVariantID(&lines[i]))
		}
	}
	return nil
}

// resolveRunLineKeys returns the stable identity of each submitted line, minting one for a keyless
// line and rejecting a payload that names the same identity twice (which the keyed diff would
// otherwise collapse onto a single row, silently losing a line).
func resolveRunLineKeys(lines []entity.ProductionRunLine) ([]string, error) {
	keys := make([]string, len(lines))
	seen := make(map[string]bool, len(lines))
	for i := range lines {
		key := strings.TrimSpace(lines[i].LineKey)
		if key == "" {
			// The DTO layer mints keys for every RPC payload; this covers direct callers (seeders,
			// tests) so a line always reaches the table with a durable handle.
			minted, err := entity.MintProductionRunLineKey()
			if err != nil {
				return nil, fmt.Errorf("production run line: %w", err)
			}
			key = minted
		} else if !entity.IsValidProductionRunLineKey(key) {
			// The DTO validates RPC payloads; re-checking here keeps a direct caller's bad key from
			// dying in the driver as a CHAR(26) truncation error that never names the real problem.
			return nil, fmt.Errorf("production run line: invalid line_key %q", key)
		}
		if seen[key] {
			return nil, fmt.Errorf("production run line: duplicate line_key %q in the payload", key)
		}
		seen[key] = true
		keys[i] = key
	}
	return keys, nil
}

// runLineIdentity is a stored line as the keyed diff needs it: the identity to match on, the id that
// must survive the match, and the uniq_prl slot the row currently occupies.
type runLineIdentity struct {
	Id        int           `db:"id"`
	LineKey   string        `db:"line_key"`
	ProductId sql.NullInt32 `db:"product_id"`
	SizeId    int           `db:"size_id"`
}

// upsertRunLines reconciles a run's plan grid by line_key instead of the old delete-all + reinsert
// (production-costing pre-PR, adversarial review B3) — the same keyed upsert-diff the tech-card BOM
// has used since 0159: a line_key already stored is UPDATEd in place, so its id survives, which is
// the entire point (receipt lines will hold a foreign key to that id, and the old full-replace would
// have either dangled them or cascade-deleted the received history on the next edit of the run); a
// new key is INSERTed; a key that vanished from the payload is DELETEd.
//
// The order of the four steps is load-bearing, because production_run_line carries a SECOND unique
// key the diff must not trip over: uniq_prl (run_id, product_id, size_id). A line may legitimately
// move — a size is corrected, or the colour-model that was unpublished at planning time is finally
// attached — and two lines may even swap slots in one save, so "UPDATE every matched row" would
// collide with a row that has not moved out of the way yet. Instead:
//
//  1. DELETE the vanished keys, freeing the slots they held;
//  2. UPDATE every matched row, PARKING the ones that move at product_id = NULL. MySQL never treats
//     a unique-index entry containing NULL as a duplicate, so a parked row occupies no slot and no
//     intermediate state can collide (a row whose final product is NULL needs no parking at all —
//     it is already slot-less);
//  3. INSERT the new keys — every slot they can want is free, held by a row that is staying put, or
//     duplicated in the payload itself (which the DTO rejects);
//  4. UN-PARK: restore the parked rows' product_id, now that every other row sits at its final slot
//     or is slot-less.
func upsertRunLines(ctx context.Context, db dependency.DB, runID int, lines []entity.ProductionRunLine) error {
	stored, err := storeutil.QueryListNamed[runLineIdentity](ctx, db,
		`SELECT id, COALESCE(line_key, '') AS line_key, product_id, COALESCE(size_id, 0) AS size_id
		 FROM production_run_line WHERE run_id = :run_id`, map[string]any{"run_id": runID})
	if err != nil {
		return fmt.Errorf("failed to load existing production run lines: %w", err)
	}
	// Resolve identities before writing anything: the delete set has to be known up front.
	keys, err := resolveRunLineKeys(lines)
	if err != nil {
		return err
	}
	plan := planRunLineDiff(stored, lines, keys)

	// 1. keys that vanished from the payload — one statement: an old client that predates line_key
	// sends every key blank, which retires the WHOLE stored grid here (full churn, as before 0230).
	if len(plan.deletes) > 0 {
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM production_run_line WHERE id IN (:ids)`,
			map[string]any{"ids": plan.deletes}); err != nil {
			return fmt.Errorf("failed to delete production run lines: %w", err)
		}
	}

	// 2. matched keys: UPDATE in place (id survives), parking slot movers.
	for _, u := range plan.updates {
		params := runLineParams(runID, &lines[u.index], keys[u.index])
		params["id"] = u.id
		if u.park {
			params["product_id"] = nil
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE production_run_line SET
				product_id = :product_id, output_variant_id = :output_variant_id,
				size_id = :size_id, planned_qty = :planned_qty,
				received_qty = :received_qty, defect_qty = :defect_qty
			WHERE id = :id`, params); err != nil {
			return fmt.Errorf("failed to update production run line: %w", err)
		}
	}

	// 3. new keys.
	inserts := make([]map[string]any, 0, len(plan.inserts))
	for _, i := range plan.inserts {
		inserts = append(inserts, runLineParams(runID, &lines[i], keys[i]))
	}
	if len(inserts) > 0 {
		if err := storeutil.BulkInsert(ctx, db, "production_run_line", inserts); err != nil {
			return fmt.Errorf("failed to insert production run lines: %w", err)
		}
	}

	// 4. un-park.
	for _, u := range plan.updates {
		if !u.park {
			continue
		}
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE production_run_line SET product_id = :product_id WHERE id = :id`,
			map[string]any{"product_id": lines[u.index].ProductId, "id": u.id}); err != nil {
			return fmt.Errorf("failed to restore production run line product: %w", err)
		}
	}
	return nil
}

// runLineUpdate is one matched line: the payload line (index) that keeps a stored row's id, and
// whether its product_id must be parked at NULL for the duration of the diff because the row changes
// the uniq_prl slot it occupies.
type runLineUpdate struct {
	index int
	id    int
	park  bool
}

// runLineDiff is the whole write plan of upsertRunLines, decided before a single statement runs so
// the ordering argument (see upsertRunLines) can be unit-tested without a database.
type runLineDiff struct {
	deletes []int           // stored ids whose key vanished, ascending
	updates []runLineUpdate // matched rows, in payload order
	inserts []int           // payload indexes with no stored row, in payload order
}

// planRunLineDiff decides delete/update/park/insert for one save. Pure: no DB, no ordering surprises
// (the delete set is sorted, everything else follows payload order), so the parking rules that make
// the diff safe against uniq_prl (run_id, product_id, size_id) are directly testable.
func planRunLineDiff(stored []runLineIdentity, lines []entity.ProductionRunLine, keys []string) runLineDiff {
	existing := make(map[string]runLineIdentity, len(stored))
	for _, row := range stored {
		if row.LineKey == "" {
			continue // pre-0230 row the backfill missed: it has no identity, so it can only be replaced
		}
		existing[row.LineKey] = row
	}
	submitted := make(map[string]bool, len(keys))
	for _, k := range keys {
		submitted[k] = true
	}

	plan := runLineDiff{}
	for _, row := range stored {
		if row.LineKey != "" && submitted[row.LineKey] {
			continue
		}
		plan.deletes = append(plan.deletes, row.Id)
	}
	sort.Ints(plan.deletes)

	for i := range lines {
		row, ok := existing[keys[i]]
		if !ok {
			plan.inserts = append(plan.inserts, i)
			continue
		}
		// Park only when the row both moves to another uniq_prl slot AND is landing on a real one: a
		// line whose final product_id is NULL occupies no slot at all (MySQL never counts a unique
		// entry containing NULL as a duplicate), so it can move freely in a single statement.
		park := lines[i].ProductId.Valid &&
			(row.SizeId != lines[i].SizeId || row.ProductId != lines[i].ProductId)
		plan.updates = append(plan.updates, runLineUpdate{index: i, id: row.Id, park: park})
	}
	return plan
}

func clampPagination(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
