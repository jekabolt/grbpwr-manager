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

// pieceInputKeys is the operation's piece inputs in order, from whichever input list it carries.
func pieceInputKeys(op *entity.TechCardOperation, pieceKeys map[string]bool) []string {
	switch {
	case len(op.AssemblyInputs) > 0:
		keys := make([]string, 0, len(op.AssemblyInputs))
		for _, input := range op.AssemblyInputs {
			if input.Kind == entity.AssemblyInputPiece {
				keys = append(keys, input.Key)
			}
		}
		return keys
	case len(op.InputKeys) > 0 && pieceKeys != nil:
		keys := make([]string, 0, len(op.InputKeys))
		for _, input := range entity.ClassifyAssemblyInputs(pieceKeys, op.InputKeys) {
			if input.Kind == entity.AssemblyInputPiece {
				keys = append(keys, input.Key)
			}
		}
		return keys
	default:
		return op.PieceLineKeys
	}
}

func shapeOfFusingOp(op *entity.TechCardOperation) fusingOpShape {
	return fusingOpShape{draft: op.Draft, bom: sortedKeySet(op.BomLineKeys), pieces: sortedKeySet(pieceInputKeys(op, nil))}
}

// sameFusingOpContent compares the complete wire-visible operation while ignoring fields that are
// either its position in the list or resolved/private store metadata. This is the strongest identity
// available on the wire: operation_number changes when a client reorders steps, and no operation id
// is exposed by the admin contract.
func sameFusingOpContent(a, b *entity.TechCardOperation) bool {
	left, right := *a, *b
	for _, op := range []*entity.TechCardOperation{&left, &right} {
		op.OperationNumber = sql.NullInt32{}
		op.AutoFusingLineKey = sql.NullString{}
		op.AutoFusingPieceKey = sql.NullString{}
		op.PieceIds = nil
		op.BomIds = nil
		if len(op.Media) > 0 {
			op.Media = append([]entity.TechCardOperationMedia(nil), op.Media...)
			for i := range op.Media {
				op.Media[i].Id = 0
				op.Media[i].TechCardOperationId = 0
				op.Media[i].AnnotationsRaw = nil
			}
		}
	}
	return reflect.DeepEqual(left, right)
}

// autoIdentity is the private identity of an automatic FUSING step: the piece it fuses, or for a
// grouped legacy step the interlining line it was created for. ok is false for a manual step.
func autoIdentity(op *entity.TechCardOperation) (string, bool) {
	if op.AutoFusingPieceKey.Valid {
		return "piece\x00" + op.AutoFusingPieceKey.String, true
	}
	if op.AutoFusingLineKey.Valid {
		return "line\x00" + strings.TrimSpace(op.AutoFusingLineKey.String), true
	}
	return "", false
}

type autoFusingMatch struct {
	matched    map[int]bool
	unresolved bool // at least one retained identity had several equally valid counterparts
}

// matchAutoFusingOps pairs every stored automatic FUSING step (a piece or line key set) with at most
// one incoming FUSING step and copies the private keys onto it. carryMissingAutoFusingOps treats every
// unmatched draft automatic step as dropped by the payload. An ambiguous content match is rejected:
// no candidate receives the private key and unresolved tells the caller to leave the store fallback
// enabled for the full replace.
//
// The wire has no operation id, and neither the number nor the interlining link is identity, so
// identity is operation content, in this order:
//  0. an incoming step that already carries the same key (server-built writers);
//  1. globally pair exact full-content echoes, at any number, for both automatic and manual stored
//     steps. A manual echo is only reserved and is never keyed. If the same content could belong to
//     both kinds, no key is assigned and the store fallback stays enabled;
//  2. a grouped legacy echo with the same draft flag, BOM links and pieces;
//  3. a unique step on the same single piece (per-piece step), regardless of draft state or other
//     edits — changing its BOM link, note, SMV or press settings does not discard its identity;
//  4. a grouped legacy draft still a draft with the same BOM links, at any number — a client that
//     holds the step but not its latest piece list;
//  5. the same step reviewed in this save: draft cleared, same BOM links, same pieces, at the
//     stored number.
//
// In the strict keyed/legacy passes the candidate at the stored number wins, so an up-to-date echo
// pairs with itself. Per-piece content matching is different: the same-piece candidate must be
// unique. Anything looser (a link alone, a number alone) is not identity: a missed draft step is
// carried back as a visible duplicate, while a false match silently deletes it and hands a manual
// step an automatic identity.
func matchAutoFusingOps(stored, incoming *entity.TechCardInsert) autoFusingMatch {
	result := autoFusingMatch{matched: make(map[int]bool)}
	if stored == nil || incoming == nil {
		return result
	}
	autoIdx := make([]int, 0)
	fusingIdx := make([]int, 0)
	for i := range stored.Operations {
		op := &stored.Operations[i]
		if op.OperationType != entity.OpTypeFusing {
			continue
		}
		fusingIdx = append(fusingIdx, i)
		if _, ok := autoIdentity(op); ok {
			autoIdx = append(autoIdx, i)
		}
	}
	if len(autoIdx) == 0 {
		return result
	}
	shapes := make([]fusingOpShape, len(incoming.Operations))
	keyed := make([]bool, len(incoming.Operations))
	for j := range incoming.Operations {
		shapes[j] = shapeOfFusingOp(&incoming.Operations[j])
		_, keyed[j] = autoIdentity(&incoming.Operations[j])
	}
	taken := make(map[int]bool, len(incoming.Operations))
	blocked := make(map[int]bool, len(autoIdx))
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
	pass := func(accept func(s *entity.TechCardOperation, sShape fusingOpShape, j int, in *entity.TechCardOperation) bool) {
		for _, i := range autoIdx {
			if result.matched[i] || blocked[i] {
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
			result.matched[i] = true
			if !keyed[j] {
				incoming.Operations[j].AutoFusingLineKey = s.AutoFusingLineKey
				incoming.Operations[j].AutoFusingPieceKey = s.AutoFusingPieceKey
			}
		}
	}

	pass(func(s *entity.TechCardOperation, _ fusingOpShape, j int, in *entity.TechCardOperation) bool {
		sKey, _ := autoIdentity(s)
		inKey, ok := autoIdentity(in)
		return ok && inKey == sKey
	})

	// Match exact echoes as content equivalence classes before reserving either side. Matching a
	// manual row first by its old number is not global: after a reorder it can claim the automatic
	// echo and leave the manual echo as the automatic row's only same-piece candidate.
	grouped := make(map[int]bool, len(fusingIdx))
	for _, i := range fusingIdx {
		if grouped[i] || result.matched[i] {
			continue
		}
		var autos, manuals []int
		for _, k := range fusingIdx {
			if result.matched[k] || !sameFusingOpContent(&stored.Operations[i], &stored.Operations[k]) {
				continue
			}
			grouped[k] = true
			if _, ok := autoIdentity(&stored.Operations[k]); ok {
				autos = append(autos, k)
			} else {
				manuals = append(manuals, k)
			}
		}
		candidates := make([]int, 0)
		for j := range incoming.Operations {
			in := &incoming.Operations[j]
			if taken[j] || keyed[j] || in.OperationType != entity.OpTypeFusing ||
				!sameFusingOpContent(&stored.Operations[i], in) {
				continue
			}
			candidates = append(candidates, j)
		}
		switch {
		case len(autos) == 0 && len(candidates) == len(manuals):
			for _, j := range candidates {
				taken[j] = true
			}
		case len(autos) == 1 && len(manuals) == 0 && len(candidates) == 1:
			j := candidates[0]
			s := &stored.Operations[autos[0]]
			taken[j] = true
			result.matched[autos[0]] = true
			incoming.Operations[j].AutoFusingLineKey = s.AutoFusingLineKey
			incoming.Operations[j].AutoFusingPieceKey = s.AutoFusingPieceKey
		case len(autos) > 0 && len(candidates) > 0:
			result.unresolved = true
			for _, k := range autos {
				blocked[k] = true
			}
		}
	}
	pass(func(s *entity.TechCardOperation, sShape fusingOpShape, j int, _ *entity.TechCardOperation) bool {
		return !s.AutoFusingPieceKey.Valid && !keyed[j] && shapes[j] == sShape
	})
	// Shape is intentionally not identity for an edited per-piece row. The piece is: preserve the
	// private key across any unique same-piece edit, including draft -> reviewed and BOM/SMV/note
	// changes. With two candidates, choosing by order or operation number would silently key a manual
	// step, so reject the content match and let the store's conservative fallback have the last word.
	for _, i := range autoIdx {
		s := &stored.Operations[i]
		if result.matched[i] || !s.AutoFusingPieceKey.Valid {
			continue
		}
		candidate := -1
		count := 0
		for j := range incoming.Operations {
			in := &incoming.Operations[j]
			if taken[j] || keyed[j] || in.OperationType != entity.OpTypeFusing ||
				shapes[j].pieces != s.AutoFusingPieceKey.String {
				continue
			}
			candidate = j
			count++
		}
		switch count {
		case 0:
		case 1:
			taken[candidate] = true
			result.matched[i] = true
			incoming.Operations[candidate].AutoFusingLineKey = s.AutoFusingLineKey
			incoming.Operations[candidate].AutoFusingPieceKey = s.AutoFusingPieceKey
		default:
			result.unresolved = true
			blocked[i] = true
		}
	}
	pass(func(s *entity.TechCardOperation, sShape fusingOpShape, j int, in *entity.TechCardOperation) bool {
		return !keyed[j] && !s.AutoFusingPieceKey.Valid && s.Draft && in.Draft && shapes[j].bom == sShape.bom
	})
	pass(func(s *entity.TechCardOperation, sShape fusingOpShape, j int, in *entity.TechCardOperation) bool {
		reviewed := sShape
		reviewed.draft = false
		return !keyed[j] && s.Draft && s.OperationNumber.Valid &&
			in.OperationNumber == s.OperationNumber && shapes[j] == reviewed
	})
	return result
}

// PrepareTechCardUpdateAssembly restores private automatic identities before the DTO layer performs
// its first assembly validation. That timing matters when a save deletes a piece: the wire still
// echoes the server-owned FUSING step with that now-dead input, and the reconciler must be allowed to
// delete its draft or retain its edited orphan before the graph rejects the stale key.
func PrepareTechCardUpdateAssembly(stored, incoming *entity.TechCardInsert) {
	if stored == nil || incoming == nil {
		return
	}
	matchAutoFusingOps(stored, incoming)
	live := make(map[string]bool, len(incoming.Pieces))
	for i := range incoming.Pieces {
		live[incoming.Pieces[i].LineKey] = true
	}
	dead := make(map[string]bool)
	for i := range stored.Pieces {
		key := stored.Pieces[i].LineKey
		if key != "" && !live[key] {
			dead[key] = true
		}
	}
	if len(dead) == 0 {
		return
	}
	keepKey := func(key string) bool { return !dead[key] }
	for i := range incoming.Operations {
		op := &incoming.Operations[i]
		if op.OperationType != entity.OpTypeFusing || !op.AutoFusingPieceKey.Valid {
			continue
		}
		op.PieceIds = nil
		op.PieceLineKeys = filterStrings(op.PieceLineKeys, keepKey)
		op.InputKeys = filterStrings(op.InputKeys, keepKey)
		inputs := op.AssemblyInputs[:0]
		for _, input := range op.AssemblyInputs {
			if input.Kind == entity.AssemblyInputPiece && dead[input.Key] {
				continue
			}
			inputs = append(inputs, input)
		}
		op.AssemblyInputs = nilIfEmptyInputs(inputs)
	}
}

func filterStrings(values []string, keep func(string) bool) []string {
	var out []string
	for _, value := range values {
		if keep(value) {
			out = append(out, value)
		}
	}
	return out
}

func nilIfEmptyInputs(inputs []entity.OperationInput) []entity.OperationInput {
	if len(inputs) == 0 {
		return nil
	}
	return inputs
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
// the client dropped was deleted by a person and stays deleted; manual steps (no automatic identity)
// are never candidates.
//
// A carried step goes just before the first payload step that uses one of its pieces, as the
// reconciler places a new step; with no such step it returns to its stored index. Every step is then
// renumbered 10, 20, … and the payload's issue references follow their step's new number, exactly as
// ReconcileTechCard renumbers.
func carryMissingAutoFusingOps(stored, incoming *entity.TechCardInsert, matched map[int]bool) bool {
	if stored == nil || incoming == nil {
		return false
	}
	missing := make([]int, 0)
	for i := range stored.Operations {
		op := &stored.Operations[i]
		if _, auto := autoIdentity(op); op.OperationType == entity.OpTypeFusing && op.Draft && auto && !matched[i] {
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
		pieces    []string
		oldNumber sql.NullInt32 // payload numbering; invalid for a carried step
	}
	slots := make([]slot, 0, len(incoming.Operations)+len(missing))
	for _, op := range incoming.Operations {
		slots = append(slots, slot{op: op, pieces: pieceInputKeys(&op, pieceKeys), oldNumber: op.OperationNumber})
	}
	for _, i := range missing {
		carried := slot{op: carriedAutoFusingOp(stored.Operations[i], pieceKeys, bomKeys)}
		carried.pieces = carried.op.PieceLineKeys
		position := -1
		for s := range slots {
			if sharesKey(slots[s].pieces, carried.pieces) {
				position = s
				break
			}
		}
		if position < 0 {
			position = i
			if position > len(slots) {
				position = len(slots)
			}
		}
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

func sharesKey(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// hasGroupedAutoFusing reports a grouped automatic step of the one-per-line model (line key without
// piece key) that is not an orphan: the card still has to be split into per-piece steps.
func hasGroupedAutoFusing(tc *entity.TechCardInsert) bool {
	if tc == nil {
		return false
	}
	for i := range tc.Operations {
		op := &tc.Operations[i]
		if op.OperationType == entity.OpTypeFusing && op.AutoFusingLineKey.Valid && !op.AutoFusingPieceKey.Valid &&
			!(op.Review.Valid && op.Review.String == ReviewOrphan) {
			return true
		}
	}
	return false
}

// UpdateOutcome is what an update's FUSING pass did to the write model.
type UpdateOutcome struct {
	// Reconcile: piece fusing or INTERLINING membership changed, or the FUSING steps are not in
	// their per-piece shape. The write model is reconciled and the store must repeat the
	// reconciliation against rows (entity.TechCardInsert.ReconcileFusing).
	Reconcile bool
	// Carried: nothing fusing-relevant changed, but the payload lacked draft automatic FUSING steps
	// and they were put back (operations renumbered, issue references remapped).
	Carried bool
}

// ReconcileTechCardUpdate is the update-only trigger. Create and clone paths call ReconcileTechCard
// directly. It reconciles when
//   - piece fusing or INTERLINING membership changed (FusingStateChanged);
//   - the stored card still holds a grouped step of the one-per-line model (it is split on this
//     save; there is no separate backfill);
//   - the payload's FUSING steps are not what the reconciler would leave: a grouped or adoptable
//     step to convert, a skeleton draft duplicating a piece's step, a step whose material link or
//     piece no longer matches its piece. A fused piece merely lacking a step is not a reason: a
//     person may have deleted its edited step.
//
// Otherwise it only restores the draft automatic steps a stale payload dropped. Either way the caller
// must re-run assembly validation when the outcome reports a change.
func ReconcileTechCardUpdate(stored, incoming *entity.TechCardInsert) (UpdateOutcome, *entity.ValidationError) {
	if incoming == nil {
		return UpdateOutcome{}, nil
	}
	// This private identity is intentionally absent from the wire, so carry it across the operation
	// full-replace even on a no-op save. It changes no instruction or workflow state. The store's
	// number-only fallback must not second-guess this identity match.
	matches := matchAutoFusingOps(stored, incoming)
	if FusingStateChanged(stored, incoming) || hasGroupedAutoFusing(stored) || fusingOpsNeedReconcile(incoming) {
		// The reconcile creates a step for every fused piece without one, so automatic steps a
		// stale payload dropped come back here too (as fresh drafts before their first use).
		verr := ReconcileTechCard(incoming)
		incoming.AutoFusingKeysResolved = verr == nil && !matches.unresolved
		return UpdateOutcome{Reconcile: true}, verr
	}
	carried := carryMissingAutoFusingOps(stored, incoming, matches.matched)
	incoming.AutoFusingKeysResolved = !matches.unresolved
	return UpdateOutcome{Carried: carried}, nil
}
