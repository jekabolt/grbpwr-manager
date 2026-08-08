package techcard

import (
	"context"
	"database/sql"
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
			(tech_card_id, default_seam_class, default_stitches_per_cm, overlock_thread_count,
			 hem_finish, pressing, notes)
		VALUES (:tech_card_id, :default_seam_class, :default_stitches_per_cm, :overlock_thread_count,
			 :hem_finish, :pressing, :notes)`,
		map[string]any{
			"tech_card_id":            tcID,
			"default_seam_class":      c.DefaultSeamClass,
			"default_stitches_per_cm": c.DefaultStitchesPerCm,
			"overlock_thread_count":   c.OverlockThreadCount,
			"hem_finish":              c.HemFinish,
			"pressing":                c.Pressing,
			"notes":                   c.Notes,
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
		rows = append(rows, map[string]any{
			"tech_card_id":     tcID,
			"operation_number": o.OperationNumber,
			"operation_type":   string(o.OperationType),
			"zone":             string(o.Zone),
			"stitches_per_cm":  o.StitchesPerCm,
			"seam_class":       o.SeamClass,
			// Millimetres. NULL here means «inherit the card standard», and 0 means «cut on the line
			// as drawn» — the store writes whichever the payload carried and invents neither.
			"seam_allowance_mm":  o.SeamAllowanceMm,
			"topstitch_mode":     o.TopstitchMode,
			"topstitch_width_mm": o.TopstitchWidthMm,
			"topstitch_rows":     o.TopstitchRows,
			"attachment_kind":    o.AttachmentKind,
			"attachment_size_mm": o.AttachmentSizeMm,
			"smv":                o.SMV,
			"note":               o.Note,
			"callout_number":     o.CalloutNumber,
			"display_order":      i,
		})
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_operation", rows); err != nil {
		return fmt.Errorf("failed to insert tech card operations: %w", err)
	}
	if err := insertTechCardOperationPieces(ctx, db, tcID, ops); err != nil {
		return err
	}
	return insertTechCardOperationBoms(ctx, db, tcID, ops, bomRes)
}

// insertTechCardOperationBoms writes the operation -> BOM-line links (0200): the off-part materials
// an operation consumes. Many-to-many, mirroring the piece links -- one operation can join several
// materials. Operation ids are re-read by display_order for the same reason as there.
func insertTechCardOperationBoms(ctx context.Context, db dependency.DB, tcID int, ops []entity.TechCardOperation, bomRes bomResolver) error {
	wanted := false
	for i := range ops {
		if len(ops[i].BomLineKeys) > 0 {
			wanted = true
			break
		}
	}
	if !wanted {
		return nil
	}
	opRows, err := storeutil.QueryListNamed[struct {
		Id           int `db:"id"`
		DisplayOrder int `db:"display_order"`
	}](ctx, db,
		`SELECT id, display_order FROM tech_card_operation WHERE tech_card_id = :id`,
		map[string]any{"id": tcID})
	if err != nil {
		return fmt.Errorf("load operations for bom links: %w", err)
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
		for j, key := range o.BomLineKeys {
			bomID, err := resolveBomRef(bomRes, key, sql.NullInt32{},
				fmt.Sprintf("operations[%d].bom_line_keys[%d]", i, j))
			if err != nil {
				return err
			}
			if bomID == nil {
				continue
			}
			links = append(links, map[string]any{
				"operation_id":  opID,
				"bom_item_id":   bomID,
				"display_order": j,
			})
		}
	}
	if len(links) == 0 {
		return nil
	}
	if err := storeutil.BulkInsert(ctx, db, "tech_card_operation_bom", links); err != nil {
		return fmt.Errorf("failed to insert operation bom links: %w", err)
	}
	return nil
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

func insertTechCardLabels(ctx context.Context, db dependency.DB, tcID int, labels []entity.TechCardLabel, bomRes bomResolver) error {
	if len(labels) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(labels))
	for i, l := range labels {
		bomItemID := l.BomItemId
		if bomItemID.Valid && bomItemID.Int32 == 0 {
			bomItemID = sql.NullInt32{}
		}
		if bomItemID.Valid && !bomRes.containsID(int(bomItemID.Int32)) {
			return entity.NewFieldViolation(fmt.Sprintf("labels[%d].bom_item_id", i), "not_in_tech_card",
				fmt.Sprintf("BOM line %d", bomItemID.Int32), "select a BOM line from this tech card")
		}
		rows = append(rows, map[string]any{
			"tech_card_id":  tcID,
			"label_type":    string(l.LabelType),
			"content":       l.Content,
			"placement":     l.Placement,
			"attachment":    l.Attachment,
			"size":          l.Size,
			"note":          l.Note,
			"bom_item_id":   bomItemID,
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
	// hardware_cost / packaging_cost are deliberately absent (Phase 2): the columns still exist —
	// they hold the pre-migration values 0237's exception report points at — but the application
	// writes NULL rows only; hardware/packaging money lives in the BOM.
	if err := storeutil.ExecNamed(ctx, db, `
		INSERT INTO tech_card_costing
			(tech_card_id, cmt_cost, logistics_cost, overhead_cost,
			 defect_percent, currency, notes, target_margin_pct)
		VALUES (:tech_card_id, :cmt_cost, :logistics_cost, :overhead_cost,
			 :defect_percent, :currency, :notes, :target_margin_pct)`,
		map[string]any{
			"tech_card_id":      tcID,
			"cmt_cost":          c.CmtCost,
			"logistics_cost":    c.LogisticsCost,
			"overhead_cost":     c.OverheadCost,
			"defect_percent":    c.DefectPercent,
			"currency":          c.Currency,
			"notes":             c.Notes,
			"target_margin_pct": c.TargetMarginPct,
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
			"signed_digest": s.SignedDigest,
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
	// Id is the operation's primary key. It is read purely so the link passes below can join on the
	// real row identity instead of guessing it from display_order; it is deliberately NOT part of
	// entity.TechCardOperation and never reaches the wire (the wire identity of an operation is its
	// operation_number).
	Id int `db:"id"`
	entity.TechCardOperation
}

// operationPos locates one operation inside the per-card slice enrichProduction builds, so a link row
// carrying only operation_id can be attached to the right element.
type operationPos struct {
	cardID int
	index  int
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
	//
	// The LEFT JOIN onto tech_card_bom_item is gone with the singular bom_item_id it resolved: the
	// materials a step consumes are the many-to-many links (0200) read below, and the single column
	// was a second answer that the printed sheet had to subtract from the first.
	opRows, err := storeutil.QueryListNamed[techCardOperationRow](ctx, s.DB, `
		SELECT o.id, o.tech_card_id, o.operation_number, o.operation_type, o.zone,
		       o.stitches_per_cm, o.seam_class, o.seam_allowance_mm,
		       o.topstitch_mode, o.topstitch_width_mm, o.topstitch_rows,
		       o.attachment_kind, o.attachment_size_mm,
		       o.smv, o.note, o.callout_number
		FROM tech_card_operation o
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.operation_number IS NULL, o.operation_number, o.display_order`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operations: %w", err)
	}
	opsByCard := make(map[int][]entity.TechCardOperation, len(ids))
	// posByOpID is the only thing that may be used to attach a link row to an operation. The rows are
	// ordered by operation_number first, so an operation's POSITION in the slice below is not its
	// display_order whenever the card holds legacy rows with a NULL or non-canonical operation_number
	// (those sort last). Keying the link passes on display_order-as-index therefore silently attached
	// pieces/materials to the wrong operation on exactly those cards.
	posByOpID := make(map[int]operationPos, len(opRows))
	for _, r := range opRows {
		op := r.TechCardOperation
		opsByCard[r.TechCardID] = append(opsByCard[r.TechCardID], op)
		posByOpID[r.Id] = operationPos{cardID: r.TechCardID, index: len(opsByCard[r.TechCardID]) - 1}
	}

	// Operation -> cut-piece links (0199). Read as its own pass and joined back by operation_id — the
	// row identity, not a positional proxy for it. line_key travels alongside the id so the client
	// gets the durable reference it writes with, not just the resolved FK.
	pieceLinkRows, err := storeutil.QueryListNamed[struct {
		OpID     int    `db:"op_id"`
		PieceID  int    `db:"piece_id"`
		PieceKey string `db:"line_key"`
	}](ctx, s.DB, `
		SELECT o.id AS op_id, l.piece_id AS piece_id, p.line_key AS line_key
		FROM tech_card_operation_piece l
		JOIN tech_card_operation o ON o.id = l.operation_id
		JOIN tech_card_piece p ON p.id = l.piece_id
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.display_order, l.display_order, l.id`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operation piece links: %w", err)
	}
	for _, l := range pieceLinkRows {
		pos, ok := posByOpID[l.OpID]
		if !ok {
			continue
		}
		list := opsByCard[pos.cardID]
		list[pos.index].PieceIds = append(list[pos.index].PieceIds, l.PieceID)
		list[pos.index].PieceLineKeys = append(list[pos.index].PieceLineKeys, l.PieceKey)
	}

	// Operation -> BOM-line links (0200), same keying as the piece links above.
	bomLinkRows, err := storeutil.QueryListNamed[struct {
		OpID      int    `db:"op_id"`
		BomItemID int    `db:"bom_item_id"`
		BomKey    string `db:"line_key"`
	}](ctx, s.DB, `
		SELECT o.id AS op_id, l.bom_item_id AS bom_item_id, b.line_key AS line_key
		FROM tech_card_operation_bom l
		JOIN tech_card_operation o ON o.id = l.operation_id
		JOIN tech_card_bom_item b ON b.id = l.bom_item_id
		WHERE o.tech_card_id IN (:ids)
		ORDER BY o.tech_card_id, o.display_order, l.display_order, l.id`,
		map[string]any{"ids": ids})
	if err != nil {
		return fmt.Errorf("can't load tech card operation bom links: %w", err)
	}
	for _, l := range bomLinkRows {
		pos, ok := posByOpID[l.OpID]
		if !ok {
			continue
		}
		list := opsByCard[pos.cardID]
		list[pos.index].BomIds = append(list[pos.index].BomIds, l.BomItemID)
		list[pos.index].BomLineKeys = append(list[pos.index].BomLineKeys, l.BomKey)
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
		`SELECT tech_card_id, cmt_cost, logistics_cost, overhead_cost, defect_percent, currency, notes,
		        target_margin_pct
		 FROM tech_card_costing WHERE tech_card_id IN (:ids)`, map[string]any{"ids": ids})
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
		SELECT tech_card_id, section, state, signed_by, signed_at, note, signed_digest
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
