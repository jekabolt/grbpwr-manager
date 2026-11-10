package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
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

// enrichCutStrips loads strips and resolves every derived line norm. Production enrichment has already
// attached the operation-owned topstitch lines.
func (s *Store) enrichCutStrips(ctx context.Context, cards []entity.TechCard) error {
	ids := make([]int, 0, len(cards))
	materialIDs := make([]int, 0)
	seenMaterial := make(map[int]bool)
	addMaterial := func(id int64) {
		if id > 0 && !seenMaterial[int(id)] {
			seenMaterial[int(id)] = true
			materialIDs = append(materialIDs, int(id))
		}
	}
	for i := range cards {
		ids = append(ids, cards[i].Id)
		for j := range cards[i].BomItems {
			if cards[i].BomItems[j].MaterialId.Valid {
				addMaterial(cards[i].BomItems[j].MaterialId.Int64)
			}
		}
		for j := range cards[i].Colorways {
			for k := range cards[i].Colorways[j].Usages {
				if cards[i].Colorways[j].Usages[k].MaterialId.Valid {
					addMaterial(cards[i].Colorways[j].Usages[k].MaterialId.Int64)
				}
			}
		}
	}
	strips, err := cutStripRows(ctx, s.DB, ids)
	if err != nil {
		return err
	}
	byCard := make(map[int][]entity.TechCardCutStrip, len(cards))
	for i := range strips {
		byCard[strips[i].TechCardId] = append(byCard[strips[i].TechCardId], strips[i])
	}
	// Widths matter only to roll strips; a page of cards without strips never loads the catalogue.
	var linked map[int]entity.MaterialWithPrice
	if len(strips) > 0 {
		if linked, err = s.getMaterialsByIDs(ctx, materialIDs); err != nil {
			return err
		}
	}
	for i := range cards {
		c := &cards[i]
		c.CutStrips = byCard[c.Id]
		entity.ApplyDerivedLineNormsWithMaterials(c.BomItems, c.CutStrips,
			entity.TopstitchLines(c.Operations), c.SizeIds, c.Colorways, linked)
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
// bought by the metre, and the main fabric is never a strip. The effective unit follows the shared BOM
// resolver, including the thread-metres exception, and only the database can provide both source units.
func refuseCutStripsOnWrongLines(ctx context.Context, db dependency.DB, tcID int, strips []entity.TechCardCutStrip, bomRes bomResolver) error {
	if len(strips) == 0 {
		return nil
	}
	lines, err := storeutil.QueryListNamed[struct {
		LineKey     string `db:"line_key"`
		SlotUnit    string `db:"slot_unit"`
		ArticleUnit string `db:"article_unit"`
		Section     string `db:"section"`
		Purpose     string `db:"purpose"`
	}](ctx, db, `
		SELECT bi.line_key, COALESCE(bi.unit, '') AS slot_unit, COALESCE(m.unit, '') AS article_unit,
		       COALESCE(bi.section, '') AS section,
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
		section := entity.TechCardBomSection(strings.ToLower(strings.TrimSpace(l.Section)))
		byKey[l.LineKey] = lineFacts{entity.ResolveBomLineUnit(section, l.SlotUnit, l.ArticleUnit), l.Section, l.Purpose}
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
	if err != nil {
		return usages, err
	}
	topByOp, err := loadOperationTopstitchLines(ctx, s.DB, styles[:1])
	if err != nil {
		return usages, err
	}
	var topstitch []entity.TechCardTopstitchLine
	for _, lines := range topByOp {
		topstitch = append(topstitch, lines...)
	}
	if len(strips) == 0 && len(topstitch) == 0 {
		return usages, nil
	}
	bomRows, err := storeutil.QueryListNamed[struct {
		entity.TechCardBomItem
		SlotUnit    sql.NullString `db:"slot_unit"`
		ArticleUnit sql.NullString `db:"article_unit"`
	}](ctx, s.DB, `
		SELECT bi.id, bi.line_key, bi.section, bi.purpose, bi.material_id, bi.fabric_width,
		       bi.unit AS slot_unit, m.unit AS article_unit, bi.thread_per_mm
		FROM tech_card_bom_item bi
		LEFT JOIN material m ON m.id = bi.material_id
		WHERE bi.tech_card_id = :id
		ORDER BY bi.display_order, bi.id`, map[string]any{"id": styles[0]})
	if err != nil {
		return nil, fmt.Errorf("load style bom for cut strips: %w", err)
	}
	bom := make([]entity.TechCardBomItem, 0, len(bomRows))
	for _, row := range bomRows {
		resolved := entity.ResolveBomLineUnit(row.Section, row.SlotUnit.String, row.ArticleUnit.String)
		row.Unit = sql.NullString{String: resolved, Valid: strings.TrimSpace(resolved) != ""}
		bom = append(bom, row.TechCardBomItem)
	}
	sizes, err := storeutil.QueryScalarListNamed[int](ctx, s.DB,
		`SELECT size_id FROM tech_card_size WHERE tech_card_id = :id ORDER BY display_order, size_id`, map[string]any{"id": styles[0]})
	if err != nil {
		return nil, fmt.Errorf("load size range for cut strips: %w", err)
	}
	materialIDs := make([]int, 0, len(bom)+len(usages))
	seen := make(map[int]bool)
	addMaterial := func(id int64) {
		if id > 0 && !seen[int(id)] {
			seen[int(id)] = true
			materialIDs = append(materialIDs, int(id))
		}
	}
	for i := range bom {
		if bom[i].MaterialId.Valid {
			addMaterial(bom[i].MaterialId.Int64)
		}
	}
	for i := range usages {
		if usages[i].MaterialId.Valid {
			addMaterial(usages[i].MaterialId.Int64)
		}
	}
	linked, err := s.getMaterialsByIDs(ctx, materialIDs)
	if err != nil {
		return nil, err
	}
	norms := entity.DerivedLineNormsForRecipe(bom, strips, topstitch, sizes, linked, usages)
	return entity.ResolveDerivedLineUsages(usages, bom, norms), nil
}

// cutStripStoredRow is a garment-level recipe row as the table holds it, on the way to being written back.
type cutStripStoredRow struct {
	Id                int                 `db:"id"`
	BomItemID         sql.NullInt64       `db:"bom_item_id"`
	BomItemIndex      sql.NullInt32       `db:"bom_item_index"`
	Placement         sql.NullString      `db:"placement"`
	Consumption       decimal.NullDecimal `db:"consumption"`
	Quantity          decimal.NullDecimal `db:"quantity"`
	ConsumptionSource string              `db:"consumption_source"`
	WasteSelvedgePct  decimal.NullDecimal `db:"waste_selvedge_pct"`
	WasteCutPct       decimal.NullDecimal `db:"waste_cut_pct"`
	NormMarkerID      sql.NullInt64       `db:"norm_marker_id"`
	NormAppliedAt     sql.NullTime        `db:"norm_applied_at"`
	sizes             []entity.TechCardBomSizeConsumption
	taken             bool
}

// cutStripStoredRows is a colourway's garment-level rows of its strip lines by line key, in stored order.
func cutStripStoredRows(ctx context.Context, db dependency.DB, colorwayID int, lineKeyOf func(*cutStripStoredRow) string, stripLine map[string]bool) (map[string][]*cutStripStoredRow, error) {
	rows, err := storeutil.QueryListNamed[cutStripStoredRow](ctx, db, `
		SELECT id, bom_item_id, bom_item_index, placement, consumption, quantity, consumption_source,
		       waste_selvedge_pct, waste_cut_pct, norm_marker_id, norm_applied_at
		FROM tech_card_colorway_usage
		WHERE colorway_id = :id AND piece_id IS NULL AND piece_index IS NULL
		ORDER BY display_order, id`, map[string]any{"id": colorwayID})
	if err != nil {
		return nil, fmt.Errorf("load colourway %d stored recipe of strip lines: %w", colorwayID, err)
	}
	out := make(map[string][]*cutStripStoredRow, len(stripLine))
	byID := make(map[int]*cutStripStoredRow, len(rows))
	ids := make([]int, 0, len(rows))
	for i := range rows {
		key := lineKeyOf(&rows[i])
		if !stripLine[key] {
			continue
		}
		out[key] = append(out[key], &rows[i])
		byID[rows[i].Id] = &rows[i]
		ids = append(ids, rows[i].Id)
	}
	if len(ids) == 0 {
		return out, nil
	}
	sizes, err := storeutil.QueryListNamed[struct {
		UsageID     int             `db:"usage_id"`
		SizeID      int             `db:"size_id"`
		Consumption decimal.Decimal `db:"consumption"`
	}](ctx, db, `
		SELECT usage_id, size_id, consumption FROM tech_card_colorway_usage_consumption
		WHERE usage_id IN (:ids) ORDER BY usage_id, display_order, id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, fmt.Errorf("load colourway %d stored per-size norms of strip lines: %w", colorwayID, err)
	}
	for _, row := range sizes {
		byID[row.UsageID].sizes = append(byID[row.UsageID].sizes, entity.TechCardBomSizeConsumption{SizeId: row.SizeID, Consumption: row.Consumption})
	}
	return out, nil
}

// sanitizeCutStripRecipe applies one rule of the recipe to its full-replace write: a derived line norm
// is not typed. What a client sends for a garment-level row of such a line is what really is the
// colourway's (the pinned article, the colour, the placement); the numbers of the row come from the table.
//
// The wire row has no id, so a stored row is found by its line and placement, and a row left over then
// takes a stored row left over on its line: a renamed placement is the same row. Whatever a stored row
// holds is written back as it is, so that removing the strip gives the norm back; the second result says
// which rows of the first have their provenance decided here, by position. A stored row nobody sent is
// deleted, as in any full replace.
//
// A row with no stored row behind it has no numbers to keep. With nothing else on it, it is the echo of a
// row the resolver made up, which is not stored at all. A row of an archive being applied brings its
// stored numbers along. A claim of cut_length on a line without a strip has no number behind it, so it is
// refused.
func sanitizeCutStripRecipe(ctx context.Context, db dependency.DB, styleID, colorwayID int, usages []entity.TechCardColorwayUsage) ([]entity.TechCardColorwayUsage, map[int]usageProvenance, error) {
	strips, err := cutStripRows(ctx, db, []int{styleID})
	if err != nil {
		return nil, nil, err
	}
	topRows, err := operationTopstitchRows(ctx, db, []int{styleID})
	if err != nil {
		return nil, nil, err
	}
	claims := false
	for i := range usages {
		claims = claims || usages[i].IsCutLengthNorm()
	}
	if len(strips) == 0 && len(topRows) == 0 && !claims {
		return usages, nil, nil
	}
	lines, err := storeutil.QueryListNamed[struct {
		Id      int    `db:"id"`
		LineKey string `db:"line_key"`
	}](ctx, db, `SELECT id, line_key FROM tech_card_bom_item WHERE tech_card_id = :id ORDER BY display_order, id`,
		map[string]any{"id": styleID})
	if err != nil {
		return nil, nil, fmt.Errorf("load style bom for cut strips: %w", err)
	}
	stripLine := make(map[string]bool, len(strips)+len(topRows))
	for i := range strips {
		stripLine[strips[i].BomLineKey] = true
	}
	for i := range topRows {
		stripLine[topRows[i].BomLineKey] = true
	}
	keyAt := func(index sql.NullInt32) string {
		if index.Valid {
			if idx := int(index.Int32); idx >= 0 && idx < len(lines) {
				return lines[idx].LineKey
			}
		}
		return ""
	}
	keyByID := make(map[int64]string, len(lines))
	for i := range lines {
		keyByID[int64(lines[i].Id)] = lines[i].LineKey
	}
	stored, err := cutStripStoredRows(ctx, db, colorwayID, func(row *cutStripStoredRow) string {
		if row.BomItemID.Valid {
			return keyByID[row.BomItemID.Int64]
		}
		return keyAt(row.BomItemIndex)
	}, stripLine)
	if err != nil {
		return nil, nil, err
	}

	keys := make([]string, len(usages))
	findsStored := make([]bool, len(usages))
	for i := range usages {
		u := &usages[i]
		keys[i] = strings.TrimSpace(u.BomLineKey)
		if keys[i] == "" {
			keys[i] = keyAt(u.BomItemIndex)
		}
		if !stripLine[keys[i]] && u.IsCutLengthNorm() {
			return nil, nil, entity.NewFieldViolation(fmt.Sprintf("usages[%d].consumption_source", i),
				"cut_length_without_strip", "", "this line has no measured strip or topstitch line")
		}
		pieceBound := strings.TrimSpace(u.PieceLineKey) != "" || u.PieceIndex.Valid // says which fabric, not how much
		findsStored[i] = stripLine[keys[i]] && !pieceBound && !u.ArchivedNorm
	}
	matched := make([]*cutStripStoredRow, len(usages))
	for _, samePlacement := range []bool{true, false} {
		for i := range usages {
			if !findsStored[i] || matched[i] != nil {
				continue
			}
			for _, row := range stored[keys[i]] {
				if row.taken || samePlacement && normalizedRecipePlacement(row.Placement) != normalizedRecipePlacement(usages[i].Placement) {
					continue
				}
				row.taken = true
				matched[i] = row
				break
			}
		}
	}

	out := make([]entity.TechCardColorwayUsage, 0, len(usages))
	kept := make(map[int]usageProvenance, len(usages))
	for i := range usages {
		u := usages[i]
		switch row := matched[i]; {
		case row != nil:
			kept[len(out)] = usageProvenance{
				source: row.ConsumptionSource, selvedge: row.WasteSelvedgePct, cut: row.WasteCutPct,
				markerID: row.NormMarkerID, appliedAt: row.NormAppliedAt,
			}
			u.Consumption, u.Quantity, u.SizeConsumptions = row.Consumption, row.Quantity, row.sizes
			// Presence-less, so nothing the client said about provenance is checked: none of it is written.
			u.ConsumptionSource, u.WasteSelvedgePct, u.WasteCutPct = sql.NullString{}, decimal.NullDecimal{}, decimal.NullDecimal{}
			u.NormMarkerId, u.NormMarkerIdSet = sql.NullInt64{}, false
			out = append(out, u)
		case findsStored[i]:
			if entity.ClearCutStripRecipeRow(&u) {
				out = append(out, u)
			}
		default:
			out = append(out, u)
		}
	}
	return out, kept, nil
}
