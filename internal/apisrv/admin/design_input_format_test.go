package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ═══ ВХОД ПЛАТНОГО ПРОГОНА ОБЯЗАН БЫТЬ КАРТИНКОЙ — ЖИВОЙ ЗАМЕР У ДВЕРИ ══════════════════════════
//
// Довод целиком — в шапке design_input_format.go. Здесь измеряется ровно то, что нельзя доказать
// чтением: что отказ приходит ДО `Design().StartRun`, то есть до строки и до резерва дня.
//
// ⚠ КАК ИМЕННО ЭТО ИЗМЕРЯЕТСЯ, И ПОЧЕМУ ЭТО СИЛЬНЕЕ «ЕСТЬ ОШИБКА». Стенд отказа СОБРАН БЕЗ
// ЗАГЛУШКИ StartRun: у mockery в строгом режиме любой вызов, на который нет ожидания, роняет пробу
// по имени. Значит проба краснеет не только когда сторож снят, но и когда его ПЕРЕНЕСЛИ НИЖЕ
// резерва — а это ровно тот способ, которым такой сторож обычно и портят. Тот же приём и тот же
// довод, что у J-26 (design_j26_front_test.go).

const (
	// designGLBMediaID — руками загруженная модель: свежая строка media, ничья, .glb на конце.
	// Ровно то, что чеканит UploadContentModel.
	designGLBMediaID = 8100
	// designSVGMediaID — ВЫХОД ПРОГОНА `vector`. Его род кадра — flat (DesignPictureKindOfRun:
	// `default: flat`), то есть он ЗАКОННО встаёт на флэт-слот верстака и уезжает в каждый рендер.
	// На бете это картинки 16, 25 и 66 (прогоны 5, 10, 19) — стенд повторяет форму их адреса.
	designSVGMediaID = 8200
	// designLegacyMediaID — адрес без расширения: то, про что bucket.ObjectMediaType честно
	// говорит «не знаю».
	designLegacyMediaID = 8300

	designGLBURL    = "https://cdn.grbpwr.com/grbpwr-com/design/2026/september/run-20-0-og.glb"
	designSVGURL    = "https://cdn.grbpwr.com/grbpwr-com/design/2026/september/run-19-0-og.svg"
	designPNGURL    = "https://cdn.grbpwr.com/grbpwr-com/design/2026/september/a1b2c3-og.png"
	designWEBPURL   = "https://cdn.grbpwr.com/grbpwr-com/design/2026/september/d4e5f6-og.webp"
	designLegacyURL = "https://cdn.grbpwr.com/legacy/no-extension-here"
)

// designFormatMedia — адреса, которые медиа-стор отдаст на этом стенде. Всё, что проба не назвала
// явно, приезжает обычной картинкой: иначе положительный контроль краснел бы по случайной причине.
func designFormatMedia(override map[int]string) map[int]entity.MediaFull {
	base := map[int]string{
		designRefMediaID:         designPNGURL,
		designPlateMediaID:       designWEBPURL,
		designRenderPlateMediaID: designWEBPURL,
		designBoardMediaID:       designBoardMediaURL,
		designTechnicalMediaID:   designPNGURL,
		designGLBMediaID:         designGLBURL,
		designSVGMediaID:         designSVGURL,
		designLegacyMediaID:      designLegacyURL,
	}
	for id, u := range override {
		base[id] = u
	}
	out := make(map[int]entity.MediaFull, len(base))
	for id, u := range base {
		out[id] = entity.MediaFull{Id: id, MediaItem: entity.MediaItem{FullSizeMediaURL: u}}
	}
	return out
}

// designFormatRig — стенд с медиа-стором. `withStartRun = false` — это и есть измерение «до
// денег»: обращение к StartRun роняет пробу.
func designFormatRig(t *testing.T, band *entity.DesignBand, withStartRun bool, override map[int]string) *designRunRig {
	t.Helper()
	rig := &designRunRig{
		repo:   mocks.NewMockRepository(t),
		cards:  mocks.NewMockTechCards(t),
		design: mocks.NewMockDesign(t),
	}
	media := mocks.NewMockMedia(t)
	rig.repo.EXPECT().TechCards().Return(rig.cards).Maybe()
	rig.repo.EXPECT().Design().Return(rig.design).Maybe()
	rig.repo.EXPECT().Media().Return(media).Maybe()
	designStubNoDisplayOnly(rig.design)
	rig.cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).
		Return(designMoodCard(), nil).Maybe()
	rig.design.EXPECT().GetBand(mock.Anything, designRunCardID, mock.Anything).
		Return(band, nil).Maybe()
	rows := designFormatMedia(override)
	media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).
		Return(rows, nil).Maybe()
	// ГРАНИЦА КАРТОЧКИ — ОТДЕЛЬНЫЙ, БОЛЕЕ РАННИЙ СТОРОЖ, и стенд обязан его пропускать.
	// Эти пробы про ФОРМАТ файла, а не про «чей это номер»: если бы граница отказывала первой,
	// они мерили бы её отказ и зеленели бы при снятой проверке формата. Свежее загруженное медиа
	// ничьё, и настоящий стор его пропускает — заглушка повторяет ровно это.
	rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).
		Return(nil).Maybe()
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

// designStubAnyMedia — МЕДИА-СТОР ДЛЯ РИГОВ, КОТОРЫЕ ПРО ФОРМАТ НЕ ПРОБУЮТ.
//
// ⚠ ЗАЧЕМ ОН ПОНАДОБИЛСЯ И ЧТО ЭТО ГОВОРИТ. С этой волны КАЖДЫЙ старт прогона читает адреса своих
// входов (designRefuseNonPictureInputs), поэтому всякий стенд StartDesignRun/DraftDesignIdea
// обязан иметь медиа-стор — иначе строгий mockery роняет пробу на незаявленном вызове. Это не
// формальность стенда, а цена решения: у денежной двери появился ещё один запрос, и он тут виден.
//
// ⚠ ОТДАЮТСЯ НАСТОЯЩИЕ КАРТИНОЧНЫЕ АДРЕСА, А НЕ ПУСТАЯ КАРТА. Пустая карта означала бы «строки
// медиа нет», и сторож в этих пробах не исполнялся бы вовсе — то есть двадцать восемь стендов
// молча перестали бы проходить через новый код. С настоящими png/webp он исполняется в каждом из
// них, и любой отказ, который он начнёт выдавать по ошибке, покраснеет здесь же.
func designStubAnyMedia(t *testing.T, repo *mocks.MockRepository) {
	t.Helper()
	media := mocks.NewMockMedia(t)
	repo.EXPECT().Media().Return(media).Maybe()
	media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).
		Return(designFormatMedia(nil), nil).Maybe()
}

// ─────────────── ГЛАВНАЯ ПРОБА: ТОТ САМЫЙ ИЗМЕРЕННЫЙ ПУТЬ ───────────────

// TestARenderRunNamingAnUploadedModelIsRefusedBeforeTheReserve — ДОСЛОВНО ТА ЦЕПОЧКА, КОТОРУЮ
// ОТКРЫЛА ДВЕРЬ ЗАГРУЗКИ МОДЕЛЕЙ.
//
// Загрузить .glb → получить свежий НИЧЕЙНЫЙ media id → запустить `render` с
// `extra_input_media_ids: [<этот id>]`. Граница карточки такое медиа пропускает НАМЕРЕННО (ничьё),
// поэтому до этой волны прогон резервировал деньги и отдавал поставщику адрес .glb в слоте
// картинки.
//
// МУТАЦИИ, КОТОРЫЕ ЭТО КРАСНЯТ:
//   - снять вызов `s.designRefuseNonPictureInputs` из StartDesignRun;
//   - перенести его ПОСЛЕ `s.repo.Design().StartRun` — тогда падает неожиданный вызов стора, то
//     есть проба ловит именно «отказ пришёл после денег», а не только «отказа нет»;
//   - вписать "model/gltf-binary" в designVendorReadableMediaTypes;
//   - выбросить `extra_input_media_ids` из designRunInputMediaRefs.
func TestARenderRunNamingAnUploadedModelIsRefusedBeforeTheReserve(t *testing.T) {
	rig := designFormatRig(t, designBandWith(true), false, nil)

	req := designStartRequest(entity.DesignRunKindRender)
	req.Params.ExtraInputMediaIds = []int32{designGLBMediaID}

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.Error(t, err, "адрес .glb в слоте картинки — оплаченный отказ у поставщика")
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code,
		"просьба синтаксически законна — не годится ФАЙЛ; это не InvalidArgument")
	require.Equal(t, "input_not_a_picture", md["reason"])
	require.Equal(t, "8100", md["media_id"], "отказ обязан назвать НОМЕР, иначе его нечем чинить")
	require.Equal(t, "model/gltf-binary", md["content_type"])
	require.Equal(t, "params.extra_input_media_ids", md["where"],
		"…и поле, иначе человек ищет по четырём экранам")
	require.Contains(t, err.Error(), "Nothing was reserved and nothing was charged")
	require.Nil(t, rig.sent, "строки прогона нет, значит и резерв дня не двигался")
}

// TestAModelStandingOnTheBenchIsRefusedToo — ВТОРАЯ ПОЛОВИНА ТОГО ЖЕ ПУТИ, и без неё первая
// закрывала бы одну дверь из двух.
//
// Тот же .glb, зарегистрированный кадром рода `flat` (RegisterDesignUpload формат не гейтит
// намеренно) и поставленный на флэт-слот, уезжает КАЖДОМУ рендеру через designSelectBench — минуя
// `extra_input_media_ids` целиком. Проба стоит именно на плите, поэтому проверка, написанная
// только по параметрам, её не проходит.
func TestAModelStandingOnTheBenchIsRefusedToo(t *testing.T) {
	band := designBandWith(true)
	band.Bench[0].Picture.MediaId = designGLBMediaID

	rig := designFormatRig(t, band, false, nil)
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindRender))

	require.Error(t, err)
	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, "input_not_a_picture", md["reason"])
	require.Equal(t, "the bench plate on "+entity.DesignViewFront, md["where"],
		"человеку надо сказать, ЧТО снять с верстака, а не «какое-то медиа»")
	require.Nil(t, rig.sent)
}

// TestAModelAsTheFabricTextureIsRefusedToo — третий путь: ткань. Скаляр и список — два независимых
// источника (контракт DesignColourRecipe), поэтому проверяются оба.
func TestAModelAsTheFabricTextureIsRefusedToo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*pb_common.DesignRunParams)
		where  string
	}{
		{
			name: "scalar",
			mutate: func(p *pb_common.DesignRunParams) {
				p.Colour = &pb_common.DesignColourRecipe{FabricMediaId: designGLBMediaID}
			},
			where: "params.colour.fabric_media_id",
		},
		{
			name: "second cloth",
			mutate: func(p *pb_common.DesignRunParams) {
				p.Colour = &pb_common.DesignColourRecipe{
					Code: "BLK",
					Fabrics: []*pb_common.DesignFabricUse{
						{Name: "main", MediaId: designRefMediaID},
						{Name: "rib", MediaId: designGLBMediaID},
					},
				}
			},
			where: "params.colour.fabrics.1.media_id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := designFormatRig(t, designBandWith(true), false, nil)
			req := designStartRequest(entity.DesignRunKindRender)
			tc.mutate(req.Params)

			_, err := rig.srv.StartDesignRun(designRunCtx(), req)
			require.Error(t, err)
			_, md := errorReason(t, err)
			require.Equal(t, "input_not_a_picture", md["reason"])
			require.Equal(t, tc.where, md["where"])
			require.Nil(t, rig.sent)
		})
	}
}

// ─────────────── ОТРИЦАТЕЛЬНЫЕ КОНТРОЛИ ───────────────

// TestAnOrdinaryPictureRunStillStarts — БЕЗ ЭТОГО ВСЁ ВЫШЕ ЗЕЛЕНЕЛО БЫ И У СТОРОЖА, КОТОРЫЙ
// ОТКАЗЫВАЕТ ВСЕМУ. Обычный рендер по обычным png/webp обязан дойти до стора.
func TestAnOrdinaryPictureRunStillStarts(t *testing.T) {
	rig := designFormatRig(t, designBandWith(true), true, nil)

	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindRender))
	require.NoError(t, err)
	require.NotNil(t, rig.sent, "сторож формата не имеет права трогать прогон по настоящим картинкам")
}

// TestAVectorOutputOnTheBenchStillStarts — ЗАМЕРЕННОЕ РЕШЕНИЕ ПРО SVG, ЗАПЕРТОЕ ПРОБОЙ.
//
// Прогон `vector` рождает кадр рода **flat**, и его медиа — сам .svg (на бете: картинки 16, 25,
// 66 от прогонов 5, 10, 19). Флэт-слот — ровно то место, куда такой кадр и ставят, а
// designSelectBench отдаёт флэт-плиты КАЖДОМУ рендеру. Правило «не растр — отказ» рубило бы здесь
// законный оплаченный прогон, а сторож, отказывающий законному прогону, хуже дыры.
//
// ⚠ ЭТА ПРОБА КРАСНЕЕТ ОТ «УЖЕСТОЧЕНИЯ», И ЭТО ЕЁ РАБОТА. Тот, кто выкинет "image/svg+xml" из
// designVendorReadableMediaTypes, обязан сначала прочитать, почему он там, и измерить, читает ли
// поставщик .svg по адресу, — отсюда это не проверяется (openrouter.validateImageURL гейтит только
// схему).
func TestAVectorOutputOnTheBenchStillStarts(t *testing.T) {
	band := designBandWith(true)
	band.Bench[0].Picture.MediaId = designSVGMediaID

	rig := designFormatRig(t, band, true, nil)
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindRender))

	require.NoError(t, err, "векторный выход на флэт-верстаке — законный вход рендера")
	require.NotNil(t, rig.sent)
}

// TestAnUnrecognisedAddressStillStarts — ВТОРОЕ НАЗВАННОЕ РЕШЕНИЕ: «не знаю» это не «отказ».
//
// bucket.ObjectMediaType отвечает по расширению, которое сам bucket и пишет. Легаси-строка или
// адрес из другого места опознаны не будут, и превратить это в отказ значило бы рубить оплаченный
// прогон на данных, которых я не видел (прод читать нельзя).
func TestAnUnrecognisedAddressStillStarts(t *testing.T) {
	band := designBandWith(true)
	band.Bench[0].Picture.MediaId = designLegacyMediaID

	rig := designFormatRig(t, band, true, nil)
	_, err := rig.srv.StartDesignRun(designRunCtx(), designStartRequest(entity.DesignRunKindRender))

	require.NoError(t, err)
	require.NotNil(t, rig.sent)
}

// TestADeletedInputIsNotAFormatRefusal — пропавшая строка медиа не должна приходить отказом ПРО
// ФОРМАТ: это другой вопрос, и воркер его уже решает, роняя картинку из списка.
func TestADeletedInputIsNotAFormatRefusal(t *testing.T) {
	rig := designFormatRig(t, designBandWith(true), true, nil)
	req := designStartRequest(entity.DesignRunKindRender)
	req.Params.ExtraInputMediaIds = []int32{999999} // такой строки в медиа-сторе нет

	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
}

// ─────────────── ТЕКСТОВЫЙ ПРОГОН ───────────────

// TestDraftIdeaWithAModelOnTheBoardIsRefusedBeforeTheReserve — вторая денежная дверь, и она
// платит ИНАЧЕ: черновик идеи зовёт модель ПРЯМО В ХЕНДЛЕРЕ. Поэтому здесь измеряются ОБА конца —
// строки прогона нет (стенд без заглушки StartRun) и ПОСТАВЩИКА НЕ ЗВАЛИ (поддельный сервер
// считает обращения). Одного первого было бы мало: отказ, пришедший после вызова модели, оставил
// бы StartRun непозванным и всё равно стоил бы денег.
//
// Доска не приходит с провода — это строки tech_card_media, — но .glb, прицепленный к ней,
// уезжает в тот же слот картинки того же платного вызова.
func TestDraftIdeaWithAModelOnTheBoardIsRefusedBeforeTheReserve(t *testing.T) {
	client, calls := newFakeOpenRouter(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"an idea"}}]}`))
	})

	card := designMoodCard()
	card.Media = append(card.Media, entity.TechCardMediaItem{
		MediaId: designGLBMediaID, Category: entity.TechCardMediaCategoryMoodboard,
	})

	rig := &designRunRig{
		repo:   mocks.NewMockRepository(t),
		cards:  mocks.NewMockTechCards(t),
		design: mocks.NewMockDesign(t),
	}
	media := mocks.NewMockMedia(t)
	rig.repo.EXPECT().TechCards().Return(rig.cards).Maybe()
	rig.repo.EXPECT().Design().Return(rig.design).Maybe()
	rig.repo.EXPECT().Media().Return(media).Maybe()
	designStubNoDisplayOnly(rig.design)
	rig.cards.EXPECT().GetTechCardById(mock.Anything, designRunCardID).Return(card, nil).Maybe()
	media.EXPECT().GetMediaByIds(mock.Anything, mock.Anything).
		Return(designFormatMedia(nil), nil).Maybe()
	rig.srv = &Server{repo: rig.repo, designGenerationEnabled: true, aiOps: client}

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), &pb_admin.DraftDesignIdeaRequest{
		TechCardId:      designRunCardID,
		ClientRequestId: "44444444-4444-4444-4444-444444444444",
	})
	require.Error(t, err)
	_, md := errorReason(t, err)
	require.Equal(t, "input_not_a_picture", md["reason"])
	require.Equal(t, "the moodboard of this card", md["where"])
	require.Equal(t, "8100", md["media_id"])
	require.Nil(t, rig.sent, "строки прогона нет, значит и резерв дня не двигался")
	require.Empty(t, *calls, "и модель не звали: этот вызов платный сам по себе")
}

// ─────────────── РАЗБИЕНИЕ, КОТОРОЕ НЕ УМЕЕТ СТАРЕТЬ ───────────────

// TestEveryStorableTypeIsClassifiedForTheVendor — СВЯЗЬ, А НЕ СПИСОК.
//
// Всякий тип, который bucket объявил ХРАНИМЫМ, обязан быть либо назван читаемым, либо попасть в
// непускаемые. Иначе следующая волна, научившая bucket хранить очередной формат, молча получила бы
// его в слоте картинки платного вызова — а добавляют такой формат ровно тогда, когда о денежной
// двери никто не думает. Множество берётся ИЗ bucket.StorableMediaTypes(), поэтому проба
// сертифицирует поведение, а не свою копию списка.
func TestEveryStorableTypeIsClassifiedForTheVendor(t *testing.T) {
	storable := bucket.StorableMediaTypes()
	require.NotEmpty(t, storable)

	unreadable := designUnreadableStorableTypes()
	require.Equal(t, []string{"model/gltf-binary"}, unreadable,
		"из хранимых типов поставщику не годится ровно модель; SVG пущен НАМЕРЕННО — см. "+
			"designVendorReadableMediaTypes и TestAVectorOutputOnTheBenchStillStarts")

	for _, ct := range storable {
		_, readable := designVendorReadableMediaTypes[ct]
		inUnreadable := false
		for _, u := range unreadable {
			if u == ct {
				inUnreadable = true
			}
		}
		require.Truef(t, readable != inUnreadable,
			"%q обязан быть ровно в одном из двух множеств: разбиение полное и непересекающееся", ct)
	}
}

// ─────────────── СБОРЩИК ───────────────

// TestTheInputCollectorSeesAllFiveSources — все пять источников в одном месте, без повторов.
//
// МУТАЦИЯ: удалить любой из пяти циклов designRunInputMediaRefs. Проверка по КОЛИЧЕСТВУ и по
// именам полей, а не по «непусто»: список из четырёх источников выглядел бы работающим.
func TestTheInputCollectorSeesAllFiveSources(t *testing.T) {
	refs := designRunInputMediaRefs(
		&pb_common.DesignRunParams{
			ExtraInputMediaIds: []int32{31, 31}, // повтор снимается
			Colour: &pb_common.DesignColourRecipe{
				FabricMediaId: 41,
				Fabrics:       []*pb_common.DesignFabricUse{{MediaId: 41}, {MediaId: 51}},
			},
		},
		&pb_common.DesignInputSnapshot{
			Slots: []*pb_common.DesignInputSlot{
				{ViewKey: entity.DesignViewFront, MediaId: 11},
				{ViewKey: entity.DesignViewDetail, DetailName: "cuff", MediaId: 12},
				{ViewKey: entity.DesignViewDetail, DetailName: "collar"}, // просьба «нарисуй», без картинки
			},
			Refs: []*pb_common.DesignInputRef{{MediaId: 21, Role: "silhouette"}},
		},
	)

	got := map[int]string{}
	order := make([]int, 0, len(refs))
	for _, r := range refs {
		got[r.ID] = r.Where
		order = append(order, r.ID)
	}
	require.Equal(t, []int{31, 41, 51, 11, 12, 21}, order,
		"пять источников, повтор 41 снят, пустая именная деталь не картинка; названное ВЫЗЫВАЮЩИМ "+
			"идёт раньше принадлежащего карточке — дедупликация оставляет первый источник, и он же "+
			"попадает в отказ")
	require.Equal(t, "the bench plate on front", got[11])
	require.Equal(t, "the bench plate on the detail «cuff»", got[12])
	require.Equal(t, "the card reference «silhouette»", got[21])
	require.Equal(t, "params.extra_input_media_ids", got[31])
	require.Equal(t, "params.colour.fabric_media_id", got[41],
		"повтор оставляет ПЕРВЫЙ источник — тот, который человек увидит первым на экране")
	require.Equal(t, "params.colour.fabrics.1.media_id", got[51])
}
