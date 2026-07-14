// Package productionrun implements production-run (партия) management: the run header, its
// per-size planned/received/defect grid, and (later phases) actual costs and stock integration.
// A run snapshots its planned unit cost at plan time so it stops tracking edits to the tech card.
package productionrun

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/inventory"
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

const runColumns = `tech_card_id, release_id, status, started_at, received_at,
	planned_unit_cost, planned_currency, marker_efficiency_pct, marker_notes, notes`

const runValues = `:tech_card_id, :release_id, :status, :started_at, :received_at,
	:planned_unit_cost, :planned_currency, :marker_efficiency_pct, :marker_notes, :notes`

func runParams(r *entity.ProductionRunInsert) map[string]any {
	return map[string]any{
		"tech_card_id":          r.TechCardId,
		"release_id":            r.ReleaseId,
		"status":                string(r.Status),
		"started_at":            r.StartedAt,
		"received_at":           r.ReceivedAt,
		"planned_unit_cost":     r.PlannedUnitCost,
		"planned_currency":      r.PlannedCurrency,
		"marker_efficiency_pct": r.MarkerEfficiencyPct,
		"marker_notes":          r.MarkerNotes,
		"notes":                 r.Notes,
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
		if err := insertRunCosts(ctx, rep.DB(), id, r.Costs); err != nil {
			return err
		}
		return insertRunMarkers(ctx, rep.DB(), id, r.Markers)
	})
	if err != nil {
		return 0, fmt.Errorf("can't create production run: %w", err)
	}
	return id, nil
}

// UpdateProductionRun updates a run's header and full-replaces its line grid + cost articles. The
// planned-cost snapshot (planned_unit_cost/planned_currency) is intentionally NOT written here — it
// is frozen at plan time. It first locks the run FOR UPDATE and enforces the status invariants that
// keep received facts and warehouse WIP honest:
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
func (s *Store) UpdateProductionRun(ctx context.Context, id int, r *entity.ProductionRunInsert) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			Status     string `db:"status"`
			TechCardId int    `db:"tech_card_id"`
		}](ctx, rep.DB(), `SELECT status, tech_card_id FROM production_run WHERE id = :id FOR UPDATE`, map[string]any{"id": id})
		if err != nil {
			if err == sql.ErrNoRows {
				return sql.ErrNoRows
			}
			return fmt.Errorf("failed to load production run for update: %w", err)
		}
		if cur.Status == string(entity.ProductionRunReceived) || cur.Status == string(entity.ProductionRunClosed) {
			return entity.ErrProductionRunReceivedImmutable
		}
		if r.Status == entity.ProductionRunReceived {
			return entity.ErrProductionRunReceiveViaUpdate
		}
		// The run's style is fixed at creation: the planned-cost snapshot, the movements' denormalised
		// tech_card_id and the style roll-ups are all anchored to it (g25-13).
		if r.TechCardId != cur.TechCardId {
			return entity.ErrProductionRunCardChange
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
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE production_run SET
				tech_card_id = :tech_card_id, release_id = :release_id, status = :status,
				started_at = :started_at, received_at = :received_at,
				marker_efficiency_pct = :marker_efficiency_pct, marker_notes = :marker_notes, notes = :notes
			WHERE id = :id`, params); err != nil {
			return fmt.Errorf("failed to update production run: %w", err)
		}
		for _, tbl := range []string{"production_run_line", "production_run_cost", "production_run_marker"} {
			if err := storeutil.ExecNamed(ctx, rep.DB(),
				fmt.Sprintf(`DELETE FROM %s WHERE run_id = :id`, tbl), map[string]any{"id": id}); err != nil {
				return fmt.Errorf("failed to clear %s: %w", tbl, err)
			}
		}
		if err := insertRunLines(ctx, rep.DB(), id, r.Lines); err != nil {
			return err
		}
		if err := insertRunCosts(ctx, rep.DB(), id, r.Costs); err != nil {
			return err
		}
		return insertRunMarkers(ctx, rep.DB(), id, r.Markers)
	})
	if err != nil {
		switch err {
		case sql.ErrNoRows, entity.ErrProductionRunReceivedImmutable,
			entity.ErrProductionRunReceiveViaUpdate, entity.ErrProductionRunHasOpenIssues,
			entity.ErrProductionRunCardChange:
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

// ReceiveProductionRun receives a multi-colourway run into stock and transitions it to `received`,
// in one transaction: it locks the run and refuses if it is already received/closed (guarding
// against a double count), then for each product in perProduct increments that product's per-size
// stock (production_received source) and — when costPrice is set — seeds that product's cost_price
// from the run's actual unit cost (one style-level figure applied to every colour-model of the
// batch; colourways are not costed apart in v1). Finally it stamps status/received_at. perProduct
// maps product_id → (size_id → qty), already validated by the caller (every product ∈ the card's
// products, at least one positive qty). Returns entity.ErrProductionRunAlreadyReceived on a repeat
// receipt and sql.ErrNoRows when the run does not exist.
func (s *Store) ReceiveProductionRun(ctx context.Context, runID int, perProduct map[int]map[int]int, updateCostPrice bool, username string) (bool, error) {
	var costPriceUpdated bool
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		cur, err := storeutil.QueryNamedOne[struct {
			Status string `db:"status"`
		}](ctx, db, `SELECT status FROM production_run WHERE id = :id FOR UPDATE`, map[string]any{"id": runID})
		if err != nil {
			if err == sql.ErrNoRows {
				return sql.ErrNoRows
			}
			return fmt.Errorf("failed to load production run for receive: %w", err)
		}
		if cur.Status == string(entity.ProductionRunReceived) || cur.Status == string(entity.ProductionRunClosed) {
			return entity.ErrProductionRunAlreadyReceived
		}
		// Re-read the lines under the run lock and confirm the received grid the caller validated is
		// still current — a concurrent UpdateProductionRun (also lock-serialised) could have changed
		// it between the caller's read and this transaction. If it diverges, abort so the caller
		// reloads and revalidates rather than booking a stale grid.
		lines, err := loadRunLines(ctx, db, runID)
		if err != nil {
			return err
		}
		fresh, missingProduct := receivedMapFromLines(lines)
		if missingProduct || !sameReceivedMap(fresh, perProduct) {
			return entity.ErrProductionRunConcurrentModification
		}
		// Recompute the actual unit cost from the freshly-read costs + movements INSIDE the lock, so a
		// material issue that committed after the caller's read is included in cost_price.
		var costPrice decimal.NullDecimal
		if updateCostPrice {
			costs, err := loadRunCosts(ctx, db, runID)
			if err != nil {
				return err
			}
			movements, err := loadRunMovements(ctx, db, runID)
			if err != nil {
				return err
			}
			run := &entity.ProductionRun{
				ProductionRunInsert: entity.ProductionRunInsert{Lines: lines, Costs: costs},
				MaterialMovements:   movements,
			}
			costPrice = run.ActualUnitCostBase()
		}
		for productID, perSize := range perProduct {
			if len(perSize) == 0 {
				continue
			}
			if err := rep.Products().ReceiveProductionStock(ctx, productID, perSize, runID, username); err != nil {
				return err
			}
			if costPrice.Valid {
				if err := rep.Products().SetProductCostPriceFromProductionRun(ctx, productID, runID, costPrice.Decimal); err != nil {
					return err
				}
			}
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE production_run SET status = :status, received_at = :received_at WHERE id = :id`,
			map[string]any{"id": runID, "status": string(entity.ProductionRunReceived), "received_at": s.Now()}); err != nil {
			return err
		}
		costPriceUpdated = costPrice.Valid
		return nil
	})
	if err != nil {
		switch err {
		case sql.ErrNoRows, entity.ErrProductionRunAlreadyReceived, entity.ErrProductionRunConcurrentModification:
			return false, err
		}
		return false, fmt.Errorf("can't receive production run: %w", err)
	}
	return costPriceUpdated, nil
}

// receivedMapFromLines builds the product_id → (size_id → received qty) map from a run's lines,
// counting only lines with a positive received quantity. missingProduct is true when some received
// line has no product to book into (an inconsistent grid).
func receivedMapFromLines(lines []entity.ProductionRunLine) (perProduct map[int]map[int]int, missingProduct bool) {
	perProduct = make(map[int]map[int]int)
	for _, ln := range lines {
		if !ln.ReceivedQty.Valid || ln.ReceivedQty.Int64 <= 0 {
			continue
		}
		if !ln.ProductId.Valid {
			missingProduct = true
			continue
		}
		pid := int(ln.ProductId.Int32)
		if perProduct[pid] == nil {
			perProduct[pid] = make(map[int]int)
		}
		perProduct[pid][ln.SizeId] = int(ln.ReceivedQty.Int64)
	}
	return perProduct, missingProduct
}

// sameReceivedMap reports whether two product→size→qty maps are identical.
func sameReceivedMap(a, b map[int]map[int]int) bool {
	if len(a) != len(b) {
		return false
	}
	for pid, as := range a {
		bs, ok := b[pid]
		if !ok || len(as) != len(bs) {
			return false
		}
		for sz, q := range as {
			if bs[sz] != q {
				return false
			}
		}
	}
	return true
}

// ReceiveAuxiliaryProductionRun receives an AUXILIARY run's output into the material warehouse
// (NF-07) and transitions it to `received`, in one transaction: it locks the run, refuses a repeat
// receipt, re-reads the lines and the actual costs UNDER THE LOCK (so a material issue or line edit
// racing the receive is included — g25-07, mirroring ReceiveProductionRun), books a
// receipt_production of Σ received_qty into outputMaterialID at the run's actual per-unit base cost
// (moving that material's average and appending a production_run price point), then stamps
// status/received_at. The unit cost may be uncosted (the run's actuals had no base) — the receipt
// then does not move the average. A line that gained a product, or a grid whose received total
// dropped to zero, means the run changed since the caller validated it →
// ErrProductionRunConcurrentModification / ErrProductionRunNothingReceived. Returns
// entity.ErrProductionRunAlreadyReceived / sql.ErrNoRows.
func (s *Store) ReceiveAuxiliaryProductionRun(ctx context.Context, runID, outputMaterialID int, username string) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		cur, err := storeutil.QueryNamedOne[struct {
			Status string `db:"status"`
		}](ctx, db, `SELECT status FROM production_run WHERE id = :id FOR UPDATE`, map[string]any{"id": runID})
		if err != nil {
			if err == sql.ErrNoRows {
				return sql.ErrNoRows
			}
			return fmt.Errorf("failed to load production run for auxiliary receive: %w", err)
		}
		if cur.Status == string(entity.ProductionRunReceived) || cur.Status == string(entity.ProductionRunClosed) {
			return entity.ErrProductionRunAlreadyReceived
		}
		lines, err := loadRunLines(ctx, db, runID)
		if err != nil {
			return err
		}
		var qty int64
		for _, ln := range lines {
			if ln.ProductId.Valid {
				// the caller validated a product-free grid; a product appeared since → stale read.
				return entity.ErrProductionRunConcurrentModification
			}
			if ln.ReceivedQty.Valid && ln.ReceivedQty.Int64 > 0 {
				qty += ln.ReceivedQty.Int64
			}
		}
		if qty == 0 {
			return entity.ErrProductionRunNothingReceived
		}
		costs, err := loadRunCosts(ctx, db, runID)
		if err != nil {
			return err
		}
		movements, err := loadRunMovements(ctx, db, runID)
		if err != nil {
			return err
		}
		run := &entity.ProductionRun{
			ProductionRunInsert: entity.ProductionRunInsert{Lines: lines, Costs: costs},
			MaterialMovements:   movements,
		}
		unitCostBase := run.ActualUnitCostBase()
		// Book the receipt in THIS transaction (not via ReceiveMaterialStock, which opens its own):
		// the movement's FK back to production_run needs a shared lock on the run row we hold FOR
		// UPDATE, so a separate transaction would deadlock. ReceiveInTx participates in rep's tx.
		if _, err := inventory.ReceiveInTx(ctx, rep, entity.MaterialReceiptInsert{
			MaterialId:      outputMaterialID,
			Quantity:        decimal.NewFromInt(qty),
			UnitCost:        unitCostBase, // base-currency actual unit cost (or invalid → uncosted)
			ProductionRunId: sql.NullInt32{Int32: int32(runID), Valid: true},
			FromProduction:  true,
			AdminUsername:   username,
		}, s.Now()); err != nil {
			return err
		}
		return storeutil.ExecNamed(ctx, db, `
			UPDATE production_run SET status = :status, received_at = :received_at WHERE id = :id`,
			map[string]any{"id": runID, "status": string(entity.ProductionRunReceived), "received_at": s.Now()})
	})
	if err != nil {
		switch err {
		case sql.ErrNoRows, entity.ErrProductionRunAlreadyReceived,
			entity.ErrProductionRunConcurrentModification, entity.ErrProductionRunNothingReceived:
			return err
		}
		return fmt.Errorf("can't receive auxiliary production run: %w", err)
	}
	return nil
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
	if cur.Status == string(entity.ProductionRunReceived) || cur.Status == string(entity.ProductionRunClosed) {
		return entity.ErrProductionRunReceivedImmutable
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
	markers, err := s.runMarkers(ctx, id)
	if err != nil {
		return nil, err
	}
	run.Markers = markers
	movements, err := s.runMaterialMovements(ctx, id)
	if err != nil {
		return nil, err
	}
	run.MaterialMovements = movements
	return &run, nil
}

// runMaterialMovements loads the material stock ledger rows booked to this run (issues/returns to
// production), ordered by id. It feeds the run's materials-from-stock actual cost and the material
// plan's issued column.
func (s *Store) runMaterialMovements(ctx context.Context, runID int) ([]entity.MaterialMovement, error) {
	return loadRunMovements(ctx, s.DB, runID)
}

// loadRunMovements loads a run's material movement ledger on the given db (pool or tx).
func loadRunMovements(ctx context.Context, db dependency.DB, runID int) ([]entity.MaterialMovement, error) {
	mv, err := storeutil.QueryListNamed[entity.MaterialMovement](ctx, db, `
		SELECT id, material_id, movement_type, quantity, on_hand_before, on_hand_after,
		       unit_cost, currency, unit_cost_base, production_run_id, sample_id, tech_card_id, product_id,
		       lot, lot_id, supplier_doc, reason, comment, admin_username, occurred_at, created_at
		FROM material_stock_movement WHERE production_run_id = :run_id ORDER BY id`,
		map[string]any{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("can't load production run material movements: %w", err)
	}
	return mv, nil
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
	if err := s.attachMarkers(ctx, runs); err != nil {
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
		`SELECT id, product_id, size_id, planned_qty, received_qty, defect_qty
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
		`SELECT run_id, id, product_id, size_id, planned_qty, received_qty, defect_qty
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

// runMarkers loads one run's imported nesting markers ordered by id (insertion order).
func (s *Store) runMarkers(ctx context.Context, runID int) ([]entity.ProductionRunMarker, error) {
	return loadRunMarkers(ctx, s.DB, runID)
}

const markerColumns = `id, source, marker_name, size_id, material_id, marker_width, lay_length,
	units_per_marker, efficiency_pct, marker_file_url, notes`

// loadRunMarkers loads a run's marker records on the given db (pool or tx).
func loadRunMarkers(ctx context.Context, db dependency.DB, runID int) ([]entity.ProductionRunMarker, error) {
	markers, err := storeutil.QueryListNamed[entity.ProductionRunMarker](ctx, db,
		fmt.Sprintf(`SELECT %s FROM production_run_marker WHERE run_id = :run_id ORDER BY id`, markerColumns),
		map[string]any{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("can't load production run markers: %w", err)
	}
	return markers, nil
}

// markerRow scans a marker together with its run_id for the batched list attach.
type markerRow struct {
	RunID int `db:"run_id"`
	entity.ProductionRunMarker
}

// attachMarkers loads the markers for a page of runs in one query and attaches them.
func (s *Store) attachMarkers(ctx context.Context, runs []entity.ProductionRun) error {
	if len(runs) == 0 {
		return nil
	}
	ids := make([]int, len(runs))
	idx := make(map[int]int, len(runs))
	for i := range runs {
		ids[i] = runs[i].Id
		idx[runs[i].Id] = i
	}
	rows, err := storeutil.QueryListNamed[markerRow](ctx, s.DB,
		fmt.Sprintf(`SELECT run_id, %s FROM production_run_marker WHERE run_id IN (:ids) ORDER BY run_id, id`, markerColumns),
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load production run markers: %w", err)
	}
	for _, r := range rows {
		if i, ok := idx[r.RunID]; ok {
			runs[i].Markers = append(runs[i].Markers, r.ProductionRunMarker)
		}
	}
	return nil
}

func insertRunMarkers(ctx context.Context, db dependency.DB, runID int, markers []entity.ProductionRunMarker) error {
	if len(markers) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(markers))
	for _, m := range markers {
		rows = append(rows, map[string]any{
			"run_id":           runID,
			"source":           string(m.Source),
			"marker_name":      m.MarkerName,
			"size_id":          m.SizeId,
			"material_id":      m.MaterialId,
			"marker_width":     m.MarkerWidth,
			"lay_length":       m.LayLength,
			"units_per_marker": m.UnitsPerMarker,
			"efficiency_pct":   m.EfficiencyPct,
			"marker_file_url":  m.MarkerFileUrl,
			"notes":            m.Notes,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "production_run_marker", rows); err != nil {
		return fmt.Errorf("failed to insert production run markers: %w", err)
	}
	return nil
}

func insertRunLines(ctx context.Context, db dependency.DB, runID int, lines []entity.ProductionRunLine) error {
	if len(lines) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(lines))
	for _, ln := range lines {
		rows = append(rows, map[string]any{
			"run_id":       runID,
			"product_id":   ln.ProductId,
			"size_id":      ln.SizeId,
			"planned_qty":  ln.PlannedQty,
			"received_qty": ln.ReceivedQty,
			"defect_qty":   ln.DefectQty,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "production_run_line", rows); err != nil {
		return fmt.Errorf("failed to insert production run lines: %w", err)
	}
	return nil
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
