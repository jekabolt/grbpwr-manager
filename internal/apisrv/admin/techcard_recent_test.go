package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func recentCtx(adminID int) context.Context {
	return aiprov.WithActor(context.Background(), aiprov.Actor{Username: "anna", AdminID: &adminID})
}

func TestTouchedTechCardID(t *testing.T) {
	m := func(name string) string { return rbac.MethodPrefix + name }
	cases := []struct {
		name   string
		method string
		req    any
		resp   any
		want   int
	}{
		{"open", m("GetTechCard"), &pb_admin.GetTechCardRequest{Id: 7}, &pb_admin.GetTechCardResponse{}, 7},
		{"save", m("UpdateTechCard"), &pb_admin.UpdateTechCardRequest{Id: 8}, &pb_admin.UpdateTechCardResponse{}, 8},
		{"create answers the new id", m("CreateTechCard"), &pb_admin.CreateTechCardRequest{}, &pb_admin.CreateTechCardResponse{Id: 9}, 9},
		{"delete cascades, no touch", m("DeleteTechCard"), &pb_admin.DeleteTechCardRequest{Id: 7}, &pb_admin.DeleteTechCardResponse{}, 0},
		{"sub-resource write", m("UpsertTechCardSeams"), &pb_admin.UpsertTechCardSeamsRequest{TechCardId: 3}, &pb_admin.UpsertTechCardSeamsResponse{}, 3},
		{"import names the card in the response", m("CommitTechCardImport"), &pb_admin.CommitTechCardImportRequest{}, &pb_admin.CommitTechCardImportResponse{TechCardId: 4}, 4},
		{"an id that is not a card", m("DeleteTechCardDevExpense"), &pb_admin.DeleteTechCardDevExpenseRequest{Id: 2}, &pb_admin.DeleteTechCardDevExpenseResponse{}, 0},
		{"a read other than open", m("GetTechCardReadiness"), &pb_admin.GetTechCardReadinessRequest{TechCardId: 5}, &pb_admin.GetTechCardReadinessResponse{}, 0},
		{"a write without a card", m("CreateMaterial"), &pb_admin.CreateMaterialRequest{}, &pb_admin.CreateMaterialResponse{Id: 11}, 0},
		{"another service", "/auth.AuthService/Login", &pb_admin.GetTechCardRequest{Id: 7}, nil, 0},
		{"nil response", m("UpsertTechCardSeams"), &pb_admin.UpsertTechCardSeamsRequest{}, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, touchedTechCardID(c.method, c.req, c.resp))
		})
	}
}

func TestRecentTechCardsUnaryTouchesAfterSuccessOnly(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: rbac.MethodPrefix + "GetTechCard"}
	req := &pb_admin.GetTechCardRequest{Id: 7}
	ok := func(context.Context, any) (any, error) { return &pb_admin.GetTechCardResponse{}, nil }

	t.Run("success touches the caller's row", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		tc := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(tc)
		tc.EXPECT().TouchTechCardRecent(mock.Anything, 3, 7).Return(nil).Once()
		resp, err := (&Server{repo: repo}).RecentTechCardsUnary()(recentCtx(3), req, info, ok)
		require.NoError(t, err)
		require.NotNil(t, resp)
	})

	t.Run("a failed call touches nothing", func(t *testing.T) {
		repo := mocks.NewMockRepository(t) // no expectations: any repo call fails the test
		fail := func(context.Context, any) (any, error) { return nil, status.Error(codes.NotFound, "nope") }
		_, err := (&Server{repo: repo}).RecentTechCardsUnary()(recentCtx(3), req, info, fail)
		require.Equal(t, codes.NotFound, status.Code(err))
	})

	t.Run("a failed touch never fails the call", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		tc := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(tc)
		tc.EXPECT().TouchTechCardRecent(mock.Anything, 3, 7).Return(errors.New("db down")).Once()
		resp, err := (&Server{repo: repo}).RecentTechCardsUnary()(recentCtx(3), req, info, ok)
		require.NoError(t, err)
		require.NotNil(t, resp)
	})

	t.Run("a caller without an account row touches nothing", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		_, err := (&Server{repo: repo}).RecentTechCardsUnary()(context.Background(), req, info, ok)
		require.NoError(t, err)
	})

	t.Run("a username whose id the auth chain could not resolve: no second lookup, no touch", func(t *testing.T) {
		repo := mocks.NewMockRepository(t) // no expectations: any repo call fails the test
		zero := 0
		ctx := authsrv.PutAdminUsername(context.Background(), "boris")
		ctx = aiprov.WithActor(ctx, aiprov.Actor{Username: "boris", AdminID: &zero})
		_, err := (&Server{repo: repo}).RecentTechCardsUnary()(ctx, req, info, ok)
		require.NoError(t, err)
		ctx = aiprov.WithActor(authsrv.PutAdminUsername(context.Background(), "boris"), aiprov.Actor{Username: "boris"})
		_, err = (&Server{repo: repo}).RecentTechCardsUnary()(ctx, req, info, ok)
		require.NoError(t, err)
	})
}

func TestListMyRecentTechCards(t *testing.T) {
	t.Run("the caller's cards, latest first, with when", func(t *testing.T) {
		at := time.Date(2026, 11, 19, 13, 0, 0, 0, time.UTC)
		repo := mocks.NewMockRepository(t)
		tc := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(tc)
		card := func(id int, style, name string) entity.TechCard {
			var c entity.TechCard
			c.Id = id
			c.StyleNumber = sql.NullString{String: style, Valid: true}
			c.Name = name
			c.Stage = entity.TechCardStageProto
			return c
		}
		tc.EXPECT().ListRecentTechCards(mock.Anything, 3, 6).Return([]entity.RecentTechCard{
			{Card: card(2701, "ml-2701", "boxy shirt"), TouchedAt: at},
			{Card: card(2704, "ml-2704", "field jacket"), TouchedAt: at.Add(-time.Hour)},
		}, nil)
		resp, err := (&Server{repo: repo}).ListMyRecentTechCards(recentCtx(3), &pb_admin.ListMyRecentTechCardsRequest{Limit: 6})
		require.NoError(t, err)
		require.Len(t, resp.GetItems(), 2)
		require.EqualValues(t, 2701, resp.GetItems()[0].GetCard().GetId())
		require.Equal(t, "ml-2701", resp.GetItems()[0].GetCard().GetStyleNumber())
		require.Equal(t, "boxy shirt", resp.GetItems()[0].GetCard().GetName())
		require.True(t, at.Equal(resp.GetItems()[0].GetTouchedAt().AsTime()))
		require.EqualValues(t, 2704, resp.GetItems()[1].GetCard().GetId())
		require.NotEqual(t, pb_common.TechCardStage_TECH_CARD_STAGE_UNKNOWN, resp.GetItems()[0].GetCard().GetStage())
	})

	t.Run("the limit travels as asked; the store clamps", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		tc := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(tc)
		tc.EXPECT().ListRecentTechCards(mock.Anything, 3, 0).Return([]entity.RecentTechCard{}, nil)
		resp, err := (&Server{repo: repo}).ListMyRecentTechCards(recentCtx(3), &pb_admin.ListMyRecentTechCardsRequest{})
		require.NoError(t, err)
		require.Empty(t, resp.GetItems())
	})

	t.Run("no account row: an empty list, no store call", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		resp, err := (&Server{repo: repo}).ListMyRecentTechCards(context.Background(), &pb_admin.ListMyRecentTechCardsRequest{})
		require.NoError(t, err)
		require.Empty(t, resp.GetItems())
	})

	t.Run("a store error is Internal", func(t *testing.T) {
		repo := mocks.NewMockRepository(t)
		tc := mocks.NewMockTechCards(t)
		repo.EXPECT().TechCards().Return(tc)
		tc.EXPECT().ListRecentTechCards(mock.Anything, 3, 6).Return(nil, errors.New("db down"))
		_, err := (&Server{repo: repo}).ListMyRecentTechCards(recentCtx(3), &pb_admin.ListMyRecentTechCardsRequest{Limit: 6})
		require.Equal(t, codes.Internal, status.Code(err))
	})
}

func TestClampRecentTechCardsLimit(t *testing.T) {
	require.Equal(t, entity.RecentTechCardsDefault, entity.ClampRecentTechCardsLimit(0))
	require.Equal(t, entity.RecentTechCardsDefault, entity.ClampRecentTechCardsLimit(-4))
	require.Equal(t, 3, entity.ClampRecentTechCardsLimit(3))
	require.Equal(t, entity.RecentTechCardsMax, entity.ClampRecentTechCardsLimit(99))
}
