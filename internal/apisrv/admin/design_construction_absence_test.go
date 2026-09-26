package admin

// ПРОБЫ ВОЛНЫ 26.09 (O-32, D-33): АСПЕКТ — ТОЛЬКО КОГДА ОН ЕСТЬ. Переписаны по ревью Codex.
//
// Владелец получил в CONSTRUCTION два мусорных аспекта: FASTENING = «No visible closures; pull-on
// construction, relying on jersey stretch for fit» на изделии без застёжек и AUX MATERIALS =
// «Small woven brand/size label sewn at inner side seam» — ярлык, то есть строку спеки. Починка
// в двух половинах, и обе прибиты здесь:
//
//  1. ПРОМПТ: правило 11 велит ПРОПУСКАТЬ неприменимый ключ, никогда не описывать отсутствие и
//     называет ярлыки спецификацией по имени.
//  2. РАЗБОР: ДВА ПРЕДИКАТА — голая заглушка («none», «n/a», «Fastening: N/A», «—») и короткое
//     чистое отрицание («no closures», «there aren't any fastenings»). Всё длиннее четырёх слов
//     или с положительной связкой — ОПИСАНИЕ, и оно остаётся. Только у ЖИВОГО ответа: канон
//     повтора читается как написан. Хендлер называет ключ выброшенного в логе; ответ и канон его
//     не несут.
//
// ⚠ ОБА ПРИМЕРА ВЛАДЕЛЬЦА РАЗБОР НЕ ЛОВИТ, И ЭТО РЕШЕНИЕ РЕВЬЮ, А НЕ ПРОПУСК. Первый — «No visible
// closures; ПУЛОВЕР…» — после точки с запятой несёт описание, и правило, которое выбрасывало бы его,
// выбрасывало бы и «Without lining; single layer throughout» вместе с фактом «в один слой». Второй
// (ярлык) — суждение о СОДЕРЖАНИИ. Оба держит правило 11 промпта; таблица ниже показывает это
// честно, строкой с «absent: false».

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
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

// ПРАВИЛО ОТСУТСТВИЯ ДЛЯ ТЕКСТА АСПЕКТА — ОДНА ТАБЛИЦА: что ловят (а) и (б), и чего не трогают.
func TestAbsenceStatementRule(t *testing.T) {
	for _, tc := range []struct {
		text   string
		absent bool
		why    string
	}{
		// ─── ДВА ПРИМЕРА ВЛАДЕЛЬЦА — ОБА ОСТАЮТСЯ, ИХ ДЕРЖИТ ПРАВИЛО 11 ПРОМПТА ───
		{"No visible closures; pull-on construction, relying on jersey stretch for fit.", false,
			"O-32 #1: после «;» стоит описание — то же правило, что бережёт «Without lining; single layer»"},
		{"Small woven brand/size label sewn at inner side seam, visible in picture 1.", false,
			"O-32 #2: ярлык — СОДЕРЖАНИЕ, его чинит правило 11"},

		// ─── (а) ГОЛЫЕ ЗАГЛУШКИ ───
		{"none", true, ""},
		{"None.", true, "регистр и точка"},
		{"N/A", true, ""},
		{"n/a.", true, ""},
		{"NA", true, ""},
		{"Not applicable", true, ""},
		{"Not applicable.", true, ""},
		{"Does not apply", true, ""},
		{"Not present", true, ""},
		{"Absent.", true, "ревью: раньше выживал"},
		{"Omitted", true, "ревью: раньше выживал"},
		{"Nothing", true, ""},
		{"nil", true, ""},
		{"null", true, "модель отвечает словом, а не JSON-null"},
		{"zero", true, ""},
		{"0", true, ""},
		{"—", true, "одна пунктуация"},
		{"-", true, ""},
		{"(none)", true, "скобки снимаются"},
		{"  \"None\"  ", true, "кавычки и пробелы снимаются"},
		{"Fastening: N/A", true, "ревью: метка «ключ:» снимается"},
		{"sleeve / cuff: none", true, "метка из трёх слов со слэшем"},
		{"Pockets: —", true, "метка и одна пунктуация"},
		{"", true, "пусто — заглушка по построению; разбор перехватывает его раньше как брак формы"},

		// ─── (б) КОРОТКИЕ ЧИСТЫЕ ОТРИЦАНИЯ ───
		{"No closures", true, ""},
		{"No pockets.", true, ""},
		{"No visible closures", true, ""},
		{"No visible closures at all", true, "ровно четыре слова после «no»"},
		{"There are no fastenings", true, ""},
		{"There is no collar", true, ""},
		{"There aren’t any fastenings", true, "ревью: типографский апостроф"},
		{"There isn't any lining", true, ""},
		{"Not lined", true, ""},
		{"Not visible", true, ""},
		{"Without lining", true, ""},
		{"Nothing visible", true, ""},
		{"None visible", true, ""},
		{"Zero closures", true, ""},
		{"0 closures", true, "ревью: раньше выживал"},
		{"— no closures", true, "ведущее тире снимается"},
		{"no closures;", true, "хвостовая пунктуация снимается, связкой не считается"},

		// ─── ОПИСАНИЯ, НАЧИНАЮЩИЕСЯ С «НЕТ» — ОСТАЮТСЯ (ревью: раньше терялись) ───
		{"Without lining; single layer throughout", false,
			"РЯД ПЕРЕВЁРНУТ ревью: после «;» — факт «в один слой», и он дороже сетки"},
		{"Nothing but a raw-edge finish at the hem", false, "«but» — положительная связка"},
		{"Without side seams — tubular-knit body", false, "em dash — связка"},
		{"Without side seams – tubular-knit body", false, "en dash — связка"},
		{"Without side seams - tubular-knit body", false, "дефис с пробелами — тире"},
		{"No visible closures at all on the front", false, "пять слов — уже описание"},
		{"None visible; likely pull-on", false, "«;» — связка"},
		{"n/a — no pockets on this style", false,
			"известный предел правила: тире делает остаток описанием; держит правило 11"},
		{"No lining with a bound facing", false, "«with» — связка"},
		{"No closures, just a tie", false, "запятая и «just»"},
		{"Not presented in the pictures but the note says: welt pockets", false, "«but» и «:»"},
		{"Hidden placket, no visible stitching from the outside", false, "«нет» в середине не считается"},
		{"Pull-on construction relying on jersey stretch; no closures", false, "начинается с детали"},
		{"There aren't any fastenings: pull-on", false,
			"четыре слова с апострофом — не метка ключа; «:» остаётся связкой"},

		// ─── ГРАНИЦА СЛОВА: НАСТОЯЩИЕ ДЕТАЛИ, ПОХОЖИЕ НА ОТРИЦАНИЕ ПО ПЕРВЫМ БУКВАМ ───
		{"Notched lapel collar with a 4 cm stand", false, "«not…» — не «not»"},
		{"Nonwoven fusible interfacing at collar and cuffs", false, "«non…» — не «none»"},
		{"No-sew bonded hem, 2 cm", false, "ASCII-дефис сцепляет слово"},
		{"No‑sew bonded hem", false, "ревью: U+2011 сцепляет слово так же"},
		{"No–sew bonded hem", false, "U+2013 сразу после слова — сцепка, не тире"},
		{"Normal hem, 2 cm, blind stitched", false, ""},
		{"Nothingness print at chest", false, ""},
		{"Nilotic print", false, ""},
		{"Nullstitch decorative seam", false, ""},
		{"Zero-waste pattern layout", false, "«zero-» сцеплено"},
		{"0.5 cm hem allowance", false, "число, а не «ноль штук»"},
		{"Two-piece sleeve with a buttoned cuff", false, "положительный контроль"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			require.Equal(t, tc.absent, designIsAbsentAspectText(tc.text), "%q: %s", tc.text, tc.why)
		})
	}
}

// ИМЯ ДЕТАЛИ ДЛЯ ОТДЕЛЬНОГО РИСУНКА — ТОЛЬКО ГОЛАЯ ЗАГЛУШКА: имя — подпись, а не фраза.
func TestFlatDetailNameSentinelRule(t *testing.T) {
	for _, tc := range []struct {
		name   string
		absent bool
	}{
		{"none", true},
		{"N/A", true},
		{"—", true},
		{"No separate drawing needed.", true},
		{"no separate drawings needed", true},
		{"None needed", true},
		{"Flat details: none", true},
		{"Without side seams — tubular-knit body", false},
		{"No-sew bonded hem", false},
		{"welt pocket with flap", false},
		// Правило (б) к именам НЕ применяется: короткое отрицание именем не бывает, но и цензурировать
		// подпись нечем — имя либо заглушка, либо имя.
		{"No closures", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.absent, designIsAbsentFlatDetailName(tc.name))
		})
	}
}

// ЖИВОЙ РАЗБОР ВЫБРАСЫВАЕТ ОТСУТСТВИЯ, ОСТАВЛЯЕТ НАСТОЯЩИЕ, НЕ ДАЁТ ОТСУТСТВИЮ ЗАНЯТЬ МЕСТО В
// ПОТОЛКЕ И ОТДАЁТ НАБЛЮДАТЕЛЮ ТОЛЬКО КЛЮЧ.
func TestParseConstructionDraftDropsAbsenceAspectsAndKeepsRealOnes(t *testing.T) {
	longCustom := strings.Repeat("k", 80)
	aspects := []map[string]string{
		{"key": "fastening", "text": "No closures"},
		{"key": "pockets", "text": "None."},
		{"key": "collar", "text": "Rib-knit crew neck, 2 cm, self-fabric"},
		{"key": "auxMaterials", "text": "Small woven brand/size label sewn at inner side seam, visible in picture 1."},
		{"key": "extra_details", "text": "n/a"}, // ключ в чужом написании — в лог уедет канонический
		{"key": longCustom, "text": "there aren't any"},
		{"key": "topstitching", "text": "—"}, // одна пунктуация — пустой текст, а не отсутствие
		{"key": "hem", "text": "Nothing but a raw-edge finish at the hem"},
	}
	// РОВНО ПОТОЛОК настоящих строк (воротник + ярлык + подгибка + семь самодельных = 10) ПОВЕРХ
	// четырёх отсутствий: если отсутствия выбрасываются ПОСЛЕ потолка, они займут четыре места.
	for i := 0; i < designConstructionMaxAspects-3; i++ {
		aspects = append(aspects, map[string]string{
			"key": "custom" + string(rune('a'+i)), "text": "a real construction detail",
		})
	}
	body, err := json.Marshal(map[string]any{"silhouette": "boxy", "aspects": aspects})
	require.NoError(t, err)

	var seen []string
	draft, stats, perr := parseConstructionDraftTracing(string(body), "stop", func(key string) {
		seen = append(seen, key)
	})
	require.NoError(t, perr)

	require.Equal(t, 4, stats.AspectsAbsent, "no closures, none, n/a, there aren't any")
	require.Equal(t, 1, stats.AspectsDropped, "«—» — пустой текст, свой счётчик")
	require.Equal(t, 0, stats.OverLimit, "отсутствия не занимают мест в потолке")
	require.True(t, stats.Coerced(), "выброшенная строка — потеря, и она поднимает Warn")

	keys := make([]string, 0, len(draft.GetAspects()))
	for _, a := range draft.GetAspects() {
		keys = append(keys, a.GetKey())
		require.False(t, designIsAbsentAspectText(a.GetText()), "%s: отсутствие доехало до ответа", a.GetKey())
	}
	require.Len(t, keys, designConstructionMaxAspects, "воротник + ярлык + подгибка + семь настоящих — ровно потолок")
	require.Contains(t, keys, "collar")
	require.Contains(t, keys, "auxMaterials", "ярлык — содержание; правило отсутствия его не трогает")
	require.Contains(t, keys, "hem", "«nothing but …» — описание, а не отсутствие")
	require.NotContains(t, keys, "fastening")
	require.NotContains(t, keys, "pockets")
	require.NotContains(t, keys, "extraDetails")

	// НАБЛЮДАТЕЛЬ ПОЛУЧИЛ КЛЮЧ, И ТОЛЬКО КЛЮЧ: словарный — каноническим, самодельный — обрезанным.
	require.Len(t, seen, 4)
	require.Equal(t, []string{"fastening", "pockets", "extraDetails"}, seen[:3])
	require.Equal(t, designConstructionMaxTraceKeyRunes, utf8.RuneCountInString(seen[3]))
	require.True(t, strings.HasSuffix(seen[3], "…"), "самодельный ключ обрезан с маркером")
	for _, k := range seen {
		require.NotContains(t, k, "closures", "текст аспекта в наблюдатель не едет")
	}

	// КОРОТКАЯ ФОРМА — ТОТ ЖЕ РАЗБОР БЕЗ НАБЛЮДАТЕЛЯ, и nil-наблюдатель не роняет ничего.
	plain, plainStats, perr := parseConstructionDraft(string(body), "stop")
	require.NoError(t, perr)
	require.Equal(t, stats, plainStats)
	require.Len(t, plain.GetAspects(), len(draft.GetAspects()))
}

// ПОВТОР ЧИТАЕТ КАНОН ТАК, КАК ОН НАПИСАН (ревью 26.09, MAJOR 1).
//
// Прогон, сохранённый ДО этой волны с «No closures» под ключом fastening, обязан вернуть этот аспект
// и сегодня: один client_request_id не имеет права отдавать разное число аспектов до и после
// выката, а `run.output_text` и `construction.aspects` в одном ответе — противоречить друг другу.
func TestConstructionDraftReplayReadsTheCanonAsWritten(t *testing.T) {
	// ─── КАНОН ПРЕЖНЕГО ПИСАТЕЛЯ, ДОСЛОВНО: protojson с именами proto, пустые ключи на месте,
	// `flat_details` ещё не существует ───
	const stored = `{"silhouette":"tee","fabric":"","fit":"","concept":"",` +
		`"aspects":[{"key":"fastening","text":"No closures"},{"key":"collar","text":"rib neck"}],` +
		`"callouts":[],"bom":[],"missing":[],"colourways":[]}`

	back := designConstructionDraftFromRun(stored)
	require.NotNil(t, back)
	require.Len(t, back.GetAspects(), 2, "канон отдан как написан — отсутствие на месте")
	require.Equal(t, "fastening", back.GetAspects()[0].GetKey())
	require.Equal(t, "No closures", back.GetAspects()[0].GetText())

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: ЖИВОЙ разбор тех же байтов отсутствие выбрасывает — разница ровно в
	// режиме, а не в тексте.
	live, stats, err := parseConstructionDraft(stored, "stop")
	require.NoError(t, err)
	require.Len(t, live.GetAspects(), 1)
	require.Equal(t, "collar", live.GetAspects()[0].GetKey())
	require.Equal(t, 1, stats.AspectsAbsent)

	// ─── И КАНОН СЕГОДНЯШНЕГО ПИСАТЕЛЯ: что записано, то и прочитано, ПОЛЕ В ПОЛЕ, включая имя
	// детали «none» — смысловые сторожа у канона выключены все, а не выборочно ───
	in := &pb_common.DesignConstructionDraft{
		Silhouette: "tee",
		Aspects: []*pb_common.DesignConstructionAspect{
			{Key: "fastening", Text: "n/a"}, {Key: "collar", Text: "rib neck"},
		},
		FlatDetails: []*pb_common.DesignFlatDetail{{Name: "none"}, {Name: "storm flap", Note: "x"}},
	}
	canon, err := designMarshalConstructionDraft(in)
	require.NoError(t, err)
	round := designConstructionDraftFromRun(string(canon))
	require.NotNil(t, round)
	require.True(t, proto.Equal(in, round), "хранилось: %s\nпрочитано: %v", canon, round)

	liveAgain, stats, err := parseConstructionDraft(string(canon), "stop")
	require.NoError(t, err)
	require.Len(t, liveAgain.GetAspects(), 1)
	require.Len(t, liveAgain.GetFlatDetails(), 1)
	require.Equal(t, 1, stats.AspectsAbsent)
	require.Equal(t, 1, stats.FlatDetailsDropped)
}

// ХЕНДЛЕР: ОТВЕТ И КАНОН БЕЗ ОТСУТСТВИЙ, А ЛОГ НАЗЫВАЕТ КЛЮЧ ВЫБРОШЕННОГО — И ТОЛЬКО КЛЮЧ.
func TestDraftDesignIdeaDropsAbsenceAspectsAndNamesThemInTheLog(t *testing.T) {
	answer := `{"silhouette":"Boxy pull-on tee","fabric":"Cotton jersey",
	  "aspects":[
	    {"key":"fastening","text":"No closures on launch sample"},
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
	require.NotContains(t, rig.completedText, "No closures")
	require.NotContains(t, rig.completedText, "\"pockets\"")
	stored := designConstructionDraftFromRun(rig.completedText)
	require.NotNil(t, stored)
	require.Len(t, stored.GetAspects(), 1)

	// ЛОГ: одна Debug-строка на каждое выброшенное отсутствие — с ключом и БЕЗ текста …
	var named []string
	for _, r := range sink.records {
		if r.Level != slog.LevelDebug {
			continue
		}
		key, ok := r.Attrs["aspect_key"]
		if !ok {
			continue
		}
		named = append(named, key)
		_, leaked := r.Attrs["aspect_text"]
		require.False(t, leaked, "текст аспекта в лог не едет (ревью 26.09, MINOR)")
		for _, v := range r.Attrs {
			require.NotContains(t, v, "launch sample", "слова с доски не имеют права попасть в лог")
		}
	}
	require.Equal(t, []string{"fastening", "pockets"}, named)

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
