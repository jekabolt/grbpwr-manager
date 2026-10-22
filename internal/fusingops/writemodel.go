package fusingops

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// writeModel is the card as the pure reconciler sees it: pieces numbered by position (id = index+1),
// operations numbered by position, every fused piece resolved to its INTERLINING line.
type writeModel struct {
	tc           *entity.TechCardInsert
	sectionByKey map[string]entity.TechCardBomSection // live BOM lines by trimmed line_key
	pieceKeys    map[string]bool
	pieceIDByKey map[string]int
	lineOf       []string // resolved INTERLINING line per piece position; "" = unresolved
	pieces       []Piece
}

func newWriteModel(tc *entity.TechCardInsert) (*writeModel, *entity.ValidationError) {
	m := &writeModel{
		tc:           tc,
		sectionByKey: make(map[string]entity.TechCardBomSection, len(tc.BomItems)),
		pieceKeys:    make(map[string]bool, len(tc.Pieces)),
		pieceIDByKey: make(map[string]int, len(tc.Pieces)),
		lineOf:       make([]string, len(tc.Pieces)),
		pieces:       make([]Piece, 0, len(tc.Pieces)),
	}
	interlinings := make([]string, 0)
	for i := range tc.BomItems {
		key := strings.TrimSpace(tc.BomItems[i].LineKey)
		if key == "" {
			continue
		}
		m.sectionByKey[key] = tc.BomItems[i].Section
		if tc.BomItems[i].Section == entity.BomSectionInterlining {
			interlinings = append(interlinings, key)
		}
	}
	for i := range tc.Pieces {
		piece := &tc.Pieces[i]
		m.pieceKeys[piece.LineKey] = true
		if _, dup := m.pieceIDByKey[piece.LineKey]; !dup {
			m.pieceIDByKey[piece.LineKey] = i + 1
		}
		if piece.Fused {
			selected := strings.TrimSpace(piece.FusingBomLineKey)
			switch {
			case selected != "":
				if m.sectionByKey[selected] != entity.BomSectionInterlining {
					return nil, entity.NewFieldViolation(fmt.Sprintf("pieces[%d].fusing_bom_line_key", i),
						"the selected BOM line is not interlining", selected,
						"select a BOM line from the INTERLINING section")
				}
				m.lineOf[i] = selected
			case len(interlinings) == 1:
				m.lineOf[i] = interlinings[0]
			}
		}
		m.pieces = append(m.pieces, Piece{ID: i + 1, Fused: piece.Fused})
	}
	return m, nil
}

// inputsOf is the operation's classified input list (units and pieces, in order).
func (m *writeModel) inputsOf(op *entity.TechCardOperation) []entity.OperationInput {
	switch {
	case len(op.AssemblyInputs) > 0:
		return op.AssemblyInputs
	case len(op.InputKeys) > 0:
		return entity.ClassifyAssemblyInputs(m.pieceKeys, op.InputKeys)
	}
	inputs := make([]entity.OperationInput, 0, len(op.PieceLineKeys))
	for _, key := range op.PieceLineKeys {
		inputs = append(inputs, entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: key})
	}
	return inputs
}

// liveBomKeys is the operation's BOM links without the ones whose line is gone from this card.
func (m *writeModel) liveBomKeys(op *entity.TechCardOperation) []string {
	live := make([]string, 0, len(op.BomLineKeys))
	for _, raw := range op.BomLineKeys {
		if _, ok := m.sectionByKey[strings.TrimSpace(raw)]; ok {
			live = append(live, raw)
		}
	}
	return live
}

// classify is the reconciler's view of operation i. The store classifies rows the same way.
func (m *writeModel) classify(i int) Operation {
	source := &m.tc.Operations[i]
	op := Operation{ID: i + 1, Draft: source.Draft}
	if source.Review.Valid {
		op.Review = source.Review.String
	}
	for _, input := range m.inputsOf(source) {
		if input.Kind != entity.AssemblyInputPiece {
			op.UnitInputs = true
			continue
		}
		if id, ok := m.pieceIDByKey[input.Key]; ok {
			op.PieceIDs = append(op.PieceIDs, id)
		}
	}
	if source.OperationType != entity.OpTypeFusing {
		op.Kind = KindOther
		return op
	}
	switch {
	case source.AutoFusingPieceKey.Valid:
		op.Kind = KindAuto
		op.Owner = m.pieceIDByKey[source.AutoFusingPieceKey.String]
	case source.AutoFusingLineKey.Valid:
		op.Kind = LegacyKind(op.Review)
	case AdoptableShape(strings.TrimSpace(source.OutputUnitKey.String) != "", op.UnitInputs, m.liveSections(source), op.Review):
		op.Kind = KindAdoptable
	default:
		op.Kind = KindManual
	}
	return op
}

func (m *writeModel) liveSections(op *entity.TechCardOperation) []entity.TechCardBomSection {
	live := m.liveBomKeys(op)
	sections := make([]entity.TechCardBomSection, 0, len(live))
	for _, key := range live {
		sections = append(sections, m.sectionByKey[strings.TrimSpace(key)])
	}
	return sections
}

// LegacyKind classifies a step of the one-per-line model (line key, no piece key). An orphaned one is
// already in its final state, a per-piece orphan with no piece; any other one is still to split.
func LegacyKind(review string) Kind {
	if review == ReviewOrphan {
		return KindAuto
	}
	return KindLegacy
}

// AdoptableShape is the shape of a step without automatic identity that the reconciler may adopt
// as a piece's FUSING step: it assembles nothing, works on pieces only, and links no material or
// exactly one INTERLINING line. A step the reconciler itself orphaned is never adopted again.
func AdoptableShape(hasOutput, unitInputs bool, linkSections []entity.TechCardBomSection, review string) bool {
	if hasOutput || unitInputs || review == ReviewOrphan {
		return false
	}
	switch len(linkSections) {
	case 0:
		return true
	case 1:
		return linkSections[0] == entity.BomSectionInterlining
	}
	return false
}

func (m *writeModel) operations() []Operation {
	ops := make([]Operation, 0, len(m.tc.Operations))
	for i := range m.tc.Operations {
		ops = append(ops, m.classify(i))
	}
	return ops
}

// canonicalBom is an automatic step's material list: its piece's INTERLINING line first, then the
// step's other live non-INTERLINING links in their order. Other INTERLINING links and dead keys go.
func (m *writeModel) canonicalBom(op *entity.TechCardOperation, line string) []string {
	var bom []string
	seen := make(map[string]bool, len(op.BomLineKeys)+1)
	if line != "" {
		bom = append(bom, line)
		seen[line] = true
	}
	for _, raw := range op.BomLineKeys {
		key := strings.TrimSpace(raw)
		section, live := m.sectionByKey[key]
		if !live || section == entity.BomSectionInterlining || seen[key] {
			continue
		}
		seen[key] = true
		bom = append(bom, key)
	}
	return bom
}

func keepQuantities(quantities []entity.OperationBomQty, keys []string) []entity.OperationBomQty {
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[strings.TrimSpace(key)] = true
	}
	var kept []entity.OperationBomQty
	for _, qty := range quantities {
		if allowed[strings.TrimSpace(qty.LineKey)] {
			kept = append(kept, qty)
		}
	}
	return kept
}

// materialize writes planned automatic state onto op: never in place into slices shared with the
// caller's card, and nil rather than empty lists, as the read path returns them.
func (m *writeModel) materialize(op *entity.TechCardOperation, planned Operation) {
	var inputs []entity.OperationInput
	for _, input := range m.inputsOf(op) {
		if input.Kind == entity.AssemblyInputUnit {
			inputs = append(inputs, input)
		}
	}
	op.PieceIds = nil
	op.PieceLineKeys = nil
	if planned.IsOrphan() {
		// The step lost its piece. Its keys stay, so it is never mistaken for a manual step, and only
		// dead material links go — a live one is a person's statement.
		live := m.liveBomKeys(op)
		if len(live) != len(op.BomLineKeys) {
			op.BomLineKeys = nilIfEmpty(live)
			op.BomQuantities = keepQuantities(op.BomQuantities, live)
		}
	} else {
		piece := &m.tc.Pieces[planned.Owner-1]
		line := m.lineOf[planned.Owner-1]
		op.AutoFusingPieceKey = sql.NullString{String: piece.LineKey, Valid: true}
		op.AutoFusingLineKey = sql.NullString{String: line, Valid: true}
		inputs = append(inputs, entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: piece.LineKey})
		op.PieceLineKeys = []string{piece.LineKey}
		bom := m.canonicalBom(op, line)
		op.BomLineKeys = bom
		op.BomQuantities = keepQuantities(op.BomQuantities, bom)
	}
	op.AssemblyInputs = inputs
	op.InputKeys = nil
	for _, input := range inputs {
		op.InputKeys = append(op.InputKeys, input.Key)
	}
	op.Review = sql.NullString{String: planned.Review, Valid: planned.Review != ""}
}

// isMaterialized reports whether op already holds the state materialize would write.
func (m *writeModel) isMaterialized(op *entity.TechCardOperation, planned Operation) bool {
	want := *op
	m.materialize(&want, planned)
	if want.AutoFusingPieceKey != op.AutoFusingPieceKey || want.AutoFusingLineKey != op.AutoFusingLineKey ||
		want.Review != op.Review {
		return false
	}
	return equalStrings(pieceInputKeys(&want, m.pieceKeys), pieceInputKeys(op, m.pieceKeys)) &&
		equalStrings(want.BomLineKeys, op.BomLineKeys) && len(want.BomQuantities) == len(op.BomQuantities)
}

func equalStrings(a, b []string) bool {
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

func nilIfEmpty(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	return keys
}

// newAutoFusingOp is a fresh automatic step: defaults, or the press settings of the step it splits.
func newAutoFusingOp(copyOf *entity.TechCardOperation) entity.TechCardOperation {
	op := entity.TechCardOperation{
		OperationType:  entity.OpTypeFusing,
		Zone:           entity.ZoneInterlining,
		PressEquipment: sql.NullString{String: "fusing_press", Valid: true},
		Draft:          true,
	}
	if copyOf != nil {
		CopyPressSettings(&op, copyOf)
	}
	return op
}

// fusingOpsNeedReconcile reports whether the reconciler would change the payload's FUSING steps,
// leaving aside fused pieces that merely lack a step (see ReconcileTechCardUpdate). A card the
// reconciler rejects needs it too, so the rejection reaches the caller.
func fusingOpsNeedReconcile(tc *entity.TechCardInsert) bool {
	m, verr := newWriteModel(tc)
	if verr != nil {
		return true
	}
	ops := m.operations()
	for i := range tc.Operations {
		if tc.Operations[i].OperationType == entity.OpTypeFusing &&
			len(m.liveBomKeys(&tc.Operations[i])) != len(tc.Operations[i].BomLineKeys) {
			return true
		}
	}
	plan := ReconcileFusingOps(m.pieces, ops)
	if len(plan.DeletedIDs) > 0 {
		return true
	}
	next := 0
	for _, planned := range plan.Operations {
		if planned.ID == 0 {
			if planned.CopyOf > 0 {
				return true
			}
			continue
		}
		if planned.ID != ops[next].ID {
			return true
		}
		before := ops[next]
		next++
		if planned.Kind != before.Kind || planned.Review != before.Review {
			return true
		}
		if planned.Kind == KindAuto && !m.isMaterialized(&tc.Operations[planned.ID-1], planned) {
			return true
		}
	}
	return false
}

// ReconcileTechCard applies the same FUSING transition to the in-memory write model. The API calls
// this before sign-off digests are stamped; the store then repeats the reconciliation against rows
// in the transaction. Keeping both views canonical prevents a generated operation or renumbered
// issue reference from making a fresh sign-off stale as soon as the card is read back.
func ReconcileTechCard(tc *entity.TechCardInsert) *entity.ValidationError {
	if tc == nil {
		return nil
	}
	m, verr := newWriteModel(tc)
	if verr != nil {
		return verr
	}
	plan := ReconcileFusingOps(m.pieces, m.operations())

	oldNumber := func(id int) sql.NullInt32 {
		if id <= 0 {
			return sql.NullInt32{}
		}
		return tc.Operations[id-1].OperationNumber
	}
	deletedOldNumbers := make(map[int32]bool, len(plan.DeletedIDs))
	for _, id := range plan.DeletedIDs {
		if old := oldNumber(id); old.Valid {
			deletedOldNumbers[old.Int32] = true
		}
	}
	reconciled := make([]entity.TechCardOperation, 0, len(plan.Operations))
	newNumberByOld := make(map[int32]int32, len(plan.Operations))
	for i, planned := range plan.Operations {
		var op entity.TechCardOperation
		switch {
		case planned.ID > 0:
			op = tc.Operations[planned.ID-1]
		case planned.CopyOf > 0:
			op = newAutoFusingOp(&tc.Operations[planned.CopyOf-1])
		default:
			op = newAutoFusingOp(nil)
		}
		if op.OperationType == entity.OpTypeFusing {
			// Never send a dead BOM key to the FK resolver: the line went with this save.
			if live := m.liveBomKeys(&op); len(live) != len(op.BomLineKeys) {
				op.BomLineKeys = nilIfEmpty(live)
				op.BomQuantities = keepQuantities(op.BomQuantities, live)
			}
		}
		if planned.Kind == KindAuto {
			m.materialize(&op, planned)
		}
		newNumber := int32((i + 1) * 10)
		if old := oldNumber(planned.ID); old.Valid {
			newNumberByOld[old.Int32] = newNumber
		}
		op.OperationNumber = sql.NullInt32{Int32: newNumber, Valid: true}
		reconciled = append(reconciled, op)
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
