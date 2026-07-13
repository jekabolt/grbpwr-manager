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
		return insertRunCosts(ctx, rep.DB(), id, r.Costs)
	})
	if err != nil {
		return 0, fmt.Errorf("can't create production run: %w", err)
	}
	return id, nil
}

// UpdateProductionRun updates a run's header and full-replaces its size grid. The planned-cost
// snapshot (planned_unit_cost/planned_currency) is intentionally NOT written here — it is frozen
// at plan time. Returns sql.ErrNoRows when no run exists.
func (s *Store) UpdateProductionRun(ctx context.Context, id int, r *entity.ProductionRunInsert) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		params := runParams(r)
		params["id"] = id
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(), `
			UPDATE production_run SET
				tech_card_id = :tech_card_id, release_id = :release_id, status = :status,
				started_at = :started_at, received_at = :received_at, notes = :notes
			WHERE id = :id`, params)
		if err != nil {
			return fmt.Errorf("failed to update production run: %w", err)
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		for _, tbl := range []string{"production_run_line", "production_run_cost"} {
			if err := storeutil.ExecNamed(ctx, rep.DB(),
				fmt.Sprintf(`DELETE FROM %s WHERE run_id = :id`, tbl), map[string]any{"id": id}); err != nil {
				return fmt.Errorf("failed to clear %s: %w", tbl, err)
			}
		}
		if err := insertRunLines(ctx, rep.DB(), id, r.Lines); err != nil {
			return err
		}
		return insertRunCosts(ctx, rep.DB(), id, r.Costs)
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return err
		}
		return fmt.Errorf("can't update production run: %w", err)
	}
	return nil
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
func (s *Store) ReceiveProductionRun(ctx context.Context, runID int, perProduct map[int]map[int]int, username string, costPrice decimal.NullDecimal) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			Status string `db:"status"`
		}](ctx, rep.DB(), `SELECT status FROM production_run WHERE id = :id FOR UPDATE`, map[string]any{"id": runID})
		if err != nil {
			if err == sql.ErrNoRows {
				return sql.ErrNoRows
			}
			return fmt.Errorf("failed to load production run for receive: %w", err)
		}
		if cur.Status == string(entity.ProductionRunReceived) || cur.Status == string(entity.ProductionRunClosed) {
			return entity.ErrProductionRunAlreadyReceived
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
		return storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE production_run SET status = :status, received_at = :received_at WHERE id = :id`,
			map[string]any{"id": runID, "status": string(entity.ProductionRunReceived), "received_at": s.Now()})
	})
	if err != nil {
		switch err {
		case sql.ErrNoRows, entity.ErrProductionRunAlreadyReceived:
			return err
		}
		return fmt.Errorf("can't receive production run: %w", err)
	}
	return nil
}

// ReceiveAuxiliaryProductionRun receives an AUXILIARY run's output into the material warehouse
// (NF-07) and transitions it to `received`, in one transaction: it locks the run, refuses a repeat
// receipt, books a receipt_production of qty into outputMaterialID (moving that material's average
// by unitCostBase and appending a production_run price point), then stamps status/received_at.
// unitCostBase may be invalid (the run's actuals had no base) — the receipt is then uncosted and
// does not move the average. Returns entity.ErrProductionRunAlreadyReceived / sql.ErrNoRows.
func (s *Store) ReceiveAuxiliaryProductionRun(ctx context.Context, runID, outputMaterialID int, qty decimal.Decimal, unitCostBase decimal.NullDecimal, username string) error {
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			Status string `db:"status"`
		}](ctx, rep.DB(), `SELECT status FROM production_run WHERE id = :id FOR UPDATE`, map[string]any{"id": runID})
		if err != nil {
			if err == sql.ErrNoRows {
				return sql.ErrNoRows
			}
			return fmt.Errorf("failed to load production run for auxiliary receive: %w", err)
		}
		if cur.Status == string(entity.ProductionRunReceived) || cur.Status == string(entity.ProductionRunClosed) {
			return entity.ErrProductionRunAlreadyReceived
		}
		// Book the receipt in THIS transaction (not via ReceiveMaterialStock, which opens its own):
		// the movement's FK back to production_run needs a shared lock on the run row we hold FOR
		// UPDATE, so a separate transaction would deadlock. ReceiveInTx participates in rep's tx.
		if _, err := inventory.ReceiveInTx(ctx, rep, entity.MaterialReceiptInsert{
			MaterialId:      outputMaterialID,
			Quantity:        qty,
			UnitCost:        unitCostBase, // base-currency actual unit cost (or invalid → uncosted)
			ProductionRunId: sql.NullInt32{Int32: int32(runID), Valid: true},
			FromProduction:  true,
			AdminUsername:   username,
		}, s.Now()); err != nil {
			return err
		}
		return storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE production_run SET status = :status, received_at = :received_at WHERE id = :id`,
			map[string]any{"id": runID, "status": string(entity.ProductionRunReceived), "received_at": s.Now()})
	})
	if err != nil {
		switch err {
		case sql.ErrNoRows, entity.ErrProductionRunAlreadyReceived:
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
	mv, err := storeutil.QueryListNamed[entity.MaterialMovement](ctx, s.DB, `
		SELECT id, material_id, movement_type, quantity, on_hand_before, on_hand_after,
		       unit_cost, currency, unit_cost_base, production_run_id, sample_id, tech_card_id,
		       lot, supplier_doc, reason, comment, admin_username, occurred_at, created_at
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
	return runs, total, nil
}

// runLines loads one run's colour-model × size lines, ordered by product then size (NULL product
// first, so planning lines lead) for a stable display.
func (s *Store) runLines(ctx context.Context, runID int) ([]entity.ProductionRunLine, error) {
	lines, err := storeutil.QueryListNamed[entity.ProductionRunLine](ctx, s.DB,
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
	costs, err := storeutil.QueryListNamed[entity.ProductionRunCost](ctx, s.DB,
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
