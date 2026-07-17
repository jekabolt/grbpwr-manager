package product

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// stylePatchParams maps a StylePatch onto the shared styleFieldsSet SQL bind names — the same set the
// legacy writeStyleFields wrote, now owned solely by UpdateStyle (R4/§14.7). season_year is preserved
// (COALESCE in styleFieldsSet); category_id stays a PLM/UpdateTechCard fact and is not written here.
func stylePatchParams(p entity.StylePatch) map[string]any {
	return map[string]any{
		"brand":              p.Brand,
		"seasonCode":         string(p.Season),
		"collection":         p.Collection,
		"targetGender":       string(p.TargetGender),
		"fit":                p.Fit,
		"composition":        p.Composition,
		"careInstructions":   p.CareInstructions,
		"modelWearsHeightCm": p.ModelWearsHeightCm,
		"modelWearsSizeId":   p.ModelWearsSizeId,
		"topCategoryId":      p.TopCategoryId,
		"subCategoryId":      p.SubCategoryId,
		"typeId":             p.TypeId,
	}
}

// UpdateStyle is the SOLE writer of a style's catalogue facts (brand/sku_season/collection/
// target_gender/fit/composition/care/model-wears/categories) — R4/§14.7. It is optimistically locked
// on the shared tech_card.lock_version (entity.ErrTechCardConflict on a stale value or a concurrent
// bump -> ABORTED; sql.ErrNoRows when the style is absent -> NOT_FOUND). A change to a SKU fact (the
// season code) re-mints EVERY unfrozen sibling colourway in the same tx; if ANY sibling is SKU-frozen
// (sku_locked_at set, has order/label history) the whole change is refused with
// entity.ErrStyleFrozenSiblings (FAILED_PRECONDITION) — the official path is CloneStyleForSeason. PLM
// facts (BOM/POM/ops/lifecycle) remain UpdateTechCard's; no fact is written by both. Returns the new
// shared lock_version.
func (s *Store) UpdateStyle(ctx context.Context, styleID, expectedLockVersion int, patch entity.StylePatch) (int, error) {
	var newLockVersion int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			LockVersion int            `db:"lock_version"`
			SeasonCode  sql.NullString `db:"season_code"`
		}](ctx, rep.DB(),
			`SELECT lock_version, season_code FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
		if err != nil {
			return err // sql.ErrNoRows -> NOT_FOUND upstream
		}
		if cur.LockVersion != expectedLockVersion {
			return entity.ErrTechCardConflict
		}
		// The season is the only SKU fact UpdateStyle can change; when it moves, the frozen policy applies.
		skuFactsChanged := cur.SeasonCode.String != string(patch.Season)
		if skuFactsChanged {
			frozen, err := storeutil.QueryNamedOne[struct {
				N int `db:"n"`
			}](ctx, rep.DB(),
				`SELECT COUNT(*) AS n FROM product WHERE style_id = :id AND sku_locked_at IS NOT NULL`,
				map[string]any{"id": styleID})
			if err != nil {
				return fmt.Errorf("check frozen siblings of style %d: %w", styleID, err)
			}
			if frozen.N > 0 {
				return entity.ErrStyleFrozenSiblings
			}
		}
		params := stylePatchParams(patch)
		params["styleId"] = styleID
		params["expected"] = expectedLockVersion
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE tech_card SET`+styleFieldsSet+`, lock_version = lock_version + 1 WHERE id = :styleId AND lock_version = :expected`,
			params)
		if err != nil {
			return fmt.Errorf("update style %d: %w", styleID, err)
		}
		// The row provably exists (loaded above), so 0 rows means the lock moved under us.
		if rows == 0 {
			return entity.ErrTechCardConflict
		}
		if skuFactsChanged {
			// Re-mint every sibling — we proved above that none is frozen (MintProductSKUs is a no-op on
			// a frozen product anyway, but the guard already refused the change if any sibling was frozen).
			ids, err := captureStyleColorwayIDs(ctx, rep.DB(), styleID)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := MintProductSKUs(ctx, rep.DB(), id); err != nil {
					return fmt.Errorf("re-mint colourway %d after style %d SKU change: %w", id, styleID, err)
				}
			}
		}
		// P4-flyover M1 (04-MAZE-FLYOVER.md): re-derive the structural composition (S17/WS3-Ф5) from
		// the style's shell-fabric BOM every save; a manual override already on file is preserved
		// (entity.ReconcileStyleComposition). A field-tagged error here (a fabric's own composition
		// does not sum to 100) aborts the whole style save, same as any other validation failure.
		if err := ReconcileStyleCompositionTx(ctx, rep.DB(), styleID); err != nil {
			return err
		}
		newLockVersion = expectedLockVersion + 1
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newLockVersion, nil
}

// captureStyleColorwayIDs returns the ids of every colourway (product) of a style (R1: a colourway is
// a product with product.style_id = the style).
func captureStyleColorwayIDs(ctx context.Context, db dependency.DB, styleID int) ([]int, error) {
	rows, err := storeutil.QueryListNamed[struct {
		ID int `db:"id"`
	}](ctx, db, `SELECT id FROM product WHERE style_id = :id`, map[string]any{"id": styleID})
	if err != nil {
		return nil, fmt.Errorf("capture colourways of style %d: %w", styleID, err)
	}
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids, nil
}
