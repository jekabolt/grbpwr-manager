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
		name         string
		pictures     int
		construction bool
		want         string
	}{
		// ⚠ КРУГ 21: СТРУКТУРНАЯ БАЗА 0.035 → 0.129, И ЭТО НЕ «УТОЧНЕНИЕ», А ПОЧИНКА МОЛЧАНИЯ.
		// Литерал 0.035 был посчитан при потолке ответа в 3000 токенов; потолок подняли до 8000
		// (designConstructionMaxTokens), купив ещё 5000 выходных токенов, а цена не двинулась —
		// прибавка была оценена в НОЛЬ. Теперь база выведена: 3k входных по $3/M ($0.009) плюс
		// ВЕСЬ потолок ответа по выходному тарифу $15/M ($0.12).
		//
		// ⚠ ЧИСЛА ВЫПИСАНЫ ВСЛУХ, А НЕ ВЗЯТЫ У ФОРМУЛЫ, И ИМЕННО ПОЭТОМУ ОНИ ЗДЕСЬ ЧТО-ТО ЗНАЧАТ:
		// сверка формулы с собою зеленела бы и на той версии, где потолок и цена снова разъехались.
		// Инвариант «цена покрывает потолок» проверяет отдельная проба —
		// TestConstructionBasePricesTheWholeAnswerCeiling.
		{"структурный: пустая доска — только промпт и ответ", 0, true, "0.129"},
		{"структурный: одна картинка", 1, true, "0.135"},
		{"структурный: потолок клиента", 12, true, "0.201"},
		{"структурный: потолок транспорта", 16, true, "0.225"},
		{"структурный: отрицательное число читается как ноль", -3, true, "0.129"},

		// ⚠ ПРОЗА ПЛАТИТ ПРЕЖНЮЮ БАЗУ, И БЕЗ ЭТОГО РЯДА ПОЧИНКА НЕ ДОКАЗАНА. Прогон со снятым
		// флагом не покупает НИ колорвеев в ответе, НИ словаря цвета в запросе — а первая правка
		// круга 20 двинула единственную базу и заставила его платить +17% за то, чего он не
		// просил. Оценка ЖЕ И СПИСЫВАЕТСЯ, значит это не «неточный отчёт», а неверная строка
		// регистра на каждом нажатии старого клиента.
		{"проза: пустая доска", 0, false, "0.03"},
		{"проза: одна картинка", 1, false, "0.036"},
		{"проза: потолок клиента", 12, false, "0.102"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			est := designDraftIdeaEstimate(tc.pictures, tc.construction)
			require.True(t, est.Valid, "черновик обязан резервировать хоть что-то: NULL проходит мимо дневного счёта")
			require.Equal(t, tc.want, est.Decimal.String())
		})
	}

	// СТРУКТУРНАЯ ВЕТКА ДОРОЖЕ ПРОЗАИЧЕСКОЙ. Равенство здесь означало бы, что разделение баз
	// потеряно и одна из двух веток снова платит чужую цену.
	//
	// ⚠ РАЗНИЦА БОЛЬШЕ НЕ «ОДИН СПИСОК» — И ЭТО ГЛАВНОЕ, ЧТО ЗДЕСЬ СКАЗАНО. Структурная ветка
	// кладёт на провод ПОТОЛОК ОТВЕТА, прозаическая не кладёт никакого; поэтому разница — это цена
	// целого разрешённого ответа по выходному тарифу плюс словарь цвета, а не приписка к базе.
	prose, structural := designDraftIdeaEstimate(4, false), designDraftIdeaEstimate(4, true)
	require.True(t, structural.Decimal.GreaterThan(prose.Decimal),
		"потолок ответа и словарь цвета покупает только структурная ветка")
	require.Equal(t, "0.099", structural.Decimal.Sub(prose.Decimal).String(),
		"разница веток — это названная величина, а не дрейф")

	// БАЗА В ТАБЛИЦЕ РОДОВ — ПОТОЛОК ДВУХ, А НЕ ОДНА ИЗ НИХ. designEstimateFor отвечает на вопрос
	// «сколько может стоить прогон этого рода», у которого формы ещё нет, и занизив его, мы дали
	// бы двери резерв меньше траты.
	table := designPriceEstimate[entity.DesignRunKindDraftIdea]
	require.Equal(t, table.String(), designDraftIdeaEstimate(0, true).Decimal.String())
	require.True(t, table.GreaterThanOrEqual(designDraftIdeaEstimate(0, false).Decimal))
}

// ХЕНДЛЕР РЕЗЕРВИРУЕТ ИМЕННО ЭТО ЧИСЛО, И СЧИТАЕТ ЕГО ПО УЕХАВШИМ КАДРАМ.
//
// Формула, которую никто не зовёт, — это не цена, а комментарий; проба закрывает разрыв между
// таблицей и строкой прогона.
func TestDraftDesignIdeaReservesThePictureScaledPrice(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "an idea")
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err)

	// ⚠ draftRequest() ИДЁТ БЕЗ ФЛАГА, ТО ЕСТЬ ЭТО ПРОЗАИЧЕСКИЙ ПРОГОН, и цена у него прозаическая.
	// Число выписано ВСЛУХ, а не взято у самой формулы: сверка формулы с собою зеленела бы и на той
	// версии, где проза платит за колорвеи, которых в её ответе нет.
	want := designDraftIdeaEstimate(1, false)
	require.Equal(t, "0.036", want.Decimal.String(),
		"проза с одной картинкой: база 0.03 + один кадр 0.006")
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
