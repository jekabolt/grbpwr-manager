package admin

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// СГИБЫ ПРИ «ВЕРНУТЬ v.N». Сохранение, удалившее деталь, удалило и её сгибы (решение 11.11), а снимок
// их помнит (pieceFolds). Restore кладёт в сохранение карты строки сгибов снимка
// (TechCardInsert.RestorePieceFolds), и стор в той же транзакции, ДО связей шагов, заново пишет строки
// тех деталей восстановленной карты, у которых сегодня нет ни одного сгиба. Деталь, у которой сгибы
// есть, остаётся как есть: ни перезаписи, ни слияния. Сгиб детали, которой в карте нет, и строка, что не
// проходит проверку, пропускаются, restore не падает. Ключи шагов снимка режутся по «сгибы карты сейчас +
// то, что restore вернёт»: что вышло за правило, то просто без шага. Сама запись — живая база
// (TestTechCardRestoreBringsBackFoldsOfARestoredPiece).

const (
	restoreFoldA = "01JABCDEFGHJKMNPQRSTVWXYA1"
	restoreFoldB = "01JABCDEFGHJKMNPQRSTVWXYB2"
	restoreFoldC = "01JABCDEFGHJKMNPQRSTVWXYC3"
	keyGhost     = "01JRSTGHOST000000000000000" // a piece neither card has
)

func restoreFoldRow(key, piece string, seq int) entity.TechCardPieceFold {
	return entity.TechCardPieceFold{TechCardPieceFoldInput: entity.TechCardPieceFoldInput{
		FoldKey: key, PieceLineKey: piece, Seq: seq,
	}}
}

func restoreFoldPayload(steps ...[]string) *pb_common.TechCardInsert {
	in := &pb_common.TechCardInsert{}
	for _, keys := range steps {
		in.Operations = append(in.Operations, &pb_common.TechCardOperation{FoldKeys: keys})
	}
	return in
}

func TestRestoreCutsFoldLinksToTodaysFolds(t *testing.T) {
	stored := &entity.TechCard{PieceFolds: []entity.TechCardPieceFold{
		restoreFoldRow(restoreFoldA, keyFront, 2), // renumbered since the snapshot: now after B
		restoreFoldRow(restoreFoldB, keyFront, 1),
	}}
	in := restoreFoldPayload([]string{restoreFoldA}, []string{restoreFoldB, restoreFoldC}, nil)
	restoreOperationFoldKeys(in, stored, nil)
	require.Equal(t, []string{restoreFoldA}, in.Operations[0].GetFoldKeys())
	require.Empty(t, in.Operations[1].GetFoldKeys(), "reversed against today's seq, and C is gone")
	require.Empty(t, in.Operations[2].GetFoldKeys())
}

func TestRestoreKeepsFoldLinksThatStillHold(t *testing.T) {
	stored := &entity.TechCard{PieceFolds: []entity.TechCardPieceFold{
		restoreFoldRow(restoreFoldA, keyFront, 1), restoreFoldRow(restoreFoldB, keyFront, 2),
	}}
	in := restoreFoldPayload([]string{restoreFoldA}, []string{restoreFoldB})
	restoreOperationFoldKeys(in, stored, nil)
	require.Equal(t, []string{restoreFoldA}, in.Operations[0].GetFoldKeys())
	require.Equal(t, []string{restoreFoldB}, in.Operations[1].GetFoldKeys())
}

// Through the handler: the payload the store gets declares the flag and names only what holds. The
// snapshot's card says its steps closed folds; today's card holds none of them.
func TestRestoreSavesWithTheFlagAndWithoutGoneFolds(t *testing.T) {
	v6 := restoreCardAtV6()
	v6.Operations[0].FoldKeys = []string{restoreFoldA}
	written := restoreCapture(t, restoreSnapshotRow(t, v6))
	require.Empty(t, written.Operations[0].FoldKeys, "a fold that is gone today cannot be named")
}

// An older snapshot knows nothing of fold steps: the card comes back flat, and the flag lets it through
// over a card whose steps close folds today.
func TestRestoreOfAnOldSnapshotPassesTheFoldGate(t *testing.T) {
	written := restoreCapture(t, restoreSnapshotRow(t, restoreCardAtV6()))
	for i := range written.Operations {
		require.Empty(t, written.Operations[i].FoldKeys)
	}
}

// A restore asks the store to cut what its own write would refuse: only the store sees the order the
// FUSING reconciliation leaves.
func TestRestoreMakesTheStoreCutFoldLinks(t *testing.T) {
	written := restoreCapture(t, restoreSnapshotRow(t, restoreCardAtV6()))
	require.True(t, written.RestoreFolds)
}

// restoreFoldFull is a fold that validates and carries every optional field, as a snapshot holds it.
func restoreFoldFull(key, piece string, seq int) entity.TechCardPieceFold {
	return entity.TechCardPieceFold{
		TechCardPieceFoldInput: entity.TechCardPieceFoldInput{
			FoldKey: key, PieceLineKey: piece, Seq: seq,
			Kind: entity.TechCardPieceFoldPleatLeft, Source: entity.TechCardPieceFoldSourceDXF,
			Line:     []entity.TechCardSeamSample{{U: 0, V: 0.1}, {U: 1, V: 0.1}},
			FlipAt:   entity.TechCardSeamSample{U: 0.5, V: 0.05},
			EdgeHint: "hem",
			EdgeSamples: []entity.TechCardSeamSample{
				{U: 0, V: 0}, {U: 0.25, V: 0}, {U: 0.5, V: 0}, {U: 0.75, V: 0}, {U: 1, V: 0},
			},
			OffsetMm: 35, ContourSig: "sig-v6", AnchoredSize: "M", Note: "turn-up at v6",
		},
		TechCardId: restoreCardID, SourceFingerprint: "fp-v6", CreatedBy: "drafter",
	}
}

// restoreCardWithoutCollar is today's card after the collar's sheet was deleted: the piece, its alias
// and its step went, and its folds with them.
func restoreCardWithoutCollar() *entity.TechCard {
	tc := restoreCardNow()
	tc.Pieces = tc.Pieces[:1]
	tc.PieceDxfAliases = nil
	tc.Operations = tc.Operations[:1]
	return tc
}

// restoreCaptureOver is restoreCapture over a chosen card of today.
func restoreCaptureOver(t *testing.T, snapshot *entity.TechCardSnapshot, now *entity.TechCard) *entity.TechCardInsert {
	t.Helper()
	restoreHosts(t)
	rig := newRestoreRig(t)
	rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).Return(snapshot, nil)
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(now, nil)
	var written *entity.TechCardInsert
	rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), now.LockVersion).
		Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) { written = tc }).
		Return(nil, entity.ErrTechCardConflict)
	_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(int32(now.LockVersion)))
	require.Equal(t, codes.Aborted, status.Code(err), "the payload must reach the store: %v", err)
	require.NotNil(t, written)
	return written
}

func restoreFoldKeysOf(folds []entity.TechCardPieceFoldInput) []string {
	out := make([]string, 0, len(folds))
	for _, f := range folds {
		out = append(out, f.FoldKey)
	}
	return out
}

// The real gesture: the collar's sheet was deleted (piece, folds and links gone), then v6 is restored.
// The collar comes back with both folds, every field and the stale verdict as the snapshot has them,
// and its step keeps its links to them.
func TestRestoreBringsBackTheFoldsOfAPieceItBringsBack(t *testing.T) {
	v6 := restoreCardAtV6()
	staleA := restoreFoldFull(restoreFoldA, keyCollar, 1)
	staleA.Stale = true
	v6.PieceFolds = []entity.TechCardPieceFold{staleA, restoreFoldFull(restoreFoldB, keyCollar, 2)}
	v6.Operations[1].FoldKeys = []string{restoreFoldA, restoreFoldB}
	written := restoreCaptureOver(t, restoreSnapshotRow(t, v6), restoreCardWithoutCollar())

	require.NotNil(t, written.RestorePieceFolds)
	require.Equal(t, []string{restoreFoldA, restoreFoldB}, restoreFoldKeysOf(written.RestorePieceFolds.Folds))
	require.Equal(t, restoreFoldFull(restoreFoldA, keyCollar, 1).TechCardPieceFoldInput, written.RestorePieceFolds.Folds[0],
		"every field the row carries comes back")
	require.Equal(t, map[string]bool{restoreFoldA: true}, written.RestorePieceFolds.Stale)
	require.Equal(t, []string{restoreFoldA, restoreFoldB}, written.Operations[1].FoldKeys,
		"the step's links to the re-created folds survive the cut")
	require.True(t, written.RestoreFolds)
}

// The front was refolded since v6 (C instead of A): it keeps C and gets nothing from the snapshot, so
// the step that closed A loses that link. The collar holds no fold today: B comes back with its step.
func TestRestoreLeavesAPieceThatHoldsFoldsToday(t *testing.T) {
	v6 := restoreCardAtV6()
	v6.PieceFolds = []entity.TechCardPieceFold{
		restoreFoldFull(restoreFoldA, keyFront, 1), restoreFoldFull(restoreFoldB, keyCollar, 1),
	}
	v6.Operations[0].FoldKeys = []string{restoreFoldA}
	v6.Operations[1].FoldKeys = []string{restoreFoldB}
	now := restoreCardNow()
	now.PieceFolds = []entity.TechCardPieceFold{restoreFoldRow(restoreFoldC, keyFront, 1)}
	written := restoreCaptureOver(t, restoreSnapshotRow(t, v6), now)

	require.Empty(t, written.Operations[0].FoldKeys, "A is not re-created on a piece that holds folds")
	require.Equal(t, []string{restoreFoldB}, written.Operations[1].FoldKeys)
	plan := restoredPieceFoldsPlan(written.RestorePieceFolds, dto.ConvertEntityTechCardToPb(v6, dto.CostingFx{}).GetTechCard(), now)
	require.Equal(t, []string{restoreFoldB}, restoreFoldKeysOf(plan), "no overwrite, no merge on the front")
}

// A snapshot older than folds (or of a card that had none) has no pieceFolds: nothing comes back, the
// restore goes on as before.
func TestRestoreOfASnapshotWithoutFoldsBringsNone(t *testing.T) {
	v6 := restoreCardAtV6()
	v6.Operations[1].FoldKeys = []string{restoreFoldA}
	snap := restoreSnapshotRow(t, v6)
	require.NotContains(t, snap.Snapshot, `"pieceFolds"`)
	written := restoreCaptureOver(t, snap, restoreCardWithoutCollar())
	require.Nil(t, written.RestorePieceFolds)
	require.Empty(t, written.Operations[1].FoldKeys)
}

// A snapshot fold of a piece the restored card does not have is skipped, and so is its link; a row that
// does not validate is skipped too. Neither fails the restore.
func TestRestoreSkipsASnapshotFoldOfAPieceTheCardDoesNotHave(t *testing.T) {
	v6 := restoreCardAtV6()
	invalid := restoreFoldFull(restoreFoldC, keyCollar, 2)
	invalid.Kind = "" // reads back as UNKNOWN, which no write accepts
	v6.PieceFolds = []entity.TechCardPieceFold{
		restoreFoldFull(restoreFoldA, keyGhost, 1), restoreFoldFull(restoreFoldB, keyCollar, 1), invalid,
	}
	v6.Operations[0].FoldKeys = []string{restoreFoldA}
	v6.Operations[1].FoldKeys = []string{restoreFoldB, restoreFoldC}
	written := restoreCaptureOver(t, restoreSnapshotRow(t, v6), restoreCardWithoutCollar())

	require.Equal(t, []string{restoreFoldA, restoreFoldB}, restoreFoldKeysOf(written.RestorePieceFolds.Folds),
		"the invalid row never reaches the store; the ghost's row is the store's to skip")
	plan := restoredPieceFoldsPlan(written.RestorePieceFolds, dto.ConvertEntityTechCardToPb(v6, dto.CostingFx{}).GetTechCard(),
		restoreCardWithoutCollar())
	require.Equal(t, []string{restoreFoldB}, restoreFoldKeysOf(plan))
	require.Empty(t, written.Operations[0].FoldKeys)
	require.Equal(t, []string{restoreFoldB}, written.Operations[1].FoldKeys)
}

// The prediction plans after the save's own drop, as the store does: a key that today sits on a piece
// the restore deletes is free again, a key on a piece that stays is not.
func TestRestorePredictsTheStorePlanAfterTheDrop(t *testing.T) {
	back := &entity.TechCardPieceFoldsRestore{Folds: []entity.TechCardPieceFoldInput{
		restoreFoldFull(restoreFoldA, keyFront, 1).TechCardPieceFoldInput,
	}}
	stored := &entity.TechCard{
		TechCardInsert: entity.TechCardInsert{Pieces: []entity.TechCardPiece{{LineKey: keyFront}, {LineKey: keyCollar}}},
		PieceFolds:     []entity.TechCardPieceFold{restoreFoldRow(restoreFoldA, keyCollar, 1)},
	}
	frontOnly := &pb_common.TechCardInsert{Pieces: []*pb_common.TechCardPiece{{LineKey: keyFront}}}
	require.Equal(t, []string{restoreFoldA}, restoreFoldKeysOf(restoredPieceFoldsPlan(back, frontOnly, stored)))
	both := &pb_common.TechCardInsert{Pieces: []*pb_common.TechCardPiece{{LineKey: keyFront}, {LineKey: keyCollar}}}
	require.Empty(t, restoredPieceFoldsPlan(back, both, stored))
}
