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

// UpsertPackagingBom full-replaces the GLOBAL packaging recipe (back-compat wire, gap-07 v2 B). The
// global recipe now lives in packaging_recipe (scope='global') so it shares the one resolution path
// with the per-product/style overrides (PLM rework §2.8) — this keeps the legacy RPC working without
// a split brain (a global edit here is what ship-time resolution reads). created_by/updated_by are
// left empty: the legacy RPC carries no acting user; the new UpsertPackagingRecipe stamps them.
func (s *Store) UpsertPackagingBom(ctx context.Context, items []entity.PackagingBomItem) error {
	ins := make([]entity.PackagingRecipeInsert, 0, len(items))
	for _, it := range items {
		ins = append(ins, entity.PackagingRecipeInsert{
			MaterialId:  it.MaterialId,
			QtyPerOrder: it.QtyPerOrder,
			QtyPerItem:  it.QtyPerItem,
			Active:      it.Active,
		})
	}
	return s.UpsertPackagingRecipe(ctx, entity.PackagingScopeGlobal, sql.NullInt32{}, sql.NullInt32{}, ins, "")
}

// ListPackagingBom returns the GLOBAL packaging recipe joined with material name/unit for display
// (back-compat wire): the global scope of packaging_recipe.
func (s *Store) ListPackagingBom(ctx context.Context) ([]entity.PackagingBomItem, error) {
	rows, err := storeutil.QueryListNamed[entity.PackagingBomItem](ctx, s.DB, `
		SELECT pr.id, pr.material_id, m.name AS material_name, m.unit AS material_unit,
		       pr.qty_per_order, pr.qty_per_item, pr.active
		FROM packaging_recipe pr JOIN material m ON m.id = pr.material_id
		WHERE pr.scope = 'global'
		ORDER BY m.name, pr.id`, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("list packaging bom: %w", err)
	}
	return rows, nil
}

// ConsumePackagingForOrder writes off the packaging materials for a shipped order and closes their
// reservation claims (PLM rework §2.8, S22), idempotently and atomically. It:
//   - claims the order once (order_packaging_consumed PK) so a re-ship is a no-op that never
//     double-writes-off;
//   - resolves the per-material requirement most-specific-first (product → style → global);
//   - for each material, closes the reservation claim with a 'consume' row (the claim is being
//     fulfilled, regardless of physical stock) AND books the physical decrement as a material_stock_
//     movement writeoff (reason='packaging') — the ledger tracks the claim, the movement is the single
//     source of truth for on_hand;
//   - releases any still-open claim the ship-time recipe no longer covers (recipe drift cleanup).
//
// Within the recipe it stays best-effort for CLEAN per-material refusals — a material short on stock
// or since deleted is skipped (logged + counted), never failing the ship; those guards fire before any
// write, so a skip leaves the transaction intact. Any other per-material error aborts the whole consume
// (rolled back, retried on a future re-ship). Returns the movements booked. itemCount is the flat
// fallback (order unit count) used only when an order has neither reservation claims nor persisted
// lines to resolve — otherwise per-material quantities come from the claims / the order's lines.
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

		req, err := resolveConsumeRequirement(ctx, db, orderID, itemCount)
		if err != nil {
			return err
		}
		skipped := 0
		for materialID, qty := range req {
			qty = qty.Round(qtyScale)
			if qty.LessThanOrEqual(decimal.Zero) {
				continue
			}
			// Close the reservation claim first (the ship fulfils it) — idempotent on (claim_key,
			// consume). Done regardless of physical stock so the soft hold is always released on ship.
			if _, err := insertReservationEvent(ctx, db, materialID, orderID, qty,
				entity.MaterialReservationConsume, entity.PackagingClaimKey(orderID, materialID), username); err != nil {
				return err
			}
			// Physical decrement: material_stock_movement writeoff. reservedOpen=0 — a consume fulfils
			// its own reserve, so it guards only against physical on_hand, not the soft reservation.
			mv, err := adjustInTx(ctx, db, entity.MaterialAdjustInsert{
				MaterialId:    materialID,
				Mode:          entity.MaterialAdjustModeWriteoff,
				Quantity:      qty,
				Reason:        entity.MaterialAdjustReasonPackaging,
				Comment:       sql.NullString{String: fmt.Sprintf("auto packaging for order %d", orderID), Valid: true},
				AdminUsername: username,
			}, decimal.Zero)
			switch {
			case err == nil:
				out = append(out, mv)
			case errors.Is(err, entity.ErrInsufficientMaterialStock) || errors.Is(err, entity.ErrMaterialNotFound):
				// A clean refusal (guards fire before any write) — skip this material, ship anyway.
				skipped++
				slog.Default().WarnContext(ctx, "packaging writeoff skipped",
					slog.Int("order_id", orderID), slog.Int("material_id", materialID),
					slog.String("err", err.Error()))
			default:
				return fmt.Errorf("packaging writeoff for order %d material %d: %w", orderID, materialID, err)
			}
		}
		// Release any claim the ship-time recipe no longer covers, so a recipe change between placement
		// and ship can't leak an open claim that would depress available forever.
		if err := ReleaseOpenClaimsInTx(ctx, db, orderID, username); err != nil {
			return err
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
