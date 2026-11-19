package techcard

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

const (
	authoredFoldFirst  = "01JABCDEFGHJKMNPQRSTVWXYZB"
	authoredFoldSecond = "01JABCDEFGHJKMNPQRSTVWXYZC"
)

func authoredFoldFixture() (*entity.TechCard, entity.SavedPatternRowIdentity, []entity.RollGoodsLine) {
	card, anchor, lines := completeSourceFixture()
	card.SizeIds = []int{58, 59}
	card.Operations[0].FoldKeys = []string{authoredFoldFirst}
	card.Operations[1].FoldKeys = []string{authoredFoldSecond}
	ts := time.Date(2026, 11, 17, 13, 12, 11, 0, time.UTC)
	fold := entity.TechCardPieceFold{
		TechCardPieceFoldInput: entity.TechCardPieceFoldInput{
			FoldKey: authoredFoldFirst, PieceLineKey: completeSourceMainPiece, Seq: 1,
			Kind: entity.TechCardPieceFoldInward, Source: entity.TechCardPieceFoldSourceOwner,
			Line:   []entity.TechCardSeamSample{{U: .25, V: .1}, {U: .25, V: .9}},
			FlipAt: entity.TechCardSeamSample{U: .75, V: .5}, ContourSig: "#dxf/front",
			AnchoredSize: "S", Note: "Press the first turn",
		},
		TechCardId: card.Id, SourceFingerprint: strings.Repeat("b", 64),
		CreatedBy: "owner", UpdatedBy: "owner", CreatedAt: ts, UpdatedAt: ts,
	}
	second := fold
	second.FoldKey, second.Seq = authoredFoldSecond, 2
	card.PieceFolds = []entity.TechCardPieceFold{second, fold}
	return card, anchor, lines
}

func TestSavedAssemblyAuthoredFoldSourceSealsExactOwnedRowsAtCurrentRank(t *testing.T) {
	card, anchor, lines := authoredFoldFixture()
	base, err := buildCheckedSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock, "")
	require.NoError(t, err)
	sizes := map[int]string{58: "M", 59: "L"}
	request := entity.SavedAssemblySnapshotRequest{SelectedSizeID: 58}
	source, err := buildCheckedSavedAssemblyAuthoredFoldSourceV1(card, base, sizes, request)
	require.NoError(t, err)
	require.Equal(t, base.SourceSHA256, source.CompleteSourceSHA256)
	require.Equal(t, "M", source.SelectedSizeToken)
	require.Equal(t, []entity.SavedAssemblyAuthoredFoldV1{
		{OperationNumber: 10, OperationOrder: 0, Fold: card.PieceFolds[1]},
		{OperationNumber: 20, OperationOrder: 1, Fold: card.PieceFolds[0]},
	}, source.Folds)
	require.Equal(t, "S", source.Folds[0].Fold.AnchoredSize)
	require.Contains(t, string(source.CanonicalJSON), `"selectedSizeId":58`)
	require.Contains(t, string(source.CanonicalJSON), `"sourceFingerprint":"`+card.PieceFolds[1].SourceFingerprint+`"`)
	require.NotContains(t, string(source.CanonicalJSON), `"canonicalJson"`)
	require.NotContains(t, string(source.CanonicalJSON), `"sourceSha256"`)
	sum := sha256.Sum256(append([]byte("tech-card-saved-assembly-authored-fold-source/1\x00"), source.CanonicalJSON...))
	require.Equal(t, hex.EncodeToString(sum[:]), source.SourceSHA256)

	request.ExpectedAuthoredFoldSourceSHA256 = source.SourceSHA256
	replayed, err := buildCheckedSavedAssemblyAuthoredFoldSourceV1(card, base, sizes, request)
	require.NoError(t, err)
	require.Equal(t, source, replayed)
	request.SelectedSizeID = 59
	_, err = buildCheckedSavedAssemblyAuthoredFoldSourceV1(card, base, sizes, request)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
	request.SelectedSizeID = 58

	card.PieceFolds[1].FlipAt.U = .8
	sameClock, err := buildCheckedSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock, base.SourceSHA256)
	require.NoError(t, err)
	require.Equal(t, base.CanonicalJSON, sameClock.CanonicalJSON)
	_, err = buildCheckedSavedAssemblyAuthoredFoldSourceV1(card, sameClock, sizes, request)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
	_, err = buildCheckedSavedAssemblyCompleteSourceV1(card, anchor, lines,
		"asm:v1:0123456789abcdef0123456789abcdef:20", base.SourceSHA256)
	require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyMoved)
}

func TestSavedAssemblyAuthoredFoldSourceRejectsUnreadyRows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*entity.TechCard, map[int]string)
		reason string
	}{
		{"missing fold", func(card *entity.TechCard, _ map[int]string) { card.PieceFolds = card.PieceFolds[:1] }, string(entity.TechCardFoldLinkUnknown)},
		{"stale fold", func(card *entity.TechCard, _ map[int]string) { card.PieceFolds[1].Stale = true }, "stale_fold"},
		{"duplicate row", func(card *entity.TechCard, _ map[int]string) {
			card.PieceFolds = append(card.PieceFolds, card.PieceFolds[1])
		}, "duplicate_fold_row"},
		{"duplicate link", func(card *entity.TechCard, _ map[int]string) {
			card.Operations[0].FoldKeys = []string{authoredFoldFirst, authoredFoldFirst}
		}, string(entity.TechCardFoldLinkDuplicate)},
		{"second owner", func(card *entity.TechCard, _ map[int]string) {
			card.Operations[1].FoldKeys = []string{authoredFoldFirst}
		}, string(entity.TechCardFoldLinkTwoSteps)},
		{"foreign fold", func(card *entity.TechCard, _ map[int]string) { card.PieceFolds[1].TechCardId++ }, "invalid_fold_ownership"},
		{"unsupported per-size row", func(card *entity.TechCard, _ map[int]string) {
			card.PieceFolds[1].SizeId = sql.NullInt64{Int64: 58, Valid: true}
		}, "unsupported_fold_source"},
		{"unknown stored kind", func(card *entity.TechCard, _ map[int]string) { card.PieceFolds[1].Kind = "legacy" }, "invalid"},
		{"unknown selected size", func(_ *entity.TechCard, sizes map[int]string) { delete(sizes, 58) }, "missing_size"},
		{"ambiguous selected rank", func(_ *entity.TechCard, sizes map[int]string) { sizes[59] = "M" }, "ambiguous_size_token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card, anchor, lines := authoredFoldFixture()
			sizes := map[int]string{58: "M", 59: "L"}
			tc.edit(card, sizes)
			base, err := buildCheckedSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock, "")
			require.NoError(t, err)
			source, err := buildCheckedSavedAssemblyAuthoredFoldSourceV1(card, base, sizes, entity.SavedAssemblySnapshotRequest{SelectedSizeID: 58})
			require.Nil(t, source)
			require.ErrorIs(t, err, entity.ErrTechCardSavedAssemblyUnsupported)
			var violation *entity.ValidationError
			require.ErrorAs(t, err, &violation)
			require.Equal(t, tc.reason, violation.Reason)
		})
	}
}

func TestSavedAssemblyAuthoredFoldSourceSealsExplicitEmptyOwnedRows(t *testing.T) {
	card, anchor, lines := authoredFoldFixture()
	card.Operations[0].FoldKeys, card.Operations[1].FoldKeys = nil, nil
	card.PieceFolds[0].Stale = true
	base, err := buildCheckedSavedAssemblyCompleteSourceV1(card, anchor, lines, completeSourceClock, "")
	require.NoError(t, err)
	source, err := buildCheckedSavedAssemblyAuthoredFoldSourceV1(card, base, map[int]string{58: "M"}, entity.SavedAssemblySnapshotRequest{SelectedSizeID: 58})
	require.NoError(t, err)
	require.Empty(t, source.Folds)
	require.Contains(t, string(source.CanonicalJSON), `"folds":[]`)
}

func TestSavedAssemblyAuthoredFoldSourceRequestIsOptInAndRankExplicit(t *testing.T) {
	_, anchor, _ := authoredFoldFixture()
	in := entity.SavedAssemblySnapshotRequest{
		ExpectedRow: anchor, Profile: entity.SavedAssemblySnapshotCompleteSourceWithAuthoredFoldsV1,
	}
	require.ErrorContains(t, validateSavedAssemblySnapshotRequest(in), "selected_size_id")
	in.SelectedSizeID = 58
	in.ExpectedAuthoredFoldSourceSHA256 = "invalid"
	require.ErrorContains(t, validateSavedAssemblySnapshotRequest(in), "expected_authored_fold_source_sha256")
	in.ExpectedAuthoredFoldSourceSHA256 = strings.Repeat("a", 64)
	require.NoError(t, validateSavedAssemblySnapshotRequest(in))
	in.Profile = entity.SavedAssemblySnapshotCompleteSourceV1
	require.ErrorContains(t, validateSavedAssemblySnapshotRequest(in), "select the authored-fold profile")
}
