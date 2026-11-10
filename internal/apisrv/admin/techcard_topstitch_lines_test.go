package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	restoreTopThread = "01JABCDEFGHJKMNPQRSTVWXYD1"
	restoreTopFresh  = "01JABCDEFGHJKMNPQRSTVWXYD2"
	restoreTopStale  = "01JABCDEFGHJKMNPQRSTVWXYD3"
)

func TestTopstitchLinesWireGate(t *testing.T) {
	payload := &pb_common.TechCardInsert{Operations: []*pb_common.TechCardOperation{{
		TopstitchLines: []*pb_common.TechCardTopstitchLine{{LineKey: "line"}},
	}}}
	require.Error(t, topstitchLinesWireGate(payload))
	payload.TopstitchLinesAware = true
	require.NoError(t, topstitchLinesWireGate(payload))
}

func TestTopstitchLinesStoredGateProtectsFullReplace(t *testing.T) {
	stored := storedCard(entity.TechCardOperation{TopstitchLines: []entity.TechCardTopstitchLine{{LineKey: restoreTopFresh}}})
	wantFailedPrecondition(t, topstitchLinesStoredGate(&pb_common.TechCardInsert{}, stored),
		"unaware full replace over stored topstitch lines")
	require.NoError(t, topstitchLinesStoredGate(&pb_common.TechCardInsert{TopstitchLinesAware: true}, stored))
	require.NoError(t, topstitchLinesStoredGate(&pb_common.TechCardInsert{}, storedCard(entity.TechCardOperation{})))
}

func TestPre0464TopstitchCarryRequiresUnchangedOperationMapping(t *testing.T) {
	current := &pb_common.TechCardInsert{Operations: []*pb_common.TechCardOperation{{
		Note: "same step", TopstitchLines: []*pb_common.TechCardTopstitchLine{{LineKey: restoreTopFresh}},
	}}}
	frozen := proto.Clone(current).(*pb_common.TechCardInsert)
	frozen.Operations[0].TopstitchLines = nil
	require.True(t, sameOperationsIgnoringTopstitch(frozen, current))

	frozen.Operations[0].Note = "different step"
	require.False(t, sameOperationsIgnoringTopstitch(frozen, current))
	require.False(t, sameOperationsIgnoringTopstitch(&pb_common.TechCardInsert{}, current))
}

func TestSnapshotNamesEveryNestedTopstitchList(t *testing.T) {
	row := restoreSnapshotRow(t, restoreCardAtV6())
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(row.Snapshot), &top))
	var insert map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(top["techCard"], &insert))
	var operations []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(insert["operations"], &operations))
	require.Len(t, operations, 2)
	for i := range operations {
		require.JSONEq(t, `[]`, string(operations[i][snapshotTopstitchLinesField]))
	}

	frozen, err := decodeTechCardSnapshot(row)
	require.NoError(t, err)
	require.NoError(t, restoreTopstitchLinesFromSnapshot(row.Snapshot, frozen))
	require.True(t, frozen.GetTechCard().GetTopstitchLinesAware())

	delete(operations[0], snapshotTopstitchLinesField)
	rawOps, err := json.Marshal(operations)
	require.NoError(t, err)
	insert["operations"] = rawOps
	rawInsert, err := json.Marshal(insert)
	require.NoError(t, err)
	top["techCard"] = rawInsert
	old, err := json.Marshal(top)
	require.NoError(t, err)
	frozen.TechCard.TopstitchLinesAware = false
	require.NoError(t, restoreTopstitchLinesFromSnapshot(string(old), frozen))
	require.False(t, frozen.GetTechCard().GetTopstitchLinesAware(), "a pre-field snapshot stays unaware")
}

func restoreTopstitchAnchor(piece string) entity.TechCardSeamAnchor {
	return entity.TechCardSeamAnchor{
		PieceLineKey: piece,
		Samples: []entity.TechCardSeamSample{
			{U: 0, V: 0}, {U: .25, V: .1}, {U: .5, V: .2}, {U: .75, V: .3}, {U: 1, V: .4},
		},
		PerimShare: .2,
		LenMm:      250,
		EdgeHint:   "FR#1",
	}
}

func TestRestoreCarriesPerLineTopstitchFreshness(t *testing.T) {
	v6 := restoreCardAtV6()
	v6.BomItems = append(v6.BomItems, entity.TechCardBomItem{
		Id: 12, LineKey: restoreTopThread, Section: entity.BomSectionThread, Name: "thread",
		Unit:        sql.NullString{String: "m", Valid: true},
		ThreadPerMm: decimal.NewNullDecimal(decimal.RequireFromString("2.5")),
	})
	for i := range v6.Operations {
		v6.Operations[i].BomLineKeys = []string{restoreTopThread}
	}
	v6.Operations[0].TopstitchLines = []entity.TechCardTopstitchLine{{
		LineKey: restoreTopStale, BomLineKey: restoreTopThread,
		Parts: []entity.TechCardSeamAnchor{restoreTopstitchAnchor(keyFront)}, Rows: 1, Stale: true,
	}}
	v6.Operations[1].TopstitchLines = []entity.TechCardTopstitchLine{{
		LineKey: restoreTopFresh, BomLineKey: restoreTopThread,
		Parts: []entity.TechCardSeamAnchor{restoreTopstitchAnchor(keyCollar)}, Rows: 1,
	}}

	written := restoreCapture(t, restoreSnapshotRow(t, v6))
	stale := written.Operations[0].TopstitchLines[0]
	fresh := written.Operations[1].TopstitchLines[0]
	require.True(t, stale.RestoreSourceFingerprint)
	require.True(t, stale.RestoreStale)
	require.True(t, fresh.RestoreSourceFingerprint)
	require.False(t, fresh.RestoreStale)
}

func addStoredTopstitchLine(card *entity.TechCard) {
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{
		Id: 12, LineKey: restoreTopThread, Section: entity.BomSectionThread, Name: "thread",
		Unit:        sql.NullString{String: "m", Valid: true},
		ThreadPerMm: decimal.NewNullDecimal(decimal.RequireFromString("2.5")),
	})
	card.Operations[0].BomLineKeys = []string{restoreTopThread}
	card.Operations[0].TopstitchLines = []entity.TechCardTopstitchLine{{
		LineKey: restoreTopFresh, BomLineKey: restoreTopThread,
		Parts: []entity.TechCardSeamAnchor{restoreTopstitchAnchor(keyFront)}, Rows: 1,
	}}
}

func snapshotBeforeTopstitch(t *testing.T, card *entity.TechCard) *entity.TechCardSnapshot {
	t.Helper()
	row := restoreSnapshotRow(t, card)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(row.Snapshot), &top))
	var insert map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(top["techCard"], &insert))
	var operations []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(insert["operations"], &operations))
	for i := range operations {
		delete(operations[i], snapshotTopstitchLinesField)
	}
	rawOperations, err := json.Marshal(operations)
	require.NoError(t, err)
	insert["operations"] = rawOperations
	rawInsert, err := json.Marshal(insert)
	require.NoError(t, err)
	top["techCard"] = rawInsert
	raw, err := json.Marshal(top)
	require.NoError(t, err)
	row.Snapshot = string(raw)
	return row
}

func TestOnlyUnchangedPre0464RestoreCarriesStoredTopstitchLines(t *testing.T) {
	t.Run("unchanged operations carry", func(t *testing.T) {
		restoreHosts(t)
		current := restoreCardNow()
		addStoredTopstitchLine(current)
		frozen := restoreCardNow()
		frozen.LockVersion = 2
		frozen.BomItems = append(frozen.BomItems, current.BomItems[1])
		frozen.Operations[0].BomLineKeys = []string{restoreTopThread}
		row := snapshotBeforeTopstitch(t, frozen)

		rig := newRestoreRig(t)
		rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).Return(row, nil)
		rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(current, nil)
		var written *entity.TechCardInsert
		rig.cards.EXPECT().UpdateTechCardAndListOrphanedPatternURLs(mock.Anything, restoreCardID,
			mock.AnythingOfType("*entity.TechCardInsert"), 3).
			Run(func(_ context.Context, _ int, tc *entity.TechCardInsert, _ int) { written = tc }).
			Return(nil, entity.ErrTechCardConflict)

		_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(3))
		require.Equal(t, codes.Aborted, status.Code(err), "%v", err)
		require.NotNil(t, written)
		require.Len(t, written.Operations[0].TopstitchLines, 1)
		require.Equal(t, restoreTopFresh, written.Operations[0].TopstitchLines[0].LineKey)
	})

	t.Run("changed operations refuse positional carry", func(t *testing.T) {
		restoreHosts(t)
		current := restoreCardNow()
		addStoredTopstitchLine(current)
		frozen := restoreCardNow()
		frozen.LockVersion = 2
		frozen.BomItems = append(frozen.BomItems, current.BomItems[1])
		frozen.Operations[0].BomLineKeys = []string{restoreTopThread}
		frozen.Operations[0].Note = sql.NullString{String: "different step", Valid: true}
		row := snapshotBeforeTopstitch(t, frozen)

		rig := newRestoreRig(t)
		rig.cards.EXPECT().GetTechCardSnapshot(mock.Anything, restoreCardID, restoreSnapshotID).Return(row, nil)
		rig.cards.EXPECT().GetTechCardByIdConsistent(mock.Anything, restoreCardID).Return(current, nil)

		_, err := rig.srv.RestoreTechCardSnapshot(restoreCtx(), restoreReq(3))
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
	})
}

func TestSnapshotThreadPerMmPresenceDistinguishesClearFromPreserve(t *testing.T) {
	restoreHosts(t)
	row := restoreSnapshotRow(t, restoreCardAtV6())
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(row.Snapshot), &top))
	var insert map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(top["techCard"], &insert))
	var bomItems []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(insert["bomItems"], &bomItems))
	require.JSONEq(t, `{}`, string(bomItems[0][snapshotThreadPerMmField]),
		"new snapshots explicitly name a cleared factor")

	now := restoreCardNow()
	now.BomItems[0].ThreadPerMm = decimal.NewNullDecimal(decimal.RequireFromString("2.5"))
	current := dto.ConvertEntityTechCardToPb(now, dto.CostingFx{}).GetTechCard()

	newFrozen, err := decodeTechCardSnapshot(row)
	require.NoError(t, err)
	newPayload, err := restoreTechCardPayload(newFrozen.GetTechCard(), now, current)
	require.NoError(t, err)
	newEntity, err := dto.ConvertPbTechCardInsertToEntityPrepared(newPayload, func(tc *entity.TechCardInsert) {
		entity.CarryStoredThreadPerMm(tc, &now.TechCardInsert)
	})
	require.NoError(t, err)
	require.False(t, newEntity.BomItems[0].ThreadPerMm.Valid, "explicit empty clears today's factor")
	require.False(t, newEntity.BomItems[0].ThreadPerMmOmitted)

	delete(bomItems[0], snapshotThreadPerMmField)
	rawBomItems, err := json.Marshal(bomItems)
	require.NoError(t, err)
	insert["bomItems"] = rawBomItems
	rawInsert, err := json.Marshal(insert)
	require.NoError(t, err)
	top["techCard"] = rawInsert
	oldRaw, err := json.Marshal(top)
	require.NoError(t, err)
	oldRow := *row
	oldRow.Snapshot = string(oldRaw)
	oldFrozen, err := decodeTechCardSnapshot(&oldRow)
	require.NoError(t, err)
	oldPayload, err := restoreTechCardPayload(oldFrozen.GetTechCard(), now, current)
	require.NoError(t, err)
	require.Nil(t, oldPayload.GetBomItems()[0].ThreadPerMm, "a pre-0464 snapshot stays omitted")
	oldEntity, err := dto.ConvertPbTechCardInsertToEntityPrepared(oldPayload, func(tc *entity.TechCardInsert) {
		entity.CarryStoredThreadPerMm(tc, &now.TechCardInsert)
	})
	require.NoError(t, err)
	require.Equal(t, "2.5", oldEntity.BomItems[0].ThreadPerMm.Decimal.String())
	require.True(t, oldEntity.BomItems[0].ThreadPerMmOmitted)
}
