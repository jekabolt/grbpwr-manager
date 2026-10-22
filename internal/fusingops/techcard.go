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

// fusingOpShape is the identity a FUSING step keeps across a save when the wire carries no id: its
// draft flag, its BOM links and its piece inputs (as sets — order and number are not identity).
type fusingOpShape struct {
	draft  bool
	bom    string
	pieces string
}

func sortedKeySet(keys []string) string {
	set := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, raw := range keys {
		key := strings.TrimSpace(raw)
		if key == "" || set[key] {
			continue
		}
		set[key] = true
		out = append(out, key)
	}
	sort.Strings(out)
	return strings.Join(out, "\x00")
}

func shapeOfFusingOp(op *entity.TechCardOperation) fusingOpShape {
	pieces := op.PieceLineKeys
	if len(op.AssemblyInputs) > 0 {
		pieces = make([]string, 0, len(op.AssemblyInputs))
		for _, input := range op.AssemblyInputs {
			if input.Kind == entity.AssemblyInputPiece {
				pieces = append(pieces, input.Key)
			}
		}
	}
	return fusingOpShape{draft: op.Draft, bom: sortedKeySet(op.BomLineKeys), pieces: sortedKeySet(pieces)}
}

// matchAutoFusingOps pairs every stored automatic FUSING step (auto_fusing_line_key set) with at most
// one incoming FUSING step and copies the private group key onto it. It returns the stored indexes
// that found a counterpart; carryMissingAutoFusingOps treats every other draft automatic step as
// dropped by the payload.
//
// The wire has no operation id, and both the number and the interlining link are shared by steps
// that are not the same step, so identity is the whole shape (fusingOpShape), in this order:
//  0. an incoming step that already carries the key (server-built writers);
//  1. every stored MANUAL step (NULL key) first claims an incoming step of its own shape. Such a
//     step is never adopted: 0428's invariant is that NULL means manual, and a stale payload shifts
//     manual steps onto the numbers the automatic steps hold;
//  2. the exact echo: same draft flag, same BOM links, same pieces, at any number (a client may
//     renumber or reorder);
//  3. a draft step still a draft with the same BOM links, at any number — a client that holds the
//     step but not its latest piece list;
//  4. the same step reviewed in this save: draft cleared, same BOM links, same pieces, at the
//     stored number.
//
// Among several candidates the one at the stored number wins, so an up-to-date echo pairs with
// itself. Anything looser (a group link alone, a number alone) is not identity: a missed draft step
// is carried back as a visible duplicate, while a false match silently deletes it and hands a
// manual step an automatic identity.
func matchAutoFusingOps(stored, incoming *entity.TechCardInsert) map[int]bool {
	matched := make(map[int]bool)
	if stored == nil || incoming == nil {
		return matched
	}
	autoIdx := make([]int, 0)
	manualIdx := make([]int, 0)
	for i := range stored.Operations {
		op := &stored.Operations[i]
		if op.OperationType != entity.OpTypeFusing {
			continue
		}
		if op.AutoFusingLineKey.Valid {
			autoIdx = append(autoIdx, i)
		} else {
			manualIdx = append(manualIdx, i)
		}
	}
	if len(autoIdx) == 0 {
		return matched
	}
	shapes := make([]fusingOpShape, len(incoming.Operations))
	for j := range incoming.Operations {
		shapes[j] = shapeOfFusingOp(&incoming.Operations[j])
	}
	taken := make(map[int]bool, len(incoming.Operations))
	pick := func(number sql.NullInt32, accept func(j int, in *entity.TechCardOperation) bool) int {
		best := -1
		for j := range incoming.Operations {
			in := &incoming.Operations[j]
			if taken[j] || in.OperationType != entity.OpTypeFusing || !accept(j, in) {
				continue
			}
			if number.Valid && in.OperationNumber == number {
				return j
			}
			if best < 0 {
				best = j
			}
		}
		return best
	}
	groupKey := func(op *entity.TechCardOperation) string { return strings.TrimSpace(op.AutoFusingLineKey.String) }
	pass := func(accept func(s *entity.TechCardOperation, sShape fusingOpShape, j int, in *entity.TechCardOperation) bool) {
		for _, i := range autoIdx {
			if matched[i] {
				continue
			}
			s := &stored.Operations[i]
			sShape := shapeOfFusingOp(s)
			j := pick(s.OperationNumber, func(j int, in *entity.TechCardOperation) bool {
				return accept(s, sShape, j, in)
			})
			if j < 0 {
				continue
			}
			taken[j] = true
			matched[i] = true
			if !incoming.Operations[j].AutoFusingLineKey.Valid {
				incoming.Operations[j].AutoFusingLineKey = s.AutoFusingLineKey
			}
		}
	}

	pass(func(s *entity.TechCardOperation, _ fusingOpShape, _ int, in *entity.TechCardOperation) bool {
		return in.AutoFusingLineKey.Valid && groupKey(in) == groupKey(s)
	})
	for _, i := range manualIdx {
		m := &stored.Operations[i]
		mShape := shapeOfFusingOp(m)
		if j := pick(m.OperationNumber, func(j int, in *entity.TechCardOperation) bool {
			return !in.AutoFusingLineKey.Valid && shapes[j] == mShape
		}); j >= 0 {
			taken[j] = true
		}
	}
	pass(func(_ *entity.TechCardOperation, sShape fusingOpShape, j int, in *entity.TechCardOperation) bool {
		return !in.AutoFusingLineKey.Valid && shapes[j] == sShape
	})
	pass(func(s *entity.TechCardOperation, sShape fusingOpShape, j int, in *entity.TechCardOperation) bool {
		return !in.AutoFusingLineKey.Valid && s.Draft && in.Draft && shapes[j].bom == sShape.bom
	})
	pass(func(s *entity.TechCardOperation, sShape fusingOpShape, j int, in *entity.TechCardOperation) bool {
		reviewed := sShape
		reviewed.draft = false
		return !in.AutoFusingLineKey.Valid && s.Draft && s.OperationNumber.Valid &&
			in.OperationNumber == s.OperationNumber && shapes[j] == reviewed
	})
	return matched
}

// carriedAutoFusingOp copies a stored draft automatic step into the write model in wire shape: links
// by durable key only (the store re-resolves ids), and only to pieces and BOM lines this payload still
// holds, so a carried step can never be the reason a save fails to resolve a reference.
func carriedAutoFusingOp(op entity.TechCardOperation, pieceKeys, bomKeys map[string]bool) entity.TechCardOperation {
	op.PieceIds, op.BomIds = nil, nil
	source := op.AssemblyInputs
	if len(source) == 0 {
		source = nil // never append into a backing array shared with the stored card
		for _, key := range op.PieceLineKeys {
			source = append(source, entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: key})
		}
	}
	// nil rather than empty slices, as the read path and the converter produce them: the section
	// digest marshals these lists and must hash a carried step exactly as it hashes the stored one.
	op.AssemblyInputs, op.InputKeys, op.PieceLineKeys = nil, nil, nil
	for _, input := range source {
		if input.Kind == entity.AssemblyInputPiece && !pieceKeys[input.Key] {
			continue
		}
		op.AssemblyInputs = append(op.AssemblyInputs, input)
		op.InputKeys = append(op.InputKeys, input.Key)
		if input.Kind == entity.AssemblyInputPiece {
			op.PieceLineKeys = append(op.PieceLineKeys, input.Key)
		}
	}
	lineKeys := op.BomLineKeys
	op.BomLineKeys = nil
	for _, key := range lineKeys {
		if bomKeys[strings.TrimSpace(key)] {
			op.BomLineKeys = append(op.BomLineKeys, key)
		}
	}
	quantities := op.BomQuantities
	op.BomQuantities = nil
	for _, qty := range quantities {
		if bomKeys[strings.TrimSpace(qty.LineKey)] {
			op.BomQuantities = append(op.BomQuantities, qty)
		}
	}
	op.Media = append([]entity.TechCardOperationMedia(nil), op.Media...)
	return op
}

// carryMissingAutoFusingOps puts back the draft automatic FUSING steps a payload lacks. They are
// server-owned: a client that saved before the server inserted them (and never reloaded its step
// list) would otherwise delete them on its next unrelated save while the pieces stay fused, and the
// assembly order would silently lose Fusing. Only draft steps come back — an edited (non-draft) step
// the client dropped was deleted by a person and stays deleted; legacy steps (no automatic identity)
// are never candidates.
//
// A carried step returns to its stored index, capped at the leading run of FUSING steps so it stays
// at the front of the order. Every step is then renumbered 10, 20, … and the payload's issue
// references follow their step's new number, exactly as ReconcileTechCard renumbers.
func carryMissingAutoFusingOps(stored, incoming *entity.TechCardInsert, matched map[int]bool) bool {
	if stored == nil || incoming == nil {
		return false
	}
	missing := make([]int, 0)
	for i := range stored.Operations {
		op := &stored.Operations[i]
		if op.OperationType == entity.OpTypeFusing && op.Draft && op.AutoFusingLineKey.Valid && !matched[i] {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		return false
	}

	pieceKeys := make(map[string]bool, len(incoming.Pieces))
	for i := range incoming.Pieces {
		pieceKeys[incoming.Pieces[i].LineKey] = true
	}
	bomKeys := make(map[string]bool, len(incoming.BomItems))
	for i := range incoming.BomItems {
		bomKeys[strings.TrimSpace(incoming.BomItems[i].LineKey)] = true
	}

	type slot struct {
		op        entity.TechCardOperation
		oldNumber sql.NullInt32 // payload numbering; invalid for a carried step
	}
	slots := make([]slot, 0, len(incoming.Operations)+len(missing))
	for _, op := range incoming.Operations {
		slots = append(slots, slot{op: op, oldNumber: op.OperationNumber})
	}
	for _, i := range missing {
		position := i
		lead := 0
		for lead < len(slots) && slots[lead].op.OperationType == entity.OpTypeFusing {
			lead++
		}
		if position > lead {
			position = lead
		}
		carried := slot{op: carriedAutoFusingOp(stored.Operations[i], pieceKeys, bomKeys)}
		slots = append(slots[:position], append([]slot{carried}, slots[position:]...)...)
	}

	newNumberByOld := make(map[int32]int32, len(slots))
	operations := make([]entity.TechCardOperation, 0, len(slots))
	for i, s := range slots {
		newNumber := int32((i + 1) * 10)
		if s.oldNumber.Valid {
			if _, seen := newNumberByOld[s.oldNumber.Int32]; !seen {
				newNumberByOld[s.oldNumber.Int32] = newNumber
			}
		}
		s.op.OperationNumber = sql.NullInt32{Int32: newNumber, Valid: true}
		operations = append(operations, s.op)
	}
	for i := range incoming.Issues {
		if current := incoming.Issues[i].OperationNumber; current.Valid {
			if next, ok := newNumberByOld[current.Int32]; ok {
				incoming.Issues[i].OperationNumber = sql.NullInt32{Int32: next, Valid: true}
			}
		}
	}
	incoming.Operations = operations
	return true
}

// UpdateOutcome is what an update's FUSING pass did to the write model.
type UpdateOutcome struct {
	// Reconcile: piece fusing or INTERLINING membership changed. The write model is reconciled and
	// the store must repeat the reconciliation against rows (entity.TechCardInsert.ReconcileFusing).
	Reconcile bool
	// Carried: nothing fusing-relevant changed, but the payload lacked draft automatic FUSING steps
	// and they were put back (operations renumbered, issue references remapped).
	Carried bool
}

// ReconcileTechCardUpdate is the update-only trigger. Create and clone paths call ReconcileTechCard
// directly. An unrelated update never reconciles (legacy/manual FUSING steps are not touched); it only
// restores the draft automatic steps a stale payload dropped. Either way the caller must re-run
// assembly validation when the outcome reports a change, since a step moved to the front can now
// consume a unit produced later.
func ReconcileTechCardUpdate(stored, incoming *entity.TechCardInsert) (UpdateOutcome, *entity.ValidationError) {
	if incoming == nil {
		return UpdateOutcome{}, nil
	}
	// This private identity is intentionally absent from the wire, so carry it across the operation
	// full-replace even on a no-op save. It changes no instruction or workflow state. The store's
	// number-only fallback must not second-guess this identity match.
	matched := matchAutoFusingOps(stored, incoming)
	incoming.AutoFusingKeysResolved = true
	if FusingStateChanged(stored, incoming) {
		// The reconcile recreates every group still used by a fused piece, so automatic steps a
		// stale payload dropped come back here too (as fresh drafts at the front).
		return UpdateOutcome{Reconcile: true}, ReconcileTechCard(incoming)
	}
	return UpdateOutcome{Carried: carryMissingAutoFusingOps(stored, incoming, matched)}, nil
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
