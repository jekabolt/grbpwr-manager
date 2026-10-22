package fusingops

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ReconcileTechCard applies the same FUSING transition to the in-memory write model. The API calls
// this before sign-off digests are stamped; the store then repeats the reconciliation against rows
// in the transaction. Keeping both views canonical prevents a generated operation or renumbered
// issue reference from making a fresh sign-off stale as soon as the card is read back.
func ReconcileTechCard(tc *entity.TechCardInsert) *entity.ValidationError {
	if tc == nil {
		return nil
	}
	interliningKeys := make([]string, 0)
	sectionByKey := make(map[string]entity.TechCardBomSection, len(tc.BomItems))
	for i := range tc.BomItems {
		key := strings.TrimSpace(tc.BomItems[i].LineKey)
		if key == "" {
			continue
		}
		sectionByKey[key] = tc.BomItems[i].Section
		if tc.BomItems[i].Section == entity.BomSectionInterlining {
			interliningKeys = append(interliningKeys, key)
		}
	}

	groupByKey := map[string]int{"": 0}
	keyByGroup := map[int]string{0: ""}
	nextGroup := 1
	groupFor := func(key string) int {
		if group, ok := groupByKey[key]; ok {
			return group
		}
		group := nextGroup
		nextGroup++
		groupByKey[key] = group
		keyByGroup[group] = key
		return group
	}
	for _, key := range interliningKeys {
		groupFor(key)
	}

	pieceKeyByID := make(map[int]string, len(tc.Pieces))
	pieceKeys := make(map[string]bool, len(tc.Pieces))
	pieces := make([]Piece, 0, len(tc.Pieces))
	for i := range tc.Pieces {
		piece := &tc.Pieces[i]
		pieceID := i + 1
		pieceKeyByID[pieceID] = piece.LineKey
		pieceKeys[piece.LineKey] = true
		bomGroup := 0
		if piece.Fused {
			selected := strings.TrimSpace(piece.FusingBomLineKey)
			switch {
			case selected != "":
				if sectionByKey[selected] != entity.BomSectionInterlining {
					return entity.NewFieldViolation(fmt.Sprintf("pieces[%d].fusing_bom_line_key", i),
						"the selected BOM line is not interlining", selected,
						"select a BOM line from the INTERLINING section")
				}
				bomGroup = groupFor(selected)
			case len(interliningKeys) == 1:
				bomGroup = groupFor(interliningKeys[0])
			}
		}
		pieces = append(pieces, Piece{ID: pieceID, Fused: piece.Fused, BOMItemID: bomGroup})
	}

	operations := make([]Operation, 0, len(tc.Operations))
	oldNumberByID := make(map[int]sql.NullInt32, len(tc.Operations))
	for i := range tc.Operations {
		source := &tc.Operations[i]
		op := Operation{ID: i + 1, Fusing: source.OperationType == entity.OpTypeFusing, Draft: source.Draft}
		if source.Review.Valid {
			op.Review = source.Review.String
		}
		if op.Fusing {
			interlining := ""
			interliningCount := 0
			for _, rawKey := range source.BomLineKeys {
				key := strings.TrimSpace(rawKey)
				if sectionByKey[key] == entity.BomSectionInterlining {
					interlining = key
					interliningCount++
				}
			}
			switch {
			case interliningCount == 1:
				op.BOMItemID = groupFor(interlining)
			case len(source.BomLineKeys) == 0:
				op.BOMItemID = 0
			default:
				op.BOMItemID = -op.ID
			}
		}
		operations = append(operations, op)
		oldNumberByID[op.ID] = source.OperationNumber
	}

	plan := ReconcileFusingOps(pieces, operations)
	reconciled := make([]entity.TechCardOperation, 0, len(plan.Operations))
	newNumberByOld := make(map[int32]int32, len(plan.Operations))
	for i, planned := range plan.Operations {
		var operation entity.TechCardOperation
		if planned.ID > 0 {
			operation = tc.Operations[planned.ID-1]
		} else {
			operation = entity.TechCardOperation{
				OperationType: entity.OpTypeFusing,
				Zone:          entity.ZoneInterlining,
				PressEquipment: sql.NullString{
					String: "fusing_press", Valid: true,
				},
				Draft: true,
			}
			if key := keyByGroup[planned.BOMItemID]; key != "" {
				operation.BomLineKeys = []string{key}
			}
		}

		if planned.Fusing {
			lineKeys := make([]string, 0, len(planned.PieceIDs))
			for _, pieceID := range planned.PieceIDs {
				if key := pieceKeyByID[pieceID]; key != "" {
					lineKeys = append(lineKeys, key)
				}
			}
			inputs := operation.AssemblyInputs
			if inputs == nil && len(operation.InputKeys) > 0 {
				inputs = entity.ClassifyAssemblyInputs(pieceKeys, operation.InputKeys)
			}
			union := make([]entity.OperationInput, 0, len(inputs)+len(lineKeys))
			for _, input := range inputs {
				if input.Kind == entity.AssemblyInputUnit {
					union = append(union, input)
				}
			}
			for _, key := range lineKeys {
				union = append(union, entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: key})
			}
			operation.PieceLineKeys = lineKeys
			operation.PieceIds = nil
			operation.AssemblyInputs = union
			operation.InputKeys = make([]string, 0, len(union))
			for _, input := range union {
				operation.InputKeys = append(operation.InputKeys, input.Key)
			}
			operation.Review = sql.NullString{String: planned.Review, Valid: planned.Review != ""}
		}

		newNumber := int32((i + 1) * 10)
		if old := oldNumberByID[planned.ID]; old.Valid {
			newNumberByOld[old.Int32] = newNumber
		}
		operation.OperationNumber = sql.NullInt32{Int32: newNumber, Valid: true}
		reconciled = append(reconciled, operation)
	}
	for i := range tc.Issues {
		if current := tc.Issues[i].OperationNumber; current.Valid {
			if next, ok := newNumberByOld[current.Int32]; ok {
				tc.Issues[i].OperationNumber = sql.NullInt32{Int32: next, Valid: true}
			}
		}
	}
	tc.Operations = reconciled
	return nil
}
