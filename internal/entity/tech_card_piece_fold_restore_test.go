package entity

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func foldRestoreIn(key, piece string, seq int) TechCardPieceFoldInput {
	return TechCardPieceFoldInput{FoldKey: key, PieceLineKey: piece, Seq: seq}
}

func foldRestoreKeys(folds []TechCardPieceFoldInput) []string {
	out := make([]string, 0, len(folds))
	for _, f := range folds {
		out = append(out, f.FoldKey)
	}
	return out
}

// Every way a snapshot row stays out, one row each, beside the rows that come back in snapshot order.
func TestPlanTechCardPieceFoldsRestore(t *testing.T) {
	const (
		back, kept, gone = "PIECEBACK", "PIECEKEPT", "PIECEGONE"
	)
	snapshot := []TechCardPieceFoldInput{
		foldRestoreIn("K1", back, 2),
		foldRestoreIn("K2", kept, 1), // kept holds a fold today
		foldRestoreIn("K3", gone, 1), // gone is not on the restored card
		foldRestoreIn("K4", back, 3), // another piece of the card holds K4 today
		foldRestoreIn("K1", back, 4), // the snapshot repeats K1
		foldRestoreIn("K5", back, 2), // the snapshot repeats back's seq 2
		foldRestoreIn("k6", " pieceback ", 1),
	}
	held := []TechCardPieceFoldHeld{{FoldKey: "K9", PieceLineKey: kept}, {FoldKey: "k4", PieceLineKey: kept}}
	plan, skipped := PlanTechCardPieceFoldsRestore(snapshot, []string{back, kept}, held)
	require.Equal(t, []string{"K1", "k6"}, foldRestoreKeys(plan), "keys and pieces compare normalised")
	require.Equal(t, map[string]int{
		TechCardFoldRestorePieceHolds:   1,
		TechCardFoldRestorePieceMissing: 1,
		TechCardFoldRestoreKeyHeld:      1,
		TechCardFoldRestoreDuplicate:    2,
	}, skipped)

	none, skippedNone := PlanTechCardPieceFoldsRestore(nil, []string{back}, nil)
	require.Empty(t, none)
	require.Empty(t, skippedNone)
}

// The cap counts what the card will hold: today's rows plus the re-created ones.
func TestPlanTechCardPieceFoldsRestoreStopsAtTheCap(t *testing.T) {
	held := make([]TechCardPieceFoldHeld, 0, TechCardPieceFoldMaxRowsPerCard-1)
	for i := 0; i < TechCardPieceFoldMaxRowsPerCard-1; i++ {
		held = append(held, TechCardPieceFoldHeld{FoldKey: fmt.Sprintf("H%d", i), PieceLineKey: "OTHER"})
	}
	plan, skipped := PlanTechCardPieceFoldsRestore(
		[]TechCardPieceFoldInput{foldRestoreIn("A", "P", 1), foldRestoreIn("B", "P", 2)}, []string{"P", "OTHER"}, held)
	require.Equal(t, []string{"A"}, foldRestoreKeys(plan))
	require.Equal(t, map[string]int{TechCardFoldRestoreTooMany: 1}, skipped)
}
