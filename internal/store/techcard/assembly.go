package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// ListStyleAssembly returns a garment style's assembly bill (WS7, §2.8): the auxiliary components
// (labels/tags) that physically go on/into it, each resolved with the component card's name, aux_subtype
// and output material (the warehouse material consumed in production) plus the size name when scoped.
func (s *Store) ListStyleAssembly(ctx context.Context, styleID int) ([]entity.StyleAssembly, error) {
	rows, err := storeutil.QueryListNamed[entity.StyleAssembly](ctx, s.DB, `
		SELECT sa.id, sa.style_id, sa.component_tech_card_id, sa.size_id, sa.qty,
		       sa.print_note, sa.position_note, sa.active, sa.lock_version, sa.created_by, sa.updated_by,
		       c.name AS component_name, c.aux_subtype AS component_aux_subtype, c.output_material_id,
		       m.name AS output_material_name, sz.name AS size_name
		FROM style_assembly sa
		JOIN tech_card c ON c.id = sa.component_tech_card_id
		LEFT JOIN material m ON m.id = c.output_material_id
		LEFT JOIN size sz ON sz.id = sa.size_id
		WHERE sa.style_id = :style_id
		ORDER BY sa.id`, map[string]any{"style_id": styleID})
	if err != nil {
		return nil, fmt.Errorf("can't list style assembly: %w", err)
	}
	return rows, nil
}

// UpsertStyleAssembly full-replaces a garment style's ENTIRE assembly bill (mirrors WS2
// UpsertPackagingRecipe): an empty items list clears it. Every component must be an AUXILIARY tech card
// (purpose=auxiliary — it produces the material the garment consumes), must not be the style itself, and
// the (component, size) pair must be unique in the payload; violations return field-tagged errors. The
// component/size FKs (RESTRICT) are the DB backstop.
func (s *Store) UpsertStyleAssembly(ctx context.Context, styleID int, items []entity.StyleAssemblyInsert, username string) error {
	seen := make(map[[2]int]bool, len(items))
	componentIDs := make([]int, 0, len(items))
	for i, it := range items {
		field := fmt.Sprintf("items[%d]", i)
		if it.ComponentTechCardId <= 0 {
			return entity.NewFieldViolation(field+".component_tech_card_id", "required", "", "pick an auxiliary tech card")
		}
		if it.ComponentTechCardId == styleID {
			return entity.NewFieldViolation(field+".component_tech_card_id", "self_reference",
				"the style itself", "a style cannot be its own assembly component")
		}
		if !it.Qty.IsPositive() {
			return entity.NewFieldViolation(field+".qty", "must_be_positive", "", "set a quantity > 0")
		}
		key := [2]int{it.ComponentTechCardId, sizeKey(it)}
		if seen[key] {
			return entity.NewFieldViolation(field+".component_tech_card_id", "duplicate",
				"same component and size already listed", "merge into one line and use qty")
		}
		seen[key] = true
		componentIDs = append(componentIDs, it.ComponentTechCardId)
	}

	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// Every referenced component must be an auxiliary card. One query, checked in Go for a clean
		// field-tagged error (the FK only guarantees the row exists, not its purpose).
		if len(componentIDs) > 0 {
			purposeRows, err := storeutil.QueryListNamed[struct {
				Id      int    `db:"id"`
				Purpose string `db:"purpose"`
			}](ctx, db, `SELECT id, purpose FROM tech_card WHERE id IN (:ids)`, map[string]any{"ids": componentIDs})
			if err != nil {
				return fmt.Errorf("load assembly component purposes: %w", err)
			}
			purposeByID := make(map[int]string, len(purposeRows))
			for _, r := range purposeRows {
				purposeByID[r.Id] = r.Purpose
			}
			for i, it := range items {
				p, ok := purposeByID[it.ComponentTechCardId]
				if !ok {
					return entity.NewFieldViolation(fmt.Sprintf("items[%d].component_tech_card_id", i),
						"not_found", fmt.Sprintf("tech card %d", it.ComponentTechCardId), "pick an existing auxiliary tech card")
				}
				if entity.TechCardPurpose(p) != entity.TechCardPurposeAuxiliary {
					return entity.NewFieldViolation(fmt.Sprintf("items[%d].component_tech_card_id", i),
						"not_auxiliary", fmt.Sprintf("tech card %d is %q", it.ComponentTechCardId, p),
						"assembly components must be auxiliary cards")
				}
			}
		}

		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM style_assembly WHERE style_id = :style_id`, map[string]any{"style_id": styleID}); err != nil {
			return fmt.Errorf("clear style assembly: %w", err)
		}
		for _, it := range items {
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO style_assembly
					(style_id, component_tech_card_id, size_id, qty, print_note, position_note, active, created_by, updated_by)
				VALUES
					(:style_id, :component_tech_card_id, :size_id, :qty, :print_note, :position_note, :active, :created_by, :updated_by)`,
				map[string]any{
					"style_id":               styleID,
					"component_tech_card_id": it.ComponentTechCardId,
					"size_id":                it.SizeId,
					"qty":                    it.Qty,
					"print_note":             it.PrintNote,
					"position_note":          it.PositionNote,
					"active":                 it.Active,
					"created_by":             username,
					"updated_by":             username,
				}); err != nil {
				return fmt.Errorf("insert style assembly component %d: %w", it.ComponentTechCardId, err)
			}
		}
		return nil
	})
}

// sizeKey folds an optional size scope into a dedup key (0 = all sizes).
func sizeKey(it entity.StyleAssemblyInsert) int {
	if it.SizeId.Valid {
		return int(it.SizeId.Int32)
	}
	return 0
}

// GetTechCardNames returns id → name for the given tech cards — a cheap header-only lookup used by the
// packing spec to label garment styles without an N+1 GetTechCardById. Absent ids are simply omitted.
func (s *Store) GetTechCardNames(ctx context.Context, ids []int) (map[int]string, error) {
	out := make(map[int]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := storeutil.QueryListNamed[struct {
		Id   int    `db:"id"`
		Name string `db:"name"`
	}](ctx, s.DB, `SELECT id, name FROM tech_card WHERE id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("can't load tech card names: %w", err)
	}
	for _, r := range rows {
		out[r.Id] = r.Name
	}
	return out, nil
}
