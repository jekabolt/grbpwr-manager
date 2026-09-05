package admin

// ДВА КЛЮЧА ОЦЕНКИ У ЧЕРНОВИКА КОНСТРУКЦИИ (B-16): «сколько примерно уйдёт» и «в чём».
//
// ЧТО ЗДЕСЬ ПРИБИТО И ПОЧЕМУ ИМЕННО ЭТО:
//
//  1. КРУГОВОЙ РЕЙС ЧЕРЕЗ НАШ СОБСТВЕННЫЙ КАНОНИЧЕСКИЙ JSON. `google.type.Decimal` — обычное
//     сообщение, и protojson пишет его ОБЪЕКТОМ `{"value":"1.6"}`. Голый designLoose прочитал бы
//     это как «на месте скаляра приехала структура», то есть идемпотентный повтор терял бы оценку
//     у каждой строки — молча, и ещё записывал бы свою потерю в счётчик дрейфа модели.
//  2. ЗНАЧЕНИЕ СНИМАЕТСЯ, СТРОКА ОСТАЁТСЯ. «about 2» — это не число, но слот с ролью и составом
//     полезен и без оценки.
//  3. ЕДИНИЦА — ТОТ ЖЕ СЛОВАРЬ, ЧТО У КОЛОНКИ. Незнакомое написание отдаётся пустым, а не сырым:
//     `unit` лежит внутри ПОДПИСАННОГО дайджеста MATERIALS.
//  4. ПРОМПТ ДЕЙСТВИТЕЛЬНО СПРАШИВАЕТ. Ключ, которого нет в контракте, модель не заполнит, и
//     разбор будет вечно зелёным на пустоте.
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ФАЙЛ ПРОВЕРЕН (каждая прогнана и откачена, ни одна не осталась зелёной):
//  1. `EstUsage designLoose` вместо `designRawDecimal` — краснеет
//     TestConstructionDraftEstUsageSurvivesTheCanonicalRoundTrip (оценка теряется на повторе);
//  2. `designBoundedDecimal` возвращает значение и при ошибке разбора — краснеет
//     TestConstructionDraftEstUsageDropsWhatIsNotANumber;
//  3. `designUnitToken` отдаёт сырой токен вместо пустого при промахе словаря — краснеет
//     TestConstructionDraftUnitSpeaksTheColumnsVocabulary;
//  4. убрать «est_usage» из контракта системного промпта — краснеет
//     TestConstructionPromptAsksForTheEstimateAndForThreadAndHardware.

import (
	"strings"
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/proto"
)

// euDraftLine — одна строка спеки из разобранного ответа. Положительный контроль всех проб ниже:
// без него «значение снято» зеленело бы и на разборе, который не принимает ничего вовсе.
func euDraftLine(t *testing.T, bomJSON string) (*pb_common.DesignConstructionBomLine, designConstructionStats) {
	t.Helper()
	draft, stats, err := parseConstructionDraft(`{"bom": [`+bomJSON+`]}`, "stop")
	require.NoError(t, err)
	require.NotNil(t, draft)
	require.Len(t, draft.Bom, 1, "строка спеки обязана пережить разбор оценки")
	return draft.Bom[0], stats
}

// TestConstructionDraftReadsTheEstimateAndItsUnit — оба ключа доезжают, в обеих формах, в которых
// модели пишут число.
func TestConstructionDraftReadsTheEstimateAndItsUnit(t *testing.T) {
	line, stats := euDraftLine(t,
		`{"section":"fabric","purpose":"main","name":"main fabric","est_usage":1.6,"unit":"m"}`)
	require.NotNil(t, line.EstUsage, "число, написанное числом, обязано доехать")
	require.Equal(t, "1.6", line.EstUsage.Value)
	require.Equal(t, "m", line.Unit)
	require.Zero(t, stats.BomEstDropped)
	require.Zero(t, stats.UnitsUnset)
	require.Zero(t, stats.NonScalars, "скаляр не имеет права считаться структурой")

	line, _ = euDraftLine(t,
		`{"section":"hardware","kind":"zipper","name":"front zip","est_usage":"1","unit":"PCS"}`)
	require.Equal(t, "1", line.EstUsage.Value, "то же число строкой — тот же ответ")
	require.Equal(t, "pcs", line.Unit, "регистр складывается словарём")

	// Ноль — законный ответ «нисколько», а не отсутствие оценки.
	line, stats = euDraftLine(t, `{"section":"fabric","name":"main fabric","est_usage":0}`)
	require.NotNil(t, line.EstUsage)
	require.Equal(t, "0", line.EstUsage.Value)
	require.Zero(t, stats.BomEstDropped)
}

// TestConstructionDraftEstUsageDropsWhatIsNotANumber — СНИМАЕТСЯ ЗНАЧЕНИЕ, А НЕ СТРОКА. Технологу
// нужнее слот без оценки, чем отсутствие слота, который модель увидела верно и оценила словами.
func TestConstructionDraftEstUsageDropsWhatIsNotANumber(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"запятая вместо точки", `"1,6"`},
		{"проза", `"about 2"`},
		{"диапазон", `"1.5-2 m"`},
		{"единица в числе", `"1.6 m"`},
		{"отрицательное", `-1`},
		{"четвёртый знак — колонка его не хранит", `1.6001`},
		{"промах разряда", `1250000`},
		{"структура на месте числа", `{"low":1,"high":2}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, stats := euDraftLine(t,
				`{"section":"fabric","name":"main fabric","est_usage":`+tc.raw+`,"unit":"m"}`)
			require.Nil(t, line.EstUsage, "неразбираемое число не имеет права доехать")
			require.Equal(t, "main fabric", line.Name, "строка спеки обязана остаться")
			require.Equal(t, "m", line.Unit, "единица снимается отдельно от числа")
			require.Positive(t, stats.BomEstDropped+stats.NonScalars,
				"потеря обязана быть посчитана — иначе «модель отвечает не числом» не видно в логе")
		})
	}
}

// TestConstructionDraftUnitSpeaksTheColumnsVocabulary — единица ложится в `tech_card_bom_item.unit`,
// свободный текст ВНУТРИ подписанного дайджеста MATERIALS. Слово, которого наш словарь не знает,
// сдвинуло бы отпечаток строки ради догадки, поэтому отдаётся пустым и считается.
func TestConstructionDraftUnitSpeaksTheColumnsVocabulary(t *testing.T) {
	line, stats := euDraftLine(t,
		`{"section":"fabric","name":"main fabric","est_usage":1.6,"unit":"yd"}`)
	require.Empty(t, line.Unit, "«yd» словарю неизвестен — пустая единица, а не сырое слово")
	require.Equal(t, 1, stats.UnitsUnset)
	require.NotNil(t, line.EstUsage, "число остаётся: клиент покажет семейное умолчание")

	// ⚠ ПОЛНОЕ ИМЯ ЧЛЕНА — ЭТО ТЕРПИМОСТЬ К ОТВЕТУ МОДЕЛИ, А НЕ ФОРМА НАШЕГО КАНОНА, И ПРЕЖНЯЯ
	// ФОРМУЛИРОВКА ЗДЕСЬ БЫЛА НЕВЕРНОЙ. `unit` в проводе — обычный `string` (design.proto), его
	// пишет designUnitToken, а тот отдаёт КОРОТКОЕ имя в нижнем регистре: канон хранит «"unit":"m"»
	// и никогда «MATERIAL_UNIT_M». Полными именами protojson пишет СОСЕДЕЙ — section/purpose/kind,
	// которые энумы и есть. Строка ниже держит не круг повтора (его держит
	// TestConstructionDraftEstUsageSurvivesTheCanonicalRoundTrip), а один узкий случай: модель,
	// взявшая слово из энума, а не из списка в промпте.
	line, _ = euDraftLine(t,
		`{"section":"thread","name":"sewing thread","est_usage":"150","unit":"MATERIAL_UNIT_M"}`)
	require.Equal(t, "m", line.Unit)

	// Единица без числа законна — и не считается потерей.
	line, stats = euDraftLine(t, `{"section":"fabric","name":"main fabric","unit":"kg"}`)
	require.Equal(t, "kg", line.Unit)
	require.Nil(t, line.EstUsage)
	require.Zero(t, stats.BomEstDropped, "отсутствие числа — не выброшенное число")
}

// TestConstructionDraftEstUsageSurvivesTheCanonicalRoundTrip — ПОВТОР, ЗА КОТОРЫЙ УЖЕ ЗАПЛАЧЕНО.
//
// Второй клик модель не зовёт: читается сохранённый канонический JSON тем же разбором. protojson
// пишет google.type.Decimal ОБЪЕКТОМ, и это единственное поле черновика такой формы — та самая
// асимметрия «писатель против читателя», которая молчит у писателя и теряет данные у читателя.
func TestConstructionDraftEstUsageSurvivesTheCanonicalRoundTrip(t *testing.T) {
	in := &pb_common.DesignConstructionDraft{
		Bom: []*pb_common.DesignConstructionBomLine{{
			Section:  pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
			Purpose:  pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_MAIN,
			Name:     "main fabric",
			EstUsage: &pb_decimal.Decimal{Value: "1.6"},
			Unit:     "m",
		}, {
			Section: pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD,
			Name:    "sewing thread",
			Unit:    "m",
		}},
	}

	stored, err := designMarshalConstructionDraft(in)
	require.NoError(t, err)
	require.Contains(t, string(stored), `"est_usage"`,
		"писатель обязан хранить ключ — иначе круг сходится на пустоте")

	back := designConstructionDraftFromRun(string(stored))
	require.NotNil(t, back, "сохранено %d байт: %s", len(stored), stored)
	require.True(t, proto.Equal(in, back),
		"круг не сошёлся.\nхранилось: %s\nпрочитано: %v", stored, back)

	// Именно оценка, а не «что-нибудь»: proto.Equal выше покраснел бы и от чужого поля.
	require.NotNil(t, back.Bom[0].EstUsage)
	require.Equal(t, "1.6", back.Bom[0].EstUsage.Value)
	require.Nil(t, back.Bom[1].EstUsage, "незаполненная оценка возвращается отсутствующей, а не нулём")
}

// TestConstructionPromptAsksForTheEstimateAndForThreadAndHardware — ключ, которого нет в контракте,
// модель не заполнит, и весь разбор выше будет вечно зелёным на пустоте.
func TestConstructionPromptAsksForTheEstimateAndForThreadAndHardware(t *testing.T) {
	p := designConstructionSystemPrompt
	require.Contains(t, p, `"est_usage": number`, "контракт ответа обязан нести ключ оценки")
	require.Contains(t, p, `"unit": string`, "и ключ единицы")
	require.Contains(t, p, `"composition" is written as "NN% fibre, NN% fibre"`,
		"форма состава — то, что читают оба наших парсера состава")
	require.Contains(t, p, `always includes one "thread" line`, "нитка есть на каждом изделии (B-19)")
	require.Contains(t, p, "never add hardware the pictures do not show",
		"фурнитура — только видимая (B-19), это уточнение правила 1, а не отмена его")

	// Правило 9 остаётся колорвеями: правила не перенумеровываются, иначе сохранённый прогон
	// читается человеком неверно.
	require.Contains(t, p, `9. "colourways"`)
	require.Contains(t, p, `10. "bom" always includes`)

	// Словарь единиц уезжает в пользовательский промпт — иначе «unit» спрашивается без списка.
	require.Contains(t, designUnitTokens, "m")
	require.Contains(t, designUnitTokens, "pcs")
	require.False(t, strings.Contains(strings.Join(designUnitTokens, ","), "unknown"),
		"нулевой член словаря — это «не задано», а не значение, и предлагать его нельзя")
}
