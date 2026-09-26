package admin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// ═══ O-44 п.2 — КОЛОРВЕЙ КРАСИТ КАЖДЫЙ ЦВЕТНОЙ СЛОТ, НИТКУ ТОЖЕ ════════════════════════════════
//
// Владелец: «в MATERIAL SLOTS есть слот THREAD но в COLOURWAYS этого слота нету». Правило 9 просило
// красить «cloth slot from bom», а строки спеки доезжали до модели только запретом «уже на карточке
// — не повторяй». Теперь правило просит цветные слоты карточки и ответа, а промпт называет слоты
// карточки поимённо — секцией «Slots to colour».
//
// РЕВЬЮ O-44 (26.09, Codex): «каждый цветной слот» при потолке в восемь цветов не держался, и порядок
// «сначала все ткани» выталкивал нитку за восьмёрку. Теперь список и правило 9 говорят одним
// порядком — главные ткани, нитка, остальное, — и имена идут в мере разбора (60 рун), чтобы эхо
// длинного имени привязалось.

// mainCloth — рулонная строка с назначением main (0265).
func mainCloth(name string) entity.TechCardBomItem {
	return entity.TechCardBomItem{
		Section: entity.BomSectionFabric, Name: name,
		Purpose: sql.NullString{String: string(entity.BomPurposeMain), Valid: true},
	}
}

// slotLines — имена списка «Slots to colour» по порядку, без хвоста «(+N more …)».
func slotLines(list string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSuffix(list, "\n"), "\n") {
		if strings.HasPrefix(l, "- (+") {
			continue
		}
		out = append(out, strings.TrimPrefix(l, "- "))
	}
	return out
}

// РОЛЬ ПРОСИТ ЦВЕТНЫЕ СЛОТЫ ОДНИМ ПОРЯДКОМ, С ПАНТОНОМ ПО СЕМЕЙСТВУ, И ССЫЛАЕТСЯ НА СПИСОК.
//
// МУТАЦИИ: вернуть «every cloth slot from "bom"» (нитка снова без цвета — исходная жалоба); вернуть
// «every colour-bearing slot» (обещание, которого потолок в восемь не держит); вернуть «the main
// cloths first» без нитки (нитка снова может не войти в восьмёрку); снять перечень семейств или
// правило пантона; потерять ссылку на секцию «Slots to colour».
func TestConstructionRuleNineColoursTheSlotsInOneOrder(t *testing.T) {
	p := designConstructionSystemPrompt
	for _, want := range []string{
		"naming the colour-bearing slots of the card and of \"bom\"",
		"cloth, lining, thread, hardware, trims",
		"each by its exact name",
		fmt.Sprintf("at most %d, in this order: the main cloths, the thread, then the rest;",
			designConstructionMaxColourwaySlots),
		"Pantone code (TCX for cloth, TCX or C otherwise) and a hex",
		"under \"Slots to colour\" in that order",
	} {
		require.Contains(t, p, want)
	}
	require.NotContains(t, p, "every cloth slot", "правило 9 больше не ограничено тканями (O-44 п.2)")
	require.NotContains(t, p, "every colour-bearing slot", "потолок в восемь не держит «каждый» (ревью O-44)")
	require.NotContains(t, p, "the main cloths first;", "порядок правила — с ниткой (ревью O-44)")
}

// СЛОТЫ ИДУТ ИМЕНАМИ В ПОРЯДКЕ ПРИОРИТЕТА: ГЛАВНЫЕ ТКАНИ, НИТКА, ОСТАЛЬНЫЕ РУЛОННЫЕ, ПРОЧЕЕ.
//
// Внутри ступени — порядок карточки. Карточка без отмеченной main главной считает первую рулонную
// строку. Дубли по складке схлопываются, пустые имена не пишутся.
//
// МУТАЦИИ: вернуть порядок «все ткани, затем нитка» (подклад встаёт перед ниткой); перечислять в
// порядке карточки (нитка уезжает за фурнитуру); не читать назначение main (отмеченная ткань не
// поднимается над первой по порядку); писать секцию, а не имя; не схлопывать дубли; писать пустое имя.
func TestConstructionSlotsToColourListsEveryLineByNameInPriorityOrder(t *testing.T) {
	card := &entity.TechCard{}
	card.BomItems = []entity.TechCardBomItem{
		{Section: entity.BomSectionHardware, Name: "metal zip"},
		{Section: entity.BomSectionThread, Name: "poly-core thread"},
		{Section: entity.BomSectionFabric, Name: "main jersey"},
		{Section: entity.BomSectionLabel, Name: "care label"},
		{Section: entity.BomSectionLining, Name: "mesh lining"},
		{Section: entity.BomSectionTrim, Name: "rib binding"},
		{Section: entity.BomSectionFabric, Name: "Main  Jersey"}, // та же складка — дубль
		{Section: entity.BomSectionFabric, Name: "   "},          // пустое имя — не слот
	}
	require.Equal(t,
		"- main jersey\n- poly-core thread\n- mesh lining\n- metal zip\n- care label\n- rib binding\n",
		designSlotsToColour(card), "без отмеченной main главная — первая рулонная строка")

	// Отмеченная main поднимается над первой по порядку карточки, и главной становится ОНА.
	card.BomItems = []entity.TechCardBomItem{
		{Section: entity.BomSectionFabric, Name: "pocket bag"},
		{Section: entity.BomSectionThread, Name: "poly-core thread"},
		mainCloth("shell twill"),
		{Section: entity.BomSectionHardware, Name: "shank button"},
	}
	require.Equal(t,
		"- shell twill\n- poly-core thread\n- pocket bag\n- shank button\n",
		designSlotsToColour(card))

	require.Empty(t, designSlotsToColour(&entity.TechCard{}), "пустой карточке секции не положено")
	require.Empty(t, designSlotsToColour(nil))
}

// ЗА ПОТОЛКОМ — ЧЕСТНЫЙ ХВОСТ, А НЕ МОЛЧАЛИВАЯ ОБРЕЗКА.
func TestConstructionSlotsToColourNamesWhatItDidNotList(t *testing.T) {
	card := &entity.TechCard{}
	for i := 0; i < designConstructionMaxSlotsToColour+3; i++ {
		card.BomItems = append(card.BomItems, entity.TechCardBomItem{
			Section: entity.BomSectionHardware, Name: fmt.Sprintf("part %02d", i),
		})
	}
	got := designSlotsToColour(card)
	require.Equal(t, designConstructionMaxSlotsToColour+1, strings.Count(got, "\n"))
	require.Contains(t, got, "- part 00\n")
	require.NotContains(t, got, fmt.Sprintf("part %02d", designConstructionMaxSlotsToColour))
	require.True(t, strings.HasSuffix(got, "- (+3 more slots on the card, not listed)\n"), got)
}

// ПОЛЬЗОВАТЕЛЬСКИЙ ПРОМПТ НЕСЁТ СЕКЦИЮ — И НЕ НЕСЁТ ЕЁ У КАРТОЧКИ БЕЗ СЛОТОВ.
func TestConstructionUserPromptListsTheSlotsToColour(t *testing.T) {
	card := draftBindCard()
	card.BomItems = []entity.TechCardBomItem{
		{Section: entity.BomSectionThread, Name: "SLOTNAME-thread"},
		{Section: entity.BomSectionFabric, Name: "SLOTNAME-main"},
	}
	prompt := designConstructionUserPrompt(card, designMoodSnapshot(card), []int{11, 22, 33}, draftProbeColours())
	require.Contains(t, prompt, "\nSlots to colour — the card's material slots, in the order to colour them "+
		"(the main cloths, the thread, then the rest);")
	require.Contains(t, prompt, "- SLOTNAME-main\n- SLOTNAME-thread")

	bare := draftBindCard()
	require.NotContains(t,
		designConstructionUserPrompt(bare, designMoodSnapshot(bare), []int{11, 22, 33}, draftProbeColours()),
		"Slots to colour", "карточка без слотов — секции нет")
}

// ХЕНДЛЕР ОТДАЁТ МОДЕЛИ НИТКУ КАРТОЧКИ ПОИМЁННО — ПО БАЙТАМ, УШЕДШИМ В СЕТЬ.
//
// Это и есть жалоба владельца, измеренная там, где она случилась: слот THREAD стоит в MATERIAL
// SLOTS, и платный запрос обязан его назвать в списке того, что красить, а не только в запрете
// «уже на карточке». МУТАЦИЯ: не вызвать designSlotsToColour из designConstructionUserPrompt —
// строка нитки остаётся только в виде «- bom: thread · …», и проба краснеет.
func TestDraftConstructionTellsTheModelToColourTheCardsThread(t *testing.T) {
	card := designMoodCard()
	card.BomItems = []entity.TechCardBomItem{
		{Section: entity.BomSectionHardware, Name: "SLOT-metal-zip"},
		{Section: entity.BomSectionThread, Name: "SLOT-poly-core-thread"},
		{Section: entity.BomSectionFabric, Name: "SLOT-main-jersey"},
	}
	rig := newDraftRigWithCard(t, http.StatusOK, constructionAnswer, card,
		[]int{designBoardMediaID},
		map[int]entity.MediaFull{designBoardMediaID: {
			Id:        designBoardMediaID,
			MediaItem: entity.MediaItem{FullSizeMediaURL: designBoardMediaURL},
		}})
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.NoError(t, err)

	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(rig.stub.body), &body))
	var userTurn string
	for _, m := range body.Messages {
		if m.Role == "user" {
			userTurn, _ = orContent(t, m.Content)
		}
	}
	at := strings.Index(userTurn, "Slots to colour")
	require.GreaterOrEqual(t, at, 0, "секция слотов обязана доехать до модели: %s", userTurn)
	section := userTurn[at:]
	require.Contains(t, section, "- SLOT-poly-core-thread\n", "нитка карточки названа поимённо")
	jersey := strings.Index(section, "- SLOT-main-jersey")
	thread := strings.Index(section, "- SLOT-poly-core-thread")
	zip := strings.Index(section, "- SLOT-metal-zip")
	require.True(t, jersey >= 0 && jersey < thread && thread < zip,
		"порядок приоритета: главная ткань, нитка, фурнитура: %s", section)
}

// НИТКА ВХОДИТ В ВОСЬМЁРКУ, ДАЖЕ КОГДА ТКАНЕЙ БОЛЬШЕ ВОСЬМИ (ревью O-44, сценарий Codex дословно).
//
// Девять рулонных строк, одна нитка, пятнадцать единиц фурнитуры: правило 9 красит не больше восьми
// слотов в порядке списка, и нитка обязана стоять среди первых восьми имён — и когда main не отмечена
// ни у одной ткани, и когда отмечена у всех девяти (главных тогда семь, восьмое место — нитке).
// Правило называет этот же порядок словами.
//
// МУТАЦИИ: вернуть порядок «все ткани, затем нитка» (нитка — десятая); снять потолок главных (девять
// отмеченных main — нитка десятая); поставить нитку за «остальными рулонными».
func TestConstructionSlotsToColourKeepsTheThreadWithinTheColourCap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marked bool
	}{{"main не отмечена", false}, {"main у всех девяти", true}} {
		t.Run(tc.name, func(t *testing.T) {
			card := &entity.TechCard{}
			for i := 0; i < 9; i++ {
				item := entity.TechCardBomItem{Section: entity.BomSectionFabric, Name: fmt.Sprintf("cloth %02d", i)}
				if tc.marked {
					item = mainCloth(fmt.Sprintf("cloth %02d", i))
				}
				card.BomItems = append(card.BomItems, item)
			}
			card.BomItems = append(card.BomItems, entity.TechCardBomItem{Section: entity.BomSectionThread, Name: "poly-core thread"})
			for i := 0; i < 15; i++ {
				card.BomItems = append(card.BomItems, entity.TechCardBomItem{
					Section: entity.BomSectionHardware, Name: fmt.Sprintf("snap %02d", i),
				})
			}
			names := slotLines(designSlotsToColour(card))
			at := -1
			for i, n := range names {
				if n == "poly-core thread" {
					at = i
				}
			}
			require.GreaterOrEqual(t, at, 0, "нитка обязана быть в списке: %v", names)
			require.Less(t, at, designConstructionMaxColourwaySlots,
				"нитка обязана войти в восемь окрашиваемых слотов: %v", names)
			require.Equal(t, "cloth 00", names[0], "первой идёт главная ткань")
		})
	}
	require.Contains(t, designConstructionSystemPrompt,
		fmt.Sprintf("at most %d, in this order: the main cloths, the thread, then the rest",
			designConstructionMaxColourwaySlots),
		"правило 9 называет тот же порядок, что и список")
}

// НИТКА ВХОДИТ В СПИСОК, ДАЖЕ КОГДА ТКАНЕЙ БОЛЬШЕ ПОТОЛКА СПИСКА (ревью O-44).
//
// Двадцать пять рулонных строк и нитка последней в карточке: при прежнем порядке нитка была бы
// двадцать шестой и не попала бы в промпт вовсе. Хвост честно называет, сколько не показано.
func TestConstructionSlotsToColourKeepsTheThreadWithinTheList(t *testing.T) {
	card := &entity.TechCard{}
	for i := 0; i < 25; i++ {
		card.BomItems = append(card.BomItems, entity.TechCardBomItem{
			Section: entity.BomSectionFabric, Name: fmt.Sprintf("cloth %02d", i),
		})
	}
	card.BomItems = append(card.BomItems, entity.TechCardBomItem{Section: entity.BomSectionThread, Name: "poly-core thread"})

	list := designSlotsToColour(card)
	names := slotLines(list)
	require.Len(t, names, designConstructionMaxSlotsToColour)
	require.Contains(t, names, "poly-core thread", "нитка обязана доехать до модели")
	require.Equal(t, "poly-core thread", names[1], "сразу за главной тканью")
	require.True(t, strings.HasSuffix(list, "- (+6 more slots on the card, not listed)\n"), list)
}

// ИМЯ СЛОТА ЕДЕТ И ПРИВЯЗЫВАЕТСЯ В ОДНОЙ МЕРЕ — ГРАНИЦА 60/61 (ревью O-44, MINOR).
//
// Имя в 60 знаков идёт как есть; в 61 — обрезанным разбором до 60 с маркером. Эхо модели — и
// обрезанное, как его показал список, и полное — привязывается к строке карточки, и клиенту уходит
// ПОЛНОЕ имя строки: клиент складывает полные имена, и обрезанное эхо у него не сложилось бы ни с
// чем. Короткое имя не переписывается. Повтор отдаёт то же, что первый ответ: канон не режет полное
// имя обратно.
//
// МУТАЦИИ: слать в список имя до 200 рун (эхо обрезается разбором, мера расходится); привязывать по
// складке полного имени карточки (61 знак отваливается — исходный дефект); не возвращать полное имя
// (клиент не привяжет); резать слот канона по рунам (повтор расходится с первым ответом).
func TestConstructionSlotNamesAreSentAndBoundInOneMeasure(t *testing.T) {
	at60 := strings.Repeat("a", designConstructionMaxNameRunes-6) + " cloth"
	at61 := strings.Repeat("b", designConstructionMaxNameRunes-5) + " cloth"
	require.Len(t, []rune(at60), designConstructionMaxNameRunes)
	require.Len(t, []rune(at61), designConstructionMaxNameRunes+1)

	card := &entity.TechCard{}
	card.BomItems = []entity.TechCardBomItem{mainCloth(at60), {Section: entity.BomSectionThread, Name: at61}}
	names := slotLines(designSlotsToColour(card))
	require.Equal(t, at60, names[0], "60 знаков — как есть")
	shown := names[1]
	require.Len(t, []rune(shown), designConstructionMaxNameRunes, "61 знак — в мере разбора")
	require.True(t, strings.HasSuffix(shown, "…"), "обрезка маркирована: %q", shown)

	answer := func(echo61 string) *pb_common.DesignConstructionDraft {
		t.Helper()
		js, err := json.Marshal(map[string]any{"colourways": []any{map[string]any{
			"name": "Black", "color_code": "BLK",
			"slots": []any{
				map[string]any{"slot": at60, "colour": "black"},
				map[string]any{"slot": echo61, "colour": "black"},
			},
		}}})
		require.NoError(t, err)
		draft, _, err := parseConstructionDraft(string(js), "stop")
		require.NoError(t, err)
		var stats designConstructionStats
		designVerifyColourways(draft, designBuildColourDictionary(draftProbeColours()),
			designCardSlotFolds(card), &stats)
		require.Zero(t, stats.SlotColoursUnbound, "оба слота обязаны привязаться")
		return draft
	}
	for _, echo := range []string{shown, at61} {
		draft := answer(echo)
		slots := draft.GetColourways()[0].GetSlots()
		require.Len(t, slots, 2)
		require.Equal(t, at60, slots[0].GetSlot(), "короткое имя не переписывается")
		require.Equal(t, at61, slots[1].GetSlot(), "клиенту — полное имя строки карточки")

		canonical, err := designMarshalConstructionDraft(draft)
		require.NoError(t, err)
		replayed := designConstructionDraftFromRun(string(canonical))
		require.NotNil(t, replayed)
		require.Equal(t, at61, replayed.GetColourways()[0].GetSlots()[1].GetSlot(),
			"повтор отдаёт то же, что первый ответ")
	}
}
