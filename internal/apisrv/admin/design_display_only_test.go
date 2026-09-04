package admin

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ═══ D-24: КАДР «ТОЛЬКО ДЛЯ ПОКАЗА» НЕ УЕЗЖАЕТ НИ В ОДИН ПЛАТНЫЙ ВЫЗОВ — ЖИВОЙ ЗАМЕР У ДВЕРИ ═══
//
// Довод целиком — в шапке design_input_format.go (раздел D-24). Здесь измеряется то, чего не
// доказать чтением: что отказ приходит ДО `Design().StartRun`, то есть до строки и до резерва дня,
// и что спрашивают ПО ВСЕМ ПЯТИ источникам входа, а не по одному верстаку. Стенд отказа собран БЕЗ
// заглушки StartRun — тот же приём, что у двери формата: сторож, перенесённый ниже резерва,
// роняет пробу на незаявленном вызове по имени.
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСНИТ (по числу исходов):
//   - снять вызов `s.designRefuseDisplayOnlyInputs` из StartDesignRun — первые три пробы;
//   - перенести его ПОСЛЕ `s.repo.Design().StartRun` — те же три, уже на незаявленном StartRun;
//   - спрашивать только плиты верстака (фильтр в designSelectBench вместо двери) — первая и третья;
//   - снять вызов из DraftDesignIdea — проба доски.

const designDisplayOnlyMediaID = 8400

// displayOnlyRig — стенд, у которого стор полосы отвечает, ЧТО из названных медиа помечено
// «только для показа», и записывает, о чём его спросили.
type displayOnlyRig struct {
	*designRunRig
	asked []int
}

func newDisplayOnlyRig(t *testing.T, band *entity.DesignBand, held []int, withStartRun bool, override map[int]string) *displayOnlyRig {
	t.Helper()
	rig := &displayOnlyRig{designRunRig: &designRunRig{
		repo:   mocks.NewMockRepository(t),
		cards:  mocks.NewMockTechCards(t),
		design: mocks.NewMockDesign(t),
	}}
	media := mocks.NewMockMedia(t)
	rig.repo.EXPECT().TechCards().Return(rig.cards).Maybe()
	rig.repo.EXPECT().Design().Return(rig.design).Maybe()
	rig.repo.EXPECT().Media().Return(media).Maybe()
	rig.cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).
		Return(designMoodCard(), nil).Maybe()
	rig.design.EXPECT().GetBand(mock.Anything, designRunCardID, mock.Anything).
		Return(band, nil).Maybe()
	// Всё, что не названо явно, — обычная картинка: дверь формата обязана ПРОПУСКАТЬ, иначе эти
	// пробы мерили бы её отказ.
	media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).
		Return(designFormatMedia(override), nil).Maybe()
	rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).
		Return(nil).Maybe()
	rig.design.EXPECT().MediaHeldDisplayOnly(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, ids []int) ([]int, error) {
			rig.asked = append(rig.asked, ids...)
			return held, nil
		}).Maybe()
	if withStartRun {
		rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
			Run(func(_ context.Context, req entity.DesignRunStart) {
				cp := req
				rig.sent = &cp
			}).
			Return(&entity.DesignRunStarted{
				Run:    entity.DesignRun{Id: 900, TechCardId: designRunCardID, Status: entity.DesignRunPending},
				Budget: entity.DesignBudget{Day: "2026-09-04"},
			}, nil).Maybe()
	}
	rig.srv = &Server{repo: rig.repo, designGenerationEnabled: true}
	return rig
}

// ─────────────── ГЛАВНАЯ ПРОБА: НОМЕР С ПРОВОДА ───────────────

// TestARunNamingADisplayOnlyPictureIsRefusedBeforeTheReserve — путь, который минует верстак
// целиком: медиа кадра «только для показа» названо в `extra_input_media_ids`. Фильтр в отборе плит
// его бы не увидел; дверь — видит.
func TestARunNamingADisplayOnlyPictureIsRefusedBeforeTheReserve(t *testing.T) {
	rig := newDisplayOnlyRig(t, designBandWith(true), []int{designDisplayOnlyMediaID}, false,
		map[int]string{designDisplayOnlyMediaID: designPNGURL})

	req := designStartRequest(entity.DesignRunKindRender)
	req.Params.ExtraInputMediaIds = []int32{designDisplayOnlyMediaID}

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err, "кадр «только для показа» в слоте картинки платного вызова")
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code,
		"просьба синтаксически законна — не годится состояние кадра; это не InvalidArgument")
	require.Equal(t, entity.DesignErrorCodeDisplayOnlyInput, md["reason"])
	require.Equal(t, "8400", md["media_id"], "отказ обязан назвать НОМЕР, иначе его нечем чинить")
	require.Equal(t, "params.extra_input_media_ids", md["where"],
		"…и поле, иначе человек ищет по четырём экранам")
	require.Contains(t, err.Error(), "Nothing was reserved and nothing was charged")
	require.Nil(t, rig.sent, "строки прогона нет, значит и резерв дня не двигался")
	require.Contains(t, rig.asked, designDisplayOnlyMediaID, "дверь спросила у стора именно про этот номер")
}

// TestADisplayOnlyPlateOnTheBenchIsRefusedToo — ВТОРАЯ ПОЛОВИНА: стор отказывает такому кадру в
// слоте (bench.go), но снимок рерана заморожен, а стенд ниже изображает ровно строку, которую
// стор мог записать до 0361 либо после ослабления той двери. Деньги держит ЭТА дверь.
func TestADisplayOnlyPlateOnTheBenchIsRefusedToo(t *testing.T) {
	rig := newDisplayOnlyRig(t, designBandWith(true), []int{designPlateMediaID}, false, nil)
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindRender))

	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, entity.DesignErrorCodeDisplayOnlyInput, md["reason"])
	require.Equal(t, "the bench plate on "+entity.DesignViewFront, md["where"],
		"человеку надо сказать, ЧТО снять с верстака, а не «какое-то медиа»")
	require.Nil(t, rig.sent)
}

// TestADisplayOnlyReferenceIsRefusedToo — третий путь: роль референса. Стор отказывает ей тоже
// (reference.go), и по тому же доводу, что выше, деньги всё равно держит дверь прогона.
func TestADisplayOnlyReferenceIsRefusedToo(t *testing.T) {
	rig := newDisplayOnlyRig(t, designBandWith(true), []int{designRefMediaID}, false, nil)
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindRender))

	require.Error(t, err)
	_, md := errorReason(t, err)
	require.Equal(t, entity.DesignErrorCodeDisplayOnlyInput, md["reason"])
	require.Equal(t, "the card reference «"+entity.DesignViewFront+"»", md["where"])
	require.Nil(t, rig.sent)
}

// TestTheDisplayOnlyDoorAsksAboutEveryInputSource — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ И ПОЛНОТА ВОПРОСА
// СРАЗУ. Ничего не помечено — прогон стартует; а спросили при этом про ВСЕ пять источников:
// дополнительный вход, скаляр ткани, список тканей, плиту верстака и референс карточки. Дверь,
// спрашивающая только верстак, зеленела бы на трёх пробах выше и краснела бы здесь.
func TestTheDisplayOnlyDoorAsksAboutEveryInputSource(t *testing.T) {
	const extra, clothA, clothB = 8400, 8600, 8700
	rig := newDisplayOnlyRig(t, designBandWith(true), nil, true,
		map[int]string{extra: designPNGURL, clothA: designPNGURL, clothB: designPNGURL})

	req := designStartRequest(entity.DesignRunKindRender)
	req.Params.ExtraInputMediaIds = []int32{extra}
	req.Params.Colour = &pb_common.DesignColourRecipe{
		FabricMediaId: clothA,
		Fabrics: []*pb_common.DesignFabricUse{
			{Name: "main cloth", Words: "washed cotton", Parts: "body", MediaId: clothA},
			{Name: "contrast rib", Words: "2x2 rib", Parts: "collar", MediaId: clothB},
		},
	}

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err, "ничего не помечено — дверь обязана пропустить")
	require.NotNil(t, rig.sent)
	for _, id := range []int{extra, clothA, clothB, designPlateMediaID, designRefMediaID} {
		require.Contains(t, rig.asked, id, "источник %d не был спрошен: тот путь остался бы открытым", id)
	}
}

// ─────────────── ТЕКСТОВЫЙ ПРОГОН ───────────────

// TestADraftWithADisplayOnlyBoardPictureIsRefusedBeforeTheModelIsCalled — доска не проходит через
// полосу, но медиа кадра «только для показа» человек может положить на неё руками; тогда оно
// уехало бы в платный вызов, минуя все три двери стора. Измеряются ОБА конца: строки прогона нет и
// ПОСТАВЩИКА НЕ ЗВАЛИ.
func TestADraftWithADisplayOnlyBoardPictureIsRefusedBeforeTheModelIsCalled(t *testing.T) {
	client, calls := newFakeOpenRouter(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"an idea"}}]}`))
	})

	rig := newDisplayOnlyRig(t, designBandWith(true), []int{designBoardMediaID}, false, nil)
	rig.srv.aiOps = client

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "55555555-5555-5555-5555-555555555555",
	})
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, entity.DesignErrorCodeDisplayOnlyInput, md["reason"])
	require.Equal(t, "the moodboard of this card", md["where"])
	require.Equal(t, "900", md["media_id"])
	require.Nil(t, rig.sent, "строки прогона нет, значит и резерв дня не двигался")
	require.Empty(t, *calls, "и модель не звали: этот вызов платный сам по себе")
}

// ─────────────── ЗАГРУЗКА: ФЛАГ И МУЛЬТИВЬЮ ДОЕЗЖАЮТ ДО СТОРА ───────────────

// TestRegisterDesignUploadCarriesDisplayOnlyAndCompositeViewsToTheStore — оба новых утверждения
// загружающего (D-24, D-26) уезжают в стор, а не на пол. МУТАЦИЯ: убрать любое из двух полей из
// сборки entity.DesignUploadItem — флаг молча читается как «обычный кадр», а мультивью — как
// одиночная плита, которая встаёт в слот.
func TestRegisterDesignUploadCarriesDisplayOnlyAndCompositeViewsToTheStore(t *testing.T) {
	rig := newDesignUploadRig(t)
	_, err := rig.srv.RegisterDesignUpload(designRunCtx(), &pb_admin.RegisterDesignUploadRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "66666666-6666-6666-6666-666666666666",
		Items: []*pb_admin.DesignUploadItem{
			{MediaId: 601, Kind: entity.DesignPictureKindRender, DisplayOnly: true},
			{MediaId: 602, Kind: entity.DesignPictureKindThreed,
				CompositeViews: []string{entity.DesignViewFront, entity.DesignViewThreeQuarterL, entity.DesignViewBack}},
			{MediaId: 603},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Len(t, rig.sent.Items, 3)
	require.True(t, rig.sent.Items[0].DisplayOnly, "«только для показа» — утверждение загружающего")
	require.Empty(t, rig.sent.Items[0].CompositeViews)
	require.Equal(t, []string{entity.DesignViewFront, entity.DesignViewThreeQuarterL, entity.DesignViewBack},
		rig.sent.Items[1].CompositeViews, "мультивью едет в порядке листа слева направо — разрез читает его позиционно")
	require.False(t, rig.sent.Items[1].DisplayOnly)
	require.False(t, rig.sent.Items[2].DisplayOnly, "неназванное остаётся обычным кадром")
	require.Empty(t, rig.sent.Items[2].CompositeViews)
}

// TestRegisterDesignUploadRefusesAMalformedComposite — три формы, в которых объявленное мультивью
// противоречит себе, и каждая называет ЭЛЕМЕНТ пачки: один вид (это ghost_view), вместе с
// ghost_view (у композита нет одного вида), неизвестный вид. Стор проверяет то же
// (checkUploadCompositeViews) — здесь измеряется, что до стора такое не доезжает.
func TestRegisterDesignUploadRefusesAMalformedComposite(t *testing.T) {
	for name, item := range map[string]*pb_admin.DesignUploadItem{
		"one view": {MediaId: 601, CompositeViews: []string{entity.DesignViewFront}},
		"with a ghost view": {MediaId: 601, GhostView: entity.DesignViewFront,
			CompositeViews: []string{entity.DesignViewFront, entity.DesignViewBack}},
		"an unknown view": {MediaId: 601, CompositeViews: []string{entity.DesignViewFront, "isometric"}},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newDesignUploadRig(t)
			_, err := rig.srv.RegisterDesignUpload(designRunCtx(), &pb_admin.RegisterDesignUploadRequest{
				TechCardId:      designRunCardID,
				ClientRequestId: "77777777-7777-7777-7777-777777777777",
				Items:           []*pb_admin.DesignUploadItem{{MediaId: 600}, item},
			})
			require.Error(t, err)
			code, _ := errorReason(t, err)
			require.Equal(t, codes.InvalidArgument, code)
			require.Contains(t, err.Error(), "items.1.composite_views", "отказ называет элемент пачки")
			require.Nil(t, rig.sent, "противоречие не доезжает до стора")
		})
	}
}

// ─────────────── ПЕРЕВОД СЕНТИНЕЛА СТОРА ───────────────

// TestDisplayOnlyIsAPreconditionOnTheWire — три двери стора (слот, роль, разрез «для промпта»)
// поднимают один сентинел; таблица переводов обязана знать его, иначе он уедет на провод как
// Internal с ERROR в логе. Проверяется через живой хендлер, а не через designError напрямую.
func TestDisplayOnlyIsAPreconditionOnTheWire(t *testing.T) {
	design := mocks.NewMockDesign(t)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Design().Return(design).Maybe()
	design.EXPECT().SetBenchSlot(mock.Anything, mock.AnythingOfType("entity.DesignBenchSlotSet")).
		Return(nil, fmt.Errorf("%w: picture 77 is display-only and does not stand on the bench",
			entity.ErrDesignDisplayOnly)).Once()
	srv := &Server{repo: repo}

	_, err := srv.SetDesignBenchSlot(designRunCtx(), &pb_admin.SetDesignBenchSlotRequest{
		TechCardId: designRunCardID,
		Slot:       &pb_admin.DesignBenchSlotRef{Slot: &pb_admin.DesignBenchSlotRef_ViewKey{ViewKey: entity.DesignViewFront}},
		PictureId:  77,
	})
	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code, "состояние кадра, а не поломка сервера")
	require.Equal(t, "display_only", md["reason"], "машинный токен, по которому ветвится клиент")
}

// TestDisplayOnlyRidesTheWireOnThePicture — флаг читается обратно: без него клиент не смог бы
// ни нарисовать пометку, ни объяснить отказ.
func TestDisplayOnlyRidesTheWireOnThePicture(t *testing.T) {
	out := designPictureToPb(entity.DesignPicture{Id: 5, TechCardId: designRunCardID, DisplayOnly: true})
	require.True(t, out.GetDisplayOnly())
	require.False(t, designPictureToPb(entity.DesignPicture{Id: 6, TechCardId: designRunCardID}).GetDisplayOnly())
}
