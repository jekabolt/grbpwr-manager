package admin

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ПРОБЫ ТКАНИ ПАРЫ (КОЛОРВЕЙ, СЛОТ) НА ПРОВОДЕ (0368). Стор замокан: ключ пары, каскады и три
// границы карточки — собственность internal/store/design и живой базы (asset_binding_db_test.go).

// КОНВЕРТЕР СВЯЗКИ НЕ ТЕРЯЕТ НИ ОДНОГО ПОЛЯ, А ПУСТОЙ СПИСОК — ЭТО [], НЕ nil.
//
// МУТАЦИИ: выбросить любую строку из designAssetBindingToPb (пара приезжает без слота — экран не
// знает, чью строку закрасить); вернуть nil из designAssetBindingsToPb на пустоте (на проводе
// «ничего не выбрано» превратилось бы в «старый бинарь», и клиент закрыл бы двери слотов).
func TestDesignAssetBindingConverterCarriesEveryField(t *testing.T) {
	set := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	pb := designAssetBindingToPb(entity.DesignAssetBinding{
		Id: 4, TechCardId: designRunCardID, ColorwayId: 51, BomItemId: 902, AssetId: 12,
		SetBy: "designer", SetAt: set,
	})
	assert.Equal(t, int32(4), pb.GetId())
	assert.Equal(t, int32(designRunCardID), pb.GetTechCardId())
	assert.Equal(t, int32(51), pb.GetColorwayId())
	assert.Equal(t, int32(902), pb.GetBomItemId())
	assert.Equal(t, int32(12), pb.GetAssetId())
	assert.Equal(t, "designer", pb.GetSetBy())
	require.NotNil(t, pb.GetSetAt())
	assert.Equal(t, set.Unix(), pb.GetSetAt().GetSeconds())

	empty := designAssetBindingsToPb(nil)
	require.NotNil(t, empty, "пустое ≠ отсутствующее: [] — это «ничего не выбрано»")
	assert.Empty(t, empty)
}

// ПОЛОСА ВЕЗЁТ ТКАНИ ПАР ВСЕЙ КАРТОЧКИ, НЕ СУЖАЯ ИХ ВЕРСТАКОМ.
//
// МУТАЦИЯ: убрать из GetDesignBand строку `AssetBindings: …` либо отфильтровать её по
// bench_colorway_id. Полоса отвечает 200, а шаг паттерна рисует все слоты пустыми.
func TestGetDesignBandCarriesEveryPairsFabric(t *testing.T) {
	rig := newDesignAssetRig(t)
	rig.design.EXPECT().GetBand(mock.Anything, designRunCardID, mock.AnythingOfType("int")).
		Return(&entity.DesignBand{
			AssetBindings: []entity.DesignAssetBinding{
				{Id: 1, TechCardId: designRunCardID, ColorwayId: 51, BomItemId: 902, AssetId: 12},
				{Id: 2, TechCardId: designRunCardID, ColorwayId: 52, BomItemId: 902, AssetId: 13},
			},
		}, nil).Once()

	resp, err := rig.srv.GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{
		TechCardId: designRunCardID, BenchColorwayId: 51,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetAssetBindings(), 2, "bench_colorway_id не сужает ткани пар")
	assert.Equal(t, int32(52), resp.GetAssetBindings()[1].GetColorwayId())
}

// ЗАПРОС ДОЕЗЖАЕТ ДО СТОРА ЦЕЛИКОМ, С АВТОРОМ; СНЯТИЕ ОТВЕЧАЕТ ПУСТЫМ binding.
func TestSetDesignAssetBindingCarriesThePairToTheStore(t *testing.T) {
	rig := newDesignAssetRig(t)
	var sent entity.DesignAssetBindingSet
	rig.design.EXPECT().SetAssetBinding(mock.Anything, mock.AnythingOfType("entity.DesignAssetBindingSet")).
		Run(func(_ context.Context, req entity.DesignAssetBindingSet) { sent = req }).
		Return(&entity.DesignAssetBinding{
			Id: 4, TechCardId: designRunCardID, ColorwayId: 51, BomItemId: 902, AssetId: 12, SetBy: "designer",
		}, nil).Once()

	resp, err := rig.srv.SetDesignAssetBinding(designRunCtx(), &pb_admin.SetDesignAssetBindingRequest{
		TechCardId: designRunCardID, ColorwayId: 51, BomItemId: 902, AssetId: 12,
	})
	require.NoError(t, err)
	assert.Equal(t, entity.DesignAssetBindingSet{
		TechCardId: designRunCardID, ColorwayId: 51, BomItemId: 902, AssetId: 12, SetBy: "designer",
	}, sent)
	require.NotNil(t, resp.GetBinding())
	assert.Equal(t, int32(12), resp.GetBinding().GetAssetId())

	rig = newDesignAssetRig(t)
	rig.design.EXPECT().SetAssetBinding(mock.Anything, mock.AnythingOfType("entity.DesignAssetBindingSet")).
		Return(nil, nil).Once()
	resp, err = rig.srv.SetDesignAssetBinding(designRunCtx(), &pb_admin.SetDesignAssetBindingRequest{
		TechCardId: designRunCardID, ColorwayId: 51, BomItemId: 902, AssetId: 0,
	})
	require.NoError(t, err)
	assert.Nil(t, resp.GetBinding(), "после снятия у пары нет ткани, и ответ это говорит")
}

// ОТКАЗЫ ГЛАГОЛА ДОЕЗЖАЮТ ДО КЛИЕНТА ТЕМИ ЖЕ КОДАМИ, ЧТО У СОСЕДЕЙ.
//
// МУТАЦИЯ: убрать строку foreign_bom_line из designRefusals — отказ уезжает Internal «failed to …»
// без машинной причины, а клиент показывает поломку сервера на штатное состояние.
func TestSetDesignAssetBindingRefusalsReachTheClientAsThemselves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		code   codes.Code
		reason string
	}{
		{"строка BOM чужой карточки", entity.ErrDesignForeignBomLine, codes.FailedPrecondition, "foreign_bom_line"},
		{"колорвей чужой карточки", entity.ErrDesignForeignColorway, codes.FailedPrecondition, "foreign_colorway"},
		{"фурнитура тканью слота не бывает", entity.ErrDesignColorwayForbidden, codes.InvalidArgument, "colorway_forbidden"},
		{"ассет чужой карточки", entity.ErrDesignNotFound, codes.NotFound, "not_found"},
		{"ноль вместо слота", entity.ErrDesignInvalidArgument, codes.InvalidArgument, "invalid_argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDesignAssetRig(t)
			rig.design.EXPECT().SetAssetBinding(mock.Anything, mock.AnythingOfType("entity.DesignAssetBindingSet")).
				Return(nil, tc.err).Once()
			_, err := rig.srv.SetDesignAssetBinding(designRunCtx(), &pb_admin.SetDesignAssetBindingRequest{
				TechCardId: designRunCardID, ColorwayId: 51, BomItemId: 902, AssetId: 12,
			})
			require.Error(t, err)
			code, md := errorReason(t, err)
			assert.Equal(t, tc.code, code)
			require.NotNil(t, md)
			assert.Equal(t, tc.reason, md["reason"])
		})
	}
}
