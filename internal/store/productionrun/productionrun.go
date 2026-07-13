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
	planned_unit_cost, planned_currency, notes`

const runValues = `:tech_card_id, :release_id, :status, :started_at, :received_at,
	:planned_unit_cost, :planned_currency, :notes`

func runParams(r *entity.ProductionRunInsert) map[string]any {
	return map[string]any{
		"tech_card_id":      r.TechCardId,
		"release_id":        r.ReleaseId,
		"status":            string(r.Status),
		"started_at":        r.StartedAt,
		"received_at":       r.ReceivedAt,
		"planned_unit_cost": r.PlannedUnitCost,
		"planned_currency":  r.PlannedCurrency,
		"notes":             r.Notes,
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
		if err := insertRunSizes(ctx, rep.DB(), id, r.Sizes); err != nil {
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
		for _, tbl := range []string{"production_run_size", "production_run_cost"} {
			if err := storeutil.ExecNamed(ctx, rep.DB(),
				fmt.Sprintf(`DELETE FROM %s WHERE run_id = :id`, tbl), map[string]any{"id": id}); err != nil {
				return fmt.Errorf("failed to clear %s: %w", tbl, err)
			}
		}
		if err := insertRunSizes(ctx, rep.DB(), id, r.Sizes); err != nil {
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

// ReceiveProductionRun receives a run into a product's stock and transitions it to `received`, in
// one transaction: it locks the run and refuses if it is already received/closed (guarding against
// a double count), increments the product's per-size stock from perSize (production_received
// source), optionally sets the product's cost_price from the run's actual unit cost, then stamps
// status/received_at. Returns entity.ErrProductionRunAlreadyReceived on a repeat receipt and
// sql.ErrNoRows when the run does not exist.
func (s *Store) ReceiveProductionRun(ctx context.Context, runID, productID int, perSize map[int]int, username string, costPrice decimal.NullDecimal) error {
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
		if len(perSize) > 0 {
			if err := rep.Products().ReceiveProductionStock(ctx, productID, perSize, runID, username); err != nil {
				return err
			}
		}
		if costPrice.Valid {
			if err := rep.Products().SetProductCostPriceFromProductionRun(ctx, productID, runID, costPrice.Decimal); err != nil {
				return err
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
	sizes, err := s.runSizes(ctx, id)
	if err != nil {
		return nil, err
	}
	run.Sizes = sizes
	costs, err := s.runCosts(ctx, id)
	if err != nil {
		return nil, err
	}
	run.Costs = costs
	return &run, nil
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
	if err := s.attachSizes(ctx, runs); err != nil {
		return nil, 0, err
	}
	if err := s.attachCosts(ctx, runs); err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}

// runSizes loads one run's size grid ordered by size_id.
func (s *Store) runSizes(ctx context.Context, runID int) ([]entity.ProductionRunSize, error) {
	sizes, err := storeutil.QueryListNamed[entity.ProductionRunSize](ctx, s.DB,
		`SELECT size_id, planned_qty, received_qty, defect_qty
		 FROM production_run_size WHERE run_id = :run_id ORDER BY size_id`,
		map[string]any{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("can't load production run sizes: %w", err)
	}
	return sizes, nil
}

// sizeGridRow scans a size line together with its run_id for the batched list attach.
type sizeGridRow struct {
	RunID int `db:"run_id"`
	entity.ProductionRunSize
}

// attachSizes loads the size grids for a page of runs in one query and attaches them.
func (s *Store) attachSizes(ctx context.Context, runs []entity.ProductionRun) error {
	if len(runs) == 0 {
		return nil
	}
	ids := make([]int, len(runs))
	idx := make(map[int]int, len(runs))
	for i := range runs {
		ids[i] = runs[i].Id
		idx[runs[i].Id] = i
	}
	rows, err := storeutil.QueryListNamed[sizeGridRow](ctx, s.DB,
		`SELECT run_id, size_id, planned_qty, received_qty, defect_qty
		 FROM production_run_size WHERE run_id IN (:ids) ORDER BY run_id, size_id`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load production run sizes: %w", err)
	}
	for _, r := range rows {
		if i, ok := idx[r.RunID]; ok {
			runs[i].Sizes = append(runs[i].Sizes, r.ProductionRunSize)
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

func insertRunSizes(ctx context.Context, db dependency.DB, runID int, sizes []entity.ProductionRunSize) error {
	if len(sizes) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(sizes))
	for _, sz := range sizes {
		rows = append(rows, map[string]any{
			"run_id":       runID,
			"size_id":      sz.SizeId,
			"planned_qty":  sz.PlannedQty,
			"received_qty": sz.ReceivedQty,
			"defect_qty":   sz.DefectQty,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "production_run_size", rows); err != nil {
		return fmt.Errorf("failed to insert production run sizes: %w", err)
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
