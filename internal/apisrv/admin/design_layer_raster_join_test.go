package admin

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ОБРАТНЫЙ ПУТЬ ПИКСЕЛЬНОГО КАНАЛА (0355 + X-2): «покрасил → сохранил → открыл заново».
//
// ЧТО БЫЛО СЛОМАНО. `raster_media_id` приезжал ГОЛЫМ ИДЕНТИФИКАТОРОМ, а глагола «прочитай медиа по
// id» в этом контракте нет вовсе. Клиент, получивший число и ничего больше, имеет ровно два хода, и
// оба плохие: запереть пиксельные инструменты или завести холст КОПИЕЙ ПОДЛОЖКИ — а первое же
// сохранение записало бы эту копию поверх вчерашней живописи, безвозвратно, потому что ленты правок
// у слоя нет по контракту. Соединение здесь — это и есть замыкание круга.
//
// ЭТО ТОТ ЖЕ ПРИЁМ, ЧТО У `joinDesignRunInputMedia`, А НЕ ВТОРОЙ. Те же три вопроса решаются теми же
// ответами: живое медиа доезжает картинкой, исчезнувшее ПОМЕЧАЕТСЯ удалённым, запрос ОДИН на весь
// ответ. Разница ровно одна и она в объёме: слой отдаётся по одному, но соединение всё равно
// батчевое — иначе оно не смогло бы обслужить список, и второй, построчный, вариант появился бы в
// тот же день, когда список понадобится.

// TestDesignLayerRasterJoinResolvesTheStoredPainting — живое медиа доезжает картинкой.
//
// МУТАЦИЯ: убрать присваивание `l.RasterMedia` в joinDesignLayerRasterMedia.
func TestDesignLayerRasterJoinResolvesTheStoredPainting(t *testing.T) {
	const (
		painted = 3001
		gone    = 3002
	)

	repo := mocks.NewMockRepository(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media)

	// ОДИН вызов на ВЕСЬ ответ, с полным множеством идентификаторов. Ожидание без `.Maybe()` и без
	// `.Times(n>1)`: мок сам провалит тест, если соединение пойдёт по одному запросу на слой.
	media.EXPECT().
		GetMediaByIds(mock.Anything, mock.MatchedBy(func(ids []int) bool {
			if len(ids) != 2 {
				return false
			}
			seen := map[int]bool{ids[0]: true, ids[1]: true}
			return seen[painted] && seen[gone]
		})).
		Return(map[int]entity.MediaFull{
			painted: {Id: painted, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://example.test/paint.png"}},
		}, nil).
		Once()

	srv := &Server{repo: repo}
	layers := []*pb_common.DesignEditLayer{
		{Id: 7, RasterMediaId: painted},
		{Id: 8, RasterMediaId: gone},
		// Незакрашенный слой не участвует в запросе вовсе: 0 — это «никогда не красили», а не
		// «медиа пропало», и спрашивать про него значило бы покупать строку в IN-списке за ничто.
		{Id: 9, RasterMediaId: 0},
	}

	srv.joinDesignLayerRasterMedia(context.Background(), layers)

	require.NotNil(t, layers[0].GetRasterMedia(),
		"живая живопись обязана приехать картинкой — без неё редактор не может открыть вчерашние пиксели")
	require.Equal(t, "https://example.test/paint.png",
		layers[0].GetRasterMedia().GetMedia().GetFullSize().GetMediaUrl())
	require.False(t, layers[0].GetRasterDeleted(), "живое медиа не удалено")

	require.Nil(t, layers[1].GetRasterMedia(), "исчезнувшее медиа не выдумывается")
	require.True(t, layers[1].GetRasterDeleted(),
		"исчезнувшее медиа обязано быть НАЗВАНО удалённым: «живописи нет» и «живопись пропала» — разные ответы человеку")

	require.Nil(t, layers[2].GetRasterMedia(), "незакрашенный слой не получает картинку")
	require.False(t, layers[2].GetRasterDeleted(),
		"НИКОГДА НЕ КРАСИЛИ — ЭТО НЕ ПРОПАЖА. Пометить пустой слой удалённым значило бы отправить "+
			"человека искать живопись, которой не было")
}

// TestDesignLayerRasterJoinFailureNeitherLiesNorHidesTheLayer — отказ соединения не лжёт и не прячет.
//
// Ошибка чтения медиа значит «мы не знаем», а не «его нет». Пометить `raster_deleted` по неудачному
// запросу — солгать про пропажу файла, который, вероятно, жив; уронить весь ответ — спрятать слой,
// у которого кроме пикселей есть ШТРИХИ, ревизия и идентификатор, и всё это правда без соединения.
//
// МУТАЦИЯ: заменить `return` после ошибки на пометку `l.RasterDeleted = true`.
func TestDesignLayerRasterJoinFailureNeitherLiesNorHidesTheLayer(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media)
	media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).
		Return(nil, context.DeadlineExceeded).Once()

	srv := &Server{repo: repo}
	layers := []*pb_common.DesignEditLayer{{
		Id:            7,
		Rev:           4,
		RasterMediaId: 3001,
		Strokes:       []byte(`[{"k":"pen"}]`),
	}}

	srv.joinDesignLayerRasterMedia(context.Background(), layers)

	require.Equal(t, []byte(`[{"k":"pen"}]`), layers[0].GetStrokes(),
		"штрихи пережили отказ соединения — уронить ответ значило бы спрятать читаемый рисунок")
	require.EqualValues(t, 4, layers[0].GetRev(), "ревизия пережила отказ: без неё сохранение невозможно")
	require.EqualValues(t, 3001, layers[0].GetRasterMediaId(), "идентификатор хранимого — стойкий факт")
	require.False(t, layers[0].GetRasterDeleted(),
		"неудачное чтение не смеет объявлять живопись пропавшей: «мы не знаем» — это не «его нет»")
	require.Nil(t, layers[0].GetRasterMedia(), "и не смеет выдумывать байты")
}

// TestGetDesignEditLayerAnswersWithTheRasterResolved — ПРОВОДКА, А НЕ ХЕЛПЕР.
//
// Две пробы выше монтируют соединение НАПРЯМУЮ и потому зеленеют даже тогда, когда его никто не
// зовёт: сторож у мёртвого кода — один из способов, которыми проба врёт, и в этом репозитории он
// уже был (см. TestListDesignRunsAnswersWithTheInputPicturesJoined).
//
// Эта проба идёт ЧЕРЕЗ ГЛАГОЛ, которым редактор открывает слой: строка приходит из стора голым
// идентификатором, а из ответа обязана выйти картинкой.
//
// МУТАЦИЯ: снять вызов joinDesignLayerRasterMedia из GetDesignEditLayer.
func TestGetDesignEditLayerAnswersWithTheRasterResolved(t *testing.T) {
	const rasterID = 3001

	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Design().Return(design)
	repo.EXPECT().Media().Return(media)

	design.EXPECT().GetEditLayer(mock.Anything, 42, 7).Return(&entity.DesignEditLayer{
		Id:            7,
		TechCardId:    42,
		Rev:           4,
		RasterMediaId: sql.NullInt32{Int32: rasterID, Valid: true},
		Strokes:       entity.RawJSON(`[{"k":"pen"}]`),
	}, nil).Once()

	media.EXPECT().GetMediaByIds(mock.Anything, []int{rasterID}).
		Return(map[int]entity.MediaFull{rasterID: {
			Id:        rasterID,
			MediaItem: entity.MediaItem{FullSizeMediaURL: "https://example.test/paint.png"},
		}}, nil).Once()

	srv := &Server{repo: repo}
	resp, err := srv.GetDesignEditLayer(context.Background(), &pb_admin.GetDesignEditLayerRequest{
		TechCardId: 42, LayerId: 7,
	})
	require.NoError(t, err)

	layer := resp.GetLayer()
	require.EqualValues(t, rasterID, layer.GetRasterMediaId(), "идентификатор по-прежнему на месте")
	require.NotNil(t, layer.GetRasterMedia(),
		"ГЛАГОЛ РЕДАКТОРА обязан отдать пиксели, а не только число: числом холст не заводится, и "+
			"экран остаётся выбирать между отказом и записью копии подложки поверх живописи")
	require.Equal(t, "https://example.test/paint.png",
		layer.GetRasterMedia().GetMedia().GetFullSize().GetMediaUrl())
	require.NotEmpty(t, layer.GetStrokes(), "штрихи этот глагол отдаёт и отдавал")
}

// TestGetDesignEditLayerSurvivesAFailedRasterRead — ОТКАЗ СОЕДИНЕНИЯ НЕ ПРЯЧЕТ ОТВЕТ, через глагол.
//
// Проба выше держит ту же границу на хелпере; эта держит её ТАМ, ГДЕ ОНА ЛОМАЕТСЯ НА ПРАКТИКЕ — в
// глаголе, где один `return nil, err` превратил бы неудавшееся украшение в отказ открыть слой. За
// штрихами редактор сюда и приходит: они читаются, ревизия читается, и рисовать можно — пиксельные
// инструменты запрутся сами, увидев id без байтов и без пометки.
//
// МУТАЦИЯ: сделать ошибку чтения медиа ошибкой всего вызова.
func TestGetDesignEditLayerSurvivesAFailedRasterRead(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Design().Return(design)
	repo.EXPECT().Media().Return(media)

	design.EXPECT().GetEditLayer(mock.Anything, 42, 7).Return(&entity.DesignEditLayer{
		Id: 7, TechCardId: 42, Rev: 4,
		RasterMediaId: sql.NullInt32{Int32: 3001, Valid: true},
		Strokes:       entity.RawJSON(`[{"k":"pen"}]`),
	}, nil).Once()
	media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).
		Return(nil, context.DeadlineExceeded).Once()

	srv := &Server{repo: repo}
	resp, err := srv.GetDesignEditLayer(context.Background(), &pb_admin.GetDesignEditLayerRequest{
		TechCardId: 42, LayerId: 7,
	})
	require.NoError(t, err,
		"неудавшееся украшение не смеет закрывать слой: штрихи и ревизия — это и есть то, за чем сюда идут")
	require.NotEmpty(t, resp.GetLayer().GetStrokes(), "штрихи доехали")
	require.EqualValues(t, 4, resp.GetLayer().GetRev(), "ревизия доехала — без неё нечем сохранять")
	require.False(t, resp.GetLayer().GetRasterDeleted(), "и никто не объявлен пропавшим")
}

// TestGetDesignBandDoesNotBuyTheRasterMedia — ГДЕ СОЕДИНЕНИЯ НАМЕРЕННО НЕТ.
//
// Полоса перечисляет слои БЕЗ штрихов и НЕ рисует пиксели слоя нигде: её плитки — это картинки
// прогонов и партий, а от слоя ей нужен ровно один факт «закрашен или нет», и на него отвечает сам
// `raster_media_id`. Соединение здесь купило бы чтение медиа на КАЖДОЕ открытие вкладки ради URL,
// который никто не рисует.
//
// Проба стоит здесь не ради экономии, а потому что «лишняя работа» — это тоже утверждение, и без
// сторожа оно тихо перестало бы быть правдой: соединение, добавленное в полосу «за компанию»,
// выглядит как улучшение и не краснеет нигде.
func TestGetDesignBandDoesNotBuyTheRasterMedia(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	repo.EXPECT().Design().Return(design)

	// НИ ОДНОГО ожидания на Media(): мок провалит тест на первом же обращении к нему.
	design.EXPECT().GetBand(mock.Anything, 42, mock.Anything).Return(&entity.DesignBand{
		Layers: []entity.DesignEditLayer{{
			Id: 7, TechCardId: 42, Rev: 4,
			RasterMediaId: sql.NullInt32{Int32: 3001, Valid: true},
		}},
	}, nil).Once()

	srv := &Server{repo: repo}
	resp, err := srv.GetDesignBand(context.Background(), &pb_admin.GetDesignBandRequest{TechCardId: 42})
	require.NoError(t, err)
	require.Len(t, resp.GetLayers(), 1)
	require.EqualValues(t, 3001, resp.GetLayers()[0].GetRasterMediaId(),
		"ФАКТ «слой закрашен» полоса отдаёт и обязана отдавать — на нём стоит вся её разница между "+
			"закрашенным холстом и пустым")
	require.Nil(t, resp.GetLayers()[0].GetRasterMedia(),
		"а БАЙТЫ — нет: их здесь некому рисовать, и чтение медиа на каждое открытие вкладки было бы "+
			"куплено ни за что")
}
