package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// UpdateColorwayRecipe replaces a colourway's material recipe (usages), restoring the write-path cut
// in the R1 merge — ColorwayDevelopmentInsert.usages was accepted on the wire but never written (the
// silent no-op, A3.4). The recipe is a colourway-owned sub-aggregate (R2/R4): it is optimistically
// locked on the shared tech_card.lock_version and each usage references a style BOM line by its
// stable line_key (S2/S3), resolved to a real bom_item_id FK. Returns the bumped lock_version. A
// mismatched version yields ErrTechCardConflict; a missing colourway yields sql.ErrNoRows.
func (s *Store) UpdateColorwayRecipe(ctx context.Context, colorwayID, expectedVersion int, usages []entity.TechCardColorwayUsage) (int, error) {
	newVersion := expectedVersion + 1
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// The colourway's optimistic version is its style's shared tech_card.lock_version (R2/R4).
		cur, err := storeutil.QueryNamedOne[struct {
			StyleID     int `db:"style_id"`
			LockVersion int `db:"lock_version"`
		}](ctx, rep.DB(),
			`SELECT p.style_id, t.lock_version FROM product p JOIN tech_card t ON t.id = p.style_id WHERE p.id = :id`,
			map[string]any{"id": colorwayID})
		if err != nil {
			return fmt.Errorf("load colourway %d recipe lock: %w", colorwayID, err) // sql.ErrNoRows -> NotFound upstream
		}
		if cur.LockVersion != expectedVersion {
			return entity.ErrTechCardConflict
		}

		// Resolve the style's BOM: by stable line_key (preferred) and ordered for the legacy index ref.
		bomRows, err := storeutil.QueryListNamed[bomExistingRow](ctx, rep.DB(),
			`SELECT id, line_key FROM tech_card_bom_item WHERE tech_card_id = :id ORDER BY display_order, id`,
			map[string]any{"id": cur.StyleID})
		if err != nil {
			return fmt.Errorf("load style bom for recipe: %w", err)
		}
		byKey := make(map[string]int, len(bomRows))
		ordered := make([]int, 0, len(bomRows))
		for _, r := range bomRows {
			byKey[r.LineKey] = r.Id
			ordered = append(ordered, r.Id)
		}

		// Full-replace this colourway's usages (per-size consumptions cascade on delete).
		if err := storeutil.ExecNamed(ctx, rep.DB(),
			`DELETE FROM tech_card_colorway_usage WHERE colorway_id = :id`, map[string]any{"id": colorwayID}); err != nil {
			return fmt.Errorf("clear colourway %d usages: %w", colorwayID, err)
		}
		for i := range usages {
			u := &usages[i]
			bomItemID, err := resolveUsageBom(u, byKey, ordered, i)
			if err != nil {
				return err
			}
			usageID, err := storeutil.ExecNamedLastId(ctx, rep.DB(), `
				INSERT INTO tech_card_colorway_usage
					(colorway_id, bom_item_id, bom_item_index, placement, color, pantone, consumption, quantity, piece_index, display_order)
				VALUES (:colorway_id, :bom_item_id, :bom_item_index, :placement, :color, :pantone, :consumption, :quantity, :piece_index, :display_order)`,
				map[string]any{
					"colorway_id":    colorwayID,
					"bom_item_id":    bomItemID,
					"bom_item_index": u.BomItemIndex,
					"placement":      u.Placement,
					"color":          u.Color,
					"pantone":        u.Pantone,
					"consumption":    u.Consumption,
					"quantity":       u.Quantity,
					"piece_index":    u.PieceIndex,
					"display_order":  i,
				})
			if err != nil {
				return fmt.Errorf("insert colourway usage: %w", err)
			}
			for j := range u.SizeConsumptions {
				sc := &u.SizeConsumptions[j]
				if err := storeutil.ExecNamed(ctx, rep.DB(), `
					INSERT INTO tech_card_colorway_usage_consumption (usage_id, size_id, consumption, display_order)
					VALUES (:usage_id, :size_id, :consumption, :display_order)`,
					map[string]any{"usage_id": usageID, "size_id": sc.SizeId, "consumption": sc.Consumption, "display_order": j}); err != nil {
					return fmt.Errorf("insert usage consumption: %w", err)
				}
			}
		}

		// Bump the shared lock under the guard — a recipe write is a mutation of the style aggregate,
		// so a concurrent style/colourway edit holding the old version is rejected.
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE tech_card SET lock_version = lock_version + 1 WHERE id = :id AND lock_version = :ver`,
			map[string]any{"id": cur.StyleID, "ver": expectedVersion})
		if err != nil {
			return fmt.Errorf("bump lock for recipe: %w", err)
		}
		if rows == 0 {
			return entity.ErrTechCardConflict
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, entity.ErrTechCardConflict) {
			return 0, err
		}
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return 0, err
		}
		return 0, fmt.Errorf("can't update colourway %d recipe: %w", colorwayID, err)
	}
	return newVersion, nil
}

// resolveUsageBom resolves a usage's BOM reference to a real bom_item id: by stable line_key
// (preferred), else the legacy positional index, else SQL NULL. An unknown line_key is field-tagged.
func resolveUsageBom(u *entity.TechCardColorwayUsage, byKey map[string]int, ordered []int, i int) (any, error) {
	if key := strings.TrimSpace(u.BomLineKey); key != "" {
		if id, ok := byKey[key]; ok {
			return id, nil
		}
		return nil, entity.NewFieldViolation(fmt.Sprintf("usages[%d].bom_line_key", i),
			fmt.Sprintf("no BOM line %q in this style", key), "", "reference an existing BOM line by its line_key")
	}
	if u.BomItemIndex.Valid {
		if idx := int(u.BomItemIndex.Int32); idx >= 0 && idx < len(ordered) {
			return ordered[idx], nil
		}
	}
	return nil, nil
}
