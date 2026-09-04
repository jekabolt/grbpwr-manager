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
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
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
	// ⚠ ОДИН СЧЁТЧИК ЗДЕСЬ ЗАКОННО НЕ НОЛЬ, И ЭТО КРУГ 20. Фикстура — ФОРМА ОТВЕТА ДО B-13, вместе
	// с выноской: она держит обещание «сохранённый прогон читается обратно». Промпт выносок больше
	// не просит, поэтому принятая строка считается как CalloutsUnasked — «модель ответила на
	// незаданный вопрос». Остальные поправки обязаны быть нулём: это положительный контроль.
	require.Equal(t, 1, stats.CalloutsUnasked)
	clean := stats
	clean.CalloutsUnasked = 0
	require.False(t, clean.Any(), "чистый ответ не нуждается ни в одной поправке: %+v", stats)

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
	prompt := designConstructionUserPrompt(card, mood, []int{11, 22, 33}, draftProbeColours())

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
	prompt := designConstructionUserPrompt(card, designMoodSnapshot(card), []int{11, 22, 33}, draftProbeColours())
	require.Contains(t, prompt, "PINWORDS-collar-roll")
	require.NotContains(t, prompt, "- callout: ", "выноска доски не смеет прийти дважды")
}

// ПУСТОЙ ЗАМЫСЕЛ — ОДНА СТРОКА-РАЗРЕШЕНИЕ; ЗАПОЛНЕННЫЙ — ОДНА СТРОКА-ЗАПРЕТ.
//
// Условие про ЭТУ карточку, поэтому оно и стоит в пользовательском промпте, а не в роли: роль
// одна на все карточки.
func TestConstructionUserPromptGatesTheConceptRow(t *testing.T) {
	card := draftBindCard()
	require.Contains(t, designConstructionUserPrompt(card, designMoodSnapshot(card), []int{11, 22, 33}, draftProbeColours()),
		"already has a concept")

	bare := &entity.TechCard{}
	bare.Name = "wordless"
	bare.Media = []entity.TechCardMediaItem{{MediaId: 11, Category: entity.TechCardMediaCategoryMoodboard}}
	require.Contains(t, designConstructionUserPrompt(bare, &pb_common.DesignMoodSnapshot{}, []int{11}, draftProbeColours()),
		"propose one in \"concept\"")
}

// СИСТЕМНАЯ РОЛЬ НАЗЫВАЕТ ФОРМУ ОТВЕТА И ЗАПРЕТ ВЫДУМЫВАТЬ, И НЕ ТРОГАЕТ СТАРУЮ.
func TestConstructionSystemPromptAsksForOneJSONObject(t *testing.T) {
	require.Contains(t, designConstructionSystemPrompt, "ONE JSON object")
	require.Contains(t, designConstructionSystemPrompt, "Never invent")
	// ⚠ ВЫНОСОК РОЛЬ БОЛЬШЕ НЕ ПРОСИТ ВОВСЕ (B-13) — ни в форме ответа, ни в правилах. Слово
	// «callouts» здесь и есть предмет проверки: пока оно в промпте, мы платим выходными токенами
	// за строки, которых никто не рисует.
	require.NotContains(t, designConstructionSystemPrompt, "callouts",
		"промпт не имеет права просить выноски (B-13)")
	// А ФАКТЫ, РАДИ КОТОРЫХ ИХ ПРОСИЛИ, НИКУДА НЕ ДЕЛИСЬ: правило 3 переписано на аспекты.
	require.Contains(t, designConstructionSystemPrompt, "go into \"aspects\"")
	require.Contains(t, designConstructionSystemPrompt, "seams, closures, edges, pockets, bindings")
	// И РОЛЬ СПРАШИВАЕТ КОЛОРВЕИ (B-25).
	require.Contains(t, designConstructionSystemPrompt, "\"colourways\"")
	require.Contains(t, designConstructionSystemPrompt, "\"color_code\"")
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

// ═══════════════════ ПОЧИНКИ ПО АДВЕРСАРНОМУ РЕВЬЮ КРУГА 19 ═══════════════════
//
// Каждая проба ниже КРАСНЕЛА БЫ НА КОММИТЕ 553926c. Пять из них меряют деньги или отказ в
// сохранении карточки, поэтому у каждой названо, что именно ломалось.

// ПОТОЛОК ТОКЕНОВ ОБЯЗАН ЕХАТЬ ВМЕСТЕ С ВЫКЛЮЧЕННЫМ МЫШЛЕНИЕМ — И МЕРЯЕТСЯ ЭТО В БАЙТАХ ЗАПРОСА.
//
// Слуг — думающая модель; рассуждения биллятся и тратятся ИЗ бюджета завершения ДО ответа, поэтому
// потолок без `reasoning:{"effort":"none"}` покупает размышление вместо ответа (замер соседа: 2500
// токенов, ноль контента, 42 с, ~$0.11). Вторая половина пробы не менее несущая: у прозы потолка
// нет, значит и поля быть не должно — её байты это контракт со старым клиентом.
func TestDraftDesignIdeaTurnsReasoningOffWhereverItCapsTheAnswer(t *testing.T) {
	readReasoning := func(t *testing.T, body string) (present bool, effort string) {
		t.Helper()
		var req struct {
			MaxTokens int `json:"max_tokens"`
			Reasoning *struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		if req.Reasoning == nil {
			return false, ""
		}
		return true, req.Reasoning.Effort
	}

	t.Run("структурный ответ: потолок стоит — мышление выключено", func(t *testing.T) {
		rig := newDraftRig(t, http.StatusOK, constructionAnswer)
		_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
		require.NoError(t, err)
		present, effort := readReasoning(t, rig.stub.body)
		require.True(t, present,
			"потолок 3000 без выключенного мышления оплачивает размышление и отдаёт пустоту")
		require.Equal(t, "none", effort)
	})

	t.Run("проза: потолка нет — поля нет", func(t *testing.T) {
		rig := newDraftRig(t, http.StatusOK, "A boxy coat with a storm flap.")
		_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftRequest())
		require.NoError(t, err)
		present, _ := readReasoning(t, rig.stub.body)
		require.False(t, present, "у старого запроса байты не менялись и меняться не должны")
	})
}

// ПОТОЛОК, СЪЕДЕННЫЙ БЕЗ ОТВЕТА, — СВОЙ КОД ПРИЧИНЫ И ОПЛАЧЕННАЯ ПОПЫТКА.
//
// ⚠ ЭТО ПРО ДЕНЬГИ. Тот же потолок даёт два исхода: половина ответа (`invalid_output`) платится
// оценкой, а полное отсутствие ответа платилось НУЛЁМ — то есть дешевле выглядел ХУДШИЙ из двух, и
// «жечь бюджет бесплатно» было способом. Токены поставщик напечатал в usage в обоих случаях.
func TestDraftDesignIdeaChargesTheCeilingItAteWithoutAnswering(t *testing.T) {
	rig := newDraftRig(t, http.StatusOK, "")
	rig.stub.raw = `{"choices":[{"message":{"content":""},"finish_reason":"length"}],` +
		`"usage":{"prompt_tokens":1200,"completion_tokens":3000,"total_tokens":4200}}`

	_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.Error(t, err)

	code, md := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Equal(t, designReasonBudgetExhausted, md["reason"],
		"«бюджет ответа съеден» — не «поставщик упал»: чинят их разные люди разными правками")

	require.Len(t, rig.failed, 1)
	require.Equal(t, designReasonBudgetExhausted, rig.failed[0].ErrorCode)
	require.Len(t, rig.finished, 1)
	require.Equal(t, entity.DesignAttemptFailed, rig.finished[0].State)
	require.True(t, rig.finished[0].Price.Valid,
		"картинки уехали и токены завершения потрачены — списать ноль значит спрятать деньги")
	require.Equal(t, designDraftIdeaEstimate(1).Decimal.String(),
		rig.finished[0].Price.Decimal.String(),
		"тот же потолок, тот же счёт: с половиной ответа и без ответа платится одинаково")
}

// ПРОВАЛЕННЫЙ ПРОГОН НА ПОВТОРЕ ОТДАЁТ ОТКАЗ, А НЕ ПУСТОЙ УСПЕХ.
//
// Предикат перехвата резюмирует только `pending|running`, поэтому `failed` — навсегда. Хендлер
// отвечал на него HTTP-OK с `construction: nil` и без ошибки, а проза отказа велит «draft again»:
// человек жал ту же кнопку и получал молчаливую пустоту.
func TestDraftDesignIdeaReplayOfAFailedRunIsARefusalNotAnEmptyOK(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		want string
	}{
		{"не та форма", designConstructionReasonInvalidOutput, "did not answer in the shape asked for"},
		{"бюджет съеден", designReasonBudgetExhausted, "used up the whole answer budget"},
		{"код неизвестен этому файлу", "provider_error", "already failed"},
		{"кода нет вовсе", "", "already failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDraftIdeaRig(t, openrouter.New(openrouter.Config{
				APIKey: "test-key", BaseURL: "http://127.0.0.1:1",
			}))
			prior := entity.DesignRun{
				Id: 901, TechCardId: designRunCardID, Kind: entity.DesignRunKindDraftIdea,
				Status:    entity.DesignRunFailed,
				ErrorCode: sql.NullString{String: tc.code, Valid: tc.code != ""},
			}
			rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
				Return(&entity.DesignRunStarted{Run: prior, Idempotent: true}, nil).Once()

			_, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
			require.Error(t, err, "пустой успех на проваленном прогоне — это тупик, а не ответ")
			code, md := errorReason(t, err)
			require.Equal(t, codes.FailedPrecondition, code)
			require.Contains(t, err.Error(), tc.want)
			require.NotEmpty(t, md["reason"])
		})
	}
}

// ТОТ ЖЕ КЛЮЧ С ПРОТИВОПОЛОЖНЫМ ФЛАГОМ — ОТКАЗ, А НЕ РЕЗУЛЬТАТ ДРУГОЙ ФОРМЫ.
//
// Флаг в ключ идемпотентности не входит: прогон отвечен один раз и навсегда в той форме, в какой
// был отвечен. Соседняя ось (колорвей) ровно этот случай считает отказом.
func TestDraftDesignIdeaReplayRefusesTheOppositeShape(t *testing.T) {
	canonical, err := designMarshalConstructionDraft(&pb_common.DesignConstructionDraft{
		Silhouette: "Sleeveless V-neck tank top",
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		name        string
		stored      string
		asksForJSON bool
		wantErr     bool
	}{
		{"проза сохранена, просят структуру", "A boxy coat with a storm flap.", true, true},
		{"структура сохранена, просят прозу", string(canonical), false, true},
		{"структура сохранена, просят структуру", string(canonical), true, false},
		{"проза сохранена, просят прозу", "A boxy coat with a storm flap.", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDraftIdeaRig(t, openrouter.New(openrouter.Config{
				APIKey: "test-key", BaseURL: "http://127.0.0.1:1",
			}))
			prior := entity.DesignRun{
				Id: 902, TechCardId: designRunCardID, Kind: entity.DesignRunKindDraftIdea,
				Status:     entity.DesignRunDone,
				OutputText: sql.NullString{String: tc.stored, Valid: true},
			}
			rig.design.EXPECT().StartRun(mock.Anything, mock.AnythingOfType("entity.DesignRunStart")).
				Return(&entity.DesignRunStarted{Run: prior, Idempotent: true}, nil).Once()

			req := draftRequest()
			req.Construction = tc.asksForJSON
			resp, err := rig.srv.DraftDesignIdea(designRunCtx(), req)
			if !tc.wantErr {
				require.NoError(t, err)
				require.Equal(t, tc.asksForJSON, resp.GetConstruction() != nil)
				return
			}
			require.Error(t, err)
			_, md := errorReason(t, err)
			require.Equal(t, designReasonShapeMismatch, md["reason"])
		})
	}
}

// КРУГОВОЙ ОБХОД ХРАНЕНИЯ ДЕРЖИТСЯ НА ЛЮБОЙ ФОРМЕ ОТВЕТА, ВКЛЮЧАЯ ЗАМЕРЕННУЮ РЕВЬЮ.
//
// ⚠ ЭТО ПРОБА ПРО ОПЛАЧЕННЫЙ ДВОЙНОЙ КЛИК. Ревью круга 19 ЗАМЕРИЛО: черновик, содержательный одним
// лишь `missing`, сохранялся писателем `inputs` как `{"missing":[…]}` — protojson по умолчанию не
// пишет пустых полей, — а читатель требует присутствия хотя бы одного из семи ключей и возвращал
// nil. То есть УСПЕШНЫЙ ОПЛАЧЕННЫЙ прогон на повторе отдавал пустоту, и системный промпт сам ведёт
// к этой форме (правило 1: «оставь поле пустым, назови нехватку в missing»).
//
// ⚠ У КАЖДОГО СЛУЧАЯ ЕСТЬ НЕГАТИВНЫЙ КОНТРОЛЬ — тот же круг ПРЕЖНИМ писателем. Без него проба
// зеленела бы и на разборе, который принимает вообще всё, и не доказывала бы, что чинилась именно
// асимметрия «писатель против читателя».
func TestConstructionDraftRoundTripsEveryShapeIncludingTheEmptyOnes(t *testing.T) {
	full, _, err := parseConstructionDraft(`{
	  "silhouette": "boxy", "fabric": "melton wool", "fit": "relaxed", "concept": "a winter shell",
	  "aspects": [{"key": "sleeve / cuff", "text": "two-piece sleeve"}],
	  "callouts": [{"feature": "storm flap", "details": "single layer", "dimensions": "80 mm"}],
	  "bom": [{"section": "hardware", "kind": "zipper", "name": "front zip", "pantone": "19-4052"}],
	  "missing": ["picture 3 — the cuff"]
	}`, "stop")
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		in   *pb_common.DesignConstructionDraft
		// lossyLosesIt — читал ли ПРЕЖНИЙ писатель эту форму обратно. false = ровно тот дефект.
		lossyLosesIt bool
	}{
		{
			name:         "только missing — случай, замеренный ревью",
			in:           &pb_common.DesignConstructionDraft{Missing: []string{"picture 1 — the shoulder"}},
			lossyLosesIt: true,
		},
		{
			name:         "всё пусто — модель посмотрела, и ей нечего сказать",
			in:           &pb_common.DesignConstructionDraft{},
			lossyLosesIt: true,
		},
		{name: "полный черновик", in: full},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored, err := designMarshalConstructionDraft(tc.in)
			require.NoError(t, err)
			back := designConstructionDraftFromRun(string(stored))
			require.NotNil(t, back, "сохранено %d байт: %s", len(stored), stored)
			require.True(t, proto.Equal(tc.in, back),
				"круг не сошёлся.\nхранилось (%d байт): %s\nпрочитано: %v", len(stored), stored, back)
			t.Logf("%s: сохранено %d байт, прочитано обратно ПОЛЕ В ПОЛЕ РАВНЫМ (proto.Equal)",
				tc.name, len(stored))

			lossy, err := designMarshalJSON(tc.in)
			require.NoError(t, err)
			lossyBack := designConstructionDraftFromRun(string(lossy))
			if tc.lossyLosesIt {
				require.Nil(t, lossyBack,
					"негативный контроль обязан ТЕРЯТЬ эту форму, иначе проба ничего не меряет; "+
						"прежний писатель дал %d байт: %s", len(lossy), lossy)
			} else {
				require.NotNil(t, lossyBack)
			}
		})
	}
}

// ПАРА «СЕКЦИЯ ↔ НАЗНАЧЕНИЕ/ВИД» — ЗДЕСЬ, ПОТОМУ ЧТО ИНАЧЕ ОТКАЗЫВАЕТ СОХРАНЕНИЕ ВСЕЙ КАРТОЧКИ.
//
// ⚠ КАЖДЫЙ ТОКЕН НИЖЕ ЗАКОНЕН САМ ПО СЕБЕ, И ИМЕННО ПОЭТОМУ РАЗБОР ИХ ПРОПУСКАЛ. Сохранение
// требует ПАР (store/techcard/materials.go: назначение только на рулонных, вид только в своей
// домашней секции), а UpsertTechCard — всё-или-ничего: одна предложенная строка спеки отказывала
// в сохранении ВСЕЙ тех-карты, причём поля, которые её сломали, интерфейс предложения не рисует.
func TestParseConstructionDraftClearsImpossibleBomPairs(t *testing.T) {
	const (
		unsetPurpose = pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET
		mainPurpose  = pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_MAIN
		unsetKind    = pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET
		buttonKind   = pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_BUTTON
		otherKind    = pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_OTHER
		bindingKind  = pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_BINDING
	)
	for _, tc := range []struct {
		name        string
		line        string
		wantSection pb_common.TechCardBomSection
		wantPurpose pb_common.TechCardBomPurpose
		wantKind    pb_common.TechCardBomKind
		wantCleared int
	}{{
		name:        "назначение на фурнитуре — снято, вид в своём доме остаётся",
		line:        `{"section":"hardware","purpose":"main","kind":"button","name":"front button"}`,
		wantSection: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE,
		wantPurpose: unsetPurpose, wantKind: buttonKind, wantCleared: 1,
	}, {
		name:        "вид на ткани — снят, назначение на рулонном остаётся",
		line:        `{"section":"fabric","purpose":"main","kind":"button","name":"main cloth"}`,
		wantSection: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		wantPurpose: mainPurpose, wantKind: unsetKind, wantCleared: 1,
	}, {
		name:        "вид не своей секции — снят",
		line:        `{"section":"trim","kind":"button","name":"tape"}`,
		wantSection: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_TRIM,
		wantPurpose: unsetPurpose, wantKind: unsetKind, wantCleared: 1,
	}, {
		name:        "вид своей секции — остаётся",
		line:        `{"section":"trim","kind":"binding","name":"neck binding"}`,
		wantSection: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_TRIM,
		wantPurpose: unsetPurpose, wantKind: bindingKind, wantCleared: 0,
	}, {
		name:        "`other` — запасной выход КАЖДОЙ пригодной семьи",
		line:        `{"section":"packaging","kind":"other","name":"filler"}`,
		wantSection: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_PACKAGING,
		wantPurpose: unsetPurpose, wantKind: otherKind, wantCleared: 0,
	}, {
		name:        "этикетка вид не принимает вовсе — своим словарём владеет label_type",
		line:        `{"section":"label","kind":"other","name":"care label"}`,
		wantSection: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_LABEL,
		wantPurpose: unsetPurpose, wantKind: unsetKind, wantCleared: 1,
	}, {
		name:        "секции нет — не может быть ни назначения, ни вида",
		line:        `{"purpose":"main","kind":"button","name":"something"}`,
		wantSection: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_UNKNOWN,
		wantPurpose: unsetPurpose, wantKind: unsetKind, wantCleared: 2,
	}, {
		name:        "рулонное назначение на рулонной секции — остаётся",
		line:        `{"section":"lining","purpose":"lining","name":"cupro"}`,
		wantSection: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_LINING,
		wantPurpose: pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_LINING,
		wantKind:    unsetKind, wantCleared: 0,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, stats, err := parseConstructionDraft(`{"bom":[`+tc.line+`]}`, "stop")
			require.NoError(t, err, "невозможная пара роняет ТОКЕН, а не оплаченный прогон")
			require.Len(t, got.GetBom(), 1, "строка обязана уцелеть: технологу нужнее строка без токена")
			require.Equal(t, tc.wantSection, got.GetBom()[0].GetSection())
			require.Equal(t, tc.wantPurpose, got.GetBom()[0].GetPurpose())
			require.Equal(t, tc.wantKind, got.GetBom()[0].GetKind())
			require.Equal(t, tc.wantCleared, stats.PairsCleared,
				"снятая пара обязана быть ВИДНА в логе: иначе «ответ чистый» врёт")
		})
	}
}

// ПОТОЛКИ СОВПАДАЮТ С КОЛОНКАМИ, И СЧИТАЮТ ОНИ БАЙТЫ.
//
// ⚠ ЕДИНИЦЫ — ВЕСЬ СМЫСЛ ПРОБЫ. Сторожа DTO дальше по маршруту меряют `len()`, то есть БАЙТЫ, и
// колонка меряет байты; потолок в 500 рун пропускал ~85 кириллических рун сверх varchar(255), а у
// `pantone` (varchar(64), 0363) сторожа не было ВООБЩЕ — приезжал сырой MySQL 1406, не называющий
// ни строки, ни поля. Поэтому каждый случай ниже кириллический: на латинице дефект невидим.
func TestParseConstructionDraftCapsEveryValueByItsColumn(t *testing.T) {
	long := strings.Repeat("я", 400) // 400 рун = 800 байт
	raw := `{"aspects":[{"key":"` + long + `","text":"текст"}],
	  "callouts":[{"feature":"` + long + `","details":"ок","dimensions":"` + long + `"}],
	  "bom":[{"section":"fabric","name":"` + long + `","composition":"` + long + `",
	          "colour":"` + long + `","pantone":"` + long + `"}]}`

	got, stats, err := parseConstructionDraft(raw, "stop")
	require.NoError(t, err)

	for _, tc := range []struct {
		field    string
		value    string
		maxBytes int
	}{
		{"aspects[0].key → tech_card_detail.detail_key varchar(64)", got.GetAspects()[0].GetKey(), designConstructionMaxVarchar64},
		{"callouts[0].feature → tech_card_callout.part varchar(255)", got.GetCallouts()[0].GetFeature(), designConstructionMaxVarchar255},
		{"callouts[0].dimensions → varchar(255)", got.GetCallouts()[0].GetDimensions(), designConstructionMaxVarchar255},
		{"bom[0].name → varchar(255)", got.GetBom()[0].GetName(), designConstructionMaxVarchar255},
		{"bom[0].composition → varchar(255)", got.GetBom()[0].GetComposition(), designConstructionMaxVarchar255},
		{"bom[0].colour → color varchar(255)", got.GetBom()[0].GetColour(), designConstructionMaxVarchar255},
		{"bom[0].pantone → varchar(64) (0363)", got.GetBom()[0].GetPantone(), designConstructionMaxVarchar64},
	} {
		t.Run(tc.field, func(t *testing.T) {
			require.LessOrEqual(t, len(tc.value), tc.maxBytes,
				"%s: %d БАЙТ при потолке %d — сторож DTO меряет len(), и это отказ в сохранении ВСЕЙ карточки",
				tc.field, len(tc.value), tc.maxBytes)
			require.True(t, utf8.ValidString(tc.value),
				"резать посреди руны — это MySQL 1366 вместо обрезанного слова")
			require.NotEmpty(t, tc.value, "обрезка не имеет права съедать значение целиком")
		})
	}
	require.Positive(t, stats.Truncated, "обрезка обязана быть видна в логе")

	// ⚠ ПОЛЯ КОЛОНКИ TEXT ОСТАЮТСЯ ПОД ПОТОЛКОМ РУН: там ограничение смысловое, а не про колонку.
	longer, _, err := parseConstructionDraft(
		`{"silhouette":"`+strings.Repeat("я", designConstructionMaxLongRunes+50)+`"}`, "stop")
	require.NoError(t, err)
	require.Equal(t, designConstructionMaxLongRunes, utf8.RuneCountInString(longer.GetSilhouette()))
}

// НЕСКАЛЯР НА МЕСТЕ СКАЛЯРА — ЭТО ПУСТО, А НЕ ЗНАЧЕНИЕ СО СКОБКАМИ.
//
// Прежняя ветка «что-то ещё скалярное — берётся как написано» брала ЛЮБОЙ токен байтами, и
// технологу предлагалось вписать в поле силуэта строку `{"top":"tank","bottom":"none"}`.
// Статистика при этом была нулевой, а лог — Info, то есть «ответ чистый».
func TestParseConstructionDraftRefusesToQuoteObjectsAsValues(t *testing.T) {
	got, stats, err := parseConstructionDraft(`{
	  "silhouette": {"top":"tank","bottom":"none"},
	  "fabric": ["jersey","rib"],
	  "aspects": [{"key":"collar","text":{"a":1}}],
	  "bom": [{"section":"fabric","name":"main cloth","colour":{"hex":"#000"}}]
	}`, "stop")
	require.NoError(t, err, "форма ответа цела — ронять оплаченный прогон не за что")
	require.Empty(t, got.GetSilhouette(), "объект — не текст, который человек согласится вписать")
	require.Empty(t, got.GetFabric())
	require.Empty(t, got.GetAspects(), "аспект без текста не строка")
	require.Len(t, got.GetBom(), 1)
	require.Empty(t, got.GetBom()[0].GetColour())
	require.Equal(t, 4, stats.NonScalars,
		"«модель отвечает объектами» — это факт про промпт, и узнать его можно только из лога")
}

// ИДЕАЛЬНО ОФОРМЛЕННЫЙ ОТВЕТ СО ВСЕМИ null — НЕ `invalid_output`, А ЗАКОННЫЙ ПУСТОЙ ЧЕРНОВИК.
//
// ⚠ ЭТО ПРО ДЕНЬГИ: такой ответ ронял ВЕСЬ оплаченный прогон. Присутствие ключа спрашивается у
// карты сырых кусков, а не у указателя после разбора, — json.Unmarshal кладёт nil в указатель и на
// `null`, схлопывая «ключ не написан» с «ключ написан пустым».
func TestParseConstructionDraftAcceptsAnAllNullAnswerAndDropsOnlyBrokenFields(t *testing.T) {
	t.Run("все ключи есть, все null", func(t *testing.T) {
		got, _, err := parseConstructionDraft(`{"silhouette":null,"fabric":null,"fit":null,
		  "concept":null,"aspects":null,"callouts":null,"bom":null,"missing":["the cuff"]}`, "stop")
		require.NoError(t, err, "«тут ничего» — обычный способ модели ответить, и он оплачен")
		require.Empty(t, got.GetSilhouette())
		require.Equal(t, []string{"the cuff"}, got.GetMissing())
	})

	t.Run("несовпадение типа роняет ПОЛЕ, а не оплаченный ответ", func(t *testing.T) {
		got, stats, err := parseConstructionDraft(`{
		  "silhouette":"boxy", "aspects":"none",
		  "bom":[{"section":"fabric","name":"main cloth"}, "not a line",
		         {"section":"hardware","kind":"button","name":"front button"}]}`, "stop")
		require.NoError(t, err)
		require.Equal(t, "boxy", got.GetSilhouette(), "шесть целых полей не уезжают за одно кривое")
		require.Empty(t, got.GetAspects())
		require.Len(t, got.GetBom(), 2, "одна кривая строка спеки не уносит соседние")
		require.Equal(t, 2, stats.FieldsDropped)
	})

	t.Run("ни одного из семи ключей — по-прежнему отказ", func(t *testing.T) {
		_, _, err := parseConstructionDraft(`{"missing":["the cuff"]}`, "stop")
		require.Error(t, err, "ответ из одного совета не отвечает на заданный вопрос")
	})
}

// СТРОГОЕ ЧТЕНИЕ СОХРАНЁННОЙ СТРОКИ: ПРОЗА С ФИГУРНЫМИ СКОБКАМИ — НЕ ЧЕРНОВИК.
//
// Живой ответ модели терпит ограду и прозу вокруг объекта (так же, как разбор тех-карты), и это
// правильно. Здесь читается НАШ СОБСТВЕННЫЙ канонический JSON, всегда объект целиком, — а прежняя
// терпимость превращала прозаический прогон в выдуманное предложение, которого никто не делал.
func TestConstructionDraftFromRunRefusesProseThatMerelyContainsBraces(t *testing.T) {
	for _, prose := range []string{
		`Use a {"fabric": "jersey"} weight.`,
		"DESCRIPTION\nA boxy coat.\n```json\n{\"fabric\":\"melton\"}\n```",
		`  {"fabric":"melton"} and then some prose`,
	} {
		require.Nil(t, designConstructionDraftFromRun(prose),
			"на прогоне, который структурного ответа не просил, предложение выдумывать нечем: %q", prose)
	}
	// Положительный контроль: наш собственный канонический JSON читается.
	canonical, err := designMarshalConstructionDraft(&pb_common.DesignConstructionDraft{Fabric: "melton"})
	require.NoError(t, err)
	require.NotNil(t, designConstructionDraftFromRun("  "+string(canonical)+"\n"))
}

// СЧЁТЧИК САМОДЕЛЬНЫХ КЛЮЧЕЙ СЧИТАЕТ ПРИНЯТЫЕ СТРОКИ, А НЕ УВИДЕННЫЕ.
//
// Он стоял выше дедупа и потолка и печатал 40 там, где технологу предложено 10, — лог отвечал на
// вопрос, которого никто не задавал.
func TestParseConstructionDraftCountsOnlyTheCustomAspectsItKept(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"aspects":[`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"key":"custom%d","text":"t"}`, i)
	}
	b.WriteString(`]}`)

	got, stats, err := parseConstructionDraft(b.String(), "stop")
	require.NoError(t, err)
	require.Len(t, got.GetAspects(), designConstructionMaxAspects)
	require.Equal(t, designConstructionMaxAspects, stats.AspectsCustom,
		"счётчик обязан называть число ПРЕДЛОЖЕННЫХ строк")
	require.Equal(t, 30, stats.OverLimit)
}

// СЕКЦИЯ «УЖЕ НА КАРТОЧКЕ» ИМЕЕТ ПОТОЛОК, И ОБРЕЗКА НАЗЫВАЕТ СЕБЯ ВСЛУХ.
//
// Цикл обходил ВСЕ детали, выноски и строки спеки без предела: карточка со ста выносками и
// шестьюдесятью строками спеки добавляла десятки килобайт (~15k входных токенов, ≈$0.045) к
// КАЖДОМУ нажатию — невидимо для оценки, которая считает одни картинки.
func TestConstructionUserPromptCapsWhatTheCardAlreadySays(t *testing.T) {
	card := &entity.TechCard{}
	card.Name = "a very talkative card"
	for i := 0; i < 100; i++ {
		card.Details = append(card.Details, entity.TechCardDetail{
			Key:  sql.NullString{String: fmt.Sprintf("aspect%d", i), Valid: true},
			Text: sql.NullString{String: strings.Repeat("подробность ", 200), Valid: true},
		})
		card.Callouts = append(card.Callouts, entity.TechCardCallout{
			Number: i + 1,
			Part:   sql.NullString{String: strings.Repeat("узел ", 200), Valid: true},
		})
		card.BomItems = append(card.BomItems, entity.TechCardBomItem{
			Section: entity.BomSectionFabric,
			Name:    strings.Repeat("материал ", 200),
		})
	}

	already := designCardAlreadySays(card)
	require.LessOrEqual(t, len(already), designConstructionMaxAlreadyBytes+512,
		"секция «уже на карточке» — входные токены КАЖДОГО нажатия: %d байт", len(already))
	require.Contains(t, already, "more aspects on the card, not listed",
		"молча показав половину, мы велели бы модели молчать о том, чего не показали")
	require.Contains(t, already, "not listed")
	t.Logf("100+100+100 громких строк карточки ужались до %d байт", len(already))

	// Положительный контроль: короткая карточка по-прежнему едет ЦЕЛИКОМ и без хвостов.
	small := &entity.TechCard{}
	small.Details = []entity.TechCardDetail{{
		Key: sql.NullString{String: "collar", Valid: true}, Text: sql.NullString{String: "notch", Valid: true},
	}}
	quiet := designCardAlreadySays(small)
	require.Equal(t, "- collar: notch\n", quiet)
}

// СТОРОЖ ПУСТОТЫ СПРАШИВАЕТ ТО, ЧТО ДОЕДЕТ ДО МОДЕЛИ, А НЕ ТО, ЧТО ЛЕЖИТ НА ДОСКЕ.
//
// Снимок бывает НЕПУСТЫМ от одной только выноски, а сборка промпта выбрасывает выноску, чья
// картинка не уехала. Доска из трёх плиток с удалёнными медиа и записками на них проходила дверь и
// покупала вызов, чей промпт — две строки шапки.
func TestDraftDesignIdeaRefusesABoardWhoseWordsTravelWithPicturesThatDidNot(t *testing.T) {
	card := &entity.TechCard{}
	card.Name = "a board of ghosts with notes"
	card.Media = []entity.TechCardMediaItem{
		{MediaId: designBoardMediaID, Category: entity.TechCardMediaCategoryMoodboard},
	}
	// Записки есть — но они приколоты к картинке, чьей строки медиа больше нет.
	card.Callouts = []entity.TechCardCallout{{
		Number:      1,
		Description: sql.NullString{String: "the shoulder seam", Valid: true},
		MediaId:     sql.NullInt32{Int32: designBoardMediaID, Valid: true},
	}}

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
	// СТЕНД БЕЗ ЕДИНОГО ОЖИДАНИЯ StartRun — отказ обязан прийти ДО денег, и строгий мок это меряет.
	srv := &Server{
		repo: repo, designGenerationEnabled: true,
		aiOps: openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: "http://127.0.0.1:1"}),
	}

	_, err := srv.DraftDesignIdea(designRunCtx(), draftRequest())
	require.Error(t, err, "промпт из двух строк шапки — платный вызов ни о чём")
	code, _ := errorReason(t, err)
	require.Equal(t, codes.FailedPrecondition, code)
	require.Contains(t, err.Error(), "there is nothing to read")
}

// ─────────────────────── КРУГ 20: КОЛОРВЕИ И УШЕДШИЕ ВЫНОСКИ ───────────────────────

// draftProbeColours — СЛОВАРЬ ЦВЕТА СТЕНДА. Три кода, из них один с именем, которое модель
// охотнее напишет вместо кода («bone white»), — иначе проба узнавания по имени зеленела бы на
// словаре, где имя и код совпадают.
func draftProbeColours() []entity.Color {
	return []entity.Color{
		{ID: 1, Code: "BLK", Name: "black", Hex: sql.NullString{String: "#000000", Valid: true}},
		{ID: 2, Code: "BON", Name: "bone white", Hex: sql.NullString{String: "#EFE9DC", Valid: true}},
		{ID: 3, Code: "OLV", Name: "olive", Hex: sql.NullString{String: "#5B6236", Valid: true}},
	}
}

// ПРОМПТ ПОКАЗЫВАЕТ СЛОВАРЬ ЦВЕТА — ИЛИ ЧЕСТНО ГОВОРИТ, ЧТО СПИСКА НЕТ.
//
// ⚠ ТРИ ИСХОДА, И ДВА ИЗ НИХ СОВПАДАЮТ НАМЕРЕННО. Пустой словарь и словарь длиннее потолка дают
// одну строку «списка нет», потому что вопрос один: есть ли модели чем выбрать код. Урезанный
// список был бы худшим третьим ответом — модель выбирала бы «ближайший» из набора, который мы
// молча обрезали, и её выбор выглядел бы таким же уверенным, как настоящий.
func TestConstructionUserPromptCarriesTheColourDictionary(t *testing.T) {
	card := draftBindCard()
	mood := designMoodSnapshot(card)

	with := designConstructionUserPrompt(card, mood, []int{11, 22, 33}, draftProbeColours())
	require.Contains(t, with, "colours (code · name · hex):")
	require.Contains(t, with, "BLK · black · #000000")
	require.Contains(t, with, "BON · bone white · #EFE9DC")

	none := designConstructionUserPrompt(card, mood, []int{11, 22, 33}, nil)
	require.Contains(t, none, "no colour list is given; leave \"color_code\" empty")
	require.NotContains(t, none, "colours (code · name · hex):")

	// ЗА ПОТОЛКОМ — ТА ЖЕ СТРОКА, А НЕ ПОЛОВИНА СЛОВАРЯ.
	huge := make([]entity.Color, 0, designConstructionMaxColourRows+1)
	for i := 0; i <= designConstructionMaxColourRows; i++ {
		huge = append(huge, entity.Color{ID: i + 1, Code: fmt.Sprintf("C%02d", i), Name: "colour"})
	}
	over := designConstructionUserPrompt(card, mood, []int{11, 22, 33}, huge)
	require.Contains(t, over, "no colour list is given")
	require.NotContains(t, over, "C42",
		"половина словаря заставила бы модель выбирать «ближайший» из молча обрезанного набора")
}

// РАЗБОР ЧИТАЕТ КОЛОРВЕИ ЦЕЛИКОМ: имя, код, пантон образца, hex и цвет НА СЛОТ.
//
// Положительный контроль всех проверок ниже: без него «неверное выброшено» зеленело бы и на
// разборе, который не принимает вообще ничего.
func TestParseConstructionDraftReadsColourways(t *testing.T) {
	raw := `{
	  "bom": [{"section":"fabric","purpose":"main","name":"main fabric"},
	          {"section":"lining","name":"lining"}],
	  "colourways": [
	    {"name":"Black / Bone","color_code":"BLK","pantone":"19-4005 TCX","hex":"#0A0A0A",
	     "slots":[{"slot":"main fabric","pantone":"19-4005 TCX","hex":"#0A0A0A","colour":"black"},
	              {"slot":"Lining","pantone":"11-0601 TCX","hex":"#F2F0E6","color":"bright white"}]},
	    {"name":"Olive / Sand","colour_code":"olive","pantone":"18-0625 TCX","hex":"#5B6236",
	     "slots":[{"slot":"main fabric","pantone":"18-0625 TCX","colour":"olive"}]}
	  ]}`
	draft, stats, err := parseConstructionDraft(raw, "stop")
	require.NoError(t, err)
	require.Len(t, draft.GetColourways(), 2)

	first := draft.GetColourways()[0]
	require.Equal(t, "Black / Bone", first.GetName())
	require.Equal(t, "BLK", first.GetColorCode())
	require.Equal(t, "19-4005 TCX", first.GetPantone())
	require.Equal(t, "#0A0A0A", first.GetHex())
	require.Len(t, first.GetSlots(), 2)
	require.Equal(t, "main fabric", first.GetSlots()[0].GetSlot())
	require.Equal(t, "black", first.GetSlots()[0].GetColour())
	// COLOR/COLOUR — ОДНО ПОЛЕ, ДВА НАПИСАНИЯ: американское принимается наравне с британским.
	require.Equal(t, "bright white", first.GetSlots()[1].GetColour())
	// COLOR_CODE/COLOUR_CODE — ТОЖЕ.
	require.Equal(t, "olive", draft.GetColourways()[1].GetColorCode(),
		"разбор кода не сверяет — это делает designVerifyColourways")
	require.Zero(t, stats.ColourwaysDropped)
	require.Zero(t, stats.SlotColoursUnbound)
}

// ⚠ ПРОВЕРКА КОДА И ПРИВЯЗКИ СЛОТОВ — ОТДЕЛЬНЫЙ ШАГ, И ОН ДЕЛАЕТ ЧЕТЫРЕ ВЕЩИ.
func TestVerifyColourwaysFoldsCodesAndBindsSlots(t *testing.T) {
	draft, _, err := parseConstructionDraft(`{
	  "bom": [{"name":"main fabric"}],
	  "colourways": [
	    {"name":"Black","color_code":"blk",
	     "slots":[{"slot":"Main Fabric","pantone":"19-4005 TCX","colour":"black"},
	              {"slot":"moon dust","pantone":"11-0601 TCX","colour":"white"}]},
	    {"name":"Bone","color_code":"bone white","slots":[{"slot":"neck binding","colour":"bone"}]},
	    {"name":"Fuchsia","color_code":"NOPE","slots":[{"slot":"main fabric","colour":"pink"}]}
	  ]}`, "stop")
	require.NoError(t, err)

	card := &entity.TechCard{}
	card.BomItems = []entity.TechCardBomItem{{Section: entity.BomSectionTrim, Name: "neck binding"}}

	var stats designConstructionStats
	designVerifyColourways(draft, designBuildColourDictionary(draftProbeColours()),
		designCardSlotFolds(card), &stats)

	require.Len(t, draft.GetColourways(), 3)
	// 1 — КОД УЗНАЁТСЯ ПО СКЛАДКЕ И ПРИВОДИТСЯ К КАНОНУ.
	require.Equal(t, "BLK", draft.GetColourways()[0].GetColorCode())
	// 2 — И ПО ИМЕНИ ЦВЕТА ТОЖЕ: модель охотнее пишет «bone white», чем «BON».
	require.Equal(t, "BON", draft.GetColourways()[1].GetColorCode())
	// 3 — НЕУЗНАННЫЙ КОД ОБНУЛЯЕТСЯ, А СТРОКА ОСТАЁТСЯ: человеку нужнее предложение, у которого
	//     код надо выбрать самому, чем отсутствие предложения.
	require.Equal(t, "", draft.GetColourways()[2].GetColorCode())
	require.Equal(t, 1, stats.ColourCodesUnset)
	// 4 — СЛОТ ПРИВЯЗЫВАЕТСЯ ПО СЛОЖЕННОМУ ИМЕНИ: к строке спеки ЭТОГО ОТВЕТА («Main Fabric»)
	//     или к строке спеки КАРТОЧКИ («neck binding»); неизвестный — выброшен.
	require.Len(t, draft.GetColourways()[0].GetSlots(), 1)
	require.Equal(t, "Main Fabric", draft.GetColourways()[0].GetSlots()[0].GetSlot())
	require.Len(t, draft.GetColourways()[1].GetSlots(), 1)
	require.Equal(t, 1, stats.SlotColoursUnbound, "цвет слота «moon dust» некуда положить")
}

// БЕЗЫМЯННЫЙ КОЛОРВЕЙ СО СЛОТАМИ ПОДПИСЫВАЕТСЯ СЕРВЕРОМ; БЕЗ ИМЕНИ И БЕЗ СЛОТОВ — ВЫБРАСЫВАЕТСЯ.
func TestVerifyColourwaysNamesTheUnnamedAndDropsTheEmpty(t *testing.T) {
	draft, stats, err := parseConstructionDraft(`{
	  "bom": [{"name":"main fabric"}],
	  "colourways": [
	    {"name":"","color_code":"BLK","pantone":"19-4005 TCX"},
	    {"name":"Named","slots":[{"slot":"main fabric","colour":"black"}]},
	    {"name":"","slots":[{"slot":"main fabric","colour":"olive"}]}
	  ]}`, "stop")
	require.NoError(t, err)
	// ПЕРВЫЙ УМЕР УЖЕ В РАЗБОРЕ: ни имени, ни слотов — подтверждать нечего, даже с пантоном.
	require.Equal(t, 1, stats.ColourwaysDropped)
	require.Len(t, draft.GetColourways(), 2)

	designVerifyColourways(draft, designBuildColourDictionary(draftProbeColours()),
		map[string]struct{}{}, &stats)
	require.Len(t, draft.GetColourways(), 2)
	require.Equal(t, "Named", draft.GetColourways()[0].GetName())
	// ПОДПИСЬ ПО ПОРЯДКУ В ОТВЕТЕ, А НЕ ПО СЧЁТУ БЕЗЫМЯННЫХ: «colourway 2» = второе предложение.
	require.Equal(t, "colourway 2", draft.GetColourways()[1].GetName())
}

// ПОТОЛКИ, ДЕДУП И ШЕСТНАДЦАТЕРИЧНЫЙ ЦВЕТ.
func TestParseConstructionDraftBoundsColourways(t *testing.T) {
	t.Run("не шестнадцатеричный hex читается как ПУСТО, а не обрезается", func(t *testing.T) {
		for _, in := range []string{"black", "#ABC", "#GGGGGG", "19-4005 TCX", "#0A0A0A0"} {
			require.Equal(t, "", designHexColour(in), in)
		}
		require.Equal(t, "#0a0A0f", designHexColour(" #0a0A0f "))
	})

	t.Run("пятый колорвей и шестнадцатый цвет не влезают", func(t *testing.T) {
		var b strings.Builder
		b.WriteString(`{"bom":[`)
		for i := 0; i < 20; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"name":"slot %d"}`, i)
		}
		b.WriteString(`],"colourways":[`)
		for i := 0; i < 6; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"name":"cw %d","slots":[`, i)
			for j := 0; j < 20; j++ {
				if j > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"slot":"slot %d","colour":"c"}`, j)
			}
			b.WriteString(`]}`)
		}
		b.WriteString(`]}`)

		draft, stats, err := parseConstructionDraft(b.String(), "stop")
		require.NoError(t, err)
		require.Len(t, draft.GetColourways(), designConstructionMaxColourways)
		for _, cw := range draft.GetColourways() {
			require.Len(t, cw.GetSlots(), designConstructionMaxColourwaySlots)
		}
		require.Positive(t, stats.OverLimit)
	})

	t.Run("один слот дважды — это один слот", func(t *testing.T) {
		draft, stats, err := parseConstructionDraft(`{"bom":[{"name":"main fabric"}],
		  "colourways":[{"name":"A","slots":[
		    {"slot":"main fabric","colour":"black"},
		    {"slot":"Main / Fabric","colour":"grey"}]}]}`, "stop")
		require.NoError(t, err)
		require.Len(t, draft.GetColourways()[0].GetSlots(), 1)
		require.Positive(t, stats.Deduped)
	})

	t.Run("не-список на месте slots не уносит весь колорвей", func(t *testing.T) {
		draft, _, err := parseConstructionDraft(
			`{"colourways":[{"name":"A","color_code":"BLK","slots":"black"}]}`, "stop")
		require.NoError(t, err)
		require.Len(t, draft.GetColourways(), 1)
		require.Equal(t, "A", draft.GetColourways()[0].GetName())
		require.Empty(t, draft.GetColourways()[0].GetSlots())
	})

	t.Run("длинное имя режется, а не роняет сохранение", func(t *testing.T) {
		long := strings.Repeat("я", 400)
		draft, stats, err := parseConstructionDraft(
			`{"colourways":[{"name":"`+long+`","slots":[{"slot":"x","colour":"c"}]}]}`, "stop")
		require.NoError(t, err)
		name := draft.GetColourways()[0].GetName()
		require.LessOrEqual(t, utf8.RuneCountInString(name), designConstructionMaxColourwayNameRunes)
		require.LessOrEqual(t, len(name), designConstructionMaxVarchar255)
		require.Positive(t, stats.Truncated)
	})
}

// ОТВЕТ, СОДЕРЖАТЕЛЬНЫЙ ОДНИМИ КОЛОРВЕЯМИ, — ЗАКОННЫЙ ОТВЕТ.
//
// Без `colourways` в списке ключей, которым есть куда лечь, он уходил бы в `invalid_output`
// ОПЛАЧЕННЫМ.
func TestParseConstructionDraftAcceptsAColourwayOnlyAnswer(t *testing.T) {
	draft, _, err := parseConstructionDraft(
		`{"colourways":[{"name":"Black","color_code":"BLK","slots":[]}]}`, "stop")
	require.NoError(t, err)
	require.Len(t, draft.GetColourways(), 1)
}

// ⚠ СОХРАНЁННЫЙ ДО КРУГА 20 ОТВЕТ ВОССТАНАВЛИВАЕТСЯ ЦЕЛИКОМ, ВМЕСТЕ С ВЫНОСКАМИ.
//
// ЭТО НЕ УТВЕРЖДЕНИЕ, А ИСПОЛНЕНИЕ: строка ниже — БАЙТЫ, которые прежний маршалер положил в
// `output_text`, и они прогоняются через сегодняшний читатель. Выбросив ключ `callouts` из разбора
// вместе с ним из промпта, мы сломали бы ВТОРОЕ нажатие на каждом прогоне, отвеченном до этой
// волны, — и сломали бы навсегда: перезвонить модели по тому же ключу идемпотентности нельзя.
func TestOldStoredAnswerWithCalloutsStillReplays(t *testing.T) {
	// Форма — ровно та, что писал designConstructionMarshal ДО круга 20: UseProtoNames,
	// EmitUnpopulated, БЕЗ ключа `colourways` (поля 9 тогда не существовало).
	preB13 := `{"silhouette":"Sleeveless V-neck tank top","fabric":"Stretch knit jersey",` +
		`"fit":"regular","concept":"","aspects":[{"key":"collar","text":"V-neck binding"}],` +
		`"callouts":[{"feature":"neck binding","details":"self-fabric, folded","dimensions":"1 cm"},` +
		`{"feature":"side seam","details":"overlocked","dimensions":""}],` +
		`"bom":[{"section":"TECH_CARD_BOM_SECTION_FABRIC","purpose":"TECH_CARD_BOM_PURPOSE_MAIN",` +
		`"kind":"TECH_CARD_BOM_KIND_UNSET","name":"main fabric","composition":"","colour":"black",` +
		`"pantone":"19-4005 TCX","material_id":"0"}],"missing":["picture 1 — the shoulder"]}`

	back := designConstructionDraftFromRun(preB13)
	require.NotNil(t, back, "прогон, отвеченный до круга 20, обязан читаться сегодняшним разбором")
	require.Equal(t, "Sleeveless V-neck tank top", back.GetSilhouette())
	require.Len(t, back.GetAspects(), 1)
	require.Len(t, back.GetBom(), 1)
	require.Equal(t, pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC, back.GetBom()[0].GetSection())
	require.Len(t, back.GetCallouts(), 2, "выноски старого ответа обязаны доехать до клиента как были")
	require.Equal(t, "neck binding", back.GetCallouts()[0].GetFeature())
	require.Equal(t, "1 cm", back.GetCallouts()[0].GetDimensions())
	require.Empty(t, back.GetColourways(), "поля 9 в том ответе не было — и выдумывать его нечем")

	// И ОТВЕТ, СОДЕРЖАТЕЛЬНЫЙ ОДНИМИ ВЫНОСКАМИ, — ТОЖЕ: ключ остался в списке «есть куда лечь».
	onlyCallouts := designConstructionDraftFromRun(
		`{"callouts":[{"feature":"hem","details":"double needle","dimensions":""}]}`)
	require.NotNil(t, onlyCallouts)
	require.Len(t, onlyCallouts.GetCallouts(), 1)
}

// ВЫНОСКИ, ПРИСЛАННЫЕ БЕЗ СПРОСА, ПРИНИМАЮТСЯ И СЧИТАЮТСЯ.
//
// Клиент их не рисует; счётчик существует ради одного вопроса — работает ли переписанное правило 3.
// Ноль здесь доказывает, что работает; растущее число — счёт за выходные токены, которых не читают.
func TestParseConstructionDraftCountsUnaskedCallouts(t *testing.T) {
	draft, stats, err := parseConstructionDraft(
		`{"callouts":[{"feature":"hem","details":"double needle"},
		              {"feature":"cuff","details":"binding"}],"fabric":"jersey"}`, "stop")
	require.NoError(t, err)
	require.Len(t, draft.GetCallouts(), 2)
	require.Equal(t, 2, stats.CalloutsUnasked)
	require.Zero(t, stats.CalloutsDropped, "строка со словами — не брак формы")
	require.True(t, stats.Any(), "«модель отвечает на незаданный вопрос» обязано доехать до лога")

	clean, cleanStats, err := parseConstructionDraft(`{"fabric":"jersey"}`, "stop")
	require.NoError(t, err)
	require.Empty(t, clean.GetCallouts())
	require.Zero(t, cleanStats.CalloutsUnasked)
}

// КРУГОВОЙ ОБХОД С КОЛОРВЕЯМИ: маршалер → строка → тот же разбор.
func TestConstructionDraftRoundTripsColourways(t *testing.T) {
	in := &pb_common.DesignConstructionDraft{
		Colourways: []*pb_common.DesignColourwayProposal{{
			Name: "Black / Bone", ColorCode: "BLK", Pantone: "19-4005 TCX", Hex: "#0A0A0A",
			Slots: []*pb_common.DesignColourwaySlotColour{
				{Slot: "main fabric", Pantone: "19-4005 TCX", Hex: "#0A0A0A", Colour: "black"},
			},
		}},
	}
	stored, err := designMarshalConstructionDraft(in)
	require.NoError(t, err)
	require.Contains(t, string(stored), "\"colourways\"")
	require.Contains(t, string(stored), "\"color_code\"")

	back := designConstructionDraftFromRun(string(stored))
	require.NotNil(t, back)
	require.True(t, proto.Equal(in, back), "круг обязан вернуть то же самое:\n%v\n%v", in, back)
}

// ЖИВОЙ КРУГ: НАЖАТИЕ ОТДАЁТ КОЛОРВЕИ, СЛОВАРЬ ЕДЕТ В ПРОМПТ, А В СТРОКУ УХОДИТ ПРОВЕРЕННОЕ.
func TestDraftDesignIdeaAnswersWithVerifiedColourways(t *testing.T) {
	answer := `{"silhouette":"tank","fabric":"jersey",
	  "bom":[{"section":"fabric","purpose":"main","name":"main fabric"}],
	  "colourways":[
	    {"name":"Black","color_code":"black","pantone":"19-4005 TCX","hex":"#0A0A0A",
	     "slots":[{"slot":"Main Fabric","pantone":"19-4005 TCX","colour":"black"},
	              {"slot":"nowhere","colour":"grey"}]},
	    {"name":"Fuchsia","color_code":"NOPE","slots":[{"slot":"main fabric","colour":"pink"}]}]}`
	rig := newDraftRig(t, http.StatusOK, answer)
	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.NoError(t, err)

	// СЛОВАРЬ УЕХАЛ В ПЛАТНЫЙ ВЫЗОВ — по БАЙТАМ запроса, а не по намерению кода.
	require.Contains(t, rig.stub.body, "BLK · black · #000000")

	cws := resp.GetConstruction().GetColourways()
	require.Len(t, cws, 2)
	require.Equal(t, "BLK", cws[0].GetColorCode(), "код узнан по имени цвета и приведён к канону")
	require.Len(t, cws[0].GetSlots(), 1, "цвет слота, которого нет ни в ответе, ни на карточке, выброшен")
	require.Equal(t, "Main Fabric", cws[0].GetSlots()[0].GetSlot())
	require.Equal(t, "", cws[1].GetColorCode(), "неузнанный код обнулён, а строка осталась")

	// В `output_text` УЕХАЛО ПРОВЕРЕННОЕ, А НЕ ОТВЕТ МОДЕЛИ: повтор обязан отдать то же, что
	// человек уже видел.
	stored := designConstructionDraftFromRun(rig.completedText)
	require.NotNil(t, stored)
	require.Len(t, stored.GetColourways(), 2)
	require.Equal(t, "BLK", stored.GetColourways()[0].GetColorCode())
	require.Len(t, stored.GetColourways()[0].GetSlots(), 1)
	require.NotContains(t, rig.completedText, "nowhere",
		"выброшенный цвет слота не имеет права остаться в сохранённой строке")
}
