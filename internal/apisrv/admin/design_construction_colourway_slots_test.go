package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ O-44 п.2 — КОЛОРВЕЙ КРАСИТ КАЖДЫЙ ЦВЕТНОЙ СЛОТ, НИТКУ ТОЖЕ ════════════════════════════════
//
// Владелец: «в MATERIAL SLOTS есть слот THREAD но в COLOURWAYS этого слота нету». Правило 9 просило
// красить «cloth slot from bom», а строки спеки доезжали до модели только запретом «уже на карточке
// — не повторяй». Теперь правило просит каждый цветной слот карточки и ответа, а промпт называет
// слоты карточки поимённо — секцией «Slots to colour».

// РОЛЬ ПРОСИТ ВСЕ ЦВЕТНЫЕ СЛОТЫ, С ПАНТОНОМ ПО СЕМЕЙСТВУ, И ССЫЛАЕТСЯ НА СПИСОК.
//
// МУТАЦИИ: вернуть «every cloth slot from "bom"» (нитка снова без цвета — исходная жалоба); снять
// перечень семейств или правило пантона; потерять ссылку на секцию «Slots to colour».
func TestConstructionRuleNineColoursEveryMaterialSlot(t *testing.T) {
	p := designConstructionSystemPrompt
	for _, want := range []string{
		"naming every colour-bearing slot of the card and of \"bom\"",
		"cloth, lining, thread, hardware, trims",
		"by its exact name",
		"Pantone code (TCX for cloth, TCX or C otherwise) and a hex",
		"under \"Slots to colour\"",
		// Потолок и порядок, которые ввело ревью 26.09, остались как были.
		fmt.Sprintf("(the main cloths first; at most %d)", designConstructionMaxColourwaySlots),
	} {
		require.Contains(t, p, want)
	}
	require.NotContains(t, p, "every cloth slot", "правило 9 больше не ограничено тканями (O-44 п.2)")
}

// СЛОТЫ ИДУТ ТЕМИ ЖЕ ИМЕНАМИ И В ТОМ ЖЕ ПОРЯДКЕ, ЧТО В ТАБЛИЦЕ MATERIAL SLOTS.
//
// Ткани (рулонные секции) — первыми, нитки — сразу за ними, всё остальное — после; внутри семейства
// — порядок карточки. Правило 9 красит не больше восьми слотов «главные ткани первыми», поэтому
// порядок решает, что будет окрашено. Дубли по складке схлопываются, пустые имена не пишутся.
//
// МУТАЦИИ: перечислять в порядке карточки (нитка уезжает за фурнитуру); писать секцию, а не имя;
// не схлопывать дубли; писать пустое имя.
func TestConstructionSlotsToColourListsEveryLineByNameInFamilyOrder(t *testing.T) {
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
		"- main jersey\n- mesh lining\n- poly-core thread\n- metal zip\n- care label\n- rib binding\n",
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
	require.Contains(t, prompt, "\nSlots to colour — the card's material slots;")
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
		"порядок таблицы MATERIAL SLOTS: ткань, нитка, фурнитура: %s", section)
}
