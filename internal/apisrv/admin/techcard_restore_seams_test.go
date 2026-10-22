package admin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// ШВЫ ПРИ «ВЕРНУТЬ v.N». Швы — боковая таблица (tech_card_seam), full-replace карты их не пишет.
// Restore кладёт в сохранение карты список швов снимка (TechCardInsert.RestoreSeams), и стор в той
// же транзакции приводит к нему таблицу: upsert тех, что в снимке, delete остальных. Здесь — решение
// хендлера: что уходит в стор. Сама запись — UpdateTechCardTx → restoreTechCardSeamsTx (живая база).

const (
	seamKeyKept  = "01JRSTSEAMKEPT000000000000"
	seamKeyStale = "01JRSTSEAMSTAXE00000000000"
)

func restoreSeamAnchor(piece string) *pb_common.TechCardSeamAnchor {
	return &pb_common.TechCardSeamAnchor{
		PieceLineKey: piece,
		Samples: []*pb_common.TechCardSeamSample{
			{U: 0, V: 0}, {U: 0.25, V: 0.1}, {U: 0.5, V: 0.2}, {U: 0.75, V: 0.3}, {U: 1, V: 0.4},
		},
		PerimShare: 0.12, LenMm: 142.5, RangeFrom: 0.1, RangeTo: 0.9,
	}
}

func restoreSeams(t *testing.T) []entity.TechCardSeam {
	t.Helper()
	seam := func(key string) *pb_common.TechCardSeam {
		return &pb_common.TechCardSeam{
			SeamKey:   key,
			Status:    pb_common.TechCardSeamStatus_TECH_CARD_SEAM_STATUS_CONFIRMED,
			Kind:      pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_EDGE,
			Direction: pb_common.TechCardSeamDirection_TECH_CARD_SEAM_DIRECTION_REVERSED,
			Source:    pb_common.TechCardSeamSource_TECH_CARD_SEAM_SOURCE_DOLL,
			SideA:     &pb_common.TechCardSeamSide{Parts: []*pb_common.TechCardSeamAnchor{restoreSeamAnchor(keyFront)}},
			SideB:     &pb_common.TechCardSeamSide{Parts: []*pb_common.TechCardSeamAnchor{restoreSeamAnchor(keyCollar)}},
			Note:      "at v6",
		}
	}
	in, err := dto.TechCardSeamsWriteFromPb(restoreCardID, []*pb_common.TechCardSeam{seam(seamKeyKept), seam(seamKeyStale)}, "author")
	require.NoError(t, err)
	return []entity.TechCardSeam{
		{TechCardSeamInput: in.Seams[0], TechCardId: restoreCardID},
		{TechCardSeamInput: in.Seams[1], TechCardId: restoreCardID, Stale: true},
	}
}

// restoreCapture runs a restore that stops in the store (Aborted) and returns what reached it.
func restoreCapture(t *testing.T, snapshot *entity.TechCardSnapshot) *entity.TechCardInsert {
	t.Helper()
	restoreHosts(t)
	rig := newRestoreRig(t)
	rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).Return(snapshot, nil)
	rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(restoreCardNow(), nil)
	var written *entity.TechCardInsert
	rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
		mock.AnythingOfType("*entity.TechCardInsert"), 3).
		Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) { written = tc }).
		Return(nil, entity.ErrTechCardConflict)
	_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(3))
	require.Equal(t, codes.Aborted, status.Code(err), "the payload must reach the store: %v", err)
	require.NotNil(t, written)
	return written
}

// A snapshot with seams: the save carries exactly the snapshot's list, keys canonical, the stale
// verdict kept (the store writes it with a fingerprint that never matches), in the restorer's name.
func TestRestoreBringsTheSnapshotSeamsIntoTheSameSave(t *testing.T) {
	v6 := restoreCardAtV6()
	v6.Seams = restoreSeams(t)
	written := restoreCapture(t, restoreSnapshotRow(t, v6))

	require.NotNil(t, written.RestoreSeams, "a snapshot naming its seams must bring them back")
	require.Len(t, written.RestoreSeams.Seams, 2)
	require.Equal(t, seamKeyKept, written.RestoreSeams.Seams[0].SeamKey)
	require.Equal(t, seamKeyStale, written.RestoreSeams.Seams[1].SeamKey)
	require.Equal(t, "at v6", written.RestoreSeams.Seams[0].Note)
	require.Equal(t, keyFront, written.RestoreSeams.Seams[0].SideA[0].PieceLineKey)
	require.Equal(t, map[string]bool{seamKeyStale: true}, written.RestoreSeams.Stale,
		"a seam stale in the snapshot must not come back as decided on the restored patterns")
	require.Equal(t, "restorer", written.UpdatedBy, "the seam rows' updated_by is the save's author")
}

// A snapshot of a card with NO seams says so (`"seams": []`): the restore deletes today's seams.
func TestRestoreOfASeamlessSnapshotClearsTheSeams(t *testing.T) {
	row := restoreSnapshotRow(t, restoreCardAtV6())
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(row.Snapshot), &top))
	require.JSONEq(t, `[]`, string(top["seams"]), "the snapshot writer must name an empty seam list")

	written := restoreCapture(t, row)
	require.NotNil(t, written.RestoreSeams, "an empty list is a list: the seams go")
	require.Empty(t, written.RestoreSeams.Seams)
}

// An OLD snapshot (written before the seams were frozen, no `seams` key): the seams are unknown, and
// the restore leaves tech_card_seam alone.
func TestRestoreOfAnOldSnapshotLeavesTheSeams(t *testing.T) {
	old, err := protojson.Marshal(dto.ConvertEntityTechCardToPb(restoreCardAtV6(), dto.CostingFx{}))
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(old, &top))
	_, has := top["seams"]
	require.False(t, has, "precondition: plain protojson omits an empty seam list")
	row := &entity.TechCardSnapshot{Id: restoreSnapshotID, TechCardId: restoreCardID, LockVersion: 2,
		Reason: "dxf-reupload", Snapshot: string(old)}

	written := restoreCapture(t, row)
	require.Nil(t, written.RestoreSeams, "a snapshot without the seams field must not touch the seams")
}

// The writer's `"seams": []` is still the same card for every reader of the snapshot.
func TestSnapshotWithEmptySeamsDecodesAsTheCard(t *testing.T) {
	card := restoreCardAtV6()
	blob, err := marshalTechCardSnapshot(card, dto.CostingFx{})
	require.NoError(t, err)
	var got pb_common.TechCard
	require.NoError(t, protojson.Unmarshal([]byte(blob), &got))
	want := dto.ConvertEntityTechCardToPb(card, dto.CostingFx{})
	wantJSON, err := protojson.Marshal(want)
	require.NoError(t, err)
	gotJSON, err := protojson.Marshal(&got)
	require.NoError(t, err)
	require.JSONEq(t, string(wantJSON), string(gotJSON))
}
