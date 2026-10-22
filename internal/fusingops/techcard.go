package fusingops

import (
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type nullStringState struct {
	Valid bool
	Value string
}

type nullDecimalState struct {
	Valid bool
	Value string
}

type nullBoolState struct {
	Valid bool
	Value bool
}

type edgeState struct {
	Edge           int
	StartMm, EndMm string
}

type zoneState struct {
	DxfPieceKey    string
	Rect           bool
	X0, Y0, X1, Y1 string
}

type pieceFusingState struct {
	Fused      bool
	Mode       nullStringState
	Width      nullDecimalState
	BOMLineKey string
	Edges      []edgeState
	Zone       zoneState
	InsetMm    nullDecimalState
	InsetNet   nullBoolState
	Grain      nullStringState
	Darts      nullStringState
}

func stringState(v sql.NullString) nullStringState {
	return nullStringState{Valid: v.Valid, Value: v.String}
}

func decimalState(valid bool, value fmt.Stringer) nullDecimalState {
	if !valid {
		return nullDecimalState{}
	}
	return nullDecimalState{Valid: true, Value: value.String()}
}

func pieceState(piece entity.TechCardPiece) pieceFusingState {
	state := pieceFusingState{
		Fused:      piece.Fused,
		Mode:       stringState(piece.FusingMode),
		Width:      decimalState(piece.FusingWidthMm.Valid, piece.FusingWidthMm.Decimal),
		BOMLineKey: strings.TrimSpace(piece.FusingBomLineKey),
		InsetMm:    decimalState(piece.FusingInsetMm.Valid, piece.FusingInsetMm.Decimal),
		InsetNet:   nullBoolState{Valid: piece.FusingInsetNet.Valid, Value: piece.FusingInsetNet.Bool},
		Grain:      stringState(piece.FusingGrain),
		Darts:      stringState(piece.FusingDarts),
	}
	for _, edge := range piece.FusingEdges {
		state.Edges = append(state.Edges, edgeState{
			Edge: edge.Edge, StartMm: edge.StartMm.String(), EndMm: edge.EndMm.String(),
		})
	}
	if piece.FusingZone != nil {
		state.Zone.DxfPieceKey = strings.TrimSpace(piece.FusingZone.DxfPieceKey)
		if rect := piece.FusingZone.Rect; rect != nil {
			state.Zone.Rect = true
			state.Zone.X0, state.Zone.Y0 = rect.X0.String(), rect.Y0.String()
			state.Zone.X1, state.Zone.Y1 = rect.X1.String(), rect.Y1.String()
		}
	}
	return state
}

func hasPieceFusingState(state pieceFusingState) bool {
	return state.Fused || state.Mode.Valid || state.Width.Valid || state.BOMLineKey != "" ||
		len(state.Edges) > 0 || state.Zone.Rect || state.Zone.DxfPieceKey != "" ||
		state.InsetMm.Valid || state.InsetNet.Valid || state.Grain.Valid || state.Darts.Valid
}

func cardFusingState(tc *entity.TechCardInsert) (map[string]pieceFusingState, []string) {
	pieces := make(map[string]pieceFusingState)
	interlinings := make([]string, 0)
	if tc == nil {
		return pieces, interlinings
	}
	for i := range tc.Pieces {
		state := pieceState(tc.Pieces[i])
		if hasPieceFusingState(state) {
			pieces[strings.TrimSpace(tc.Pieces[i].LineKey)] = state
		}
	}
	for i := range tc.BomItems {
		if tc.BomItems[i].Section == entity.BomSectionInterlining {
			interlinings = append(interlinings, strings.TrimSpace(tc.BomItems[i].LineKey))
		}
	}
	sort.Strings(interlinings)
	return pieces, interlinings
}

// FusingStateChanged reports whether an update changes anything that owns automatic FUSING
// operations. Full BOM metadata is intentionally not compared: operation grouping depends on the
// membership/identity of INTERLINING lines, while piece geometry/settings are all compared. Presence
// flags are ignored because the API has already carried omitted values from storage before calling.
func FusingStateChanged(stored, incoming *entity.TechCardInsert) bool {
	storedPieces, storedInterlinings := cardFusingState(stored)
	incomingPieces, incomingInterlinings := cardFusingState(incoming)
	return !reflect.DeepEqual(storedPieces, incomingPieces) ||
		!reflect.DeepEqual(storedInterlinings, incomingInterlinings)
}

func carryAutoFusingLineKeys(stored, incoming *entity.TechCardInsert) {
	if stored == nil || incoming == nil {
		return
	}
	byNumber := make(map[int32]sql.NullString, len(stored.Operations))
	for i := range stored.Operations {
		op := &stored.Operations[i]
		if op.OperationType == entity.OpTypeFusing && op.OperationNumber.Valid && op.AutoFusingLineKey.Valid {
			byNumber[op.OperationNumber.Int32] = op.AutoFusingLineKey
		}
	}
	for i := range incoming.Operations {
		op := &incoming.Operations[i]
		if op.OperationType != entity.OpTypeFusing || op.AutoFusingLineKey.Valid || !op.OperationNumber.Valid {
			continue
		}
		if key, ok := byNumber[op.OperationNumber.Int32]; ok {
			op.AutoFusingLineKey = key
		}
	}
}

// ReconcileTechCardUpdate is the update-only trigger. Create and clone paths call ReconcileTechCard
// directly; an unrelated update returns false without touching a single operation or issue.
func ReconcileTechCardUpdate(stored, incoming *entity.TechCardInsert) (bool, *entity.ValidationError) {
	// This private identity is intentionally absent from the wire, so carry it across the operation
	// full-replace even on a no-op save. It changes no instruction or workflow state.
	carryAutoFusingLineKeys(stored, incoming)
	if !FusingStateChanged(stored, incoming) {
		return false, nil
	}
	return true, ReconcileTechCard(incoming)
}

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
			if source.AutoFusingLineKey.Valid {
				autoKey := strings.TrimSpace(source.AutoFusingLineKey.String)
				switch {
				case autoKey == "":
					op.BOMItemID = 0
				case interliningCount == 1 && interlining == autoKey:
					op.BOMItemID = groupFor(autoKey)
				default:
					// A resolved automatic group without its matching live FK is a permanent
					// orphan, even if a different/new BOM row later reuses the old line key.
					op.BOMItemID = -op.ID
				}
			} else {
				switch {
				case interliningCount == 1:
					op.BOMItemID = groupFor(interlining)
					source.AutoFusingLineKey = sql.NullString{String: interlining, Valid: true}
				case len(source.BomLineKeys) == 0 && op.Review != ReviewOrphan:
					op.BOMItemID = 0
					source.AutoFusingLineKey = sql.NullString{Valid: true}
				default:
					op.BOMItemID = -op.ID
					// First reconcile after deletion still sees the stale wire key. Snapshot it
					// before the dead FK-backed link is removed, so review acknowledgement can
					// never turn this operation into the unresolved automatic group.
					if len(source.BomLineKeys) == 1 {
						if key := strings.TrimSpace(source.BomLineKeys[0]); key != "" {
							source.AutoFusingLineKey = sql.NullString{String: key, Valid: true}
						}
					}
				}
			}
		}
		operations = append(operations, op)
		oldNumberByID[op.ID] = source.OperationNumber
	}

	plan := ReconcileFusingOps(pieces, operations)
	reconciled := make([]entity.TechCardOperation, 0, len(plan.Operations))
	newNumberByOld := make(map[int32]int32, len(plan.Operations))
	deletedOldNumbers := make(map[int32]bool, len(plan.DeletedIDs))
	for _, deletedID := range plan.DeletedIDs {
		if old := oldNumberByID[deletedID]; old.Valid {
			deletedOldNumbers[old.Int32] = true
		}
	}
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
				AutoFusingLineKey: sql.NullString{
					String: keyByGroup[planned.BOMItemID], Valid: true,
				},
			}
			if key := keyByGroup[planned.BOMItemID]; key != "" {
				operation.BomLineKeys = []string{key}
			}
		}

		if planned.Fusing {
			if planned.Review == ReviewOrphan && planned.BOMItemID < 0 {
				// The automatic group this edited operation belonged to was deleted. Retain the
				// operation, but never send a dead BOM key to the FK resolver. Live custom links
				// survive; AutoFusingLineKey keeps a now-linkless row distinct from unresolved.
				kept := operation.BomLineKeys[:0]
				for _, rawKey := range operation.BomLineKeys {
					if _, live := sectionByKey[strings.TrimSpace(rawKey)]; live {
						kept = append(kept, rawKey)
					}
				}
				operation.BomLineKeys = kept
				keptQty := operation.BomQuantities[:0]
				for _, qty := range operation.BomQuantities {
					if _, live := sectionByKey[strings.TrimSpace(qty.LineKey)]; live {
						keptQty = append(keptQty, qty)
					}
				}
				operation.BomQuantities = keptQty
			}
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
			if deletedOldNumbers[current.Int32] {
				tc.Issues[i].OperationNumber = sql.NullInt32{}
			} else if next, ok := newNumberByOld[current.Int32]; ok {
				tc.Issues[i].OperationNumber = sql.NullInt32{Int32: next, Valid: true}
			}
		}
	}
	tc.Operations = reconciled
	return nil
}
