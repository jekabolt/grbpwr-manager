// Package fusingops contains the database-free state transition behind automatic FUSING operations.
package fusingops

import "sort"

const ReviewOrphan = "orphan"

// Piece is the minimum cut-piece state the operation reconciler needs, in the card's piece order.
type Piece struct {
	ID    int
	Fused bool
}

// Kind is how the reconciler treats an operation. Callers classify every operation the same way
// on both sides (write model and rows), so the two passes yield the same plan.
type Kind int

const (
	// KindOther is not a FUSING step. Never changed; its piece inputs place new FUSING steps.
	KindOther Kind = iota
	// KindManual is a FUSING step without automatic identity that is not shaped like a pre-fuse
	// step (output unit, unit inputs, or non-interlining links). Automatic steps override its
	// duplicate piece inputs; its other inputs and content stay untouched.
	KindManual
	// KindAdoptable is a FUSING step without automatic identity shaped like a pre-fuse step: pieces
	// only, no output, no BOM link or one INTERLINING link (the assembly skeleton's steps). It is
	// adopted as the step of its first fused piece that has none.
	KindAdoptable
	// KindLegacy is a grouped automatic step of the one-per-line model (line key, no piece key).
	// It is split: the first fused piece keeps the row, every other one gets a copy.
	KindLegacy
	// KindAuto is a per-piece automatic step. Owner is the piece it fuses (0 = the piece is gone).
	KindAuto
)

// Operation is the minimum operation state the pure reconciler reads and returns. ID zero denotes an
// operation the caller must create; CopyOf then names the operation whose press settings it copies
// (zero = defaults). UnitInputs reports unit inputs, which keep a converted step where it stands.
type Operation struct {
	ID         int
	Kind       Kind
	Owner      int
	Draft      bool
	Review     string
	PieceIDs   []int
	UnitInputs bool
	CopyOf     int
}

// Plan is the reconciled operation list in assembly order plus the ids of steps to delete.
// Every operation of KindAuto in it is server-owned: an orphan (Review == ReviewOrphan) has no piece
// inputs, any other one has exactly [Owner]. Manual FUSING steps lose inputs already owned by an
// automatic step and are deleted when that override leaves them with no inputs.
type Plan struct {
	Operations []Operation
	DeletedIDs []int
}

// IsOrphan reports a retained automatic step that no longer fuses a piece.
func (o Operation) IsOrphan() bool { return o.Kind == KindAuto && o.Review == ReviewOrphan }

// ReconcileFusingOps computes the idempotent FUSING state: exactly one automatic step per fused
// piece, placed just before the first step that uses the piece.
//
//  1. A per-piece step keeps its piece while the piece is fused and no earlier step claimed it.
//  2. In operation order, a grouped legacy step or an adoptable manual step takes its fused pieces
//     nobody claimed yet: the first keeps the row, each other gets a new draft copying its press
//     settings. A legacy step left without pieces, like a per-piece step without its piece, is
//     deleted when draft and kept as an orphan when edited. An adoptable draft whose pieces are all
//     fused (and so all claimed) is a duplicate and is deleted; any other adoptable step is kept.
//  3. Every fused piece still unclaimed gets a new draft step.
//  4. Automatic ownership wins over manual FUSING: covered piece inputs are removed from every
//     remaining manual step. A step made empty by those removals is deleted; a step that was already
//     empty is not. Non-FUSING operations are never changed.
//
// Retained steps keep their order. New steps, and grouped legacy rows the old model had forced to the
// front (unless they have unit inputs), go immediately before the first retained step whose inputs
// hold their piece; ties follow piece order, and a piece no step uses yet goes first.
func ReconcileFusingOps(pieces []Piece, operations []Operation) Plan {
	rank := make(map[int]int, len(pieces))
	fused := make(map[int]bool, len(pieces))
	fusedOrder := make([]int, 0, len(pieces))
	for _, piece := range pieces {
		if piece.ID <= 0 {
			continue
		}
		if _, seen := rank[piece.ID]; seen {
			continue
		}
		rank[piece.ID] = len(rank)
		if piece.Fused {
			fused[piece.ID] = true
			fusedOrder = append(fusedOrder, piece.ID)
		}
	}

	out := make([]Operation, len(operations))
	originalPieces := make(map[int][]int, len(operations))
	keep := make([]bool, len(operations))
	move := make([]bool, len(operations))
	claimed := make(map[int]bool, len(fusedOrder))
	var deleted []int
	dropOrOrphan := func(i int) {
		if out[i].Draft {
			keep[i] = false
			if out[i].ID > 0 {
				deleted = append(deleted, out[i].ID)
			}
			return
		}
		out[i].Kind = KindAuto
		out[i].PieceIDs = nil
		out[i].Review = ReviewOrphan
	}
	own := func(i, pieceID int) {
		claimed[pieceID] = true
		out[i].Kind = KindAuto
		out[i].Owner = pieceID
		out[i].PieceIDs = []int{pieceID}
		if out[i].Review == ReviewOrphan {
			out[i].Review = ""
		}
	}

	for i, operation := range operations {
		operation.PieceIDs = append([]int(nil), operation.PieceIDs...)
		if operation.ID > 0 {
			originalPieces[operation.ID] = operation.PieceIDs
		}
		operation.CopyOf = 0
		out[i], keep[i] = operation, true
		if operation.Kind != KindAuto {
			continue
		}
		if operation.Owner > 0 && fused[operation.Owner] && !claimed[operation.Owner] {
			own(i, operation.Owner)
			continue
		}
		dropOrOrphan(i)
	}

	fresh := make([]Operation, 0)
	for i, operation := range operations {
		if operation.Kind != KindLegacy && operation.Kind != KindAdoptable {
			continue
		}
		candidates := make([]int, 0, len(operation.PieceIDs))
		allFused := len(operation.PieceIDs) > 0
		for _, pieceID := range operation.PieceIDs {
			if !fused[pieceID] {
				allFused = false
				continue
			}
			if claimed[pieceID] {
				continue
			}
			claimed[pieceID] = true // reserve; own() below re-marks the first
			candidates = append(candidates, pieceID)
		}
		if len(candidates) == 0 {
			switch {
			case operation.Kind == KindLegacy:
				dropOrOrphan(i)
			case operation.Draft && allFused:
				keep[i] = false
				if operation.ID > 0 {
					deleted = append(deleted, operation.ID)
				}
			}
			continue
		}
		own(i, candidates[0])
		move[i] = operation.Kind == KindLegacy && !operation.UnitInputs
		for _, pieceID := range candidates[1:] {
			fresh = append(fresh, Operation{
				Kind: KindAuto, Owner: pieceID, Draft: true, PieceIDs: []int{pieceID}, CopyOf: operation.ID,
			})
		}
	}
	for _, pieceID := range fusedOrder {
		if claimed[pieceID] {
			continue
		}
		claimed[pieceID] = true
		fresh = append(fresh, Operation{Kind: KindAuto, Owner: pieceID, Draft: true, PieceIDs: []int{pieceID}})
	}

	// All fused pieces now have an automatic owner (pre-existing, adopted, or fresh). Strip those
	// pieces from manual FUSING steps so the same cut piece can never be fused twice. Do this only
	// after adoption: a compatible single-piece skeleton/manual step may itself become the automatic
	// owner under the established adoption rule.
	for i, operation := range out {
		if !keep[i] || (operation.Kind != KindManual && operation.Kind != KindAdoptable) {
			continue
		}
		var remaining []int
		removed := false
		for _, pieceID := range operation.PieceIDs {
			if claimed[pieceID] {
				removed = true
				continue
			}
			remaining = append(remaining, pieceID)
		}
		if !removed {
			continue // in particular, an already-empty manual step stays untouched
		}
		out[i].PieceIDs = remaining
		if len(remaining) == 0 && !operation.UnitInputs {
			keep[i] = false
			if operation.ID > 0 {
				deleted = append(deleted, operation.ID)
			}
		}
	}

	retained := make([]Operation, 0, len(out)+len(fresh))
	placed := make([]Operation, 0, len(fresh))
	for i := range out {
		switch {
		case !keep[i]:
		case move[i]:
			placed = append(placed, out[i])
		default:
			retained = append(retained, out[i])
		}
	}
	placed = append(placed, fresh...)
	if len(placed) == 0 {
		return Plan{Operations: retained, DeletedIDs: deleted}
	}

	anchor := make([]int, len(placed))
	for p := range placed {
		owner := placed[p].Owner
		for j := range retained {
			inputs := retained[j].PieceIDs
			if original, ok := originalPieces[retained[j].ID]; ok &&
				(retained[j].Kind == KindManual || retained[j].Kind == KindAdoptable) {
				inputs = original
			}
			if containsInt(inputs, owner) {
				anchor[p] = j
				break
			}
		}
	}
	index := make([]int, len(placed))
	for p := range index {
		index[p] = p
	}
	sort.SliceStable(index, func(a, b int) bool {
		pa, pb := index[a], index[b]
		if anchor[pa] != anchor[pb] {
			return anchor[pa] < anchor[pb]
		}
		return rank[placed[pa].Owner] < rank[placed[pb].Owner]
	})
	ordered := make([]Operation, 0, len(retained)+len(placed))
	next := 0
	for j := 0; j <= len(retained); j++ {
		for next < len(index) && anchor[index[next]] == j {
			ordered = append(ordered, placed[index[next]])
			next++
		}
		if j < len(retained) {
			ordered = append(ordered, retained[j])
		}
	}
	return Plan{Operations: ordered, DeletedIDs: deleted}
}

func containsInt(values []int, value int) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
