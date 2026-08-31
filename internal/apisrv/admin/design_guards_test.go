package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ПРОБЫ ВОРОТ, КОТОРЫЕ СТЕРЕГУТ ПРАВДУ: цену, происхождение входа и то, что дверь приняла.
//
// ЧТО ЭТИ ПРОБЫ МОГУТ ДОКАЗАТЬ. Стор здесь замокан, значит ни резерв, ни транзакция, ни
// идемпотентность ими не проверяются — это собственность internal/store/design. Предмет проб —
// ровно то, что дверь ОТДАЛА стору (entity.DesignRunStart) и то, чем она ОТКАЗАЛА: и то и другое
// замерзает навсегда либо тратит деньги, и ломается молча.
//
// ⚠ ПОЧЕМУ ВЕЗДЕ ПРОВЕРЯЕТСЯ ОТДАННОЕ СТОРУ, А НЕ ВОЗВРАТ ХЕЛПЕРА. Хелпер, посчитавший правильную
// цену, ничего не доказывает про место вызова: этот дефект ровно так и выглядел — таблица оценок
// существовала, а рядом с ней стояла своя копия цены. Красным обязан становиться ПРОВОД.

const (
	// designGuardCardID — карточка, о которой идёт речь.
	designGuardCardID = 41
	// designGuardOtherCardID — ЧУЖАЯ карточка. Всё, что принадлежит ей, обязано быть отвергнуто.
	designGuardOtherCardID = 42
)

func designGuardCtx() context.Context {
	return authsrv.PutAdminUsername(fullAccessCtx(), "designer")
}

// ─────────────────────── стенд ───────────────────────

type designGuardRig struct {
	srv    *Server
	repo   *mocks.MockRepository
	cards  *mocks.MockTechCards
	design *mocks.MockDesign
	// foreign — множество медиа, про которые СТОР говорит «принадлежит другой карточке».
	//
	// ⚠ ЗДЕСЬ ЗАМОКАН ОТВЕТ, А НЕ ПРАВИЛО, И ЭТО ГРАНИЦА ТОГО, ЧТО ЭТИ ПРОБЫ ДОКАЗЫВАЮТ. Само
	// правило («держателей двое: tech_card_media и design_picture; ничейное проходит») живёт в
	// сторе, ходит в две таблицы и проверяется живыми пробами базы (layer_import_db_test.go).
	// Здесь проверяется дверь: что она СПРАШИВАЕТ — до резерва денег — и что делает с ответом.
	foreign map[int]bool
	// sent — то, что дверь отдала стору. nil значит, что до денег дело не дошло.
	sent *entity.DesignRunStart
	// asked — номера, о которых дверь спросила. Пустой список при непустых входах значит, что
	// ворота не сработали вовсе.
	asked []int
}

func newDesignGuardRig(t *testing.T, card *entity.TechCard, band *entity.DesignBand) *designGuardRig {
	t.Helper()
	rig := &designGuardRig{
		repo:    mocks.NewMockRepository(t),
		cards:   mocks.NewMockTechCards(t),
		design:  mocks.NewMockDesign(t),
		foreign: map[int]bool{},
	}
	rig.repo.EXPECT().TechCards().Return(rig.cards).Maybe()
	rig.repo.EXPECT().Design().Return(rig.design).Maybe()
	rig.cards.EXPECT().GetTechCardById(mock.Anything, designGuardCardID).Return(card, nil).Maybe()
	rig.design.EXPECT().GetBand(mock.Anything, designGuardCardID, mock.Anything).Return(band, nil).Maybe()
	rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, cardID int, ids []int) error {
			rig.asked = append(rig.asked, ids...)
			for _, id := range ids {
				if rig.foreign[id] {
					return fmt.Errorf("%w: media %d belongs to another tech card, not to %d",
						entity.ErrDesignForeignMedia, id, cardID)
				}
			}
			return nil
		}).Maybe()
	rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Run(func(_ context.Context, req entity.DesignRunStart) {
			cp := req
			rig.sent = &cp
		}).
		Return(&entity.DesignRunStarted{
			Run:    entity.DesignRun{Id: 900, TechCardId: designGuardCardID, Status: entity.DesignRunPending},
			Budget: entity.DesignBudget{Day: "2026-08-30"},
		}, nil).Maybe()
	rig.srv = &Server{repo: rig.repo, designGenerationEnabled: true}
	return rig
}

// designGuardCard — карточка без единого длинного поля: всё, что меряется потолками, задаётся
// пробой явно.
func designGuardCard() *entity.TechCard {
	card := &entity.TechCard{}
	card.Id = designGuardCardID
	card.Name = "guarded"
	card.Fit = sql.NullString{String: "oversized", Valid: true}
	return card
}

// designGuardBand — полоса с рендером (иначе 3D закрыто воротами W-13) и двумя плитами: флэт и
// рендер одного вида, чтобы отбор было чем ошибиться.
func designGuardBand() *entity.DesignBand {
	return &entity.DesignBand{
		HasFabricRender: true,
		Bench: []entity.DesignBenchSlot{
			{
				Id: 11, TechCardId: designGuardCardID, ViewKey: entity.DesignViewFront,
				Kind:    entity.DesignPictureKindRender,
				Picture: &entity.DesignPicture{Id: 501, TechCardId: designGuardCardID, MediaId: 201},
			},
			{
				Id: 12, TechCardId: designGuardCardID, ViewKey: entity.DesignViewBack,
				Kind:    entity.DesignPictureKindRender,
				Picture: &entity.DesignPicture{Id: 502, TechCardId: designGuardCardID, MediaId: 202},
			},
			{
				Id: 13, TechCardId: designGuardCardID, ViewKey: entity.DesignViewFront,
				Kind:    entity.DesignPictureKindFlat,
				Picture: &entity.DesignPicture{Id: 503, TechCardId: designGuardCardID, MediaId: 203},
			},
		},
	}
}

func designGuardStart(kind string) *pb_admin.StartDesignRunRequest {
	return &pb_admin.StartDesignRunRequest{
		TechCardId:      designGuardCardID,
		ClientRequestId: "11111111-1111-1111-1111-" + strings.Repeat("1", 12),
		Kind:            kind,
	}
}

// ─────────────────────── 1. ЦЕНА ───────────────────────

// РЕЗЕРВ ВЕКТОРА ПОКРЫВАЕТ ЛЮБОЙ ОПУБЛИКОВАННЫЙ ТАРИФ ПРОВАЙДЕРА.
//
// ЧТО БЫЛО: дверь резервировала $0.04, а собственная константа пакета списания говорит $0.08 за
// стандартный тир и $0.30 за pro. Дневной потолок пропускал вдвое больше трат, чем согласовано, и
// молча.
//
// ⚠ ТРЕБОВАНИЕ ЗДЕСЬ СФОРМУЛИРОВАНО НЕЗАВИСИМО ОТ РЕАЛИЗАЦИИ — через recraft.Tiers(), то есть
// через ТОТ ЖЕ ИСТОЧНИК, из которого берёт число списание, плюс жёсткий якорь в долларах. Проба,
// сверяющая таблицу оценок с самой собой, зеленела бы под любой правкой обеих.
func TestVectorReserveCoversEveryPublishedTier(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
	_, err := rig.srv.StartDesignRun(designGuardCtx(), designGuardStart(entity.DesignRunKindVector))
	require.NoError(t, err)
	require.NotNil(t, rig.sent, "прогон обязан был дойти до стора")
	require.True(t, rig.sent.PriceEstimate.Valid, "у платного рода обязана быть оценка")

	got := rig.sent.PriceEstimate.Decimal
	for _, tier := range recraft.Tiers() {
		published := decimal.NewFromFloat(tier.EstimatedUSD())
		require.Falsef(t, got.LessThan(published),
			"резерв %s ниже опубликованной цены тира %s ($%s): дневной потолок пропустит больше, "+
				"чем владелец согласился оплатить", got, tier, published)
	}
	// ЯКОРЬ В ДОЛЛАРАХ. Он повторяет число провайдера НАМЕРЕННО: требование обязано быть сказано
	// в пробе своими словами, иначе таблица оценок сверяется сама с собой.
	require.False(t, got.LessThan(decimal.RequireFromString("0.30")),
		"pro-вектор стоит $0.30; резерв ниже факта недопустим")
}

// РЕЗЕРВ КАРТИНКИ ПОКРЫВАЕТ САМОЕ ДОРОГОЕ ПОЛОЖЕНИЕ ДИЛА DESIGN_IMAGE_QUALITY.
//
// ЧТО БЫЛО: плоская константа БЕЗ члена качества вовсе, тогда как качество — крупнейший множитель
// цены прессы (orimages: «`high` roughly four times `medium`»), и на `high` кадр стоил кратно
// дороже оценки. Связи между дилом и резервом не было никакой.
func TestImageReserveCoversTheDearestQualityDial(t *testing.T) {
	for _, tc := range []struct {
		kind string
		// floor — цена кадра на `high`, сказанная пробой СВОИМИ ЧИСЛАМИ: базовая догадка волны
		// ($0.04 флэт, $0.08 рендер) на замеренный множитель качества (4160/1056 токенов ≈ ×4).
		floor string
	}{
		{entity.DesignRunKindFlat, "0.16"},
		{entity.DesignRunKindRender, "0.32"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
			_, err := rig.srv.StartDesignRun(designGuardCtx(), designGuardStart(tc.kind))
			require.NoError(t, err)
			require.NotNil(t, rig.sent)
			require.True(t, rig.sent.PriceEstimate.Valid)
			require.Falsef(t, rig.sent.PriceEstimate.Decimal.LessThan(decimal.RequireFromString(tc.floor)),
				"резерв %s ниже цены кадра на DESIGN_IMAGE_QUALITY=high ($%s)",
				rig.sent.PriceEstimate.Decimal, tc.floor)
		})
	}
}

// ОЦЕНКА УМНОЖАЕТСЯ НА ЧИСЛО КАДРОВ, А НЕ НА ОДИН.
//
// Положительный контроль к двум пробам выше: резерв, потерявший множитель выходов, тоже «не ниже
// потолка одного кадра» — и обе они остались бы зелёными.
func TestReserveScalesWithTheFramesAsked(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
	req := designGuardStart(entity.DesignRunKindFlat)
	req.Params = &pb_common.DesignRunParams{
		Layout: "per_view",
		Views:  []string{entity.DesignViewFront, entity.DesignViewBack, entity.DesignViewSideL},
	}
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Equal(t, 3, rig.sent.RequestedOutputs)

	one := newDesignGuardRig(t, designGuardCard(), designGuardBand())
	_, err = one.srv.StartDesignRun(designGuardCtx(), designGuardStart(entity.DesignRunKindFlat))
	require.NoError(t, err)
	require.NotNil(t, one.sent)
	require.Equal(t, 1, one.sent.RequestedOutputs)
	require.Equal(t,
		one.sent.PriceEstimate.Decimal.Mul(decimal.NewFromInt(3)).String(),
		rig.sent.PriceEstimate.Decimal.String(),
		"три кадра обязаны резервировать втрое против одного")
}

// ─────────────────────── 4. ЧУЖОЕ МЕДИА ───────────────────────

// КАРТИНКА ЧУЖОЙ КАРТОЧКИ НЕ УХОДИТ В ПЛАТНУЮ ГЕНЕРАЦИЮ.
//
// ЧТО БЫЛО: `extra_input_media_ids` проверялись только на «> 0», то есть любой номер из системы
// уезжал поставщику и замерзал в снимке как вход этого прогона.
func TestRunRefusesAPictureOfAnotherCard(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
	const foreignMedia = 777
	rig.foreign[foreignMedia] = true

	req := designGuardStart(entity.DesignRunKindRender)
	req.Params = &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{foreignMedia}}
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.Error(t, err)
	// FailedPrecondition + reason=foreign_media — ровно то, чем этот же сентинел отвечает у
	// SetDesignReferenceRole (designRefusals). ОДНА новость обязана звучать одинаково, откуда бы
	// ни пришла: клиент ветвится по слову, а не по тому, какой глагол её родил.
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "another tech card")
	require.Equal(t, []int{foreignMedia}, rig.asked, "дверь обязана СПРОСИТЬ про названный номер")
	require.Nil(t, rig.sent, "отказ обязан прийти ДО стора: иначе день уже потерял резерв")
}

// ТО ЖЕ ПРАВИЛО ДЛЯ ФОТО ТКАНИ РЕЦЕПТА ЦВЕТА.
//
// `colour.fabric_media_id` — второй номер медиа, который приезжает с провода и который воркер
// кладёт в ссылки прогона (designgen/snapshot.go: referenceMediaIDs). Дефект у них один, и
// закрыты они обязаны быть одним правилом.
func TestRunRefusesAFabricPhotoOfAnotherCard(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
	const foreignFabric = 778
	rig.foreign[foreignFabric] = true

	req := designGuardStart(entity.DesignRunKindRender)
	req.Params = &pb_common.DesignRunParams{
		Colour: &pb_common.DesignColourRecipe{Source: "photo", FabricMediaId: foreignFabric},
	}
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, rig.asked, foreignFabric, "про фото ткани обязаны спросить отдельно")
	require.Nil(t, rig.sent)
}

// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ, БЕЗ КОТОРОГО ДВЕ ПРОБЫ ВЫШЕ НИЧЕГО НЕ СТОЯТ.
//
// Ворота, отказывающие ВСЕМУ, отказывают и чужому. Здесь проверяются оба законных входа: картинка
// ЭТОЙ карточки и файл, на который не ссылается ещё никто (свежая загрузка — по контракту
// нормальный случай, и положительное правило «обязано лежать в карточке» ломало бы его).
func TestRunAcceptsItsOwnPictureAndAFreshUpload(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
	const ownMedia, freshMedia = 300, 301
	// Ни один из двух не объявлен чужим: первый — картинка этой карточки, второй — свежая
	// загрузка, за которой не стоит ещё ни одна карточка. Оба законны.

	req := designGuardStart(entity.DesignRunKindRender)
	req.Params = &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{ownMedia, freshMedia}}
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent, "законный вход обязан доехать до стора")
	// ОБА НАЗВАННЫХ ВХОДА ОБЯЗАНЫ ОКАЗАТЬСЯ В СНИМКЕ. Ворота, пропустившие запрос и потерявшие
	// вход, зеленели бы на одной проверке «отказа не было».
	snap := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Inputs, snap))
	got := make([]int32, 0, len(snap.GetRefs()))
	for _, r := range snap.GetRefs() {
		got = append(got, r.GetMediaId())
	}
	require.ElementsMatch(t, []int32{ownMedia, freshMedia}, got,
		"снимок обязан нести оба явно названных входа")
}

// ДВЕРЬ СПРАШИВАЕТ ПРО ВСЕ НАЗВАННЫЕ НОМЕРА, А НЕ ПРО ПЕРВЫЙ.
//
// Ворота, спросившие про один номер из трёх, зеленеют на пробе про чужой первый номер и молча
// пропускают чужой третий.
func TestEveryNamedInputIsChecked(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
	rig.foreign[303] = true
	req := designGuardStart(entity.DesignRunKindRender)
	req.Params = &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{301, 302, 303}}
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.Error(t, err, "чужой номер обязан быть найден, на каком бы месте он ни стоял")
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, []int{301, 302, 303}, rig.asked)
	require.Nil(t, rig.sent)
}

// ─────────────────────── 5. ПОТОЛКИ ───────────────────────

// ОПИСАНИЕ ИЗДЕЛИЯ ИМЕЕТ ПОТОЛОК, И ЭТО ОТКАЗ, А НЕ ОБРЕЗКА.
//
// Поле уезжает в КАЖДЫЙ прогон и замерзает в снимке. Колонка объявлена TEXT, то есть до 64 KB —
// ровно столько же, сколько отпущено ВСЕМУ снимку.
func TestRunRefusesAnOverlongGarmentDescription(t *testing.T) {
	card := designGuardCard()
	card.GarmentDescription = sql.NullString{
		String: strings.Repeat("a", designMaxGarmentNoteRunes+1), Valid: true,
	}
	rig := newDesignGuardRig(t, card, designGuardBand())
	_, err := rig.srv.StartDesignRun(designGuardCtx(), designGuardStart(entity.DesignRunKindFlat))
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "garment description")
	require.Nil(t, rig.sent, "отказ обязан стоять ДО резерва")
}

// ПОТОЛОК ОПИСАНИЯ СЧИТАЕТ РУНЫ, А НЕ БАЙТЫ, И ЦЕЛОЕ ОПИСАНИЕ ДОЕЗЖАЕТ ЦЕЛИКОМ.
//
// Двойной контроль: кириллица весит вдвое, и потолок по байтам отказал бы законному описанию на
// половине длины; а описание, доехавшее до стора обрезанным, утверждало бы в истории слова,
// которых человек не писал.
func TestGarmentDescriptionCeilingCountsRunesAndNeverTrims(t *testing.T) {
	card := designGuardCard()
	full := strings.Repeat("я", designMaxGarmentNoteRunes)
	card.GarmentDescription = sql.NullString{String: full, Valid: true}
	rig := newDesignGuardRig(t, card, designGuardBand())
	_, err := rig.srv.StartDesignRun(designGuardCtx(), designGuardStart(entity.DesignRunKindFlat))
	require.NoError(t, err, "описание ровно в потолок рун обязано пройти")
	require.NotNil(t, rig.sent)
	require.Contains(t, string(rig.sent.Inputs), full,
		"описание обязано замёрзнуть в снимке ЦЕЛИКОМ: обрезка молча переписала бы просьбу")
}

// ЗАПИСКА РЕФЕРЕНСА ИМЕЕТ ПОТОЛОК.
//
// Она уезжает в снимок каждого прогона, который возьмёт эту картинку, и там замерзает; колонка
// TEXT позволяет 64 KB на ОДНУ записку при 24 референсах на снимок.
func TestReferenceNoteHasACeiling(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	srv := &Server{repo: repo}
	_, err := srv.SetDesignReferenceRole(designGuardCtx(), &pb_admin.SetDesignReferenceRoleRequest{
		TechCardId: designGuardCardID,
		MediaId:    100,
		Role:       entity.DesignViewFront,
		Note:       proto.String(strings.Repeat("a", designMaxRefNoteRunes+1)),
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "note is")
	// Стор НЕ ЗВАЛСЯ вовсе: строгий мок покраснел бы на неожиданном вызове Design().
}

// ПОТОЛОК ЗАПИСКИ СЧИТАЕТ РУНЫ, И ЗАПИСКА В ПОТОЛОК ДОЕЗЖАЕТ ДОСЛОВНО.
func TestReferenceNoteCeilingCountsRunes(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	dsg := mocks.NewMockDesign(t)
	repo.EXPECT().Design().Return(dsg).Once()
	note := strings.Repeat("я", designMaxRefNoteRunes)
	var sent entity.DesignReferenceRole
	dsg.EXPECT().SetReferenceRole(mock.Anything, mock.AnythingOfType("entity.DesignReferenceRole")).
		Run(func(_ context.Context, req entity.DesignReferenceRole) { sent = req }).
		Return(&entity.DesignReference{TechCardId: designGuardCardID, MediaId: 100}, nil).Once()

	srv := &Server{repo: repo}
	_, err := srv.SetDesignReferenceRole(designGuardCtx(), &pb_admin.SetDesignReferenceRoleRequest{
		TechCardId: designGuardCardID, MediaId: 100,
		Role: entity.DesignViewFront, Note: proto.String(note),
	})
	require.NoError(t, err, "записка ровно в потолок рун обязана пройти")
	require.Equal(t, note, sent.Note, "записка обязана доехать до стора дословно")
}

// ЧЕРНОВИК ИДЕИ ПРОВЕРЯЕТ СВОЙ ПОТОЛОК СНИМКА, КОТОРОГО У НЕГО НЕ БЫЛО ВОВСЕ.
//
// Снимок текстового прогона — это доска целиком, и ни у записки доски, ни у выносок своего потолка
// нет. Без этой проверки мегабайтная доска уезжала бы в стор и в платный вызов.
func TestDraftIdeaRefusesAMoodboardOverTheSnapshotCeiling(t *testing.T) {
	card := designGuardCard()
	card.MoodNote = sql.NullString{String: strings.Repeat("m", designMaxInputsBytes+1), Valid: true}

	repo := mocks.NewMockRepository(t)
	cards := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(cards).Once()
	cards.EXPECT().GetTechCardById(mock.Anything, designGuardCardID).Return(card, nil).Once()
	// Ни Design(), ни модель не ожидаются: строгий мок покраснеет, если дверь дойдёт до денег.

	// Ключ есть — значит «не настроено» не может стать причиной отказа; ходить по этому адресу
	// проба не даст, потому что потолок стоит РАНЬШЕ вызова модели.
	srv := &Server{
		repo: repo, designGenerationEnabled: true,
		aiOps: openrouter.New(openrouter.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"}),
	}
	_, err := srv.DraftDesignIdea(designGuardCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId:      designGuardCardID,
		ClientRequestId: "22222222-2222-2222-2222-222222222222",
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "the ceiling is")
}

// ─────────────────────── 6. ПЛИТЫ ПОВОРОТНОГО СТОЛА ───────────────────────

// 3D-ПРОГОН НАЗЫВАЕТ ПЛИТЫ, ИЗ КОТОРЫХ СОБРАН.
//
// ЧТО БЫЛО: `DesignThreedParams.source_picture_ids` объявлено контрактом и не писал его НИКТО.
// Параметры — замороженная история, и пустое поле в ней не молчит, а утверждает, что прогон не
// видел ни одной плиты.
func TestThreedRunStampsThePlatesItWasBuiltFrom(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
	_, err := rig.srv.StartDesignRun(designGuardCtx(), designGuardStart(entity.DesignRunKindThreed))
	require.NoError(t, err)
	require.NotNil(t, rig.sent)

	stored := &pb_common.DesignRunParams{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Params, stored))
	require.Equal(t, []int32{501, 502}, stored.GetThreed().GetSourcePictureIds(),
		"названы обязаны быть id КАРТИНОК рендер-плит, которые отобрал этот прогон")
	require.Contains(t, string(rig.sent.Params), `"source_picture_ids"`,
		"snake_case: SQL-пути стора написаны по нему, а lowerCamelCase молча делает их пустыми")
}

// ШТАМП СЛЕДУЕТ ЗА СУЖЕНИЕМ ПРОГОНА.
//
// Положительный контроль, без которого проба выше зеленела бы на реализации, которая перечисляет
// ВСЕ плиты карточки: `fix_targets` сужает отбор, и провенанс обязан назвать ровно отобранное.
func TestThreedStampFollowsTheFixSelection(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
	req := designGuardStart(entity.DesignRunKindThreed)
	req.Params = &pb_common.DesignRunParams{FixTargets: []string{entity.DesignViewBack}}
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)

	stored := &pb_common.DesignRunParams{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Params, stored))
	require.Equal(t, []int32{502}, stored.GetThreed().GetSourcePictureIds())
}

// ПРОВЕНАНС ПРИНАДЛЕЖИТ СЕРВЕРУ: КЛИЕНТСКОЕ ЗНАЧЕНИЕ ЗАТИРАЕТСЯ.
//
// Пустое поле врало молчанием; непустое, присланное клиентом, врало бы содержанием — и точно так
// же навсегда. У флэта плит нет вовсе, и правдива там только пустота.
func TestSourcePicturesAreServerOwned(t *testing.T) {
	t.Run("threed", func(t *testing.T) {
		rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
		req := designGuardStart(entity.DesignRunKindThreed)
		req.Params = &pb_common.DesignRunParams{
			Threed: &pb_common.DesignThreedParams{SourcePictureIds: []int32{999}},
		}
		_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
		require.NoError(t, err)
		require.NotNil(t, rig.sent)
		stored := &pb_common.DesignRunParams{}
		require.NoError(t, designUnmarshalJSON(rig.sent.Params, stored))
		require.Equal(t, []int32{501, 502}, stored.GetThreed().GetSourcePictureIds(),
			"клиент не вправе назвать плиты, которых прогон не брал")
	})
	t.Run("flat", func(t *testing.T) {
		rig := newDesignGuardRig(t, designGuardCard(), designGuardBand())
		req := designGuardStart(entity.DesignRunKindFlat)
		req.Params = &pb_common.DesignRunParams{
			Threed: &pb_common.DesignThreedParams{SourcePictureIds: []int32{999}},
		}
		_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
		require.NoError(t, err)
		require.NotNil(t, rig.sent)
		stored := &pb_common.DesignRunParams{}
		require.NoError(t, designUnmarshalJSON(rig.sent.Params, stored))
		require.Empty(t, stored.GetThreed().GetSourcePictureIds(),
			"род, который плит не берёт, обязан говорить об этом пустотой")
	})
}

// ─────────────────────── 6. ТКАНИ ПРОГОНА (V-8) ───────────────────────

// designGuardBandWithShelf — та же полоса плюс ДВЕ полки ЭТОЙ карточки. Всё, чего в этом списке
// нет, для карточки чужое.
func designGuardBandWithShelf() *entity.DesignBand {
	band := designGuardBand()
	band.Assets = []entity.DesignAsset{
		{Id: 61, TechCardId: designGuardCardID, Kind: entity.DesignAssetKindFabric, Name: "main jersey"},
		{Id: 62, TechCardId: designGuardCardID, Kind: entity.DesignAssetKindFabric, Name: "contrast rib"},
	}
	return band
}

// designGuardTwoCloths — ДВЕ ткани в рецепте: первая законная и повторённая в скаляре ровно так,
// как велит контракт, вторая — та, которую проба портит.
func designGuardTwoCloths(second *pb_common.DesignFabricUse) *pb_admin.StartDesignRunRequest {
	req := designGuardStart(entity.DesignRunKindRender)
	req.Params = &pb_common.DesignRunParams{
		Colour: &pb_common.DesignColourRecipe{
			Source: "photo", FabricMediaId: 300,
			Fabrics: []*pb_common.DesignFabricUse{
				{AssetId: 61, Name: "main jersey", MediaId: 300, Parts: "body"},
				second,
			},
		},
	}
	return req
}

// ФОТОГРАФИЯ ТКАНИ ЧУЖОЙ КАРТОЧКИ НЕ УХОДИТ В ПЛАТНЫЙ ПРОГОН.
//
// ЧТО БЫЛО. Дверь спрашивала про `extra_input_media_ids` и про легаси-скаляр
// `colour.fabric_media_id` — и НИ РАЗУ про `fabrics[*].media_id`, хотя эту же волну воркер научили
// отправлять фотографию КАЖДОЙ ткани (designgen/snapshot.go). Достаточно было положить чужой номер
// во ВТОРУЮ ткань: скаляр законный, дверь довольна, чужая картинка уезжает поставщику и замерзает
// в истории этой карточки.
func TestRunRefusesAClothPhotoOfAnotherCard(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBandWithShelf())
	const foreignCloth = 779
	rig.foreign[foreignCloth] = true

	req := designGuardTwoCloths(&pb_common.DesignFabricUse{
		AssetId: 62, Name: "contrast rib", MediaId: foreignCloth, Parts: "collar",
	})
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, rig.asked, foreignCloth,
		"про текстуру каждой ткани обязаны спросить: воркер отправляет их все")
	require.Nil(t, rig.sent, "отказ обязан прийти ДО резерва денег")
}

// ТКАНЬ С ЧУЖОЙ ПОЛКИ НЕ ЗАМЕРЗАЕТ В ИСТОРИИ ЭТОЙ КАРТОЧКИ.
//
// `asset_id` — провенанс, и контракт прямо говорит, что читатель его НЕ РАЗРЕШАЕТ: строка истории
// навсегда утверждает «эта ткань пришла с полки 99». Полка 99 принадлежит другой карточке, значит
// утверждение ложно, а починить его нельзя — параметры прогона заморожены.
func TestRunRefusesAClothFromAnotherCardsShelf(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBandWithShelf())
	req := designGuardTwoCloths(&pb_common.DesignFabricUse{
		AssetId: 99, Name: "contrast rib", Parts: "collar",
	})
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "params.colour.fabrics.1.asset_id")
	require.Nil(t, rig.sent)
}

// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: ЗАКОННЫЕ ДВЕ ТКАНИ ПРОХОДЯТ ЦЕЛИКОМ.
//
// Ворота, отказывающие всякому списку тканей, зеленят обе пробы выше и убивают саму волну V-8.
// Здесь обе ткани названы полками ЭТОЙ карточки, текстура второй — свежая загрузка (за ней не
// стоит ещё ни одна карточка, и это по контракту законно), и обе обязаны доехать до стора.
func TestRunAcceptsTwoClothsOfItsOwnShelf(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBandWithShelf())
	req := designGuardTwoCloths(&pb_common.DesignFabricUse{
		AssetId: 62, Name: "contrast rib", MediaId: 301, Parts: "collar",
	})
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent, "законный список тканей обязан доехать до стора")
	require.Contains(t, rig.asked, 301, "про текстуру второй ткани всё равно спрашивают")

	stored := &pb_common.DesignRunParams{}
	require.NoError(t, designUnmarshalJSON(rig.sent.Params, stored))
	require.Len(t, stored.GetColour().GetFabrics(), 2, "обе ткани обязаны замёрзнуть в параметрах")
}

// ТКАНЬ БЕЗ ПОЛКИ — ЗАКОННАЯ ТКАНЬ.
//
// Контракт объявляет это дословно: «asset_id = 0, когда ткань названа без строки полки». Правило,
// требующее полку, закрыло бы обычный жест «просто напиши, из чего это сшито».
func TestRunAcceptsAClothStatedWithoutAShelfRow(t *testing.T) {
	rig := newDesignGuardRig(t, designGuardCard(), designGuardBandWithShelf())
	req := designGuardTwoCloths(&pb_common.DesignFabricUse{
		Name: "contrast rib", Words: "2x2 rib", Parts: "collar",
	})
	_, err := rig.srv.StartDesignRun(designGuardCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
}
