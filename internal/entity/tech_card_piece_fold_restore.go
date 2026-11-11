package entity

import (
	"fmt"
	"strings"
)

// FOLDS COME BACK WITH THE PIECES A RESTORE BRINGS BACK. A card save that drops a cut piece deletes
// its folds with it (owner decision 11.11), so «restore v.N» that returns the piece must return its
// folds too, or they are lost for good. The rule is per piece: a piece of the restored card that holds
// NO fold row today gets every row the snapshot has for it; a piece that holds folds today keeps them
// untouched — no overwrite, no delete, no merge. A row that cannot land is skipped and counted, never
// refused: a snapshot must stay restorable.

// TechCardPieceFoldsRestore is the snapshot's fold list a restore may bring back
// (RestoreTechCardSnapshot → TechCardInsert.RestorePieceFolds).
type TechCardPieceFoldsRestore struct {
	// Folds are the snapshot's rows that validate on their own, normalised, in snapshot order.
	Folds []TechCardPieceFoldInput
	// Stale holds the keys the snapshot read as stale. They come back stamped stale, as a restored
	// seam does: a restore never forges «drawn on these patterns» for a row that was not.
	Stale map[string]bool
	// Recreated is store-private: the rows the store wrote, re-stamped once the restored sheets are
	// in. Reset on every attempt of the transaction.
	Recreated []TechCardPieceFoldInput `json:"-"`
}

// TechCardPieceFoldHeld is a fold row the card holds today, as the restore plan reads it.
type TechCardPieceFoldHeld struct {
	FoldKey      string
	PieceLineKey string
}

// Why a snapshot fold does not come back.
const (
	TechCardFoldRestorePieceMissing = "piece_not_on_card" // the restored card has no such piece
	TechCardFoldRestorePieceHolds   = "piece_holds_folds" // the piece kept folds of its own today
	TechCardFoldRestoreKeyHeld      = "key_on_card"       // another piece of this card holds the key today
	TechCardFoldRestoreDuplicate    = "duplicate"         // the snapshot repeats the key, or the piece's seq
	TechCardFoldRestoreTooMany      = "too_many"          // past TechCardPieceFoldMaxRowsPerCard
	TechCardFoldRestoreInvalid      = "invalid"           // the row does not validate
)

// PlanTechCardPieceFoldsRestore picks the snapshot rows a restore re-creates and counts the rest by
// reason. pieces are the line keys of the restored card; held is what the card holds once the save
// has dropped the folds of the pieces it deletes. The handler predicts the store's plan with it, so
// both must call it on the same facts.
func PlanTechCardPieceFoldsRestore(snapshot []TechCardPieceFoldInput, pieces []string, held []TechCardPieceFoldHeld) ([]TechCardPieceFoldInput, map[string]int) {
	if len(snapshot) == 0 {
		return nil, nil
	}
	onCard := make(map[string]bool, len(pieces))
	for _, p := range pieces {
		if p = foldRestorePiece(p); p != "" {
			onCard[p] = true
		}
	}
	holding := make(map[string]bool, len(held))
	heldKey := make(map[string]bool, len(held))
	for _, h := range held {
		holding[foldRestorePiece(h.PieceLineKey)] = true
		heldKey[NormalizeTechCardSeamKey(h.FoldKey)] = true
	}
	room := TechCardPieceFoldMaxRowsPerCard - len(held)
	seenKey := make(map[string]bool, len(snapshot))
	seenSeq := make(map[string]bool, len(snapshot))
	skipped := map[string]int{}
	var plan []TechCardPieceFoldInput
	for _, f := range snapshot {
		key, piece := NormalizeTechCardSeamKey(f.FoldKey), foldRestorePiece(f.PieceLineKey)
		seq := fmt.Sprintf("%s\x00%d", piece, f.Seq)
		repeated := seenKey[key]
		seenKey[key] = true
		switch {
		case repeated:
			skipped[TechCardFoldRestoreDuplicate]++
		case !onCard[piece]:
			skipped[TechCardFoldRestorePieceMissing]++
		case holding[piece]:
			skipped[TechCardFoldRestorePieceHolds]++
		case heldKey[key]:
			skipped[TechCardFoldRestoreKeyHeld]++
		case seenSeq[seq]:
			skipped[TechCardFoldRestoreDuplicate]++
		case len(plan) >= room:
			skipped[TechCardFoldRestoreTooMany]++
		default:
			seenSeq[seq] = true
			plan = append(plan, f)
		}
	}
	return plan, skipped
}

func foldRestorePiece(lineKey string) string { return strings.ToUpper(strings.TrimSpace(lineKey)) }
