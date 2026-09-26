package admin

// ПРОБЫ ВОЛНЫ 26.09 (O-32, D-33): АСПЕКТ — ТОЛЬКО КОГДА ОН ЕСТЬ.
//
// Владелец получил в CONSTRUCTION два мусорных аспекта: FASTENING = «No visible closures; pull-on
// construction, relying on jersey stretch for fit» на изделии без застёжек и AUX MATERIALS =
// «Small woven brand/size label sewn at inner side seam» — ярлык, то есть строку спеки. Починка
// в двух половинах, и обе прибиты здесь:
//
//  1. ПРОМПТ: правило 11 велит ПРОПУСКАТЬ неприменимый ключ, никогда не описывать отсутствие и
//     называет ярлыки спецификацией по имени.
//  2. РАЗБОР: текст-отсутствие выбрасывается ДО потолка списка и считается отдельно
//     (AspectsAbsent); хендлер называет выброшенное в логе на Debug; ответ и канон его не несут.
//
// ⚠ ВТОРОЙ ПРИМЕР ВЛАДЕЛЬЦА (ярлык) РАЗБОР НЕ ЛОВИТ, И ЭТО РЕШЕНИЕ, А НЕ ПРОПУСК. «Ярлык — не деталь
// конструкции» — суждение о СОДЕРЖАНИИ, а граница разбора в этом файле проходит по ФОРМЕ (решение
// 3 в шапке design_construction_draft.go): эвристика «текст про label — выбросить» уносила бы и
// «back neck facing; label sewn under the facing». Ярлык чинит правило 11, и проба ниже честно
// показывает, что правило отсутствия его НЕ трогает.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// РОЛЬ ГОВОРИТ «ПРОПУСТИ КЛЮЧ», «НЕ ОПИСЫВАЙ ОТСУТСТВИЕ» И «ЯРЛЫКИ — СПЕЦИФИКАЦИЯ» — СЛОВАМИ.
func TestConstructionSystemPromptAsksForAspectsOnlyWhereTheyExist(t *testing.T) {
	require.Contains(t, designConstructionSystemPrompt, "OMIT any key that does not apply")
	require.Contains(t, designConstructionSystemPrompt, "never write an entry that describes an absence")
	require.Contains(t, designConstructionSystemPrompt, "\"no closures\", \"none\", \"not applicable\"")
	require.Contains(t, designConstructionSystemPrompt,
		"construction or making detail that is actually on this garment")
	// Ярлыки названы ПО ИМЕНИ и отправлены в спеку, а не просто «не аспект».
	require.Contains(t, designConstructionSystemPrompt, "care labels, size labels and brand labels")
	require.Contains(t, designConstructionSystemPrompt, "never aspects")
	require.Contains(t, designConstructionSystemPrompt, "list them under \"bom\"")
	// И ПРАВИЛА НЕ ПЕРЕНУМЕРОВАНЫ: десятое по-прежнему про нитку, одиннадцатое — новое.
	require.Contains(t, designConstructionSystemPrompt, "\n10. \"bom\" always includes one \"thread\"")
	require.Contains(t, designConstructionSystemPrompt, "\n11. An aspect is")

	// Список ключей в пользовательском промпте повторяет разрешение там, где модель его читает.
	card := draftBindCard()
	prompt := designConstructionUserPrompt(card, designMoodSnapshot(card), []int{11, 22, 33}, draftProbeColours())
	require.Contains(t, prompt, "omit a key this garment does not have")
}

// ПРАВИЛО ОТСУТСТВИЯ — ОДНО МЕСТО, ОДНА ТАБЛИЦА: что оно ловит и, не менее важно, чего не трогает.
func TestAbsenceStatementRule(t *testing.T) {
	for _, tc := range []struct {
		text   string
		absent bool
		why    string
	}{
		// ─── ДВА ПРИМЕРА ВЛАДЕЛЬЦА ───
		{"No visible closures; pull-on construction, relying on jersey stretch for fit.", true,
			"O-32, FASTENING на изделии без застёжек"},
		{"Small woven brand/size label sewn at inner side seam, visible in picture 1.", false,
			"O-32, ярлык: это СОДЕРЖАНИЕ, его чинит правило 11 промпта, а не правило отсутствия"},

		// ─── ОТСУТСТВИЯ ───
		{"none", true, "голое слово"},
		{"None.", true, "регистр и точка"},
		{"NONE VISIBLE; likely pull-on", true, "прописные, продолжение после слова"},
		{"N/A", true, "каноническое n/a"},
		{"n/a — no pockets on this style", true, "n/a с продолжением"},
		{"Not applicable", true, ""},
		{"Not applicable to this garment.", true, ""},
		{"Does not apply: sleeveless", true, ""},
		{"Not present on this garment", true, ""},
		{"No", true, "одно слово"},
		{"No.", true, ""},
		{"No pockets.", true, ""},
		{"Without lining; single layer throughout", true, "«without» — из списка владельца"},
		{"There are no fastenings", true, ""},
		{"There is no collar; bound neckline", true, ""},
		{"Nothing visible", true, ""},
		{"nil", true, ""},
		{"null", true, "модель отвечает словом, а не JSON-null"},
		{"(none)", true, "ведущая пунктуация снимается"},
		{"— no closures", true, "ведущее тире снимается"},
		{"  \"None\"  ", true, "кавычки и пробелы снимаются"},

		// ─── НАСТОЯЩИЕ ДЕТАЛИ, ПОХОЖИЕ НА ОТСУТСТВИЕ ПО ПЕРВЫМ БУКВАМ ───
		{"Notched lapel collar with a 4 cm stand", false, "«not…» — не «not applicable»"},
		{"Nonwoven fusible interfacing at collar and cuffs", false, "«non…» — не «none»"},
		{"No-sew bonded hem, 2 cm", false, "дефис — не граница слова: «no-sew» это техника"},
		{"Normal hem, 2 cm, blind stitched", false, ""},
		{"Nothingness print at chest", false, "«nothingness» — не «nothing»"},
		{"Not presented in the pictures but the note says: welt pockets", false,
			"«not presented» — не «not present»"},
		{"Hidden placket, no visible stitching from the outside", false,
			"слово в середине фразы не считается — это описание планки"},
		{"Pull-on construction relying on jersey stretch; no closures", false,
			"начинается с детали, отсутствие лишь дописано"},
		{"Nilotic print", false, "«nil…» с продолжением буквой"},
		{"Nullstitch decorative seam", false, ""},
		{"Two-piece sleeve with a buttoned cuff", false, "положительный контроль"},
		{"", false, "пустое — не отсутствие, а брак формы (свой счётчик)"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			require.Equal(t, tc.absent, designIsAbsenceStatement(tc.text), "%q: %s", tc.text, tc.why)
		})
	}
}

// РАЗБОР ВЫБРАСЫВАЕТ ОТСУТСТВИЯ, ОСТАВЛЯЕТ НАСТОЯЩИЕ И НЕ ДАЁТ ОТСУТСТВИЮ ЗАНЯТЬ МЕСТО В ПОТОЛКЕ.
func TestParseConstructionDraftDropsAbsenceAspectsAndKeepsRealOnes(t *testing.T) {
	aspects := []map[string]string{
		{"key": "fastening", "text": "No visible closures; pull-on construction, relying on jersey stretch for fit."},
		{"key": "pockets", "text": "None."},
		{"key": "collar", "text": "Rib-knit crew neck, 2 cm, self-fabric"},
		{"key": "auxMaterials", "text": "Small woven brand/size label sewn at inner side seam, visible in picture 1."},
		{"key": "extraDetails", "text": "n/a"},
		{"key": "topstitching", "text": "—"}, // одна пунктуация — пустой текст, а не отсутствие
	}
	// РОВНО ПОТОЛОК настоящих строк (воротник + ярлык + восемь самодельных = 10) ПОВЕРХ трёх
	// отсутствий: если отсутствия выбрасываются ПОСЛЕ потолка, они займут три места и три
	// настоящих строки уйдут в OverLimit.
	for i := 0; i < designConstructionMaxAspects-2; i++ {
		aspects = append(aspects, map[string]string{
			"key": "custom" + string(rune('a'+i)), "text": "a real construction detail",
		})
	}
	body, err := json.Marshal(map[string]any{"silhouette": "boxy", "aspects": aspects})
	require.NoError(t, err)

	var seen [][2]string
	draft, stats, perr := parseConstructionDraftTracing(string(body), "stop", func(key, text string) {
		seen = append(seen, [2]string{key, text})
	})
	require.NoError(t, perr)

	require.Equal(t, 3, stats.AspectsAbsent, "три отсутствия: no…, none, n/a")
	require.Equal(t, 1, stats.AspectsDropped, "«—» — пустой текст, свой счётчик")
	require.Equal(t, 0, stats.OverLimit, "отсутствия не занимают мест в потолке")
	require.True(t, stats.Coerced(), "выброшенная строка — потеря, и она поднимает Warn")

	keys := make([]string, 0, len(draft.GetAspects()))
	for _, a := range draft.GetAspects() {
		keys = append(keys, a.GetKey())
		require.False(t, designIsAbsenceStatement(a.GetText()), "%s: отсутствие доехало до ответа", a.GetKey())
	}
	require.Len(t, keys, designConstructionMaxAspects, "воротник + ярлык + восемь настоящих — ровно потолок")
	require.Contains(t, keys, "collar")
	require.Contains(t, keys, "auxMaterials", "ярлык — содержание; правило отсутствия его не трогает")
	require.NotContains(t, keys, "fastening")
	require.NotContains(t, keys, "pockets")
	require.NotContains(t, keys, "extraDetails")

	// НАБЛЮДАТЕЛЬ ПОЛУЧИЛ КЛЮЧ И ТЕКСТ КАЖДОГО ВЫБРОШЕННОГО — как их прочтёт человек в логе.
	require.Equal(t, [][2]string{
		{"fastening", "No visible closures; pull-on construction, relying on jersey stretch for fit."},
		{"pockets", "None."},
		{"extraDetails", "n/a"},
	}, seen)

	// КОРОТКАЯ ФОРМА — ТОТ ЖЕ РАЗБОР БЕЗ НАБЛЮДАТЕЛЯ, и nil-наблюдатель не роняет ничего.
	plain, plainStats, perr := parseConstructionDraft(string(body), "stop")
	require.NoError(t, perr)
	require.Equal(t, stats, plainStats)
	require.Len(t, plain.GetAspects(), len(draft.GetAspects()))
}

// ХЕНДЛЕР: ОТВЕТ И КАНОН БЕЗ ОТСУТСТВИЙ, А ЛОГ НАЗЫВАЕТ ВЫБРОШЕННОЕ ПО ОДНОМУ НА DEBUG.
func TestDraftDesignIdeaDropsAbsenceAspectsAndNamesThemInTheLog(t *testing.T) {
	answer := `{"silhouette":"Boxy pull-on tee","fabric":"Cotton jersey",
	  "aspects":[
	    {"key":"fastening","text":"No visible closures; pull-on construction, relying on jersey stretch for fit."},
	    {"key":"collar","text":"Rib-knit crew neck, 2 cm, self-fabric"},
	    {"key":"pockets","text":"None"}],
	  "bom":[{"section":"fabric","purpose":"main","name":"main fabric"}]}`
	rig := newDraftRig(t, http.StatusOK, answer)
	sink := tcaCaptureLog(t)

	resp, err := rig.srv.DraftDesignIdea(designRunCtx(), draftConstructionRequest())
	require.NoError(t, err)

	got := resp.GetConstruction().GetAspects()
	require.Len(t, got, 1, "ответ несёт только настоящую деталь")
	require.Equal(t, "collar", got[0].GetKey())

	// КАНОН — ТО ЖЕ, ЧТО ПОЛУЧИЛ КЛИЕНТ: повтор не воскресит выброшенное.
	require.NotContains(t, rig.completedText, "No visible closures")
	require.NotContains(t, rig.completedText, "\"pockets\"")
	stored := designConstructionDraftFromRun(rig.completedText)
	require.NotNil(t, stored)
	require.Len(t, stored.GetAspects(), 1)

	// ЛОГ: одна Debug-строка на каждое выброшенное отсутствие, с ключом и текстом …
	var named []string
	for _, r := range sink.records {
		if r.Level != slog.LevelDebug {
			continue
		}
		if key, ok := r.Attrs["aspect_key"]; ok {
			named = append(named, key+": "+r.Attrs["aspect_text"])
		}
	}
	require.Equal(t, []string{
		"fastening: No visible closures; pull-on construction, relying on jersey stretch for fit.",
		"pockets: None",
	}, named)

	// … и строка итога несёт число на уровне Warn — выброшенная строка это потеря.
	var summary []map[string]string
	for _, r := range sink.records {
		if _, ok := r.Attrs["aspects_absent"]; ok {
			require.Equal(t, slog.LevelWarn, r.Level, "выброшенное отсутствие — коэрция, и уровень Warn")
			summary = append(summary, r.Attrs)
		}
	}
	require.Len(t, summary, 1, "один прогон — одна строка итога")
	require.Equal(t, "2", summary[0]["aspects_absent"])
	require.Empty(t, rig.failed)
}
