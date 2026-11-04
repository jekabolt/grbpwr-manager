package techcard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// CUT-TO-LENGTH STRIPS (0449). See entity/tech_card_cut_strip.go for what they are and why the norm is
// resolved on read. This file is the write and the raw read of the two tables.

// cutStripRows reads the strips of the given cards with their size rows, in display order.
func cutStripRows(ctx context.Context, db dependency.DB, ids []int) ([]entity.TechCardCutStrip, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	strips, err := storeutil.QueryListNamed[entity.TechCardCutStrip](ctx, db, `
		SELECT s.id, s.tech_card_id, s.bom_item_id, b.line_key AS bom_line_key, s.line_key, s.length_mode,
		       s.seam_key, s.fit_pct, s.overlap_mm, s.width_mm, s.one_length_mm, s.qty, s.display_order
		FROM tech_card_cut_strip s
		JOIN tech_card_bom_item b ON b.id = s.bom_item_id
		WHERE s.tech_card_id IN (:ids)
		ORDER BY s.tech_card_id, s.display_order, s.id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("load cut strips: %w", err)
	}
	if len(strips) == 0 {
		return nil, nil
	}
	stripIDs := make([]int, 0, len(strips))
	byID := make(map[int]*entity.TechCardCutStrip, len(strips))
	for i := range strips {
		stripIDs = append(stripIDs, strips[i].Id)
		byID[strips[i].Id] = &strips[i]
	}
	sizes, err := storeutil.QueryListNamed[entity.TechCardCutStripSize](ctx, db, `
		SELECT strip_id, size_id, seam_mm, manual_cut_mm
		FROM tech_card_cut_strip_size
		WHERE strip_id IN (:ids)
		ORDER BY strip_id, size_id`, map[string]any{"ids": stripIDs})
	if err != nil {
		return nil, fmt.Errorf("load cut strip sizes: %w", err)
	}
	for _, row := range sizes {
		if s, ok := byID[row.StripId]; ok {
			s.Sizes = append(s.Sizes, row)
		}
	}
	return strips, nil
}

// enrichCutStrips loads the strips and runs the read-time norm resolver on every card. Every reader of
// a norm walks an enriched card, so this is the single place the number appears.
func (s *Store) enrichCutStrips(ctx context.Context, cards []entity.TechCard) error {
	ids := make([]int, 0, len(cards))
	for i := range cards {
		ids = append(ids, cards[i].Id)
	}
	strips, err := cutStripRows(ctx, s.DB, ids)
	if err != nil {
		return err
	}
	if len(strips) == 0 {
		return nil
	}
	byCard := make(map[int][]entity.TechCardCutStrip, len(cards))
	for i := range strips {
		byCard[strips[i].TechCardId] = append(byCard[strips[i].TechCardId], strips[i])
	}
	for i := range cards {
		c := &cards[i]
		c.CutStrips = byCard[c.Id]
		entity.ApplyCutStripNorms(c.BomItems, c.CutStrips, c.SizeIds, c.Colorways)
	}
	return nil
}

// refuseCutStripsInUseBySeams refuses a write that would delete a strip a seam of the card still
// anchors on. It runs before the BOM upsert, because deleting a BOM line takes its strips with it by
// cascade: an unaware client that removes the line is covered too.
//
// On a snapshot restore the seam list is replaced later in the same transaction, so what the seams say
// today is not the question there.
func refuseCutStripsInUseBySeams(ctx context.Context, db dependency.DB, tcID int, tc *entity.TechCardInsert) error {
	if tc.RestoreSeams != nil {
		return nil
	}
	stored, err := cutStripRows(ctx, db, []int{tcID})
	if err != nil || len(stored) == 0 {
		return err
	}
	keptLine := make(map[string]bool, len(tc.BomItems))
	for i := range tc.BomItems {
		keptLine[entity.CutStripLineKey(tc.BomItems[i].LineKey)] = true
	}
	keptStrip := make(map[string]bool, len(tc.CutStrips))
	for i := range tc.CutStrips {
		keptStrip[entity.CutStripLineKey(tc.CutStrips[i].LineKey)] = true
	}
	doomed := make(map[string]bool)
	for i := range stored {
		key := entity.CutStripLineKey(stored[i].LineKey)
		if !keptLine[entity.CutStripLineKey(stored[i].BomLineKey)] || (tc.CutStripsAware && !keptStrip[key]) {
			doomed[key] = true
		}
	}
	if len(doomed) == 0 {
		return nil
	}
	sides, err := storeutil.QueryListNamed[struct {
		SideA []byte `db:"side_a"`
		SideB []byte `db:"side_b"`
	}](ctx, db, `SELECT side_a, side_b FROM tech_card_seam WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load seam anchors of tech card %d: %w", tcID, err)
	}
	for _, row := range sides {
		for _, raw := range [][]byte{row.SideA, row.SideB} {
			// A side that does not decode is a corrupted row, which the seam read reports as stale. It
			// is no reason to refuse a card save, and it names no strip this check could see.
			var anchors []entity.TechCardSeamAnchor
			if err := json.Unmarshal(raw, &anchors); err != nil {
				continue
			}
			for _, a := range anchors {
				if doomed[entity.CutStripLineKey(a.PieceLineKey)] {
					return entity.NewFieldViolation("cut_strips", "cut_strip_in_use", "",
						"a seam uses this strip; remove it from the seam first")
				}
			}
		}
	}
	return nil
}

// upsertTechCardCutStrips reconciles the strips by line_key, after the BOM (a strip's FK) and pieces and
// before the operations (whose inputs reference strips). Ids stay stable; size rows are replaced. Without
// the aware flag it does nothing: an older client leaves the strips alone.
func upsertTechCardCutStrips(ctx context.Context, db dependency.DB, tcID int, tc *entity.TechCardInsert, bomRes bomResolver) error {
	if !tc.CutStripsAware {
		return nil
	}
	if err := refuseCutStripsOnWrongLines(ctx, db, tcID, tc.CutStrips, bomRes); err != nil {
		return err
	}
	existing, err := storeutil.QueryListNamed[struct {
		Id      int    `db:"id"`
		LineKey string `db:"line_key"`
	}](ctx, db, `SELECT id, line_key FROM tech_card_cut_strip WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load cut strips of tech card %d: %w", tcID, err)
	}
	idByKey := make(map[string]int, len(existing))
	for _, row := range existing {
		idByKey[entity.CutStripLineKey(row.LineKey)] = row.Id
	}
	kept := make(map[string]bool, len(tc.CutStrips))
	for i := range tc.CutStrips {
		kept[entity.CutStripLineKey(tc.CutStrips[i].LineKey)] = true
	}
	var gone []int
	for _, row := range existing {
		if !kept[entity.CutStripLineKey(row.LineKey)] {
			gone = append(gone, row.Id)
		}
	}
	if len(gone) > 0 {
		if err := storeutil.ExecNamed(ctx, db, `DELETE FROM tech_card_cut_strip WHERE id IN (:ids)`,
			map[string]any{"ids": gone}); err != nil {
			return fmt.Errorf("delete cut strips of tech card %d: %w", tcID, err)
		}
	}
	for i := range tc.CutStrips {
		strip := &tc.CutStrips[i]
		bomID, ok := bomRes.byLineKey[strip.BomLineKey]
		if !ok {
			return entity.NewFieldViolation(fmt.Sprintf("cut_strips[%d].bom_line_key", i),
				"no_such_bom_line", "", "a strip is cut from a BOM line of this card")
		}
		params := map[string]any{
			"tech_card_id": tcID, "bom_item_id": bomID, "line_key": strip.LineKey, "length_mode": strip.LengthMode,
			"seam_key": strip.SeamKey, "fit_pct": strip.FitPct, "overlap_mm": strip.OverlapMm, "width_mm": strip.WidthMm,
			"one_length_mm": strip.OneLengthMm, "qty": strip.Qty, "display_order": i,
		}
		stripID, known := idByKey[entity.CutStripLineKey(strip.LineKey)]
		if known {
			params["id"] = stripID
			if err := storeutil.ExecNamed(ctx, db, `
				UPDATE tech_card_cut_strip
				SET bom_item_id = :bom_item_id, length_mode = :length_mode, seam_key = :seam_key, fit_pct = :fit_pct,
				    overlap_mm = :overlap_mm, width_mm = :width_mm, one_length_mm = :one_length_mm, qty = :qty,
				    display_order = :display_order
				WHERE id = :id`, params); err != nil {
				return fmt.Errorf("update cut strip %d: %w", stripID, err)
			}
			if err := storeutil.ExecNamed(ctx, db, `DELETE FROM tech_card_cut_strip_size WHERE strip_id = :id`,
				map[string]any{"id": stripID}); err != nil {
				return fmt.Errorf("clear sizes of cut strip %d: %w", stripID, err)
			}
		} else {
			id, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO tech_card_cut_strip
					(tech_card_id, bom_item_id, line_key, length_mode, seam_key, fit_pct, overlap_mm, width_mm,
					 one_length_mm, qty, display_order)
				VALUES (:tech_card_id, :bom_item_id, :line_key, :length_mode, :seam_key, :fit_pct, :overlap_mm, :width_mm,
					 :one_length_mm, :qty, :display_order)`, params)
			if err != nil {
				return fmt.Errorf("insert cut strip: %w", err)
			}
			stripID = id
		}
		if len(strip.Sizes) == 0 {
			continue
		}
		rows := make([]map[string]any, 0, len(strip.Sizes))
		for _, size := range strip.Sizes {
			rows = append(rows, map[string]any{
				"strip_id": stripID, "size_id": size.SizeId, "seam_mm": size.SeamMm, "manual_cut_mm": size.ManualCutMm,
			})
		}
		if err := storeutil.BulkInsert(ctx, db, "tech_card_cut_strip_size", rows); err != nil {
			return fmt.Errorf("insert sizes of cut strip %d: %w", stripID, err)
		}
	}
	return nil
}

// refuseCutStripsOnWrongLines holds the two rules of the mark: a strip's norm is metres, so the line is
// bought by the metre, and the main fabric is never a strip. The unit is the one every read shows, the
// linked article's over the line's own, which only the database can say.
func refuseCutStripsOnWrongLines(ctx context.Context, db dependency.DB, tcID int, strips []entity.TechCardCutStrip, bomRes bomResolver) error {
	if len(strips) == 0 {
		return nil
	}
	lines, err := storeutil.QueryListNamed[struct {
		LineKey string `db:"line_key"`
		Unit    string `db:"unit"`
		Section string `db:"section"`
		Purpose string `db:"purpose"`
	}](ctx, db, `
		SELECT bi.line_key, COALESCE(NULLIF(m.unit, ''), bi.unit, '') AS unit, COALESCE(bi.section, '') AS section,
		       COALESCE(bi.purpose, '') AS purpose
		FROM tech_card_bom_item bi
		LEFT JOIN material m ON m.id = bi.material_id
		WHERE bi.tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load BOM lines for cut strips of tech card %d: %w", tcID, err)
	}
	type lineFacts struct{ unit, section, purpose string }
	byKey := make(map[string]lineFacts, len(lines))
	for _, l := range lines {
		byKey[l.LineKey] = lineFacts{l.Unit, l.Section, l.Purpose}
	}
	for i := range strips {
		line, ok := byKey[strips[i].BomLineKey]
		if !ok {
			continue // the upsert below names the missing line
		}
		field := fmt.Sprintf("cut_strips[%d].bom_line_key", i)
		section := entity.TechCardBomSection(strings.ToLower(strings.TrimSpace(line.section)))
		switch entity.CutStripLineProblem(line.unit, section, line.purpose) {
		case "cut_strip_not_metres":
			return entity.NewFieldViolation(field, "cut_strip_not_metres", line.unit, "a strip is cut from a line bought by the metre")
		case "cut_strip_main_fabric":
			return entity.NewFieldViolation(field, "cut_strip_main_fabric", "", "the main fabric is cut as pieces")
		}
	}
	return nil
}

// pruneCutStripSizesOutsideRange is the strip half of pruneSizeScopedDataOutsideRange.
func pruneCutStripSizesOutsideRange(ctx context.Context, db dependency.DB, id int, sizeIDs []int) error {
	if err := storeutil.ExecNamed(ctx, db, `
		DELETE z FROM tech_card_cut_strip_size z
		JOIN tech_card_cut_strip s ON s.id = z.strip_id
		WHERE s.tech_card_id = :id AND z.size_id NOT IN (:sizes)`,
		map[string]any{"id": id, "sizes": sizeIDs}); err != nil {
		return fmt.Errorf("prune style %d cut strip sizes outside its size range: %w", id, err)
	}
	return nil
}

// cutStripIDsByKey is what the operation input writer resolves a strip key with.
func cutStripIDsByKey(ctx context.Context, db dependency.DB, tcID int) (map[string]int, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Id      int    `db:"id"`
		LineKey string `db:"line_key"`
	}](ctx, db, `SELECT id, line_key FROM tech_card_cut_strip WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return nil, fmt.Errorf("load cut strips for operation links: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.LineKey] = r.Id
	}
	return out, nil
}

// resolveCutStripRecipe is the resolver for the one norm reader that does not walk an enriched card:
// the colourway recipe read. Same function, same numbers.
func (s *Store) resolveCutStripRecipe(ctx context.Context, colorwayID int, usages []entity.TechCardColorwayUsage) ([]entity.TechCardColorwayUsage, error) {
	styles, err := storeutil.QueryScalarListNamed[int](ctx, s.DB,
		`SELECT style_id FROM product WHERE id = :id AND style_id IS NOT NULL`, map[string]any{"id": colorwayID})
	if err != nil {
		return nil, fmt.Errorf("load style of colourway %d: %w", colorwayID, err)
	}
	if len(styles) == 0 {
		return usages, nil
	}
	strips, err := cutStripRows(ctx, s.DB, styles[:1])
	if err != nil || len(strips) == 0 {
		return usages, err
	}
	bom, err := storeutil.QueryListNamed[entity.TechCardBomItem](ctx, s.DB, `
		SELECT bi.id, bi.line_key, COALESCE(NULLIF(m.unit, ''), bi.unit) AS unit
		FROM tech_card_bom_item bi
		LEFT JOIN material m ON m.id = bi.material_id
		WHERE bi.tech_card_id = :id
		ORDER BY bi.display_order, bi.id`, map[string]any{"id": styles[0]})
	if err != nil {
		return nil, fmt.Errorf("load style bom for cut strips: %w", err)
	}
	sizes, err := storeutil.QueryScalarListNamed[int](ctx, s.DB,
		`SELECT size_id FROM tech_card_size WHERE tech_card_id = :id ORDER BY display_order, size_id`, map[string]any{"id": styles[0]})
	if err != nil {
		return nil, fmt.Errorf("load size range for cut strips: %w", err)
	}
	return entity.ResolveCutStripUsages(usages, bom, entity.CutStripLineNorms(bom, strips, sizes)), nil
}

// sanitizeCutStripRecipe applies one rule of the recipe to its full-replace write: the norm of a strip
// line is not typed. A row of such a line keeps what is really the colourway's (the pinned article, the
// colour, the placement) and loses its numbers, and a row with nothing else on it is the echo of a row
// the resolver made up, which is not stored at all. A claim of cut_length on a line without a strip has
// no number behind it, so it is refused.
func sanitizeCutStripRecipe(ctx context.Context, db dependency.DB, styleID int, usages []entity.TechCardColorwayUsage) ([]entity.TechCardColorwayUsage, error) {
	strips, err := cutStripRows(ctx, db, []int{styleID})
	if err != nil {
		return nil, err
	}
	claims := false
	for i := range usages {
		claims = claims || usages[i].IsCutLengthNorm()
	}
	if len(strips) == 0 && !claims {
		return usages, nil
	}
	lines, err := storeutil.QueryListNamed[struct {
		Id      int    `db:"id"`
		LineKey string `db:"line_key"`
	}](ctx, db, `SELECT id, line_key FROM tech_card_bom_item WHERE tech_card_id = :id ORDER BY display_order, id`,
		map[string]any{"id": styleID})
	if err != nil {
		return nil, fmt.Errorf("load style bom for cut strips: %w", err)
	}
	stripLine := make(map[string]bool, len(strips))
	for i := range strips {
		stripLine[strips[i].BomLineKey] = true
	}
	out := make([]entity.TechCardColorwayUsage, 0, len(usages))
	for i := range usages {
		u := usages[i]
		key := strings.TrimSpace(u.BomLineKey)
		if key == "" && u.BomItemIndex.Valid {
			if idx := int(u.BomItemIndex.Int32); idx >= 0 && idx < len(lines) {
				key = lines[idx].LineKey
			}
		}
		if !stripLine[key] {
			if u.IsCutLengthNorm() {
				return nil, entity.NewFieldViolation(fmt.Sprintf("usages[%d].consumption_source", i),
					"cut_length_without_strip", "", "this line has no strip, so its norm is not a cut length")
			}
			out = append(out, u)
			continue
		}
		if strings.TrimSpace(u.PieceLineKey) != "" || u.PieceIndex.Valid {
			out = append(out, u) // a piece-bound row says which fabric, not how much
			continue
		}
		if entity.ClearCutStripRecipeRow(&u) {
			out = append(out, u)
		}
	}
	return out, nil
}
