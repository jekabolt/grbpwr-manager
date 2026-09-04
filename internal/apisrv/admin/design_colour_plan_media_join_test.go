package admin

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ОБРАТНЫЙ ПУТЬ ПОКРАСКИ: «покрасил → сохранил → вернулся → поправил».
//
// ЧТО БЫЛО СЛОМАНО. `DesignColourMap` вёз ОДИН `media_id`, а глагола «прочитай медиа по id» в этом
// контракте нет вовсе, и PNG карты нигде больше в ответе полосы не лежит — он не кадр, не плита
// верстака и не строка полки, а собственная загрузка. После перезагрузки страницы сохранённую карту
// нечем было ни показать, ни открыть: плитка откатывалась к флэту, «paint ▸» открывал чистый холст
// поверх него, а следующее сохранение записывало эту чистоту поверх покраски. Палитра и назначения
// при этом переживали перезагрузку целыми — экран выглядел рабочим и молча терял ровно ту половину,
// ради которой фича существует.
//
// ЭТО ТОТ ЖЕ ПРИЁМ, ЧТО У joinDesignRunInputMedia И joinDesignLayerRasterMedia, А НЕ ЧЕТВЁРТЫЙ, и
// пробы ниже держат ровно те же три обещания: живое медиа доезжает картинкой, ИСЧЕЗНУВШЕЕ
// помечается, а неудавшееся чтение не смеет ни соврать про пропажу, ни спрятать план.

// designProbePlan — сохранённый план «как из студии»: два покрашенных вида, палитра и назначение.
func designProbePlan(liveMap, goneMap int) *entity.DesignColourPlan {
	return &entity.DesignColourPlan{
		TechCardId: 42,
		Rev:        4,
		Maps: []entity.DesignColourMap{
			{
				MediaId: liveMap, View: entity.DesignViewFront, BaseMediaId: 1,
				Palette: []entity.DesignColourSwatch{{Hex: "#3a7bd5", Px: 40000}},
			},
			{
				MediaId: goneMap, View: entity.DesignViewBack, BaseMediaId: 2,
				Palette: []entity.DesignColourSwatch{{Hex: "#3a7bd5", Px: 31000}},
			},
		},
		Cloths:    []entity.DesignColourCloth{{Hex: "#3a7bd5", AssetId: 9, Parts: "body"}},
		UpdatedBy: "designer",
	}
}

// TestDesignColourPlanJoinResolvesThePaintingAndNamesTheGoneOne — живопись доезжает картинкой, а
// пропавшая НАЗЫВАЕТСЯ пропавшей.
//
// ⚠ ВТОРАЯ ПОЛОВИНА ЭТОЙ ПРОБЫ — ТРЕБОВАНИЕ РЕВЬЮ, А НЕ ВЕЖЛИВОСТЬ. Находка 1 адверсарного ревью
// была ровно про это: состояние, в котором картинки у модели НЕТ, читалось как состояние, в котором
// она есть. Карта, чьё медиа снесли, обязана вернуться так, чтобы её нельзя было принять за живую:
// `media` пустой И `deleted` поднят, а `media_id` на месте — «какая живопись пропала» отвечается
// только по нему.
//
// МУТАЦИЯ: убрать `m.Deleted = true` в ветке ненайденного медиа (оставив `m.Media = nil`).
func TestDesignColourPlanJoinResolvesThePaintingAndNamesTheGoneOne(t *testing.T) {
	const (
		painted = 4001
		gone    = 4002
	)

	repo := mocks.NewMockRepository(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media)

	// ОДИН вызов на ВЕСЬ план, с полным множеством номеров. Ожидание без `.Maybe()` и без
	// `.Times(n>1)`: мок сам провалит тест, если соединение пойдёт по запросу на карту.
	media.EXPECT().
		GetMediaByIds(mock.Anything, mock.MatchedBy(func(ids []int) bool {
			if len(ids) != 2 {
				return false
			}
			seen := map[int]bool{ids[0]: true, ids[1]: true}
			return seen[painted] && seen[gone]
		})).
		Return(map[int]entity.MediaFull{
			painted: {Id: painted, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://example.test/map-front.png"}},
		}, nil).
		Once()

	srv := &Server{repo: repo}
	plan := srv.designColourPlanToPb(context.Background(), designProbePlan(painted, gone))
	require.Len(t, plan.GetMaps(), 2)

	front, back := plan.GetMaps()[0], plan.GetMaps()[1]

	require.NotNil(t, front.GetMedia(),
		"живая карта обязана приехать КАРТИНКОЙ: числом плитку не нарисовать и холст не завести, "+
			"а её PNG нигде больше в ответе полосы не лежит")
	require.Equal(t, "https://example.test/map-front.png",
		front.GetMedia().GetMedia().GetFullSize().GetMediaUrl())
	require.False(t, front.GetDeleted(), "живое медиа не удалено")
	require.EqualValues(t, painted, front.GetMediaId(), "номер остаётся на месте и после соединения")

	require.Nil(t, back.GetMedia(), "исчезнувшее медиа не выдумывается")
	require.True(t, back.GetDeleted(),
		"КАРТА, ЧЬЁ МЕДИА СНЕСЛИ, ОБЯЗАНА БЫТЬ НАЗВАНА ПРОПАВШЕЙ. Без флага у клиента остаётся "+
			"голый номер и пустая картинка — то есть ровно та неотличимость «есть» от «нет», "+
			"которую находка 1 ревью замерила на платном промпте")
	require.EqualValues(t, gone, back.GetMediaId(),
		"а НОМЕР переживает пропажу: «какая живопись исчезла» отвечается только по нему")

	// Остальной документ соединением не тронут: он и без картинок правда.
	require.EqualValues(t, 4, plan.GetRev(), "ревизия цела — без неё следующее сохранение мимо CAS")
	require.Len(t, back.GetPalette(), 1, "палитра пропавшей карты остаётся читаемой")
	require.Len(t, plan.GetCloths(), 1, "назначения остаются на месте")
}

// TestDesignColourPlanJoinFailureNeitherLiesNorHidesThePlan — отказ чтения медиа значит «мы не
// знаем», а не «его нет».
//
// Поднять `deleted` по неудавшемуся запросу значило бы сказать человеку, что его покраска удалена,
// когда она, вероятно, жива, — и он перекрасил бы вид заново поверх целого файла. Уронить весь
// ответ нельзя тем более: кроме картинок в плане есть палитра, назначения и `rev`.
//
// МУТАЦИЯ: заменить `return` после ошибки на пометку `m.Deleted = true`.
func TestDesignColourPlanJoinFailureNeitherLiesNorHidesThePlan(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media)
	media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).
		Return(nil, context.DeadlineExceeded).Once()

	srv := &Server{repo: repo}
	plan := srv.designColourPlanToPb(context.Background(), designProbePlan(4001, 4002))

	require.EqualValues(t, 4, plan.GetRev(), "ревизия пережила отказ соединения")
	require.Len(t, plan.GetCloths(), 1, "назначения пережили — за ними сюда тоже приходят")
	for i, m := range plan.GetMaps() {
		require.NotZero(t, m.GetMediaId(), "номер карты %d — стойкий факт", i)
		require.Nil(t, m.GetMedia(), "неудавшееся чтение не смеет выдумывать байты")
		require.False(t, m.GetDeleted(),
			"и не смеет объявлять живопись пропавшей: «мы не знаем» — это не «его нет»")
	}
}

// TestGetDesignBandAnswersWithTheColourPlanPaintingResolved — ПРОВОДКА, А НЕ ХЕЛПЕР.
//
// Пробы выше монтируют конвертер напрямую и потому зеленели бы даже тогда, когда полоса его не
// зовёт, — сторож у мёртвого кода. Эта проба идёт ЧЕРЕЗ ТОТ ГЛАГОЛ, которым студия перечитывает
// карточку после перезагрузки: план приходит из стора набором чисел, а из ответа обязан выйти
// картинками.
//
// МУТАЦИЯ: снять вызов joinDesignColourPlanMedia из designColourPlanToPb.
func TestGetDesignBandAnswersWithTheColourPlanPaintingResolved(t *testing.T) {
	const (
		painted = 4001
		gone    = 4002
	)

	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Design().Return(design)
	repo.EXPECT().Media().Return(media)

	design.EXPECT().GetBand(mock.Anything, 42, mock.Anything).
		Return(&entity.DesignBand{ColourPlan: designProbePlan(painted, gone)}, nil).Once()
	media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).
		Return(map[int]entity.MediaFull{
			painted: {Id: painted, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://example.test/map-front.png"}},
		}, nil).Once()

	srv := &Server{repo: repo}
	resp, err := srv.GetDesignBand(context.Background(), &pb_admin.GetDesignBandRequest{TechCardId: 42})
	require.NoError(t, err)

	maps := resp.GetColourPlan().GetMaps()
	require.Len(t, maps, 2)
	require.NotNil(t, maps[0].GetMedia(),
		"ПЕРЕЗАГРУЗКА ОБЯЗАНА ВЕРНУТЬ ЖИВОПИСЬ. Без этой картинки плитка откатывается к флэту, "+
			"«paint ▸» открывает чистый холст, и следующее сохранение пишет пустоту поверх покраски")
	require.Equal(t, "https://example.test/map-front.png",
		maps[0].GetMedia().GetMedia().GetFullSize().GetMediaUrl())
	require.Nil(t, maps[1].GetMedia())
	require.True(t, maps[1].GetDeleted(),
		"а снесённая карта возвращается НАЗВАННОЙ пропавшей — состояние, которое клиент не может "+
			"принять за живую карту")
}

// TestTheRunDoorFreezesAColourMapByIdNotByPicture — ЧТО ЕДЕТ В ИСТОРИЮ.
//
// `media`/`deleted` — поля ЧТЕНИЯ, и клиент, собравший рецепт прогона из только что прочитанного
// плана, вернёт их у двери не по злому умыслу, а потому что честно echo'ит полученное. В
// замороженные `params` они попасть не должны: контракт говорит дословно «IDS ARE STORED, MediaFull
// IS SERVED» — объекты переезжают, и URL, вмёрзший в историю, однажды станет показывать чужую
// картинку в строке, которую уже оплатили.
//
// ⚠ И ЭТО ЖЕ ДЕЛАЕТ ПУСТОТУ ЭТИХ ПОЛЕЙ НА ЗАМОРОЖЕННОЙ КОПИИ СВОЙСТВОМ, А НЕ СОГЛАШЕНИЕМ: раз
// записать их в `params` нельзя, пустой `media` на рецепте прогона всегда значит «картинки здесь
// нет» — ровно то же, что и на плане. Ни одно из двух мест, где живёт это сообщение, не читается
// как «картинка есть».
//
// МУТАЦИЯ: снять цикл, стирающий `m.Media`/`m.Deleted`, в designEffectiveParams.
func TestTheRunDoorFreezesAColourMapByIdNotByPicture(t *testing.T) {
	spoken := designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
		c.ColourMaps[0].Media = &pb_common.MediaFull{
			Id: 20, Media: &pb_common.MediaItem{
				FullSize: &pb_common.MediaInfo{MediaUrl: "https://example.test/map-front.png"},
			},
		}
		c.ColourMaps[0].Deleted = true
	})

	params, err := designEffectiveParams(spoken, nil)
	require.NoError(t, err)

	frozen := params.GetColour().GetColourMaps()
	require.Len(t, frozen, 1)
	require.EqualValues(t, 20, frozen[0].GetMediaId(),
		"НОМЕР — это и есть то, что морозится: по нему снимок соберёт картинку в тот день, когда её "+
			"попросят, а не в тот, когда её положили")
	require.Nil(t, frozen[0].GetMedia(),
		"а КАРТИНКА в историю не едет: замороженный URL однажды перестанет быть той картинкой, и "+
			"оплаченная строка будет уверенно показывать чужую")
	require.False(t, frozen[0].GetDeleted(),
		"и флаг чтения тоже: он отвечает на вопрос «жив ли файл СЕЙЧАС», а замороженная копия "+
			"отвечать на него не может вовсе")

	// Сказанное клиентом не тронуто: правится КЛОН, а входящее сообщение видят перехватчики и логи.
	require.NotNil(t, spoken.GetColour().GetColourMaps()[0].GetMedia(),
		"дверь чистит клон, а не чужую память")
}
