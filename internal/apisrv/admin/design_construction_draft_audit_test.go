package admin

// ЧЕТЫРЕ ДЕФЕКТА АДВЕРСАРНОГО РЕВЬЮ КРУГА 20, КАЖДЫЙ ПРИБИТ СВОЕЙ ПРОБОЙ.
//
//  1. «m²» СКЛАДЫВАЛОСЬ В «m» — единица площади становилась погонным метром ВНУТРИ подписанного
//     дайджеста MATERIALS, и ни один счётчик при этом не рос.
//  2. ДВА СЧЁТЧИКА НЕ ПЕЧАТАЛИСЬ ВОВСЕ, а третий (CalloutsUnasked) поднимал Warn «коэрцировано»
//     на том, что было ПРИНЯТО.
//  3. ДВА КОЛОРВЕЯ МОГЛИ ПРИЕХАТЬ С ОДНИМ КОДОМ — второе подтверждение отказывало словами
//     сервера про UNIQUE(style_id, color_code) вместо наших.
//  4. ПОТОЛОК ОТВЕТА НЕ ДВИГАЛИ, хотя в ответ вошли три новых списка; `finish_reason=length` —
//     это ОПЛАЧЕННЫЙ отказ всего прогона.
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ФАЙЛ ПРОВЕРЕН (каждая прогнана, покраснела и откачена):
//  1. вернуть в designUnitToken поиск `designUnitByFold[designFoldToken(raw)]` → краснеет
//     TestConstructionDraftUnitVocabularyIsTheColumnsOwn на самом «m²»;
//  2. убрать slog.Int("bom_est_dropped", …) из списка печати → краснеет
//     TestConstructionDraftLogPrintsEveryCounter;
//  2b. вернуть CalloutsUnasked в Coerced() → краснеет TestCoercedCountsOnlyWhatWasChanged;
//  3. снять проверку seenCode в designVerifyColourways → краснеет
//     TestVerifyColourwaysKeepsOneProposalPerColourCode;
//  4. вернуть designConstructionMaxTokens = 3000 → краснеет
//     TestConstructionAnswerCeilingHoldsTheWorstRealisticAnswer.
//
// ═══ КРУГ 21: ДВА ДЕФЕКТА, КОТОРЫЕ ОСТАВИЛ ЗА СОБОЙ САМ ПОДЪЁМ ПОТОЛКА 3000 → 8000 ═══════════
//
//  5. ПОТОЛОК ПОДНЯЛИ, А ВРЕМЯ — НЕТ. openrouter держал http.Client{Timeout: 60s} на весь вызов;
//     8000 токенов за 60 s это 133 ток/с при замеренных ~60. Разрешённый ответ физически не
//     успевал приехать, обрыв приходил ТРАНСПОРТНОЙ ошибкой (не ErrBudgetExhausted), и
//     designFailDraft закрывал попытку ЦЕНОЙ NULL: поставщик напечатал, регистр записал ноль.
//  6. ПОТОЛОК ПОДНЯЛИ, А ЦЕНУ — НЕТ. designDraftIdeaConstructionBaseUSD стоял литералом 0.035,
//     посчитанным при потолке 3000; +5000 выходных токенов были оценены в НОЛЬ. Это число и
//     РЕЗЕРВИРУЕТ, и СПИСЫВАЕТСЯ (FinishAttempt), то есть занижение уходит в регистр навсегда.
//
// ⚠️ МУТАЦИИ КРУГА 21 (каждая прогнана, покраснела и откачена):
//  5. вернуть в openrouter.CompletionBudget `return base` (то есть игнорировать max_tokens) →
//     краснеет TestConstructionCeilingCanPhysicallyArrive;
//  5b. убрать context.WithTimeout из postChatCompletion → краснеет
//     TestTheAnswerCeilingBuysItsOwnTime (internal/openrouter);
//  6. вернуть designDraftIdeaConstructionBaseUSD = decimal.RequireFromString("0.035") → краснеет
//     TestConstructionBasePricesTheWholeAnswerCeiling;
//  6b. поменять designChatUSDPerMTokOut на "3" (входной тариф) → краснеет она же.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────── 1. ЕДИНИЦА ───────────────────────────

// TestConstructionDraftUnitVocabularyIsTheColumnsOwn — У ЕДИНИЦЫ ОДИН СЛОВАРЬ НА ВЕСЬ СЕРВЕР.
//
// `unit` лежит ДЕВЯТЫМ В ЗАМОРОЖЕННОЙ ГОЛОВЕ строки materialsRow, то есть внутри ПОДПИСАННОГО
// дайджеста. Прежнее узнавание шло через designFoldToken, а тот оставляет только IsLetter||IsDigit;
// «²» — категория No, не Nd, поэтому fold("m²") == "m". Модель отвечала «m²» про клеевой, человек
// читал «0.45 m» и принимал: квадратные метры уезжали в подпись погонными, и UnitsUnset оставался
// нулём — то есть в логе не было НИЧЕГО.
//
// Записать в подпись ЧУЖОЕ ЗНАКОМОЕ слово хуже, чем оставить пусто: пустое видно.
func TestConstructionDraftUnitVocabularyIsTheColumnsOwn(t *testing.T) {
	t.Run("узнанные написания приводятся к канону колонки", func(t *testing.T) {
		for _, tc := range []struct{ raw, want string }{
			// ⚠ РАДИ ЭТОЙ СТРОКИ ВСЁ И ЗАТЕВАЛОСЬ.
			{"m²", "m2"},
			{"M²", "m2"},
			{" m² ", "m2"},
			{"sqm", "m2"},
			{"m2", "m2"},
			// Синонимы, которые словарь колонки знает, а складка не знала вовсе.
			{"м", "m"}, {"metres", "m"}, {"meter", "m"},
			{"pc", "pcs"}, {"шт", "pcs"}, {"кг", "kg"}, {"см", "cm"},
			// Канонические написания — те, что промпт и показывает.
			{"m", "m"}, {"PCS", "pcs"}, {"Kg", "kg"}, {"cone", "cone"}, {"roll", "roll"},
			// Полное имя члена: модель, взявшая слово из энума, а не из списка в промпте.
			{"MATERIAL_UNIT_M", "m"}, {"material_unit_m2", "m2"},
		} {
			var stats designConstructionStats
			got := designUnitToken(tc.raw, &stats)
			require.Equal(t, tc.want, got, "написание %q", tc.raw)
			require.Zero(t, stats.UnitsUnset, "узнанное написание не считается потерей: %q", tc.raw)
		}
	})

	t.Run("неузнанное отдаётся ПУСТЫМ и считается, а не подменяется соседом", func(t *testing.T) {
		// ⚠ «cm²» — ТОТ ЖЕ КЛАСС ПРОМАХА, ЧТО «m²», И ОН ЛОВИТ ВОЗВРАТ СКЛАДКИ ЧЕРЕЗ ЧЁРНЫЙ ХОД:
		// fold("cm²") == "cm". Пусто здесь — правильный ответ, «cm» — неправильный.
		for _, raw := range []string{"yd", "yards", "cm²", "m³", "дюйм", "ярд", "sq ft"} {
			var stats designConstructionStats
			require.Equal(t, "", designUnitToken(raw, &stats),
				"словарь колонки не знает %q — пусто, а не догадка", raw)
			require.Equal(t, 1, stats.UnitsUnset,
				"потеря обязана быть посчитана, иначе «модель отвечает не тем словом» не видно: %q", raw)
		}
	})

	t.Run("пустое — не потеря", func(t *testing.T) {
		var stats designConstructionStats
		require.Equal(t, "", designUnitToken("   ", &stats))
		require.Zero(t, stats.UnitsUnset, "не сказано — не то же, что сказано непонятно")
	})

	// ─── ВКЛЮЧЕНИЕ, А НЕ СОВПАДЕНИЕ ПО ПАМЯТИ ───
	//
	// Промпт показывает КОРОТКИЕ имена членов энума; узнаёт entity.NormalizeMaterialUnit. Это два
	// разных выражения одного словаря, и разойтись им негде ровно до тех пор, пока это проверено:
	// написание, которое мы ПОКАЗАЛИ модели и не узнали бы в ответе, — это UNSET у верного ответа.
	t.Run("каждое написание из промпта узнаётся словарём колонки", func(t *testing.T) {
		require.NotEmpty(t, designUnitTokens)
		for _, tok := range designUnitTokens {
			var stats designConstructionStats
			require.Equal(t, tok, designUnitToken(tok, &stats),
				"промпт показывает %q — разбор обязан его узнать", tok)
			u, ok := entity.NormalizeMaterialUnit(tok)
			require.True(t, ok, "%q обязано быть членом словаря колонки", tok)
			require.Equal(t, tok, string(u), "канон энума и канон словаря — одно написание")
		}
	})

	// ⚠ ЧТО БЫ РАЗБОР НИ ВЕРНУЛ, ЭТО ОБЯЗАНО БЫТЬ СЛОВОМ, КОТОРОЕ КОЛОНКА ЧИТАЕТ ОБРАТНО. Иначе
	// строка спеки едет в подписанный дайджест с единицей, которую pbMaterialUnit покажет как
	// UNKNOWN, — то есть с числом, у которого нет измерения.
	t.Run("ничто, кроме пустого, не выходит за словарь колонки", func(t *testing.T) {
		for _, raw := range []string{
			"m", "m²", "sqm", "шт", "yd", "MATERIAL_UNIT_ROLL", "", "  ", "12", "{}",
		} {
			var stats designConstructionStats
			got := designUnitToken(raw, &stats)
			if got == "" {
				continue
			}
			_, ok := entity.NormalizeMaterialUnit(got)
			require.True(t, ok, "разбор вернул %q на %q — колонка такого не читает", got, raw)
		}
	})

	// ─── И ТО ЖЕ САМОЕ ЧЕРЕЗ ЖИВОЙ РАЗБОР, А НЕ ТОЛЬКО ЧЕРЕЗ ФУНКЦИЮ ───
	//
	// Положительный контроль: без него проба зеленела бы и на разборе, который единицу вовсе не
	// читает.
	t.Run("строка спеки доносит единицу площади до провода", func(t *testing.T) {
		line, stats := euDraftLine(t,
			`{"section":"trim","name":"fusible interlining","est_usage":0.45,"unit":"m²"}`)
		require.Equal(t, "m2", line.Unit,
			"«0.45 m» вместо «0.45 m2» — это подписанная строка, которая врёт про измерение")
		require.Equal(t, "0.45", line.EstUsage.GetValue())
		require.Zero(t, stats.UnitsUnset)
	})
}

// ─────────────────────────── 2. СЧЁТЧИКИ И УРОВЕНЬ СТРОКИ ───────────────────────────

// TestConstructionDraftLogPrintsEveryCounter — КАЖДЫЙ СЧЁТЧИК ОБЯЗАН БЫТЬ НАПЕЧАТАН.
//
// ⚠ ПРОБА ПО ОТРАЖЕНИЮ, А НЕ ПО СПИСКУ ИМЁН, И ЭТО НЕСУЩЕЕ. Дефект был ШВОМ МЕЖДУ ДВУМЯ ВОЛНАМИ:
// счётчики завели в разборе (design_construction_draft.go), а список печати живёт в другом файле
// (design_run.go), и коммит его не тронул. Проба, перечисляющая имена руками, — это третий список
// рядом с двумя, и он отстанет ровно так же. Здесь спрашивается сама структура: поле, у которого
// нет своей строки в логе, краснеет в тот же день, когда его добавили.
//
// Двенадцать строк с «est_usage»: «about 2» давали Warn «черновик коэрцирован», у которого ВСЕ
// напечатанные числа равны нулю, — тревога без причины.
func TestConstructionDraftLogPrintsEveryCounter(t *testing.T) {
	var stats designConstructionStats
	v := reflect.ValueOf(&stats).Elem()
	want := make(map[string]int64, v.NumField())
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		require.Equal(t, reflect.Int, f.Type.Kind(), "счётчик %s обязан быть int", f.Name)
		// Числа различимые и заведомо не совпадающие ни с id, ни с токенами ниже.
		n := int64(9000 + i)
		v.Field(i).SetInt(n)
		want[f.Name] = n
	}
	require.Len(t, want, v.NumField())

	sink := tcaCaptureLog(t)
	(&Server{}).designLogConstructionDraft(context.Background(), 0, 0, "stop",
		openrouter.Usage{}, stats, nil)

	require.Len(t, sink.records, 1, "один структурный черновик — одна строка лога")
	printed := make(map[string]struct{}, len(sink.records[0].Attrs))
	for _, val := range sink.records[0].Attrs {
		printed[val] = struct{}{}
	}
	for name, n := range want {
		_, ok := printed[fmt.Sprint(n)]
		require.True(t, ok,
			"счётчик %s (=%d) не напечатан ни одним атрибутом: счётчик без строки лога — "+
				"это статистика, которую никто не видит.\nнапечатано: %v",
			name, n, sink.records[0].Attrs)
	}
}

// TestCoercedCountsOnlyWhatWasChanged — ИМЯ, СОСТАВ И СТРОКА ЛОГА ГОВОРЯТ ОДНО.
//
// Строка лога дословно «design construction draft was coerced». Счётчик того, что мы ПРИНЯЛИ И
// СОХРАНИЛИ, поднимая её, называет ответ чужим именем: модель, добровольно приславшая одну
// выноску, давала Warn на прогоне, в котором коэрцировать было нечего.
func TestCoercedCountsOnlyWhatWasChanged(t *testing.T) {
	var volunteered designConstructionStats
	volunteered.CalloutsUnasked = 3
	require.False(t, volunteered.Coerced(),
		"принятые строки — не поправка: Warn «was coerced» тут был бы неправдой")

	// ⚠ ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ПО КАЖДОМУ ПОЛЮ. Без него «Coerced() всегда false» было бы зелёным.
	var probe designConstructionStats
	v := reflect.ValueOf(&probe).Elem()
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		var one designConstructionStats
		reflect.ValueOf(&one).Elem().Field(i).SetInt(1)
		if name == "CalloutsUnasked" {
			require.False(t, one.Coerced(), "%s считает ПРИНЯТОЕ и не поднимает уровень", name)
			continue
		}
		require.True(t, one.Coerced(),
			"%s — это потеря или поправка, и она обязана поднимать Warn", name)
	}

	// И через живой хендлер: две потери, добавленные волной B-16, обязаны доводить до Warn.
	sink := tcaCaptureLog(t)
	var lost designConstructionStats
	lost.BomEstDropped = 12
	(&Server{}).designLogConstructionDraft(context.Background(), 0, 0, "stop",
		openrouter.Usage{}, lost, nil)
	require.Len(t, sink.records, 1)
	require.Equal(t, "12", sink.records[0].Attrs["bom_est_dropped"],
		"тревога обязана нести своё число, иначе она «что-то пошло не так» без причины")
}

// ─────────────────────────── 3. ОДИН КОД — ОДИН КОЛОРВЕЙ ───────────────────────────

// TestVerifyColourwaysKeepsOneProposalPerColourCode — ПОДТВЕРЖДЕНИЕ КОЛОРВЕЯ СОЗДАЁТ ПРОДУКТ.
//
// `product` держит UNIQUE(style_id, color_code) (entity.ErrColorwayColorExists). Дедуп разбора
// складывает «имя|код» ДО канонизации и поэтому этого не ловит: «Black / Bone» и «Black / Ivory» —
// две разные складки, а канонизируются обе в BLK. Человек подтверждал первое предложение, продукт
// создавался, второе отказывало СЛОВАМИ СЕРВЕРА про занятый код — ровно то, ради предотвращения
// чего проверка ответа и стоит.
func TestVerifyColourwaysKeepsOneProposalPerColourCode(t *testing.T) {
	// Оба имени указывают на один цвет словаря: «black» по имени и «BLK» по коду.
	draft, stats, err := parseConstructionDraft(`{
	  "bom": [{"name":"main fabric"}],
	  "colourways": [
	    {"name":"Black / Bone","color_code":"BLK","slots":[{"slot":"main fabric","colour":"black"}]},
	    {"name":"Black / Ivory","color_code":"black","slots":[{"slot":"main fabric","colour":"black"}]},
	    {"name":"Olive","color_code":"OLV","slots":[{"slot":"main fabric","colour":"olive"}]}
	  ]}`, "stop")
	require.NoError(t, err)
	require.Len(t, draft.GetColourways(), 3,
		"дедуп разбора складывает «имя|код» ДО канонизации и обязан пропустить обе строки")

	designVerifyColourways(draft, designBuildColourDictionary(draftProbeColours()),
		map[string]struct{}{}, &stats)

	require.Len(t, draft.GetColourways(), 3, "строка остаётся — обнуляется только код")
	require.Equal(t, "BLK", draft.GetColourways()[0].GetColorCode(),
		"первое предложение оставляет код за собой: порядок в ответе и есть предпочтение модели")
	require.Equal(t, "", draft.GetColourways()[1].GetColorCode(),
		"второй BLK не подтвердится — предложить его с кодом значит пообещать отказ сервера")
	require.Equal(t, "OLV", draft.GetColourways()[2].GetColorCode(),
		"чужой код не задет")
	require.Equal(t, 1, stats.ColourCodesUnset,
		"снятый код обязан быть посчитан — иначе «модель предлагает один цвет дважды» не видно")

	// ⚠ ВЫБРОШЕННЫЙ КОЛОРВЕЙ НЕ ИМЕЕТ ПРАВА ЗАНЯТЬ КОД. Сверив до строки «выбрасываем», мы обнулили
	// бы код у ЖИВОГО предложения ради соседнего, которого в ответе уже нет.
	dropped, dstats, err := parseConstructionDraft(`{
	  "bom": [{"name":"main fabric"}],
	  "colourways": [
	    {"name":"","color_code":"BLK","slots":[{"slot":"nothing on this card","colour":"black"}]},
	    {"name":"Black","color_code":"BLK","slots":[{"slot":"main fabric","colour":"black"}]}
	  ]}`, "stop")
	require.NoError(t, err)
	designVerifyColourways(dropped, designBuildColourDictionary(draftProbeColours()),
		map[string]struct{}{}, &dstats)
	require.Len(t, dropped.GetColourways(), 1, "безымянный и без привязанных слотов — подтверждать нечего")
	require.Equal(t, "BLK", dropped.GetColourways()[0].GetColorCode(),
		"код, освободившийся вместе с выброшенной строкой, остаётся свободным")
}

// ─────────────────────────── 4. ПОТОЛОК ОТВЕТА ───────────────────────────

// TestConstructionAnswerCeilingHoldsTheWorstRealisticAnswer — ПОТОЛОК СЧИТАЕТСЯ ИЗ ПОТОЛКОВ.
//
// `finish_reason=length` — это не «ответ подрезан», а ОТКАЗ ВСЕГО ПРОГОНА (designFailDraftAs), и
// отказ ОПЛАЧЕННЫЙ: токены поставщик уже напечатал. То есть тесный потолок ничего не экономит, он
// покупает ноль за полную цену.
//
// ⚠ ХУДШИЙ СЛУЧАЙ СОБИРАЕТСЯ ИЗ САМИХ КОНСТАНТ, А НЕ ВЫПИСЫВАЕТСЯ ЧИСЛОМ. Дефект был именно в
// расхождении: три волны добавили в ответ списки (колорвеи по 15 цветов, две колонки оценки на
// каждой строке спеки, до 15 выносок), а потолок никто не двигал. Проба, знающая размер ответа
// числом, отстала бы ровно так же — здесь она пересчитывает его при КАЖДОЙ правке любого потолка.
func TestConstructionAnswerCeilingHoldsTheWorstRealisticAnswer(t *testing.T) {
	// ~4.3 знака на слово — измеренная средняя длина английского слова в такой прозе.
	words := func(n int) string {
		src := strings.Fields("the front placket is closed with five metal snaps set on a folded " +
			"facing and the seam is topstitched twice at three millimetres from the edge to keep " +
			"the fold flat under wear")
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, src[i%len(src)])
		}
		return strings.Join(out, " ")
	}

	names := make([]string, 0, designConstructionMaxBom)
	bom := make([]any, 0, designConstructionMaxBom)
	for i := 0; i < designConstructionMaxBom; i++ {
		n := fmt.Sprintf("main fabric shell panel %d", i)
		names = append(names, n)
		bom = append(bom, map[string]any{
			"section": "fabric", "purpose": "main", "kind": "", "name": n,
			"composition": "80% cotton, 20% polyamide", "colour": "off white",
			"pantone": "11-0601 TCX", "est_usage": 1.625, "unit": "m",
		})
	}
	aspects := make([]any, 0, designConstructionMaxAspects)
	for i := 0; i < designConstructionMaxAspects; i++ {
		// Правило 5 промпта: «at most 60 words each».
		aspects = append(aspects, map[string]any{"key": "extraDetails", "text": words(60)})
	}
	colourways := make([]any, 0, designConstructionMaxColourways)
	for i := 0; i < designConstructionMaxColourways; i++ {
		// Правило 9: «naming EVERY cloth slot» — потолок и есть худший случай.
		slots := make([]any, 0, designConstructionMaxColourwaySlots)
		for s := 0; s < designConstructionMaxColourwaySlots; s++ {
			slots = append(slots, map[string]any{
				"slot": names[s%len(names)], "pantone": "19-4005 TCX",
				"hex": "#101010", "colour": "washed black",
			})
		}
		colourways = append(colourways, map[string]any{
			"name": "Black / Bone / Ecru", "color_code": "BLK",
			"pantone": "19-4005 TCX", "hex": "#101010", "slots": slots,
		})
	}
	missing := make([]any, 0, designConstructionMaxMissing)
	for i := 0; i < designConstructionMaxMissing; i++ {
		missing = append(missing, words(18))
	}

	answer := map[string]any{
		"silhouette": words(40), "fabric": words(40), "fit": "oversized", "concept": "",
		"aspects": aspects, "bom": bom, "colourways": colourways, "missing": missing,
	}

	compact, err := json.Marshal(answer)
	require.NoError(t, err)
	// ⚠ ОТСТУПЫ СЧИТАЮТСЯ. json-режим их не запрещает, и модели их ставят; ответ с отступами
	// упирается в тот же потолок, что и плотный.
	pretty, err := json.MarshalIndent(answer, "", "  ")
	require.NoError(t, err)

	// 3 ЗНАКА НА ТОКЕН — НИЖНЯЯ ГРАНИЦА ОТНОШЕНИЯ, а не среднее: у JSON с короткими ключами и
	// плотной пунктуацией токен дешевле, чем у английской прозы (~4). Считать по среднему значило
	// бы занизить худший случай ровно там, где он и важен.
	const bytesPerToken = 3
	worst := len(pretty) / bytesPerToken
	t.Logf("худший ответ по всем потолкам: плотный %d Б ≈ %d токенов, с отступами %d Б ≈ %d токенов",
		len(compact), len(compact)/bytesPerToken, len(pretty), worst)

	require.Greater(t, worst, 3000,
		"положительный контроль: если худший случай вдруг влезает в 3000, замер построен неверно")
	require.GreaterOrEqual(t, designConstructionMaxTokens, worst,
		"потолок ответа (%d) меньше худшего ответа по нашим же потолкам (≈%d токенов): "+
			"finish_reason=length отказывает во ВСЁМ прогоне и всё равно платит",
		designConstructionMaxTokens, worst)
}

// TestConstructionCeilingBuysTheAnswerAndNotTheThinking — ПОТОЛОК И ВЫКЛЮЧЕННОЕ МЫШЛЕНИЕ — ОДИН
// ДОГОВОР. Замер выше считает ТОКЕНЫ ОТВЕТА; думающая модель тратит рассуждения из этого же
// бюджета и ДО ответа, поэтому потолок без выключенного мышления делает замер бессмысленным.
func TestConstructionCeilingBuysTheAnswerAndNotTheThinking(t *testing.T) {
	require.Positive(t, designConstructionMaxTokens)
	// Связь живёт в openrouter: кто ставит потолок, тот и получает reasoning:{effort:"none"}.
	// Здесь прибита ровно наша половина — что потолок вообще уезжает на структурной ветке.
	require.NotEqual(t, 0, designConstructionMaxTokens,
		"ветка без потолка не ловит обрезанный ответ вовсе")
}

// ─────────────────────────── общая сборка ───────────────────────────

// TestConstructionDraftStillRoundTripsAfterTheAudit — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ НА ВЕСЬ ФАЙЛ.
//
// Все четыре починки трогают путь, по которому едет ОПЛАЧЕННЫЙ ответ. Круг «маршалер → строка →
// тот же разбор» держит обещание идемпотентного повтора, и он обязан сойтись после них.
func TestConstructionDraftStillRoundTripsAfterTheAudit(t *testing.T) {
	in := &pb_common.DesignConstructionDraft{
		Bom: []*pb_common.DesignConstructionBomLine{{
			Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_TRIM,
			Name:    "fusible interlining",
			Unit:    "m2",
		}},
		Colourways: []*pb_common.DesignColourwayProposal{
			{Name: "Black / Bone", ColorCode: "BLK"},
			{Name: "Black / Ivory"},
		},
	}
	stored, err := designMarshalConstructionDraft(in)
	require.NoError(t, err)
	back := designConstructionDraftFromRun(string(stored))
	require.NotNil(t, back)
	require.Equal(t, "m2", back.GetBom()[0].GetUnit(),
		"единица площади обязана пережить круг ровно тем же словом")
	require.Len(t, back.GetColourways(), 2)
	require.Equal(t, "", back.GetColourways()[1].GetColorCode())
}

// ─────────────────────────── 5. ПОТОЛОК И ВРЕМЯ ───────────────────────────

// TestConstructionCeilingCanPhysicallyArrive — ОТВЕТ, КОТОРЫЙ ПОТОЛОК РАЗРЕШИЛ, ОБЯЗАН УСПЕВАТЬ
// ПРИЕХАТЬ. Это та половина, которой у круга 20 не было вовсе: соседняя проба
// (TestConstructionAnswerCeilingHoldsTheWorstRealisticAnswer) сверяет потолок с РАЗМЕРОМ ответа и
// ни разу — со ВРЕМЕНЕМ, за которое этот размер печатается.
//
// ⚠ ЧТО ИМЕННО ЗДЕСЬ СЛУЧИЛОСЬ. Потолок подняли 3000 → 8000 в одиночку, а транспорт держал 60 s на
// весь вызов. 8000 / 60 = 133 ток/с — вдвое быстрее единственного замера, который у нас есть
// (openrouter.go: 2500 токенов за 42 s ≈ 60 ток/с). То есть с того дня полный ответ обрывался на
// 60-й секунде, ошибка приезжала ТРАНСПОРТНАЯ, а designFailDraft закрывал попытку ценой NULL:
// поставщик выставил счёт за 22k входных и 5–7k выходных токенов, регистр записал НОЛЬ, человек
// увидел «погода, повтори» — и повторил.
//
// ⚠ ПРОБА СПРАШИВАЕТ ТРАНСПОРТ ТЕМ ЖЕ ЧИСЛОМ, КОТОРОЕ КЛАДЁТ НА ПРОВОД. Не «60 s ли там» — это
// сверяло бы константу с константой, — а «какую скорость печати требует наш потолок при том
// бюджете, который транспорт ему даёт». Ответ обязан быть НЕ БЫСТРЕЕ замеренной скорости.
func TestConstructionCeilingCanPhysicallyArrive(t *testing.T) {
	// Замер живёт в openrouter (analysisReasoningEffort): 2500 токенов завершения за 42 s.
	// Переписан здесь ЧИСЛАМИ ЗАМЕРА, а не ссылкой на константу: проба, взявшая скорость у того же
	// кода, который проверяет, зеленела бы при любом значении.
	const measuredTokens, measuredSeconds = 2500.0, 42.0
	measuredRate := measuredTokens / measuredSeconds // ≈ 59.5 ток/с

	// База — та, что живёт на бете и на проде: OPENROUTER_HTTP_TIMEOUT не задан ни в одном спеке.
	budget := openrouter.DefaultCompletionBudget(designConstructionMaxTokens)
	required := float64(designConstructionMaxTokens) / budget.Seconds()
	t.Logf("потолок %d токенов, бюджет вызова %s → требуется %.1f ток/с при замеренных %.1f",
		designConstructionMaxTokens, budget, required, measuredRate)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: при прежнем фиксированном бюджете в 60 s этот же потолок требовал бы
	// скорости ВЫШЕ замеренной. Без этой строки проба зеленела бы и на потолке в сто токенов, то
	// есть не доказывала бы ничего про связь.
	require.Greater(t, float64(designConstructionMaxTokens)/60.0, measuredRate,
		"положительный контроль: при 60 s этот потолок был недостижим — если нет, замер построен неверно")

	require.LessOrEqual(t, required, measuredRate,
		"потолок ответа (%d токенов) требует %.1f ток/с, а замерено %.1f: разрешённый ответ не успеет "+
			"приехать, обрыв придёт транспортной ошибкой и попытка закроется ценой NULL",
		designConstructionMaxTokens, required, measuredRate)

	// И ВТОРАЯ ПОЛОВИНА СВЯЗИ: потолок обязан ПОКУПАТЬ время, а не просто помещаться в чужое.
	require.Greater(t, budget, openrouter.DefaultCompletionBudget(0),
		"бюджет вызова не вырос от потолка — значит время и потолок снова два независимых числа")
}

// ─────────────────────────── 6. ПОТОЛОК И ЦЕНА ───────────────────────────

// TestConstructionBasePricesTheWholeAnswerCeiling — ЦЕНА ОБЯЗАНА ПОКРЫВАТЬ ВЕСЬ РАЗРЕШЁННЫЙ ОТВЕТ.
//
// `est` здесь не отчёт: он и РЕЗЕРВИРУЕТ день, и СПИСЫВАЕТСЯ как ФАКТ (FinishAttempt, и каждый
// designFailDraftAs). Доктрина блока цен сказана вслух: каждое число там — ВЕРХНЯЯ ГРАНИЦА рода,
// «заниженный факт врёт про потраченное навсегда». Потолок в 8000 токенов — это разрешение
// поставщику напечатать 8000 токенов; значит верхняя граница обязана быть посчитана по ним.
//
// ⚠ ТАРИФ ЗДЕСЬ ВЫПИСАН СВОЕЙ КОПИЕЙ, И ЭТО НАРОЧНО. Взяв designChatUSDPerMTokOut, проба сверяла бы
// формулу с собою и зеленела бы в тот день, когда выходной тариф случайно поправят на входной —
// ровно пятикратное занижение того слагаемого, которое здесь и растёт.
func TestConstructionBasePricesTheWholeAnswerCeiling(t *testing.T) {
	// $15 за миллион выходных токенов — класс Sonnet у Anthropic (входной $3, выходной впятеро).
	outUSDPerMTok := decimal.RequireFromString("15")
	floor := outUSDPerMTok.Mul(decimal.NewFromInt(int64(designConstructionMaxTokens))).
		Div(decimal.NewFromInt(1_000_000))
	t.Logf("потолок %d токенов → пол цены %s; база структурной ветки %s",
		designConstructionMaxTokens, floor.String(), designDraftIdeaConstructionBaseUSD.String())

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: прежний литерал 0.035 этот пол НЕ проходил. Без него проба зеленела
	// бы на любом потолке, лишь бы база была велика — и не говорила бы ничего про их связь.
	require.True(t, decimal.RequireFromString("0.035").LessThan(floor),
		"положительный контроль: литерал круга 20 обязан быть НИЖЕ пола, иначе пол построен неверно")

	require.True(t, designDraftIdeaConstructionBaseUSD.GreaterThanOrEqual(floor),
		"база структурной ветки (%s) не покрывает даже сам потолок ответа (%s): "+
			"это число и резервирует, и списывается — занижение уходит в регистр навсегда",
		designDraftIdeaConstructionBaseUSD.String(), floor.String())

	// И ВХОДНЫЕ ТОКЕНЫ САМОГО ПРОМПТА СВЕРХ ОТВЕТА: база покрывает не только печать.
	require.True(t, designDraftIdeaConstructionBaseUSD.GreaterThan(floor),
		"база обязана покрывать и входные токены промпта, а не только ответ")

	// СТРУКТУРНАЯ БАЗА — ТА, ЧТО СТОИТ В ТАБЛИЦЕ РОДОВ: занизив её, дверь резервирует меньше траты.
	require.True(t, designDraftIdeaBaseUSD.GreaterThanOrEqual(designDraftIdeaConstructionBaseUSD),
		"таблица родов обязана держать ПОТОЛОК двух баз")
}
