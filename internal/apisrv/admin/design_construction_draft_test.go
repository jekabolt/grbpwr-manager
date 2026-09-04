package admin

// ПРОБЫ СТРУКТУРНОГО ЧЕРНОВИКА КОНСТРУКЦИИ (фаза 1).
//
// ЧЕТЫРЕ ВЕЩИ, КОТОРЫЕ ЛОМАЮТСЯ МОЛЧА, И ПОЭТОМУ ПРИБИТЫ ЗДЕСЬ:
//
//  1. СТАРЫЙ ЗАПРОС ОБЯЗАН ДАВАТЬ СТАРЫЕ БАЙТЫ. Флаг отсутствует — значит та же роль, тот же
//     промпт, никакого json-режима и никакого потолка токенов. Сломав это, мы сломали бы кнопку,
//     работающую на бете, ради кнопки, которой ещё нет ни у одного клиента.
//  2. РАЗБОР КОЭРЦИРУЕТ ФОРМУ И РОНЯЕТ ТОЛЬКО ФОРМУ. Узнаваемое написание («Sleeve / Cuff»,
//     «FABRIC») приводится молча; неузнаваемая строка выбрасывается по одной, а весь оплаченный
//     прогон роняют ровно два исхода — не тот JSON и обрезанный ответ.
//  3. ПОВТОР ВОССТАНАВЛИВАЕТ ЧЕРНОВИК ИЗ СТРОКИ. Второй клик модель не зовёт; черновик, который
//     нельзя прочитать обратно из `output_text`, исчезал бы именно на том жесте, ради которого
//     идемпотентность и заведена.
//  4. ПРОМПТ НЕСЁТ ПРИВЯЗКУ И «УЖЕ НА КАРТОЧКЕ». Без первого модель не знает, где отмечено; без
//     второго правило «не повторяй» невыполнимо, и половина предложений приезжает дубликатами.
//
// МУТАЦИИ, КОТОРЫМИ ПРОВЕРЕНО (по ЧИСЛУ ИСПОЛНЕННЫХ ИСХОДОВ, а не по коду возврата):
//   - звать CompleteWithImages с jsonMode=true всегда → краснеет проба старых байтов;
//   - принять ответ при finish_reason=length → краснеет проба обрезанного ответа;
//   - писать в output_text `text` модели вместо канонического JSON → краснеет проба повтора;
//   - собрать пользовательский промпт без секции «уже на карточке» → краснеет проба промпта.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// ─────────────────────────── РАЗБОР ───────────────────────────

// ХОРОШИЙ ОТВЕТ РАЗБИРАЕТСЯ ЦЕЛИКОМ, И КАЖДОЕ ПОЛЕ ДОЕЗЖАЕТ ТУДА, КУДА ОБЕЩАНО.
//
// Положительный контроль всех проб ниже: без него «дрейф выброшен» зеленело бы и на разборе,
// который не принимает вообще ничего.
func TestParseConstructionDraftReadsAWellFormedAnswer(t *testing.T) {
	raw := `{
	  "silhouette": "Sleeveless V-neck tank top",
	  "fabric": "Stretch knit jersey",
	  "fit": "regular",
	  "concept": "A lean summer tank.",
	  "aspects": [{"key": "collar", "text": "V-neck, self-fabric binding 1 cm"}],
	  "callouts": [{"feature": "neck binding", "details": "self-fabric, folded", "dimensions": "1 cm"}],
	  "bom": [{"section": "fabric", "purpose": "main", "name": "main fabric",
	           "composition": "95% cotton 5% elastane", "colour": "black", "pantone": "19-4005 TCX"}],
	  "missing": ["picture 2 — the strap join at the shoulder"]
	}`
	draft, stats, err := parseConstructionDraft(raw, "stop")
	require.NoError(t, err)
	require.False(t, stats.Any(), "чистый ответ не нуждается ни в одной поправке: %+v", stats)

	require.Equal(t, "Sleeveless V-neck tank top", draft.GetSilhouette())
	require.Equal(t, "Stretch knit jersey", draft.GetFabric())
	require.Equal(t, "regular", draft.GetFit())
	require.Equal(t, "A lean summer tank.", draft.GetConcept())

	require.Len(t, draft.GetAspects(), 1)
	require.Equal(t, "collar", draft.GetAspects()[0].GetKey())

	require.Len(t, draft.GetCallouts(), 1)
	require.Equal(t, "neck binding", draft.GetCallouts()[0].GetFeature())
	require.Equal(t, "1 cm", draft.GetCallouts()[0].GetDimensions())

	require.Len(t, draft.GetBom(), 1)
	line := draft.GetBom()[0]
	require.Equal(t, pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, line.GetSection())
	require.Equal(t, pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_MAIN, line.GetPurpose())
	require.Equal(t, pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET, line.GetKind(),
		"вид не был назван — он и обязан остаться «не задано», а не выдуматься")
	require.Equal(t, "main fabric", line.GetName())
	require.Equal(t, "black", line.GetColour())
	require.EqualValues(t, 0, line.GetMaterialId())

	require.Equal(t, []string{"picture 2 — the strap join at the shoulder"}, draft.GetMissing())
}

// ФОРМА ОТВЕТА: ЧТО ПРИНИМАЕТСЯ, А ЧТО РОНЯЕТ ВЕСЬ ОПЛАЧЕННЫЙ ПРОГОН.
func TestParseConstructionDraftShapeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name         string
		raw          string
		finishReason string
		wantErr      string
	}{
		{
			name:    "проза вместо объекта",
			raw:     "DESCRIPTION\nA boxy coat with a storm flap.",
			wantErr: "no JSON object",
		},
		{
			name:    "пустое тело",
			raw:     "   ",
			wantErr: "no JSON object",
		},
		{
			name:    "объект без единого ключа черновика",
			raw:     `{"answer": "a boxy coat", "notes": []}`,
			wantErr: "none of the construction draft keys",
		},
		{
			name:    "только совет и ничего, чему есть куда лечь",
			raw:     `{"missing": ["picture 1 — the hem"]}`,
			wantErr: "none of the construction draft keys",
		},
		{
			name:    "сломанный JSON",
			raw:     `{"silhouette": "boxy",}`,
			wantErr: "not a construction draft",
		},
		{
			name:         "ответ обрезан потолком токенов",
			raw:          `{"silhouette": "boxy", "aspects": [{"key": "collar", "text": "V-ne`,
			finishReason: "length",
			wantErr:      "cut by the token ceiling",
		},
		{
			// ⚠ ОБРЕЗАННЫЙ ОТВЕТ РОНЯЕТСЯ, ДАЖЕ ЕСЛИ ОН СЛУЧАЙНО РАЗБИРАЕТСЯ. Половина черновика
			// неотличима от полного: человек прочитал бы её как «остального модель не увидела».
			name:         "обрезан, но синтаксически целый",
			raw:          `{"silhouette": "boxy"}`,
			finishReason: "LENGTH",
			wantErr:      "cut by the token ceiling",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			draft, _, err := parseConstructionDraft(tc.raw, tc.finishReason)
			require.Error(t, err)
			require.Nil(t, draft)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// ПУСТОЙ, НО ПРИСУТСТВУЮЩИЙ КЛЮЧ — ЗАКОННЫЙ ОТВЕТ.
//
// «Модель посмотрела и ей нечего сказать» и «модель ответила не по схеме» — разные новости, и
// схлопнув их, разбор либо показал бы человеку четыре пустые группы вместо отказа, либо отказал бы
// на честном ответе.
func TestParseConstructionDraftAcceptsAnEmptyButPresentAnswer(t *testing.T) {
	draft, _, err := parseConstructionDraft(`{"silhouette": "", "aspects": []}`, "stop")
	require.NoError(t, err)
	require.NotNil(t, draft)
	require.Empty(t, draft.GetSilhouette())
	require.Empty(t, draft.GetAspects())
}

// ОГРАДА И ПРОЗА ВОКРУГ ОБЪЕКТА ТЕРПИМЫ — ровно как у разбора тех-карты, и по той же причине:
// расхождение двух разборов означало бы, что один и тот же ответ модели принимает один платный
// путь и отвергает соседний.
func TestParseConstructionDraftToleratesAFenceAndProse(t *testing.T) {
	raw := "Here is the draft:\n```json\n{\"silhouette\": \"boxy\"}\n```\nHope it helps."
	draft, _, err := parseConstructionDraft(raw, "")
	require.NoError(t, err)
	require.Equal(t, "boxy", draft.GetSilhouette())
}

// КЛЮЧИ АСПЕКТОВ: УЗНАВАЕМЫЙ ДРЕЙФ СКЛАДЫВАЕТСЯ, НЕУЗНАВАЕМЫЙ ЖИВЁТ КАК САМОДЕЛЬНЫЙ.
//
// Ключ «почти тот» — это ВТОРАЯ строка рядом с существующей: на экране один и тот же аспект
// оказался бы дважды. Поэтому три написания обязаны схлопнуться в одно.
func TestParseConstructionDraftFoldsAspectKeys(t *testing.T) {
	for _, tc := range []struct {
		spelling string
		want     string
		custom   bool
	}{
		{spelling: "sleeveCuff", want: "sleeveCuff"},
		{spelling: "sleeve_cuff", want: "sleeveCuff"},
		{spelling: "Sleeve / Cuff", want: "sleeveCuff"},
		{spelling: "SLEEVECUFF", want: "sleeveCuff"},
		{spelling: "extra details", want: "extraDetails"},
		{spelling: "Topstitching", want: "topstitching"},
		{spelling: "vent", want: "vent", custom: true},
	} {
		t.Run(tc.spelling, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"aspects": []map[string]string{{"key": tc.spelling, "text": "some words"}},
			})
			require.NoError(t, err)
			draft, stats, perr := parseConstructionDraft(string(body), "stop")
			require.NoError(t, perr)
			require.Len(t, draft.GetAspects(), 1)
			require.Equal(t, tc.want, draft.GetAspects()[0].GetKey())
			if tc.custom {
				require.Equal(t, 1, stats.AspectsCustom, "самодельный ключ обязан быть СОСЧИТАН, а не принят молча")
			} else {
				require.Zero(t, stats.AspectsCustom)
			}
		})
	}
}

// ТОКЕНЫ СПЕЦИФИКАЦИИ: КОРОТКОЕ ИМЯ, ПОЛНОЕ ИМЯ ЧЛЕНА И ПИСЬМО ЧЕЛОВЕКА — ОДНО И ТО ЖЕ.
//
// Полное имя здесь не гипотеза: им отвечает НАШ СОБСТВЕННЫЙ канонический JSON, который тот же
// разбор читает на повторе. Приняв только короткое, повтор терял бы секцию у каждой строки.
func TestParseConstructionDraftFoldsBomTokens(t *testing.T) {
	for _, tc := range []struct {
		name    string
		section string
		kind    string
		want    pb_common.TechCardBomSection
		wantK   pb_common.TechCardBomKind
		unset   int
	}{
		{name: "короткие имена", section: "hardware", kind: "zipper",
			want:  pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE,
			wantK: pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_ZIPPER},
		{name: "полные имена членов", section: "TECH_CARD_BOM_SECTION_TRIM", kind: "TECH_CARD_BOM_KIND_DRAWCORD",
			want:  pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_TRIM,
			wantK: pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_DRAWCORD},
		{name: "регистр и пробелы", section: " Hardware ", kind: "Hook and bar",
			want:  pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE,
			wantK: pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_HOOK_AND_BAR},
		{name: "синоним не узнан — строка сохранена, токен пуст", section: "textile", kind: "clasp",
			want:  pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_UNKNOWN,
			wantK: pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET, unset: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"bom": []map[string]string{{"section": tc.section, "kind": tc.kind, "name": "a component"}},
			})
			require.NoError(t, err)
			draft, stats, perr := parseConstructionDraft(string(body), "stop")
			require.NoError(t, perr)
			require.Len(t, draft.GetBom(), 1, "строка спеки обязана выжить даже с неузнанным токеном")
			require.Equal(t, tc.want, draft.GetBom()[0].GetSection())
			require.Equal(t, tc.wantK, draft.GetBom()[0].GetKind())
			require.Equal(t, tc.unset, stats.EnumsUnset)
		})
	}
}

// АРТИКУЛ, ПРЕДЛОЖЕННЫЙ МОДЕЛЬЮ, ОБНУЛЯЕТСЯ — И СЧИТАЕТСЯ.
//
// Каталог в промпт не уезжает, значит подтвердить id нечем. Строка, выглядящая связанной и
// оценённой, но указывающая на чужой артикул, — ошибка себестоимости с ценником.
func TestParseConstructionDraftNeverTrustsAMaterialID(t *testing.T) {
	for _, raw := range []string{
		`{"bom":[{"name":"main fabric","material_id":4211}]}`,
		`{"bom":[{"name":"main fabric","material_id":"4211"}]}`,
	} {
		draft, stats, err := parseConstructionDraft(raw, "stop")
		require.NoError(t, err)
		require.Len(t, draft.GetBom(), 1)
		require.EqualValues(t, 0, draft.GetBom()[0].GetMaterialId())
		require.Equal(t, 1, stats.MaterialIDs,
			"«модель придумывает артикулы» — это про промпт, и узнать это можно только из лога")
	}
}

// COLOR И COLOUR — ОДНО ПОЛЕ. Промпт просит британское написание, модель регулярно пишет
// американское; приняв одно, разбор терял бы цвет у каждой второй строки по орфографии.
func TestParseConstructionDraftAcceptsBothSpellingsOfColour(t *testing.T) {
	draft, _, err := parseConstructionDraft(`{"bom":[{"name":"binding","color":"off-white"}]}`, "stop")
	require.NoError(t, err)
	require.Equal(t, "off-white", draft.GetBom()[0].GetColour())
}

// ПОТОЛКИ, ПОВТОРЫ И ПУСТЫЕ СТРОКИ.
func TestParseConstructionDraftCapsDedupesAndDropsEmpties(t *testing.T) {
	long := strings.Repeat("я", designConstructionMaxTextRunes+50)
	veryLong := strings.Repeat("s", designConstructionMaxLongRunes+50)

	aspects := make([]map[string]string, 0, designConstructionMaxAspects+5)
	for i := 0; i < designConstructionMaxAspects+5; i++ {
		aspects = append(aspects, map[string]string{"key": "custom" + string(rune('a'+i)), "text": "words"})
	}
	body, err := json.Marshal(map[string]any{
		"silhouette": veryLong,
		"aspects":    aspects,
		"callouts": []map[string]string{
			{"feature": "side seam", "details": "overlocked"},
			{"feature": "Side Seam", "details": "Overlocked"}, // тот же ряд другим написанием
			{"feature": "", "details": ""},                    // строка без слов
			{"feature": "", "details": "", "dimensions": "12 mm"},
			{"feature": "hem", "details": long},
		},
		"bom": []map[string]string{
			{"name": "main fabric"},
			{"name": "MAIN FABRIC"},
			{"name": "   "},
		},
		"missing": []string{"pin the hem", "pin the hem", ""},
	})
	require.NoError(t, err)

	draft, stats, perr := parseConstructionDraft(string(body), "stop")
	require.NoError(t, perr)

	require.Len(t, []rune(draft.GetSilhouette()), designConstructionMaxLongRunes)
	require.Len(t, draft.GetAspects(), designConstructionMaxAspects, "потолок списка держит")
	require.Equal(t, 5, stats.OverLimit)

	require.Len(t, draft.GetCallouts(), 2, "повтор схлопнут, две бессловесные строки выброшены")
	require.Equal(t, 2, stats.CalloutsDropped)
	require.Len(t, []rune(draft.GetCallouts()[1].GetDetails()), designConstructionMaxTextRunes)

	require.Len(t, draft.GetBom(), 1)
	require.Equal(t, 1, stats.BomDropped)

	require.Equal(t, []string{"pin the hem"}, draft.GetMissing())
	require.Equal(t, 1, stats.MissingDropped)
	require.Equal(t, 3, stats.Deduped)
	require.True(t, stats.Truncated >= 2)
}

// ПОСАДКА ВНЕ ПИКЕРА НЕ ПРЕДЛАГАЕТСЯ ВОВСЕ.
//
// Посадка — это пикер; значение вне его списка человек не сможет принять одним кликом, а
// подставленное «похожее» слово было бы утверждением, которого модель не делала.
func TestParseConstructionDraftFoldsFitOntoThePicker(t *testing.T) {
	for _, tc := range []struct{ answer, want string }{
		{answer: "regular", want: "regular"},
		{answer: "Relaxed", want: "relaxed"},
		{answer: "oversized", want: ""},
		{answer: "", want: ""},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"fit": tc.answer, "silhouette": "boxy"})
			require.NoError(t, err)
			draft, _, perr := parseConstructionDraft(string(body), "stop")
			require.NoError(t, perr)
			require.Equal(t, tc.want, draft.GetFit())
		})
	}
}

// ─────────────────────────── КРУГ ХРАНЕНИЯ ───────────────────────────

// КАНОНИЧЕСКИЙ JSON ЧИТАЕТСЯ ТЕМ ЖЕ РАЗБОРОМ — ЭТО И ЕСТЬ ВОССТАНОВЛЕНИЕ НА ПОВТОРЕ.
//
// ⚠ СРАВНИВАЮТСЯ ЗНАЧЕНИЯ, А НЕ БАЙТЫ: protojson намеренно подмешивает в вывод случайный пробел,
// чтобы отучить сравнивать его побайтно (detrand). Проба, утверждающая байты, краснела бы через
// раз и была бы вычеркнута следующим же человеком.
func TestConstructionDraftSurvivesTheCanonicalRoundTrip(t *testing.T) {
	raw := `{
	  "silhouette": "boxy", "fabric": "melton wool", "fit": "relaxed",
	  "aspects": [{"key": "sleeve / cuff", "text": "two-piece sleeve"}],
	  "callouts": [{"feature": "storm flap", "details": "single layer", "dimensions": "80 mm"}],
	  "bom": [{"section": "hardware", "kind": "zipper", "name": "front zip"}],
	  "missing": ["picture 3 — the cuff"]
	}`
	first, _, err := parseConstructionDraft(raw, "stop")
	require.NoError(t, err)

	stored, err := designMarshalJSON(first)
	require.NoError(t, err)

	back := designConstructionDraftFromRun(string(stored))
	require.NotNil(t, back, "сохранённый черновик обязан читаться обратно: иначе повтор отдаёт пустоту")
	require.Equal(t, first.GetSilhouette(), back.GetSilhouette())
	require.Equal(t, first.GetFit(), back.GetFit())
	require.Equal(t, "sleeveCuff", back.GetAspects()[0].GetKey())
	require.Equal(t, first.GetCallouts()[0].GetDimensions(), back.GetCallouts()[0].GetDimensions())
	require.Equal(t, pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE, back.GetBom()[0].GetSection())
	require.Equal(t, pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_ZIPPER, back.GetBom()[0].GetKind())
	require.Equal(t, first.GetMissing(), back.GetMissing())
}

// ПРОЗА ИЗ СТАРОГО ПРОГОНА НЕ ПРИТВОРЯЕТСЯ ЧЕРНОВИКОМ.
//
// Именно так поле `construction` отличает структурный прогон от прозаического: вопрос задаётся
// САМОЙ СТРОКЕ, а не флагу запроса, потому что прогон отвечен один раз и навсегда в той форме,
// в какой был отвечен.
func TestConstructionDraftFromRunIgnoresProse(t *testing.T) {
	require.Nil(t, designConstructionDraftFromRun(""))
	require.Nil(t, designConstructionDraftFromRun("DESCRIPTION\nA boxy coat.\n\nDESIGN ASPECTS\n- storm flap"))
	require.Nil(t, designConstructionDraftFromRun("{}"))
}

// ─────────────────────────── ПРОМПТ ───────────────────────────

// ПОЛЬЗОВАТЕЛЬСКИЙ ПРОМПТ НЕСЁТ ВСЕ ПЯТЬ СЕКЦИЙ, И ПРИВЯЗКА В НЁМ — ЧУЖИМИ СТРОКАМИ.
func TestConstructionUserPromptCarriesEverySection(t *testing.T) {
	card := draftBindCard()
	card.TargetGender = sql.NullString{String: "unisex", Valid: true}
	card.Details = []entity.TechCardDetail{{
		Key:  sql.NullString{String: "collar", Valid: true},
		Text: sql.NullString{String: "ALREADY-collar-band", Valid: true},
	}}
	card.Callouts = append(card.Callouts, entity.TechCardCallout{
		Number:      7,
		Part:        sql.NullString{String: "hem", Valid: true},
		Description: sql.NullString{String: "ALREADY-hem-row", Valid: true},
	})
	card.BomItems = []entity.TechCardBomItem{{
		Section:     entity.BomSectionFabric,
		Name:        "ALREADY-main-cloth",
		Composition: sql.NullString{String: "100% wool", Valid: true},
	}}

	mood := designMoodSnapshot(card)
	require.NotNil(t, mood)
	prompt := designConstructionUserPrompt(card, mood, []int{11, 22, 33})

	// 1 — шапка изделия.
	require.Contains(t, prompt, "Garment: bind subject")
	require.Contains(t, prompt, "Gender: unisex")
	// 2 — слова дизайнера.
	require.Contains(t, prompt, "CONCEPT-the-one-note")
	// 3 — ПРИВЯЗКА, собранная общей функцией: номер картинки и место на ней.
	require.Contains(t, prompt, "picture 2")
	require.Contains(t, prompt, "PINWORDS-collar-roll")
	require.Contains(t, prompt, "% from the left")
	// 4 — что карточка уже говорит. Три вида строк, каждый со своим маркером.
	require.Contains(t, prompt, "Already on the card")
	require.Contains(t, prompt, "collar: ALREADY-collar-band")
	require.Contains(t, prompt, "#7")
	require.Contains(t, prompt, "ALREADY-hem-row")
	require.Contains(t, prompt, "fabric · ALREADY-main-cloth · 100% wool")
	// 5 — токены закрытых словарей.
	require.Contains(t, prompt, "sleeveCuff")
	require.Contains(t, prompt, "bom sections: fabric, lining")
	require.Contains(t, prompt, "hook_and_bar")
	require.Contains(t, prompt, "fit: regular, slim")
	require.Contains(t, prompt, "never invent an article id")
}

// ВЫНОСКА ДОСКИ НЕ ПОПАДАЕТ В «УЖЕ НА КАРТОЧКЕ».
//
// Она уже уехала секцией «записки на картинках»; второй раз она приехала бы как «уже сказано», то
// есть велела бы модели молчать ровно о том, что она и должна прочитать.
func TestConstructionUserPromptDoesNotEchoBoardCallouts(t *testing.T) {
	card := draftBindCard()
	prompt := designConstructionUserPrompt(card, designMoodSnapshot(card), []int{11, 22, 33})
	require.Contains(t, prompt, "PINWORDS-collar-roll")
	require.NotContains(t, prompt, "- callout: ", "выноска доски не смеет прийти дважды")
}

// ПУСТОЙ ЗАМЫСЕЛ — ОДНА СТРОКА-РАЗРЕШЕНИЕ; ЗАПОЛНЕННЫЙ — ОДНА СТРОКА-ЗАПРЕТ.
//
// Условие про ЭТУ карточку, поэтому оно и стоит в пользовательском промпте, а не в роли: роль
// одна на все карточки.
func TestConstructionUserPromptGatesTheConceptRow(t *testing.T) {
	card := draftBindCard()
	require.Contains(t, designConstructionUserPrompt(card, designMoodSnapshot(card), []int{11, 22, 33}),
		"already has a concept")

	bare := &entity.TechCard{}
	bare.Name = "wordless"
	bare.Media = []entity.TechCardMediaItem{{MediaId: 11, Category: entity.TechCardMediaCategoryMoodboard}}
	require.Contains(t, designConstructionUserPrompt(bare, &pb_common.DesignMoodSnapshot{}, []int{11}),
		"propose one in \"concept\"")
}

// СИСТЕМНАЯ РОЛЬ НАЗЫВАЕТ ФОРМУ ОТВЕТА И ЗАПРЕТ ВЫДУМЫВАТЬ, И НЕ ТРОГАЕТ СТАРУЮ.
func TestConstructionSystemPromptAsksForOneJSONObject(t *testing.T) {
	require.Contains(t, designConstructionSystemPrompt, "ONE JSON object")
	require.Contains(t, designConstructionSystemPrompt, "Never invent")
	require.Contains(t, designConstructionSystemPrompt, "\"callouts\"")
	require.NotEqual(t, draftIdeaSystemPrompt, designConstructionSystemPrompt)
	// СТАРАЯ РОЛЬ — КОНТРАКТ С РАБОТАЮЩИМ КЛИЕНТОМ: три заголовка, по которым он режет ответ.
	require.Contains(t, draftIdeaSystemPrompt, "DESCRIPTION")
	require.Contains(t, draftIdeaSystemPrompt, "DESIGN ASPECTS")
	require.Contains(t, draftIdeaSystemPrompt, "MISSING CALLOUTS")
}

// ─────────────────────────── ХЕНДЛЕР ───────────────────────────

// draftConstructionRequest — тот же запрос, что draftRequest(), но с поднятым флагом.
func draftConstructionRequest() *pb_admin.DraftDesignIdeaRequest {
	req := draftRequest()
	req.Construction = true
	return req
}

// constructionAnswer — правдоподобный ответ модели на структурный вопрос.
const constructionAnswer = `{"silhouette":"Sleeveless V-neck tank top","fabric":"Stretch knit jersey",
 "fit":"regular","aspects":[{"key":"collar","text":"V-neck, self-fabric binding"}],
 "callouts":[{"feature":"neck binding","details":"self-fabric, folded","dimensions":"1 cm"}],
 "bom":[{"section":"fabric","purpose":"main","name":"main fabric"}],
 "missing":["picture 1 — the shoulder"]}`

// ПОДНЯТЫЙ ФЛАГ МЕНЯЕТ ВЕСЬ ДОГОВОР С МОДЕЛЬЮ РАЗОМ: роль, промпт, json-режим и потолок токенов.
//
// Порознь эти четыре настройки дают ответ, который формально пришёл и содержательно наполовину;
// поэтому и проверяются вместе, по БАЙТАМ ЗАПРОСА, а не по намерению кода.
func TestDraftDesignIdeaConstructionChangesTheWholeContract(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, constructionAnswer)
	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.NoError(t, err)

	var body struct {
		MaxTokens      int `json:"max_tokens"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(rig.stub.body), &body))
	require.Equal(t, designConstructionMaxTokens, body.MaxTokens, "без потолка обрезанный ответ не ловится")
	require.NotNil(t, body.ResponseFormat)
	require.Equal(t, "json_object", body.ResponseFormat.Type)

	var systemTurn, userTurn string
	for _, m := range body.Messages {
		text, _ := orContent(t, m.Content)
		switch m.Role {
		case "system":
			systemTurn = text
		case "user":
			userTurn = text
		}
	}
	require.Equal(t, designConstructionSystemPrompt, systemTurn)
	require.Contains(t, userTurn, "Tokens — use these spellings exactly")
	// КАРТИНКИ ДОСКИ ПО-ПРЕЖНЕМУ ЕДУТ: структурный вопрос задаётся ПО НИМ, а не вместо них.
	require.Equal(t, []string{designBoardMediaURL}, rig.stub.imageURLs(t))

	// ОТВЕТ РАЗОБРАН И ОТДАН КЛИЕНТОМ, А В СТРОКУ УЕХАЛ КАНОНИЧЕСКИЙ JSON.
	require.NotNil(t, resp.GetConstruction())
	require.Equal(t, "Sleeveless V-neck tank top", resp.GetConstruction().GetSilhouette())
	require.NotEqual(t, constructionAnswer, rig.completedText,
		"в строку обязан уехать ПРОВЕРЕННЫЙ канонический JSON, а не ответ модели дословно")
	stored := designConstructionDraftFromRun(rig.completedText)
	require.NotNil(t, stored, "сохранённое обязано читаться обратно тем же разбором")
	require.Equal(t, resp.GetConstruction().GetSilhouette(), stored.GetSilhouette())
	require.Empty(t, rig.failed)
}

// СТАРЫЙ ЗАПРОС — СТАРЫЕ БАЙТЫ, ДОСЛОВНО.
//
// Это половина обещания «old clients are unchanged», и меряется она там, где ложь была бы не
// видна: в теле HTTP-запроса к поставщику.
func TestDraftDesignIdeaWithoutTheFlagSendsTheOldBytes(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "A boxy coat with a storm flap.")
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.NoError(t, err)

	var body struct {
		MaxTokens      int             `json:"max_tokens"`
		ResponseFormat json.RawMessage `json:"response_format"`
		Messages       []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(rig.stub.body), &body))
	require.Zero(t, body.MaxTokens, "у прозы потолка не было и быть не должно")
	require.Empty(t, body.ResponseFormat, "у прозы json-режима не было и быть не должно")

	card := designMoodCard()
	want := designDraftIdeaPrompt(card, designMoodSnapshot(card), []int{designBoardMediaID})
	var systemTurn, userTurn string
	for _, m := range body.Messages {
		text, _ := orContent(t, m.Content)
		switch m.Role {
		case "system":
			systemTurn = text
		case "user":
			userTurn = text
		}
	}
	require.Equal(t, draftIdeaSystemPrompt, systemTurn)
	require.Equal(t, want, userTurn)
}

// ОТВЕТ НЕ ТОЙ ФОРМЫ РОНЯЕТ ПРОГОН — И ПОПЫТКА ЗАКРЫВАЕТСЯ ОПЛАЧЕННОЙ.
//
// Вызов состоялся, картинки уехали, входные токены посчитаны. Списать ноль значило бы сделать
// «модель ответила прозой» бесплатным способом жечь бюджет.
func TestDraftDesignIdeaRefusesAnAnswerOutOfShapeAndStillPays(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "DESCRIPTION\nA boxy coat, no JSON in sight.")
	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.Error(t, err)

	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, designConstructionReasonInvalidOutput, md["reason"],
		"клиент отличает «не та форма» от провала поставщика по машинной причине, а не по прозе")
	require.Contains(t, err.Error(), "did not answer in the shape asked for")

	require.Len(t, rig.failed, 1)
	require.Equal(t, designConstructionReasonInvalidOutput, rig.failed[0].ErrorCode,
		"«ответ был не той формы» и «ответа не было» — разные новости, и различает их колонка")
	require.Len(t, rig.finished, 1)
	require.Equal(t, entity.DesignAttemptFailed, rig.finished[0].State)
	require.True(t, rig.finished[0].Price.Valid, "оплаченный вызов обязан быть виден в регистре")
	require.Equal(t, designDraftIdeaEstimate(1).Decimal.String(), rig.finished[0].Price.Decimal.String())
	require.Empty(t, rig.completedText, "прогон провален — закрывать его нечем")
}

// ПОВТОР ОТДАЁТ ТОТ ЖЕ ЧЕРНОВИК, ПЕРЕСОБРАННЫЙ ИЗ СТРОКИ, И МОДЕЛЬ НЕ ЗОВЁТ.
//
// Это тот самый жест, ради которого заведена идемпотентность, и ровно то место, где фича
// сломалась бы молча: ответ есть, деньги списаны один раз, а предложение пустое.
func TestDraftDesignIdeaReplayRebuildsTheDraftFromTheStoredRun(t *testing.T) {
	// Клиент настроен, но смотрит в закрытый порт: повтор обязан не звонить вовсе, и попытка
	// позвонить провалилась бы громко, а не молча зазеленела.
	rig := newDraftIdeaRig(t, openrouter.New(openrouter.Config{
		APIKey: "test-key", BaseURL: "http://127.0.0.1:1",
	}))

	canonical, err := designMarshalJSON(&pb_common.DesignConstructionDraft{
		Silhouette: "Sleeveless V-neck tank top",
		Aspects:    []*pb_common.DesignConstructionAspect{{Key: "collar", Text: "V-neck binding"}},
		Bom: []*pb_common.DesignConstructionBomLine{{
			Name: "main fabric", Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		}},
	})
	require.NoError(t, err)

	prior := entity.DesignRun{
		Id: 900, TechCardId: designRunCardID, Kind: entity.DesignRunKindDraftIdea,
		Status:     entity.DesignRunDone,
		OutputText: sql.NullString{String: string(canonical), Valid: true},
	}
	rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
		Return(&entity.DesignRunStarted{Run: prior, Idempotent: true, Resumed: false}, nil).Once()

	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.NoError(t, err)
	require.NotNil(t, resp.GetConstruction(),
		"повтор без пересборки вернул бы пустое предложение при оплаченном и успешном прогоне")
	require.Equal(t, "Sleeveless V-neck tank top", resp.GetConstruction().GetSilhouette())
	require.Equal(t, "collar", resp.GetConstruction().GetAspects()[0].GetKey())
	require.Equal(t, pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		resp.GetConstruction().GetBom()[0].GetSection())
}
