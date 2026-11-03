package fusingops

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// FOLD LINKS OF A STEP THE RECONCILIATION TAKES AWAY. A fold may close on a FUSING step, and the
// reconciliation deletes a duplicate of such a step or splits a grouped one. The link then moves, it
// never goes silently: both passes take it off the step here or in the store, and the store, which
// knows the fold's piece, puts it on the step FoldHome names or refuses by the link's address.

// FoldStep is what choosing a new step for a fold link reads about one step.
type FoldStep struct {
	Owner  string   // the piece an automatic step fuses; "" on any other step and on an orphan
	Pieces []string // the step's piece inputs
}

// FoldStepsOf reads the steps of a reconciled write model.
func FoldStepsOf(ops []entity.TechCardOperation, pieces []entity.TechCardPiece) []FoldStep {
	pieceKeys := make(map[string]bool, len(pieces))
	for i := range pieces {
		pieceKeys[pieces[i].LineKey] = true
	}
	steps := make([]FoldStep, len(ops))
	for i := range ops {
		op := &ops[i]
		steps[i].Pieces = pieceInputKeys(op, pieceKeys)
		if op.AutoFusingPieceKey.Valid && !(op.Review.Valid && op.Review.String == ReviewOrphan) {
			steps[i].Owner = op.AutoFusingPieceKey.String
		}
	}
	return steps
}

// FoldHome returns the index of the step that takes over a fold link, -1 when none can. foldPiece is
// the fold's piece, from the pieces of the step the link sat on. In this order, the earliest step of
// each kind wins:
//  1. the automatic step of the fold's own piece: a grouped step's fold lands on its piece's step;
//  2. the automatic step of one of the pieces the link's step fused: its survivor, at the same moment
//     of the assembly;
//  3. any step taking the fold's piece as an input;
//  4. any step taking one of the link's step's pieces as an input.
func FoldHome(foldPiece string, from []string, steps []FoldStep) int {
	for _, matches := range []func(FoldStep) bool{
		func(s FoldStep) bool { return sameFoldPiece(s.Owner, foldPiece) },
		func(s FoldStep) bool { return foldPieceIn(s.Owner, from) },
		func(s FoldStep) bool { return foldPieceIn(foldPiece, s.Pieces) },
		func(s FoldStep) bool {
			for _, piece := range from {
				if foldPieceIn(piece, s.Pieces) {
					return true
				}
			}
			return false
		},
	} {
		for i := range steps {
			if matches(steps[i]) {
				return i
			}
		}
	}
	return -1
}

func sameFoldPiece(a, b string) bool {
	a = strings.TrimSpace(a)
	return a != "" && strings.EqualFold(a, strings.TrimSpace(b))
}

func foldPieceIn(piece string, pieces []string) bool {
	for _, p := range pieces {
		if sameFoldPiece(piece, p) {
			return true
		}
	}
	return false
}

// displaceFoldKeys records the fold links of the steps a plan deletes or splits and returns the split
// steps. Plan ids are positions + 1 in the body as it was sent, which is what a refusal must address.
func displaceFoldKeys(tc *entity.TechCardInsert, plan Plan) map[int]bool {
	split := make(map[int]bool)
	for _, planned := range plan.Operations {
		if planned.ID == 0 && planned.CopyOf > 0 {
			split[planned.CopyOf] = true
		}
	}
	displace := func(id int) {
		op := &tc.Operations[id-1]
		if len(op.FoldKeys) == 0 {
			return
		}
		pieces := append([]string(nil), pieceInputKeys(op, nil)...)
		if op.AutoFusingPieceKey.Valid {
			pieces = append(pieces, op.AutoFusingPieceKey.String)
		}
		for j, key := range op.FoldKeys {
			tc.DisplacedFoldKeys = append(tc.DisplacedFoldKeys, entity.TechCardDisplacedFoldKey{
				Key: key, Field: fmt.Sprintf("operations[%d].fold_keys[%d]", id-1, j), Pieces: pieces,
			})
		}
	}
	for _, id := range plan.DeletedIDs {
		displace(id)
	}
	for _, planned := range plan.Operations {
		if planned.ID > 0 && split[planned.ID] {
			displace(planned.ID)
		}
	}
	return split
}

// foldKeysNoStepNames keeps the keys of a carried step that no step of the payload names: the payload
// moved the others on purpose, and carrying them back would put one fold on two steps.
func foldKeysNoStepNames(keys []string, ops []entity.TechCardOperation) []string {
	named := make(map[string]bool)
	for i := range ops {
		for _, key := range ops[i].FoldKeys {
			named[entity.NormalizeTechCardSeamKey(key)] = true
		}
	}
	var out []string
	for _, key := range keys {
		if !named[entity.NormalizeTechCardSeamKey(key)] {
			out = append(out, key)
		}
	}
	return out
}
