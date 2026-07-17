package product

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// RelinkDraftColorway moves a DRAFT colourway onto a different style (R4 official workaround for the
// frozen-sibling problem: CloneStyleForSeason a style under a new season, then relink the draft rather
// than re-minting frozen siblings). Only a DRAFT may be relinked — an ACTIVE/HIDDEN/ARCHIVED colourway
// has (or had) a public identity with order/label history bound to its style, so it is frozen to it
// (entity.ErrColorwayNotDraft). Both sides are optimistically guarded on their shared
// tech_card.lock_version (entity.ErrTechCardConflict on a stale value or a concurrent relink), the
// target style must exist (sql.ErrNoRows otherwise), and the colourway's SKU is re-minted from the
// target style's facts (season/model) so its identity reflects its new style.
func (s *Store) RelinkDraftColorway(ctx context.Context, colorwayID, targetStyleID, expectedColorwayVersion, expectedTargetStyleVersion int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cw, err := storeutil.QueryNamedOne[struct {
			StyleID int   `db:"style_id"`
			Status  uint8 `db:"lifecycle_status"`
		}](ctx, rep.DB(), `SELECT style_id, lifecycle_status FROM product WHERE id = :id`, map[string]any{"id": colorwayID})
		if err != nil {
			return err // sql.ErrNoRows -> NOT_FOUND upstream
		}
		if entity.ColorwayStatus(cw.Status) != entity.ColorwayStatusDraft {
			return fmt.Errorf("colourway %d: %w", colorwayID, entity.ErrColorwayNotDraft)
		}
		if cw.StyleID == targetStyleID {
			return fmt.Errorf("colourway %d already belongs to style %d", colorwayID, targetStyleID)
		}
		// The colourway's version is the shared lock of its CURRENT style.
		curLV, err := styleLockVersion(ctx, rep.DB(), cw.StyleID)
		if err != nil {
			return err
		}
		if curLV != expectedColorwayVersion {
			return entity.ErrTechCardConflict
		}
		tgtLV, err := styleLockVersion(ctx, rep.DB(), targetStyleID)
		if err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("target style %d not found: %w", targetStyleID, sql.ErrNoRows)
			}
			return err
		}
		if tgtLV != expectedTargetStyleVersion {
			return entity.ErrTechCardConflict
		}
		// Relink under a source-membership + still-draft guard, so a concurrent relink/publish is rejected.
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE product SET style_id = :target WHERE id = :id AND lifecycle_status = :draft AND style_id = :source`,
			map[string]any{"target": targetStyleID, "id": colorwayID, "draft": uint8(entity.ColorwayStatusDraft), "source": cw.StyleID})
		if err != nil {
			return fmt.Errorf("relink colourway %d to style %d: %w", colorwayID, targetStyleID, err)
		}
		if rows != 1 {
			return entity.ErrTechCardConflict
		}
		// Re-mint the colourway's SKU from the target style's facts (a no-op if it is SKU-frozen — but a
		// draft never is). The base/variant SKUs now reflect the target season/model.
		if err := MintProductSKUs(ctx, rep.DB(), colorwayID); err != nil {
			return fmt.Errorf("re-mint colourway %d after relink: %w", colorwayID, err)
		}
		return nil
	})
}

// styleLockVersion loads a style's shared optimistic-lock token (tech_card.lock_version); sql.ErrNoRows
// when the style is absent.
func styleLockVersion(ctx context.Context, db dependency.DB, styleID int) (int, error) {
	row, err := storeutil.QueryNamedOne[struct {
		LockVersion int `db:"lock_version"`
	}](ctx, db, `SELECT lock_version FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
	if err != nil {
		return 0, err
	}
	return row.LockVersion, nil
}
