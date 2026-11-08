package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

type shrinkageLotState struct {
	entity.MaterialLot
}

type shrinkageMaterialState struct {
	Unit         sql.NullString      `db:"unit"`
	Class        string              `db:"material_class"`
	ShrinkagePct decimal.NullDecimal `db:"shrinkage_pct"`
}

type shrinkageRatio struct {
	LotId        int             `db:"lot_id"`
	LengthBefore decimal.Decimal `db:"length_before"`
	LengthAfter  decimal.Decimal `db:"length_after"`
}

// ShrinkMaterialLot records a physical lot shrink measurement. Quantity, lot facts, moving
// average, event history, catalog percent and lock_version either all commit or all roll back.
func (s *Store) ShrinkMaterialLot(ctx context.Context, ins entity.MaterialLotShrinkInsert) (*entity.MaterialLotShrinkResult, error) {
	if ins.LotId <= 0 || ins.LengthAfter.LessThanOrEqual(decimal.Zero) {
		return nil, entity.ErrMaterialConversionInvalid
	}
	var out *entity.MaterialLotShrinkResult
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		out, err = shrinkMaterialLotInTx(ctx, rep, ins, s.Now())
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func shrinkMaterialLotInTx(ctx context.Context, rep dependency.Repository, ins entity.MaterialLotShrinkInsert, now time.Time) (*entity.MaterialLotShrinkResult, error) {
	db := rep.DB()
	// The first read only discovers which stock row must be locked. The lot is re-read FOR UPDATE
	// after the stock lock, and a mismatch/disappearance aborts the transaction.
	ref, err := storeutil.QueryNamedOne[struct {
		MaterialId int `db:"material_id"`
	}](ctx, db, `SELECT material_id FROM material_lot WHERE id = :id`, map[string]any{"id": ins.LotId})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: lot not found", entity.ErrMaterialLotMismatch)
		}
		return nil, fmt.Errorf("read material lot %d: %w", ins.LotId, err)
	}
	if err := ensureStockRow(ctx, db, ref.MaterialId); err != nil {
		return nil, err
	}
	stock, err := readStockForUpdate(ctx, db, ref.MaterialId)
	if err != nil {
		return nil, err
	}
	lot, err := readShrinkageLotForUpdate(ctx, db, ins.LotId)
	if err != nil {
		return nil, err
	}
	if lot.MaterialId != ref.MaterialId {
		return nil, fmt.Errorf("%w: lot changed material", entity.ErrMaterialLotMismatch)
	}

	material, err := storeutil.QueryNamedOne[shrinkageMaterialState](ctx, db, `
		SELECT m.unit, m.material_class, a.shrinkage_pct
		FROM material m
		LEFT JOIN material_fabric_attr a ON a.material_id = m.id
		WHERE m.id = :id FOR UPDATE`, map[string]any{"id": lot.MaterialId})
	if err != nil {
		return nil, fmt.Errorf("lock shrinkage material %d: %w", lot.MaterialId, err)
	}
	unit, ok := entity.NormalizeMaterialUnit(material.Unit.String)
	if !ok || (unit != entity.MaterialUnitM && unit != entity.MaterialUnitCm && unit != entity.MaterialUnitMm) {
		return nil, fmt.Errorf("%w: shrinkage requires m, cm or mm", entity.ErrMaterialConversionInvalid)
	}

	before := lot.RemainingQty
	if ins.LengthBefore.Valid && !ins.LengthBefore.Decimal.Equal(before) {
		return nil, fmt.Errorf("%w: expected %s, found %s", entity.ErrMaterialLotStale, ins.LengthBefore.Decimal.String(), before.String())
	}
	if ins.LengthAfter.LessThanOrEqual(decimal.Zero) || !ins.LengthAfter.LessThan(before) {
		return nil, fmt.Errorf("%w: length_after must be between zero and %s", entity.ErrMaterialConversionInvalid, before.String())
	}
	loss := before.Sub(ins.LengthAfter)
	if loss.GreaterThan(stock.OnHand) {
		return nil, fmt.Errorf("%w: %s has %s available", entity.ErrInsufficientMaterialStock, materialLabel(ctx, db, lot.MaterialId), stock.OnHand.String())
	}
	newOnHand := stock.OnHand.Sub(loss)
	newAvg := stock.Avg
	if stock.Avg.Valid {
		if !newOnHand.IsPositive() {
			return nil, fmt.Errorf("%w: shrinkage cannot preserve value at zero on-hand", entity.ErrInsufficientMaterialStock)
		}
		newAvg = decimal.NullDecimal{
			Decimal: stock.OnHand.Mul(stock.Avg.Decimal).Div(newOnHand).Round(avgScale),
			Valid:   true,
		}
	}
	newLotCost := lot.UnitCost
	if lot.UnitCost.Valid {
		newLotCost = decimal.NullDecimal{
			Decimal: lot.UnitCost.Decimal.Mul(before).Div(ins.LengthAfter).Round(avgScale),
			Valid:   true,
		}
	}

	ratioRows, err := storeutil.QueryListNamed[shrinkageRatio](ctx, db, `
		SELECT lot_id, length_before, length_after
		FROM material_lot_shrinkage WHERE material_id = :id ORDER BY id`,
		map[string]any{"id": lot.MaterialId})
	if err != nil {
		return nil, fmt.Errorf("load shrinkage history for material %d: %w", lot.MaterialId, err)
	}
	ratioRows = append(ratioRows, shrinkageRatio{LotId: lot.Id, LengthBefore: before, LengthAfter: ins.LengthAfter})
	lotPct, materialPct := compoundedShrinkage(ratioRows, lot.Id)

	widthBefore := lot.WashedWidthCm
	if !widthBefore.Valid {
		widthBefore = lot.MeasuredWidthCm
	}
	washedWidth := lot.WashedWidthCm
	if ins.WidthAfter.Valid {
		washedWidth = ins.WidthAfter
	}

	if err := upsertStock(ctx, db, lot.MaterialId, newOnHand, newAvg); err != nil {
		return nil, err
	}
	movement, err := insertMovement(ctx, db, entity.MaterialMovement{
		MaterialId: lot.MaterialId, MovementType: entity.MaterialMovementShrinkage,
		Quantity: loss, OnHandBefore: stock.OnHand, OnHandAfter: newOnHand,
		UnitCostBase: decimal.NullDecimal{Decimal: decimal.Zero, Valid: true},
		LotId:        sql.NullInt32{Int32: int32(lot.Id), Valid: true}, Comment: ins.Comment,
		AdminUsername: ins.AdminUsername,
	}, now)
	if err != nil {
		return nil, err
	}
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE material_lot
		SET remaining_qty = :remaining, shrunk_qty = shrunk_qty + :loss,
		    unit_cost = :unit_cost, washed_width_cm = :washed_width, shrinkage_pct = :pct
		WHERE id = :id`, map[string]any{
		"id": lot.Id, "remaining": ins.LengthAfter.Round(qtyScale), "loss": loss.Round(qtyScale),
		"unit_cost": nullDecimal(newLotCost), "washed_width": nullDecimal(washedWidth), "pct": lotPct,
	}); err != nil {
		return nil, fmt.Errorf("update material lot %d shrinkage: %w", lot.Id, err)
	}

	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO material_lot_shrinkage
			(movement_id, lot_id, material_id, length_before, length_after,
			 width_before, width_after, avg_before, avg_after, lot_pct_after,
			 material_pct_before, material_pct_after, admin_username, comment)
		VALUES
			(:movement_id, :lot_id, :material_id, :length_before, :length_after,
			 :width_before, :width_after, :avg_before, :avg_after, :lot_pct_after,
			 :material_pct_before, :material_pct_after, :admin_username, :comment)`,
		map[string]any{
			"movement_id": movement.Id, "lot_id": lot.Id, "material_id": lot.MaterialId,
			"length_before": before, "length_after": ins.LengthAfter,
			"width_before": nullDecimal(widthBefore), "width_after": nullDecimal(ins.WidthAfter),
			"avg_before": nullDecimal(stock.Avg), "avg_after": nullDecimal(newAvg),
			"lot_pct_after": lotPct, "material_pct_before": nullDecimal(material.ShrinkagePct),
			"material_pct_after": materialPct, "admin_username": ins.AdminUsername, "comment": ins.Comment,
		}); err != nil {
		return nil, fmt.Errorf("insert material lot %d shrinkage event: %w", lot.Id, err)
	}
	if err := rep.TechCards().SetFabricShrinkagePct(ctx, lot.MaterialId, materialPct); err != nil {
		return nil, err
	}
	updated, err := readMaterialLot(ctx, db, lot.Id)
	if err != nil {
		return nil, err
	}
	return &entity.MaterialLotShrinkResult{
		Movement: movement, Lot: updated,
		MaterialShrinkagePct: decimal.NullDecimal{Decimal: materialPct, Valid: true},
	}, nil
}

func readShrinkageLotForUpdate(ctx context.Context, db dependency.DB, id int) (entity.MaterialLot, error) {
	lot, err := storeutil.QueryNamedOne[entity.MaterialLot](ctx, db, `
		SELECT id, material_id, lot_code, supplier_doc, received_qty, remaining_qty, unit_cost,
		       currency, received_at, note, archived, measured_width_cm, shade_code,
		       shrunk_qty, washed_width_cm, shrinkage_pct
		FROM material_lot WHERE id = :id FOR UPDATE`, map[string]any{"id": id})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entity.MaterialLot{}, fmt.Errorf("%w: lot not found", entity.ErrMaterialLotMismatch)
		}
		return entity.MaterialLot{}, fmt.Errorf("lock material lot %d: %w", id, err)
	}
	return lot, nil
}

func readMaterialLot(ctx context.Context, db dependency.DB, id int) (entity.MaterialLot, error) {
	lot, err := storeutil.QueryNamedOne[entity.MaterialLot](ctx, db, `
		SELECT id, material_id, lot_code, supplier_doc, received_qty, remaining_qty, unit_cost,
		       currency, received_at, note, archived, measured_width_cm, shade_code,
		       shrunk_qty, washed_width_cm, shrinkage_pct
		FROM material_lot WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return entity.MaterialLot{}, fmt.Errorf("read material lot %d: %w", id, err)
	}
	return lot, nil
}

// compoundedShrinkage returns the current lot's compounded percent and the material's first-event-
// length-weighted percent. Rows must be chronological for each lot.
func compoundedShrinkage(rows []shrinkageRatio, currentLotID int) (decimal.Decimal, decimal.Decimal) {
	type aggregate struct {
		Weight decimal.Decimal
		Ratio  decimal.Decimal
	}
	byLot := make(map[int]aggregate)
	for _, row := range rows {
		a, exists := byLot[row.LotId]
		if !exists {
			a = aggregate{Weight: row.LengthBefore, Ratio: decimal.NewFromInt(1)}
		}
		a.Ratio = a.Ratio.Mul(row.LengthAfter.Div(row.LengthBefore))
		byLot[row.LotId] = a
	}
	weighted := decimal.Zero
	totalWeight := decimal.Zero
	current := decimal.Zero
	for lotID, a := range byLot {
		pct := decimal.NewFromInt(1).Sub(a.Ratio).Mul(decimal.NewFromInt(100)).Round(2)
		if lotID == currentLotID {
			current = pct
		}
		weighted = weighted.Add(a.Weight.Mul(pct))
		totalWeight = totalWeight.Add(a.Weight)
	}
	if totalWeight.IsZero() {
		return current, decimal.Zero
	}
	return current, weighted.Div(totalWeight).Round(2)
}
