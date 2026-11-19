package techcard

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type pointClosurePattern struct {
	row   patternHistoryRow
	scope string
}

type pointClosureSources struct {
	patterns   map[string][]pointClosurePattern
	blocks     map[string][]entity.PieceAreaBlockRef
	sizeTokens map[int]string
	workByStep map[int32][]string
}

func loadPointClosureSources(ctx context.Context, db dependency.DB, cardID int) (pointClosureSources, error) {
	var source pointClosureSources
	lines, err := loadRollGoodsLines(ctx, db, cardID)
	if err != nil {
		return source, err
	}
	rows, err := techCardPatternRows(ctx, db, cardID)
	if err != nil {
		return source, err
	}
	artifacts := make([]entity.FabricArtifact[patternHistoryRow], 0, len(rows))
	for _, row := range rows {
		purpose, line := row.FabricPurpose.String, row.BomLineKey.String
		if entity.ResolveFabricScope(purpose, line, lines).Live() {
			artifacts = append(artifacts, entity.FabricArtifact[patternHistoryRow]{
				FabricPurpose: purpose, BomLineKey: line, Value: row,
			})
		}
	}
	source.patterns = make(map[string][]pointClosurePattern, len(artifacts))
	for scope, group := range entity.PreferredFabricArtifactsByScope(artifacts, lines) {
		if len(group) != 1 {
			continue // an alias cannot pick a sheet in a multiply bound scope
		}
		for _, row := range group {
			source.patterns[row.LineKey] = append(source.patterns[row.LineKey], pointClosurePattern{row: row, scope: scope})
		}
	}
	source.blocks, err = scopeBlockRefs(ctx, db, cardID, lines)
	if err != nil {
		return source, err
	}
	cardSizes, err := storeutil.QueryScalarListNamed[int](ctx, db,
		`SELECT size_id FROM tech_card_size WHERE tech_card_id = :id`, map[string]any{"id": cardID})
	if err != nil {
		return source, fmt.Errorf("load point closure sizes of tech card %d: %w", cardID, err)
	}
	source.sizeTokens, err = readSavedAssemblyCardSizeTokens(ctx, db, cardSizes)
	if err != nil {
		return source, fmt.Errorf("load point closure size dictionary of tech card %d: %w", cardID, err)
	}
	ops, err := storeutil.QueryListNamed[struct {
		Number int32          `db:"operation_number"`
		Work   sql.NullString `db:"work"`
	}](ctx, db, `SELECT operation_number, work FROM tech_card_operation
		WHERE tech_card_id = :id AND operation_number IS NOT NULL`, map[string]any{"id": cardID})
	if err != nil {
		return source, fmt.Errorf("load point closure operations of tech card %d: %w", cardID, err)
	}
	source.workByStep = make(map[int32][]string, len(ops))
	for _, op := range ops {
		source.workByStep[op.Number] = append(source.workByStep[op.Number], op.Work.String)
	}
	return source, nil
}

type pointClosureOperationPosition struct {
	operation sql.NullInt32
	position  sql.NullInt32
}

// Ordinary full updates may round-trip a point closure link but cannot edit its owner or order.
// The explicit snapshot-restore path owns both the operation links and the restored seam rows.
func guardPointClosureLinksOnDocumentUpdate(ctx context.Context, db dependency.DB, cardID int, operations []entity.TechCardOperation) error {
	rows, err := storeutil.QueryListNamed[struct {
		Key       string        `db:"seam_key"`
		Operation sql.NullInt32 `db:"operation_number"`
		Position  sql.NullInt32 `db:"display_order"`
	}](ctx, db, `
		SELECT s.seam_key, linked.operation_number, linked.display_order
		FROM tech_card_seam s
		LEFT JOIN (
			SELECT o.tech_card_id, o.operation_number, l.seam_key,
			       ROW_NUMBER() OVER (PARTITION BY o.id ORDER BY l.display_order, l.id) - 1 AS display_order
			FROM tech_card_operation o JOIN tech_card_operation_seam l ON l.operation_id = o.id
			WHERE o.tech_card_id = :id
		) linked ON linked.tech_card_id = s.tech_card_id AND linked.seam_key = s.seam_key
		WHERE s.tech_card_id = :id AND s.kind = :kind
		ORDER BY s.id, linked.operation_number, linked.display_order`,
		map[string]any{"id": cardID, "kind": string(entity.TechCardSeamKindPointClosure)})
	if err != nil {
		return fmt.Errorf("load ordered point closure links of tech card %d: %w", cardID, err)
	}
	baseline := make(map[string][]pointClosureOperationPosition, len(rows))
	for _, row := range rows {
		if row.Position.Valid {
			baseline[row.Key] = append(baseline[row.Key], pointClosureOperationPosition{row.Operation, row.Position})
		} else if _, exists := baseline[row.Key]; !exists {
			baseline[row.Key] = nil
		}
	}
	return comparePointClosureDocumentLinks(baseline, operations)
}

func comparePointClosureDocumentLinks(baseline map[string][]pointClosureOperationPosition, operations []entity.TechCardOperation) error {
	if len(baseline) == 0 {
		return nil
	}
	next := make(map[string][]pointClosureOperationPosition, len(baseline))
	for _, operation := range operations {
		for index, rawKey := range operation.SeamKeys {
			key := entity.NormalizeTechCardSeamKey(rawKey)
			if _, native := baseline[key]; native {
				next[key] = append(next[key], pointClosureOperationPosition{operation.OperationNumber,
					sql.NullInt32{Int32: int32(index), Valid: true}})
			}
		}
	}
	for key, before := range baseline {
		if !slices.Equal(before, next[key]) {
			return entity.NewFieldViolation("operations.seam_keys", "point_closure_requires_change", key,
				"preserve the saved point closure link; use ChangeTechCardSeams to edit it")
		}
	}
	return nil
}

func (source pointClosureSources) check(field string, seam entity.TechCardSeamInput, exactRevision bool) error {
	for _, side := range []struct {
		field  string
		anchor entity.TechCardSeamAnchor
	}{{field + ".side_a.parts[0].point_closure", seam.SideA[0]}, {field + ".side_b.parts[0].point_closure", seam.SideB[0]}} {
		point := side.anchor.PointClosure
		rows := source.patterns[point.PatternLineKey]
		if len(rows) != 1 {
			return pointClosureSourceMoved(side.field+".pattern_line_key", "the exact live saved pattern row is unavailable")
		}
		pattern := rows[0]
		if !pattern.row.SHA256.Valid || pattern.row.SHA256.String != point.DXFSHA256 ||
			!pattern.row.SizeBytes.Valid || pattern.row.SizeBytes.Int64 <= 0 || pattern.row.SavedRowRevision == 0 ||
			pattern.row.Version <= 0 || !isDXFPattern(entity.TechCardSizePattern{URL: pattern.row.URL, Filename: pattern.row.Filename}) {
			return pointClosureSourceMoved(side.field+".dxf_sha256", "the exact saved DXF is unavailable")
		}
		if exactRevision && pattern.row.SavedRowRevision != point.SavedRowRevision {
			return pointClosureSourceMoved(side.field+".saved_row_revision", "reload the current saved DXF row")
		}
		// SizeId on the pattern row is the filing slot for the entire graded sheet.
		// Exact DXF replay proves the rank within that sheet. Bind that declared rank to the card's dictionary.
		token := source.sizeTokens[int(point.SizeID)]
		if token == "" || !strings.EqualFold(seam.AnchoredSize, token) {
			return pointClosureSourceMoved(side.field+".size_id", "the declared DXF rank does not match a size on this card")
		}
		alias := strings.ToLower(strings.Join(strings.Fields(point.AliasBlockName), " "))
		matches, exact := 0, false
		for _, block := range source.blocks[pattern.scope] {
			if strings.ToLower(strings.Join(strings.Fields(block.BlockName), " ")) == alias {
				matches++
				exact = block.BlockName == point.AliasBlockName && block.PieceLineKey == side.anchor.PieceLineKey
			}
		}
		if matches != 1 || !exact {
			return pointClosureSourceMoved(side.field+".alias_block_name", "reload the unique saved DXF alias for this piece")
		}
	}
	a := seam.SideA[0].PointClosure
	for _, operation := range []struct {
		number int32
		work   string
		field  string
	}{
		{a.ButtonholeOperationNumber, "buttonhole", "buttonhole_operation_number"},
		{a.ButtonOperationNumber, "button_attach", "button_operation_number"},
	} {
		work := source.workByStep[operation.number]
		if len(work) != 1 || work[0] != operation.work {
			return pointClosureSourceMoved(field+".side_a.parts[0].point_closure."+operation.field,
				"reload the exact numbered "+operation.work+" step")
		}
	}
	return nil
}

func pointClosureSourceMoved(field, detail string) error {
	return fmt.Errorf("%w: %w", entity.ErrTechCardSavedAssemblyMoved,
		entity.NewFieldViolation(field, "point_closure_source_moved", "", detail))
}

// The card row is locked by legacy callers of this helper. A key formerly editable by that RPC
// stays editable; native point decisions use the clock-aware ChangeTechCardSeams RPC.
func refuseLegacyPointClosureKeys(ctx context.Context, db dependency.DB, id int, keys []string) error {
	found, err := storeutil.QueryScalarListNamed[string](ctx, db, `
		SELECT seam_key FROM tech_card_seam
		WHERE tech_card_id = :id AND kind = :kind AND seam_key IN (:keys)`,
		map[string]any{"id": id, "kind": string(entity.TechCardSeamKindPointClosure), "keys": keys})
	if err != nil {
		return err
	}
	if len(found) > 0 {
		return entity.NewFieldViolation("seam_key", "point_closure_requires_change", found[0], "use ChangeTechCardSeams with the saved Assembly clock")
	}
	return nil
}
