package techcard

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fusingops"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

type fusingPieceRow struct {
	ID               int            `db:"id"`
	LineKey          sql.NullString `db:"line_key"`
	Fused            bool           `db:"fused"`
	FusingBOMItemID  sql.NullInt64  `db:"fusing_bom_item_id"`
	FusingBOMCardID  sql.NullInt64  `db:"fusing_bom_card_id"`
	FusingBOMSection sql.NullString `db:"fusing_bom_section"`
}

type fusingInterliningRow struct {
	ID      int            `db:"id"`
	LineKey sql.NullString `db:"line_key"`
}

type fusingOperationRow struct {
	ID                 int            `db:"id"`
	OperationType      string         `db:"operation_type"`
	Draft              bool           `db:"draft"`
	Review             sql.NullString `db:"review"`
	OperationNumber    sql.NullInt64  `db:"operation_number"`
	DisplayOrder       int            `db:"display_order"`
	OutputUnitKey      sql.NullString `db:"output_unit_key"`
	AutoFusingLineKey  sql.NullString `db:"auto_fusing_line_key"`
	AutoFusingPieceKey sql.NullString `db:"auto_fusing_piece_key"`
}

type fusingInputRow struct {
	OperationID int            `db:"operation_id"`
	PieceID     sql.NullInt64  `db:"piece_id"`
	UnitKey     sql.NullString `db:"unit_key"`
}

type fusingBomLinkRow struct {
	OperationID int                 `db:"operation_id"`
	BomItemID   int                 `db:"bom_item_id"`
	Section     string              `db:"section"`
	Qty         decimal.NullDecimal `db:"qty_per_garment"`
}

// carryStoredAutoFusingKeys preserves private automatic identity across the operation full-replace
// for writers that bypass the public API (archive/maintenance commands). The keys are deliberately
// absent from protobuf. A unique same-piece match is authoritative for per-piece rows; a unique
// operation number is the conservative fallback for legacy/direct replacements. Ambiguous addresses
// are rejected, and the operation type guard prevents number reuse by another verb.
func carryStoredAutoFusingKeys(ctx context.Context, db dependency.DB, techCardID int,
	operations []entity.TechCardOperation,
) error {
	rows, err := storeutil.QueryListNamed[struct {
		OperationNumber    sql.NullInt64  `db:"operation_number"`
		AutoFusingLineKey  sql.NullString `db:"auto_fusing_line_key"`
		AutoFusingPieceKey sql.NullString `db:"auto_fusing_piece_key"`
	}](ctx, db, `
		SELECT operation_number, auto_fusing_line_key, auto_fusing_piece_key
		FROM tech_card_operation
		WHERE tech_card_id = :tech_card_id AND operation_type = 'fusing'
		  AND (auto_fusing_line_key IS NOT NULL OR auto_fusing_piece_key IS NOT NULL)`,
		map[string]any{"tech_card_id": techCardID})
	if err != nil {
		return fmt.Errorf("load automatic fusing identities before operation replacement: %w", err)
	}
	type keys struct{ line, piece sql.NullString }
	byNumber := make(map[int64]keys, len(rows))
	duplicateNumber := make(map[int64]bool)
	byPiece := make(map[string]keys, len(rows))
	duplicatePiece := make(map[string]bool)
	for _, row := range rows {
		if row.OperationNumber.Valid {
			if _, exists := byNumber[row.OperationNumber.Int64]; exists {
				duplicateNumber[row.OperationNumber.Int64] = true
			} else {
				byNumber[row.OperationNumber.Int64] = keys{line: row.AutoFusingLineKey, piece: row.AutoFusingPieceKey}
			}
		}
		if row.AutoFusingPieceKey.Valid {
			piece := row.AutoFusingPieceKey.String
			if _, exists := byPiece[piece]; exists {
				duplicatePiece[piece] = true
			} else {
				byPiece[piece] = keys{piece: row.AutoFusingPieceKey}
			}
		}
	}
	for number := range duplicateNumber {
		delete(byNumber, number)
	}
	for piece := range duplicatePiece {
		delete(byPiece, piece)
	}
	// A direct writer may reorder or renumber operations. Preserve a per-piece identity by its one
	// piece before considering the legacy number fallback, but only when both sides are unique.
	candidatesByPiece := make(map[string][]int)
	claimedPiece := make(map[string]bool)
	for i := range operations {
		op := &operations[i]
		if op.OperationType != entity.OpTypeFusing {
			continue
		}
		if op.AutoFusingPieceKey.Valid {
			claimedPiece[op.AutoFusingPieceKey.String] = true
			continue
		}
		if op.AutoFusingLineKey.Valid {
			continue
		}
		if piece, ok := soleOperationPieceKey(op); ok {
			candidatesByPiece[piece] = append(candidatesByPiece[piece], i)
		}
	}
	for piece, candidates := range candidatesByPiece {
		stored, ok := byPiece[piece]
		if !ok || claimedPiece[piece] || len(candidates) != 1 {
			continue
		}
		op := &operations[candidates[0]]
		op.AutoFusingPieceKey = stored.piece
		claimedPiece[piece] = true
	}
	for i := range operations {
		op := &operations[i]
		if op.OperationType != entity.OpTypeFusing || op.AutoFusingLineKey.Valid || op.AutoFusingPieceKey.Valid ||
			!op.OperationNumber.Valid {
			continue
		}
		stored, ok := byNumber[int64(op.OperationNumber.Int32)]
		if !ok {
			continue
		}
		if stored.piece.Valid {
			if claimedPiece[stored.piece.String] {
				continue
			}
			if piece, hasPiece := soleOperationPieceKey(op); hasPiece {
				// A different piece is a different row; several candidates for one piece are ambiguous.
				if piece != stored.piece.String || len(candidatesByPiece[piece]) != 1 || claimedPiece[piece] {
					continue
				}
			}
		}
		op.AutoFusingLineKey, op.AutoFusingPieceKey = stored.line, stored.piece
		if stored.piece.Valid {
			claimedPiece[stored.piece.String] = true
		}
	}
	return nil
}

func soleOperationPieceKey(op *entity.TechCardOperation) (string, bool) {
	keys := op.PieceLineKeys
	if len(op.AssemblyInputs) > 0 {
		keys = nil
		for _, input := range op.AssemblyInputs {
			if input.Kind == entity.AssemblyInputPiece {
				keys = append(keys, input.Key)
			}
		}
	}
	seen := make(map[string]bool, len(keys))
	sole := ""
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if sole != "" {
			return "", false
		}
		sole = key
	}
	return sole, sole != ""
}

// ReconcileFusingOps makes automatic FUSING operations agree with the current piece and BOM state:
// one step per fused piece (fusingops.ReconcileFusingOps), classified exactly as the write model
// classifies them (fusingops.ReconcileTechCard), so a reconciled write model is a no-op here.
// db is the caller's transaction: this function deliberately never opens or commits a transaction.
func ReconcileFusingOps(ctx context.Context, db dependency.DB, techCardID int) error {
	params := map[string]any{"tech_card_id": techCardID}
	interlinings, err := storeutil.QueryListNamed[fusingInterliningRow](ctx, db, `
		SELECT id, line_key
		FROM tech_card_bom_item
		WHERE tech_card_id = :tech_card_id AND section = 'interlining'
		ORDER BY display_order, id`, params)
	if err != nil {
		return fmt.Errorf("load interlining BOM lines for fusing reconciliation: %w", err)
	}
	soleInterliningID := 0
	if len(interlinings) == 1 {
		soleInterliningID = interlinings[0].ID
	}
	pieceRows, err := storeutil.QueryListNamed[fusingPieceRow](ctx, db, `
		SELECT p.id, p.line_key, p.fused, p.fusing_bom_item_id,
		       b.tech_card_id AS fusing_bom_card_id, b.section AS fusing_bom_section
		FROM tech_card_piece p
		LEFT JOIN tech_card_bom_item b ON b.id = p.fusing_bom_item_id
		WHERE p.tech_card_id = :tech_card_id
		ORDER BY p.display_order, p.id`, params)
	if err != nil {
		return fmt.Errorf("load pieces for fusing reconciliation: %w", err)
	}
	pieces := make([]fusingops.Piece, 0, len(pieceRows))
	pieceIDByKey := make(map[string]int, len(pieceRows))
	pieceKeyByID := make(map[int]string, len(pieceRows))
	lineIDOf := make(map[int]int, len(pieceRows)) // resolved INTERLINING line per fused piece; 0 = unresolved
	for _, row := range pieceRows {
		if row.LineKey.Valid {
			if _, dup := pieceIDByKey[row.LineKey.String]; !dup {
				pieceIDByKey[row.LineKey.String] = row.ID
			}
			pieceKeyByID[row.ID] = row.LineKey.String
		}
		if row.Fused {
			switch {
			case row.FusingBOMItemID.Valid:
				if !row.FusingBOMCardID.Valid || int(row.FusingBOMCardID.Int64) != techCardID ||
					!row.FusingBOMSection.Valid || row.FusingBOMSection.String != "interlining" {
					return entity.NewFieldViolation("pieces.fusing_bom_line_key",
						"selected fusing material is not an INTERLINING line of this tech card", "",
						"choose an INTERLINING BOM line from this tech card")
				}
				lineIDOf[row.ID] = int(row.FusingBOMItemID.Int64)
			case soleInterliningID != 0:
				lineIDOf[row.ID] = soleInterliningID
			}
		}
		pieces = append(pieces, fusingops.Piece{ID: row.ID, Fused: row.Fused})
	}

	operationRows, err := storeutil.QueryListNamed[fusingOperationRow](ctx, db, `
		SELECT id, operation_type, draft, review, operation_number, display_order, output_unit_key,
		       auto_fusing_line_key, auto_fusing_piece_key
		FROM tech_card_operation
		WHERE tech_card_id = :tech_card_id
		ORDER BY display_order, id`, params)
	if err != nil {
		return fmt.Errorf("load operations for fusing reconciliation: %w", err)
	}
	inputRows, err := storeutil.QueryListNamed[fusingInputRow](ctx, db, `
		SELECT l.operation_id, l.piece_id, l.unit_key
		FROM tech_card_operation_input l
		JOIN tech_card_operation o ON o.id = l.operation_id
		WHERE o.tech_card_id = :tech_card_id
		ORDER BY l.operation_id, l.display_order, l.id`, params)
	if err != nil {
		return fmt.Errorf("load operation inputs for fusing reconciliation: %w", err)
	}
	linkRows, err := storeutil.QueryListNamed[fusingBomLinkRow](ctx, db, `
		SELECT l.operation_id, l.bom_item_id, b.section, l.qty_per_garment
		FROM tech_card_operation_bom l
		JOIN tech_card_operation o ON o.id = l.operation_id
		JOIN tech_card_bom_item b ON b.id = l.bom_item_id
		WHERE o.tech_card_id = :tech_card_id
		ORDER BY l.operation_id, l.display_order, l.id`, params)
	if err != nil {
		return fmt.Errorf("load operation BOM links for fusing reconciliation: %w", err)
	}
	piecesOf := make(map[int][]int, len(operationRows))
	unitsOf := make(map[int]bool, len(operationRows))
	for _, row := range inputRows {
		switch {
		case row.UnitKey.Valid && row.UnitKey.String != "":
			unitsOf[row.OperationID] = true
		case row.PieceID.Valid:
			piecesOf[row.OperationID] = append(piecesOf[row.OperationID], int(row.PieceID.Int64))
		}
	}
	linksOf := make(map[int][]fusingBomLinkRow, len(operationRows))
	for _, row := range linkRows {
		linksOf[row.OperationID] = append(linksOf[row.OperationID], row)
	}

	operations := make([]fusingops.Operation, 0, len(operationRows))
	rowByID := make(map[int]fusingOperationRow, len(operationRows))
	oldNumberByID := make(map[int]sql.NullInt64, len(operationRows))
	for _, row := range operationRows {
		rowByID[row.ID] = row
		oldNumberByID[row.ID] = row.OperationNumber
		op := fusingops.Operation{
			ID: row.ID, Draft: row.Draft, PieceIDs: piecesOf[row.ID], UnitInputs: unitsOf[row.ID],
		}
		if row.Review.Valid {
			op.Review = row.Review.String
		}
		switch {
		case row.OperationType != string(entity.OpTypeFusing):
			op.Kind = fusingops.KindOther
		case row.AutoFusingPieceKey.Valid:
			op.Kind = fusingops.KindAuto
			op.Owner = pieceIDByKey[row.AutoFusingPieceKey.String]
		case row.AutoFusingLineKey.Valid:
			op.Kind = fusingops.LegacyKind(op.Review)
		default:
			sections := make([]entity.TechCardBomSection, 0, len(linksOf[row.ID]))
			for _, link := range linksOf[row.ID] {
				sections = append(sections, entity.TechCardBomSection(link.Section))
			}
			op.Kind = fusingops.KindManual
			if fusingops.AdoptableShape(strings.TrimSpace(row.OutputUnitKey.String) != "", op.UnitInputs,
				len(op.PieceIDs), sections, op.Review) {
				op.Kind = fusingops.KindAdoptable
			}
		}
		operations = append(operations, op)
	}

	plan := fusingops.ReconcileFusingOps(pieces, operations)
	for _, operationID := range plan.DeletedIDs {
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_operation WHERE tech_card_id = :tech_card_id AND id = :id AND operation_type = 'fusing' AND draft = 1`,
			map[string]any{"tech_card_id": techCardID, "id": operationID}); err != nil {
			return fmt.Errorf("delete draft fusing operation %d: %w", operationID, err)
		}
	}

	copyColumns := strings.Join(fusingops.PressCopyColumns, ", ")
	for i := range plan.Operations {
		op := &plan.Operations[i]
		if op.ID != 0 {
			continue
		}
		insert := `
			INSERT INTO tech_card_operation
				(tech_card_id, operation_type, zone, press_equipment, draft, display_order)
			VALUES
				(:tech_card_id, 'fusing', 'interlining', 'fusing_press', 1, :display_order)`
		if op.CopyOf > 0 {
			// A piece split off a grouped step keeps that step's press settings (same column list
			// as the write model's fusingops.CopyPressSettings).
			insert = `
				INSERT INTO tech_card_operation (tech_card_id, operation_type, draft, display_order, ` + copyColumns + `)
				SELECT tech_card_id, 'fusing', 1, :display_order, ` + copyColumns + `
				FROM tech_card_operation
				WHERE tech_card_id = :tech_card_id AND id = :source_id`
		}
		opID, err := storeutil.ExecNamedLastId(ctx, db, insert, map[string]any{
			"tech_card_id": techCardID, "display_order": i, "source_id": op.CopyOf,
		})
		if err != nil {
			return fmt.Errorf("create automatic fusing operation: %w", err)
		}
		if opID == 0 {
			return fmt.Errorf("create automatic fusing operation: source operation %d not found", op.CopyOf)
		}
		op.ID = opID
		rowByID[opID] = fusingOperationRow{ID: opID, OperationType: string(entity.OpTypeFusing), Draft: true, DisplayOrder: -1}
	}

	for _, op := range plan.Operations {
		if op.Kind != fusingops.KindAuto {
			continue
		}
		if err := applyAutoFusingOp(ctx, db, techCardID, op, rowByID[op.ID], piecesOf[op.ID], linksOf[op.ID],
			pieceKeyByID, lineIDOf); err != nil {
			return err
		}
	}

	if err := remapFusingIssueNumbers(ctx, db, techCardID, plan.Operations, plan.DeletedIDs, oldNumberByID); err != nil {
		return err
	}
	for i, op := range plan.Operations {
		row := rowByID[op.ID]
		if row.DisplayOrder == i && row.OperationNumber.Valid && row.OperationNumber.Int64 == int64((i+1)*10) {
			continue
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE tech_card_operation
			SET display_order = :display_order, operation_number = :operation_number
			WHERE tech_card_id = :tech_card_id AND id = :id`, map[string]any{
			"display_order": i, "operation_number": (i + 1) * 10,
			"tech_card_id": techCardID, "id": op.ID,
		}); err != nil {
			return fmt.Errorf("order reconciled operation %d: %w", op.ID, err)
		}
	}
	return nil
}

// applyAutoFusingOp writes one planned automatic step: identity, review, piece input and material
// links. Rows already in that state are left alone, so a write model the API reconciled is a no-op.
func applyAutoFusingOp(ctx context.Context, db dependency.DB, techCardID int, op fusingops.Operation,
	row fusingOperationRow, currentPieces []int, currentLinks []fusingBomLinkRow,
	pieceKeyByID map[int]string, lineIDOf map[int]int,
) error {
	lineKey, pieceKey := row.AutoFusingLineKey, row.AutoFusingPieceKey
	var wantPieces []int
	if row.AutoFusingPieceKey.Valid || op.Owner > 0 {
		// Rollback safety: an old binary only knows this legacy line key. Keeping it NULL makes a
		// per-piece row look manual to that binary, so it can add a duplicate but cannot regroup or
		// delete the per-piece row. The operation BOM link below remains the material identity.
		lineKey = sql.NullString{}
	}
	if !op.IsOrphan() {
		pieceKey = sql.NullString{String: pieceKeyByID[op.Owner], Valid: true}
		wantPieces = []int{op.Owner}
	}
	review := sql.NullString{String: op.Review, Valid: op.Review != ""}
	if review != row.Review || lineKey != row.AutoFusingLineKey || pieceKey != row.AutoFusingPieceKey {
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE tech_card_operation
			SET review = :review, auto_fusing_line_key = :auto_fusing_line_key, auto_fusing_piece_key = :auto_fusing_piece_key
			WHERE tech_card_id = :tech_card_id AND id = :id AND operation_type = 'fusing'`, map[string]any{
			"review": review, "auto_fusing_line_key": lineKey, "auto_fusing_piece_key": pieceKey,
			"tech_card_id": techCardID, "id": op.ID,
		}); err != nil {
			return fmt.Errorf("update fusing operation %d identity: %w", op.ID, err)
		}
	}

	if !equalInts(currentPieces, wantPieces) {
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_operation_piece WHERE operation_id = :operation_id`,
			map[string]any{"operation_id": op.ID}); err != nil {
			return fmt.Errorf("clear legacy piece links of fusing operation %d: %w", op.ID, err)
		}
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_operation_input WHERE operation_id = :operation_id AND piece_id IS NOT NULL`,
			map[string]any{"operation_id": op.ID}); err != nil {
			return fmt.Errorf("clear piece inputs of fusing operation %d: %w", op.ID, err)
		}
		position, err := storeutil.QueryNamedOne[struct {
			Next int `db:"next_position"`
		}](ctx, db, `
			SELECT COALESCE(MAX(display_order), -1) + 1 AS next_position
			FROM tech_card_operation_input WHERE operation_id = :operation_id`,
			map[string]any{"operation_id": op.ID})
		if err != nil {
			return fmt.Errorf("load next input position of fusing operation %d: %w", op.ID, err)
		}
		for piecePosition, pieceID := range wantPieces {
			params := map[string]any{
				"operation_id": op.ID,
				"piece_id":     pieceID,
				"input_order":  position.Next + piecePosition,
				"legacy_order": piecePosition,
			}
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO tech_card_operation_input (operation_id, piece_id, unit_key, display_order)
				VALUES (:operation_id, :piece_id, NULL, :input_order)`, params); err != nil {
				return fmt.Errorf("link piece %d to fusing operation %d input: %w", pieceID, op.ID, err)
			}
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO tech_card_operation_piece (operation_id, piece_id, display_order)
				VALUES (:operation_id, :piece_id, :legacy_order)`, params); err != nil {
				return fmt.Errorf("link piece %d to legacy fusing operation %d: %w", pieceID, op.ID, err)
			}
		}
	}

	if op.IsOrphan() {
		return nil // only dead links would go, and rows cannot hold one
	}
	// The piece's INTERLINING line first, then the step's other non-INTERLINING links in order —
	// the order fusingops.ReconcileTechCard writes, since the section digest hashes it.
	current := make([]int, 0, len(currentLinks))
	qtyByID := make(map[int]decimal.NullDecimal, len(currentLinks))
	for _, link := range currentLinks {
		current = append(current, link.BomItemID)
		qtyByID[link.BomItemID] = link.Qty
	}
	want := make([]int, 0, len(currentLinks)+1)
	seen := make(map[int]bool, len(currentLinks)+1)
	if lineID := lineIDOf[op.Owner]; lineID != 0 {
		want = append(want, lineID)
		seen[lineID] = true
	}
	for _, link := range currentLinks {
		if link.Section == string(entity.BomSectionInterlining) || seen[link.BomItemID] {
			continue
		}
		seen[link.BomItemID] = true
		want = append(want, link.BomItemID)
	}
	if equalInts(current, want) {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, db,
		`DELETE FROM tech_card_operation_bom WHERE operation_id = :operation_id`,
		map[string]any{"operation_id": op.ID}); err != nil {
		return fmt.Errorf("clear BOM links of fusing operation %d: %w", op.ID, err)
	}
	for position, bomItemID := range want {
		var qty any
		if q := qtyByID[bomItemID]; q.Valid {
			qty = q.Decimal
		}
		if err := storeutil.ExecNamed(ctx, db, `
			INSERT INTO tech_card_operation_bom (operation_id, bom_item_id, display_order, qty_per_garment)
			VALUES (:operation_id, :bom_item_id, :display_order, :qty_per_garment)`, map[string]any{
			"operation_id": op.ID, "bom_item_id": bomItemID, "display_order": position, "qty_per_garment": qty,
		}); err != nil {
			return fmt.Errorf("link fusing operation %d to BOM line %d: %w", op.ID, bomItemID, err)
		}
	}
	return nil
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func remapFusingIssueNumbers(ctx context.Context, db dependency.DB, techCardID int,
	operations []fusingops.Operation, deletedIDs []int, oldNumberByID map[int]sql.NullInt64,
) error {
	params := map[string]any{"tech_card_id": techCardID}
	deletedRefs := make([]string, 0, len(deletedIDs))
	seenDeleted := make(map[int64]bool, len(deletedIDs))
	for _, id := range deletedIDs {
		oldNumber := oldNumberByID[id]
		if !oldNumber.Valid || seenDeleted[oldNumber.Int64] {
			continue
		}
		seenDeleted[oldNumber.Int64] = true
		key := fmt.Sprintf("deleted_%d", len(deletedRefs))
		deletedRefs = append(deletedRefs, ":"+key)
		params[key] = oldNumber.Int64
	}
	if len(deletedRefs) > 0 {
		query := `UPDATE tech_card_issue SET operation_number = NULL
			WHERE tech_card_id = :tech_card_id AND operation_number IN (` + strings.Join(deletedRefs, ", ") + `)`
		if err := storeutil.ExecNamed(ctx, db, query, params); err != nil {
			return fmt.Errorf("clear issue references to deleted draft fusing operations: %w", err)
		}
	}

	clauses := make([]string, 0, len(operations))
	seen := make(map[int64]bool, len(operations))
	for i, op := range operations {
		oldNumber := oldNumberByID[op.ID]
		newNumber := int64((i + 1) * 10)
		if !oldNumber.Valid || oldNumber.Int64 == newNumber || seen[oldNumber.Int64] {
			continue
		}
		seen[oldNumber.Int64] = true
		oldKey := fmt.Sprintf("old_%d", i)
		newKey := fmt.Sprintf("new_%d", i)
		clauses = append(clauses, "WHEN :"+oldKey+" THEN :"+newKey)
		params[oldKey] = oldNumber.Int64
		params[newKey] = newNumber
	}
	if len(clauses) == 0 {
		return nil
	}
	query := `UPDATE tech_card_issue
		SET operation_number = CASE operation_number ` + strings.Join(clauses, " ") + ` ELSE operation_number END
		WHERE tech_card_id = :tech_card_id AND operation_number IS NOT NULL`
	if err := storeutil.ExecNamed(ctx, db, query, params); err != nil {
		return fmt.Errorf("remap issue references after fusing operation reconciliation: %w", err)
	}
	return nil
}
