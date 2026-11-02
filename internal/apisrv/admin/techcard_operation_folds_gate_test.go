package admin

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const testFoldKey = "01JABCDEFGHJKMNPQRSTVWXYA1"

func operationFoldsPayload(aware bool, keys ...string) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		OperationFoldsAware: aware,
		Operations:          []*pb_common.TechCardOperation{{FoldKeys: keys}},
	}
}

func TestOperationFoldsGateProtectsFullReplace(t *testing.T) {
	wireErr := operationFoldsWireGate(operationFoldsPayload(false, testFoldKey))
	wantFailedPrecondition(t, wireErr, "fold keys without awareness")
	if !strings.Contains(wireErr.Error(), "hard-refresh") {
		t.Fatalf("wire refusal must tell the operator how to recover: %v", wireErr)
	}

	stored := storedCard(entity.TechCardOperation{FoldKeys: []string{testFoldKey}})
	wantFailedPrecondition(t, operationFoldsStoredGate(operationFoldsPayload(false), stored),
		"unaware full replace over stored fold keys")

	if err := operationFoldsStoredGate(operationFoldsPayload(true), stored); err != nil {
		t.Fatalf("aware client must be able to clear the folds of a step: %v", err)
	}
	if err := operationFoldsWireGate(operationFoldsPayload(false)); err != nil {
		t.Fatalf("cards without fold steps remain writable by an old bundle: %v", err)
	}
	if err := operationFoldsStoredGate(operationFoldsPayload(false), storedCard(entity.TechCardOperation{})); err != nil {
		t.Fatalf("stored card without fold steps stays writable: %v", err)
	}
}

func TestStripOperationFoldKeys(t *testing.T) {
	pb := operationFoldsPayload(true, testFoldKey)
	stripOperationFoldKeys(pb)
	if len(pb.Operations[0].FoldKeys) != 0 {
		t.Fatalf("fold keys survived the strip: %v", pb.Operations[0].FoldKeys)
	}
	stripOperationFoldKeys(nil)
}

func TestOperationFoldsGateIsWiredIntoWrites(t *testing.T) {
	src := readDraftGateSource(t, "techcard.go")
	if strings.Count(src, "operationFoldsWireGate(") < 2 ||
		!strings.Contains(src, "operationFoldsStoredGate(in, stored)") {
		t.Fatal("operation folds shield is not wired into create/update")
	}
	archive := readDraftGateSource(t, "techcard_archive.go")
	if !strings.Contains(archive, "operationFoldsWireGate,") || !strings.Contains(archive, "operationFoldsStoredGate,") {
		t.Fatal("archive import does not run the operation folds shield")
	}
	for _, f := range []string{"style.go", "techcard_archive_card.go", "techcard_archive_resolve.go", "techcard_restore.go"} {
		if !strings.Contains(readDraftGateSource(t, f), "OperationFoldsAware = true") {
			t.Fatalf("%s builds a server payload without operation_folds_aware", f)
		}
	}
	require.Contains(t, readDraftGateSource(t, "style.go"), "stripOperationFoldKeys(pbInsert)",
		"a season clone does not copy the folds, so its steps must not name them")
}

// A season clone copies neither the folds nor the links: the rows are not carried (as seams), and a key
// left on the payload would be refused by the store as unknown on the new card.
func TestCloneStyleForSeasonLeavesFoldKeysBehind(t *testing.T) {
	source := cloneSourceCard()
	source.Operations = []entity.TechCardOperation{{OperationType: entity.OpTypeHandwork, Zone: entity.ZoneOuter,
		FoldKeys: []string{testFoldKey}}}
	source.PieceFolds = []entity.TechCardPieceFold{{TechCardPieceFoldInput: entity.TechCardPieceFoldInput{FoldKey: testFoldKey}}}

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardByIdConsistent(mock.Anything, 7).Return(source, nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)
	tc.EXPECT().SuggestStyleNumber(mock.Anything, "FW", 2026).Return("FW26-0007", nil)
	tc.EXPECT().CloneTechCardForSeason(mock.Anything, 7, 4, mock.AnythingOfType("*entity.TechCardInsert")).
		Run(func(_ context.Context, _, _ int, insert *entity.TechCardInsert) {
			require.Len(t, insert.Operations, 1)
			require.Empty(t, insert.Operations[0].FoldKeys)
		}).Return(11, nil)

	resp, err := (&Server{repo: repo}).CloneStyleForSeason(fullAccessCtx(), cloneRequest())
	require.NoError(t, err)
	require.Equal(t, int32(11), resp.NewStyleId)
}
