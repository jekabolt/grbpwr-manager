// Package inventory implements the material warehouse (new-flow NF-01): a maintained on-hand
// balance and moving-average unit cost per catalog material, plus an append-only movement ledger.
// Every write runs in one transaction that locks the stock row (FOR UPDATE), so an issue can be
// guarded against going negative atomically, mirroring product stock. Valuation is moving-average
// in the base currency; lot/FIFO is out of scope for v1.
package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 100
	// avgScale is the decimal scale of the moving average (matches DECIMAL(12,4)).
	avgScale = 4
	// qtyScale is the decimal scale of quantities (matches DECIMAL(12,3)).
	qtyScale = 3
)

// TxFunc runs f within a transaction.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.MaterialStock.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new material-warehouse store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// stockState is the current balance + average for a material, read FOR UPDATE inside a txn.
type stockState struct {
	OnHand decimal.Decimal     `db:"on_hand"`
	Avg    decimal.NullDecimal `db:"avg_unit_cost_base"`
}

// readStockForUpdate locks and returns the material's stock row, or a zero state when no row
// exists yet (lazy creation).
func readStockForUpdate(ctx context.Context, db dependency.DB, materialID int) (stockState, error) {
	st, err := storeutil.QueryNamedOne[stockState](ctx, db,
		`SELECT on_hand, avg_unit_cost_base FROM material_stock WHERE material_id = :id FOR UPDATE`,
		map[string]any{"id": materialID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return stockState{OnHand: decimal.Zero}, nil
		}
		return stockState{}, fmt.Errorf("read material stock %d: %w", materialID, err)
	}
	return st, nil
}

// upsertStock writes the new balance and average for a material.
func upsertStock(ctx context.Context, db dependency.DB, materialID int, onHand decimal.Decimal, avg decimal.NullDecimal) error {
	return storeutil.ExecNamed(ctx, db, `
		INSERT INTO material_stock (material_id, on_hand, avg_unit_cost_base)
		VALUES (:id, :on_hand, :avg)
		ON DUPLICATE KEY UPDATE on_hand = VALUES(on_hand), avg_unit_cost_base = VALUES(avg_unit_cost_base)`,
		map[string]any{
			"id":      materialID,
			"on_hand": onHand.Round(qtyScale),
			"avg":     nullDecimal(avg),
		})
}

// insertMovement appends a ledger row and returns it with its assigned id.
func insertMovement(ctx context.Context, db dependency.DB, m entity.MaterialMovement) (entity.MaterialMovement, error) {
	id, err := storeutil.ExecNamedLastId(ctx, db, `
		INSERT INTO material_stock_movement
			(material_id, movement_type, quantity, on_hand_before, on_hand_after,
			 unit_cost, currency, unit_cost_base, production_run_id, sample_id, tech_card_id, product_id,
			 lot, lot_id, supplier_doc, reason, comment, admin_username, occurred_at,
			 input_vat_amount, input_vat_regime)
		VALUES
			(:material_id, :movement_type, :quantity, :on_hand_before, :on_hand_after,
			 :unit_cost, :currency, :unit_cost_base, :production_run_id, :sample_id, :tech_card_id, :product_id,
			 :lot, :lot_id, :supplier_doc, :reason, :comment, :admin_username, :occurred_at,
			 :input_vat_amount, :input_vat_regime)`,
		map[string]any{
			"material_id":       m.MaterialId,
			"movement_type":     string(m.MovementType),
			"quantity":          m.Quantity.Round(qtyScale),
			"on_hand_before":    m.OnHandBefore.Round(qtyScale),
			"on_hand_after":     m.OnHandAfter.Round(qtyScale),
			"unit_cost":         nullDecimal(m.UnitCost),
			"currency":          m.Currency,
			"unit_cost_base":    nullDecimal(m.UnitCostBase),
			"production_run_id": m.ProductionRunId,
			"sample_id":         m.SampleId,
			"tech_card_id":      m.TechCardId,
			"product_id":        m.ProductId,
			"lot":               m.Lot,
			"lot_id":            m.LotId,
			"supplier_doc":      m.SupplierDoc,
			"reason":            m.Reason,
			"comment":           m.Comment,
			"admin_username":    m.AdminUsername,
			"occurred_at":       m.OccurredAt,
			"input_vat_amount":  nullDecimal(m.InputVatAmount),
			"input_vat_regime":  m.InputVatRegime,
		})
	if err != nil {
		return entity.MaterialMovement{}, fmt.Errorf("insert material movement: %w", err)
	}
	m.Id = id
	return m, nil
}

// materialMeta is the descriptive state needed to gate a movement (archived flag).
type materialMeta struct {
	Archived bool `db:"archived"`
}

func readMaterialMeta(ctx context.Context, db dependency.DB, materialID int) (materialMeta, error) {
	meta, err := storeutil.QueryNamedOne[materialMeta](ctx, db,
		`SELECT archived FROM material WHERE id = :id`, map[string]any{"id": materialID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return materialMeta{}, fmt.Errorf("%w: material %d", entity.ErrMaterialNotFound, materialID)
		}
		return materialMeta{}, fmt.Errorf("read material %d: %w", materialID, err)
	}
	return meta, nil
}

// materialExists reports whether a material id exists (used to tell a real zero-stock material from
// a nonexistent one on GetMaterialStock).
func materialExists(ctx context.Context, db dependency.DB, materialID int) (bool, error) {
	n, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM material WHERE id = :id`, map[string]any{"id": materialID})
	if err != nil {
		return false, fmt.Errorf("check material %d exists: %w", materialID, err)
	}
	return n > 0, nil
}

// checkSampleOpen verifies a sample exists and is not scrapped — the states that accept a material
// issue/return. Mirrors checkRunOpen for the sample target (NF-04).
func checkSampleOpen(ctx context.Context, db dependency.DB, sampleID int) error {
	cur, err := storeutil.QueryNamedOne[struct {
		Status string `db:"status"`
	}](ctx, db, `SELECT status FROM sample WHERE id = :id`, map[string]any{"id": sampleID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: sample %d not found", entity.ErrMaterialIssueTargetInvalid, sampleID)
		}
		return fmt.Errorf("read sample %d: %w", sampleID, err)
	}
	if cur.Status == entity.SampleStatusScrapped {
		return fmt.Errorf("%w: sample %d is scrapped", entity.ErrMaterialIssueTargetInvalid, sampleID)
	}
	return nil
}

// outstandingIssued returns the net quantity of a material still out on a target (issues minus
// prior returns) — the cap for a return — plus the same net over COSTED movements only (quantity +
// value), which price the return: costedVal/costedQty is what those units actually left stock at.
// Pricing over costed movements only stops an uncosted issue from diluting the return price toward
// zero (g25-14); the cap still counts every movement. When productID is set, all three sums are
// scoped to that colourway's movements, so a per-colourway return can neither exceed nor mis-price
// what was issued for that colourway (g25-04).
func outstandingIssued(ctx context.Context, db dependency.DB, materialID int, runID, sampleID, productID sql.NullInt32) (qty, costedQty, costedVal decimal.Decimal, err error) {
	where := "material_id = :m"
	params := map[string]any{"m": materialID}
	if runID.Valid {
		where += " AND production_run_id = :run"
		params["run"] = runID.Int32
	} else {
		where += " AND sample_id = :sample"
		params["sample"] = sampleID.Int32
	}
	if productID.Valid {
		where += " AND product_id = :product"
		params["product"] = productID.Int32
	}
	row, err := storeutil.QueryNamedOne[struct {
		Qty       decimal.Decimal `db:"qty"`
		CostedQty decimal.Decimal `db:"costed_qty"`
		CostedVal decimal.Decimal `db:"costed_val"`
	}](ctx, db, fmt.Sprintf(`
		SELECT
			COALESCE(SUM(CASE WHEN movement_type IN ('issue_production','issue_sample')   THEN quantity
			                  WHEN movement_type IN ('return_production','return_sample') THEN -quantity ELSE 0 END), 0) AS qty,
			COALESCE(SUM(CASE WHEN unit_cost_base IS NULL THEN 0
			                  WHEN movement_type IN ('issue_production','issue_sample')   THEN quantity
			                  WHEN movement_type IN ('return_production','return_sample') THEN -quantity ELSE 0 END), 0) AS costed_qty,
			COALESCE(SUM(CASE WHEN unit_cost_base IS NULL THEN 0
			                  WHEN movement_type IN ('issue_production','issue_sample')   THEN quantity * unit_cost_base
			                  WHEN movement_type IN ('return_production','return_sample') THEN -quantity * unit_cost_base ELSE 0 END), 0) AS costed_val
		FROM material_stock_movement WHERE %s`, where), params)
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("read outstanding issued for material %d: %w", materialID, err)
	}
	return row.Qty, row.CostedQty, row.CostedVal, nil
}

// checkRunProduct validates the colourway attribution of a run issue (g25-03): the product the
// material is cut for must be one of the run's own colour-models — a product on the run's line
// grid, or one linked to the run's tech card. A foreign/typo'd product would otherwise seed
// by_colorway rows that belong to another style.
func checkRunProduct(ctx context.Context, db dependency.DB, runID int, techCardID sql.NullInt32, productID int) error {
	onLines, err := storeutil.QueryCountNamed(ctx, db,
		`SELECT COUNT(*) FROM production_run_line WHERE run_id = :run AND product_id = :p`,
		map[string]any{"run": runID, "p": productID})
	if err != nil {
		return fmt.Errorf("check issue colourway: %w", err)
	}
	if onLines > 0 {
		return nil
	}
	if techCardID.Valid {
		onCard, err := storeutil.QueryCountNamed(ctx, db,
			`SELECT COUNT(*) FROM tech_card_product WHERE tech_card_id = :tc AND product_id = :p`,
			map[string]any{"tc": techCardID.Int32, "p": productID})
		if err != nil {
			return fmt.Errorf("check issue colourway: %w", err)
		}
		if onCard > 0 {
			return nil
		}
	}
	return fmt.Errorf("%w: product %d is not a colour-model of this run (not on its lines or tech card)", entity.ErrMaterialIssueTargetInvalid, productID)
}

// blendIntoAvg folds a returned quantity+cost back into the material's moving average (like a
// mini-receipt), so returning units at the cost they left at keeps on-hand valuation consistent. An
// uncosted return leaves the average unchanged.
func blendIntoAvg(before stockState, qty decimal.Decimal, unitCost decimal.NullDecimal) decimal.NullDecimal {
	if !unitCost.Valid {
		return before.Avg
	}
	newQty := before.OnHand.Add(qty)
	if before.Avg.Valid && before.OnHand.GreaterThan(decimal.Zero) && newQty.GreaterThan(decimal.Zero) {
		total := before.OnHand.Mul(before.Avg.Decimal).Add(qty.Mul(unitCost.Decimal))
		return decimal.NullDecimal{Decimal: total.Div(newQty).Round(avgScale), Valid: true}
	}
	return decimal.NullDecimal{Decimal: unitCost.Decimal.Round(avgScale), Valid: true}
}

// ReceiveMaterialStock records a stock receipt and moves the balance up, updating the moving
// average when the receipt is costed. A purchase receipt (not FromProduction) also appends a
// `purchase`-sourced point to the material's price history so the catalog reflects real buys.
func (s *Store) ReceiveMaterialStock(ctx context.Context, ins entity.MaterialReceiptInsert) (entity.MaterialMovement, error) {
	if ins.Quantity.LessThanOrEqual(decimal.Zero) {
		return entity.MaterialMovement{}, fmt.Errorf("receipt quantity must be positive")
	}
	var out entity.MaterialMovement
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		out, err = ReceiveInTx(ctx, rep, ins, s.Now())
		return err
	})
	if err != nil {
		return entity.MaterialMovement{}, err
	}
	return out, nil
}

// ReceiveInTx performs a stock receipt WITHIN the caller's transaction (rep) — the moving-average
// update, the movement row and the price-history point — and returns the inserted movement. It is
// the shared core of ReceiveMaterialStock (which wraps it in its own tx) and of a production run's
// auxiliary receive (NF-07), which must book the receipt in the SAME transaction that holds the run
// row's FOR UPDATE lock: a nested transaction there would deadlock on the movement's FK back to
// production_run. `now` is the fallback price-effective date when the receipt carries no date.
func ReceiveInTx(ctx context.Context, rep dependency.Repository, ins entity.MaterialReceiptInsert, now time.Time) (entity.MaterialMovement, error) {
	db := rep.DB()
	// Confirm the material exists first so a bad id is a clean ErrMaterialNotFound (mapped to a 4xx),
	// not a late FK violation surfacing as a 500. Receipts into an archived material are still allowed.
	if _, err := readMaterialMeta(ctx, db, ins.MaterialId); err != nil {
		return entity.MaterialMovement{}, err
	}
	before, err := readStockForUpdate(ctx, db, ins.MaterialId)
	if err != nil {
		return entity.MaterialMovement{}, err
	}

	// Resolve the receipt's base-currency unit cost.
	var unitCostBase decimal.NullDecimal
	currency := strings.ToUpper(strings.TrimSpace(ins.Currency))
	if ins.FromProduction {
		// The caller supplies the run's actual per-unit base cost directly.
		unitCostBase = ins.UnitCost
		currency = strings.ToUpper(cache.GetBaseCurrency())
	} else if ins.UnitCost.Valid {
		base, ok, err := toBase(ctx, rep, ins.UnitCost.Decimal, currency)
		if err != nil {
			return entity.MaterialMovement{}, err
		}
		if ok {
			unitCostBase = decimal.NullDecimal{Decimal: base, Valid: true}
		}
	}

	newOnHand := before.OnHand.Add(ins.Quantity)
	newAvg := before.Avg
	if unitCostBase.Valid && newOnHand.GreaterThan(decimal.Zero) {
		if before.Avg.Valid && before.OnHand.GreaterThan(decimal.Zero) {
			total := before.OnHand.Mul(before.Avg.Decimal).Add(ins.Quantity.Mul(unitCostBase.Decimal))
			newAvg = decimal.NullDecimal{Decimal: total.Div(newOnHand).Round(avgScale), Valid: true}
		} else {
			newAvg = decimal.NullDecimal{Decimal: unitCostBase.Decimal.Round(avgScale), Valid: true}
		}
	}

	if err := upsertStock(ctx, db, ins.MaterialId, newOnHand, newAvg); err != nil {
		return entity.MaterialMovement{}, err
	}

	mvType := entity.MaterialMovementReceipt
	if ins.FromProduction {
		mvType = entity.MaterialMovementReceiptProduction
	}
	m := entity.MaterialMovement{
		MaterialId:      ins.MaterialId,
		MovementType:    mvType,
		Quantity:        ins.Quantity,
		OnHandBefore:    before.OnHand,
		OnHandAfter:     newOnHand,
		UnitCost:        ins.UnitCost,
		UnitCostBase:    unitCostBase,
		ProductionRunId: ins.ProductionRunId,
		Lot:             ins.Lot,
		SupplierDoc:     ins.SupplierDoc,
		Comment:         ins.Comment,
		AdminUsername:   ins.AdminUsername,
		OccurredAt:      ins.OccurredAt,
		InputVatAmount:  ins.InputVatAmount,
		InputVatRegime:  ins.InputVatRegime,
	}
	if currency != "" {
		m.Currency = sql.NullString{String: currency, Valid: true}
	}
	if !unitCostBase.Valid && ins.UnitCost.Valid && !ins.FromProduction {
		m.Comment = appendNote(m.Comment, "no FX rate — average not updated")
	}
	// gap-07 v2 D: a receipt that names a lot code opens (or tops up) a structured lot, and the
	// movement references it. Traceability only — the lot's unit_cost never drives valuation.
	lotID, err := upsertLotOnReceipt(ctx, db, ins)
	if err != nil {
		return entity.MaterialMovement{}, err
	}
	m.LotId = lotID
	out, err := insertMovement(ctx, db, m)
	if err != nil {
		return entity.MaterialMovement{}, err
	}

	// A receipt with a price feeds the catalog price history: a purchase point for a supplier
	// receipt, a production_run point for our own auxiliary-run output (NF-07) — that keeps the
	// dust-bag's cost history reflecting its real cost of production.
	if ins.UnitCost.Valid {
		validFrom := now
		if ins.OccurredAt.Valid {
			validFrom = ins.OccurredAt.Time
		}
		source := entity.MaterialPriceSourcePurchase
		if ins.FromProduction {
			source = entity.MaterialPriceSourceProductionRun
		}
		if err := rep.TechCards().AddMaterialPrice(ctx, entity.MaterialPrice{
			MaterialId: ins.MaterialId,
			Price:      ins.UnitCost.Decimal,
			Currency:   currency,
			ValidFrom:  validFrom,
			Source:     source,
		}); err != nil {
			return entity.MaterialMovement{}, fmt.Errorf("append material price: %w", err)
		}
	}
	return out, nil
}

// IssueMaterialStock issues to (or returns from) a production run or a sample. Exactly one target
// must be set. An issue is guarded against negative stock and against an archived material; a
// production-run target must be open (planned/in_progress). The issue's cost is the moving average
// frozen at issue time (unchanged by the movement).
func (s *Store) IssueMaterialStock(ctx context.Context, ins entity.MaterialIssueInsert) (entity.MaterialMovement, error) {
	if ins.Quantity.LessThanOrEqual(decimal.Zero) {
		return entity.MaterialMovement{}, fmt.Errorf("issue quantity must be positive")
	}
	if ins.ProductionRunId.Valid == ins.SampleId.Valid {
		return entity.MaterialMovement{}, fmt.Errorf("exactly one of production_run_id / sample_id must be set")
	}
	var out entity.MaterialMovement
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		meta, err := readMaterialMeta(ctx, db, ins.MaterialId)
		if err != nil {
			return err
		}
		if !ins.IsReturn && meta.Archived {
			return entity.ErrMaterialArchived
		}
		// Denormalise the owning style onto the movement (tech_card_id) so the movement journal can be
		// filtered by style directly, instead of joining through the run/sample every time (gap-06 —
		// the column existed but no write path filled it). Derived, never a free-standing target.
		var ownerTC sql.NullInt32
		if ins.ProductionRunId.Valid {
			if err := checkRunOpen(ctx, db, int(ins.ProductionRunId.Int32)); err != nil {
				return err
			}
			ownerTC, err = techCardIdOfTarget(ctx, db, "production_run", int(ins.ProductionRunId.Int32))
		} else {
			if err := checkSampleOpen(ctx, db, int(ins.SampleId.Int32)); err != nil {
				return err
			}
			ownerTC, err = techCardIdOfTarget(ctx, db, "sample", int(ins.SampleId.Int32))
		}
		if err != nil {
			return err
		}
		// A colourway attribution must name one of the run's own products (g25-03) — a typo'd or
		// foreign product would show up as another style's colourway in the run's cost breakdown.
		if attributed := productAttribution(ins); attributed.Valid {
			if err := checkRunProduct(ctx, db, int(ins.ProductionRunId.Int32), ownerTC, int(attributed.Int32)); err != nil {
				return err
			}
		}

		before, err := readStockForUpdate(ctx, db, ins.MaterialId)
		if err != nil {
			return err
		}

		var newOnHand decimal.Decimal
		var newAvg, movementCost decimal.NullDecimal
		var mvType entity.MaterialMovementType
		if ins.IsReturn {
			// Cap the return at what is still outstanding on the target and price it at the average of
			// those outstanding COSTED issues (what these units left stock at), not the drifted current
			// average — else a return after a price move would book out more than went in (WIP drift)
			// or a return over-issue would mint phantom stock. A colourway-attributed return caps and
			// prices against that colourway's own movements (g25-04). Fold the returned value back into
			// the moving average so raw-stock value stays consistent (NF-01).
			outQty, costedQty, costedVal, err := outstandingIssued(ctx, db, ins.MaterialId, ins.ProductionRunId, ins.SampleId, productAttribution(ins))
			if err != nil {
				return err
			}
			if ins.Quantity.GreaterThan(outQty) {
				if attributed := productAttribution(ins); attributed.Valid {
					return fmt.Errorf("%w: colourway %d has %s of material %d outstanding on this run", entity.ErrExcessiveMaterialReturn, attributed.Int32, outQty.String(), ins.MaterialId)
				}
				return fmt.Errorf("%w: material %d has %s outstanding on this target", entity.ErrExcessiveMaterialReturn, ins.MaterialId, outQty.String())
			}
			// Price the return at the costed outstanding average — but only when the returned quantity
			// is covered by costed issues. A return that includes units which left UNPRICED cannot be
			// honestly priced: valuing them at the costed average would mint stock value out of thin
			// air, so such a return books uncosted (conservative — the average stays put).
			if costedQty.GreaterThanOrEqual(ins.Quantity) && costedVal.GreaterThan(decimal.Zero) {
				movementCost = decimal.NullDecimal{Decimal: costedVal.Div(costedQty).Round(avgScale), Valid: true}
			}
			newOnHand = before.OnHand.Add(ins.Quantity)
			newAvg = blendIntoAvg(before, ins.Quantity, movementCost)
			mvType = entity.MaterialMovementReturnProduction
			if ins.SampleId.Valid {
				mvType = entity.MaterialMovementReturnSample
			}
		} else {
			if before.OnHand.LessThan(ins.Quantity) {
				return fmt.Errorf("%w: material %d has %s available", entity.ErrInsufficientMaterialStock, ins.MaterialId, before.OnHand.String())
			}
			newOnHand = before.OnHand.Sub(ins.Quantity)
			// An issue does not change the average; the movement freezes it as the issue cost.
			newAvg = before.Avg
			movementCost = before.Avg
			mvType = entity.MaterialMovementIssueProduction
			if ins.SampleId.Valid {
				mvType = entity.MaterialMovementIssueSample
			}
		}

		if err := upsertStock(ctx, db, ins.MaterialId, newOnHand, newAvg); err != nil {
			return err
		}
		// gap-07 v2 D: draw from (issue) or return to a specific structured lot, if named. Guards the
		// lot's remaining and validates it belongs to this material; aborts the issue on mismatch.
		if ins.LotId.Valid {
			if err := drawFromLot(ctx, db, int(ins.LotId.Int32), ins.MaterialId, ins.Quantity, ins.IsReturn); err != nil {
				return err
			}
		}
		m := entity.MaterialMovement{
			MaterialId:      ins.MaterialId,
			MovementType:    mvType,
			Quantity:        ins.Quantity,
			OnHandBefore:    before.OnHand,
			OnHandAfter:     newOnHand,
			UnitCostBase:    movementCost,
			ProductionRunId: ins.ProductionRunId,
			SampleId:        ins.SampleId,
			TechCardId:      ownerTC,
			// Per-colourway attribution (gap-07 v2 C): only meaningful for a run issue; ignored for a
			// sample. Carried onto returns too so a return nets against the same colourway.
			ProductId:     productAttribution(ins),
			LotId:         ins.LotId,
			Comment:       ins.Comment,
			AdminUsername: ins.AdminUsername,
			OccurredAt:    ins.OccurredAt,
		}
		if movementCost.Valid {
			m.Currency = sql.NullString{String: strings.ToUpper(cache.GetBaseCurrency()), Valid: true}
		}
		out, err = insertMovement(ctx, db, m)
		return err
	})
	if err != nil {
		return entity.MaterialMovement{}, err
	}
	return out, nil
}

// productAttribution returns the colourway (product) an issue is attributed to (gap-07 v2 C). It is
// only meaningful for a production-run issue; a sample issue never carries a product_id.
func productAttribution(ins entity.MaterialIssueInsert) sql.NullInt32 {
	if !ins.ProductionRunId.Valid {
		return sql.NullInt32{}
	}
	return ins.ProductId
}

// techCardIdOfTarget returns the tech_card_id owning an issue target (a production_run or a sample),
// used to denormalise the owning style onto the movement (gap-06). `table` is a fixed literal, not
// user input. A NULL tech_card_id (e.g. an unlinked run) yields an invalid NullInt32.
func techCardIdOfTarget(ctx context.Context, db dependency.DB, table string, id int) (sql.NullInt32, error) {
	tc, err := storeutil.QueryNamedOne[struct {
		TechCardId sql.NullInt32 `db:"tech_card_id"`
	}](ctx, db, fmt.Sprintf(`SELECT tech_card_id FROM %s WHERE id = :id`, table), map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.NullInt32{}, nil
		}
		return sql.NullInt32{}, fmt.Errorf("resolve tech card of %s %d: %w", table, id, err)
	}
	return tc.TechCardId, nil
}

// AdjustMaterialStock records a stock count (set/adjust) or a write-off. Set makes on-hand equal
// Quantity; Adjust adds a signed Quantity; Writeoff subtracts a positive Quantity. Decreases are
// guarded against negative stock. The average is never changed (a count corrects quantity, not
// value); a write-off records the average as the value written off.
func (s *Store) AdjustMaterialStock(ctx context.Context, ins entity.MaterialAdjustInsert) (entity.MaterialMovement, error) {
	var out entity.MaterialMovement
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// Manual adjustments are reservation-aware (S22): a decrease may not drive on-hand below what
		// is reserved for open orders — that would deepen a packaging oversell. The packaging consume/
		// release paths pass reservedOpen=0 (they fulfil their own reserve); only this operator-facing
		// path guards against the soft reservation.
		reserved, err := openReservedQty(ctx, db, ins.MaterialId)
		if err != nil {
			return err
		}
		out, err = adjustInTx(ctx, db, ins, reserved)
		return err
	})
	if err != nil {
		return entity.MaterialMovement{}, err
	}
	return out, nil
}

// adjustInTx is AdjustMaterialStock's body on the caller's transaction, so multi-material flows
// (packaging auto-consume) can share one atomic transaction with their bookkeeping (g25-02). Every
// guard fails BEFORE any write, so a guard failure leaves the shared transaction clean.
func adjustInTx(ctx context.Context, db dependency.DB, ins entity.MaterialAdjustInsert, reservedOpen decimal.Decimal) (entity.MaterialMovement, error) {
	if _, err := readMaterialMeta(ctx, db, ins.MaterialId); err != nil {
		return entity.MaterialMovement{}, err
	}
	before, err := readStockForUpdate(ctx, db, ins.MaterialId)
	if err != nil {
		return entity.MaterialMovement{}, err
	}

	var newOnHand decimal.Decimal
	mvType := entity.MaterialMovementAdjustment
	switch ins.Mode {
	case entity.MaterialAdjustModeSet:
		if ins.Quantity.LessThan(decimal.Zero) {
			return entity.MaterialMovement{}, fmt.Errorf("set quantity must be non-negative")
		}
		newOnHand = ins.Quantity
	case entity.MaterialAdjustModeAdjust:
		newOnHand = before.OnHand.Add(ins.Quantity)
		if newOnHand.LessThan(decimal.Zero) {
			return entity.MaterialMovement{}, fmt.Errorf("%w: material %d has %s available", entity.ErrInsufficientMaterialStock, ins.MaterialId, before.OnHand.String())
		}
	case entity.MaterialAdjustModeWriteoff:
		if ins.Quantity.LessThanOrEqual(decimal.Zero) {
			return entity.MaterialMovement{}, fmt.Errorf("write-off quantity must be positive")
		}
		if before.OnHand.LessThan(ins.Quantity) {
			return entity.MaterialMovement{}, fmt.Errorf("%w: material %d has %s available", entity.ErrInsufficientMaterialStock, ins.MaterialId, before.OnHand.String())
		}
		newOnHand = before.OnHand.Sub(ins.Quantity)
		mvType = entity.MaterialMovementWriteoff
	default:
		return entity.MaterialMovement{}, fmt.Errorf("invalid adjust mode %q", ins.Mode)
	}

	// Reservation-aware guard (S22): never let a manual adjustment drive on-hand below the quantity
	// reserved for open orders — that deepens a packaging oversell. reservedOpen=0 disables the guard
	// for internal reserve-fulfilling paths (packaging consume).
	if reservedOpen.IsPositive() && newOnHand.LessThan(reservedOpen) {
		return entity.MaterialMovement{}, fmt.Errorf("%w: material %d has %s reserved for open orders, only %s free",
			entity.ErrMaterialReserved, ins.MaterialId, reservedOpen.String(), before.OnHand.String())
	}

	if err := upsertStock(ctx, db, ins.MaterialId, newOnHand, before.Avg); err != nil {
		return entity.MaterialMovement{}, err
	}
	m := entity.MaterialMovement{
		MaterialId:    ins.MaterialId,
		MovementType:  mvType,
		Quantity:      newOnHand.Sub(before.OnHand).Abs(),
		OnHandBefore:  before.OnHand,
		OnHandAfter:   newOnHand,
		UnitCostBase:  before.Avg,
		Comment:       ins.Comment,
		AdminUsername: ins.AdminUsername,
	}
	if ins.Reason != "" {
		m.Reason = sql.NullString{String: ins.Reason, Valid: true}
	}
	return insertMovement(ctx, db, m)
}

// GetMaterialStock returns a material's stock balance, or a zero balance (no error) when the
// material has no movements yet.
func (s *Store) GetMaterialStock(ctx context.Context, materialID int) (*entity.MaterialStock, error) {
	st, err := storeutil.QueryNamedOne[entity.MaterialStock](ctx, s.DB,
		`SELECT material_id, on_hand, avg_unit_cost_base, updated_at FROM material_stock WHERE material_id = :id`,
		map[string]any{"id": materialID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No stock row yet — but only report a zero balance for a material that actually exists,
			// so a bogus id is a NotFound rather than a fabricated zero-stock row.
			exists, err := materialExists(ctx, s.DB, materialID)
			if err != nil {
				return nil, err
			}
			if !exists {
				return nil, sql.ErrNoRows
			}
			return &entity.MaterialStock{MaterialId: materialID, OnHand: decimal.Zero}, nil
		}
		return nil, fmt.Errorf("get material stock %d: %w", materialID, err)
	}
	return &st, nil
}

// ListMaterialMovements returns the movement ledger (newest first) matching the filter, with the
// total count ignoring pagination.
func (s *Store) ListMaterialMovements(ctx context.Context, limit, offset int, filter entity.MaterialMovementFilter) ([]entity.MaterialMovement, int, error) {
	limit, offset = clampPagination(limit, offset)
	params := map[string]any{}
	where := ""
	if filter.MaterialId > 0 {
		where += " AND material_id = :material_id"
		params["material_id"] = filter.MaterialId
	}
	if filter.ProductionRunId > 0 {
		where += " AND production_run_id = :production_run_id"
		params["production_run_id"] = filter.ProductionRunId
	}
	if filter.SampleId > 0 {
		where += " AND sample_id = :sample_id"
		params["sample_id"] = filter.SampleId
	}
	if filter.MovementType != "" {
		where += " AND movement_type = :movement_type"
		params["movement_type"] = string(filter.MovementType)
	}
	// Inclusive occurred_at date window (B-5): compare on DATE(occurred_at) so a plain YYYY-MM-DD
	// upper bound includes movements stamped any time that day.
	if filter.OccurredFrom != "" {
		where += " AND DATE(occurred_at) >= :occurred_from"
		params["occurred_from"] = filter.OccurredFrom
	}
	if filter.OccurredTo != "" {
		where += " AND DATE(occurred_at) <= :occurred_to"
		params["occurred_to"] = filter.OccurredTo
	}
	total, err := storeutil.QueryCountNamed(ctx, s.DB,
		fmt.Sprintf(`SELECT COUNT(*) FROM material_stock_movement WHERE 1=1%s`, where), params)
	if err != nil {
		return nil, 0, fmt.Errorf("count material movements: %w", err)
	}
	params["limit"] = limit
	params["offset"] = offset
	rows, err := storeutil.QueryListNamed[entity.MaterialMovement](ctx, s.DB, fmt.Sprintf(`
		SELECT id, material_id, movement_type, quantity, on_hand_before, on_hand_after,
			unit_cost, currency, unit_cost_base, production_run_id, sample_id, tech_card_id, product_id,
			lot, lot_id, supplier_doc, reason, comment, admin_username, occurred_at, created_at
		FROM material_stock_movement WHERE 1=1%s
		ORDER BY id DESC LIMIT :limit OFFSET :offset`, where), params)
	if err != nil {
		return nil, 0, fmt.Errorf("list material movements: %w", err)
	}
	return rows, total, nil
}

// materialStockRow is the flat scan target for the warehouse list (material joined with its
// balance). on_hand/avg come from the LEFT-joined stock row (0 / NULL when the material has no
// movements yet); min_stock is a catalog column (NF-02).
type materialStockRow struct {
	entity.Material
	MinStock decimal.NullDecimal `db:"min_stock"`
	OnHand   decimal.NullDecimal `db:"on_hand"`
	Avg      decimal.NullDecimal `db:"avg_unit_cost_base"`
}

// ListMaterialStock returns catalog materials joined with their stock balance, valuation and
// low-stock flag, matching the filter. Archived materials are excluded. Ordered by section, name.
func (s *Store) ListMaterialStock(ctx context.Context, filter entity.MaterialStockFilter) ([]entity.MaterialStockRow, error) {
	params := map[string]any{
		"section": strings.ToLower(strings.TrimSpace(filter.Section)),
		"q":       "%" + strings.TrimSpace(filter.Query) + "%",
		"hasQ":    strings.TrimSpace(filter.Query) != "",
		"withStk": filter.WithStockOnly,
		"belowMn": filter.BelowMinOnly,
	}
	rows, err := storeutil.QueryListNamed[materialStockRow](ctx, s.DB, `
		SELECT m.*, s.on_hand AS on_hand, s.avg_unit_cost_base AS avg_unit_cost_base
		FROM material m
		LEFT JOIN material_stock s ON s.material_id = m.id
		WHERE m.archived = FALSE
			AND (:section = '' OR m.section = :section)
			AND (NOT :hasQ OR m.name LIKE :q OR m.code LIKE :q OR m.supplier_ref LIKE :q)
			AND (NOT :withStk OR COALESCE(s.on_hand, 0) > 0)
			AND (NOT :belowMn OR (m.min_stock IS NOT NULL AND COALESCE(s.on_hand, 0) < m.min_stock))
		ORDER BY m.section, m.name`, params)
	if err != nil {
		return nil, fmt.Errorf("list material stock: %w", err)
	}
	out := make([]entity.MaterialStockRow, len(rows))
	for i, r := range rows {
		onHand := decimal.Zero
		if r.OnHand.Valid {
			onHand = r.OnHand.Decimal
		}
		// sqlx maps the min_stock column to the shallower materialStockRow.MinStock, leaving the
		// nested Material.MinStock unset; copy it so a client reading the material sees its threshold.
		r.Material.MinStock = r.MinStock
		row := entity.MaterialStockRow{
			Material:        r.Material,
			OnHand:          onHand,
			AvgUnitCostBase: r.Avg,
			MinStock:        r.MinStock,
		}
		if r.Avg.Valid {
			row.StockValueBase = decimal.NullDecimal{Decimal: onHand.Mul(r.Avg.Decimal).Round(2), Valid: true}
		}
		if r.MinStock.Valid && onHand.LessThan(r.MinStock.Decimal) {
			row.BelowMinStock = true
		}
		out[i] = row
	}
	return out, nil
}

// checkRunOpen verifies a production run exists and is open (planned/in_progress) — the only
// states that accept material movement.
func checkRunOpen(ctx context.Context, db dependency.DB, runID int) error {
	cur, err := storeutil.QueryNamedOne[struct {
		Status string `db:"status"`
	}](ctx, db, `SELECT status FROM production_run WHERE id = :id`, map[string]any{"id": runID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: production run %d not found", entity.ErrMaterialIssueTargetInvalid, runID)
		}
		return fmt.Errorf("read production run %d: %w", runID, err)
	}
	if cur.Status != string(entity.ProductionRunPlanned) && cur.Status != string(entity.ProductionRunInProgress) {
		return fmt.Errorf("%w: production run %d is %s (not open)", entity.ErrMaterialIssueTargetInvalid, runID, cur.Status)
	}
	return nil
}

// toBase converts amount in currency to the base currency via costing FX. ok=false means the
// currency has no rate (the caller leaves the base value unset). The base currency itself is 1:1.
func toBase(ctx context.Context, rep dependency.Repository, amount decimal.Decimal, currency string) (decimal.Decimal, bool, error) {
	base := strings.ToUpper(cache.GetBaseCurrency())
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if cur == "" || cur == base {
		return amount, true, nil
	}
	rates, err := rep.TechCards().GetCostingFxRatesToBase(ctx)
	if err != nil {
		return decimal.Zero, false, fmt.Errorf("load costing fx rates: %w", err)
	}
	rate, ok := rates[cur]
	if !ok {
		return decimal.Zero, false, nil
	}
	return amount.Mul(rate), true, nil
}

func nullDecimal(d decimal.NullDecimal) any {
	if !d.Valid {
		return nil
	}
	return d.Decimal
}

func appendNote(existing sql.NullString, note string) sql.NullString {
	if existing.Valid && existing.String != "" {
		return sql.NullString{String: existing.String + "; " + note, Valid: true}
	}
	return sql.NullString{String: note, Valid: true}
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
