package techcard

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// --- inserts (called within the AddTechCard / UpdateTechCard transaction) ---

// insertTechCardColorways inserts the colourways and returns their new ids in the
// same order, so BOM colorway_index can be resolved to a colourway id.
func insertTechCardColorways(ctx context.Context, db dependency.DB, tcID int, cws []entity.TechCardColorway) ([]int, error) {
	ids := make([]int, len(cws))
	for i := range cws {
		c := &cws[i]
		id, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_colorway
				(tech_card_id, code, name, lab_dip_status, product_id, comment, display_order)
			VALUES (:tech_card_id, :code, :name, :lab_dip_status, :product_id, :comment, :display_order)`,
			map[string]any{
				"tech_card_id":   tcID,
				"code":           c.Code,
				"name":           c.Name,
				"lab_dip_status": string(c.LabDipStatus),
				"product_id":     c.ProductId,
				"comment":        c.Comment,
				"display_order":  i,
			})
		if err != nil {
			return nil, fmt.Errorf("failed to insert tech card colorway: %w", err)
		}
		ids[i] = id
	}
	return ids, nil
}

// insertTechCardBom inserts the BOM lines and, for each, the per-colourway colour
// cells (resolving ColorwayIndex against colorwayIds).
func insertTechCardBom(ctx context.Context, db dependency.DB, tcID int, items []entity.TechCardBomItem, colorwayIds []int) error {
	for i := range items {
		b := &items[i]
		bomID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_bom_item
				(tech_card_id, section, name, placement, supplier, supplier_ref, color, composition, spec,
				 consumption, unit, quantity, unit_price, currency, comment, display_order)
			VALUES (:tech_card_id, :section, :name, :placement, :supplier, :supplier_ref, :color, :composition, :spec,
				 :consumption, :unit, :quantity, :unit_price, :currency, :comment, :display_order)`,
			map[string]any{
				"tech_card_id":  tcID,
				"section":       string(b.Section),
				"name":          b.Name,
				"placement":     b.Placement,
				"supplier":      b.Supplier,
				"supplier_ref":  b.SupplierRef,
				"color":         b.Color,
				"composition":   b.Composition,
				"spec":          b.Spec,
				"consumption":   b.Consumption,
				"unit":          b.Unit,
				"quantity":      b.Quantity,
				"unit_price":    b.UnitPrice,
				"currency":      b.Currency,
				"comment":       b.Comment,
				"display_order": i,
			})
		if err != nil {
			return fmt.Errorf("failed to insert tech card bom item: %w", err)
		}
		if len(b.ColorwayColors) == 0 {
			continue
		}
		rows := make([]map[string]any, 0, len(b.ColorwayColors))
		for j, cc := range b.ColorwayColors {
			if cc.ColorwayIndex < 0 || cc.ColorwayIndex >= len(colorwayIds) {
				return fmt.Errorf("bom colorway_index %d out of range", cc.ColorwayIndex)
			}
			rows = append(rows, map[string]any{
				"bom_item_id":   bomID,
				"colorway_id":   colorwayIds[cc.ColorwayIndex],
				"color":         cc.Color,
				"pantone":       cc.Pantone,
				"display_order": j,
			})
		}
		if err := storeutil.BulkInsert(ctx, db, "tech_card_bom_colorway", rows); err != nil {
			return fmt.Errorf("failed to insert tech card bom colorway: %w", err)
		}
	}
	return nil
}

// insertTechCardPom inserts the POM points and, for each, its graded values and
// actual measurements.
func insertTechCardPom(ctx context.Context, db dependency.DB, tcID int, points []entity.TechCardPomPoint) error {
	for i := range points {
		p := &points[i]
		pomID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_pom_point
				(tech_card_id, section, code, name, how_to_measure, base_value, tolerance_plus, tolerance_minus, display_order)
			VALUES (:tech_card_id, :section, :code, :name, :how_to_measure, :base_value, :tolerance_plus, :tolerance_minus, :display_order)`,
			map[string]any{
				"tech_card_id":    tcID,
				"section":         p.Section,
				"code":            p.Code,
				"name":            p.Name,
				"how_to_measure":  p.HowToMeasure,
				"base_value":      p.BaseValue,
				"tolerance_plus":  p.TolerancePlus,
				"tolerance_minus": p.ToleranceMinus,
				"display_order":   i,
			})
		if err != nil {
			return fmt.Errorf("failed to insert tech card pom point: %w", err)
		}
		if len(p.Grades) > 0 {
			rows := make([]map[string]any, 0, len(p.Grades))
			for _, g := range p.Grades {
				rows = append(rows, map[string]any{
					"pom_point_id": pomID,
					"size_id":      g.SizeId,
					"value":        g.Value,
				})
			}
			if err := storeutil.BulkInsert(ctx, db, "tech_card_pom_grade", rows); err != nil {
				return fmt.Errorf("failed to insert tech card pom grades: %w", err)
			}
		}
		if len(p.Actuals) > 0 {
			rows := make([]map[string]any, 0, len(p.Actuals))
			for j, a := range p.Actuals {
				rows = append(rows, map[string]any{
					"pom_point_id":  pomID,
					"fitting_id":    a.FittingId,
					"label":         a.Label,
					"value":         a.Value,
					"display_order": j,
				})
			}
			if err := storeutil.BulkInsert(ctx, db, "tech_card_pom_actual", rows); err != nil {
				return fmt.Errorf("failed to insert tech card pom actuals: %w", err)
			}
		}
	}
	return nil
}

// --- enrich (load materials for read paths) ---

type techCardColorwayRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardColorway
}

type techCardBomItemRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardBomItem
}

type techCardBomColorwayRow struct {
	BomItemID  int            `db:"bom_item_id"`
	ColorwayID int            `db:"colorway_id"`
	Color      sql.NullString `db:"color"`
	Pantone    sql.NullString `db:"pantone"`
}

type techCardPomPointRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardPomPoint
}

type techCardPomGradeRow struct {
	PomPointID int `db:"pom_point_id"`
	entity.TechCardPomGrade
}

type techCardPomActualRow struct {
	PomPointID int `db:"pom_point_id"`
	entity.TechCardPomActual
}

// enrichMaterials loads colourways, BOM lines (+ the per-colourway colour matrix)
// and POM points (+ grades and actuals) for each card and attaches them.
func (s *Store) enrichMaterials(ctx context.Context, cards []entity.TechCard) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]int, 0, len(cards))
	for i := range cards {
		ids = append(ids, cards[i].Id)
	}

	// Colourways: grouped per card (in display order), plus a colourway id -> index
	// map so the BOM colour matrix can be emitted by index.
	// product_id resolves through a LEFT JOIN that excludes soft-deleted products
	// (products are soft-deleted, so the ON DELETE SET NULL never fires) — a dead
	// SKU surfaces as NULL instead of a dangling id, mirroring productIdsByTechCardIds.
	cwRows, err := storeutil.QueryListNamed[techCardColorwayRow](ctx, s.DB, `
		SELECT c.id, c.tech_card_id, c.code, c.name, c.lab_dip_status, p.id AS product_id, c.comment
		FROM tech_card_colorway c
		LEFT JOIN product p ON p.id = c.product_id AND p.deleted_at IS NULL
		WHERE c.tech_card_id IN (:ids)
		ORDER BY c.tech_card_id, c.display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card colorways: %w", err)
	}
	colorwaysByCard := make(map[int][]entity.TechCardColorway, len(ids))
	colorwayIndexByID := make(map[int]int, len(cwRows))
	for _, r := range cwRows {
		colorwayIndexByID[r.Id] = len(colorwaysByCard[r.TechCardID])
		colorwaysByCard[r.TechCardID] = append(colorwaysByCard[r.TechCardID], r.TechCardColorway)
	}

	// BOM lines per card.
	bomRows, err := storeutil.QueryListNamed[techCardBomItemRow](ctx, s.DB, `
		SELECT id, tech_card_id, section, name, placement, supplier, supplier_ref, color, composition, spec,
		       consumption, unit, quantity, unit_price, currency, comment
		FROM tech_card_bom_item
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card bom items: %w", err)
	}
	bomByCard := make(map[int][]entity.TechCardBomItem, len(ids))
	for _, r := range bomRows {
		bomByCard[r.TechCardID] = append(bomByCard[r.TechCardID], r.TechCardBomItem)
	}
	// Slices are final now; index BOM items by id to attach the colour matrix.
	bomItemByID := make(map[int]*entity.TechCardBomItem, len(bomRows))
	bomItemIDs := make([]int, 0, len(bomRows))
	for card := range bomByCard {
		items := bomByCard[card]
		for i := range items {
			bomItemByID[items[i].Id] = &items[i]
			bomItemIDs = append(bomItemIDs, items[i].Id)
		}
	}
	if len(bomItemIDs) > 0 {
		cells, err := storeutil.QueryListNamed[techCardBomColorwayRow](ctx, s.DB, `
			SELECT bom_item_id, colorway_id, color, pantone
			FROM tech_card_bom_colorway
			WHERE bom_item_id IN (:ids)
			ORDER BY bom_item_id, display_order`, map[string]any{"ids": bomItemIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card bom colorways: %w", err)
		}
		for _, c := range cells {
			item, ok := bomItemByID[c.BomItemID]
			if !ok {
				continue
			}
			idx, ok := colorwayIndexByID[c.ColorwayID]
			if !ok {
				continue
			}
			item.ColorwayColors = append(item.ColorwayColors, entity.TechCardBomColorwayColor{
				ColorwayIndex: idx,
				Color:         c.Color,
				Pantone:       c.Pantone,
			})
		}
	}

	// POM points per card, then grades and actuals per point.
	pomRows, err := storeutil.QueryListNamed[techCardPomPointRow](ctx, s.DB, `
		SELECT id, tech_card_id, section, code, name, how_to_measure, base_value, tolerance_plus, tolerance_minus
		FROM tech_card_pom_point
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card pom points: %w", err)
	}
	pomByCard := make(map[int][]entity.TechCardPomPoint, len(ids))
	for _, r := range pomRows {
		pomByCard[r.TechCardID] = append(pomByCard[r.TechCardID], r.TechCardPomPoint)
	}
	pomPointByID := make(map[int]*entity.TechCardPomPoint, len(pomRows))
	pomPointIDs := make([]int, 0, len(pomRows))
	for card := range pomByCard {
		points := pomByCard[card]
		for i := range points {
			pomPointByID[points[i].Id] = &points[i]
			pomPointIDs = append(pomPointIDs, points[i].Id)
		}
	}
	if len(pomPointIDs) > 0 {
		gradeRows, err := storeutil.QueryListNamed[techCardPomGradeRow](ctx, s.DB, `
			SELECT pom_point_id, size_id, value
			FROM tech_card_pom_grade
			WHERE pom_point_id IN (:ids)
			ORDER BY pom_point_id, id`, map[string]any{"ids": pomPointIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card pom grades: %w", err)
		}
		for _, g := range gradeRows {
			if p, ok := pomPointByID[g.PomPointID]; ok {
				p.Grades = append(p.Grades, g.TechCardPomGrade)
			}
		}
		actualRows, err := storeutil.QueryListNamed[techCardPomActualRow](ctx, s.DB, `
			SELECT pom_point_id, fitting_id, label, value
			FROM tech_card_pom_actual
			WHERE pom_point_id IN (:ids)
			ORDER BY pom_point_id, display_order`, map[string]any{"ids": pomPointIDs})
		if err != nil {
			return fmt.Errorf("can't load tech card pom actuals: %w", err)
		}
		for _, a := range actualRows {
			if p, ok := pomPointByID[a.PomPointID]; ok {
				p.Actuals = append(p.Actuals, a.TechCardPomActual)
			}
		}
	}

	for i := range cards {
		id := cards[i].Id
		cards[i].Colorways = colorwaysByCard[id]
		cards[i].BomItems = bomByCard[id]
		points := pomByCard[id]
		sortPomGradesBySizeOrder(points, cards[i].SizeIds)
		cards[i].PomPoints = points
	}
	return nil
}

// sortPomGradesBySizeOrder orders each point's grades by the card's declared size
// range (tech_card_size order), so the graded chart renders its size columns in a
// stable, meaningful order instead of insertion order. cards[i].SizeIds is already
// populated by enrich before enrichMaterials runs.
func sortPomGradesBySizeOrder(points []entity.TechCardPomPoint, sizeIds []int) {
	if len(sizeIds) == 0 {
		return
	}
	rank := make(map[int]int, len(sizeIds))
	for i, sid := range sizeIds {
		rank[sid] = i
	}
	order := func(sid int) int {
		if r, ok := rank[sid]; ok {
			return r
		}
		return len(sizeIds) // sizes outside the range (shouldn't happen) sort last
	}
	for i := range points {
		g := points[i].Grades
		sort.SliceStable(g, func(a, b int) bool { return order(g[a].SizeId) < order(g[b].SizeId) })
	}
}
