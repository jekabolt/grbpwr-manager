package admin

import (
	"errors"
	"fmt"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const seamHandlerKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"

func seamHandlerAnchor(piece string) *pb_common.TechCardSeamAnchor {
	return &pb_common.TechCardSeamAnchor{
		PieceLineKey: piece,
		Samples: []*pb_common.TechCardSeamSample{
			{U: 0, V: 0}, {U: 0.25, V: 0.1}, {U: 0.5, V: 0.2}, {U: 0.75, V: 0.3}, {U: 1, V: 0.4},
		},
		PerimShare: 0.1, LenMm: 120, EdgeHint: "P#1",
	}
}

func seamHandlerReq() *pb_admin.UpsertTechCardSeamsRequest {
	return &pb_admin.UpsertTechCardSeamsRequest{TechCardId: 7, Seams: []*pb_common.TechCardSeam{{
		SeamKey:   seamHandlerKey,
		Status:    pb_common.TechCardSeamStatus_TECH_CARD_SEAM_STATUS_REJECTED,
		Kind:      pb_common.TechCardSeamKind_TECH_CARD_SEAM_KIND_EDGE,
		Direction: pb_common.TechCardSeamDirection_TECH_CARD_SEAM_DIRECTION_REVERSED,
		Source:    pb_common.TechCardSeamSource_TECH_CARD_SEAM_SOURCE_GRAPH,
		SideA:     &pb_common.TechCardSeamSide{Parts: []*pb_common.TechCardSeamAnchor{seamHandlerAnchor("BP")}},
		SideB:     &pb_common.TechCardSeamSide{Parts: []*pb_common.TechCardSeamAnchor{seamHandlerAnchor("FRONT_L")}},
		Note:      "wrong shoulder",
	}}}
}

// The decision reaches the store converted, stamped with the caller, and the store's full list is
// echoed with the server's stale verdict.
func TestUpsertTechCardSeamsPassesTheDecisionAndEchoesTheList(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().UpsertTechCardSeams(mock.Anything, mock.MatchedBy(func(in entity.TechCardSeamsWrite) bool {
		return in.TechCardId == 7 && in.By == "ann" && len(in.Seams) == 1 &&
			in.Seams[0].SeamKey == seamHandlerKey &&
			in.Seams[0].Status == entity.TechCardSeamRejected &&
			in.Seams[0].SideA[0].PieceLineKey == "BP" && in.Seams[0].Note == "wrong shoulder"
	})).Return([]entity.TechCardSeam{
		{TechCardSeamInput: entity.TechCardSeamInput{SeamKey: seamHandlerKey, Status: entity.TechCardSeamRejected,
			Kind: entity.TechCardSeamKindEdge, Source: entity.TechCardSeamSourceGraph, Direction: entity.TechCardSeamReversed}, Stale: true},
		{TechCardSeamInput: entity.TechCardSeamInput{SeamKey: "01JABCDEFGHJKMNPQRSTVWXYZ1", Status: entity.TechCardSeamConfirmed}},
	}, 1, nil)

	ctx := authsrv.PutAdminUsername(fullAccessCtx(), "ann")
	resp, err := (&Server{repo: repo}).UpsertTechCardSeams(ctx, seamHandlerReq())
	require.NoError(t, err)
	require.EqualValues(t, 1, resp.GetWritten())
	require.Len(t, resp.GetSeams(), 2, "the FULL list, not just the written row")
	require.True(t, resp.GetSeams()[0].GetStale())
	require.Equal(t, pb_common.TechCardSeamStatus_TECH_CARD_SEAM_STATUS_REJECTED, resp.GetSeams()[0].GetStatus())
}

// A malformed seam is refused before the store, with the field named.
func TestUpsertTechCardSeamsRefusesBeforeTheStore(t *testing.T) {
	repo := mocks.NewMockRepository(t) // strict: any store call fails the test
	req := seamHandlerReq()
	req.Seams[0].SideB.Parts[0].Samples = req.Seams[0].SideB.Parts[0].Samples[:3]
	_, err := (&Server{repo: repo}).UpsertTechCardSeams(fullAccessCtx(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "seams[0].side_b.parts[0].samples")

	_, err = (&Server{repo: repo}).UpsertTechCardSeams(fullAccessCtx(), &pb_admin.UpsertTechCardSeamsRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestUpsertTechCardSeamsMapsStoreRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code codes.Code
	}{
		{"released card", fmt.Errorf("seams: %w", entity.ErrTechCardReleased), codes.FailedPrecondition},
		{"piece not on card", entity.NewFieldViolation("seams[0].side_a.parts[0].piece_line_key", "piece_not_on_card", "BP", ""), codes.InvalidArgument},
		{"unexpected", errors.New("boom"), codes.Internal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			techCards := mocks.NewMockTechCards(t)
			repo.EXPECT().TechCards().Return(techCards)
			techCards.EXPECT().UpsertTechCardSeams(mock.Anything, mock.Anything).Return(nil, 0, c.err)
			_, err := (&Server{repo: repo}).UpsertTechCardSeams(fullAccessCtx(), seamHandlerReq())
			require.Equal(t, c.code, status.Code(err))
		})
	}
}

func TestDeleteTechCardSeams(t *testing.T) {
	repo := mocks.NewMockRepository(t) // strict
	_, err := (&Server{repo: repo}).DeleteTechCardSeams(fullAccessCtx(), &pb_admin.DeleteTechCardSeamsRequest{TechCardId: 7})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "no keys is refused before the store")

	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().DeleteTechCardSeams(mock.Anything, 7, []string{seamHandlerKey}).
		Return([]entity.TechCardSeam{}, 1, nil)
	resp, err := (&Server{repo: repo}).DeleteTechCardSeams(fullAccessCtx(),
		&pb_admin.DeleteTechCardSeamsRequest{TechCardId: 7, SeamKeys: []string{seamHandlerKey}})
	require.NoError(t, err)
	require.EqualValues(t, 1, resp.GetDeleted())
	require.Empty(t, resp.GetSeams())
}

// Both RPCs are tech-cards WRITE — the piece areas' level. A missing entry would fail closed (or be
// allowlisted by accident); a read level would let a viewer rewrite the technologist's decisions.
func TestTechCardSeamsRBAC(t *testing.T) {
	want, _, ok := rbac.Lookup(rbac.MethodPrefix + "SaveTechCardPieceAreas")
	require.True(t, ok)
	for _, m := range []string{"UpsertTechCardSeams", "DeleteTechCardSeams"} {
		got, allowlisted, known := rbac.Lookup(rbac.MethodPrefix + m)
		require.True(t, known, m)
		require.False(t, allowlisted, m)
		require.Equal(t, want, got, m)
		require.Equal(t, entity.AccessWrite, got.Access, m)
	}
}
