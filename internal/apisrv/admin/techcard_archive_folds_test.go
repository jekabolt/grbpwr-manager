package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

const (
	archFoldA = "01JABCDEFGHJKMNPQRSTVWXYA1"
	archFoldB = "01JABCDEFGHJKMNPQRSTVWXYB2"
	archFoldC = "01JABCDEFGHJKMNPQRSTVWXYC3"
	archFlap  = "01JRSTFLAP0000000000000000"
	archCuff  = "01JRSTCUFF0000000000000000"
)

func archFold(key, piece string, seq int32) *pb_common.TechCardPieceFold {
	return &pb_common.TechCardPieceFold{
		FoldKey: key, PieceLineKey: piece, Seq: seq,
		Kind:   pb_common.TechCardPieceFoldKind_TECH_CARD_PIECE_FOLD_KIND_INWARD,
		Source: pb_common.TechCardPieceFoldSource_TECH_CARD_PIECE_FOLD_SOURCE_OWNER,
		Line:   []*pb_common.TechCardSeamSample{{U: 0, V: 0.5}, {U: 1, V: 0.5}},
		FlipAt: &pb_common.TechCardSeamSample{U: 0.5, V: 0.9},
	}
}

func archFoldResolve(folds []*pb_common.TechCardPieceFold, steps ...[]string) *tcimpResolver {
	ops := make([]*pb_common.TechCardOperation, 0, len(steps))
	for _, keys := range steps {
		ops = append(ops, &pb_common.TechCardOperation{FoldKeys: keys})
	}
	insert := &pb_common.TechCardInsert{
		Pieces:     []*pb_common.TechCardPiece{{LineKey: archFlap}, {LineKey: archCuff}},
		Operations: ops,
	}
	r := &tcimpResolver{
		out: &resolvedTechCardImport{
			Counters: techcardarchive.NewCounters(),
			Insert:   insert,
			Card:     &pb_common.TechCard{TechCard: insert, PieceFolds: folds},
		},
		holeSeen: map[string]bool{},
	}
	r.resolveFolds()
	return r
}

func archFoldKeys(r *tcimpResolver) [][]string {
	var out [][]string
	for _, op := range r.out.Insert.GetOperations() {
		out = append(out, op.GetFoldKeys())
	}
	return out
}

func archFoldLine(t *testing.T, r *tcimpResolver, ref string, reason techcardarchive.Reason) {
	t.Helper()
	for _, h := range r.out.Holes {
		if h.Entity == techcardarchive.EntityFold && h.Ref == ref && h.Reason == reason {
			return
		}
	}
	t.Fatalf("no %s line for %s in %+v", reason, ref, r.out.Holes)
}

// A card with no folds: no counter, no line, no plan. The report reads as it did before the folds.
func TestResolveFoldsOnAFoldlessCardSaysNothing(t *testing.T) {
	r := archFoldResolve(nil, nil, nil)
	require.Empty(t, r.out.FoldPlan)
	require.Empty(t, r.out.Holes)
	_, counted := r.out.Counters[techcardarchive.EntityFold]
	require.False(t, counted, "the fold counter appears only when the archive carries folds")
}

// Every archive written before the fold steps: the folds land, no step closes them, nothing is lost.
func TestResolveFoldsOfAnOldArchiveLandWithNoStep(t *testing.T) {
	r := archFoldResolve([]*pb_common.TechCardPieceFold{archFold(archFoldA, archFlap, 1), archFold(archFoldB, archCuff, 1)}, nil, nil)
	require.Len(t, r.out.FoldPlan, 2)
	require.Empty(t, r.out.Holes)
	require.Equal(t, [][]string{nil, nil}, archFoldKeys(r))
	require.Equal(t, 2, r.out.Counters[techcardarchive.EntityFold].Imported)
	require.Zero(t, r.out.Counters[techcardarchive.EntityFold].Skipped)
}

func TestResolveFoldsDropsARowWithALineAndItsLinksGo(t *testing.T) {
	broken := archFold(archFoldC, archFlap, 3)
	broken.Line = broken.Line[:1]
	r := archFoldResolve([]*pb_common.TechCardPieceFold{
		archFold(strings.ToLower(archFoldA), archFlap, 1),
		archFold(archFoldA, archFlap, 2),                     // the same key again
		archFold(archFoldB, "01JRSTGONE0000000000000000", 1), // on a piece the card does not have
		broken,
		archFold("01JABCDEFGHJKMNPQRSTVWXYD4", archFlap, 1), // the same (piece, seq) again
	}, []string{archFoldA, archFoldB}, []string{archFoldC})

	require.Len(t, r.out.FoldPlan, 1)
	require.Equal(t, archFoldA, r.out.FoldPlan[0].FoldKey, "the key is normalised")
	require.Equal(t, [][]string{{archFoldA}, nil}, archFoldKeys(r), "a dropped fold takes its links with it")
	archFoldLine(t, r, "fold_key="+archFoldA+" index=1", techcardarchive.ReasonArchiveRowInvalid)
	archFoldLine(t, r, "fold_key="+archFoldB, techcardarchive.ReasonFoldPieceMissing)
	archFoldLine(t, r, "fold_key="+archFoldC, techcardarchive.ReasonArchiveRowInvalid)
	archFoldLine(t, r, "fold_key=01JABCDEFGHJKMNPQRSTVWXYD4", techcardarchive.ReasonArchiveRowInvalid)
	require.Equal(t, 1, r.out.Counters[techcardarchive.EntityFold].Imported)
	require.Equal(t, 4, r.out.Counters[techcardarchive.EntityFold].Skipped)
}

func TestResolveFoldsKeepsTheStaleVerdict(t *testing.T) {
	stale := archFold(archFoldA, archFlap, 1)
	stale.Stale = true
	r := archFoldResolve([]*pb_common.TechCardPieceFold{stale, archFold(archFoldB, archFlap, 2)}, nil)
	require.Equal(t, map[string]bool{archFoldA: true}, r.out.FoldStale)
}

// A hand-made or damaged card.json: the rule is held here too, softly. The refusal would be the
// store's, and the import never fails for a fold.
func TestResolveFoldsCutsWhatBreaksTheRule(t *testing.T) {
	r := archFoldResolve([]*pb_common.TechCardPieceFold{
		archFold(archFoldA, archFlap, 1), archFold(archFoldB, archFlap, 2), archFold(archFoldC, archCuff, 1),
	}, []string{archFoldB}, []string{archFoldA}, []string{archFoldC}, []string{archFoldC})
	require.Equal(t, [][]string{{archFoldB}, nil, {archFoldC}, nil}, archFoldKeys(r))
	archFoldLine(t, r, "fold_key="+archFoldA+" step=2", techcardarchive.ReasonFoldStepConflict)
	archFoldLine(t, r, "fold_key="+archFoldC+" step=4", techcardarchive.ReasonFoldStepConflict)
	require.Equal(t, 3, r.out.Counters[techcardarchive.EntityFold].Imported, "a cut link is not a lost fold")
}
