package product

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

func insertColorwayStyleMirror(ctx context.Context, db dependency.DB, styleID, colorwayID int) error {
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_product (tech_card_id, product_id)
		VALUES (:style, :colorway)`, map[string]any{
		"style":    styleID,
		"colorway": colorwayID,
	}); err != nil {
		return fmt.Errorf("insert style mirror for colourway %d: %w", colorwayID, err)
	}
	return nil
}

func initializeColorwayOwnershipMirrors(ctx context.Context, db dependency.DB, styleID, colorwayID int) error {
	if err := insertColorwayStyleMirror(ctx, db, styleID, colorwayID); err != nil {
		return err
	}
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE product SET primary_tech_card_id = :style
		WHERE id = :colorway AND primary_tech_card_id IS NULL`, map[string]any{
		"style":    styleID,
		"colorway": colorwayID,
	}); err != nil {
		return fmt.Errorf("initialize primary tech card for colourway %d: %w", colorwayID, err)
	}
	return nil
}

func moveColorwayOwnershipMirrors(ctx context.Context, db dependency.DB, sourceStyleID, targetStyleID, colorwayID int) error {
	// Remove the source mirror and any stale target duplicate before appending the canonical target
	// link. This keeps the denormalised table aligned with product.style_id in one transaction.
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE FROM tech_card_product
		WHERE product_id = :colorway AND tech_card_id IN (:source, :target)`, map[string]any{
		"source":   sourceStyleID,
		"target":   targetStyleID,
		"colorway": colorwayID,
	}); err != nil {
		return fmt.Errorf("remove old style mirror for colourway %d: %w", colorwayID, err)
	}
	if err := insertColorwayStyleMirror(ctx, db, targetStyleID, colorwayID); err != nil {
		return err
	}
	if err := storeutil.ExecNamed(ctx, db, `
		UPDATE product SET primary_tech_card_id = :target
		WHERE id = :colorway AND primary_tech_card_id = :source`, map[string]any{
		"source":   sourceStyleID,
		"target":   targetStyleID,
		"colorway": colorwayID,
	}); err != nil {
		return fmt.Errorf("repoint primary tech card for colourway %d: %w", colorwayID, err)
	}
	return nil
}
