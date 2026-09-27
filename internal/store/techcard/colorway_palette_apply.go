package techcard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/product"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ApplyColorwayPaletteToSlots is the explicit «apply to slots» door (T45, owner's decision 7): it
// colours the named material slots of a colourway from THAT colourway's saved palette. Editing the
// palette (UpdateColorway development.colours) never reaches the recipe; this is the only write
// that carries a palette colour into it, and only for the slots the caller names.
//
// For each assignment, every GARMENT-LEVEL usage of the slot's BOM line — no cut-piece, neither by
// id nor by the legacy position — takes color := the colour's label and pantone :=
// entity.SlotPantone(colour). A slot with no such row gets one colour-only garment-level row
// (manual provenance, no consumption, no pin), exactly the row the COLORWAYS tab and the proposal
// confirm create for a coloured slot. Nothing else moves: per-piece rows, consumption, quantity,
// pins, norm stamps and per-size consumption stay as they are — which is why this is not a
// full-replace UpdateColorwayRecipe that would need the whole recipe echoed back.
//
// Matching is by bom_item_id: every row the recipe writer stored since the stable line keys
// (0159) carries it. A legacy row that addresses its line only by position is not recoloured —
// the slot then reads as having no garment-level row, and gets one.
//
// Optimistically locked on the style's shared tech_card.lock_version (ErrTechCardConflict), refused
// on a released card (ErrTechCardReleased), ErrColorwayNoPalette when there is nothing to apply,
// field violations for a key that is not a BOM line of the style or a position the palette does not
// have. No composition reconcile (composition is derived from BOM lines, not usages) and no cost
// reseed (a colour is not a cost input).
func (s *Store) ApplyColorwayPaletteToSlots(ctx context.Context, colorwayID, expectedVersion int, assignments []entity.ColorwayPaletteSlotAssignment) (entity.ColorwayPaletteApplyResult, error) {
	assignments, ve := entity.NormalizePaletteSlotAssignments(assignments)
	if ve != nil {
		return entity.ColorwayPaletteApplyResult{}, ve
	}
	var res entity.ColorwayPaletteApplyResult
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res = entity.ColorwayPaletteApplyResult{} // the closure re-runs on a deadlock retry
		cur, err := storeutil.QueryNamedOne[struct {
			StyleID     int `db:"style_id"`
			LockVersion int `db:"lock_version"`
		}](ctx, rep.DB(),
			`SELECT p.style_id, t.lock_version FROM product p JOIN tech_card t ON t.id = p.style_id WHERE p.id = :id`,
			map[string]any{"id": colorwayID})
		if err != nil {
			return fmt.Errorf("load colourway %d palette-apply lock: %w", colorwayID, err) // sql.ErrNoRows -> NotFound upstream
		}
		if err := storeutil.RequireMutableTechCard(ctx, rep.DB(), cur.StyleID); err != nil {
			return err
		}
		if cur.LockVersion != expectedVersion {
			return entity.ErrTechCardConflict
		}

		palettes, err := product.ColorwayPalettesByID(ctx, rep.DB(), []int{colorwayID})
		if err != nil {
			return err
		}
		palette := palettes[colorwayID]
		if len(palette) == 0 {
			return fmt.Errorf("%w: colourway %d", entity.ErrColorwayNoPalette, colorwayID)
		}

		bomRows, err := storeutil.QueryListNamed[paletteApplyBomRow](ctx, rep.DB(),
			paletteApplyBomQuery, map[string]any{"id": cur.StyleID})
		if err != nil {
			return fmt.Errorf("load style %d bom for palette apply: %w", cur.StyleID, err)
		}
		bomByKey := make(map[string]int, len(bomRows))
		for _, r := range bomRows {
			bomByKey[r.LineKey] = r.ID
		}
		// Resolve every assignment before the first write: a refusal leaves the recipe untouched.
		bomIDs := make([]int, len(assignments))
		for i, a := range assignments {
			id, ok := bomByKey[a.BomLineKey]
			if !ok {
				return entity.NewFieldViolation(fmt.Sprintf("assignments[%d].bom_line_key", i), "unknown_slot",
					a.BomLineKey, "name a BOM line of this colourway's style by its line_key")
			}
			if a.ColourPosition >= len(palette) {
				return entity.NewFieldViolation(fmt.Sprintf("assignments[%d].colour_position", i), "out_of_palette",
					fmt.Sprintf("position %d of a %d-colour palette", a.ColourPosition, len(palette)),
					fmt.Sprintf("pick a position from 0 to %d, or save the palette first", len(palette)-1))
			}
			bomIDs[i] = id
		}

		order, err := storeutil.QueryNamedOne[struct {
			Max int `db:"max_order"`
		}](ctx, rep.DB(), paletteApplyMaxOrderQuery, map[string]any{"cw": colorwayID})
		if err != nil {
			return fmt.Errorf("load colourway %d recipe order: %w", colorwayID, err)
		}
		nextOrder := order.Max + 1

		for i, a := range assignments {
			colour := palette[a.ColourPosition]
			params := map[string]any{
				"cw":      colorwayID,
				"bom":     bomIDs[i],
				"color":   nullableSlotText(colour.Label),
				"pantone": nullableSlotText(entity.SlotPantone(colour)),
			}
			// COUNT first: an UPDATE's affected-rows is the CHANGED rows on this driver, and a slot
			// that already wears the colour must not read as «no row» and grow a second one.
			n, err := storeutil.QueryNamedOne[struct {
				N int `db:"n"`
			}](ctx, rep.DB(), paletteApplyCountQuery, params)
			if err != nil {
				return fmt.Errorf("count garment-level usages of colourway %d slot %d: %w", colorwayID, bomIDs[i], err)
			}
			if n.N > 0 {
				if err := storeutil.ExecNamed(ctx, rep.DB(), paletteApplyUpdateQuery, params); err != nil {
					return fmt.Errorf("recolour colourway %d slot %d: %w", colorwayID, bomIDs[i], err)
				}
				res.RowsUpdated += n.N
				continue
			}
			params["source"] = entity.ConsumptionSourceManual
			params["display_order"] = nextOrder
			if err := storeutil.ExecNamed(ctx, rep.DB(), paletteApplyInsertQuery, params); err != nil {
				return fmt.Errorf("add colour row to colourway %d slot %d: %w", colorwayID, bomIDs[i], err)
			}
			nextOrder++
			res.RowsCreated++
		}

		// A recipe write is a mutation of the style aggregate: bump the shared lock under the guard.
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE tech_card SET lock_version = lock_version + 1 WHERE id = :id AND lock_version = :ver`,
			map[string]any{"id": cur.StyleID, "ver": expectedVersion})
		if err != nil {
			return fmt.Errorf("bump lock for palette apply: %w", err)
		}
		if rows == 0 {
			return entity.ErrTechCardConflict
		}
		res.LockVersion = expectedVersion + 1
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, entity.ErrTechCardConflict) ||
			errors.Is(err, entity.ErrTechCardReleased) || errors.Is(err, entity.ErrColorwayNoPalette) {
			return entity.ColorwayPaletteApplyResult{}, err
		}
		var fv *entity.ValidationError
		if errors.As(err, &fv) {
			return entity.ColorwayPaletteApplyResult{}, err
		}
		return entity.ColorwayPaletteApplyResult{}, fmt.Errorf("can't apply colourway %d palette to slots: %w", colorwayID, err)
	}
	return res, nil
}

type paletteApplyBomRow struct {
	ID      int    `db:"id"`
	LineKey string `db:"line_key"`
}

// nullableSlotText stores "" as NULL — a recipe row's colour or Pantone nobody stated.
func nullableSlotText(v string) sql.NullString {
	return sql.NullString{String: v, Valid: v != ""}
}

// The statements are package constants so the DB-free binding test can hold them (a stray colon in
// a named query fails at bind time, not at compile time). «Garment-level» = no cut-piece by id AND
// none by the legacy position.
const (
	paletteApplyBomQuery = `SELECT id, line_key FROM tech_card_bom_item WHERE tech_card_id = :id`

	paletteApplyMaxOrderQuery = `
		SELECT COALESCE(MAX(display_order), -1) AS max_order
		FROM tech_card_colorway_usage
		WHERE colorway_id = :cw`

	paletteApplyCountQuery = `
		SELECT COUNT(*) AS n
		FROM tech_card_colorway_usage
		WHERE colorway_id = :cw AND bom_item_id = :bom AND piece_id IS NULL AND piece_index IS NULL`

	paletteApplyUpdateQuery = `
		UPDATE tech_card_colorway_usage
		SET color = :color, pantone = :pantone
		WHERE colorway_id = :cw AND bom_item_id = :bom AND piece_id IS NULL AND piece_index IS NULL`

	paletteApplyInsertQuery = `
		INSERT INTO tech_card_colorway_usage
			(colorway_id, bom_item_id, color, pantone, consumption_source, display_order)
		VALUES (:cw, :bom, :color, :pantone, :source, :display_order)`
)
