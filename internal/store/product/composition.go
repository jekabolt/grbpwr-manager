package product

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// P4-flyover M1 (04-MAZE-FLYOVER.md): entity.DeriveStyleComposition / ReconcileStyleComposition (S17,
// WS3-Ф5) landed as dead schema — nothing called them and nothing read/wrote fiber / material_composition
// / bom_item_composition / style_composition. This file is the store-layer wiring the plan deferred:
// derive a style's garment fibre composition from its shell-fabric BOM lines and persist the reconciled
// result, called from UpdateStyle (below) and from techcard.UpdateColorwayRecipe.

// bomFabricRow is one shell-fabric (section='fabric') BOM line of a style: its id (to look up a
// bom_item_composition override/snapshot) and, if linked to a catalog material, that material's id (the
// material_composition fallback).
type bomFabricRow struct {
	BomItemID  int           `db:"id"`
	MaterialID sql.NullInt64 `db:"material_id"`
}

type fiberPercentRow struct {
	FiberCode string          `db:"fiber_code"`
	Percent   decimal.Decimal `db:"percent"`
}

func toFiberPercents(rows []fiberPercentRow) []entity.FiberPercent {
	out := make([]entity.FiberPercent, 0, len(rows))
	for _, r := range rows {
		out = append(out, entity.FiberPercent{FiberCode: r.FiberCode, Percent: r.Percent})
	}
	return out
}

// loadFabricComposition resolves one shell-fabric BOM line's fibre composition (Ф5 priority): the
// line's own bom_item_composition (override-or-catalog-snapshot) wins when present; otherwise it falls
// back to its linked catalog material's material_composition. A line with neither — the common case
// until a material/BOM line is actually given a fibre breakdown (there is no CRUD/RPC for this yet, see
// 04-MAZE-FLYOVER.md M1 follow-up) — returns no rows.
func loadFabricComposition(ctx context.Context, db dependency.DB, row bomFabricRow) ([]entity.FiberPercent, error) {
	rows, err := storeutil.QueryListNamed[fiberPercentRow](ctx, db,
		`SELECT fiber_code, percent FROM bom_item_composition WHERE bom_item_id = :id`,
		map[string]any{"id": row.BomItemID})
	if err != nil {
		return nil, fmt.Errorf("load bom item %d composition: %w", row.BomItemID, err)
	}
	if len(rows) == 0 && row.MaterialID.Valid {
		rows, err = storeutil.QueryListNamed[fiberPercentRow](ctx, db,
			`SELECT fiber_code, percent FROM material_composition WHERE material_id = :id`,
			map[string]any{"id": row.MaterialID.Int64})
		if err != nil {
			return nil, fmt.Errorf("load material %d composition: %w", row.MaterialID.Int64, err)
		}
	}
	return toFiberPercents(rows), nil
}

// styleCompositionRow is a persisted style_composition row.
type styleCompositionRow struct {
	FiberCode string          `db:"fiber_code"`
	Percent   decimal.Decimal `db:"percent"`
	Source    string          `db:"source"`
}

// ReconcileStyleCompositionTx re-derives a style's garment fibre composition from its shell-fabric
// (tech_card_bom_item.section='fabric') BOM lines and persists the reconciled result to
// style_composition (S17 / WS3-Ф5). It must run inside the same transaction as a write that can affect
// the style's BOM/recipe — the derive reads whatever is currently committed in this tx, so it reflects
// what the caller's write just did.
//
// A shell-fabric line with no known composition (neither a bom_item_composition override/snapshot nor,
// via its material_id, a material_composition row) is excluded from the derive input entirely rather
// than counted as a defined-but-empty fabric — the latter would make DeriveStyleComposition's equal
// division starve every OTHER fabric's share and spuriously reject the total-100 check, which would
// break every existing style today (no BOM line has composition data yet; there is no CRUD to author
// one — 04-MAZE-FLYOVER.md M1 follow-up). A style with zero known-composition fabrics simply derives an
// empty auto set.
//
// A manual override (style_composition rows with source='manual') already on file is NEVER overwritten
// (entity.ReconcileStyleComposition, closing the S17 second defect) — this only refreshes the auto set.
func ReconcileStyleCompositionTx(ctx context.Context, db dependency.DB, techCardID int) error {
	fabricRows, err := storeutil.QueryListNamed[bomFabricRow](ctx, db,
		`SELECT id, material_id FROM tech_card_bom_item WHERE tech_card_id = :id AND section = :section`,
		map[string]any{"id": techCardID, "section": string(entity.BomSectionFabric)})
	if err != nil {
		return fmt.Errorf("load style %d shell fabric bom lines: %w", techCardID, err)
	}

	fabrics := make([][]entity.FiberPercent, 0, len(fabricRows))
	for _, row := range fabricRows {
		fp, err := loadFabricComposition(ctx, db, row)
		if err != nil {
			return err
		}
		if len(fp) == 0 {
			continue
		}
		fabrics = append(fabrics, fp)
	}

	derived, err := entity.DeriveStyleComposition(fabrics)
	if err != nil {
		return err // field-tagged: a fabric line's own composition does not itself sum to 100
	}

	currentRows, err := storeutil.QueryListNamed[styleCompositionRow](ctx, db,
		`SELECT fiber_code, percent, source FROM style_composition WHERE tech_card_id = :id`,
		map[string]any{"id": techCardID})
	if err != nil {
		return fmt.Errorf("load style %d current composition: %w", techCardID, err)
	}
	currentSource := entity.CompositionSourceAuto
	currentManual := make([]entity.FiberPercent, 0, len(currentRows))
	for _, r := range currentRows {
		if r.Source == entity.CompositionSourceManual {
			currentSource = entity.CompositionSourceManual
		}
		currentManual = append(currentManual, entity.FiberPercent{FiberCode: r.FiberCode, Percent: r.Percent})
	}
	if currentSource != entity.CompositionSourceManual {
		currentManual = nil // only meaningful alongside source=manual
	}

	source, rows := entity.ReconcileStyleComposition(currentSource, currentManual, derived)

	if err := storeutil.ExecNamed(ctx, db,
		`DELETE FROM style_composition WHERE tech_card_id = :id`, map[string]any{"id": techCardID}); err != nil {
		return fmt.Errorf("clear style %d composition: %w", techCardID, err)
	}
	for _, r := range rows {
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO style_composition (tech_card_id, fiber_code, percent, source)
			VALUES (:tech_card_id, :fiber_code, :percent, :source)`,
			map[string]any{
				"tech_card_id": techCardID,
				"fiber_code":   r.FiberCode,
				"percent":      r.Percent,
				"source":       source,
			}); err != nil {
			return fmt.Errorf("write style %d composition: %w", techCardID, err)
		}
	}
	return nil
}
