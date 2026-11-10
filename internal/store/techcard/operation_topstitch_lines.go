package techcard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type operationTopstitchRow struct {
	Id                int            `db:"id"`
	OperationId       int            `db:"operation_id"`
	TechCardId        int            `db:"tech_card_id"`
	BomItemId         int            `db:"bom_item_id"`
	BomLineKey        string         `db:"bom_line_key"`
	LineKey           string         `db:"line_key"`
	Anchors           []byte         `db:"anchors"`
	AnchoredSize      string         `db:"anchored_size"`
	Rows              int            `db:"stitch_rows"`
	SourceFingerprint sql.NullString `db:"source_fingerprint"`
	DisplayOrder      int            `db:"display_order"`
}

func operationTopstitchRows(ctx context.Context, db dependency.DB, cardIDs []int) ([]operationTopstitchRow, error) {
	if len(cardIDs) == 0 {
		return nil, nil
	}
	return storeutil.QueryListNamed[operationTopstitchRow](ctx, db, `
		SELECT l.id, l.operation_id, l.tech_card_id, l.bom_item_id, b.line_key AS bom_line_key,
		       l.line_key, l.anchors, l.anchored_size, l.stitch_rows, l.source_fingerprint, l.display_order
		FROM tech_card_operation_topstitch_line l
		JOIN tech_card_operation o ON o.id = l.operation_id
		JOIN tech_card_bom_item b ON b.id = l.bom_item_id
		WHERE l.tech_card_id IN (:ids)
		ORDER BY l.tech_card_id, o.display_order, l.display_order, l.id`, map[string]any{"ids": cardIDs})
}

func loadOperationTopstitchLines(ctx context.Context, db dependency.DB, cardIDs []int) (map[int][]entity.TechCardTopstitchLine, error) {
	rows, err := operationTopstitchRows(ctx, db, cardIDs)
	if err != nil {
		return nil, fmt.Errorf("load operation topstitch lines: %w", err)
	}
	out := make(map[int][]entity.TechCardTopstitchLine)
	if len(rows) == 0 {
		return out, nil
	}
	lineIDs := make([]int, 0, len(rows))
	type linePos struct{ opID, index int }
	byID := make(map[int]linePos, len(rows))
	decodeBad := make(map[int]bool)
	for _, row := range rows {
		line := entity.TechCardTopstitchLine{
			Id: row.Id, OperationId: row.OperationId, TechCardId: row.TechCardId,
			BomItemId: row.BomItemId, BomLineKey: row.BomLineKey, LineKey: row.LineKey,
			AnchoredSize: row.AnchoredSize, Rows: row.Rows, SourceFingerprint: row.SourceFingerprint,
			DisplayOrder: row.DisplayOrder,
		}
		if err := json.Unmarshal(row.Anchors, &line.Parts); err != nil {
			decodeBad[row.Id] = true
		}
		out[row.OperationId] = append(out[row.OperationId], line)
		byID[row.Id] = linePos{opID: row.OperationId, index: len(out[row.OperationId]) - 1}
		lineIDs = append(lineIDs, row.Id)
	}
	sizes, err := storeutil.QueryListNamed[entity.TechCardTopstitchLineSize](ctx, db, `
		SELECT line_id, size_id, line_mm
		FROM tech_card_operation_topstitch_line_size
		WHERE line_id IN (:ids)
		ORDER BY line_id, size_id`, map[string]any{"ids": lineIDs})
	if err != nil {
		return nil, fmt.Errorf("load operation topstitch line sizes: %w", err)
	}
	for _, size := range sizes {
		if pos, ok := byID[size.LineId]; ok {
			line := &out[pos.opID][pos.index]
			line.Sizes = append(line.Sizes, size)
		}
	}
	sources := make(map[int]*seamSources)
	for _, row := range rows {
		if sources[row.TechCardId] == nil {
			src, err := loadSeamSources(ctx, db, row.TechCardId)
			if err != nil {
				return nil, err
			}
			sources[row.TechCardId] = src
		}
		pos := byID[row.Id]
		line := &out[pos.opID][pos.index]
		line.Stale = decodeBad[row.Id] || !line.SourceFingerprint.Valid ||
			sources[row.TechCardId].fingerprint(line.PieceLineKeys()) != line.SourceFingerprint.String
	}
	return out, nil
}

func captureStoredTopstitchLines(ctx context.Context, db dependency.DB, cardID int, tc *entity.TechCardInsert) error {
	rows, err := operationTopstitchRows(ctx, db, []int{cardID})
	if err != nil {
		return err
	}
	tc.StoredTopstitchLines = make(map[string]entity.TechCardTopstitchLine, len(rows))
	for _, row := range rows {
		line := entity.TechCardTopstitchLine{
			Id: row.Id, OperationId: row.OperationId, TechCardId: row.TechCardId,
			BomItemId: row.BomItemId, BomLineKey: row.BomLineKey, LineKey: row.LineKey,
			AnchoredSize: row.AnchoredSize, Rows: row.Rows, SourceFingerprint: row.SourceFingerprint,
			DisplayOrder: row.DisplayOrder,
		}
		if err := json.Unmarshal(row.Anchors, &line.Parts); err != nil {
			continue
		}
		tc.StoredTopstitchLines[entity.CutStripLineKey(line.LineKey)] = line
	}
	return nil
}

func prepareTopstitchLineKeys(tc *entity.TechCardInsert) error {
	if tc == nil {
		return nil
	}
	taken := make(map[string]string)
	for i := range tc.Pieces {
		taken[entity.CutStripLineKey(tc.Pieces[i].LineKey)] = fmt.Sprintf("pieces[%d].line_key", i)
	}
	for i := range tc.CutStrips {
		taken[entity.CutStripLineKey(tc.CutStrips[i].LineKey)] = fmt.Sprintf("cut_strips[%d].line_key", i)
	}
	for i := range tc.GarmentLabels {
		taken[entity.CutStripLineKey(tc.GarmentLabels[i].LineKey)] = fmt.Sprintf("garment_labels[%d].line_key", i)
	}
	for i := range tc.Operations {
		for j := range tc.Operations[i].TopstitchLines {
			line := &tc.Operations[i].TopstitchLines[j]
			field := fmt.Sprintf("operations[%d].topstitch_lines[%d].line_key", i, j)
			key := entity.CutStripLineKey(line.LineKey)
			if key == "" {
				key = newLineKey()
			}
			if owner, exists := taken[key]; exists {
				return entity.NewFieldViolation(field, "duplicate", owner, "each card line has its own key")
			}
			taken[key] = field
			line.LineKey = key
		}
	}
	return entity.ValidateTechCardTopstitchLines(tc)
}

func insertTechCardOperationTopstitchLines(ctx context.Context, db dependency.DB, tcID int,
	tc *entity.TechCardInsert, bomRes bomResolver,
) error {
	if len(entity.TopstitchLines(tc.Operations)) == 0 {
		return nil
	}
	if err := refuseTopstitchLinesOnWrongBom(ctx, db, tcID, tc.Operations); err != nil {
		return err
	}
	opRows, err := storeutil.QueryListNamed[struct {
		Id           int `db:"id"`
		DisplayOrder int `db:"display_order"`
	}](ctx, db, `SELECT id, display_order FROM tech_card_operation WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load operations for topstitch lines: %w", err)
	}
	opIDByOrder := make(map[int]int, len(opRows))
	for _, row := range opRows {
		opIDByOrder[row.DisplayOrder] = row.Id
	}
	src, err := loadSeamSources(ctx, db, tcID)
	if err != nil {
		return err
	}
	for i := range tc.Operations {
		opID, ok := opIDByOrder[i]
		if !ok {
			return fmt.Errorf("operation %d missing after insert", i)
		}
		for j := range tc.Operations[i].TopstitchLines {
			line := &tc.Operations[i].TopstitchLines[j]
			bomID, ok := bomRes.byLineKey[line.BomLineKey]
			if !ok {
				return entity.NewFieldViolation(fmt.Sprintf("operations[%d].topstitch_lines[%d].bom_line_key", i, j),
					"no_such_bom_line", line.BomLineKey, "a BOM line of this card")
			}
			anchors, err := json.Marshal(line.Parts)
			if err != nil {
				return fmt.Errorf("encode topstitch line %s anchors: %w", line.LineKey, err)
			}
			fingerprint := sql.NullString{}
			old, carried := tc.StoredTopstitchLines[entity.CutStripLineKey(line.LineKey)]
			if line.RestoreSourceFingerprint && line.RestoreStale {
				fingerprint = sql.NullString{String: seamFingerprintStale, Valid: true}
			} else if line.RestoreSourceFingerprint {
				fingerprint = sql.NullString{String: src.fingerprint(line.PieceLineKeys()), Valid: true}
			} else if carried && !line.Measured && old.AnchoredSize == line.AnchoredSize && reflect.DeepEqual(old.Parts, line.Parts) {
				fingerprint = old.SourceFingerprint
			} else {
				fingerprint = sql.NullString{String: src.fingerprint(line.PieceLineKeys()), Valid: true}
			}
			lineID, err := storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO tech_card_operation_topstitch_line
					(operation_id, tech_card_id, line_key, bom_item_id, anchors, anchored_size, stitch_rows,
					 source_fingerprint, display_order)
				VALUES
					(:operation_id, :tech_card_id, :line_key, :bom_item_id, :anchors, :anchored_size, :stitch_rows,
					 :source_fingerprint, :display_order)`, map[string]any{
				"operation_id": opID, "tech_card_id": tcID, "line_key": line.LineKey, "bom_item_id": bomID,
				"anchors": string(anchors), "anchored_size": line.AnchoredSize, "stitch_rows": line.Rows,
				"source_fingerprint": fingerprint, "display_order": j,
			})
			if err != nil {
				return fmt.Errorf("insert topstitch line %s: %w", line.LineKey, err)
			}
			rows := make([]map[string]any, 0, len(line.Sizes))
			for _, size := range line.Sizes {
				rows = append(rows, map[string]any{"line_id": lineID, "size_id": size.SizeId, "line_mm": size.LineMm})
			}
			if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_topstitch_line_size", rows); err != nil {
				return fmt.Errorf("insert sizes of topstitch line %s: %w", line.LineKey, err)
			}
		}
	}
	return nil
}

func refuseTopstitchLinesOnWrongBom(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation) error {
	rows, err := storeutil.QueryListNamed[struct {
		LineKey     string `db:"line_key"`
		Section     string `db:"section"`
		SlotUnit    string `db:"slot_unit"`
		ArticleUnit string `db:"article_unit"`
	}](ctx, db, `
		SELECT bi.line_key, COALESCE(bi.section, '') AS section,
		       COALESCE(bi.unit, '') AS slot_unit, COALESCE(m.unit, '') AS article_unit
		FROM tech_card_bom_item bi
		LEFT JOIN material m ON m.id = bi.material_id
		WHERE bi.tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load BOM lines for topstitch lines of card %d: %w", tcID, err)
	}
	type facts struct{ section, unit string }
	byKey := make(map[string]facts, len(rows))
	for _, row := range rows {
		section := entity.TechCardBomSection(strings.ToLower(strings.TrimSpace(row.Section)))
		unit := entity.ResolveBomLineUnit(section, row.SlotUnit, row.ArticleUnit)
		byKey[entity.CutStripLineKey(row.LineKey)] = facts{section: row.Section, unit: unit}
	}
	for i := range ops {
		for j := range ops[i].TopstitchLines {
			line := &ops[i].TopstitchLines[j]
			fact, ok := byKey[entity.CutStripLineKey(line.BomLineKey)]
			if !ok {
				continue
			}
			field := fmt.Sprintf("operations[%d].topstitch_lines[%d].bom_line_key", i, j)
			if entity.TechCardBomSection(strings.ToLower(strings.TrimSpace(fact.section))) != entity.BomSectionThread {
				return entity.NewFieldViolation(field, "not_thread", line.BomLineKey, "a THREAD line")
			}
			if !entity.IsCutStripUnit(fact.unit) {
				return entity.NewFieldViolation(field, "not_metres", fact.unit, "a thread line bought in metres")
			}
		}
	}
	return nil
}
