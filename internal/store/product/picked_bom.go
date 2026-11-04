package product

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// A garment label with a source (stock or make, 0451) owns its BOM line. A BOM line no recipe row names
// is neither costed nor planned (entity/countable.go), and the recipe is written by another RPC that
// needs the saved line, so a pick that stopped at the BOM would show a line and cost nothing. The rule
// is kept here, on the server, on every write that can break it: the card save, the recipe save,
// a new colourway, a colourway moved in.
//
// The count lives on the slot (qty_per_garment), as for any countable line. Recipe rows are written
// with no number, so they read the slot and a label's new count needs no recipe write. A number a
// person typed on a recipe row is left alone: it wins over the slot by the pair rule.
//
// Packaging is not here on purpose: its count goes to stock at shipping and is priced apart, so a
// recipe row would charge it twice. A packaging BOM line carries no recipe rows.

const pickedLinesQuery = `
	SELECT l.bom_item_id AS bom_item_id, b.line_key AS line_key, b.section AS section,
	       SUM(IF(l.length_cm > 0, l.length_cm / 100, l.qty_per_garment)) AS qty
	FROM tech_card_garment_label l
	JOIN tech_card_bom_item b ON b.id = l.bom_item_id
	WHERE l.tech_card_id = :id AND l.label_source <> ''
	GROUP BY l.bom_item_id, b.line_key, b.section`

type pickedLineRow struct {
	BomItemID int             `db:"bom_item_id"`
	LineKey   string          `db:"line_key"`
	Section   string          `db:"section"`
	Qty       decimal.Decimal `db:"qty"`
}

// SyncPickedBomRecipe puts every line held by a picked label into the recipe of every colourway of the
// style and writes its count on the slot. Idempotent; a card with no picked label writes nothing.
func SyncPickedBomRecipe(ctx context.Context, db dependency.DB, styleID int) error {
	lines, err := storeutil.QueryListNamed[pickedLineRow](ctx, db, pickedLinesQuery, map[string]any{"id": styleID})
	if err != nil {
		return fmt.Errorf("load picked label lines of style %d: %w", styleID, err)
	}
	for _, l := range lines {
		section := entity.TechCardBomSection(strings.ToLower(strings.TrimSpace(l.Section)))
		if entity.IsCountableSection(section) {
			if err := storeutil.ExecNamed(ctx, db,
				`UPDATE tech_card_bom_item SET qty_per_garment = :qty WHERE id = :id`,
				map[string]any{"id": l.BomItemID, "qty": l.Qty}); err != nil {
				return fmt.Errorf("write count of picked line %d: %w", l.BomItemID, err)
			}
		}
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO tech_card_colorway_usage (colorway_id, bom_item_id, display_order)
			SELECT p.id, :bom, COALESCE((SELECT MAX(o.display_order) + 1 FROM tech_card_colorway_usage o WHERE o.colorway_id = p.id), 0)
			FROM product p
			WHERE p.style_id = :style AND NOT EXISTS (
				SELECT 1 FROM tech_card_colorway_usage u
				WHERE u.colorway_id = p.id AND u.bom_item_id = :bom AND u.piece_id IS NULL)`,
			map[string]any{"bom": l.BomItemID, "style": styleID}); err != nil {
			return fmt.Errorf("add picked line %d to recipes of style %d: %w", l.BomItemID, styleID, err)
		}
	}
	return nil
}

// ReleasePickedLines undoes what the sync wrote for lines this save takes from a picked label. The save
// calls it before it clears the labels and before the BOM upsert. A line leaving the BOM loses every row:
// the recipe FK is RESTRICT, and a line that came and went with its label must not make the save refuse.
// A line that stays but no picked label holds any more (unpicked, or the label moved to another line)
// loses the bare rows and the slot count, or they would read as authored. A row a person filled is theirs
// and stays. An older client's save replaces no label, so it gives up nothing but a line it dropped.
func ReleasePickedLines(ctx context.Context, db dependency.DB, styleID int, tc *entity.TechCardInsert) error {
	lines, err := storeutil.QueryListNamed[pickedLineRow](ctx, db, pickedLinesQuery, map[string]any{"id": styleID})
	if err != nil {
		return fmt.Errorf("load picked label lines of style %d: %w", styleID, err)
	}
	if len(lines) == 0 {
		return nil
	}
	staying := make(map[string]bool, len(tc.BomItems))
	for i := range tc.BomItems {
		staying[strings.TrimSpace(tc.BomItems[i].LineKey)] = true
	}
	heldKeys, heldIDs := map[string]bool{}, map[int]bool{}
	for i := range tc.GarmentLabels {
		l := &tc.GarmentLabels[i]
		if l.Source == "" {
			continue
		}
		if key := strings.TrimSpace(l.BomLineKey); key != "" {
			heldKeys[key] = true
		} else if l.BomItemId.Valid {
			heldIDs[int(l.BomItemId.Int32)] = true
		}
	}
	for _, l := range lines {
		arg := map[string]any{"id": l.BomItemID}
		if !staying[l.LineKey] {
			if err := storeutil.ExecNamed(ctx, db,
				`DELETE FROM tech_card_colorway_usage WHERE bom_item_id = :id`, arg); err != nil {
				return fmt.Errorf("release recipe rows of picked line %d: %w", l.BomItemID, err)
			}
			continue
		}
		if !tc.LabelsAware || heldKeys[l.LineKey] || heldIDs[l.BomItemID] {
			continue
		}
		if err := storeutil.ExecNamed(ctx, db, `
			DELETE u FROM tech_card_colorway_usage u
			WHERE u.bom_item_id = :id AND u.piece_id IS NULL AND u.piece_index IS NULL
			  AND u.consumption IS NULL AND u.quantity IS NULL AND u.material_id IS NULL
			  AND u.norm_marker_id IS NULL AND COALESCE(u.consumption_source, 'manual') = 'manual'
			  AND TRIM(COALESCE(u.placement, '')) = '' AND TRIM(COALESCE(u.color, '')) = ''
			  AND TRIM(COALESCE(u.pantone, '')) = ''
			  AND NOT EXISTS (SELECT 1 FROM tech_card_colorway_usage_consumption c WHERE c.usage_id = u.id)`,
			arg); err != nil {
			return fmt.Errorf("release bare recipe rows of line %d: %w", l.BomItemID, err)
		}
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE tech_card_bom_item SET qty_per_garment = NULL WHERE id = :id`, arg); err != nil {
			return fmt.Errorf("clear count of released line %d: %w", l.BomItemID, err)
		}
	}
	return nil
}
