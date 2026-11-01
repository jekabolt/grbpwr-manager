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

const foldHandlerKey = "01JABCDEFGHJKMNPQRSTVWXYZ0"

func foldHandlerReq() *pb_admin.UpsertTechCardPieceFoldsRequest {
	return &pb_admin.UpsertTechCardPieceFoldsRequest{TechCardId: 7, Folds: []*pb_common.TechCardPieceFold{{
		FoldKey:      foldHandlerKey,
		PieceLineKey: "PLACKET",
		Seq:          1,
		Kind:         pb_common.TechCardPieceFoldKind_TECH_CARD_PIECE_FOLD_KIND_INWARD,
		Source:       pb_common.TechCardPieceFoldSource_TECH_CARD_PIECE_FOLD_SOURCE_ACCEPTED_HINT,
		Line:         []*pb_common.TechCardSeamSample{{U: 0.5, V: 0}, {U: 0.5, V: 1}},
		FlipAt:       &pb_common.TechCardSeamSample{U: 0.75, V: 0.5},
		EdgeSamples: []*pb_common.TechCardSeamSample{
			{U: 1, V: 0}, {U: 1, V: 0.25}, {U: 1, V: 0.5}, {U: 1, V: 0.75}, {U: 1, V: 1},
		},
		OffsetMm: 20,
		Stale:    true, // output-only: ignored on the way in
	}}}
}

func TestUpsertTechCardPieceFoldsPassesTheFoldAndEchoesTheList(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().UpsertTechCardPieceFolds(mock.Anything, mock.MatchedBy(func(in entity.TechCardPieceFoldsWrite) bool {
		f := in.Folds[0]
		return in.TechCardId == 7 && in.By == "ann" && len(in.Folds) == 1 &&
			f.FoldKey == foldHandlerKey && f.Seq == 1 && f.Kind == entity.TechCardPieceFoldInward &&
			f.Source == entity.TechCardPieceFoldSourceAcceptedHint && f.OffsetMm == 20 &&
			len(f.Line) == 2 && len(f.EdgeSamples) == 5 && f.FlipAt.U == 0.75
	})).Return([]entity.TechCardPieceFold{
		{TechCardPieceFoldInput: entity.TechCardPieceFoldInput{FoldKey: foldHandlerKey, Seq: 1,
			Kind: entity.TechCardPieceFoldInward, Source: entity.TechCardPieceFoldSourceAcceptedHint,
			Line: []entity.TechCardSeamSample{{U: 0.5, V: 0}, {U: 0.5, V: 1}}, FlipAt: entity.TechCardSeamSample{U: 0.75, V: 0.5}}, Stale: true},
		{TechCardPieceFoldInput: entity.TechCardPieceFoldInput{FoldKey: "01JABCDEFGHJKMNPQRSTVWXYZ1", Seq: 2,
			Kind: "sideways", Source: entity.TechCardPieceFoldSourceOwner}},
	}, 1, nil)

	ctx := authsrv.PutAdminUsername(fullAccessCtx(), "ann")
	resp, err := (&Server{repo: repo}).UpsertTechCardPieceFolds(ctx, foldHandlerReq())
	require.NoError(t, err)
	require.EqualValues(t, 1, resp.GetWritten())
	require.Len(t, resp.GetFolds(), 2, "the FULL list, not just the written row")
	require.True(t, resp.GetFolds()[0].GetStale())
	require.Equal(t, pb_common.TechCardPieceFoldKind_TECH_CARD_PIECE_FOLD_KIND_INWARD, resp.GetFolds()[0].GetKind())
	require.Len(t, resp.GetFolds()[0].GetLine(), 2)
	require.EqualValues(t, 0.75, resp.GetFolds()[0].GetFlipAt().GetU())
	require.Equal(t, pb_common.TechCardPieceFoldKind_TECH_CARD_PIECE_FOLD_KIND_UNKNOWN, resp.GetFolds()[1].GetKind(),
		"an unknown stored kind reads UNKNOWN instead of failing the read")
}

func TestUpsertTechCardPieceFoldsRefusesBeforeTheStore(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*pb_admin.UpsertTechCardPieceFoldsRequest)
		field string
	}{
		{"three points", func(r *pb_admin.UpsertTechCardPieceFoldsRequest) {
			r.Folds[0].Line = append(r.Folds[0].Line, &pb_common.TechCardSeamSample{})
		}, "folds[0].line"},
		{"three samples", func(r *pb_admin.UpsertTechCardPieceFoldsRequest) {
			r.Folds[0].EdgeSamples = r.Folds[0].EdgeSamples[:3]
		}, "folds[0].edge_samples"},
		{"no flip", func(r *pb_admin.UpsertTechCardPieceFoldsRequest) { r.Folds[0].FlipAt = nil }, "folds[0].flip_at"},
		{"unknown kind", func(r *pb_admin.UpsertTechCardPieceFoldsRequest) {
			r.Folds[0].Kind = pb_common.TechCardPieceFoldKind_TECH_CARD_PIECE_FOLD_KIND_UNKNOWN
		}, "folds[0].kind"},
		{"unknown source", func(r *pb_admin.UpsertTechCardPieceFoldsRequest) {
			r.Folds[0].Source = pb_common.TechCardPieceFoldSource_TECH_CARD_PIECE_FOLD_SOURCE_UNKNOWN
		}, "folds[0].source"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t) // strict: any store call fails the test
			req := foldHandlerReq()
			c.edit(req)
			_, err := (&Server{repo: repo}).UpsertTechCardPieceFolds(fullAccessCtx(), req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), c.field)
		})
	}
	repo := mocks.NewMockRepository(t)
	_, err := (&Server{repo: repo}).UpsertTechCardPieceFolds(fullAccessCtx(), &pb_admin.UpsertTechCardPieceFoldsRequest{TechCardId: 7})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestUpsertTechCardPieceFoldsMapsStoreRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code codes.Code
	}{
		{"released card", fmt.Errorf("folds: %w", entity.ErrTechCardReleased), codes.FailedPrecondition},
		{"piece not on card", entity.NewFieldViolation("folds[0].piece_line_key", "piece_not_on_card", "X", ""), codes.InvalidArgument},
		{"seq clash", entity.NewFieldViolation("folds[0].seq", "duplicate", "1", ""), codes.InvalidArgument},
		{"unexpected", errors.New("boom"), codes.Internal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := mocks.NewMockRepository(t)
			techCards := mocks.NewMockTechCards(t)
			repo.EXPECT().TechCards().Return(techCards)
			techCards.EXPECT().UpsertTechCardPieceFolds(mock.Anything, mock.Anything).Return(nil, 0, c.err)
			_, err := (&Server{repo: repo}).UpsertTechCardPieceFolds(fullAccessCtx(), foldHandlerReq())
			require.Equal(t, c.code, status.Code(err))
		})
	}
}

func TestDeleteTechCardPieceFolds(t *testing.T) {
	repo := mocks.NewMockRepository(t) // strict
	_, err := (&Server{repo: repo}).DeleteTechCardPieceFolds(fullAccessCtx(), &pb_admin.DeleteTechCardPieceFoldsRequest{TechCardId: 7})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "no keys is refused before the store")

	techCards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(techCards)
	techCards.EXPECT().DeleteTechCardPieceFolds(mock.Anything, 7, []string{foldHandlerKey}).
		Return([]entity.TechCardPieceFold{}, 1, nil)
	resp, err := (&Server{repo: repo}).DeleteTechCardPieceFolds(fullAccessCtx(),
		&pb_admin.DeleteTechCardPieceFoldsRequest{TechCardId: 7, FoldKeys: []string{foldHandlerKey}})
	require.NoError(t, err)
	require.EqualValues(t, 1, resp.GetDeleted())
	require.Empty(t, resp.GetFolds())
}

func TestTechCardPieceFoldsRBAC(t *testing.T) {
	want, _, ok := rbac.Lookup(rbac.MethodPrefix + "UpsertTechCardSeams")
	require.True(t, ok)
	for _, m := range []string{"UpsertTechCardPieceFolds", "DeleteTechCardPieceFolds"} {
		got, allowlisted, known := rbac.Lookup(rbac.MethodPrefix + m)
		require.True(t, known, m)
		require.False(t, allowlisted, m)
		require.Equal(t, want, got, m)
		require.Equal(t, entity.AccessWrite, got.Access, m)
	}
}
