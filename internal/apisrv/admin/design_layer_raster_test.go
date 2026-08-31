package admin

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ШОВ ХЕНДЛЕРА И СТОРА ДЛЯ ПИКСЕЛЬНОГО КАНАЛА СЛОЯ (0355, X-1).
//
// ⚠ ПОЛЕ, ЗАВЕДЁННОЕ В КОНТРАКТЕ И НЕ ДОНЕСЁННОЕ ДО СТОРА, — САМАЯ ТИХАЯ ИЗ ВОЗМОЖНЫХ ПОТЕРЬ. Ни
// компилятор, ни схема, ни один живой тест базы её не увидят: сохранение вернёт OK, слой честно
// поднимет ревизию, и только пиксели никуда не доедут. Ровно этой формы дефект уже стоил этому
// репозиторию круга (`client_request_id` доезжал до стора и НЕ ЧИТАЛСЯ), поэтому шов проверяется
// отдельной пробой, а не «покрывается» пробой базы.

// saveLayerRig — мок стора, запоминающий ТО, ЧТО ХЕНДЛЕР ЕМУ ОТДАЛ.
type saveLayerRig struct {
	srv  *Server
	sent *entity.DesignEditLayerSave
}

func newSaveLayerRig(t *testing.T) *saveLayerRig {
	t.Helper()
	rig := &saveLayerRig{}
	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	repo.EXPECT().Design().Return(design).Maybe()
	design.EXPECT().SaveEditLayer(mock.Anything, mock.AnythingOfType("entity.DesignEditLayerSave")).
		Run(func(_ context.Context, req entity.DesignEditLayerSave) {
			cp := req
			rig.sent = &cp
		}).
		Return(&entity.DesignEditLayer{Id: 7, TechCardId: 42, Rev: 4}, nil).Maybe()
	rig.srv = &Server{repo: repo}
	return rig
}

// TestSaveDesignEditLayerCarriesThePixelChannel — ЦИТАТА шва: оба поля растра доезжают до стора
// ровно теми, какими пришли с провода.
//
// ТРИ СОСТОЯНИЯ ПРОВЕРЯЮТСЯ ВСЕ ТРИ, потому что дефект «поле не донесли» выглядит одинаково с
// законным «поле не прислали»: только различение состояний отличает донесённое молчание от
// потерянного идентификатора.
//
// МУТАЦИЯ: убрать `RasterMediaId:` (или `ClearRaster:`) из литерала в SaveDesignEditLayer.
func TestSaveDesignEditLayerCarriesThePixelChannel(t *testing.T) {
	for _, c := range []struct {
		name       string
		req        *pb_admin.SaveDesignEditLayerRequest
		wantRaster int
		wantClear  bool
	}{
		{
			name: "a painted save names its media",
			req: &pb_admin.SaveDesignEditLayerRequest{
				TechCardId: 42, LayerId: 7, ExpectedRev: 3, RasterMediaId: 91,
			},
			wantRaster: 91,
		},
		{
			// ⚠ МОЛЧАНИЕ ОБЯЗАНО ДОЕХАТЬ КАК МОЛЧАНИЕ. Прочитай хендлер отсутствие поля как
			// «очистить» — и автосейв, тронувший одни штрихи, стёр бы человеку всю роспись.
			name: "a stroke-only save says nothing about the pixels",
			req: &pb_admin.SaveDesignEditLayerRequest{
				TechCardId: 42, LayerId: 7, ExpectedRev: 3, Strokes: []byte(`[{"k":"pen"}]`),
			},
		},
		{
			name: "clearing the pixels is said out loud",
			req: &pb_admin.SaveDesignEditLayerRequest{
				TechCardId: 42, LayerId: 7, ExpectedRev: 3, ClearRaster: true,
			},
			wantClear: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newSaveLayerRig(t)
			_, err := rig.srv.SaveDesignEditLayer(context.Background(), c.req)
			require.NoError(t, err)
			require.NotNil(t, rig.sent, "the handler must reach the store")
			require.Equal(t, c.wantRaster, rig.sent.RasterMediaId)
			require.Equal(t, c.wantClear, rig.sent.ClearRaster)
			// Контроль: остальные поля не перепутаны местами с новыми.
			require.Equal(t, 42, rig.sent.TechCardId)
			require.Equal(t, 7, rig.sent.LayerId)
			require.Equal(t, 3, rig.sent.ExpectedRev)
		})
	}
}

// TestDesignLayerToPbServesThePixelChannel — обратное направление того же шва.
//
// Растр отдаётся ВЕЗДЕ, включая список полосы, и это не противоречие с тем, что `strokes`
// придерживаются: штрихи — до 512 KB на слой, растр — голый id. Полоса, которая его не отдаёт, не
// может отличить закрашенный холст от пустого, а редактор — решить, с чего начинать рисовать.
//
// МУТАЦИЯ: убрать `RasterMediaId:` из designLayerToPb.
func TestDesignLayerToPbServesThePixelChannel(t *testing.T) {
	l := entity.DesignEditLayer{
		Id: 7, TechCardId: 42, Rev: 4,
		RasterMediaId: sql.NullInt32{Int32: 91, Valid: true},
		Strokes:       entity.RawJSON(`[{"k":"pen"}]`),
	}
	withStrokes := designLayerToPb(l, true)
	require.EqualValues(t, 91, withStrokes.GetRasterMediaId())

	// В СПИСКЕ — ТОЖЕ. Здесь штрихи не едут, а пиксельный канал обязан.
	listed := designLayerToPb(l, false)
	require.EqualValues(t, 91, listed.GetRasterMediaId())
	require.Empty(t, listed.GetStrokes(), "the band does not ship strokes")

	// НЕЗАКРАШЕННЫЙ СЛОЙ ЕДЕТ НУЛЁМ. Колонка NULL сканируется драйвером в {Int32: 0, Valid: false},
	// поэтому `.Int32` — законное чтение, и оно то же самое, каким уже читаются base_media_id и
	// source_media_id рядом. Отдельная проверка `.Valid` здесь сделала бы одно из трёх однородных
	// полей непохожим на два других, ничего при этом не закрыв.
	blank := designLayerToPb(entity.DesignEditLayer{Id: 8}, true)
	require.EqualValues(t, 0, blank.GetRasterMediaId())
	require.False(t, entity.DesignEditLayer{}.RasterMediaId.Valid)
}
