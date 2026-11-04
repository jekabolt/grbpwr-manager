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

// UpsertPackagingBom is refused: the global recipe no longer feeds any order (see UpsertPackagingRecipe).
func (s *Store) UpsertPackagingBom(ctx context.Context, items []entity.PackagingBomItem) error {
	return fmt.Errorf("%w: packaging lives on the tech card", entity.ErrPackagingRecipeInvalid)
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
//   - claims the order once (order_packaging_consumed PK) so a re-ship never double-writes-off;
//   - takes the per-material requirement from the open claims, else from the card's packaging items;
//   - for each material, closes the reservation claim with a 'consume' row (the claim is being
//     fulfilled, regardless of physical stock) AND books the physical decrement as a material_stock_
//     movement writeoff (reason='packaging') — the ledger tracks the claim, the movement is the single
//     source of truth for on_hand;
//   - releases any still-open claim the ship-time requirement no longer covers.
//
// A material short on stock does not fail the ship: its quantity stays in order_packaging_shortage and
// the next re-ship tries it again, so a shortage is late stock, not lost stock. A material since deleted
// is skipped for good. Those guards fire before any write, so a skip leaves the transaction intact. Any
// other per-material error aborts the whole consume. Returns the movements booked.
func (s *Store) ConsumePackagingForOrder(ctx context.Context, orderID int, username string) ([]entity.MaterialMovement, error) {
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
			out, err = s.retryPackagingShortages(ctx, db, orderID, username)
			return err
		}

		req, unresolved, err := resolveConsumeRequirement(ctx, db, orderID)
		if err != nil {
			return err
		}
		skipped := unresolved
		for _, materialID := range sortedMaterialIDs(req) {
			qty := req[materialID].Round(qtyScale)
			if qty.LessThanOrEqual(decimal.Zero) {
				continue
			}
			// Close the reservation claim first (the ship fulfils it) — idempotent on (claim_key,
			// consume). Done regardless of physical stock so the soft hold is always released on ship.
			if _, err := insertReservationEvent(ctx, db, materialID, orderOwner(orderID), qty,
				entity.MaterialReservationConsume, entity.PackagingClaimKey(orderID, materialID), username); err != nil {
				return err
			}
			mv, booked, err := s.writeoffPackaging(ctx, db, orderID, materialID, qty, username)
			if err != nil {
				return err
			}
			if !booked {
				skipped++
				continue
			}
			out = append(out, mv)
		}
		// Release any claim the ship-time requirement no longer covers, so it can't leak an open claim
		// that would depress available forever.
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

// writeoffPackaging books the physical decrement: a material_stock_movement writeoff. reservedOpen=0 —
// a consume fulfils its own reserve, so it guards only against physical on_hand. booked=false is a clean
// refusal; a shortage is kept for the next re-ship.
func (s *Store) writeoffPackaging(ctx context.Context, db dependency.DB, orderID, materialID int, qty decimal.Decimal, username string) (entity.MaterialMovement, bool, error) {
	mv, err := adjustInTx(ctx, db, entity.MaterialAdjustInsert{
		MaterialId:    materialID,
		Mode:          entity.MaterialAdjustModeWriteoff,
		Quantity:      qty,
		Reason:        entity.MaterialAdjustReasonPackaging,
		Comment:       sql.NullString{String: fmt.Sprintf("auto packaging for order %d", orderID), Valid: true},
		AdminUsername: username,
	}, decimal.Zero, s.Now())
	switch {
	case err == nil:
		return mv, true, nil
	case errors.Is(err, entity.ErrInsufficientMaterialStock):
		slog.Default().WarnContext(ctx, "packaging writeoff short on stock",
			slog.Int("order_id", orderID), slog.Int("material_id", materialID), slog.String("err", err.Error()))
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO order_packaging_shortage (order_id, material_id, qty) VALUES (:order_id, :material_id, :qty)
			ON DUPLICATE KEY UPDATE qty = VALUES(qty)`,
			map[string]any{"order_id": orderID, "material_id": materialID, "qty": qty}); err != nil {
			return entity.MaterialMovement{}, false, fmt.Errorf("record packaging shortage for order %d: %w", orderID, err)
		}
		return entity.MaterialMovement{}, false, nil
	case errors.Is(err, entity.ErrMaterialNotFound):
		slog.Default().WarnContext(ctx, "packaging writeoff skipped: material gone",
			slog.Int("order_id", orderID), slog.Int("material_id", materialID))
		return entity.MaterialMovement{}, false, nil
	default:
		return entity.MaterialMovement{}, false, fmt.Errorf("packaging writeoff for order %d material %d: %w", orderID, materialID, err)
	}
}

// retryPackagingShortages is a re-ship of a consumed order: only what was short the first time can
// still move. The rows are locked, so two re-ships cannot book the same shortage twice.
func (s *Store) retryPackagingShortages(ctx context.Context, db dependency.DB, orderID int, username string) ([]entity.MaterialMovement, error) {
	type shortageRow struct {
		MaterialId int             `db:"material_id"`
		Qty        decimal.Decimal `db:"qty"`
	}
	rows, err := storeutil.QueryListNamed[shortageRow](ctx, db, `
		SELECT material_id, qty FROM order_packaging_shortage WHERE order_id = :order_id
		ORDER BY material_id FOR UPDATE`, map[string]any{"order_id": orderID})
	if err != nil {
		return nil, fmt.Errorf("read packaging shortages for order %d: %w", orderID, err)
	}
	var out []entity.MaterialMovement
	for _, r := range rows {
		mv, booked, err := s.writeoffPackaging(ctx, db, orderID, r.MaterialId, r.Qty, username)
		if err != nil {
			return nil, err
		}
		if !booked {
			continue
		}
		out = append(out, mv)
		if err := storeutil.ExecNamed(ctx, db, `
			DELETE FROM order_packaging_shortage WHERE order_id = :order_id AND material_id = :material_id`,
			map[string]any{"order_id": orderID, "material_id": r.MaterialId}); err != nil {
			return nil, fmt.Errorf("close packaging shortage for order %d: %w", orderID, err)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE order_packaging_consumed
		SET movement_count = movement_count + :n, skipped_count = GREATEST(skipped_count - :n, 0)
		WHERE order_id = :order_id`, map[string]any{"n": len(out), "order_id": orderID}); err != nil {
		return nil, fmt.Errorf("update packaging consume for order %d: %w", orderID, err)
	}
	return out, nil
}
