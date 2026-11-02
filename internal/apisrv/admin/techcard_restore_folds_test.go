package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// ШАГ СГИБА ПРИ «ВЕРНУТЬ v.N». Таблица сгибов восстановлением не трогается: снимок не возвращает её
// строки. Ключи снимка возвращаются вместе со своими шагами и режутся по сгибам карты СЕЙЧАС, потому
// что отказ сделал бы снимок невосстановимым. Что вышло за правило, то просто без шага.

const (
	restoreFoldA = "01JABCDEFGHJKMNPQRSTVWXYA1"
	restoreFoldB = "01JABCDEFGHJKMNPQRSTVWXYB2"
	restoreFoldC = "01JABCDEFGHJKMNPQRSTVWXYC3"
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
	restoreOperationFoldKeys(in, stored)
	require.Equal(t, []string{restoreFoldA}, in.Operations[0].GetFoldKeys())
	require.Empty(t, in.Operations[1].GetFoldKeys(), "reversed against today's seq, and C is gone")
	require.Empty(t, in.Operations[2].GetFoldKeys())
}

func TestRestoreKeepsFoldLinksThatStillHold(t *testing.T) {
	stored := &entity.TechCard{PieceFolds: []entity.TechCardPieceFold{
		restoreFoldRow(restoreFoldA, keyFront, 1), restoreFoldRow(restoreFoldB, keyFront, 2),
	}}
	in := restoreFoldPayload([]string{restoreFoldA}, []string{restoreFoldB})
	restoreOperationFoldKeys(in, stored)
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
