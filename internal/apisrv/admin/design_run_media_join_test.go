package admin

import (
	"context"
	"sort"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// S-11: «в деталке рана у референсов ВСЕГДА no image».
//
// Контракт обещает у `DesignInputRef.media` и `DesignInputSlot.media` соединение по `media_id`
// «at read time», и это соединение не было написано нигде — поле не заполнялось НИ У ОДНОГО
// прогона НИ ДЛЯ КОГО, а флаг `deleted` не мог стать истинным в принципе.
//
// Проба утверждает ТРИ вещи сразу, и каждая из них умеет быть ложной:
//   - живое медиа доезжает картинкой, а не только идентификатором;
//   - исчезнувшее медиа помечается `deleted`, а не притворяется живым с пустой картинкой —
//     иначе «какой вход пропал» остаётся без ответа;
//   - запрос ОДИН на весь ответ. Без этого условия соединение прошло бы и по одному вызову на
//     строку, то есть сотней обращений на экран истории; батч проверяется тем, что мок ждёт
//     ровно один вызов с полным множеством идентификаторов.
func TestDesignRunSnapshotJoinsInputMedia(t *testing.T) {
	const (
		aliveRef  = 101
		deadRef   = 102
		aliveSlot = 103
	)

	repo := mocks.NewMockRepository(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media)

	// Одно ожидание без `.Maybe()` и без `.Times(n>1)`: мок сам провалит тест, если вызовов
	// окажется больше одного или множество идентификаторов будет неполным.
	media.EXPECT().
		GetMediaByIds(mock.Anything, mock.MatchedBy(func(ids []int) bool {
			got := append([]int(nil), ids...)
			sort.Ints(got)
			return len(got) == 3 && got[0] == aliveRef && got[1] == deadRef && got[2] == aliveSlot
		})).
		Return(map[int]entity.MediaFull{
			aliveRef:  {Id: aliveRef, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://example.test/ref.webp"}},
			aliveSlot: {Id: aliveSlot, MediaItem: entity.MediaItem{FullSizeMediaURL: "https://example.test/slot.webp"}},
		}, nil).
		Once()

	srv := &Server{repo: repo}
	runs := []*pb_common.DesignRun{{
		Id: 7,
		Inputs: &pb_common.DesignInputSnapshot{
			Refs: []*pb_common.DesignInputRef{
				{MediaId: aliveRef, Role: "front"},
				{MediaId: deadRef, Role: "back"},
			},
			Slots: []*pb_common.DesignInputSlot{
				{MediaId: aliveSlot, ViewKey: "front"},
			},
		},
	}}

	srv.joinDesignRunInputMedia(context.Background(), runs)

	in := runs[0].GetInputs()
	require.NotNil(t, in.GetRefs()[0].GetMedia(),
		"живой референс обязан приехать картинкой — ровно этого не было, и человек видел «no image»")
	require.False(t, in.GetRefs()[0].GetDeleted(), "живое медиа не удалено")

	require.Nil(t, in.GetRefs()[1].GetMedia(), "исчезнувшее медиа не выдумывается")
	require.True(t, in.GetRefs()[1].GetDeleted(),
		"исчезнувшее медиа обязано быть НАЗВАНО удалённым: иначе «какой вход пропал» без ответа")

	require.NotNil(t, in.GetSlots()[0].GetMedia(), "плита слота — тот же снимок и то же обещание")
	require.False(t, in.GetSlots()[0].GetDeleted(), "живая плита не удалена")
}

// Отказ соединения НЕ отменяет ответ и НЕ лжёт про удаление.
//
// Ошибка чтения медиа значит «мы не знаем», а не «его нет». Пометить вход удалённым по неудачному
// запросу — солгать про пропажу; уронить весь ответ — спрятать прогон, у которого кроме картинок
// есть слова, попытки и деньги.
func TestDesignRunMediaJoinFailureNeitherLiesNorHidesTheRun(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media)
	media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).
		Return(nil, context.DeadlineExceeded).Once()

	srv := &Server{repo: repo}
	runs := []*pb_common.DesignRun{{
		Id:     7,
		Ask:    "draw the front",
		Inputs: &pb_common.DesignInputSnapshot{Refs: []*pb_common.DesignInputRef{{MediaId: 101}}},
	}}

	srv.joinDesignRunInputMedia(context.Background(), runs)

	require.Equal(t, "draw the front", runs[0].GetAsk(), "строка истории пережила отказ соединения")
	require.False(t, runs[0].GetInputs().GetRefs()[0].GetDeleted(),
		"неудачное чтение не смеет объявлять вход удалённым")
}

// ПРОВОДКА, А НЕ ХЕЛПЕР. Две пробы выше монтируют соединение НАПРЯМУЮ и потому зеленеют даже
// тогда, когда его никто не зовёт: мутация «снять вызов из ListDesignRuns» не покрасила ни одной
// из них. Сторож у мёртвого кода — один из способов, которыми проба врёт, и здесь он был.
//
// Эта проба идёт ЧЕРЕЗ ГЛАГОЛ: строка приходит из стора без картинок, а из ответа обязана выйти
// с картинкой. Снимите вызов — она краснеет.
func TestListDesignRunsAnswersWithTheInputPicturesJoined(t *testing.T) {
	const refID = 101

	repo := mocks.NewMockRepository(t)
	design := mocks.NewMockDesign(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Design().Return(design)
	repo.EXPECT().Media().Return(media)

	design.EXPECT().ListRuns(mock.Anything, mock.Anything).Return(&entity.DesignRunPageResult{
		Runs: []entity.DesignRun{{
			Id:         7,
			TechCardId: 38,
			Kind:       "flat",
			Status:     "done",
			Inputs:     entity.RawJSON(`{"refs":[{"media_id":101,"role":"front"}]}`),
		}},
	}, nil).Once()

	media.EXPECT().GetMediaByIds(mock.Anything, []int{refID}).
		Return(map[int]entity.MediaFull{refID: {
			Id:        refID,
			MediaItem: entity.MediaItem{FullSizeMediaURL: "https://example.test/ref.webp"},
		}}, nil).Once()

	srv := &Server{repo: repo}
	resp, err := srv.ListDesignRuns(context.Background(), &pb_admin.ListDesignRunsRequest{TechCardId: 38})
	require.NoError(t, err)
	require.Len(t, resp.GetRuns(), 1)

	refs := resp.GetRuns()[0].GetInputs().GetRefs()
	require.Len(t, refs, 1, "снимок обязан доехать со своим референсом")
	require.NotNil(t, refs[0].GetMedia(),
		"ГЛАГОЛ обязан отдать картинку, а не только идентификатор — иначе экран рисует «no image»")
}
