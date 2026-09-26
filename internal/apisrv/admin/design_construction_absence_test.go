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
// ⚠ ПЕРВЫЙ ПРИМЕР ВЛАДЕЛЬЦА ЛОВИТ ТОЛЬКО ПРАВИЛО (в), И ТОЛЬКО ПОД СВОИМ КЛЮЧОМ. Без ключа «No visible
// closures; pull-on construction…» — описание после точки с запятой, и правило (б) бережёт его тем же
// жестом, которым бережёт «Without lining; single layer throughout»; под ключом fastening открывающее
// «no visible closures» отрицает предмет ключа, остаток о застёжке молчит — отсутствие. Тот же
// «Without lining; single layer throughout» под ключом lining — отсутствие, под extraDetails —
// описание. Второй пример (ярлык) — суждение о СОДЕРЖАНИИ, его держит правило 11 промпта; таблица
// показывает это честно, строкой с «absent: false».

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
		// ─── ДВА ПРИМЕРА ВЛАДЕЛЬЦА БЕЗ КЛЮЧА — ОБА ОСТАЮТСЯ; первый под своим ключом ловит правило
		// (в), см. TestKeyAwareAbsenceRule ───
		{"No visible closures; pull-on construction, relying on jersey stretch for fit.", false,
			"O-32 #1 без ключа: после «;» стоит описание — то же правило, что бережёт «Without lining; single layer»"},
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

// ПРАВИЛО (в) — КЛЮЧ ОТРИЦАЕТ СВОЙ ПРЕДМЕТ: тот же текст под одним ключом — отсутствие, под другим —
// описание. Строки парами, чтобы ключ был единственной переменной.
func TestKeyAwareAbsenceRule(t *testing.T) {
	for _, tc := range []struct {
		key    string
		text   string
		absent bool
		why    string
	}{
		// ─── ДВА ПРИМЕРА ВЛАДЕЛЬЦА ПОД СВОИМИ КЛЮЧАМИ ───
		{"fastening", "No visible closures; pull-on construction, relying on jersey stretch for fit.", true,
			"O-32 #1: открывается отрицанием предмета ключа, остаток о застёжке молчит"},
		{"auxMaterials", "Small woven brand/size label sewn at inner side seam, visible in picture 1.", false,
			"O-32 #2: ярлык — содержание; держит правило 11"},

		// ─── ОДИН ТЕКСТ, ДВА КЛЮЧА ───
		{"lining", "Without lining; single layer throughout", true, "отсутствие подкладки — и есть ответ ключу"},
		{"extraDetails", "Without lining; single layer throughout", false, "без словаря — описание, факт «в один слой»"},
		{"extraDetails", "Without side seams — tubular-knit body", false, "без словаря"},
		{"fastening", "Without side seams — tubular-knit body", false, "«seams» — не предмет ключа fastening"},
		{"extraDetails", "No closures", true, "правило (б) ключа не требует"},
		{"vent", "No vent; plain back", false, "самодельный ключ без словаря — правило (в) не стреляет"},
		{"silhouette", "No waist seam; A-line", false, "ключ без словаря"},

		// ─── ОСТАТОК, НАЗЫВАЮЩИЙ ДРУГОЙ ПРЕДМЕТ ТОГО ЖЕ КЛЮЧА, СПАСАЕТ СТРОКУ ───
		{"fastening", "No zipper; three buttons at the placket", false, "описание застёжки, а не её отсутствие"},
		{"fastening", "No buttons, just a tie", false, "«tie» — тоже застёжка"},
		{"fastening", "No closures; elasticated waist", true, "«elastic» — предмет auxMaterials, не fastening"},
		{"pockets", "No visible pockets — inseam pockets at the side seams", false, "остаток называет карманы"},
		{"collar", "No collar: bound neckline, 1 cm", false,
			"«no collar» — не метка ключа (двоеточие после отрицания); остаток называет neckline"},
		{"hardware", "No eyelets; grommets instead", false, "grommet — тот же предмет"},

		// ─── КАЖДЫЙ КЛЮЧ СЛОВАРЯ, ВКЛЮЧАЯ ЧУЖИЕ НАПИСАНИЯ ───
		{"pockets", "No pockets; clean front", true, ""},
		{"pocket", "No pockets; clean front", true, "ключ в единственном числе"},
		{"collar", "No collar; raw edge", true, ""},
		{"sleeveCuff", "No cuffs, plain hem", true, "«hem» — не предмет ключа cuffs"},
		{"sleeve_cuff", "No cuffs, plain hem", true, "написание модели складывается"},
		{"cuffs", "No cuff", true, ""},
		{"hem", "No hem — raw cut edge", true, ""},
		{"hems", "No hemline; raw cut", true, ""},
		{"topstitching", "No topstitching; edges bound", true, ""},
		{"topstitching", "No stitching visible; blind hem", true, ""},
		{"hardware", "No hardware, all self-fabric", true, ""},
		{"auxMaterials", "No interfacing; self-fabric facing", true, ""},
		{"fastening", "Fastening: no closures; pull-on", true, "метка ключа снята, дальше (в)"},
		{"fastenings", "There are no fastenings; pull-on", true, "ключ во множественном числе"},

		// ─── ПРЕДЕЛЫ (в), НАЗВАННЫЕ ВСЛУХ ───
		{"fastening", "None visible; likely pull-on", false, "в первой клаузе нет предмета — держит правило 11"},
		{"fastening", "No visible closures at all on the front; pull-on", false, "пять слов — не короткое отрицание"},
		{"fastening", "n/a — no pockets on this style", false, "первая клауза — заглушка, не отрицание с предметом"},
		{"fastening", "Notched closure flap with two snaps", false, "граница слова; это деталь"},
		{"fastening", "No-sew bonded closure tab", false, "«no-» сцеплено"},
	} {
		t.Run(tc.key+" / "+tc.text, func(t *testing.T) {
			require.Equal(t, tc.absent, designIsAbsentAspect(tc.key, tc.text), "%s / %q: %s", tc.key, tc.text, tc.why)
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
		// Первый пример владельца ДОСЛОВНО: ловится правилом (в) под своим ключом.
		{"key": "fastening", "text": "No visible closures; pull-on construction, relying on jersey stretch for fit."},
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
