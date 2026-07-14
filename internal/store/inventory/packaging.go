package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// UpsertPackagingBom full-replaces the global packaging recipe (gap-07 v2 B): the provided set
// becomes the whole packaging_bom (a material appears at most once — uniq_packaging_bom_material).
// Runs in one transaction so a partial write can't leave a half-recipe.
func (s *Store) UpsertPackagingBom(ctx context.Context, items []entity.PackagingBomItem) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := storeutil.ExecNamed(ctx, db, `DELETE FROM packaging_bom`, map[string]any{}); err != nil {
			return fmt.Errorf("clear packaging bom: %w", err)
		}
		for _, it := range items {
			if it.MaterialId <= 0 {
				return fmt.Errorf("packaging bom line needs a material_id")
			}
			if it.QtyPerOrder.IsNegative() || it.QtyPerItem.IsNegative() {
				return fmt.Errorf("packaging bom quantities must be >= 0")
			}
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO packaging_bom (material_id, qty_per_order, qty_per_item, active)
				VALUES (:material_id, :qty_per_order, :qty_per_item, :active)`,
				map[string]any{
					"material_id":   it.MaterialId,
					"qty_per_order": it.QtyPerOrder,
					"qty_per_item":  it.QtyPerItem,
					"active":        it.Active,
				}); err != nil {
				return fmt.Errorf("insert packaging bom material %d: %w", it.MaterialId, err)
			}
		}
		return nil
	})
}

// ListPackagingBom returns the packaging recipe joined with material name/unit for display.
func (s *Store) ListPackagingBom(ctx context.Context) ([]entity.PackagingBomItem, error) {
	return listPackagingBom(ctx, s.DB)
}

// listPackagingBom reads the recipe on the given db (pool or tx).
func listPackagingBom(ctx context.Context, db dependency.DB) ([]entity.PackagingBomItem, error) {
	rows, err := storeutil.QueryListNamed[entity.PackagingBomItem](ctx, db, `
		SELECT pb.id, pb.material_id, m.name AS material_name, m.unit AS material_unit,
		       pb.qty_per_order, pb.qty_per_item, pb.active
		FROM packaging_bom pb
		JOIN material m ON m.id = pb.material_id
		ORDER BY m.name, pb.id`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list packaging bom: %w", err)
	}
	return rows, nil
}

// ConsumePackagingForOrder writes off the packaging materials for a shipped order (gap-07 v2 B),
// idempotently and atomically: the claim (order_packaging_consumed PK — a re-ship is a no-op that
// never double-writes-off) and every writeoff run in ONE transaction, so a transient failure rolls
// the claim back and the next re-ship retries instead of silently losing the writeoff forever
// (g25-02). Within the recipe it stays best-effort for CLEAN per-material refusals — a material
// short on stock or since deleted is skipped (logged + counted in skipped_count), never failing the
// ship; those guards fire before any write, so a skip leaves the transaction intact. Any other
// per-material error aborts the whole consume (rolled back, retried on a future re-ship). Returns
// the movements booked. itemCount is the order's total unit count.
func (s *Store) ConsumePackagingForOrder(ctx context.Context, orderID, itemCount int, username string) ([]entity.MaterialMovement, error) {
	var out []entity.MaterialMovement
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		claimed, err := storeutil.ExecNamedRows(ctx, db, `
			INSERT IGNORE INTO order_packaging_consumed (order_id, movement_count) VALUES (:order_id, 0)`,
			map[string]any{"order_id": orderID})
		if err != nil {
			return fmt.Errorf("claim packaging consume for order %d: %w", orderID, err)
		}
		if claimed == 0 {
			return nil // already consumed on an earlier ship
		}

		items, err := listPackagingBom(ctx, db)
		if err != nil {
			return err
		}
		skipped := 0
		for _, it := range items {
			if !it.Active {
				continue
			}
			qty := it.QtyPerOrder.Add(it.QtyPerItem.Mul(decimal.NewFromInt(int64(itemCount)))).Round(qtyScale)
			if qty.LessThanOrEqual(decimal.Zero) {
				continue
			}
			mv, err := adjustInTx(ctx, db, entity.MaterialAdjustInsert{
				MaterialId:    it.MaterialId,
				Mode:          entity.MaterialAdjustModeWriteoff,
				Quantity:      qty,
				Reason:        entity.MaterialAdjustReasonPackaging,
				Comment:       sql.NullString{String: fmt.Sprintf("auto packaging for order %d", orderID), Valid: true},
				AdminUsername: username,
			})
			switch {
			case err == nil:
				out = append(out, mv)
			case errors.Is(err, entity.ErrInsufficientMaterialStock) || errors.Is(err, entity.ErrMaterialNotFound):
				// A clean refusal (guards fire before any write) — skip this material, ship anyway.
				skipped++
				slog.Default().WarnContext(ctx, "packaging writeoff skipped",
					slog.Int("order_id", orderID), slog.Int("material_id", it.MaterialId),
					slog.String("err", err.Error()))
			default:
				// A transient/unexpected error may have half-applied — abort so the whole consume
				// (claim included) rolls back and a future re-ship retries it.
				return fmt.Errorf("packaging writeoff for order %d material %d: %w", orderID, it.MaterialId, err)
			}
		}
		return storeutil.ExecNamed(ctx, db, `
			UPDATE order_packaging_consumed SET movement_count = :n, skipped_count = :skipped
			WHERE order_id = :order_id`,
			map[string]any{"n": len(out), "skipped": skipped, "order_id": orderID})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
