package techcard

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// --- inserts (called within the AddTechCard / UpdateTechCard transaction) ---

func insertTechCardConstruction(ctx context.Context, db dependency.DB, tcID int, c *entity.TechCardConstruction) error {
	if c == nil {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_construction
			(tech_card_id, main_stitch_type, stitch_density, overlock_threads, seam_allowances,
			 hem_finish, pressing, machine_class, notes)
		VALUES (:tech_card_id, :main_stitch_type, :stitch_density, :overlock_threads, :seam_allowances,
			 :hem_finish, :pressing, :machine_class, :notes)`,
		map[string]any{
			"tech_card_id":     tcID,
			"main_stitch_type": c.MainStitchType,
			"stitch_density":   c.StitchDensity,
			"overlock_threads": c.OverlockThreads,
			"seam_allowances":  c.SeamAllowances,
			"hem_finish":       c.HemFinish,
			"pressing":         c.Pressing,
			"machine_class":    c.MachineClass,
			"notes":            c.Notes,
		}); err != nil {
		return fmt.Errorf("failed to insert tech card construction: %w", err)
	}
	return nil
}

func insertTechCardOperations(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation, bomRes bomResolver) error {
	if len(ops) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(ops))
	for i, o := range ops {
		bomID, err := resolveBomRef(bomRes, o.BomLineKey, o.BomItemIndex,
			fmt.Sprintf("operations[%d].bom_line_key", i))
		if err != nil {
			return err
		}
		rows = append(rows, map[string]any{
			"tech_card_id":     tcID,
			"operation_number": o.OperationNumber,
			"node":             o.Node,
			"description":      o.Description,
			"seam_type":        o.SeamType,
			"machine":          o.Machine,
			"stitches_per_cm":  o.StitchesPerCm,
			"topstitch_width":  o.TopstitchWidth,
			"seam_allowance":   o.SeamAllowance,
			"thread":           o.Thread,
			"needle":           o.Needle,
			"attachment":       o.Attachment,
			"time_norm":        o.TimeNorm,
			"note":             o.Note,
			"operation_type":   string(o.OperationType),
			"zone":             string(o.Zone),
			// Resolved above by stable line_key (preferred) or the legacy positional index (S2/S3);
			// *_index kept for the transition.
			"bom_item_id":    bomID,
			"bom_item_index": o.BomItemIndex,
			"callout_number": o.CalloutNumber,
			"placement":      o.Placement,
			"display_order":  i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_operation", rows); err != nil {
		return fmt.Errorf("failed to insert tech card operations: %w", err)
	}
	return insertTechCardOperationPieces(ctx, db, tcID, ops)
}

// insertTechCardOperationPieces writes the operation -> cut-piece links (0199). Many-to-many, unlike
// the recipe's single usage.piece_id: an assembly operation spans as many pieces as it joins.
//
// It re-reads the operation ids rather than threading them out of the bulk insert, because
// BulkInsert returns no ids and inserting one-by-one just to collect LastInsertId would cost a
// round-trip per operation. display_order is unique within a card and is what was just written, so
// it is a reliable join back. Cut-pieces are upserted BEFORE operations in insertTechCardChildren,
// so their line_keys already resolve here.
func insertTechCardOperationPieces(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation) error {
	wanted := false
	for i := range ops {
		if len(ops[i].PieceLineKeys) > 0 {
			wanted = true
			break
		}
	}
	if !wanted {
		return nil
	}

	pieceRows, err := storeutil.QueryListNamed[pieceExistingRow](ctx, db,
		`SELECT id, line_key FROM tech_card_piece WHERE tech_card_id = :id`, map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load cut-pieces for operation links: %w", err)
	}
	pieceByKey := make(map[string]int, len(pieceRows))
	for _, r := range pieceRows {
		pieceByKey[r.LineKey] = r.Id
	}

	opRows, err := storeutil.QueryListNamed[struct {
		Id           int `db:"id"`
		DisplayOrder int `db:"display_order"`
	}](ctx, db,
		`SELECT id, display_order FROM tech_card_operation WHERE tech_card_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load operations for piece links: %w", err)
	}
	opIDByOrder := make(map[int]int, len(opRows))
	for _, r := range opRows {
		opIDByOrder[r.DisplayOrder] = r.Id
	}

	links := make([]map[string]any, 0)
	for i, o := range ops {
		opID, ok := opIDByOrder[i]
		if !ok {
			return fmt.Errorf("operation %d missing after insert", i)
		}
		for j, key := range o.PieceLineKeys {
			pieceID, ok := pieceByKey[key]
			if !ok {
				// Field-tagged rather than a bare error, so the admin client can pin it to the exact
				// operation row instead of failing the whole card with an unattributable message.
				return entity.NewFieldViolation(fmt.Sprintf("operations[%d].piece_line_keys[%d]", i, j),
					fmt.Sprintf("no cut-piece %q in this style", key), "",
					"reference an existing cut-piece by its line_key")
			}
			links = append(links, map[string]any{
				"operation_id":  opID,
				"piece_id":      pieceID,
				"display_order": j,
			})
		}
	}
	if len(links) == 0 {
		return nil
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_piece", links); err != nil {
		return fmt.Errorf("failed to insert operation piece links: %w", err)
	}
	return nil
}

func insertTechCardLabels(ctx context.Context, db dependency.DB, tcID int, labels []entity.TechCardLabel) error {
	if len(labels) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(labels))
	for i, l := range labels {
		rows = append(rows, map[string]any{
			"tech_card_id":  tcID,
			"label_type":    string(l.LabelType),
			"content":       l.Content,
			"placement":     l.Placement,
			"attachment":    l.Attachment,
			"size":          l.Size,
			"note":          l.Note,
			"bom_item_id":   l.BomItemId,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_label", rows); err != nil {
		return fmt.Errorf("failed to insert tech card labels: %w", err)
	}
	return nil
}

func insertTechCardPackaging(ctx context.Context, db dependency.DB, tcID int, p *entity.TechCardPackaging) error {
	if p == nil {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_packaging
			(tech_card_id, folding_method, polybag, bag_sticker, inserts, units_per_box,
			 box_marking, box_dimensions, weight_net_grams, weight_gross_grams, notes)
		VALUES (:tech_card_id, :folding_method, :polybag, :bag_sticker, :inserts, :units_per_box,
			 :box_marking, :box_dimensions, :weight_net_grams, :weight_gross_grams, :notes)`,
		map[string]any{
			"tech_card_id":       tcID,
			"folding_method":     p.FoldingMethod,
			"polybag":            p.Polybag,
			"bag_sticker":        p.BagSticker,
			"inserts":            p.Inserts,
			"units_per_box":      p.UnitsPerBox,
			"box_marking":        p.BoxMarking,
			"box_dimensions":     p.BoxDimensions,
			"weight_net_grams":   p.WeightNetGrams,
			"weight_gross_grams": p.WeightGrossGrams,
			"notes":              p.Notes,
		}); err != nil {
		return fmt.Errorf("failed to insert tech card packaging: %w", err)
	}
	return nil
}

func insertTechCardCosting(ctx context.Context, db dependency.DB, tcID int, c *entity.TechCardCosting) error {
	if c == nil {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_costing
			(tech_card_id, cmt_cost, hardware_cost, packaging_cost, logistics_cost, overhead_cost,
			 defect_percent, currency, notes)
		VALUES (:tech_card_id, :cmt_cost, :hardware_cost, :packaging_cost, :logistics_cost, :overhead_cost,
			 :defect_percent, :currency, :notes)`,
		map[string]any{
			"tech_card_id":   tcID,
			"cmt_cost":       c.CmtCost,
			"hardware_cost":  c.HardwareCost,
			"packaging_cost": c.PackagingCost,
			"logistics_cost": c.LogisticsCost,
			"overhead_cost":  c.OverheadCost,
			"defect_percent": c.DefectPercent,
			"currency":       c.Currency,
			"notes":          c.Notes,
		}); err != nil {
		return fmt.Errorf("failed to insert tech card costing: %w", err)
	}
	return nil
}

func insertTechCardIssues(ctx context.Context, db dependency.DB, tcID int, issues []entity.TechCardIssue) error {
	if len(issues) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(issues))
	for i, is := range issues {
		rows = append(rows, map[string]any{
			"tech_card_id":     tcID,
			"operation_number": is.OperationNumber,
			"callout_number":   is.CalloutNumber,
			"raised_by":        is.RaisedBy,
			"severity":         string(is.Severity),
			"status":           string(is.Status),
			"description":      is.Description,
			"resolution_note":  is.ResolutionNote,
			"display_order":    i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_issue", rows); err != nil {
		return fmt.Errorf("failed to insert tech card issues: %w", err)
	}
	return nil
}

func insertTechCardSignoffs(ctx context.Context, db dependency.DB, tcID int, signoffs []entity.TechCardSignoff) error {
	if len(signoffs) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(signoffs))
	for i, s := range signoffs {
		rows = append(rows, map[string]any{
			"tech_card_id":  tcID,
			"section":       string(s.Section),
			"state":         string(s.State),
			"signed_by":     s.SignedBy,
			"signed_at":     s.SignedAt,
			"note":          s.Note,
			"display_order": i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_signoff", rows); err != nil {
		return fmt.Errorf("failed to insert tech card signoffs: %w", err)
	}
	return nil
}

// --- enrich (load production sections for read paths) ---

type techCardConstructionRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardConstruction
}

type techCardIssueRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardIssue
}

type techCardSignoffRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardSignoff
}

type techCardOperationRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardOperation
}

type techCardLabelRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardLabel
}

type techCardPackagingRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardPackaging
}

type techCardCostingRow struct {
	TechCardID int `db:"tech_card_id"`
	entity.TechCardCosting
}

// enrichProduction loads the construction, operations, labels, packaging and
// costing sections for each card and attaches them.
func (s *Store) enrichProduction(ctx context.Context, cards []entity.TechCard) error {
	if len(cards) == 0 {
		return nil
	}
	ids := make([]int, 0, len(cards))
	for i := range cards {
		ids = append(ids, cards[i].Id)
	}

	consRows, err := storeutil.QueryListNamed[techCardConstructionRow](ctx, s.DB,
		`SELECT * FROM tech_card_construction WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card construction: %w", err)
	}
	consByCard := make(map[int]*entity.TechCardConstruction, len(consRows))
	for i := range consRows {
		c := consRows[i].TechCardConstruction
		consByCard[consRows[i].TechCardID] = &c
	}

	// Operations are returned sorted ascending by operation_number (the addressable
	// «оп. 10, 20, …»); unnumbered operations sort last, with display_order as a
	// stable tiebreaker within each group.
	opRows, err := storeutil.QueryListNamed[techCardOperationRow](ctx, s.DB, `
		SELECT tech_card_id, operation_number, node, description, seam_type, machine, stitches_per_cm,
		       topstitch_width, seam_allowance, thread, needle, attachment, time_norm, note,
		       operation_type, zone, bom_item_id, bom_item_index, callout_number, placement
		FROM tech_card_operation
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, operation_number IS NULL, operation_number, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operations: %w", err)
	}
	opsByCard := make(map[int][]entity.TechCardOperation, len(ids))
	for _, r := range opRows {
		opsByCard[r.TechCardID] = append(opsByCard[r.TechCardID], r.TechCardOperation)
	}

	// Operation -> cut-piece links (0199). Read as its own pass, keyed back by (tech_card_id,
	// display_order) — the same identity the write used — because the operation rows above carry no
	// id. line_key travels alongside the id so the client gets the durable reference it writes with,
	// not just the resolved FK.
	pieceLinkRows, err := storeutil.QueryListNamed[struct {
		TechCardID  int    `db:"tech_card_id"`
		OpOrder     int    `db:"op_order"`
		PieceID     int    `db:"piece_id"`
		PieceKey    string `db:"line_key"`
	}](ctx, s.DB, `
		SELECT o.tech_card_id AS tech_card_id, o.display_order AS op_order,
		       l.piece_id AS piece_id, p.line_key AS line_key
		FROM tech_card_operation_piece l
		JOIN tech_card_operation o ON o.id = l.operation_id
		JOIN tech_card_piece p ON p.id = l.piece_id
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.display_order, l.display_order, l.id`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operation piece links: %w", err)
	}
	if len(pieceLinkRows) > 0 {
		// The ORDER BY above matches the ORDER BY that built opsByCard, so position within a card is
		// the same display_order the links key on.
		orderIndex := make(map[int]map[int]int, len(ids))
		for cardID, list := range opsByCard {
			m := make(map[int]int, len(list))
			for i := range list {
				m[i] = i
			}
			orderIndex[cardID] = m
		}
		for _, l := range pieceLinkRows {
			list := opsByCard[l.TechCardID]
			idx, ok := orderIndex[l.TechCardID][l.OpOrder]
			if !ok || idx >= len(list) {
				continue
			}
			list[idx].PieceIds = append(list[idx].PieceIds, l.PieceID)
			list[idx].PieceLineKeys = append(list[idx].PieceLineKeys, l.PieceKey)
		}
	}

	labelRows, err := storeutil.QueryListNamed[techCardLabelRow](ctx, s.DB, `
		SELECT tech_card_id, label_type, content, placement, attachment, size, note, bom_item_id
		FROM tech_card_label
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card labels: %w", err)
	}
	labelsByCard := make(map[int][]entity.TechCardLabel, len(ids))
	for _, r := range labelRows {
		labelsByCard[r.TechCardID] = append(labelsByCard[r.TechCardID], r.TechCardLabel)
	}

	// Explicit column list (not SELECT *): the deprecated kg columns weight_net/weight_gross may
	// still exist (dropped by 0129) but are no longer mapped, and a strict StructScan rejects
	// unmapped columns.
	pkgRows, err := storeutil.QueryListNamed[techCardPackagingRow](ctx, s.DB,
		`SELECT tech_card_id, folding_method, polybag, bag_sticker, inserts, units_per_box,
		        box_marking, box_dimensions, weight_net_grams, weight_gross_grams, notes
		 FROM tech_card_packaging WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card packaging: %w", err)
	}
	pkgByCard := make(map[int]*entity.TechCardPackaging, len(pkgRows))
	for i := range pkgRows {
		p := pkgRows[i].TechCardPackaging
		pkgByCard[pkgRows[i].TechCardID] = &p
	}

	costRows, err := storeutil.QueryListNamed[techCardCostingRow](ctx, s.DB,
		`SELECT * FROM tech_card_costing WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card costing: %w", err)
	}
	costByCard := make(map[int]*entity.TechCardCosting, len(costRows))
	for i := range costRows {
		c := costRows[i].TechCardCosting
		costByCard[costRows[i].TechCardID] = &c
	}

	issueRows, err := storeutil.QueryListNamed[techCardIssueRow](ctx, s.DB, `
		SELECT tech_card_id, operation_number, callout_number, raised_by, severity, status, description, resolution_note
		FROM tech_card_issue
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card issues: %w", err)
	}
	issuesByCard := make(map[int][]entity.TechCardIssue, len(ids))
	for _, r := range issueRows {
		issuesByCard[r.TechCardID] = append(issuesByCard[r.TechCardID], r.TechCardIssue)
	}

	signoffRows, err := storeutil.QueryListNamed[techCardSignoffRow](ctx, s.DB, `
		SELECT tech_card_id, section, state, signed_by, signed_at, note
		FROM tech_card_signoff
		WHERE tech_card_id IN (:ids)
		ORDER BY tech_card_id, display_order`, map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card signoffs: %w", err)
	}
	signoffsByCard := make(map[int][]entity.TechCardSignoff, len(ids))
	for _, r := range signoffRows {
		signoffsByCard[r.TechCardID] = append(signoffsByCard[r.TechCardID], r.TechCardSignoff)
	}

	for i := range cards {
		id := cards[i].Id
		cards[i].Construction = consByCard[id]
		cards[i].Operations = opsByCard[id]
		cards[i].Labels = labelsByCard[id]
		cards[i].Packaging = pkgByCard[id]
		cards[i].Costing = costByCard[id]
		cards[i].Issues = issuesByCard[id]
		cards[i].Signoffs = signoffsByCard[id]
	}
	return nil
}
