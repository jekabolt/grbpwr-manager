// Package productionrun implements production-run (партия) management: the run header, its
// per-size planned/received/defect grid, and (later phases) actual costs and stock integration.
// A run snapshots its planned unit cost at plan time so it stops tracking edits to the tech card.
package productionrun

import (
	"context"
	"database/sql"
	"fmt"
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
	planned_unit_cost, planned_currency, marker_efficiency_pct, marker_notes, actual_wastage_percent, notes`

const runValues = `:tech_card_id, :release_id, :status, :started_at, :planned_start_at, :promised_at,
	:planned_unit_cost, :planned_currency, :marker_efficiency_pct, :marker_notes, :actual_wastage_percent, :notes`

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
	}
}

// CreateProductionRun inserts a run and its size grid, returning the new id. PlannedUnitCost/
// PlannedCurrency are expected to be already snapshotted by the caller.
func (s *Store) CreateProductionRun(ctx context.Context, r *entity.ProductionRunInsert) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
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
		return insertRunCosts(ctx, rep.DB(), id, r.Costs)
	})
	if err != nil {
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
func (s *Store) UpdateProductionRun(ctx context.Context, id int, r *entity.ProductionRunInsert, expectedLockVersion int, fx dto.CostingFx) error {
	return s.updateProductionRun(ctx, id, r, expectedLockVersion, false, fx)
}

// UpdateProductionRunPreservingCosts reloads the stored cost articles after locking the run and
// carries them through the full-replace. It is the cost-blind update path: any preservation read
// failure aborts the same transaction before destructive child deletes can run.
func (s *Store) UpdateProductionRunPreservingCosts(ctx context.Context, id int, r *entity.ProductionRunInsert, expectedLockVersion int) error {
	return s.updateProductionRun(ctx, id, r, expectedLockVersion, true, dto.CostingFx{})
}

func (s *Store) updateProductionRun(ctx context.Context, id int, r *entity.ProductionRunInsert,
	expectedLockVersion int, preserveCosts bool, fx dto.CostingFx) error {
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
				if expectedLockVersion > 0 && cur.LockVersion != expectedLockVersion {
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
				return nil
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
		// Optimistic lock (#9): a positive expected version that no longer matches means the run was
		// edited concurrently — reject rather than clobber the other writer's full-replace. 0 opts out
		// (legacy last-write-wins), so pre-existing clients are unaffected. The FOR UPDATE read above
		// serialises this against a concurrent update, so the in-Go check is authoritative; the WHERE
		// guard on the UPDATE is belt-and-suspenders (mirrors UpdateTechCard).
		if expectedLockVersion > 0 && cur.LockVersion != expectedLockVersion {
			return entity.ErrProductionRunConflict
		}
		// The run's style is fixed at creation: the planned-cost snapshot, the movements' denormalised
		// tech_card_id and the style roll-ups are all anchored to it (g25-13).
		if r.TechCardId != cur.TechCardId {
			return entity.ErrProductionRunCardChange
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
		params["expected_lock_version"] = expectedLockVersion
		lockGuard := ""
		if expectedLockVersion > 0 {
			lockGuard = " AND lock_version = :expected_lock_version"
		}
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE production_run SET
				lock_version = lock_version + 1,
				tech_card_id = :tech_card_id, release_id = :release_id, status = :status,
				started_at = :started_at,
				planned_start_at = :planned_start_at, promised_at = :promised_at,
				marker_efficiency_pct = :marker_efficiency_pct, marker_notes = :marker_notes,
				actual_wastage_percent = :actual_wastage_percent, notes = :notes
			WHERE id = :id`+lockGuard, params)
		if err != nil {
			return fmt.Errorf("failed to update production run: %w", err)
		}
		// The row provably exists (loaded above). With the lock guard present, 0 rows means the version
		// moved under us — make the WHERE guard load-bearing, not just the in-Go check.
		if expectedLockVersion > 0 && rows == 0 {
			return entity.ErrProductionRunConflict
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
	return &run, nil
}

// runMaterialMovements loads the material stock ledger rows booked to this run (issues/returns to
// production), ordered by id. It feeds the run's materials-from-stock actual cost and the material
// plan's issued column.
func (s *Store) runMaterialMovements(ctx context.Context, runID int) ([]entity.MaterialMovement, error) {
	return loadRunMovements(ctx, s.DB, runID)
}

const movementColumns = `id, material_id, movement_type, quantity, on_hand_before, on_hand_after,
	unit_cost, currency, unit_cost_base, production_run_id, sample_id, tech_card_id, product_id,
	lot, lot_id, supplier_doc, reason, comment, admin_username, occurred_at, created_at`

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
		`SELECT id, COALESCE(line_key, '') AS line_key, product_id, COALESCE(size_id, 0) AS size_id, planned_qty, received_qty, defect_qty
		 FROM production_run_line WHERE run_id = :run_id ORDER BY product_id IS NOT NULL, product_id, size_id`,
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
		`SELECT run_id, id, COALESCE(line_key, '') AS line_key, product_id, COALESCE(size_id, 0) AS size_id, planned_qty, received_qty, defect_qty
		 FROM production_run_line WHERE run_id IN (:ids) ORDER BY run_id, product_id IS NOT NULL, product_id, size_id`,
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
		`SELECT id, run_id, kind, description, amount, currency, amount_base, incurred_at
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
		`SELECT id, run_id, kind, description, amount, currency, amount_base, incurred_at
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
		"run_id":       runID,
		"line_key":     lineKey,
		"product_id":   ln.ProductId,
		"size_id":      nullIfZero(ln.SizeId),
		"planned_qty":  ln.PlannedQty,
		"received_qty": ln.ReceivedQty,
		"defect_qty":   ln.DefectQty,
	}
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
				product_id = :product_id, size_id = :size_id, planned_qty = :planned_qty,
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
