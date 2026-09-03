package admin

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ═══ КРУГ 15, J-6: СНИМОК ПАТТЕРНА БОЛЬШЕ НЕ УТВЕРЖДАЕТ, ЧТО ВИДЕЛ КАРТОЧКУ ═════════════════════
//
// Владелец: «почему у нас в паттерн генерацию отправляются наши INPUT — REFERENCES хотя они
// никакой связи не должны иметь по крайне мере они есть в card's references».
//
// ТРИ ЧАСТИ, И ЗДЕСЬ ЖИВУТ ДВЕ ИЗ НИХ. (а) Снимок морозил КАЖДУЮ строку design_reference как вход
// прогона паттерна — эта проба; (в) промпт нёс `garment:` и `fit:` для ЛЮБОГО рода — вторая
// половина этой пробы, дочитанная в designgen (TestAPatternPromptCARRIES_NO_GARMENT_AND_NO_FIT).
// (б) панель прогона рисовала снимок нефильтрованно — это клиент, и сервер починить её не может:
// снимки прошлых прогонов заморожены.
//
// ⚠ ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ НЕ УКРАШЕНИЕ. «У паттерна нет ссылок карточки» зеленеет и на сборке,
// которая не собирает НИЧЕГО. Поэтому в таблице стоит `flat` с теми же входами, и он обязан нести
// всё: ссылку карточки, названный вход, описание изделия и посадку.
func TestASnapshotCarriesTheCardsReferencesONLY_FOR_THE_KINDS_THAT_READ_THE_CARD(t *testing.T) {
	card := designMoodCard()
	card.GarmentDescription.String, card.GarmentDescription.Valid = "GARMENT-olive shirt", true
	band := designBandWith(false)
	// Три ссылки карточки, чтобы «сузилось до одной» нельзя было спутать с «сузилось случайно».
	band.References = append(band.References,
		entity.DesignReference{TechCardId: designRunCardID, MediaId: 101, Role: entity.DesignViewBack, Ordinal: 2},
		entity.DesignReference{TechCardId: designRunCardID, MediaId: 102, Ordinal: 3},
	)

	for _, tc := range []struct {
		kind        string
		wantRefs    []int32
		wantGarment bool
	}{
		{entity.DesignRunKindPattern, []int32{designExtraMediaID}, false},
		// ⚠ ПЕРЕКРАС ТЕРЯЕТ ССЫЛКИ, НО НЕ ОПИСАНИЕ ИЗДЕЛИЯ, И ЭТО РАЗНЫЕ ВОПРОСЫ. Перекрашивают
		// ФОТОГРАФИЮ ИЗДЕЛИЯ: «olive shirt, spread collar» описывает ровно то, что на снимке.
		// Владелец про перекрас не говорил ничего, и снять у него слова значило бы починить одну
		// жалобу и завести вторую.
		{entity.DesignRunKindRecolor, []int32{designExtraMediaID}, true},
		// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: род, который карточку читает, читает её целиком.
		{entity.DesignRunKindFlat, []int32{designRefMediaID, 101, 102, designExtraMediaID}, true},
		{entity.DesignRunKindRender, []int32{designRefMediaID, 101, 102, designExtraMediaID}, true},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			snap, err := designAssembleInputs(designInputSources{
				Kind: tc.kind, Card: card, Refs: band.References, Bench: band.Bench,
				Params: &pb_common.DesignRunParams{
					Layout:             designLayoutOne,
					ExtraInputMediaIds: []int32{designExtraMediaID},
				},
			})
			require.NoError(t, err)

			got := make([]int32, 0, len(snap.GetRefs()))
			for _, r := range snap.GetRefs() {
				got = append(got, r.GetMediaId())
			}
			require.Equal(t, tc.wantRefs, got,
				"снимок обязан называть входом ровно то, что уедет модели")

			if tc.wantGarment {
				require.Equal(t, "GARMENT-olive shirt", snap.GetGarmentNote())
				require.Equal(t, "oversized", snap.GetFit())
			} else {
				require.Empty(t, snap.GetGarmentNote(),
					"описание изделия в прогоне, который делает КУСОК ТКАНИ, — это деньги: "+
						"composePrompt пишет пустое значение как отсутствующий блок")
				require.Empty(t, snap.GetFit())
			}
		})
	}
}

// ДВА ЧИТАТЕЛЯ ОДНОГО ПРАВИЛА ОБЯЗАНЫ ЧИТАТЬ ЕГО ОДИНАКОВО, И РАНЬШЕ ЧИТАЛ ТОЛЬКО ОДИН.
//
// designSelectBench знал правило с самого начала (свой switch по роду), цикл по референсам в
// designAssembleInputs — никогда. Здесь оба спрашиваются об одном и том же роде и обязаны
// ответить одно и то же; расхождение и было J-6(а).
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть в designKindReadsTheCard `return true` — покраснеют обе
// половины сразу.
func TestBOTH_HALVES_OF_THE_SNAPSHOT_ASK_THE_SAME_PREDICATE(t *testing.T) {
	card := designMoodCard()
	band := designBandWith(true)
	for _, kind := range []string{
		entity.DesignRunKindFlat, entity.DesignRunKindRender, entity.DesignRunKindThreed,
		entity.DesignRunKindVector, entity.DesignRunKindRecolor, entity.DesignRunKindPattern,
	} {
		src := designInputSources{
			Kind: kind, Card: card, Refs: band.References, Bench: band.Bench,
			Params: &pb_common.DesignRunParams{Layout: designLayoutOne, UseFlatSlots: true},
		}
		snap, err := designAssembleInputs(src)
		require.NoError(t, err)
		slots, _ := designSelectBench(src)

		if designKindReadsTheCard(kind) {
			require.NotEmptyf(t, snap.GetRefs(), "kind %s reads the card: its references must be in", kind)
			continue
		}
		require.Emptyf(t, snap.GetRefs(), "kind %s names its own pictures: the card's are not its inputs", kind)
		require.Emptyf(t, slots, "kind %s takes nothing from the bench either", kind)
	}
}

// ═══ КРУГ 15, J-12: ВОРОТА ПОЛКИ И ИСТОЧНИКА — ДО ДЕНЕГ ════════════════════════════════════════

// ПОЛНАЯ ПОЛКА ОТКАЗЫВАЕТ ПРОГОНУ ПАТТЕРНА, И ОТКАЗЫВАЕТ БЕСПЛАТНО.
//
// ⚠ ЭТО ДЕНЬГИ, А НЕ АККУРАТНОСТЬ. Прогон паттерна ПОКУПАЕТ плитку и тут же сажает её на полку.
// Полная полка означает, что посадка не состоится, — и узнать об этом ПОСЛЕ списания стоит ровно
// одну оплаченную картинку, которую некуда положить.
//
// ⚠ И ЭТО НЕ ЗАКРЫВАЕТ ОСТАЛЬНЫЕ РОДЫ. Рендер на карточке с сорока тканями — обычное дело, и
// правило, протёкшее на него, закрыло бы главный маршрут полосы. Отсюда вторая половина таблицы.
func TestAFullShelfREFUSES_A_PATTERN_RUN_AND_NOTHING_ELSE(t *testing.T) {
	full := designBandWith(true)
	for i := 0; i < entity.MaxDesignAssetsPerCard; i++ {
		full.Assets = append(full.Assets, entity.DesignAsset{
			Id: 1000 + i, TechCardId: designRunCardID, Kind: entity.DesignAssetKindFabric,
			Name: "cloth " + strconv.Itoa(i),
		})
	}

	for _, tc := range []struct {
		name    string
		kind    string
		params  *pb_common.DesignRunParams
		refused bool
	}{
		{"pattern on a full shelf", entity.DesignRunKindPattern, &pb_common.DesignRunParams{
			ExtraInputMediaIds: []int32{designRefMediaID},
			Pattern:            &pb_common.DesignPatternParams{Name: "chevron"},
		}, true},
		{"render on a full shelf", entity.DesignRunKindRender, &pb_common.DesignRunParams{
			Views: []string{entity.DesignViewFront}, Layout: designLayoutOne,
		}, false},
		{"flat on a full shelf", entity.DesignRunKindFlat, &pb_common.DesignRunParams{
			Views: []string{entity.DesignViewFront}, Layout: designLayoutOne,
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDesignRunRig(t, designMoodCard(), full)
			rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, mock.Anything, mock.Anything).
				Return(nil).Maybe()
			req := designStartRequest(tc.kind)
			req.Params = tc.params
			_, err := rig.srv.StartDesignRun(designRunCtx(), req)
			if !tc.refused {
				require.NoError(t, err)
				require.NotNil(t, rig.sent, "положительный контроль: прогон дошёл до стора")
				return
			}
			require.Error(t, err)
			code, md := errorReason(t, err)
			require.Equal(t, codes.FailedPrecondition, code)
			require.Equal(t, entity.DesignErrorCodeLibraryFull, md["reason"],
				"клиент читает МАШИННУЮ причину, а не английскую прозу")
			require.Equal(t, strconv.Itoa(entity.MaxDesignAssetsPerCard), md["ceiling"])
			require.Nil(t, rig.sent,
				"отказ обязан стоять ДО резерва: стор не должен был увидеть ни одного запроса")
		})
	}
}

// ИСТОЧНИК ПЛИТКИ — ПОЛКА ЭТОЙ КАРТОЧКИ, И ПРОВЕРЯЕТСЯ ОН У ГОВОРЯЩЕГО.
//
// FK у design_asset.derived_from_asset_id говорит «какая-то строка design_asset», а не «одна из
// ЭТОЙ карточки»: без этой двери паттерн одного стиля повис бы на ткани другого, и схема приняла
// бы это молча.
func TestAPatternsSourceMustBeASHELF_ROW_OF_THIS_CARD(t *testing.T) {
	band := designBandWith(true)
	band.Assets = []entity.DesignAsset{
		{Id: 51, TechCardId: designRunCardID, Kind: entity.DesignAssetKindFabric, Name: "jersey"},
		{Id: 52, TechCardId: designRunCardID + 1, Kind: entity.DesignAssetKindFabric, Name: "someone else's"},
	}
	for _, tc := range []struct {
		name    string
		src     int32
		refused bool
	}{
		{"no source at all — a library file or a paste", 0, false},
		{"a cloth of this card", 51, false},
		{"a cloth of another card", 52, true},
		{"an id that is on no shelf", 999, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := designRefuseForeignPatternSource(designRunCardID, &pb_common.DesignRunParams{
				Pattern: &pb_common.DesignPatternParams{Name: "chevron", SourceAssetId: tc.src},
			}, band.Assets)
			if !tc.refused {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, err.Error(), "params.pattern.source_asset_id")
		})
	}
}

// ПРОГОН ПАТТЕРНА С КОЛОРВЕЕМ ДОЕЗЖАЕТ ДО СТОРА, А НЕ ОТКАЗЫВАЕТСЯ `colorway_forbidden`.
//
// До круга 15 род `pattern` стоял в списке «оси нет», и колорвей у него отказывался. Владелец
// решил обратное: «выбираем ему название и колорвей и все». Проба меряет ПРОВОД — то, что уехало в
// стор, — а не только словарь: словарь проверен в entity, здесь проверено, что дверь его читает.
func TestAPatternRunCARRIES_ITS_COLOURWAY_TO_THE_STORE(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Maybe()
	req := designStartRequest(entity.DesignRunKindPattern)
	req.Params = &pb_common.DesignRunParams{
		ExtraInputMediaIds: []int32{designRefMediaID},
		ColorwayId:         7,
		Pattern:            &pb_common.DesignPatternParams{Name: "chevron", SourceAssetId: 0},
	}
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Equal(t, 7, rig.sent.ColorwayId,
		"колорвей прогона паттерна — адрес полки, на которую сядет плитка")

	// И ИМЯ ЗАМЕРЗАЕТ В params, потому что оттуда его читает посадка (store: keepPatternTx).
	require.Contains(t, string(rig.sent.Params), `"name":"chevron"`)
	require.NotContains(t, strings.ToLower(string(rig.sent.Params)), "colorway_forbidden")
}

// ═══ D2: РЕРАН ПАТТЕРНА, ПРИСЛАВШИЙ ПАРАМЕТРЫ, НЕ ВОЗВРАЩАЕТ ОПИСАНИЕ ИЗДЕЛИЯ ══════════════════
//
// ⚠ РЕРАН НЕ ХОДИТ ЧЕРЕЗ designAssembleInputs ВООБЩЕ. Он копирует снимок РОДИТЕЛЯ целиком
// (designRunInputs) и патчит из своих параметров только `views`, `layout` и просимые детали.
// Значит предикаты J-6 на этом маршруте не стояли: реран прогона, замороженного ДО круга 15,
// приносил в НОВЫЙ платный промпт и описание изделия родителя, и все ссылки карточки.
//
// ⚠ И ЭТО НЕ «ПЕРЕПИСЫВАНИЕ ИСТОРИИ». Строка родителя не трогается ни байтом; речь о снимке
// РЕБЁНКА — новой, ещё не существующей записи о новом платном прогоне. Ровно тот же довод, по
// которому этот маршрут уже патчит `views` и `layout` из параметров ребёнка: снимок, не сходящийся
// с собственной строкой, врёт.
func TestAPatternRERUN_DOES_NOT_INHERIT_THE_GARMENT_NOTE(t *testing.T) {
	// Родитель — прогон, замороженный ДО круга 15: снимок несёт слова карточки и три её ссылки.
	parent := &entity.DesignRun{
		Id: 700, TechCardId: designRunCardID, Kind: entity.DesignRunKindPattern,
		Params: entity.RawJSON(`{"extra_input_media_ids":[90],"pattern":{"repeat_mm":120}}`),
		Inputs: entity.RawJSON(`{"garment_note":"GARMENT-olive shirt","fit":"FIT-oversized",` +
			`"refs":[{"media_id":100,"role":"front"},{"media_id":101},{"media_id":90}]}`),
	}
	srv := &Server{}
	for _, tc := range []struct {
		name  string
		kind  string
		empty bool
	}{
		{"pattern rerun with new params", entity.DesignRunKindPattern, true},
		{"recolour rerun keeps the garment: it recolours a photograph OF it", entity.DesignRunKindRecolor, false},
		{"positive control — a flat rerun replays everything", entity.DesignRunKindFlat, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := *parent
			p.Kind = tc.kind
			// Клиент прислал СВОИ параметры — ровно тот путь, которым обходится ворота имени.
			snap, fit, err := srv.designRunInputs(designRunCtx(), designInputSources{
				Kind: tc.kind, Card: designMoodCard(), Refs: nil, Bench: nil,
				Params: &pb_common.DesignRunParams{
					Layout:             designLayoutOne,
					ExtraInputMediaIds: []int32{90},
					Pattern:            &pb_common.DesignPatternParams{Name: "chevron"},
				},
			}, &p)
			require.NoError(t, err)

			if tc.empty {
				require.Empty(t, snap.GetGarmentNote(),
					"новый платный промпт паттерна не смеет нести описание изделия — ни своё, ни родительское")
				require.Empty(t, snap.GetFit())
				require.Empty(t, fit, "fit_at_launch говорит о том же, что промпт")
				ids := make([]int32, 0, len(snap.GetRefs()))
				for _, r := range snap.GetRefs() {
					ids = append(ids, r.GetMediaId())
				}
				require.Equal(t, []int32{90}, ids,
					"снимок ребёнка называет входом ровно то, что уедет модели")
				return
			}
			require.Equal(t, "GARMENT-olive shirt", snap.GetGarmentNote())
			require.Equal(t, "FIT-oversized", snap.GetFit())
		})
	}
}

// ═══ ТКАНЬ ПЕРЕКРАСА: ЧЕТЫРЕ ФОРМЫ, КОТОРЫЕ ДВЕРЬ ОСТАНАВЛИВАЕТ ДО РЕЗЕРВА ═════════════════════
//
// ⚠ ВСЕ ЧЕТЫРЕ — ДЕНЬГИ, И ВСЕ ЧЕТЫРЕ СОЗДАНЫ ЭТОЙ ЖЕ ВОЛНОЙ. До J-31 вызов перекраса нёс РОВНО
// ОДНУ ссылку, поэтому ни «две ткани в вызове», ни «одна картинка дважды», ни «перебор потолка
// поставщика» не были выразимы вовсе. Дверь заводится вместе с режимом, а не после него.
//
// ⚠ ПРОВЕРЯЮТСЯ ДЕЙСТВУЮЩИЕ ПАРАМЕТРЫ, А НЕ СООБЩЕНИЕ КЛИЕНТА — то есть реран замороженной строки
// с такой формой останавливается здесь же, до резерва, а не умирает потом отказом поставщика.
func TestARecolourCLOTH_SHAPES_THE_DOOR_REFUSES_FOR_FREE(t *testing.T) {
	cloth := func(media int32) *pb_common.DesignFabricUse {
		return &pb_common.DesignFabricUse{MediaId: media, Name: "c", Kind: "pattern"}
	}
	many := make([]*pb_common.DesignFabricUse, 0, 16)
	for i := 0; i < 16; i++ {
		many = append(many, cloth(int32(200+i)))
	}

	for _, tc := range []struct {
		name    string
		params  *pb_common.DesignRunParams
		reason  string
		mustSay string
	}{
		{
			name: "B1 — two cloths with pictures ride into one call and only one is explained",
			params: &pb_common.DesignRunParams{
				ExtraInputMediaIds: []int32{11},
				Colour:             &pb_common.DesignColourRecipe{Fabrics: []*pb_common.DesignFabricUse{cloth(9), cloth(10)}},
			},
			reason:  "one_cloth_only",
			mustSay: "image 2",
		},
		{
			name: "B3 — the cloth is also one of the photographs, so the call carries it twice",
			params: &pb_common.DesignRunParams{
				ExtraInputMediaIds: []int32{11, 9},
				Colour:             &pb_common.DesignColourRecipe{Fabrics: []*pb_common.DesignFabricUse{cloth(9)}},
			},
			reason:  "cloth_is_also_a_photograph",
			mustSay: "twice",
		},
		{
			name: "B2 — the call would not fit the provider's own ceiling",
			params: &pb_common.DesignRunParams{
				ExtraInputMediaIds: []int32{11},
				Colour:             &pb_common.DesignColourRecipe{Fabrics: many},
			},
			// ⚠ B1 СТОИТ ВЫШЕ И ЛОВИТ ЭТОТ ЖЕ ВХОД ПЕРВЫМ, и это правильный порядок: «оставь одну
			// ткань» — точнее и исполнимее, чем «их слишком много для вызова». Проба поэтому
			// требует ОТКАЗА и называет обе законные причины ниже, отдельным утверждением.
			reason:  "one_cloth_only",
			mustSay: "nothing was charged",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := designRefuseUnworkableSources(entity.DesignRunKindRecolor, tc.params)
			require.Error(t, err)
			code, md := errorReason(t, err)
			require.Equal(t, codes.InvalidArgument, code)
			require.Equal(t, tc.reason, md["reason"])
			require.Contains(t, strings.ToLower(err.Error()), tc.mustSay)
			require.Contains(t, strings.ToLower(err.Error()), "nothing was reserved")
		})
	}

	// ПОТОЛОК ВЫЗОВА ЖИВЁТ ОТДЕЛЬНО ОТ B1 И ПРОВЕРЯЕТСЯ ОТДЕЛЬНО: B1 делает его почти
	// недостижимым, но потолок ОБЩИЙ с маршрутом рендера и может поехать. Здесь он спрашивается у
	// своей функции напрямую, с числом, взятым у поставщика, а не переписанным сюда.
	//
	// ⚠ СПРАШИВАЕТСЯ У СВОЕЙ ФУНКЦИИ, А НЕ ЧЕРЕЗ ДВЕРЬ, И ЭТО НЕ УДОБСТВО. Через дверь этот вход
	// перехватывает B1 сообщением, в котором ЧИСЛО ТКАНЕЙ тоже написано, — то есть проба «отказ
	// содержит 16» зеленела бы на чужом отказе и не сторожила бы ничего. Граница меряется на своей
	// функции, ровно на переходе.
	require.NoError(t, designRefuseOversizedRecolourCall(orimages.MaxInputReferences-1),
		"фотография плюс 15 тканей — ровно потолок, и он законен")
	err := designRefuseOversizedRecolourCall(orimages.MaxInputReferences)
	require.Error(t, err, "фотография плюс 16 тканей — на одну больше потолка")
	code, md := errorReason(t, err)
	require.Equal(t, codes.InvalidArgument, code)
	require.Equal(t, "too_many_call_images", md["reason"])
	require.Equal(t, strconv.Itoa(orimages.MaxInputReferences), md["ceiling"],
		"отказ обязан назвать потолок, иначе человеку нечего чинить")

	// ─── ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: ЗАКОННАЯ ФОРМА ПРОХОДИТ ───
	//
	// Без него все три отказа доказывали бы только то, что перекрас с тканью не работает никогда.
	require.NoError(t, designRefuseUnworkableSources(entity.DesignRunKindRecolor,
		&pb_common.DesignRunParams{
			ExtraInputMediaIds: []int32{11, 12, 13},
			Colour: &pb_common.DesignColourRecipe{
				Hex: "#a41f22", Fabrics: []*pb_common.DesignFabricUse{cloth(9)},
			},
		}), "одна ткань с картинкой, три фотографии и цвет — ровно то, о чём просил владелец")

	// И ВТОРАЯ ТКАНЬ БЕЗ КАРТИНКИ НЕ СЧИТАЕТСЯ: в вызов она не уедет, значит и объяснять её нечем.
	require.NoError(t, designRefuseUnworkableSources(entity.DesignRunKindRecolor,
		&pb_common.DesignRunParams{
			ExtraInputMediaIds: []int32{11},
			Colour: &pb_common.DesignColourRecipe{Fabrics: []*pb_common.DesignFabricUse{
				cloth(9), {Name: "rib, described but never photographed"},
			}},
		}))
}

// ═══ D1: РЕРАН, ПРИСЛАВШИЙ ПАРАМЕТРЫ, НЕ НАЗЫВАЕТ ЭТИМ КОЛОРВЕЙ ════════════════════════════════
//
// ⚠ ФЛАГ РЕШАЕТ, КАКАЯ ПОЛОВИНА СТОРА ОТВЕТИТ НА УДАЛЁННЫЙ КОЛОРВЕЙ: мягкая (деградировать в
// неатрибутированный) или строгая (отказать `foreign_colorway`). Прежнее написание считало
// НАЗВАВШИМ всякого, кто прислал params по любой причине, — и реран, поправивший `ask`, приезжал
// сюда с колорвеем, УНАСЛЕДОВАННЫМ designEffectiveParams, и с флагом «это сказал я». Если тот
// колорвей успели удалить, законный реран отказывался НАВСЕГДА.
//
// Денег это не стоило (отказ до резерва) — стоило это невозможности повторить прогон.
func TestARerunThatSendsParamsDOES_NOT_THEREBY_NAME_A_COLOURWAY(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params *pb_common.DesignRunParams
		stated bool
	}{
		{"no params at all — inherits, and says so", nil, false},
		{"params sent for another reason (a new ask), colourway left at 0", &pb_common.DesignRunParams{
			Views: []string{entity.DesignViewFront}, Layout: designLayoutOne,
		}, false},
		{"the caller names a colourway itself", &pb_common.DesignRunParams{ColorwayId: 7}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := designStartRequest(entity.DesignRunKindRender)
			req.Params = tc.params
			require.Equal(t, tc.stated, req.GetParams().GetColorwayId() > 0,
				"именно это выражение уезжает в ColorwayStated")
		})
	}

	// И ЖИВОЙ ПРОГОН МЕРЯЕТ ТО ЖЕ САМОЕ НА ПРОВОДЕ, а не только выражение.
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	req := designStartRequest(entity.DesignRunKindRender)
	req.Params = &pb_common.DesignRunParams{Views: []string{entity.DesignViewFront}, Layout: designLayoutOne}
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.False(t, rig.sent.ColorwayStated,
		"прислать параметры — не значит назвать колорвей; иначе мягкая половина стора не сработает")

	rig2 := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig2.design.EXPECT().AssertMediaNotForeign(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	req2 := designStartRequest(entity.DesignRunKindRender)
	req2.Params = &pb_common.DesignRunParams{
		Views: []string{entity.DesignViewFront}, Layout: designLayoutOne, ColorwayId: 0,
	}
	req2.Params.ColorwayId = 5
	_, err = rig2.srv.StartDesignRun(designRunCtx(), req2)
	require.NoError(t, err)
	require.True(t, rig2.sent.ColorwayStated, "положительный контроль: названный колорвей назван")
	require.Equal(t, 5, rig2.sent.ColorwayId)
}

// ═══ N1: ПАТТЕРН ДЕЛАЕТСЯ ИЗ ТКАНИ ИЛИ ИЗ ПАТТЕРНА, НО НЕ ИЗ ФУРНИТУРЫ ═════════════════════════
//
// Контракт `source_asset_id` называет `fabric|pattern`, и запись едет в `derived_from_asset_id`,
// чей собственный контракт говорит «паттерн, сделанный из ткани». «Этот принт сделан из молнии» —
// предложение без смысла, а строка с ним переживает прогон навсегда. FK этого не ловит.
func TestAPatternIsNOT_MADE_FROM_HARDWARE(t *testing.T) {
	assets := []entity.DesignAsset{
		{Id: 51, TechCardId: designRunCardID, Kind: entity.DesignAssetKindFabric, Name: "jersey"},
		{Id: 52, TechCardId: designRunCardID, Kind: entity.DesignAssetKindPattern, Name: "chevron"},
		{Id: 53, TechCardId: designRunCardID, Kind: entity.DesignAssetKindHardware, Name: "zip"},
	}
	for _, tc := range []struct {
		src     int32
		refused bool
	}{{51, false}, {52, false}, {53, true}} {
		err := designRefuseForeignPatternSource(designRunCardID, &pb_common.DesignRunParams{
			Pattern: &pb_common.DesignPatternParams{Name: "x", SourceAssetId: tc.src},
		}, assets)
		if !tc.refused {
			require.NoErrorf(t, err, "asset %d", tc.src)
			continue
		}
		require.Errorf(t, err, "asset %d", tc.src)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), "made from a cloth or from another pattern")
	}
}
