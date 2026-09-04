package admin

// ПРОБЫ ФАЗЫ 0: СНИМОК ЧЕРНОВИКА НАЗЫВАЕТ СВОИ КАРТИНКИ, СТОРОЖ ПЕРЕСТАЁТ СТОЯТЬ НА ОДНИХ СЛОВАХ,
// А ЦЕНА НАЧИНАЕТ ЗАВИСЕТЬ ОТ ЧИСЛА ПРОЧИТАННЫХ КАДРОВ.
//
// ЧТО ЗДЕСЬ ОХРАНЯЕТСЯ И ПОЧЕМУ ЭТО ТРИ ПРОБЫ, А НЕ ОДНА. Три утверждения связаны одной причиной
// (картинки доски уезжают в платный вызов), но ломаются ПОРОЗНЬ:
//
//  1. СНИМОК. `design_run.inputs` — единственная запись о том, что́ было послано; без media_ids он
//     утверждал, что доска из одних картинок не послала НИЧЕГО. История, врущая про потраченные
//     деньги, хуже отказа — ровно поэтому дверь была закрыта, и ровно поэтому её нельзя открывать
//     раньше поля.
//  2. СТОРОЖ. Он обязан спрашивать «уехало ли хоть что-нибудь», а не «есть ли слова»; и считать
//     УЕХАВШИЕ картинки, а не плитки доски — доска из трёх удалённых строк медиа посылает ноль.
//  3. ДЕНЬГИ. Каждая картинка — входные токены; плоская оценка занижала резерв И ФАКТ (чат-эндпоинт
//     возвращает токены, а не деньги, поэтому оценка же и списывается).
//
// МУТАЦИИ, КОТОРЫМИ ПРОВЕРЕНО (по ЧИСЛУ ИСПОЛНЕННЫХ ИСХОДОВ, а не по коду возврата):
//   - писать в mood.MediaIds список boardIDs вместо attachedIDs → краснеет проба выживших;
//   - вернуть сторож к `mood == nil` → краснеет проба доски из одних картинок;
//   - вернуть designEstimateFor(draft_idea, 1) на место designDraftIdeaEstimate → краснеет цена.

import (
	"database/sql"
	"net/http"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// draftSnapshot разбирает снимок, который хендлер отдал стору. Читается ТЕМ ЖЕ разбором, каким
// его прочтёт панель прогона: самодельный разбор JSON согласился бы с формой, которой на проводе
// нет (тот же довод, что у designFrozenCallout).
func draftSnapshot(t *testing.T, rig *draftRig) *pb_common.DesignInputSnapshot {
	t.Helper()
	require.NotEmpty(t, rig.started.Inputs, "прогон открыт без снимка входов вовсе")
	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.started.Inputs, snap))
	return snap
}

// СНИМОК НАЗЫВАЕТ КАЖДУЮ УЕХАВШУЮ КАРТИНКУ, В ПОРЯДКЕ ОТПРАВКИ.
//
// Порядок — это и есть привязка: промпт нумерует выноски «picture N» по нему, и снимок остаётся
// единственной записью о том, что́ значило «picture 2» в тот день.
func TestDraftDesignIdeaSnapshotNamesThePicturesItSent(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "A boxy coat with a storm flap.")
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err)

	snap := draftSnapshot(t, rig)
	require.Equal(t, []int32{designBoardMediaID}, snap.GetMood().GetMediaIds(),
		"снимок обязан назвать картинку, за которую заплачено входными токенами")
	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: слова доски по-прежнему в снимке — иначе «id есть» зеленело бы на
	// снимке, потерявшем всё остальное.
	require.Contains(t, snap.GetMood().GetNote(), "MOODWORDS-do-not-generate")
}

// НУМЕРУЮТСЯ ВЫЖИВШИЕ, А НЕ ЖЕЛАЕМЫЕ: плитка, чьей строки медиа больше нет, в снимок не попадает.
//
// Без этого снимок утверждал бы картинку, которой модель не видела, а рерун по такому снимку
// послал бы не то, что послал оригинал.
func TestDraftDesignIdeaSnapshotHoldsOnlyTheSurvivors(t *testing.T) {
	card := designMoodCard()
	const goneID = 902
	card.Media = append(card.Media,
		entity.TechCardMediaItem{MediaId: goneID, Category: entity.TechCardMediaCategoryMoodboard})

	// Медиа-стор знает про доску, но строки `goneID` у него нет — обычное состояние после
	// удаления кадра.
	rig := newDraftRigWithCard(t, http.StatusOK, "an idea", card,
		[]int{designBoardMediaID, goneID},
		map[int]entity.MediaFull{designBoardMediaID: {
			Id:        designBoardMediaID,
			MediaItem: entity.MediaItem{FullSizeMediaURL: designBoardMediaURL},
		}})
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err)

	require.Equal(t, []string{designBoardMediaURL}, rig.stub.imageURLs(t),
		"пропавшая строка медиа не едет на провод")
	require.Equal(t, []int32{designBoardMediaID}, draftSnapshot(t, rig).GetMood().GetMediaIds(),
		"снимок обязан совпадать с проводом: он и есть запись о том, что было послано")
}

// ДОСКА ИЗ ОДНИХ КАРТИНОК ТЕПЕРЬ ЗАКОННА.
//
// Это и есть цель фазы 0: сторож стоял на СЛОВАХ, потому что снимок не умел хранить картинки.
// Умеет — значит отказывать больше не за что, и человек с двенадцатью кадрами и пустым описанием
// получает черновик вместо «доска пока ничего не говорит».
func TestDraftDesignIdeaAcceptsAPicturesOnlyBoard(t *testing.T) {
	card := &entity.TechCard{}
	card.Name = "wordless board"
	card.Fit = sql.NullString{String: "oversized", Valid: true}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: designBoardMediaID, Category: entity.TechCardMediaCategoryMoodboard},
	}

	rig := newDraftRigWithCard(t, http.StatusOK, "A boxy coat.", card,
		[]int{designBoardMediaID},
		map[int]entity.MediaFull{designBoardMediaID: {
			Id:        designBoardMediaID,
			MediaItem: entity.MediaItem{FullSizeMediaURL: designBoardMediaURL},
		}})
	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err, "доска из одних картинок больше не отказ")
	require.Equal(t, int32(55), resp.GetRun().GetId())

	snap := draftSnapshot(t, rig)
	require.Equal(t, []int32{designBoardMediaID}, snap.GetMood().GetMediaIds())
	require.Empty(t, snap.GetMood().GetNote(), "слов не было — снимок и не должен их выдумывать")
	require.Equal(t, []string{designBoardMediaURL}, rig.stub.imageURLs(t))
}

// НИ КАРТИНОК, НИ СЛОВ — ОТКАЗ, И СЧИТАЮТСЯ УЕХАВШИЕ КАРТИНКИ.
//
// Доска из трёх плиток, чьи строки медиа удалены, посылает НОЛЬ изображений: сторож по списку
// желаний пропустил бы её в платный вызов ни о чём.
func TestDraftDesignIdeaRefusesABoardThatSendsNothing(t *testing.T) {
	card := &entity.TechCard{}
	card.Name = "a board of ghosts"
	card.Media = []entity.TechCardMediaItem{
		{MediaId: designBoardMediaID, Category: entity.TechCardMediaCategoryMoodboard},
	}

	// СТЕНД БЕЗ ЕДИНОГО ОЖИДАНИЯ НА СТОРЕ ПОЛОСЫ — ЭТО ЧАСТЬ УТВЕРЖДЕНИЯ. Отказ обязан прийти ДО
	// StartRun; строгий mockery покраснеет на любом незаявленном вызове, то есть «стор не тронут»
	// здесь измерено, а не заявлено.
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	design := mocks.NewMockDesign(t)
	media := mocks.NewMockMedia(t)
	repo.EXPECT().TechCards().Return(cards).Maybe()
	repo.EXPECT().Design().Return(design).Maybe()
	repo.EXPECT().Media().Return(media).Maybe()
	designStubNoDisplayOnly(design)
	cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).Return(card, nil).Once()
	media.EXPECT().GetMediaByIds(mock.Anything, []int{designBoardMediaID}).
		Return(map[int]entity.MediaFull{}, nil).Once()
	srv := &Server{
		repo: repo, designGenerationEnabled: true,
		aiOps: openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: "http://127.0.0.1:1"}),
	}

	_, err := srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Contains(t, err.Error(), "there is nothing to read")
}

// ЦЕНА РАСТЁТ С ЧИСЛОМ КАРТИНОК, И БАЗА БЕРЁТСЯ ИЗ ТОЙ ЖЕ ТАБЛИЦЫ, ЧТО У ОСТАЛЬНЫХ РОДОВ.
//
// Оценка здесь не только резервирует, но и СПИСЫВАЕТСЯ (чат-эндпоинт возвращает токены, а не
// деньги), поэтому заниженное число врёт про потраченное навсегда.
func TestDraftIdeaEstimateScalesWithThePictures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pictures int
		want     string
	}{
		{"пустая доска — только промпт и ответ", 0, "0.03"},
		{"одна картинка", 1, "0.036"},
		{"потолок клиента", 12, "0.102"},
		{"потолок транспорта", 16, "0.126"},
		{"отрицательное число читается как ноль", -3, "0.03"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			est := designDraftIdeaEstimate(tc.pictures)
			require.True(t, est.Valid, "черновик обязан резервировать хоть что-то: NULL проходит мимо дневного счёта")
			require.Equal(t, tc.want, est.Decimal.String())
		})
	}

	// БАЗА — ЭТО СТРОКА ОБЩЕЙ ТАБЛИЦЫ, А НЕ ВТОРОЕ ЧИСЛО РЯДОМ. Разойдясь, они дали бы роду
	// draft_idea две цены, и правка одной оставила бы вторую врать.
	require.Equal(t, designPriceEstimate[entity.DesignRunKindDraftIdea].String(),
		designDraftIdeaEstimate(0).Decimal.String())
}

// ХЕНДЛЕР РЕЗЕРВИРУЕТ ИМЕННО ЭТО ЧИСЛО, И СЧИТАЕТ ЕГО ПО УЕХАВШИМ КАДРАМ.
//
// Формула, которую никто не зовёт, — это не цена, а комментарий; проба закрывает разрыв между
// таблицей и строкой прогона.
func TestDraftDesignIdeaReservesThePictureScaledPrice(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "an idea")
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err)

	want := designDraftIdeaEstimate(1)
	require.True(t, rig.started.PriceEstimate.Valid)
	require.Equal(t, want.Decimal.String(), rig.started.PriceEstimate.Decimal.String(),
		"одна картинка доски обязана быть в резерве прогона")
	require.Len(t, rig.finished, 1)
	require.Equal(t, want.Decimal.String(), rig.finished[0].Price.Decimal.String(),
		"списывается та же оценка: чат-эндпоинт денег не возвращает")
}

// СТАРЫЙ ЗАПРОС — СТАРЫЙ ОТВЕТ. Флаг отсутствует, значит ответ обязан остаться прозой, а поле
// `construction` в ответе — пустым: клиент, который о нём не знает, разбирает `output_text`.
func TestDraftDesignIdeaWithoutTheFlagStillAnswersProse(t *testing.T) {
	const prose = "DESCRIPTION\nA boxy coat.\n\nDESIGN ASPECTS\n- storm flap"
	rig := newDraftRig(t, http.StatusOK, prose)
	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId: designRunCardID, ClientRequestId: "44444444-4444-4444-4444-444444444444",
	})
	require.NoError(t, err)
	require.Nil(t, resp.GetConstruction(), "без флага структурного ответа нет и быть не должно")
	require.Equal(t, prose, rig.completedText,
		"проза сохраняется дословно: клиент режет её по трём заголовкам")
	require.NotContains(t, rig.stub.body, "json_object",
		"старый путь не просит json-режим — байты запроса обязаны остаться прежними")
}
