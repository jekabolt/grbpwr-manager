package admin

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	cutLineKey   = "01JABCDEFGHJKMNPQRSTVWXYL1"
	cutFabricKey = "01JABCDEFGHJKMNPQRSTVWXYF1"
	cutStripA    = "01JABCDEFGHJKMNPQRSTVWXYS1"
	cutStripB    = "01JABCDEFGHJKMNPQRSTVWXYS2"
	cutPieceKey  = "01JABCDEFGHJKMNPQRSTVWXYP1"
)

func cutPbDec(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

func cutPbRow(key string) *pb_common.TechCardCutStrip {
	return &pb_common.TechCardCutStrip{
		LineKey: key, BomLineKey: cutLineKey, LengthMode: entity.CutStripModeFromSeam,
		FitPct: cutPbDec("90"), OverlapMm: cutPbDec("20"), WidthMm: cutPbDec("30"), Qty: 1,
		Sizes: []*pb_common.TechCardCutStripSize{{SizeId: 1, SeamMm: cutPbDec("700")}},
	}
}

func cutArchiveResolve(rows []*pb_common.TechCardCutStrip, units map[int64]string, steps ...[]string) *tcimpResolver {
	ops := make([]*pb_common.TechCardOperation, 0, len(steps))
	for _, keys := range steps {
		ops = append(ops, &pb_common.TechCardOperation{InputKeys: slices.Clone(keys), PieceLineKeys: slices.Clone(keys)})
	}
	insert := &pb_common.TechCardInsert{
		BomItems: []*pb_common.TechCardBomItem{
			{
				LineKey: cutLineKey, Name: "rubber", Unit: "m", MaterialId: 200,
				Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE,
			},
			{
				LineKey: cutFabricKey, Name: "shell", Unit: "m",
				Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
				Purpose: pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_MAIN.Enum(),
			},
		},
		Pieces:     []*pb_common.TechCardPiece{{LineKey: cutPieceKey}},
		Operations: ops,
		CutStrips:  rows,
	}
	r := &tcimpResolver{
		out: &resolvedTechCardImport{
			Counters: techcardarchive.NewCounters(),
			Insert:   insert,
			Card:     &pb_common.TechCard{TechCard: insert},
		},
		holeSeen:     map[string]bool{},
		articleUnits: units,
	}
	r.resolveCutStrips()
	return r
}

func cutArchiveLine(t *testing.T, r *tcimpResolver, ref string, reason techcardarchive.Reason) {
	t.Helper()
	for _, h := range r.out.Holes {
		if h.Entity == techcardarchive.EntityCutStrip && h.Ref == ref && h.Reason == reason {
			return
		}
	}
	t.Fatalf("no %s line for %s in %+v", reason, ref, r.out.Holes)
}

func TestResolveCutStripsOnACardWithNoneSaysNothing(t *testing.T) {
	r := cutArchiveResolve(nil, nil, []string{cutPieceKey})
	require.Empty(t, r.out.Holes)
	_, counted := r.out.Counters[techcardarchive.EntityCutStrip]
	require.False(t, counted, "the counter appears only when the archive carries strips")
}

func TestResolveCutStripsKeepsAValidRowAndItsStep(t *testing.T) {
	r := cutArchiveResolve([]*pb_common.TechCardCutStrip{cutPbRow(cutStripA)}, map[int64]string{200: "m"}, []string{cutPieceKey, cutStripA})
	require.Empty(t, r.out.Holes)
	require.Len(t, r.out.Insert.GetCutStrips(), 1)
	require.Equal(t, []string{cutPieceKey, cutStripA}, r.out.Insert.GetOperations()[0].GetInputKeys())
	require.Equal(t, 1, r.out.Counters[techcardarchive.EntityCutStrip].Imported)
	require.Zero(t, r.out.Counters[techcardarchive.EntityCutStrip].Skipped)
}

// A size this base lacks was cleared by resolveSizes (size_id 0), and a from_seam row with no size left is
// still a valid strip of unknown length.
func TestResolveCutStripsDropsARowOfASizeTheBaseLacks(t *testing.T) {
	row := cutPbRow(cutStripA)
	row.Sizes = append(row.Sizes, &pb_common.TechCardCutStripSize{SizeId: 0, SeamMm: cutPbDec("800")})
	r := cutArchiveResolve([]*pb_common.TechCardCutStrip{row}, nil)
	require.Empty(t, r.out.Holes)
	require.Len(t, r.out.Insert.GetCutStrips()[0].GetSizes(), 1)
}

func TestResolveCutStripsDropsWhatASaveWouldRefuse(t *testing.T) {
	invalid := cutPbRow("01JABCDEFGHJKMNPQRSTVWXYS3")
	invalid.LengthMode = "by_eye"
	gone := cutPbRow("01JABCDEFGHJKMNPQRSTVWXYS4")
	gone.BomLineKey = "01JABCDEFGHJKMNPQRSTVWXYZZ"
	onMain := cutPbRow("01JABCDEFGHJKMNPQRSTVWXYS5")
	onMain.BomLineKey = cutFabricKey

	r := cutArchiveResolve([]*pb_common.TechCardCutStrip{
		cutPbRow(cutStripA),
		cutPbRow(cutStripA), // the same key again
		cutPbRow(cutPieceKey),
		invalid, gone, onMain,
	}, map[int64]string{200: "m"},
		[]string{cutStripA, gone.GetLineKey()},
		[]string{invalid.GetLineKey(), onMain.GetLineKey(), cutPieceKey})

	require.Len(t, r.out.Insert.GetCutStrips(), 1)
	cutArchiveLine(t, r, tcimpCutStripRef(cutStripA)+" index=1", techcardarchive.ReasonArchiveRowInvalid)
	cutArchiveLine(t, r, tcimpCutStripRef(invalid.GetLineKey()), techcardarchive.ReasonArchiveRowInvalid)
	cutArchiveLine(t, r, tcimpCutStripRef(gone.GetLineKey()), techcardarchive.ReasonCutStripLineUnusable)
	cutArchiveLine(t, r, tcimpCutStripRef(onMain.GetLineKey()), techcardarchive.ReasonCutStripLineUnusable)
	require.Equal(t, 1, r.out.Counters[techcardarchive.EntityCutStrip].Imported)
	require.Equal(t, 5, r.out.Counters[techcardarchive.EntityCutStrip].Skipped)

	ops := r.out.Insert.GetOperations()
	require.Equal(t, []string{cutStripA}, ops[0].GetInputKeys(), "a dropped strip takes its input with it")
	require.Equal(t, []string{cutStripA}, ops[0].GetPieceLineKeys())
	require.Empty(t, ops[1].GetInputKeys(), "the key of a dropped row is gone, even the one a piece also has")
}

// The article on this base decides. The archive's line says metres, the target article counts pieces: the
// store would refuse the whole card, so the strip is dropped with a line.
func TestResolveCutStripsReadsTheTargetArticlesUnit(t *testing.T) {
	r := cutArchiveResolve([]*pb_common.TechCardCutStrip{cutPbRow(cutStripA)}, map[int64]string{200: "pcs"}, []string{cutStripA})
	require.Empty(t, r.out.Insert.GetCutStrips())
	cutArchiveLine(t, r, tcimpCutStripRef(cutStripA), techcardarchive.ReasonCutStripLineUnusable)
	require.Empty(t, r.out.Insert.GetOperations()[0].GetInputKeys())
}

func TestResolveCutStripsCapsACard(t *testing.T) {
	rows := make([]*pb_common.TechCardCutStrip, 0, entity.CutStripMaxPerCard+1)
	for i := 0; i <= entity.CutStripMaxPerCard; i++ {
		key := []byte("01JABCDEFGHJKMNPQRSTVW0000")
		key[23], key[24], key[25] = byte('0'+i/100), byte('0'+i/10%10), byte('0'+i%10)
		rows = append(rows, cutPbRow(string(key)))
	}
	r := cutArchiveResolve(rows, nil)
	require.Len(t, r.out.Insert.GetCutStrips(), entity.CutStripMaxPerCard)
	require.Equal(t, 1, r.out.Counters[techcardarchive.EntityCutStrip].Skipped)
}

func cutStoredCard(strips ...string) *entity.TechCard {
	card := &entity.TechCard{}
	for _, k := range strips {
		card.CutStrips = append(card.CutStrips, entity.TechCardCutStrip{LineKey: k, BomLineKey: cutLineKey})
	}
	return card
}

func cutRequireInUse(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "cut_strip_in_use")
}

func TestCutStripsStoredGate(t *testing.T) {
	line := []*pb_common.TechCardBomItem{{LineKey: cutLineKey}}
	takes := []*pb_common.TechCardOperation{{InputKeys: []string{cutPieceKey, cutStripA}}}

	t.Run("an aware client unchecking a used strip", func(t *testing.T) {
		pb := &pb_common.TechCardInsert{CutStripsAware: true, BomItems: line, Operations: takes}
		cutRequireInUse(t, cutStripsStoredGate(pb, cutStoredCard(cutStripA)))
	})
	t.Run("an older client removing the line of a used strip", func(t *testing.T) {
		pb := &pb_common.TechCardInsert{Operations: takes}
		cutRequireInUse(t, cutStripsStoredGate(pb, cutStoredCard(cutStripA)))
	})
	t.Run("keys match case-insensitively, the way the columns do", func(t *testing.T) {
		pb := &pb_common.TechCardInsert{
			CutStripsAware: true, BomItems: line,
			Operations: []*pb_common.TechCardOperation{{InputKeys: []string{"01jabcdefghjkmnpqrstvwxys1"}}},
		}
		cutRequireInUse(t, cutStripsStoredGate(pb, cutStoredCard(cutStripA)))
	})
	t.Run("the strip stays", func(t *testing.T) {
		pb := &pb_common.TechCardInsert{
			CutStripsAware: true, BomItems: line, Operations: takes,
			CutStrips: []*pb_common.TechCardCutStrip{cutPbRow(cutStripA)},
		}
		require.NoError(t, cutStripsStoredGate(pb, cutStoredCard(cutStripA)))
	})
	t.Run("an older client keeps the line: the strip stays by carry", func(t *testing.T) {
		pb := &pb_common.TechCardInsert{BomItems: line, Operations: takes}
		require.NoError(t, cutStripsStoredGate(pb, cutStoredCard(cutStripA)))
	})
	t.Run("an unused strip goes", func(t *testing.T) {
		pb := &pb_common.TechCardInsert{CutStripsAware: true, BomItems: line}
		require.NoError(t, cutStripsStoredGate(pb, cutStoredCard(cutStripA)))
	})
	t.Run("the step dropping it in the same save", func(t *testing.T) {
		pb := &pb_common.TechCardInsert{
			CutStripsAware: true, BomItems: line,
			Operations: []*pb_common.TechCardOperation{{InputKeys: []string{cutPieceKey}}},
		}
		require.NoError(t, cutStripsStoredGate(pb, cutStoredCard(cutStripA)))
	})
	t.Run("a card with none", func(t *testing.T) {
		require.NoError(t, cutStripsStoredGate(&pb_common.TechCardInsert{}, &entity.TechCard{}))
		require.NoError(t, cutStripsStoredGate(&pb_common.TechCardInsert{}, nil))
	})
}

func TestStoredHasCutStripInputs(t *testing.T) {
	require.False(t, storedHasCutStripInputs(nil))
	card := &entity.TechCard{}
	card.Operations = []entity.TechCardOperation{{AssemblyInputs: []entity.OperationInput{{Kind: entity.AssemblyInputPiece, Key: "FR"}}}}
	require.False(t, storedHasCutStripInputs(card))
	card.Operations = append(card.Operations, entity.TechCardOperation{
		AssemblyInputs: []entity.OperationInput{{Kind: entity.AssemblyInputStrip, Key: cutStripA}},
	})
	require.True(t, storedHasCutStripInputs(card))
}

func TestCutStripsChangedComparesByValue(t *testing.T) {
	row := func(fit, seam string) entity.TechCardCutStrip {
		return entity.TechCardCutStrip{
			LineKey: cutStripA, BomLineKey: cutLineKey, LengthMode: entity.CutStripModeFromSeam,
			FitPct: decimal.RequireFromString(fit), Qty: 1,
			Sizes: []entity.TechCardCutStripSize{{SizeId: 1, SeamMm: decimal.NewNullDecimal(decimal.RequireFromString(seam))}},
		}
	}
	stored := []entity.TechCardCutStrip{row("100.00", "700.00")}
	require.False(t, cutStripsChanged(stored, &entity.TechCardInsert{CutStrips: nil}), "an older client said nothing")
	require.False(t, cutStripsChanged(stored, &entity.TechCardInsert{CutStripsAware: true, CutStrips: []entity.TechCardCutStrip{row("100", "700")}}))
	require.True(t, cutStripsChanged(stored, &entity.TechCardInsert{CutStripsAware: true, CutStrips: []entity.TechCardCutStrip{row("100", "701")}}))
	require.True(t, cutStripsChanged(stored, &entity.TechCardInsert{CutStripsAware: true}))
}

func TestCloneCutStripsDropsTheSeamNotTheLength(t *testing.T) {
	row := cutPbRow(cutStripA)
	row.SeamKey = "SEAM"
	got := cloneCutStripsWithoutSeams([]*pb_common.TechCardCutStrip{row}, nil)
	require.Empty(t, got[0].GetSeamKey())
	require.Equal(t, "700", got[0].GetSizes()[0].GetSeamMm().GetValue())
	require.Equal(t, "SEAM", row.GetSeamKey(), "the read card is not edited")
}

func TestCloneCutStripsDropsTheLengthOfAStaleSeam(t *testing.T) {
	row := cutPbRow(cutStripA)
	row.SeamKey = "SEAM"
	got := cloneCutStripsWithoutSeams([]*pb_common.TechCardCutStrip{row},
		staleSeamCutStrips([]entity.TechCardCutStrip{{LineKey: cutStripA, SeamStale: true}}))
	require.Empty(t, got[0].GetSeamKey())
	require.Nil(t, got[0].GetSizes()[0].GetSeamMm(), "a stale seam's length is not known on the clone either")
	require.Equal(t, "700", row.GetSizes()[0].GetSeamMm().GetValue(), "the read card is not edited")
}

func cutSnapshotStrip() entity.TechCardCutStrip {
	return entity.TechCardCutStrip{
		LineKey: cutStripA, BomLineKey: cutLineKey, LengthMode: entity.CutStripModeOneLength,
		FitPct: decimal.NewFromInt(100), OverlapMm: decimal.Zero, WidthMm: decimal.NewFromInt(30), Qty: 1,
	}
}

// A snapshot taken before the strips does not know them. Restoring it says nothing about strips, so the
// card keeps the ones it has today, and the whole-card save does not read as «delete them all».
func TestRestoreOfAnOldSnapshotLeavesStripsAlone(t *testing.T) {
	row := restoreSnapshotRow(t, restoreCardAtV6())
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(row.Snapshot), &top))
	_, had := top[snapshotCutStripsField]
	require.True(t, had, "a snapshot of today states the list even when it is empty")
	delete(top, snapshotCutStripsField)
	old, err := json.Marshal(top)
	require.NoError(t, err)
	row.Snapshot = string(old)

	written := restoreCapture(t, row)
	require.False(t, written.CutStripsAware)
	require.Empty(t, written.CutStrips)
}

// A snapshot of today with no strips means the card had none then, and the card comes back to that.
func TestRestoreOfASnapshotWithNoStripsEmptiesTheList(t *testing.T) {
	written := restoreCapture(t, restoreSnapshotRow(t, restoreCardAtV6()))
	require.True(t, written.CutStripsAware)
	require.Empty(t, written.CutStrips)
}
