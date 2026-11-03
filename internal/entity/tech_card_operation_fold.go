package entity

import "strings"

// OPERATION → FOLD LINKS (0444). A step names the piece folds that close on it; a fold no step names
// leaves its piece flat. The one rule over a card's steps lives here as a pure function, because five
// paths hold it against different things: the card save and the fold write refuse on the first
// problem, the archive import and the snapshot restore (TechCardInsert.RestoreFolds) cut the offending
// links and go on.

// TechCardOperationMaxFoldKeys bounds one step's folds, as TechCardOperationMaxSeamKeys does.
const TechCardOperationMaxFoldKeys = 64

// TechCardFoldOrderRef is what the rule needs to know about one fold of the card.
type TechCardFoldOrderRef struct {
	PieceLineKey string
	Seq          int
}

// TechCardFoldLinkProblemKind says what is wrong with one step's link; the value is the refusal's
// field-violation reason.
type TechCardFoldLinkProblemKind string

const (
	// TechCardFoldLinkUnknown: the key names no fold of this card.
	TechCardFoldLinkUnknown TechCardFoldLinkProblemKind = "unknown_fold"
	// TechCardFoldLinkDuplicate: the step names the fold twice.
	TechCardFoldLinkDuplicate TechCardFoldLinkProblemKind = "duplicate"
	// TechCardFoldLinkTwoSteps: an earlier step already closes this fold.
	TechCardFoldLinkTwoSteps TechCardFoldLinkProblemKind = "fold_on_two_steps"
	// TechCardFoldLinkOrder: an earlier step closes a fold of the same piece with a larger seq.
	TechCardFoldLinkOrder TechCardFoldLinkProblemKind = "fold_order"
	// TechCardFoldLinkStepGone: the FUSING reconciliation removes the step and no other step can take
	// the fold over. Never produced by CheckTechCardFoldLinks: it is about steps that stop existing.
	TechCardFoldLinkStepGone TechCardFoldLinkProblemKind = "fold_step_gone"
)

// TechCardDisplacedFoldKey is a fold link the FUSING reconciliation took off its step because it
// removed or split that step. The write model does not know which piece a fold is on, so it leaves the
// choice of the new step to the store, inside the save's transaction.
type TechCardDisplacedFoldKey struct {
	Key    string
	Field  string   // the link's address in the body as it was sent, for a refusal
	Pieces []string // the pieces of the step it sat on
}

// TechCardFoldLinkProblem is one link the rule does not admit.
type TechCardFoldLinkProblem struct {
	Step int // index of the step in the walked order
	Pos  int // index of the key within the step
	Key  string
	Kind TechCardFoldLinkProblemKind
	// OtherStep and OtherKey name the link this one collides with (two steps, order); -1 and "" otherwise.
	OtherStep int
	OtherKey  string
}

// CheckTechCardFoldLinks walks the steps in order and returns every link the rule does not admit:
//   - the key names a fold of this card (folds is keyed by the normalised fold_key);
//   - one fold closes on at most one step, once;
//   - on one piece a fold with a larger seq never stands on a STRICTLY earlier step than a fold with a
//     smaller seq. The same step takes its folds in any order, and a fold with no step takes no part.
//
// A link with a problem counts as cut: the walk goes on as if it were not there, so the links the
// result does not name are exactly a list that passes this check again. nil = all admitted.
func CheckTechCardFoldLinks(steps [][]string, folds map[string]TechCardFoldOrderRef) []TechCardFoldLinkProblem {
	type mark struct {
		seq  int
		step int
		key  string
	}
	var problems []TechCardFoldLinkProblem
	stepOf := map[string]int{}
	maxEarlier := map[string]mark{} // piece → the largest seq closed on a strictly earlier step
	for i, keys := range steps {
		var here []mark
		var herePieces []string
		for j, raw := range keys {
			k := NormalizeTechCardSeamKey(raw)
			ref, known := folds[k]
			if !known {
				problems = append(problems, TechCardFoldLinkProblem{Step: i, Pos: j, Key: k,
					Kind: TechCardFoldLinkUnknown, OtherStep: -1})
				continue
			}
			if prev, dup := stepOf[k]; dup {
				kind := TechCardFoldLinkTwoSteps
				other := prev
				if prev == i {
					kind, other = TechCardFoldLinkDuplicate, -1
				}
				problems = append(problems, TechCardFoldLinkProblem{Step: i, Pos: j, Key: k, Kind: kind, OtherStep: other})
				continue
			}
			piece := techCardFoldPiece(ref.PieceLineKey)
			if m, ok := maxEarlier[piece]; ok && m.seq > ref.Seq {
				problems = append(problems, TechCardFoldLinkProblem{Step: i, Pos: j, Key: k,
					Kind: TechCardFoldLinkOrder, OtherStep: m.step, OtherKey: m.key})
				continue
			}
			stepOf[k] = i
			here = append(here, mark{seq: ref.Seq, step: i, key: k})
			herePieces = append(herePieces, piece)
		}
		// Folds of one step do not order each other: the marks land only when the step is over.
		for n, m := range here {
			if prev, ok := maxEarlier[herePieces[n]]; !ok || m.seq > prev.seq {
				maxEarlier[herePieces[n]] = m
			}
		}
	}
	return problems
}

// CutTechCardFoldLinks returns the steps with every link CheckTechCardFoldLinks does not admit taken
// out, keys normalised and order kept, along with the problems. A step left with nothing is nil.
func CutTechCardFoldLinks(steps [][]string, folds map[string]TechCardFoldOrderRef) ([][]string, []TechCardFoldLinkProblem) {
	problems := CheckTechCardFoldLinks(steps, folds)
	cut := make(map[[2]int]bool, len(problems))
	for _, p := range problems {
		cut[[2]int{p.Step, p.Pos}] = true
	}
	out := make([][]string, len(steps))
	for i, keys := range steps {
		for j, k := range keys {
			if !cut[[2]int{i, j}] {
				out[i] = append(out[i], NormalizeTechCardSeamKey(k))
			}
		}
	}
	return out, problems
}

// techCardFoldPiece normalises a piece key the way TechCardPieceFold's write and fingerprint do.
func techCardFoldPiece(key string) string { return strings.ToUpper(strings.TrimSpace(key)) }
