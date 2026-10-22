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
)

type fusingPieceRow struct {
	ID               int            `db:"id"`
	Fused            bool           `db:"fused"`
	FusingBOMItemID  sql.NullInt64  `db:"fusing_bom_item_id"`
	FusingBOMCardID  sql.NullInt64  `db:"fusing_bom_card_id"`
	FusingBOMSection sql.NullString `db:"fusing_bom_section"`
}

type fusingOperationRow struct {
	ID               int            `db:"id"`
	OperationType    string         `db:"operation_type"`
	Draft            bool           `db:"draft"`
	Review           sql.NullString `db:"review"`
	OperationNumber  sql.NullInt64  `db:"operation_number"`
	BOMLinkCount     int            `db:"bom_link_count"`
	InterliningCount int            `db:"interlining_count"`
	InterliningBOMID sql.NullInt64  `db:"interlining_bom_id"`
}

// ReconcileFusingOps makes automatic FUSING operations agree with the current piece and BOM state.
// db is the caller's transaction: this function deliberately never opens or commits a transaction.
func ReconcileFusingOps(ctx context.Context, db dependency.DB, techCardID int) error {
	interlinings, err := storeutil.QueryScalarListNamed[int](ctx, db, `
		SELECT id
		FROM tech_card_bom_item
		WHERE tech_card_id = :tech_card_id AND section = 'interlining'
		ORDER BY display_order, id`, map[string]any{"tech_card_id": techCardID})
	if err != nil {
		return fmt.Errorf("load interlining BOM lines for fusing reconciliation: %w", err)
	}
	soleInterliningID := 0
	if len(interlinings) == 1 {
		soleInterliningID = interlinings[0]
	}

	pieceRows, err := storeutil.QueryListNamed[fusingPieceRow](ctx, db, `
		SELECT p.id, p.fused, p.fusing_bom_item_id,
		       b.tech_card_id AS fusing_bom_card_id, b.section AS fusing_bom_section
		FROM tech_card_piece p
		LEFT JOIN tech_card_bom_item b ON b.id = p.fusing_bom_item_id
		WHERE p.tech_card_id = :tech_card_id
		ORDER BY p.display_order, p.id`, map[string]any{"tech_card_id": techCardID})
	if err != nil {
		return fmt.Errorf("load pieces for fusing reconciliation: %w", err)
	}
	pieces := make([]fusingops.Piece, 0, len(pieceRows))
	for _, row := range pieceRows {
		bomItemID := 0
		if row.Fused {
			switch {
			case row.FusingBOMItemID.Valid:
				if !row.FusingBOMCardID.Valid || int(row.FusingBOMCardID.Int64) != techCardID ||
					!row.FusingBOMSection.Valid || row.FusingBOMSection.String != "interlining" {
					return entity.NewFieldViolation("pieces.fusing_bom_line_key",
						"selected fusing material is not an INTERLINING line of this tech card", "",
						"choose an INTERLINING BOM line from this tech card")
				}
				bomItemID = int(row.FusingBOMItemID.Int64)
			case soleInterliningID != 0:
				bomItemID = soleInterliningID
			}
		}
		pieces = append(pieces, fusingops.Piece{ID: row.ID, Fused: row.Fused, BOMItemID: bomItemID})
	}

	operationRows, err := storeutil.QueryListNamed[fusingOperationRow](ctx, db, `
		SELECT o.id, o.operation_type, o.draft, o.review, o.operation_number,
		       COUNT(l.id) AS bom_link_count,
		       COALESCE(SUM(CASE WHEN b.section = 'interlining' THEN 1 ELSE 0 END), 0) AS interlining_count,
		       MAX(CASE WHEN b.section = 'interlining' THEN b.id ELSE NULL END) AS interlining_bom_id
		FROM tech_card_operation o
		LEFT JOIN tech_card_operation_bom l ON l.operation_id = o.id
		LEFT JOIN tech_card_bom_item b ON b.id = l.bom_item_id
		WHERE o.tech_card_id = :tech_card_id
		GROUP BY o.id, o.operation_type, o.draft, o.review, o.operation_number, o.display_order
		ORDER BY o.display_order, o.id`, map[string]any{"tech_card_id": techCardID})
	if err != nil {
		return fmt.Errorf("load operations for fusing reconciliation: %w", err)
	}
	operations := make([]fusingops.Operation, 0, len(operationRows))
	oldNumberByID := make(map[int]sql.NullInt64, len(operationRows))
	for _, row := range operationRows {
		op := fusingops.Operation{
			ID:     row.ID,
			Fusing: row.OperationType == string(entity.OpTypeFusing),
			Draft:  row.Draft,
		}
		if row.Review.Valid {
			op.Review = row.Review.String
		}
		if op.Fusing {
			switch {
			case row.InterliningCount == 1 && row.InterliningBOMID.Valid:
				op.BOMItemID = int(row.InterliningBOMID.Int64)
			case row.BOMLinkCount == 0:
				op.BOMItemID = 0 // the shared unresolved operation
			default:
				// A malformed/custom FUSING row with no unambiguous interlining must not be
				// mistaken for the unresolved automatic row. Its private negative key can
				// only make it an orphan (or delete it while still draft).
				op.BOMItemID = -row.ID
			}
		}
		operations = append(operations, op)
		oldNumberByID[row.ID] = row.OperationNumber
	}

	plan := fusingops.ReconcileFusingOps(pieces, operations)
	for _, operationID := range plan.DeletedIDs {
		if err := storeutil.ExecNamed(ctx, db,
			`DELETE FROM tech_card_operation WHERE tech_card_id = :tech_card_id AND id = :id AND operation_type = 'fusing' AND draft = 1`,
			map[string]any{"tech_card_id": techCardID, "id": operationID}); err != nil {
			return fmt.Errorf("delete empty draft fusing operation %d: %w", operationID, err)
		}
	}

	for i := range plan.Operations {
		op := &plan.Operations[i]
		if !op.Fusing || op.ID != 0 {
			continue
		}
		opID, err := storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO tech_card_operation
				(tech_card_id, operation_type, zone, press_equipment, draft, display_order)
			VALUES
				(:tech_card_id, 'fusing', 'interlining', 'fusing_press', 1, :display_order)`,
			map[string]any{"tech_card_id": techCardID, "display_order": i})
		if err != nil {
			return fmt.Errorf("create automatic fusing operation: %w", err)
		}
		op.ID = opID
		if op.BOMItemID > 0 {
			if err := storeutil.ExecNamed(ctx, db, `
				INSERT INTO tech_card_operation_bom (operation_id, bom_item_id, display_order)
				VALUES (:operation_id, :bom_item_id, 0)`, map[string]any{
				"operation_id": opID, "bom_item_id": op.BOMItemID,
			}); err != nil {
				return fmt.Errorf("link automatic fusing operation %d to BOM: %w", opID, err)
			}
		}
	}

	for _, op := range plan.Operations {
		if !op.Fusing {
			continue
		}
		var review any
		if op.Review != "" {
			review = op.Review
		}
		if err := storeutil.ExecNamed(ctx, db, `
			UPDATE tech_card_operation SET review = :review
			WHERE tech_card_id = :tech_card_id AND id = :id AND operation_type = 'fusing'`, map[string]any{
			"review": review, "tech_card_id": techCardID, "id": op.ID,
		}); err != nil {
			return fmt.Errorf("update fusing operation %d review: %w", op.ID, err)
		}
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
		for piecePosition, pieceID := range op.PieceIDs {
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

	if err := remapFusingIssueNumbers(ctx, db, techCardID, plan.Operations, oldNumberByID); err != nil {
		return err
	}
	for i, op := range plan.Operations {
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

func remapFusingIssueNumbers(ctx context.Context, db dependency.DB, techCardID int,
	operations []fusingops.Operation, oldNumberByID map[int]sql.NullInt64,
) error {
	params := map[string]any{"tech_card_id": techCardID}
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
