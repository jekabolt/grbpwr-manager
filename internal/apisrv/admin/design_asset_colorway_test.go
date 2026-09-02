package admin

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ПРОБЫ «ТКАНИ КОЛОРВЕЯ» НА API-ЯРУСЕ (0357, волна B).
//
// Здесь проверяется ровно то, за что отвечает ярус: просьба доезжает до стора неискажённой, а
// колонка доезжает до провода. Сами сторожа (kind, граница карточки, single-select) живут в
// транзакции стора и проверяются контейнерными пробами — копия любого из них здесь была бы вторым
// мнением, которое согласно сегодня.

func designAssetColorwayRig(t *testing.T) (*Server, *mocks.MockDesign) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	repo.EXPECT().Design().Return(design).Maybe()
	return &Server{repo: repo}, design
}

// ПРОСЬБА ДОЕЗЖАЕТ ЦЕЛИКОМ, И НОЛЬ В НЕЙ — НАСТОЯЩИЙ ОТВЕТ.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: поменять местами AssetId и ColorwayId при сборке запроса стора — тело
// уедет «наоборот», и ни один сторож стора этого не заметит: оба поля int и оба положительны.
func TestSetDesignAssetColorwayCarriesTheRequestToTheStore(t *testing.T) {
	srv, design := designAssetColorwayRig(t)
	var sent entity.DesignAssetColorwaySet
	design.EXPECT().SetAssetColorway(mock.Anything, mock.Anything).
		Run(func(_ context.Context, req entity.DesignAssetColorwaySet) { sent = req }).
		Return(&entity.DesignAsset{
			Id: 9, TechCardId: 42, Kind: entity.DesignAssetKindPattern, Name: "pattern 2",
			ColorwayId: sql.NullInt32{Int32: 7, Valid: true},
		}, nil).Once()

	resp, err := srv.SetDesignAssetColorway(designRunCtx(), &pb_admin.SetDesignAssetColorwayRequest{
		TechCardId: 42, AssetId: 9, ColorwayId: 7,
	})
	require.NoError(t, err)
	require.Equal(t, 42, sent.TechCardId)
	require.Equal(t, 9, sent.AssetId)
	require.Equal(t, 7, sent.ColorwayId)
	require.NotEmpty(t, sent.Actor, "кто назначил — факт сервера, не клиента")
	// И КОЛОНКА ДОЕЗЖАЕТ ДО ПРОВОДА: без этого экран не смог бы нарисовать «worn by …», и ни один
	// round trip не покраснел бы.
	require.Equal(t, int32(7), resp.GetAsset().GetColorwayId())
}

// НОЛЬ — ЭТО СНЯТИЕ, А НЕ МОЛЧАНИЕ, И ОН ОБЯЗАН ДОЕХАТЬ ТАКИМ ЖЕ.
//
// У этого глагола одна работа, поэтому сентинел (как у bench_colorway_id) здесь НЕ нужен: кто его
// позвал, тот про колорвей и говорит. Проба фиксирует именно это решение.
func TestSetDesignAssetColorwayZeroUnassigns(t *testing.T) {
	srv, design := designAssetColorwayRig(t)
	var sent entity.DesignAssetColorwaySet
	design.EXPECT().SetAssetColorway(mock.Anything, mock.Anything).
		Run(func(_ context.Context, req entity.DesignAssetColorwaySet) { sent = req }).
		Return(&entity.DesignAsset{Id: 9, TechCardId: 42, Kind: entity.DesignAssetKindPattern}, nil).Once()

	resp, err := srv.SetDesignAssetColorway(designRunCtx(), &pb_admin.SetDesignAssetColorwayRequest{
		TechCardId: 42, AssetId: 9, ColorwayId: 0,
	})
	require.NoError(t, err)
	require.Zero(t, sent.ColorwayId)
	require.Zero(t, resp.GetAsset().GetColorwayId(), "NULL колонки — 0 на проводе, не потеря поля")
}

// ОТКАЗ СТОРА ВЫХОДИТ НАЗВАННЫМ, А НЕ ПЯТИСОТКОЙ. Словарь отказов расширен волной A — новых
// токенов не нужно, и проба это как раз и удостоверяет.
func TestSetDesignAssetColorwaySurfacesTheStoreRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   codes.Code
		reason string
	}{
		{"hardware", fmt.Errorf("%w: a hardware asset cannot be the fabric of a colourway",
			entity.ErrDesignColorwayForbidden), codes.InvalidArgument, "colorway_forbidden"},
		{"чужой колорвей", fmt.Errorf("%w: colourway 7 does not belong to tech card 42",
			entity.ErrDesignForeignColorway), codes.FailedPrecondition, "foreign_colorway"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, design := designAssetColorwayRig(t)
			design.EXPECT().SetAssetColorway(mock.Anything, mock.Anything).
				Return(nil, tc.err).Once()
			_, err := srv.SetDesignAssetColorway(designRunCtx(), &pb_admin.SetDesignAssetColorwayRequest{
				TechCardId: 42, AssetId: 9, ColorwayId: 7,
			})
			require.Error(t, err)
			code, md := errorReason(t, err)
			require.Equal(t, tc.code, code)
			require.Equal(t, tc.reason, md["reason"])
		})
	}
}
