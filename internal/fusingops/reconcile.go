// Package fusingops contains the database-free state transition behind automatic FUSING operations.
package fusingops

const ReviewOrphan = "orphan"

// Piece is the minimum cut-piece state the operation reconciler needs. BOMItemID is zero when the
// piece's interlining is unresolved (no sole line, or several lines without an explicit choice).
type Piece struct {
	ID        int
	Fused     bool
	BOMItemID int
}

// Operation is the minimum operation state the pure reconciler reads and returns. A non-FUSING
// operation is carried through byte-for-byte. ID zero denotes an operation the caller must create.
type Operation struct {
	ID        int
	Fusing    bool
	BOMItemID int
	Draft     bool
	Review    string
	PieceIDs  []int
}

// Plan is the reconciled operation list plus the ids of draft FUSING operations that became empty.
// Operations is already in assembly order: all retained/new FUSING operations first, preserving the
// relative order of existing FUSING operations, followed by untouched operations in their order.
type Plan struct {
	Operations []Operation
	DeletedIDs []int
}

// ReconcileFusingOps computes the idempotent FUSING operation state. One operation owns every fused
// piece resolved to the same interlining BOM line; BOMItemID zero is the shared unresolved bucket.
// Empty draft operations disappear, while edited operations survive with review=orphan.
func ReconcileFusingOps(pieces []Piece, operations []Operation) Plan {
	desired := make(map[int][]int)
	desiredOrder := make([]int, 0)
	seenPiece := make(map[int]bool, len(pieces))
	for _, piece := range pieces {
		if !piece.Fused || piece.ID <= 0 || seenPiece[piece.ID] {
			continue
		}
		seenPiece[piece.ID] = true
		if _, ok := desired[piece.BOMItemID]; !ok {
			desiredOrder = append(desiredOrder, piece.BOMItemID)
		}
		desired[piece.BOMItemID] = append(desired[piece.BOMItemID], piece.ID)
	}

	claimed := make(map[int]bool, len(desired))
	fusing := make([]Operation, 0, len(operations)+len(desired))
	other := make([]Operation, 0, len(operations))
	var deleted []int
	for _, operation := range operations {
		if !operation.Fusing {
			other = append(other, operation)
			continue
		}
		pieceIDs, wanted := desired[operation.BOMItemID]
		if wanted && !claimed[operation.BOMItemID] {
			operation.PieceIDs = append([]int(nil), pieceIDs...)
			if operation.Review == ReviewOrphan {
				operation.Review = ""
			}
			claimed[operation.BOMItemID] = true
			fusing = append(fusing, operation)
			continue
		}
		if operation.Draft {
			if operation.ID > 0 {
				deleted = append(deleted, operation.ID)
			}
			continue
		}
		operation.PieceIDs = nil
		operation.Review = ReviewOrphan
		fusing = append(fusing, operation)
	}
	for _, bomItemID := range desiredOrder {
		if claimed[bomItemID] {
			continue
		}
		fusing = append(fusing, Operation{
			Fusing: true, BOMItemID: bomItemID, Draft: true,
			PieceIDs: append([]int(nil), desired[bomItemID]...),
		})
	}

	return Plan{Operations: append(fusing, other...), DeletedIDs: deleted}
}
