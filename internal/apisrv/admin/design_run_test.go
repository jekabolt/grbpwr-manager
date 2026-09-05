package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ПРОБЫ ГЕНЕРАТИВНЫХ ХЕНДЛЕРОВ.
//
// ЧТО ОНИ МОГУТ ДОКАЗАТЬ, А ЧТО НЕТ, названо вслух. Стор здесь замокан — значит резерв денег,
// SERIALIZABLE-транзакция и идемпотентность по client_request_id ими НЕ проверяются: это
// собственность internal/store/design и живой базы. Проверяется то, что живёт в хендлере и
// ломается молча: ворота (флаг, W-13), СБОРКА ВХОДОВ (W-15), источник входов рерана (W-7) и
// имена ключей в снимке.

const designRunCardID = 41

// designRunCtx — полный доступ плюс имя автора: без авторизации хендлеры отказывают закрыто, а
// без имени неразличимы два автора на одной строке.
func designRunCtx() context.Context {
	return authsrv.PutAdminUsername(fullAccessCtx(), "designer")
}

// designStubNoDisplayOnly — СТОР ПОЛОСЫ ДЛЯ РИГОВ, КОТОРЫЕ ПРО «ТОЛЬКО ДЛЯ ПОКАЗА» НЕ ПРОБУЮТ.
//
// ⚠ ЗАЧЕМ ОН ПОНАДОБИЛСЯ И ЧТО ЭТО ГОВОРИТ — тот же довод, что у designStubAnyMedia. С круга 18
// (D-24) КАЖДЫЙ старт прогона и черновика спрашивает у стора, не помечен ли какой-нибудь из его
// входов «только для показа» (designRefuseDisplayOnlyInputs → MediaHeldDisplayOnly), поэтому
// всякий стенд StartDesignRun/DraftDesignIdea обязан ждать этот вызов — иначе строгий mockery
// роняет пробу на незаявленном вызове. Это цена решения «дверь до денег, а не фильтр в отборе»,
// и она тут видна: 82 пробы покраснели одним вызовом, пока его не заявили.
//
// ОТДАЁТСЯ «НИЧЕГО НЕ ПОМЕЧЕНО», И ЭТО ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ, А НЕ ПУСТАЯ ЗАГЛУШКА: дверь
// исполняется на каждом из этих стендов и отказывать не должна; отказ, который она начнёт выдавать
// по ошибке, покраснеет здесь же. Пробы САМОЙ двери ставят своё ожидание (design_display_only_test.go).
func designStubNoDisplayOnly(design *mocks.MockDesign) {
	design.EXPECT().MediaHeldDisplayOnly(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
}

// ─────────────────────── стенд ───────────────────────

type designRunRig struct {
	srv    *Server
	repo   *mocks.MockRepository
	cards  *mocks.MockTechCards
	design *mocks.MockDesign
	// sent — то, что хендлер отдал стору. Именно оно и есть предмет большинства проб: снимок
	// входов замерзает навсегда, и всё, что в него попало, попало туда отсюда.
	sent *entity.DesignRunStart
}

// newDesignRunRig собирает сервер с ВКЛЮЧЁННЫМ флагом: пробы ворот включают его сами.
func newDesignRunRig(t *testing.T, card *entity.TechCard, band *entity.DesignBand) *designRunRig {
	t.Helper()
	rig := &designRunRig{
		repo:   mocks.NewMockRepository(t),
		cards:  mocks.NewMockTechCards(t),
		design: mocks.NewMockDesign(t),
	}
	rig.repo.EXPECT().TechCards().Return(rig.cards).Maybe()
	rig.repo.EXPECT().Design().Return(rig.design).Maybe()
	designStubAnyMedia(t, rig.repo)
	designStubNoDisplayOnly(rig.design)
	rig.cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).Return(card, nil).Maybe()
	rig.design.EXPECT().GetBand(mock.Anything, designRunCardID, mock.Anything).Return(band, nil).Maybe()
	rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Run(func(_ context.Context, req entity.DesignRunStart) {
			cp := req
			rig.sent = &cp
		}).
		Return(&entity.DesignRunStarted{
			Run:    entity.DesignRun{Id: 900, TechCardId: designRunCardID, Status: entity.DesignRunPending},
			Budget: entity.DesignBudget{Day: "2026-08-30"},
		}, nil).Maybe()
	rig.srv = &Server{repo: rig.repo, designGenerationEnabled: true}
	return rig
}

// designMoodCard — карточка, у которой ДОСКА ГРОМКАЯ: своя картинка, своя записка и выноска на
// этой картинке. Именно эти три вещи и не должны доехать до модели через генерацию.
func designMoodCard() *entity.TechCard {
	card := &entity.TechCard{}
	card.Name = "mood subject"
	card.Fit = sql.NullString{String: "oversized", Valid: true}
	card.MoodNote = sql.NullString{String: "MOODWORDS-do-not-generate", Valid: true}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: designBoardMediaID, Category: entity.TechCardMediaCategoryMoodboard},
		{MediaId: designTechnicalMediaID, Category: entity.TechCardMediaCategoryTechnical},
	}
	card.Callouts = []entity.TechCardCallout{{
		Number:      1,
		Part:        sql.NullString{String: "collar", Valid: true},
		Description: sql.NullString{String: "MOODCALLOUT-do-not-generate", Valid: true},
		MediaId:     sql.NullInt32{Int32: designBoardMediaID, Valid: true},
	}}
	return card
}

const (
	// designBoardMediaID лежит ТОЛЬКО на доске и ни в одном референсе — единственный
	// правдоподобный способ для него попасть в СНИМОК ГЕНЕРАЦИИ это ошибка сборки входов.
	designBoardMediaID = 900
	// designTechnicalMediaID — технический эскиз карточки, тоже не референс.
	designTechnicalMediaID = 901
	// designRefMediaID — картинка, которую человек ЯВНО перенёс в INPUT — REFERENCES.
	designRefMediaID = 100
	// designPlateMediaID — плита верстака.
	designPlateMediaID = 200
	// designRenderPlateMediaID — плита РЕНДЕР-верстака: без неё «полоса с рендером» была флагом
	// над пустым верстаком, и ворота 3D открывались прогону без единого входа (F3).
	designRenderPlateMediaID = 210
	// designBoardMediaURL — адрес картинки доски, каким его отдаёт медиа-стор.
	//
	// ⚠ ОН НАМЕРЕННО НЕ СОДЕРЖИТ ЧИСЛА 900, И ЭТО НЕСУЩЕЕ СВОЙСТВО СТЕНДА, А НЕ УКРАШЕНИЕ.
	// Прежний «замер W-15 по байтам провода» утверждал `NotContains(body, "900")` и был ЗЕЛЁН
	// при реально уехавших картинках: настоящий CDN-адрес номера медиа не несёт, он ключ объекта.
	// Проба, которую нельзя провалить, ничего не охраняет, поэтому адрес здесь выглядит как
	// настоящий — иначе стенд снова начал бы доказывать не то, что утверждает.
	designBoardMediaURL = "https://cdn.grbpwr.com/media/9f8e7d6c/full.jpg"
)

func designBandWith(hasRender bool) *entity.DesignBand {
	band := &entity.DesignBand{
		HasFabricRender: hasRender,
		References: []entity.DesignReference{
			{TechCardId: designRunCardID, MediaId: designRefMediaID, Role: entity.DesignViewFront, Ordinal: 1},
		},
		Bench: []entity.DesignBenchSlot{{
			Id:         5,
			TechCardId: designRunCardID,
			ViewKey:    entity.DesignViewFront,
			Kind:       entity.DesignPictureKindFlat,
			PictureId:  sql.NullInt32{Int32: 77, Valid: true},
			Picture: &entity.DesignPicture{
				Id: 77, TechCardId: designRunCardID, MediaId: designPlateMediaID,
				Kind: entity.DesignPictureKindFlat,
			},
		}},
	}
	// ⚠ «ЕСТЬ РЕНДЕР» ЗНАЧИТ ЗАНЯТЫЙ РЕНДЕР-СЛОТ, А НЕ ФЛАГ РЯДОМ С ПУСТЫМ ВЕРСТАКОМ.
	//
	// Здесь стенд однажды уже соврал: он выставлял RenderBenchColorways = {0} при верстаке, где
	// не было НИ ОДНОГО render-слота, — то есть изображал ровно то состояние, которое настоящий
	// стор произвести не может (его запрос считает занятые слоты). Положительный контроль ворот
	// 3D на таком стенде доказывал, что дверь открывается карточке с ПУСТЫМ рендер-верстаком, —
	// то есть ровно то, что эти ворота обязаны закрывать.
	//
	// Лечится не подгонкой числа, а тем, что стенд больше не умеет расходиться со стором:
	// множество ВЫВОДИТСЯ из верстака тем же правилом, что в designRenderBenchColorways.
	if hasRender {
		band.Bench = append(band.Bench, entity.DesignBenchSlot{
			Id:         6,
			TechCardId: designRunCardID,
			ViewKey:    entity.DesignViewFront,
			Kind:       entity.DesignPictureKindRender,
			PictureId:  sql.NullInt32{Int32: 78, Valid: true},
			Picture: &entity.DesignPicture{
				Id: 78, TechCardId: designRunCardID, MediaId: designRenderPlateMediaID,
				Kind: entity.DesignPictureKindRender,
			},
		})
	}
	band.RenderBenchColorways = designRenderBenchColorwaysOf(band.Bench)
	return band
}

// designRenderBenchColorwaysOf — ЗЕРКАЛО СЕРВЕРНОГО ЗАПРОСА В СТЕНДЕ (`designRenderBenchColorways`,
// store/design/band.go): колорвеи занятых render-слотов, NULL как 0, по возрастанию. Стенд считает
// его САМ, а не принимает числом, чтобы «полоса с рендером» не могла означать в стенде одно, а в
// сторе другое: расхождение этих двух ответов и было дефектом F3.
func designRenderBenchColorwaysOf(bench []entity.DesignBenchSlot) []int {
	seen := map[int]struct{}{}
	out := make([]int, 0, len(bench))
	for _, s := range bench {
		if entity.DesignKindOrFlat(s.Kind) != entity.DesignPictureKindRender {
			continue
		}
		if s.Picture == nil || s.Picture.MediaId <= 0 || s.Picture.HiddenAt.Valid {
			continue
		}
		cw := entity.DesignColorwayOrNone(s.ColorwayId)
		if _, dup := seen[cw]; dup {
			continue
		}
		seen[cw] = struct{}{}
		out = append(out, cw)
	}
	sort.Ints(out)
	return out
}

func designStartRequest(kind string) *pb_admin.StartDesignRunRequest {
	return &pb_admin.StartDesignRunRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "22222222-2222-2222-2222-222222222222",
		Kind:            kind,
		Params: &pb_common.DesignRunParams{
			Views:  []string{entity.DesignViewFront},
			Layout: designLayoutPerView,
		},
	}
}

// ─────────────────────── W-15: ДОСКА НЕ УХОДИТ В ГЕНЕРАЦИЮ ───────────────────────

// МУДБОРД НЕ ПОПАДАЕТ В СНИМОК ВХОДОВ НИ ПРИ КАКОМ РОДЕ ПРОГОНА.
//
// Владелец процитировал строку прототипа дословно: «the mood, not the prompt: nothing here is sent
// to generation». Экранное обещание обходится вторым клиентом и вкладкой по ссылке, поэтому
// проверяется СЕРВЕРНАЯ половина: снимок, который замерзает в строке и который читает воркер.
//
// ⚠ ПРОБА ДЕРЖИТ ОБА КОНЦА, и одного было бы мало. «В снимке нет 900» зеленеет и на пустом
// снимке — то есть на сломанной сборке, которая не отправляет НИЧЕГО. Поэтому рядом стоит
// положительный контроль: явно перенесённый референс и плита верстака обязаны БЫТЬ.
//
// МУТАЦИЯ, КОТОРОЙ ПРОБА ПРОВЕРЕНА: в designAssembleInputs добавлена строка, подмешивающая
// медиа доски в out.Refs. Проба покраснела на всех четырёх родах (см. отчёт).
func TestDesignRunInputsNeverCarryTheMoodboard(t *testing.T) {
	card := designMoodCard()
	band := designBandWith(true)
	for _, kind := range []string{
		entity.DesignRunKindFlat, entity.DesignRunKindRender,
		entity.DesignRunKindThreed, entity.DesignRunKindVector,
	} {
		t.Run(kind, func(t *testing.T) {
			params := &pb_common.DesignRunParams{
				Views:  []string{entity.DesignViewFront},
				Layout: designLayoutPerView,
				// Дополнительный вход НАЗВАН ЯВНО — он законен и обязан доехать. Он же держит
				// границу: «явно названное едет» и «доска не едет» это одно правило про ИСТОЧНИК,
				// а не два разных.
				ExtraInputMediaIds: []int32{designExtraMediaID},
			}
			snap, err := designAssembleInputs(designInputSources{
				Kind: kind, Card: card, Refs: band.References, Bench: band.Bench, Params: params,
			})
			require.NoError(t, err)

			// ── положительный контроль: снимок не пуст и несёт именно то, что человек назвал ──
			refIDs := make([]int32, 0, len(snap.GetRefs()))
			for _, r := range snap.GetRefs() {
				refIDs = append(refIDs, r.GetMediaId())
			}
			require.ElementsMatch(t, []int32{designRefMediaID, designExtraMediaID}, refIDs,
				"снимок обязан нести явно перенесённый референс и явно названный доп-вход")

			// ── доска отсутствует и как блок, и как число ──
			require.Nil(t, snap.GetMood(),
				"у картиночного прогона mood обязан быть пустым: заполненный отдал бы воркеру "+
					"media_id доски прямо в руки")

			raw, err := designMarshalJSON(snap)
			require.NoError(t, err)
			text := string(raw)
			require.NotContains(t, text, strconv.Itoa(designBoardMediaID),
				"медиа доски не смеет появиться в снимке ни в каком поле")
			require.NotContains(t, text, "MOODWORDS",
				"записка доски не смеет появиться в снимке")
			require.NotContains(t, text, "MOODCALLOUT",
				"текст выноски доски не смеет появиться в снимке")
		})
	}
}

// designExtraMediaID — вход, названный человеком прямо в запросе (params.extra_input_media_ids).
const designExtraMediaID = 111

// КАРТИНКА, ЛЕЖАЩАЯ И НА ДОСКЕ, И В РЕФЕРЕНСАХ, УХОДИТ В МОДЕЛЬ — И ЭТО ВЕРНО.
//
// ЭТА ПРОБА ЗАЩИЩАЕТ ОТ «ПОЧИНКИ» W-15 НЕ ТЕМ СПОСОБОМ. Соблазн очевиден: отфильтровать всё, что
// числится на доске. Это сломало бы главный жест полосы — владелец требует (U-5 §5), чтобы щелчок
// по плитке доски ЗАВОДИЛ запись референса с тем же media_id, и это и есть «явный перенос»,
// единственная законная дверь картинки в промпт. Правило про ИСТОЧНИК, а не про картинку.
func TestDesignRunInputsKeepAReferenceThatAlsoSitsOnTheBoard(t *testing.T) {
	card := designMoodCard()
	// Человек перенёс на доску лежащую картинку в референсы: строка design_reference на неё есть.
	refs := []entity.DesignReference{
		{TechCardId: designRunCardID, MediaId: designBoardMediaID, Role: entity.DesignViewFront},
	}
	snap, err := designAssembleInputs(designInputSources{
		Kind: entity.DesignRunKindFlat, Card: card, Refs: refs,
		Params: &pb_common.DesignRunParams{Layout: designLayoutOne},
	})
	require.NoError(t, err)
	require.Len(t, snap.GetRefs(), 1)
	require.Equal(t, int32(designBoardMediaID), snap.GetRefs()[0].GetMediaId(),
		"явно перенесённый референс обязан доехать, даже если та же картинка висит на доске")
}

// СЛОВЕСНАЯ ЧАСТЬ ЗАПРОСА НЕСЁТ СЛОВА И НИ ОДНОГО НАШЕГО КЛЮЧА.
//
// ⚠ ЭТО БОЛЬШЕ НЕ СТОРОЖ W-15, И РАНЬШЕ ОН ИМ НЕ БЫЛ ТОЖЕ — он щупает сборщик промпта в
// ИЗОЛЯЦИИ, а картинки едут отдельными content-частями мимо этой функции, поэтому покраснеть на
// уехавшей картинке он не мог в принципе. Гарантию границы теперь держит
// TestW15BoardReachesTheDraftButNeverGeneration, которая меряет провод.
//
// Утверждение здесь осталось верным и осмысленным по ДРУГОЙ причине: media_id — наш внутренний
// ключ, модели он не сообщает ничего, а «выноска на медиа 900» заставила бы её гадать, к какому
// из присланных изображений это относится (см. шапку designDraftIdeaPrompt).
func TestDraftIdeaPromptCarriesWordsAndNoPictureIdentity(t *testing.T) {
	card := designMoodCard()
	mood := designMoodSnapshot(card)
	require.NotNil(t, mood)
	prompt := designDraftIdeaPrompt(card, mood, designBoardMediaIDs(card))

	require.Contains(t, prompt, "MOODWORDS-do-not-generate", "записка доски — это и есть вход")
	require.Contains(t, prompt, "MOODCALLOUT-do-not-generate", "текст выноски тоже вход")
	require.NotContains(t, prompt, strconv.Itoa(designBoardMediaID),
		"НАШ media_id в промпте не нужен модели: «picture N» — это порядковый номер content-части")
	require.NotContains(t, prompt, "http", "ни одной ссылки на объект")
}

// ВЫНОСКА НА ТЕХНИЧЕСКОМ ЭСКИЗЕ — НЕ ДОСКА.
//
// Иначе «мудборд» на экране и «мудборд» в снимке оказались бы разными множествами, и черновик
// идеи читал бы чертёж как вдохновение.
func TestDesignMoodSnapshotTakesOnlyBoardCallouts(t *testing.T) {
	card := designMoodCard()
	card.Callouts = append(card.Callouts, entity.TechCardCallout{
		Number:      2,
		Description: sql.NullString{String: "TECHNICAL-not-mood", Valid: true},
		MediaId:     sql.NullInt32{Int32: designTechnicalMediaID, Valid: true},
	})
	mood := designMoodSnapshot(card)
	require.NotNil(t, mood)
	require.Len(t, mood.GetCallouts(), 1)
	require.Contains(t, mood.GetCallouts()[0].GetText(), "MOODCALLOUT-do-not-generate")
}

// ─────────────────────── ФЛАГ ───────────────────────

// ВЫКЛЮЧЕННЫЙ ФЛАГ ОТКАЗЫВАЕТ И НЕ ТРОГАЕТ СТОР ВОВСЕ.
//
// «Не трогает» здесь — ИЗМЕРЕННЫЙ ФАКТ, а не отсутствие проверки: репозиторий строгий, и любой
// незаявленный вызов роняет пробу. Именно это и важно: строка, заведённая при выключенном флаге,
// зарезервировала бы деньги и осталась бы в pending навсегда — забирать её некому.
func TestStartDesignRunRefusesWhileGenerationIsDisabled(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	srv := &Server{repo: repo} // флаг не выставлен => выключен
	_, err := srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindFlat))
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, designReasonGenerationDisabled, md["reason"])
	require.Contains(t, err.Error(), "DESIGN_GENERATION_ENABLED",
		"отказ обязан называть переменную: без неё дежурному нечего включать")
	require.Contains(t, err.Error(), "worker",
		"и последствие: без воркера прогон некому забрать")
}

// ТОТ ЖЕ ФЛАГ ЗАКРЫВАЕТ И ЧЕРНОВИК ИДЕИ — второй платный глагол в том же денежном регистре.
func TestDraftDesignIdeaRefusesWhileGenerationIsDisabled(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	srv := &Server{repo: repo}
	_, err := srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId: designRunCardID, ClientRequestId: "33333333-3333-3333-3333-333333333333",
	})
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, designReasonGenerationDisabled, md["reason"])
}

// ─────────────────────── W-13: 3D ТОЛЬКО ПОСЛЕ РЕНДЕРА ───────────────────────

// БЕЗ НЕПРЯТАННОГО FABRIC RENDER 3D ОТКАЗЫВАЕТСЯ НА СЕРВЕРЕ.
//
// Клиентское приглушение — подсказка: вкладку открывают ссылкой, а платит владелец. Стор здесь
// строгий, значит «StartRun не позвался» тоже измерено.
func TestStartDesignRunRefusesThreedWithoutAFabricRender(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(false))
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "no_fabric_render", md["reason"])
	require.Nil(t, rig.sent, "отказ обязан прийти ДО стора: иначе деньги уже зарезервированы")
}

// С РЕНДЕРОМ ТА ЖЕ ПРОСЬБА ПРОХОДИТ — положительный контроль к пробе выше. Без него «отказ»
// доказывал бы только то, что 3D не работает никогда.
func TestStartDesignRunAllowsThreedOnceAFabricRenderExists(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	resp, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindThreed))
	require.NoError(t, err)
	require.NotNil(t, resp.GetRun())
	require.NotNil(t, rig.sent)
	require.Equal(t, entity.DesignRunKindThreed, rig.sent.Kind)
	require.Equal(t, "designer", rig.sent.Author)
	require.True(t, rig.sent.PriceEstimate.Valid, "резерв дня считается от оценки; без неё её нет")
}

// ─────────────────────── форма запроса ───────────────────────

// ТЕКСТОВЫЙ ПРОГОН ЧЕРЕЗ ЭТУ ДВЕРЬ НЕ ЗАВОДИТСЯ.
//
// Заведённый отсюда, он вернул бы строку `pending`, которую никто никогда не опрашивает: предикат
// захвата воркера исключает draft_idea намеренно, иначе воркер оплатил бы второй вызов модели.
func TestStartDesignRunRefusesTheTextKind(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindDraftIdea))
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), "DraftDesignIdea")
	require.Nil(t, rig.sent)
}

func TestDesignEffectiveParamsRefusesNonsense(t *testing.T) {
	_, err := designEffectiveParams(&pb_common.DesignRunParams{Views: []string{"sleeve"}}, nil)
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)

	_, err = designEffectiveParams(&pb_common.DesignRunParams{Layout: "grid"}, nil)
	require.Error(t, err)

	// ПУСТАЯ РАСКЛАДКА ЗАПИСЫВАЕТСЯ ЯВНО: она замерзает в истории и участвует в дивайдере
	// «earlier — inputs have changed»; строка без неё расходилась бы со строкой с `one` без причины.
	p, err := designEffectiveParams(nil, nil)
	require.NoError(t, err)
	require.Equal(t, designLayoutOne, p.GetLayout())
}

// КОМПОЗИТ — ОДНА КАРТИНКА, СКОЛЬКО БЫ ВИДОВ НА НЁМ НИ БЫЛО.
//
// Разрез на N кадров — отдельный и бесплатный акт; посчитанный выходами прогона, он обещал бы
// плитки, которых генерация не приносит, и человек читал бы это как потерянный результат.
func TestDesignRequestedOutputsCountsPicturesNotViews(t *testing.T) {
	three := []string{entity.DesignViewFront, entity.DesignViewBack, entity.DesignViewSideL}
	require.Equal(t, 1, designRequestedOutputs(entity.DesignRunKindFlat,
		&pb_common.DesignRunParams{Views: three, Layout: designLayoutOne}))
	require.Equal(t, 3, designRequestedOutputs(entity.DesignRunKindFlat,
		&pb_common.DesignRunParams{Views: three, Layout: designLayoutPerView}))
	require.Equal(t, 1, designRequestedOutputs(entity.DesignRunKindThreed,
		&pb_common.DesignRunParams{Views: three, Layout: designLayoutPerView}))
}

// ─────────────────────── W-7: РЕРАН СОБИРАЕТ СЕРВЕР ───────────────────────

// ВХОДЫ РЕРАНА ПРИЕЗЖАЮТ СО СТРОКИ РОДИТЕЛЯ, А НЕ С СЕГОДНЯШНЕЙ КАРТОЧКИ.
//
// В этом весь смысл того, что реран отдан серверу: повторить прогон значит послать модели ТО ЖЕ
// САМОЕ, а «то же самое» знает только история. Проба ставит эксперимент, в котором два источника
// РАСХОДЯТСЯ: у родителя референс 700, у карточки сегодня — 100. Совпадающие источники доказали бы
// ноль.
func TestDesignRerunTakesItsInputsFromTheParentNotFromTodaysCard(t *testing.T) {
	parentInputs, err := designMarshalJSON(&pb_common.DesignInputSnapshot{
		Refs: []*pb_common.DesignInputRef{{MediaId: 700, Role: entity.DesignViewBack}},
		Fit:  "slim",
	})
	require.NoError(t, err)
	parent := &entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindFlat,
		Inputs: entity.RawJSON(parentInputs),
	}
	srv := &Server{}
	params := &pb_common.DesignRunParams{Views: []string{entity.DesignViewFront}, Layout: designLayoutPerView}
	snap, fit, err := srv.designRunInputs(context.Background(), designInputSources{
		Kind:   entity.DesignRunKindFlat,
		Card:   designMoodCard(), // фит oversized, доска громкая
		Refs:   designBandWith(true).References,
		Bench:  designBandWith(true).Bench,
		Params: params,
	}, parent)
	require.NoError(t, err)

	require.Len(t, snap.GetRefs(), 1)
	require.Equal(t, int32(700), snap.GetRefs()[0].GetMediaId(),
		"референс обязан быть тот, что был у родителя")
	require.Equal(t, "slim", fit,
		"fit_at_launch обязан говорить то же, что сказали модели: иначе плита приедет со штампом "+
			"сегодняшнего фита, а нарисована будет по вчерашнему")

	// Отпечаток — КОПИЯ ПАРАМЕТРОВ, а не провенанс: он обязан сойтись с параметрами этой строки,
	// иначе дивайдер истории «earlier — inputs have changed» сравнивает не то.
	require.Equal(t, []string{entity.DesignViewFront}, snap.GetViews())
	require.Equal(t, designLayoutPerView, snap.GetLayout())
}

// РЕРАН ЧУЖОГО ПРОГОНА — NotFound, а не «взяли и повторили».
func TestDesignRerunRefusesARunOfAnotherCard(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.design.EXPECT().GetRun(mock.Anything, 12).
		Return(&entity.DesignRun{Id: 12, TechCardId: designRunCardID + 1}, nil).Once()
	req := designStartRequest(entity.DesignRunKindFlat)
	req.RerunOfRunId = 12
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.NotFound, code)
	require.Nil(t, rig.sent)
}

// ─────────────────────── имена ключей в снимке ───────────────────────

// СНИМОК ПИШЕТСЯ snake_case, И ЭТО НЕ КОСМЕТИКА.
//
// Стор ходит в колонки `params`/`inputs` SQL-путями `$.slots[*].media_id`,
// `$.extra_input_media_ids`, `$.colour`. Дефолтный protojson написал бы lowerCamelCase, и оба
// запроса стали бы МОЛЧА пустыми: сторож HidePicture перестал бы отказывать, чипы цвета исчезли
// бы, mixed_input никогда не поднялся бы. Ни одной ошибки при этом не появится — пустой результат
// законен для карточки без прогонов, поэтому без этой пробы поломку не видит никто.
func TestDesignSnapshotIsWrittenWithProtoNames(t *testing.T) {
	raw, err := designMarshalJSON(&pb_common.DesignInputSnapshot{
		Refs:  []*pb_common.DesignInputRef{{MediaId: designRefMediaID}},
		Slots: []*pb_common.DesignInputSlot{{ViewKey: entity.DesignViewFront, MediaId: designPlateMediaID}},
	})
	require.NoError(t, err)
	text := string(raw)
	require.Contains(t, text, `"media_id"`, "стор читает $.refs[*].media_id и $.slots[*].media_id")
	require.Contains(t, text, `"view_key"`)
	require.NotContains(t, text, "mediaId", "lowerCamelCase делает SQL-пути стора молча пустыми")

	params, err := designMarshalJSON(&pb_common.DesignRunParams{ExtraInputMediaIds: []int32{designExtraMediaID}})
	require.NoError(t, err)
	require.Contains(t, string(params), `"extra_input_media_ids"`)
}

// ─────────────────────── плиты верстака ───────────────────────

// РЕНДЕР СТРОИТСЯ ИЗ ФЛЭТОВ, 3D — ИЗ РЕНДЕРОВ.
//
// Ось верстака ДВЕ (вид × род, 0349). Одноосное чтение брало бы рендер фронта вместо флэта фронта
// ровно там, где есть оба, — то есть на любой карточке, дошедшей до 3D.
func TestDesignInputSlotsFollowTheSecondBenchAxis(t *testing.T) {
	bench := []entity.DesignBenchSlot{
		{
			Id: 1, ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindFlat,
			Picture: &entity.DesignPicture{Id: 1, MediaId: 201, Kind: entity.DesignPictureKindFlat},
		},
		{
			// Род плиты назван ЯВНО: опущенный читается как `flat`, и рендер-слот с флэтовой
			// плитой — пара, которую настоящая постановка отвергает (N7).
			Id: 2, ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender,
			Picture: &entity.DesignPicture{Id: 2, MediaId: 202, Kind: entity.DesignPictureKindRender},
		},
	}
	flats := designInputSlots(designInputSources{
		Kind: entity.DesignRunKindRender, Bench: bench, Params: &pb_common.DesignRunParams{},
	})
	require.Len(t, flats, 1)
	require.Equal(t, int32(201), flats[0].GetMediaId())

	renders := designInputSlots(designInputSources{
		Kind: entity.DesignRunKindThreed, Bench: bench, Params: &pb_common.DesignRunParams{},
	})
	require.Len(t, renders, 1)
	require.Equal(t, int32(202), renders[0].GetMediaId())
}

// ВЫБОРКА `fix` — ОДНА, А НЕ ДВА РЕЖИМА: стороны по виду, детали по адресу, одним прогоном (W-10).
func TestDesignInputSlotsTakeSidesAndDetailsAsOneSelection(t *testing.T) {
	bench := []entity.DesignBenchSlot{
		{Id: 1, ViewKey: entity.DesignViewFront, Picture: &entity.DesignPicture{MediaId: 201}},
		{Id: 2, ViewKey: entity.DesignViewBack, Picture: &entity.DesignPicture{MediaId: 202}},
		{
			Id: 3, ViewKey: entity.DesignViewDetail,
			DetailName: sql.NullString{String: "cuff", Valid: true},
			Picture:    &entity.DesignPicture{MediaId: 203},
		},
	}
	got := designInputSlots(designInputSources{
		Kind:  entity.DesignRunKindFlat,
		Bench: bench,
		Params: &pb_common.DesignRunParams{
			FixTargets: []string{entity.DesignViewFront},
			FixSlotIds: []int32{3},
		},
	})
	require.Len(t, got, 2)
	ids := []int32{got[0].GetMediaId(), got[1].GetMediaId()}
	require.ElementsMatch(t, []int32{201, 203}, ids)
	for _, s := range got {
		if s.GetViewKey() == entity.DesignViewDetail {
			require.Equal(t, int32(3), s.GetSlotId(),
				"деталь адресуется слотом: по виду две детали не различить, и сравнение "+
					"«вход протух» осталось бы невычислимым навсегда")
			require.Equal(t, "cuff", s.GetDetailName())
		} else {
			require.Zero(t, s.GetSlotId(), "сторону называет вид, id ей не нужен")
		}
	}
}

// ─────────────────────── прочее ───────────────────────

// ПОЛОСА ОТДАЁТ ОБА ПОЛЯ ДВЕРИ 3D, И ОНИ ЗНАЧАТ РАЗНОЕ.
//
// ⚠ ИМЯ И ТЕЛО ЭТОЙ ПРОБЫ ПЕРЕЖИЛИ СВОЁ УТВЕРЖДЕНИЕ. Она звалась «полоса ЗЕРКАЛИТ ворота
// фабрик-рендера» и проверяла только has_fabric_render — а ворота с тех пор спрашивают
// render_bench_colorway_ids (занятые рендер-слоты), и собственный текст этой волны в admin.proto
// написан капслоком: дверь по этому флагу НЕ РИСОВАТЬ. То есть проба удостоверяла ровно ту связь,
// которую волна отменила, и не касалась поля, от которого контракт клиента теперь зависит.
//
// Теперь она проверяет ОБА и то, чем они отличаются.
func TestGetDesignBandServesBothFabricRenderSignals(t *testing.T) {
	newSrv := func(band *entity.DesignBand) *Server {
		repo := mocks.NewMockRepository(t)
		design := mocks.NewMockDesign(t)
		repo.EXPECT().Design().Return(design).Maybe()
		design.EXPECT().GetBand(mock.Anything, designRunCardID, mock.Anything).
			Return(band, nil).Once()
		return &Server{repo: repo}
	}
	read := func(band *entity.DesignBand) *pb_admin.GetDesignBandResponse {
		resp, err := newSrv(band).GetDesignBand(designRunCtx(),
			&pb_admin.GetDesignBandRequest{TechCardId: designRunCardID})
		require.NoError(t, err)
		return resp
	}

	// ЗАНЯТЫЙ ВЕРСТАК: оба поля говорят «да», и второе — то, по которому рисуют дверь.
	occupied := read(designBandWith(true))
	require.True(t, occupied.GetHasFabricRender())
	require.Equal(t, []int32{0}, occupied.GetRenderBenchColorwayIds(),
		"клиент рисует дверь 3D по ЭТОМУ полю; без него он не знает, какому цвету она открыта")

	// КАДРЫ ЕСТЬ, ВЕРСТАК ПУСТ — то самое состояние, в котором два поля РАСХОДЯТСЯ, и ради
	// которого второе поле вообще заведено. Клиент, читающий флаг, откроет кнопку в отказ.
	loose := designBandWith(false)
	loose.HasFabricRender = true
	split := read(loose)
	require.True(t, split.GetHasFabricRender())
	require.Empty(t, split.GetRenderBenchColorwayIds(),
		"загруженный, но не поставленный рендер двери не открывает — и полоса обязана это сказать")
}

// newDraftIdeaRig — стенд ЧЕРНОВИКА ИДЕИ, у которого StartRun НЕ заглушён заранее: обе пробы ниже
// про то, ЧТО ИМЕННО вернул стор, и общий рижок с `.Maybe()`-заглушкой ответил бы за него сам.
func newDraftIdeaRig(t *testing.T, client *openrouter.Client) *designRunRig {
	t.Helper()
	rig := &designRunRig{
		repo:   mocks.NewMockRepository(t),
		cards:  mocks.NewMockTechCards(t),
		design: mocks.NewMockDesign(t),
	}
	rig.repo.EXPECT().TechCards().Return(rig.cards).Maybe()
	rig.repo.EXPECT().Design().Return(rig.design).Maybe()
	designStubNoDisplayOnly(rig.design)
	rig.cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).
		Return(designMoodCard(), nil).Maybe()
	// ЧЕРНОВИК ИДЕИ ТЕПЕРЬ РАЗРЕШАЕТ КАРТИНКИ ДОСКИ В АДРЕСА, поэтому медиа-стор нужен и здесь.
	// Эти пробы про захват и повтор, а не про картинки, — заглушка `.Maybe()` тут уместна.
	media := mocks.NewMockMedia(t)
	rig.repo.EXPECT().Media().Return(media).Maybe()
	media.EXPECT().GetMediaByIds(mock.Anything, []int{designBoardMediaID}).
		Return(map[int]entity.MediaFull{designBoardMediaID: {
			Id:        designBoardMediaID,
			MediaItem: entity.MediaItem{FullSizeMediaURL: designBoardMediaURL},
		}}, nil).Maybe()
	rig.srv = &Server{repo: rig.repo, designGenerationEnabled: true, aiOps: client}
	return rig
}

// ПОВТОР, КОТОРОМУ СТОР НЕ ОТДАЛ СТРОКУ, МОДЕЛЬ НЕ ЗОВЁТ ВОВСЕ.
//
// ЧТО ЭТО СТЕРЕЖЁТ. Перехват брошенного хендлера ИСКЛЮЧАЮЩИЙ, и решает его та транзакция, что
// решает про идемпотентность: она одна может сказать «строку забрал ТЫ». Пока хендлер вычислял
// признак сам («лиза истекла?»), два одновременных повтора одного client_request_id видели
// истёкшую лизу ОБА, брали ОДИН И ТОТ ЖЕ токен со строки и ОБА ПЛАТИЛИ модели — обещание
// «повтор = один платёж» в окне резюма не выполнялось.
//
// МУТАЦИЯ: вернуть условие к `!designRunResumable(run, now)` — то есть к вычислению признака на
// этой стороне. Тогда прогон с истёкшей лизой зовёт модель у КАЖДОГО повтора.
func TestDraftDesignIdeaRepeatWithoutAResumeDoesNotCallTheModel(t *testing.T) {
	client, calls := newFakeOpenRouter(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"an idea"}}]}`))
	})
	rig := newDraftIdeaRig(t, client)

	// Строка с ИСТЁКШЕЙ лизой — и стор всё равно говорит «перехват не твой»: его выиграл соседний
	// запрос. Именно этот исход прежде читался как «можно платить».
	prior := entity.DesignRun{
		Id: 900, TechCardId: designRunCardID, Status: entity.DesignRunPending,
		ClaimToken:     sql.NullString{String: "held-by-the-other-repeat", Valid: true},
		ClaimExpiresAt: sql.NullTime{Time: time.Now().Add(-time.Minute), Valid: true},
	}
	rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Return(&entity.DesignRunStarted{Run: prior, Idempotent: true, Resumed: false}, nil).Once()

	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId: designRunCardID, ClientRequestId: "44444444-4444-4444-4444-444444444444",
	})
	require.NoError(t, err)
	require.Equal(t, int32(900), resp.GetRun().GetId())
	require.Empty(t, *calls, "повтор без перехвата НЕ ПЛАТИТ: вызов идёт в соседнем запросе")
}

// ЗЕРКАЛО: повтор, которому стор ОТДАЛ строку, доисполняет её — и делает это НОВЫМ токеном,
// который вернула та же транзакция. Без этой половины первая проба была бы выполнима заглушкой
// «никогда не звать модель».
func TestDraftDesignIdeaResumeUsesTheRotatedToken(t *testing.T) {
	client, calls := newFakeOpenRouter(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"an idea"}}]}`))
	})
	rig := newDraftIdeaRig(t, client)

	const rotated = "rotated-token"
	resumedRun := entity.DesignRun{
		Id: 900, TechCardId: designRunCardID, Status: entity.DesignRunPending,
		ClaimToken:     sql.NullString{String: rotated, Valid: true},
		ClaimExpiresAt: sql.NullTime{Time: time.Now().Add(5 * time.Minute), Valid: true},
	}
	rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Return(&entity.DesignRunStarted{Run: resumedRun, Idempotent: true, Resumed: true}, nil).Once()
	rig.design.EXPECT().StartAttempt(mock.Anything, mock.MatchedBy(func(a entity.DesignAttemptStart) bool {
		return a.ClaimToken == rotated
	})).Return(&entity.DesignRunAttempt{RunId: 900, AttemptNo: 2}, nil).Once()
	rig.design.EXPECT().FinishAttempt(mock.Anything, mock.AnythingOfType("entity.DesignAttemptFinish")).
		Return(nil).Once()
	rig.design.EXPECT().CompleteRun(mock.Anything, mock.MatchedBy(func(c entity.DesignRunComplete) bool {
		return c.ClaimToken == rotated
	})).Return(&resumedRun, nil).Once()
	rig.design.EXPECT().GetBudget(mock.Anything).Return(entity.DesignBudget{Day: "2026-08-30"}, nil).Once()

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId: designRunCardID, ClientRequestId: "44444444-4444-4444-4444-444444444444",
	})
	require.NoError(t, err)
	require.Len(t, *calls, 1, "перехвативший повтор доисполняет прогон ровно один раз")
}

// ПОТОЛКИ СНИМКА ОТКАЗЫВАЮТ, А НЕ МОЛЧА ОБРЕЗАЮТ: снимок, у которого половина входов пропала,
// врал бы про то, из чего собран оплаченный кадр.
func TestDesignAssembleInputsRefusesTooManyReferences(t *testing.T) {
	refs := make([]entity.DesignReference, 0, designMaxInputRefs+1)
	for i := 0; i <= designMaxInputRefs; i++ {
		refs = append(refs, entity.DesignReference{MediaId: 1000 + i})
	}
	_, err := designAssembleInputs(designInputSources{
		Kind: entity.DesignRunKindFlat, Refs: refs, Params: &pb_common.DesignRunParams{},
	})
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.True(t, strings.Contains(err.Error(), strconv.Itoa(designMaxInputRefs)),
		"отказ обязан называть потолок, иначе его не с чем сравнить")
}

// ─────────────────────── черновик идеи: весь круг ───────────────────────

// draftIdeaStub — поставщик текста, отвечающий один раз и запоминающий, ЧТО ЕМУ ПРИСЛАЛИ.
// Тело запроса и есть предмет пробы: граница W-15 проверяется не намерением, а байтами в сети.
type draftIdeaStub struct {
	srv  *httptest.Server
	body string
	// raw — ВЕСЬ КОНВЕРТ ОТВЕТА ДОСЛОВНО, когда пробе нужен не только текст: finish_reason и
	// usage живут рядом с содержимым, и исход «потолок съеден, ответа ноль» без них невыразим.
	// Пустая строка — обычный конверт из `answer`.
	raw string
	// answered — ОТВЕТИЛ ЛИ УЖЕ ПОСТАВЩИК. Ставится ВНУТРИ обработчика, до записи тела, поэтому
	// «истина» happens-before любого чтения ответа клиентом: проба, которой нужно убить контекст
	// РОВНО между платным вызовом и закрывающими записями, опирается на этот порядок, а не на сон.
	answered atomic.Bool
}

// imageURLs — адреса, ушедшие на провод ОТДЕЛЬНЫМИ частями `image_url`, в порядке отправки.
//
// ⚠ РАЗБИРАЕТ ТЕЛО, А НЕ ИЩЕТ ПОДСТРОКУ. Это и есть починка пустого сторожа: `Contains(body, url)`
// зеленел бы и от адреса, случайно попавшего в ТЕКСТ промпта, а `NotContains(body, "900")` —
// молчал бы при реально уехавших картинках. Утверждать надо ровно то, что означает «модель
// увидела картинку»: часть типа image_url в ходе пользователя.
func (s *draftIdeaStub) imageURLs(t *testing.T) []string {
	t.Helper()
	if s.body == "" {
		return nil
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(s.body), &req), "тело запроса обязано быть JSON")
	var out []string
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		_, images := orContent(t, m.Content)
		out = append(out, images...)
	}
	return out
}

// systemTurnIsAPlainString — системный ход обязан остаться СТРОКОЙ, а не массивом частей.
//
// Это не придирка к форме: на текстовом ходе висит разбор тех-карты, РАБОТАЮЩИЙ НА ПРОДЕ, и вся
// причина, по которой мультимодальность заведена ВТОРОЙ структурой запроса, — чтобы его байты не
// поехали. Если однажды кто-то «упростит» и переведёт системный ход в части, эта проба скажет об
// этом здесь, а не 400-й от поставщика в живой функции, которую никто в тот момент не трогал.
func (s *draftIdeaStub) systemTurnIsAPlainString(t *testing.T) bool {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(s.body), &req))
	for _, m := range req.Messages {
		if m.Role != "system" {
			continue
		}
		var asString string
		return json.Unmarshal(m.Content, &asString) == nil
	}
	return false
}

func newDraftIdeaStub(t *testing.T, status int, answer string) *draftIdeaStub {
	t.Helper()
	stub := &draftIdeaStub{}
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		stub.body = string(raw)
		// ⚠ ФЛАГ СТАВИТСЯ ДО ЗАПИСИ ТЕЛА: клиент физически не может дочитать ответ раньше, чем
		// обработчик его напишет, поэтому «поставщик ответил» истинно ко времени возврата вызова.
		stub.answered.Store(true)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			if stub.raw != "" {
				_, _ = w.Write([]byte(stub.raw))
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + strconv.Quote(answer) + `}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":{"message":"upstream is on fire"}}`))
	}))
	t.Cleanup(stub.srv.Close)
	return stub
}

// draftRig — стенд текстового прогона: карточка, строка с захватом и стуб модели.
type draftRig struct {
	srv      *Server
	design   *mocks.MockDesign
	stub     *draftIdeaStub
	finished []entity.DesignAttemptFinish
	// finishedCtxErr — ЖИВ ЛИ БЫЛ КОНТЕКСТ В ТУ СЕКУНДУ, КОГДА ЗАКРЫВАЮЩАЯ ЗАПИСЬ ПОШЛА В СТОР.
	//
	// Не декорация: закрытие провалившегося прогона обязано пережить ОТМЕНУ контекста хендлера
	// (ушедший клиент), иначе стор откажет на BeginTx и исход «человек закрыл вкладку» снова стоит
	// регистру ноль — см. designFailDraftAs.
	//
	// ⚠ ЗАПОМИНАЕТСЯ ОШИБКА, А НЕ САМ КОНТЕКСТ, И ЭТО ЧАСТЬ ЗАМЕРА. У закрывающей записи свой
	// производный срок со своим `defer cancel()`; спросив сохранённый контекст ПОСЛЕ возврата,
	// проба увидела бы context.Canceled ВСЕГДА — и покраснела бы на здоровом коде.
	finishedCtxErr []error
	// finishDelay / *Deadline / failedCtxErr — ЗАМЕР ТОГО, ЧТО ДВЕ ЗАКРЫВАЮЩИЕ ЗАПИСИ НЕ ДЕЛЯТ
	// ОДИН СРОК. Первая (FinishAttempt) — семь операторов в SERIALIZABLE, каждый из которых вправе
	// ждать чужой next-key lock сколько угодно (сон повторов тут ни при чём: он 310 ms на все пять,
	// ~465 ms с джиттером — см. designCloseWriteBudget); съев общий бюджет, она оставляла второй
	// (FailRun) истёкший контекст, и та отказывала на BeginTx ОДНОЙ СТРОКОЙ ЛОГА. Списание
	// записано, прогон не закрыт, резерв не снят — и такую строку не подметает НИЧТО: из трёх мётел
	// ReviveExpiredRuns две фильтруют `status='running'`, а третья требует cancel_requested_at,
	// которого здесь нет.
	//
	// finishDelay заставляет первую запись занять время; дедлайны показывают, откуда отсчитан срок
	// второй. Равные дедлайны = общий бюджет.
	finishDelay      time.Duration
	finishedDeadline []time.Time
	failedDeadline   []time.Time
	failedRemaining  []time.Duration
	failedCtxErr     []error
	failed           []entity.DesignRunFail
	// completed* — ТО ЖЕ САМОЕ ДЛЯ УСПЕШНОЙ ПОЛОВИНЫ, и она дороже провальной: там терялась строка
	// регистра, здесь теряется ОПЛАЧЕННЫЙ ОТВЕТ. Обе закрывающие записи успеха тоже обязаны пережить
	// ушедшего клиента и не делить один срок на двоих.
	completeDelay      time.Duration
	completedCtxErr    []error
	completedDeadline  []time.Time
	completedRemaining []time.Duration
	// onProviderAnswered — ЧТО СЛУЧАЕТСЯ РОВНО МЕЖДУ ПЛАТНЫМ ВЫЗОВОМ И ЗАКРЫВАЮЩИМИ ЗАПИСЯМИ.
	//
	// Зовётся ОДИН РАЗ, на первом обращении хендлера к стору ПОСЛЕ того, как поставщик ответил
	// (stub.answered), — то есть в единственной точке, где «клиент ушёл, ответ уже куплен» ещё
	// можно изобразить без сна и без гонки. Хендлер между этими двумя точками в стор не ходит
	// ничем, кроме FinishAttempt, поэтому точка ОДНА и она детерминирована.
	onProviderAnswered func()
	providerAnswerSeen bool
	// completedText / completedTok — то, чем прогон был закрыт. Токен здесь не декорация:
	// CompleteRun сверяет его в WHERE, и закрытие чужим токеном — это claim_lost.
	completedText string
	completedTok  string
	// started — то, чем прогон был ОТКРЫТ: снимок входов и цена. Замораживается в строке навсегда,
	// поэтому пробы про доску и про деньги смотрят сюда, а не на ответ хендлера.
	started entity.DesignRunStart
	// finishErr — ЧЕМ ОТВЕЧАЕТ ПЕРВАЯ ЗАКРЫВАЮЩАЯ ЗАПИСЬ. Ноль — успех.
	//
	// Ручка нужна ровно одной пробе — той, что читает СЛЕД ПРОПАВШЕГО СПИСАНИЯ: этот исход не
	// возвращает ошибку наружу (ответ дороже строки регистра), поэтому единственный способ его
	// увидеть — заставить запись отказать и прочитать журнал.
	finishErr error
}

// newDraftRig — стенд на КАРТОЧКЕ ПО УМОЛЧАНИЮ (одна картинка доски, записка, выноска).
func newDraftRig(t *testing.T, httpStatus int, answer string) *draftRig {
	t.Helper()
	return newDraftRigWithCard(t, httpStatus, answer, designMoodCard(),
		[]int{designBoardMediaID},
		map[int]entity.MediaFull{designBoardMediaID: {
			Id:        designBoardMediaID,
			MediaItem: entity.MediaItem{FullSizeMediaURL: designBoardMediaURL},
		}})
}

// newDraftRigWithCard — тот же стенд, но доска задаётся вызывающим.
//
// `boardIDs` — ровно тот список, которым хендлер обязан спросить медиа-стор: строгий мок сам
// покраснеет, если порядок или состав разойдутся, и это ЧАСТЬ УТВЕРЖДЕНИЯ, а не удобство —
// запрос за картинками должен быть воспроизводим, иначе повтор с тем же client_request_id
// перестал бы быть тем же запросом.
func newDraftRigWithCard(
	t *testing.T, httpStatus int, answer string,
	card *entity.TechCard, boardIDs []int, byID map[int]entity.MediaFull,
) *draftRig {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	design := mocks.NewMockDesign(t)
	rig := &draftRig{design: design, stub: newDraftIdeaStub(t, httpStatus, answer)}

	run := entity.DesignRun{
		Id: 55, TechCardId: designRunCardID, Kind: entity.DesignRunKindDraftIdea,
		Status:     entity.DesignRunPending,
		ClaimToken: sql.NullString{String: "claim-55", Valid: true},
	}
	repo.EXPECT().TechCards().Return(cards).Maybe()
	repo.EXPECT().Design().Return(design).Run(func() {
		if rig.providerAnswerSeen || rig.onProviderAnswered == nil || !rig.stub.answered.Load() {
			return
		}
		rig.providerAnswerSeen = true
		rig.onProviderAnswered()
	}).Maybe()
	designStubNoDisplayOnly(design)
	cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).Return(card, nil).Maybe()
	// КАРТИНКИ ДОСКИ РАЗРЕШАЮТСЯ В АДРЕСА: с этого места черновик идеи их ЧИТАЕТ (решение
	// владельца «только в генерации»), поэтому стенд обязан уметь их отдать.
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media).Maybe()
	media.EXPECT().GetMediaByIds(mock.Anything, boardIDs).Return(byID, nil).Maybe()
	// СЛОВАРЬ ЦВЕТА (B-25) — ТОЛЬКО НЕАРХИВНЫЙ, И `false` ЗДЕСЬ ЧАСТЬ УТВЕРЖДЕНИЯ: архивный код
	// нельзя дать новому продукту, значит показывать его модели — платить за строку, ведущую
	// в отказ. Строгий мок покраснеет, если хендлер спросит с `true`.
	dict := mocks.NewMockDictionary(t)
	repo.EXPECT().Dictionary().Return(dict).Maybe()
	dict.EXPECT().ListColors(mock.Anything, false).Return(draftProbeColours(), nil).Maybe()
	design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Run(func(_ context.Context, req entity.DesignRunStart) { rig.started = req }).
		Return(&entity.DesignRunStarted{Run: run, Budget: entity.DesignBudget{Day: "2026-08-30"}}, nil).Once()
	design.EXPECT().StartAttempt(mock.Anything, mock.AnythingOfType("entity.DesignAttemptStart")).
		Return(&entity.DesignRunAttempt{RunId: 55, AttemptNo: 1}, nil).Once()
	design.EXPECT().FinishAttempt(mock.Anything, mock.AnythingOfType("entity.DesignAttemptFinish")).
		Run(func(ctx context.Context, req entity.DesignAttemptFinish) {
			rig.finished = append(rig.finished, req)
			rig.finishedCtxErr = append(rig.finishedCtxErr, ctx.Err())
			if dl, ok := ctx.Deadline(); ok {
				rig.finishedDeadline = append(rig.finishedDeadline, dl)
			}
			if rig.finishDelay > 0 {
				time.Sleep(rig.finishDelay)
			}
		}).RunAndReturn(func(context.Context, entity.DesignAttemptFinish) error {
		return rig.finishErr
	}).Maybe()
	design.EXPECT().FailRun(mock.Anything, mock.AnythingOfType("entity.DesignRunFail")).
		Run(func(ctx context.Context, req entity.DesignRunFail) {
			rig.failed = append(rig.failed, req)
			rig.failedCtxErr = append(rig.failedCtxErr, ctx.Err())
			if dl, ok := ctx.Deadline(); ok {
				rig.failedDeadline = append(rig.failedDeadline, dl)
				rig.failedRemaining = append(rig.failedRemaining, time.Until(dl))
			}
		}).Return(&run, nil).Maybe()
	design.EXPECT().CompleteRun(mock.Anything, mock.AnythingOfType("entity.DesignRunComplete")).
		Run(func(ctx context.Context, req entity.DesignRunComplete) {
			rig.completedText, rig.completedTok = req.OutputText.String, req.ClaimToken
			rig.completedCtxErr = append(rig.completedCtxErr, ctx.Err())
			if dl, ok := ctx.Deadline(); ok {
				rig.completedDeadline = append(rig.completedDeadline, dl)
				rig.completedRemaining = append(rig.completedRemaining, time.Until(dl))
			}
			if rig.completeDelay > 0 {
				time.Sleep(rig.completeDelay)
			}
		}).Return(&entity.DesignRun{Id: 55, Status: entity.DesignRunDone}, nil).Maybe()
	design.EXPECT().GetBudget(mock.Anything).Return(entity.DesignBudget{Day: "2026-08-30"}, nil).Maybe()

	rig.srv = &Server{
		repo:                    repo,
		designGenerationEnabled: true,
		aiOps: openrouter.New(openrouter.Config{
			APIKey: "test-key", BaseURL: rig.stub.srv.URL, Model: "anthropic/claude-sonnet-5",
		}),
	}
	return rig
}

func draftRequest() *pb_admin.DraftDesignIdeaRequest {
	return &pb_admin.DraftDesignIdeaRequest{
		TechCardId: designRunCardID, ClientRequestId: "44444444-4444-4444-4444-444444444444",
	}
}

// ЧЕРНОВИК ИДЕИ ИСПОЛНЯЕТСЯ В ХЕНДЛЕРЕ И ЗАКРЫВАЕТ СВОЮ СТРОКУ САМ.
//
// Воркер эту строку не заберёт по построению (`kind <> 'draft_idea'` в предикате захвата), поэтому
// незакрытая здесь строка не закроется НИКОГДА: она останется в pending с зарезервированными
// деньгами до полуночи. Проба меряет весь круг: попытка открыта, попытка закрыта с ценой, прогон
// закрыт СВОИМ токеном захвата и несёт ответ модели.
//
// И ОНА ЖЕ МЕРЯЕТ ПО БАЙТАМ, УШЕДШИМ В СЕТЬ, а не по намерению кода: в теле запроса есть слова
// доски И её картинки.
func TestDraftDesignIdeaRunsInlineAndFilesItsAnswer(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "A boxy coat with a storm flap.")
	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err)
	require.Equal(t, int32(55), resp.GetRun().GetId())

	require.Contains(t, rig.stub.body, "MOODWORDS-do-not-generate", "доска даёт слова")
	require.Contains(t, rig.stub.body, "MOODCALLOUT-do-not-generate")
	// КАРТИНКА ДОСКИ ОБЯЗАНА БЫТЬ НА ПРОВОДЕ — это решение владельца «только в генерации».
	require.Equal(t, []string{designBoardMediaURL}, rig.stub.imageURLs(t),
		"черновик идеи обязан показать модели картинки доски: прототип обещает «reads the pictures»")

	require.Len(t, rig.finished, 1)
	require.Equal(t, entity.DesignAttemptDelivered, rig.finished[0].State)
	require.True(t, rig.finished[0].Price.Valid,
		"без цены попытки `spent` дня не двигается вовсе, и потолок никогда не исчерпывается")
	require.Empty(t, rig.failed)
	require.Equal(t, "A boxy coat with a storm flap.", rig.completedText)
	require.Equal(t, "claim-55", rig.completedTok,
		"строка закрывается СВОИМ захватом: без него CompleteRun откажет claim_lost")
}

// ПРОВАЛ ПОСТАВЩИКА ЗАКРЫВАЕТ ПОПЫТКУ И ПРОГОН, А НЕ ОСТАВЛЯЕТ ИХ ВИСЕТЬ.
//
// Это та же причина: строку некому подобрать. Отказ человеку при этом — про поставщика, а не про
// нашу бухгалтерию.
func TestDraftDesignIdeaClosesTheRunWhenTheProviderFails(t *testing.T) {
	rig := newDraftRig(t, http.StatusInternalServerError, "")
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.Unavailable, code)

	require.Len(t, rig.finished, 1)
	require.Equal(t, entity.DesignAttemptFailed, rig.finished[0].State)
	require.Len(t, rig.failed, 1)
	require.Equal(t, "claim-55", rig.failed[0].ClaimToken)
	require.False(t, rig.failed[0].Retryable,
		"ретраить некому: воркер draft_idea не забирает, повтор — это новый клик человека")
	require.Empty(t, rig.completedText)
}

// ПУСТАЯ ДОСКА НЕ ПОКУПАЕТ ВЫЗОВ МОДЕЛИ.
//
// Сторож стоит на mood, а не на длине промпта: промпт несёт ещё имя изделия и фит, поэтому у
// названной карточки он непустой всегда, и сторож по длине не сработал бы НИ РАЗУ. Репозиторий
// здесь строгий — «стор не тронут» тоже измерено.
func TestDraftDesignIdeaRefusesAnEmptyBoard(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards).Maybe()
	bare := &entity.TechCard{}
	bare.Name = "a named card with nothing on its board"
	bare.Fit = sql.NullString{String: "slim", Valid: true}
	cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).Return(bare, nil).Once()
	srv := &Server{
		repo: repo, designGenerationEnabled: true,
		aiOps: openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: "http://127.0.0.1:1"}),
	}
	_, err := srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
}

// ─────────────────── ГРАНИЦА W-15: ОДНА ПРОБА, ДВЕ ПОЛОВИНЫ ───────────────────

// ДОСКА ДОЕЗЖАЕТ ДО ЧЕРНОВИКА ИДЕИ И НЕ ДОЕЗЖАЕТ ДО ГЕНЕРАЦИИ.
//
// РЕШЕНИЕ ВЛАДЕЛЬЦА ДОСЛОВНО: «только в генерации». W-15 запрещает картинки доски во ФЛЭТАХ,
// РЕНДЕРАХ и 3D — и не запрещает их в «набросай идею», про которую прототип обещает «reads the
// pictures» (proto.html:3223) и показывает счётчик «read N pictures» (3231).
//
// ⚠ ПОЧЕМУ ОБЕ ПОЛОВИНЫ В ОДНОЙ ПРОБЕ. После этой правки два соседних глагола ведут себя
// ПО-РАЗНОМУ, и различие не выражено ни типом, ни схемой — оно держится только на коде. Ровно
// такое различие «унифицируют» через полгода, из лучших побуждений, в одну сторону или в другую.
// Стоя порознь, две пробы позволили бы починить одну и не заметить вторую; стоя вместе, они
// краснеют С ОБЕИХ СТОРОН: и если доска перестанет доезжать до черновика, и если начнёт доезжать
// до генерации.
//
// МУТАЦИИ, КОТОРЫМИ ПРОВЕРЕНА (обе — по ЧИСЛУ ИСПОЛНЕННЫХ ИСХОДОВ, не по коду возврата):
//   - вернуть `Complete` вместо `CompleteWithImages` в DraftDesignIdea → краснеет первая половина;
//   - подмешать доску в out.Refs внутри designAssembleInputs → краснеет вторая.
func TestW15BoardReachesTheDraftButNeverGeneration(t *testing.T) {
	// ── ПОЛОВИНА ПЕРВАЯ: черновик идеи ВИДИТ картинки доски ──
	rig := newDraftRig(t, http.StatusOK, "A boxy coat with a storm flap.")
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err)
	require.Equal(t, []string{designBoardMediaURL}, rig.stub.imageURLs(t),
		"«набросай идею» обязан показать модели доску: это разрешённый адресат")
	require.True(t, rig.stub.systemTurnIsAPlainString(t),
		"системный ход обязан остаться строкой: на текстовом пути висит разбор тех-карты с прода")

	// ── ПОЛОВИНА ВТОРАЯ: ни один род ГЕНЕРАЦИИ доску не несёт ──
	card := designMoodCard()
	band := designBandWith(true)
	for _, kind := range []string{
		entity.DesignRunKindFlat, entity.DesignRunKindRender,
		entity.DesignRunKindThreed, entity.DesignRunKindVector,
	} {
		t.Run(kind, func(t *testing.T) {
			snap, err := designAssembleInputs(designInputSources{
				Kind: kind, Card: card, Refs: band.References, Bench: band.Bench,
				Params: &pb_common.DesignRunParams{
					Views: []string{entity.DesignViewFront}, Layout: designLayoutPerView,
				},
			})
			require.NoError(t, err)

			// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ. Без него «доски нет» зеленело бы и на пустом снимке —
			// то есть на сборке, которая не отправляет ВООБЩЕ ничего.
			refIDs := make([]int32, 0, len(snap.GetRefs()))
			for _, r := range snap.GetRefs() {
				refIDs = append(refIDs, r.GetMediaId())
			}
			require.Contains(t, refIDs, int32(designRefMediaID),
				"явно перенесённый референс обязан доехать — иначе проба зелена от пустоты")

			raw, err := designMarshalJSON(snap)
			require.NoError(t, err)
			require.NotContains(t, string(raw), strconv.Itoa(designBoardMediaID),
				"картинка доски в снимке генерации запрещена W-15")
			require.Nil(t, snap.GetMood(),
				"заполненный mood отдал бы воркеру media_id доски прямо в руки")
		})
	}
}

// ─────────────────── ПОТОЛОК ДОСКИ: ЧИСЛО КАРТИНОК СТАЛО ДЕНЬГАМИ ───────────────────

// ДОСКА СВЕРХ ПОТОЛКА — ОТКАЗ ДО ДЕНЕГ, А НЕ ОБРЕЗКА.
//
// ⚠ ПОТОЛОК ПОЯВИЛСЯ ВМЕСТЕ С КАРТИНКАМИ, И ЭТО НЕ СОВПАДЕНИЕ. Пока доска давала только слова,
// цена не зависела от числа плиток; теперь каждая картинка — входные токены. Клиентский MOOD_MAX
// обходится вторым клиентом и повтором запроса, поэтому потолок обязан стоять здесь.
//
// ОТКАЗ, А НЕ ОБРЕЗКА: молча послать 16 из 40 значит показать модели произвольную часть доски и
// выдать ответ по ней за ответ по доске. Отказ называет оба числа — без них его не с чем сравнить.
//
// СТОР НЕ ТРОНУТ ВОВСЕ: строгий мок покраснел бы на любом неожиданном вызове, поэтому «отказ
// пришёл ДО StartRun» здесь измерен, а не заявлен. Это и есть разница между «дорого» и «бесплатно»:
// отказ транспорта прилетел бы уже после резерва денег.
func TestDraftDesignIdeaRefusesABoardOverTheImageCeiling(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards).Maybe()

	card := designMoodCard()
	card.Media = nil
	for i := range openrouter.MaxImageParts + 1 {
		card.Media = append(card.Media, entity.TechCardMediaItem{
			MediaId: 1000 + i, Category: entity.TechCardMediaCategoryMoodboard,
		})
	}
	cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).Return(card, nil).Once()

	srv := &Server{
		repo: repo, designGenerationEnabled: true,
		aiOps: openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: "http://127.0.0.1:1"}),
	}
	_, err := srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.Error(t, err)
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), strconv.Itoa(openrouter.MaxImageParts),
		"отказ обязан назвать потолок, иначе его не с чем сравнить")
	require.Contains(t, err.Error(), strconv.Itoa(openrouter.MaxImageParts+1),
		"и обязан назвать, сколько картинок на доске сейчас")
}

// РОВНО ПОТОЛОК — ПРОХОДИТ, И ЭТО ГРАНИЦА, А НЕ ОКРЕСТНОСТЬ.
//
// Без этой пробы `>` и `>=` неразличимы: обе версии одинаково отказывают на потолке+1, и ошибка
// на единицу молча съела бы у человека одну картинку доски.
func TestDraftDesignIdeaAcceptsABoardExactlyAtTheCeiling(t *testing.T) {
	card := designMoodCard()
	card.Media = nil
	want := make([]string, 0, openrouter.MaxImageParts)
	byID := map[int]entity.MediaFull{}
	ids := make([]int, 0, openrouter.MaxImageParts)
	for i := range openrouter.MaxImageParts {
		id := 1000 + i
		url := fmt.Sprintf("https://cdn.grbpwr.com/media/%02x/full.jpg", i)
		card.Media = append(card.Media, entity.TechCardMediaItem{
			MediaId: id, Category: entity.TechCardMediaCategoryMoodboard,
		})
		byID[id] = entity.MediaFull{Id: id, MediaItem: entity.MediaItem{FullSizeMediaURL: url}}
		ids = append(ids, id)
		want = append(want, url)
	}

	rig := newDraftRigWithCard(t, http.StatusOK, "A boxy coat.", card, ids, byID)
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err, "доска ровно в потолок обязана пройти")
	require.Equal(t, want, rig.stub.imageURLs(t),
		"на провод обязаны уйти ВСЕ картинки доски, в порядке карточки")
}

// ─────────────────────── W-3: СЛОВА ЧЕЛОВЕКА ДОЕЗЖАЮТ ДО МОДЕЛИ ───────────────────────

const (
	// designGarmentWords — описание ИЗДЕЛИЯ. Строка нарочно ни на что не похожа: её ищут в
	// закодированном снимке, и совпадение со словом из соседнего поля дало бы ложную зелень.
	designGarmentWords = "GARMENTWORDS-oversized-boxy-shirt"
	// designRefNote — записка про ОДНУ картинку.
	designRefNote = "REFNOTE-only-the-collar"
	// designRefCalloutWords — текст выноски, приколотой на ту же картинку.
	designRefCalloutWords = "REFCALLOUT-topstitch-here"
)

// designW3Card — карточка, у которой заполнены ОБА текстовых поля: описание изделия (уходит в
// каждый прогон) и записка доски (не уходит никуда, кроме черновика идеи). Оба присутствуют
// одновременно НАМЕРЕННО: проба, где заполнено только одно, не отличила бы «доехало нужное» от
// «доехало хоть что-нибудь».
func designW3Card() *entity.TechCard {
	card := designMoodCard()
	card.GarmentDescription = sql.NullString{String: designGarmentWords, Valid: true}
	// Выноска на РЕФЕРЕНСЕ — на той картинке, которую человек явно перенёс в INPUT — REFERENCES.
	card.Callouts = append(card.Callouts, entity.TechCardCallout{
		Number:      7,
		Part:        sql.NullString{String: "cuff", Valid: true},
		Description: sql.NullString{String: designRefCalloutWords, Valid: true},
		MediaId:     sql.NullInt32{Int32: designRefMediaID, Valid: true},
		// ГЕОМЕТРИЯ НАСТОЯЩАЯ, А НЕ ЗАГЛУШКА: пробе нужен вид, который переживает конвертацию
		// в проводной энум, иначе «аннотация есть» зеленело бы на пустом сообщении.
		Kind:   entity.AnnotationKindLabel,
		PosX:   decimal.NullDecimal{Decimal: decimal.NewFromFloat(0.25), Valid: true},
		PosY:   decimal.NullDecimal{Decimal: decimal.NewFromFloat(0.5), Valid: true},
		Points: []entity.TechCardAnnotationPoint{{X: decimal.NewFromFloat(0.25), Y: decimal.NewFromFloat(0.5)}},
	})
	return card
}

// designW3Refs — референс С ЗАПИСКОЙ. Та же строка design_reference, что и в designBandWith, плюс
// колонка note, ради которой всё и затевалось.
func designW3Refs() []entity.DesignReference {
	return []entity.DesignReference{{
		TechCardId: designRunCardID,
		MediaId:    designRefMediaID,
		Role:       entity.DesignViewFront,
		Note:       sql.NullString{String: designRefNote, Valid: true},
		Ordinal:    1,
	}}
}

// ОПИСАНИЕ ИЗДЕЛИЯ, ЗАПИСКА РЕФЕРЕНСА И ЕГО РАЗМЕТКА ДОЕЗЖАЮТ ДО СНИМКА — ПОД ИМЕНАМИ КОНТРАКТА.
//
// ЭТО ЦЕНТР ТРЕБОВАНИЯ W-3, и до этой волны все три поля контракта были писателем не наполнены:
// колонки существовали, поля на проводе существовали, а сущности Go их не несли — значит снимок
// уверял, что модель ничего не читала, тогда как человек писал.
//
// ⚠ ПРОБА СМОТРИТ НА ЗАКОДИРОВАННЫЙ JSON, А НЕ ТОЛЬКО НА СООБЩЕНИЕ, и это не придирка. Воркер
// читает ИМЕННО эти байты своим узким разбором по snake_case (internal/designgen/snapshot.go), и
// поле, доехавшее до сообщения, но уехавшее в снимок под другим именем, было бы для него пустым —
// молча, без единой ошибки.
func TestDesignRunInputsCarryTheGarmentDescriptionTheNoteAndTheMarkup(t *testing.T) {
	snap, err := designAssembleInputs(designInputSources{
		Kind:   entity.DesignRunKindFlat,
		Card:   designW3Card(),
		Refs:   designW3Refs(),
		Bench:  designBandWith(true).Bench,
		Params: &pb_common.DesignRunParams{Layout: designLayoutOne},
	})
	require.NoError(t, err)

	require.Equal(t, designGarmentWords, snap.GetGarmentNote(),
		"описание изделия — то, что уходит в КАЖДЫЙ прогон; пустое здесь значит промпт без изделия")
	require.Len(t, snap.GetRefs(), 1)
	ref := snap.GetRefs()[0]
	require.Equal(t, int32(designRefMediaID), ref.GetMediaId())
	require.Equal(t, designRefNote, ref.GetNote(),
		"записка «что эта картинка добавляет» обязана доехать вместе с картинкой")
	require.Len(t, ref.GetCallouts(), 1, "разметка референса замерзает вместе с ним")
	require.Contains(t, ref.GetCallouts()[0].GetText(), designRefCalloutWords)
	require.NotNil(t, ref.GetCallouts()[0].GetAnnotation(),
		"геометрия выноски тоже замерзает: снимок без неё не воспроизводит прогон, который описывает")

	// ── ИМЕНА НА ПРОВОДЕ: ровно те, по которым читает воркер ──
	raw, err := designMarshalJSON(snap)
	require.NoError(t, err)
	text := string(raw)
	require.Contains(t, text, `"garment_note"`)
	require.Contains(t, text, designGarmentWords)
	require.Contains(t, text, `"note"`)
	require.Contains(t, text, designRefNote)
	require.Contains(t, text, `"callouts"`)
	require.Contains(t, text, designRefCalloutWords)

	// ── и доска по-прежнему не едет: W-3 не отменяет W-15 ──
	require.NotContains(t, text, "MOODWORDS",
		"описание изделия — это НЕ записка доски; их слияние отправило бы доску в генерацию")
	require.NotContains(t, text, "MOODCALLOUT",
		"выноска на картинке доски не приколота ни к одному референсу и ехать ей некуда")
}

// ВЫНОСКА, ПРИКОЛОТАЯ НЕ К РЕФЕРЕНСУ, В СНИМОК НЕ ПОПАДАЕТ.
//
// Положительный контроль к пробе выше и одновременно граница: разметка едет ЗА КАРТИНКОЙ, а не
// сама по себе. Карта выносок строится по всей карточке, и если бы её ключи перестали спрашивать
// по отобранным референсам, в снимок уехала бы разметка технического эскиза и доски.
func TestDesignRunInputsTakeOnlyTheMarkupOfTheReferencesThemselves(t *testing.T) {
	card := designW3Card()
	card.Callouts = append(card.Callouts, entity.TechCardCallout{
		Number:      9,
		Description: sql.NullString{String: "TECHNICAL-not-a-reference", Valid: true},
		MediaId:     sql.NullInt32{Int32: designTechnicalMediaID, Valid: true},
	})
	snap, err := designAssembleInputs(designInputSources{
		Kind:   entity.DesignRunKindFlat,
		Card:   card,
		Refs:   designW3Refs(),
		Params: &pb_common.DesignRunParams{Layout: designLayoutOne},
	})
	require.NoError(t, err)
	raw, err := designMarshalJSON(snap)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "TECHNICAL-not-a-reference")
	// Положительный контроль: та выноска, что приколота НА референс, на месте.
	require.Contains(t, string(raw), designRefCalloutWords)
}

// РЕРАН НЕ ПОДБИРАЕТ СЕГОДНЯШНЕЕ ОПИСАНИЕ ИЗДЕЛИЯ.
//
// Описание замораживается КОПИЕЙ ровно затем, чтобы правка завтра не переписала то, что сказали
// модели вчера. Проба ставит эксперимент, в котором источники РАСХОДЯТСЯ: у родителя одно
// описание, на карточке сегодня — другое.
func TestDesignRerunKeepsTheParentsGarmentDescription(t *testing.T) {
	parentInputs, err := designMarshalJSON(&pb_common.DesignInputSnapshot{
		GarmentNote: "YESTERDAY-cropped-jacket",
		Refs:        []*pb_common.DesignInputRef{{MediaId: 700}},
	})
	require.NoError(t, err)
	parent := &entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindFlat,
		Inputs: entity.RawJSON(parentInputs),
	}
	srv := &Server{}
	snap, _, err := srv.designRunInputs(context.Background(), designInputSources{
		Kind:   entity.DesignRunKindFlat,
		Card:   designW3Card(), // сегодня на карточке designGarmentWords
		Refs:   designW3Refs(),
		Params: &pb_common.DesignRunParams{Layout: designLayoutOne},
	}, parent)
	require.NoError(t, err)
	require.Equal(t, "YESTERDAY-cropped-jacket", snap.GetGarmentNote())
	require.NotContains(t, snap.GetGarmentNote(), designGarmentWords)
}

// ─────────────────────── W-12 и импорт вектора: ДВА БЕСПЛАТНЫХ ГЛАГОЛА ───────────────────────

// ОТМЕТКА «ВЫБРАН» РАБОТАЕТ ПРИ ВЫКЛЮЧЕННОЙ ГЕНЕРАЦИИ.
//
// ⚠ ЭТО И ЕСТЬ ПРОБА ГРАНИЦЫ ФЛАГА, а не проверка проводки. DESIGN_GENERATION_ENABLED стережёт
// ДЕНЬГИ; отметить кадр выбранным не тратит ни цента. Навешенный сюда флаг выключил бы обычную
// работу с полосой в деплое, где генерация просто не включена, — и заметили бы это только руками.
func TestSetDesignPictureSelectedIsNotGatedByTheMoneyFlag(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	dsg := mocks.NewMockDesign(t)
	repo.EXPECT().Design().Return(dsg).Once()
	dsg.EXPECT().SetPictureSelected(mock.Anything, 77, true, "designer").
		Return(&entity.DesignPicture{
			Id: 77, TechCardId: designRunCardID, Kind: entity.DesignPictureKindThreed, Selected: true,
		}, nil).Once()
	srv := &Server{repo: repo} // флаг НЕ выставлен => генерация выключена
	resp, err := srv.SetDesignPictureSelected(designRunCtx(), &pb_admin.SetDesignPictureSelectedRequest{
		PictureId: 77, Selected: true,
	})
	require.NoError(t, err)
	require.True(t, resp.GetPicture().GetSelected(),
		"отметка обязана вернуться на проводе: она и есть ответ глагола")
}

// ИМПОРТ ВЕКТОРА ТОЖЕ НЕ ПРОХОДИТ ЧЕРЕЗ ДЕНЕЖНЫЕ ВОРОТА, и провенанс доезжает до провода.
//
// Векторизация МАШИНОЙ — платный вызов и идёт через StartDesignRun с kind = vector; этот глагол
// подшивает файл, который уже загружен. Две двери для денег означали бы две проверки бюджета.
func TestImportDesignVectorIsNotGatedAndCarriesTheProvenance(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	dsg := mocks.NewMockDesign(t)
	repo.EXPECT().Design().Return(dsg).Once()
	var sent entity.DesignVectorImport
	dsg.EXPECT().ImportVector(mock.Anything, mock.AnythingOfType("entity.DesignVectorImport")).
		Run(func(_ context.Context, req entity.DesignVectorImport) { sent = req }).
		Return(&entity.DesignEditLayer{
			Id: 31, TechCardId: designRunCardID, Rev: 1,
			Origin:        entity.DesignLayerOriginVectorised,
			SourceMediaId: sql.NullInt32{Int32: 555, Valid: true},
			Strokes:       entity.RawJSON(`[{"d":"M0 0"}]`),
		}, nil).Once()
	srv := &Server{repo: repo} // флаг НЕ выставлен
	resp, err := srv.ImportDesignVector(designRunCtx(), &pb_admin.ImportDesignVectorRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "44444444-4444-4444-4444-444444444444",
		SourceMediaId:   555,
		SourcePictureId: 77,
		Origin:          entity.DesignLayerOriginVectorised,
		Strokes:         []byte(`[{"d":"M0 0"}]`),
	})
	require.NoError(t, err)
	require.Equal(t, 555, sent.SourceMediaId, "файл — это и есть предмет глагола")
	require.Equal(t, 77, sent.SourcePictureId)
	require.Equal(t, entity.DesignLayerOriginVectorised, sent.Origin)
	require.Equal(t, "designer", sent.Actor)
	require.Equal(t, entity.DesignLayerOriginVectorised, resp.GetLayer().GetOrigin(),
		"провенанс обязан вернуться: без него смешанность вектора невычислима")
	require.Equal(t, int32(555), resp.GetLayer().GetSourceMediaId(),
		"ребро «слой ↔ файл» и есть то, ради чего глагол существует")
	require.NotEmpty(t, resp.GetLayer().GetStrokes(), "штрихи эхом, как велит контракт ответа")
}

// ПУСТОЙ СЛОЙ ЧИТАЕТСЯ КАК drawn НА ЧТЕНИИ.
//
// Каждая строка до 0350 родилась рисованием, других способов не было. Пустая строка на проводе
// заставила бы клиента знать, что умолчание написала миграция, а не писатель.
func TestDesignLayerOriginReadsAsDrawnWhenTheColumnIsEmpty(t *testing.T) {
	pb := designLayerToPb(entity.DesignEditLayer{Id: 1, TechCardId: designRunCardID, Rev: 3}, false)
	require.Equal(t, entity.DesignLayerOriginDrawn, pb.GetOrigin())
}

// ИМПОРТ БЕЗ КЛЮЧА ЗАПРОСА И СО СЛИШКОМ БОЛЬШИМИ ШТРИХАМИ ОТКАЗЫВАЕТСЯ ДО СТОРА.
//
// Репозиторий здесь строгий, поэтому «стор не тронут» — измеренный факт: незаявленный вызов
// уронил бы пробу.
func TestImportDesignVectorRefusesBeforeTheStore(t *testing.T) {
	srv := &Server{repo: mocks.NewMockRepository(t)}

	_, err := srv.ImportDesignVector(designRunCtx(), &pb_admin.ImportDesignVectorRequest{
		TechCardId: designRunCardID, SourceMediaId: 555, Origin: entity.DesignLayerOriginImported,
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.ImportDesignVector(designRunCtx(), &pb_admin.ImportDesignVectorRequest{
		TechCardId: designRunCardID, ClientRequestId: "c", SourceMediaId: 555,
		Origin: entity.DesignLayerOriginImported,
		// Штрихи сверх потолка режутся ДО стора: 512 KB на слой — потолок контракта.
		Strokes: make([]byte, design.MaxStrokesBytes+1),
	})
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Equal(t, "strokes_too_large", md["reason"])

	_, err = srv.ImportDesignVector(designRunCtx(), &pb_admin.ImportDesignVectorRequest{
		TechCardId: designRunCardID, ClientRequestId: "c", SourceMediaId: 555,
		Origin: entity.DesignLayerOriginImported, Strokes: []byte("not json"),
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ─────────────────── РЕРАН ЧЕРЕЗ РОД ───────────────────

// ПОВТОРИТЬ МОЖНО ТОЛЬКО ПРОГОН СВОЕГО РОДА.
//
// ЧТО БЫЛО. Родитель проверялся на принадлежность карточке и на «не текстовый» — и ни разу на
// СОВПАДЕНИЕ РОДА. А входы родителя копируются ДОСЛОВНО (designRunInputs: «из сегодняшнего
// состояния карточки берётся ноль полей»), и в снимке слота рода нет вовсе: `DesignInputSlot`
// несёт вид и media_id, но не говорит, флэт это или плита рендера.
//
// ЧЕМ ЭТО КОНЧАЛОСЬ. `StartDesignRun(kind=threed, rerun_of_run_id=R)`, где R — рендер-прогон:
// вызов принимался, ФЛЭТЫ R копировались в снимок нового прогона, и фильтр 3D (threedPictures)
// честно узнавал в них «плиты этого прогона» — потому что для него они ими и стали. К провайдеру
// уезжали технические чертежи вместо четырёх видов готовой вещи, поворотный стол строился по ним,
// прогон закрывался `done`. Это тот же V-14, зашедший с другой стороны: там вход считали два
// писателя, здесь его подменяет род.
//
// ⚠ ОТКАЗ СТОИТ У ДВЕРИ, А НЕ В ВОРКЕРЕ, потому что StartRun РЕЗЕРВИРУЕТ ДЕНЬГИ: отказ после
// резерва стоил бы дню оплаченной строки. Тот же довод, по которому здесь стоят ворота W-13.
func TestDesignRerunRefusesAParentOfAnotherKind(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.design.EXPECT().GetRun(mock.Anything, 12).Return(&entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindRender,
		// Замороженные слоты рендер-прогона — ФЛЭТЫ, из которых рендер и делали.
		Inputs: entity.RawJSON(`{"slots":[{"view_key":"front","media_id":501}]}`),
	}, nil).Once()

	req := designStartRequest(entity.DesignRunKindThreed)
	req.RerunOfRunId = 12
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err, "3D не повторяет рендер: у них разные входы и разный смысл слова «плита»")
	code, _ := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Contains(t, err.Error(), "render")
	require.Nil(t, rig.sent, "отказ обязан прийти ДО резерва денег")
}

// ...И ПОВТОР СВОЕГО РОДА ПО-ПРЕЖНЕМУ РАБОТАЕТ.
//
// Без этой половины предыдущая проба зеленела бы и на «запретить реран вовсе», то есть на починке,
// которая убивает саму функцию.
func TestDesignRerunOfTheSameKindStillRepeatsIt(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.design.EXPECT().GetRun(mock.Anything, 12).Return(&entity.DesignRun{
		Id: 12, TechCardId: designRunCardID, Kind: entity.DesignRunKindThreed,
		Inputs: entity.RawJSON(`{"slots":[{"view_key":"front","media_id":601}]}`),
	}, nil).Once()

	req := designStartRequest(entity.DesignRunKindThreed)
	req.RerunOfRunId = 12
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))
	require.Len(t, snap.GetSlots(), 1)
	require.Equal(t, int32(601), snap.GetSlots()[0].GetMediaId(),
		"повторение обязано послать модели ТО ЖЕ САМОЕ: плиты родителя, а не сегодняшний верстак")
}
