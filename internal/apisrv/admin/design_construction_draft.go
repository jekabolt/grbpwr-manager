package admin

// СТРУКТУРНЫЙ ЧЕРНОВИК КОНСТРУКЦИИ — ВТОРАЯ ФОРМА ОТВЕТА ТОГО ЖЕ ПЛАТНОГО ПРОГОНА.
//
// ЧТО ЭТО. Владелец (круг 19, пункт 9, дословно): «Всё, чем наполнен мудборд — картинки + указания
// + CONCEPT & CONSTRUCTION DESCRIPTION — должно попадать в промпт. Внизу вместо кнопки
// `DRAFT THE IDEA ▸` мы генерируем ВЕСЬ construction info на основании того, что знаем». Кнопка
// уже была мультимодальным, платным, идемпотентным прогоном, читающим ровно эти три входа; ей
// не хватало (i) структурного ответа и (ii) места, куда он ложится, кроме `concept`. Этот файл —
// первая половина: ФОРМА ОТВЕТА и его ПРОВЕРКА. Вторая половина (приём по строкам) живёт на
// клиенте и ничего сюда не пишет.
//
// ⚠ ЧЕТЫРЕ РЕШЕНИЯ, БЕЗ КОТОРЫХ ЭТОТ ФАЙЛ ЧИТАЕТСЯ НЕВЕРНО:
//
//  1. ЭТО ФЛАГ НА СТАРОМ ГЛАГОЛЕ, А НЕ НОВЫЙ ГЛАГОЛ И НЕ НОВЫЙ РОД ПРОГОНА. Отсутствующий флаг
//     обязан давать ПРЕЖНИЕ БАЙТЫ запроса и прежнюю прозу в `output_text` — клиент, который о
//     флаге не знает, продолжает резать ответ по трём заголовкам. Новый род (`kind`) прошёл бы
//     рябью по каждой клиентской таблице родов и не сказал бы там ничего нового: это по-прежнему
//     один текстовый прогон по доске, в том же денежном регистре и с той же идемпотентностью.
//
//  2. ХРАНИТСЯ ПРОВЕРЕННЫЙ КАНОНИЧЕСКИЙ JSON, А НЕ ОТВЕТ МОДЕЛИ. Идемпотентный повтор отдаёт
//     СОХРАНЁННУЮ строку и модель не зовёт; всё, чего нельзя восстановить из `output_text`,
//     исчезло бы при втором нажатии той же кнопки. Поэтому в `output_text` уезжает protojson
//     ЭТОГО ЖЕ сообщения — то, что получил клиент, а не то, что напечатала модель.
//
//  3. КОЭРЦИЯ ПРОТИВ ОТКАЗА — ГРАНИЦА ПРОХОДИТ ПО ФОРМЕ, А НЕ ПО СОДЕРЖАНИЮ (заимствовано у
//     разбора тех-карты, techcardanalysis.VerifyModelRun). Узнаваемый дрейф написания
//     («Collar», «sleeve cuff», «FABRIC») приводится к нашему словарю молча; неузнаваемое —
//     выбрасывается по одной строке, а не роняет весь оплаченный прогон. Ронять целиком имеет
//     право ровно одно: ответ НЕ ТОЙ ФОРМЫ (нет JSON, нет ни одного ключа) и ответ, ОБРЕЗАННЫЙ
//     потолком токенов (`finish_reason=length`) — половина черновика неотличима от полного и
//     выглядела бы как «модель этого не увидела».
//
//  4. МОДЕЛЬ НЕ НАЗЫВАЕТ НАШИХ ИДЕНТИФИКАТОРОВ. Выноска рождается БЕЗ номера и без пина (номер
//     минтит сервер на сейве, пин ставит человек на картинке), а `material_id` на этой фазе
//     ПРИНУДИТЕЛЬНО ноль: каталог в промпт не уезжает (это фаза 4), значит подтвердить артикул
//     нечем, а строка, выглядящая связанной и оценённой, но указывающая на чужой артикул, — это
//     ошибка себестоимости с ценником.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ─────────────────────────── потолки и словари ───────────────────────────

const (
	// designConstructionMaxTokens — потолок ответа. Взят у разбора тех-карты по той же причине:
	// без потолка одна доска однажды выкупает ответ на тысячи строк, а с потолком обрезанный
	// ответ ЛОВИТСЯ (finish_reason=length) вместо того, чтобы приехать половиной черновика.
	designConstructionMaxTokens = 3000

	// designConstructionMaxLongRunes — потолок «длинных» полей: силуэт, ткань, замысел. Ровно тот,
	// что у соответствующих текстовых полей карточки (CONCEPT_MAX = 2000 на клиенте), потому что
	// принятая строка едет именно туда и большая просто не сохранилась бы.
	designConstructionMaxLongRunes = 2000
	// designConstructionMaxTextRunes — потолок остальных строк: аспект, выноска, строка спеки,
	// «что стоит приколоть». Это ОДНА мысль на строку, а не абзац.
	designConstructionMaxTextRunes = 500
	// ─── ПОТОЛКИ КОЛОНОК. СЧИТАЮТСЯ БАЙТЫ, А НЕ РУНЫ, И ЭТО НЕ ПЕДАНТИЗМ ───
	//
	// ⚠ РЕВЬЮ КРУГА 19: ШЕСТЬ ПОЛЕЙ ЕХАЛИ С ПОТОЛКОМ 500 РУН В КОЛОНКИ VARCHAR(255), А PANTONE — БЕЗ
	// ПОТОЛКА ВОВСЕ В VARCHAR(64) (0363). Дальше по маршруту стоят сторожа DTO, и они меряют `len()`,
	// то есть БАЙТЫ: ~85 рун кириллицы уже не влезают в 255. Промах любого из них — не «поле
	// обрезалось», а ОТКАЗ В СОХРАНЕНИИ ВСЕЙ КАРТОЧКИ (UpsertTechCard — всё-или-ничего), либо, у
	// pantone, сырой MySQL 1406, не называющий ни строки, ни поля. Поэтому предложение режется ПО
	// НАЗНАЧЕНИЮ и В ТЕХ ЖЕ ЕДИНИЦАХ, в которых считает сторож.
	//
	// ⚠ ПОТОЛОК РУН ОСТАЁТСЯ ТАМ, ГДЕ КОЛОНКА TEXT (описание выноски, тексты аспектов, «что стоит
	// приколоть»): там ограничение смысловое — «одна мысль на строку», — а не про размер колонки.
	designConstructionMaxVarchar255 = 255 // bom.name/colour/composition, callout.part, callout.dimensions
	designConstructionMaxVarchar64  = 64  // bom.pantone (0363) и detail_key самодельного аспекта

	// Потолки списков. Не вкус: предложение, которое человек обязан просмотреть по строкам, за
	// этими числами перестаёт быть предложением и становится работой.
	// ─── ПОТОЛКИ СЕКЦИИ «УЖЕ НА КАРТОЧКЕ» (входные токены КАЖДОГО нажатия) ───
	//
	// Числа взяты у соседа по смыслу, а не по величине: строка карточки читается моделью ради
	// одного — «этого не предлагай», — и для этого хватает начала строки и первых двух десятков
	// строк каждого списка. Байтовый потолок — последнее слово: он держит СУММУ трёх списков.
	designConstructionMaxAlreadyRows      = 20
	designConstructionMaxAlreadyLineRunes = 200
	designConstructionMaxAlreadyBytes     = 8 << 10 // 8 KiB на всю секцию

	designConstructionMaxAspects  = 10
	designConstructionMaxCallouts = 15
	designConstructionMaxBom      = 15
	designConstructionMaxMissing  = 8

	// ─── ПОТОЛКИ ПРЕДЛОЖЕННЫХ КОЛОРВЕЕВ (B-25) ───
	//
	// ЧЕТЫРЕ, ПОТОМУ ЧТО ВЛАДЕЛЕЦ ПРОСИЛ «НЕСКОЛЬКО», А НЕ «СПИСОК»: подтверждение колорвея
	// СОЗДАЁТ ПРОДУКТ, и предложение, которое человек обязан просмотреть по одному, за четырьмя
	// строками перестаёт быть предложением. Промпт просит 2–4; потолок — последнее слово.
	designConstructionMaxColourways = 4
	// ПЯТНАДЦАТЬ ЦВЕТОВ НА КОЛОРВЕЙ — РОВНО ПОТОЛОК СПЕКИ: слот берётся из строк спеки, и
	// шестнадцатый цвет указывал бы на слот, которого предложение не называло.
	designConstructionMaxColourwaySlots = 15
	// ИМЯ КОЛОРВЕЯ — 64 РУНЫ (сверх этого — потолок колонки tech_card_colorway.dev_name,
	// varchar(255), в байтах). Это ПОДПИСЬ («Black / Bone»), а не описание: пикер колорвеев рисует
	// её в одну строку, и длинное имя не читается ни там, ни в списке продукта.
	designConstructionMaxColourwayNameRunes = 64

	// designConstructionMaxColourRows — СКОЛЬКО СТРОК СЛОВАРЯ ЦВЕТА УЕЗЖАЕТ В ПРОМПТ.
	//
	// Тот же довод и то же место, что у потолка секции «уже на карточке»: список едет во ВХОДНЫХ
	// токенах КАЖДОГО нажатия. Двести строк — ≈4 KiB, ≈1k токенов, ≈$0.003; тысяча строк словаря
	// стоила бы впятеро дороже и не помогла бы модели выбрать. За потолком список не режется, а НЕ
	// ДАЁТСЯ ВОВСЕ, и промпт говорит об этом вслух: половина словаря заставила бы модель выбирать
	// «ближайший код» из набора, который мы ей молча урезали.
	designConstructionMaxColourRows = 200
)

// designConstructionReasonInvalidOutput — машинная причина «ответ не той формы», она же
// `error_code` проваленного прогона. Одно слово на два места: клиент отличает её от провала
// поставщика, не разбирая английскую прозу, а история прогонов — по колонке.
const designConstructionReasonInvalidOutput = "invalid_output"

// designReasonBudgetExhausted — «модель истратила весь бюджет ответа и не ответила».
//
// ⚠ ТРЕТИЙ КОД РЯДОМ С ДВУМЯ, А НЕ СИНОНИМ ОДНОГО ИЗ НИХ. `provider_error` значит «ответа не было»
// (транспорт, 404, неверная настройка), `invalid_output` — «ответ был, и он не той формы». Здесь
// ответ БЫЛ, он пуст, детерминирован и ОПЛАЧЕН токенами завершения; чинит его не дежурный и не
// промпт, а потолок вместе с выключенным мышлением. Слив его в «поставщик падает», мы получили бы
// график аварии там, где мал наш собственный потолок.
const designReasonBudgetExhausted = "budget_exhausted"

// designReasonShapeMismatch — тот же client_request_id пришёл с ПРОТИВОПОЛОЖНЫМ флагом формы.
// Флаг в ключ идемпотентности не входит (он не свойство прогона, а свойство нажатия), поэтому
// расхождение ловится сверкой с уже сохранённой строкой — ровно как расхождение по колорвею.
const designReasonShapeMismatch = "shape_mismatch"

const (
	// Две прозы на два РАЗНЫХ исхода, и различие несущее: первый чинится повтором, второй —
	// уменьшением доски или описания. Одна фраза на оба отправила бы человека жать ту же кнопку
	// до тех пор, пока он не бросит.
	designConstructionShapeRefusalMsg = "the model did not answer in the shape asked for — draft again"
	designConstructionCutRefusalMsg   = "the answer was cut off — fewer pictures or a shorter description, then draft again"
	// ТРЕТЬЯ ПРОЗА НА ТРЕТИЙ ИСХОД: бюджет ответа истрачен целиком, а ответа нет. Чинится тем же
	// жестом, что и обрезанный ответ, — доска поменьше, — но исход другой и путать их нельзя.
	designConstructionBudgetRefusalMsg = "the model used up the whole answer budget without answering — fewer pictures or a shorter description, then draft again"

	// ─── ПРОЗА ПОВТОРА: ПРОГОН УЖЕ ОТВЕЧЕН, И ОТВЕЧЕН ОН ПРОВАЛОМ ───
	//
	// Все три говорят одно и то же действие — НОВОЕ нажатие, — потому что повторить прогон под тем
	// же ключом идемпотентности нельзя: вторая платная попытка под одним ключом сломала бы
	// единственное, ради чего ключ существует.
	designConstructionReplayShapeMsg  = "this draft already failed: the model did not answer in the shape asked for — press draft again to start a new one"
	designConstructionReplayBudgetMsg = "this draft already failed: the model used up the whole answer budget without answering — press draft again to start a new one"
	designDraftReplayFailedMsg        = "this draft already failed — press draft again to start a new one"

	// ФОРМА ОТВЕТА ПРИБИТА К ПРОГОНУ, А НЕ К НАЖАТИЮ: прогон отвечен один раз и навсегда в той
	// форме, в какой был отвечен.
	designConstructionShapeMismatchMsg = "this request has already been answered in the other form — press draft again to start a new one"
)

// designConstructionAspectKeys — СТАНДАРТНЫЕ КЛЮЧИ АСПЕКТОВ, В ПОРЯДКЕ РЕДАКТОРА.
//
// ⚠ ПИШУТСЯ ТАК, КАК ИХ ХРАНИТ КАРТОЧКА, а не так, как их читает человек: `sleeveCuff`, а не
// `sleeve / cuff` и не `sleeve_cuff`. Ключ — это то, по чему клиент делает upsert строки
// `details[]`; ключ «почти тот» родил бы ВТОРУЮ строку рядом с существующей, и на экране один и
// тот же аспект оказался бы дважды.
//
// ⚠ СПИСОК — КОПИЯ КЛИЕНТСКОГО СЛОВАРЯ (components/tech-card-options.ts, detailAspects), И ЭТО
// НАЗВАНО ВСЛУХ, ПОТОМУ ЧТО КОПИЯ — ЭТО ДОЛГ. Серверного источника у него нет: колонка detail_key
// объявлена freeform, и никакой таблицы «законные аспекты» не существует. Пока это так, ключ,
// добавленный на клиенте и не добавленный здесь, не сломается — он просто приедет как
// САМОДЕЛЬНЫЙ, а редактор аспектов их принимает. Обратное («здесь есть, там нет») даёт строку без
// подписи, и это тоже видно глазом, а не молча.
var designConstructionAspectKeys = []string{
	"silhouette",
	"fabric",
	"collar",
	"fastening",
	"pockets",
	"sleeveCuff",
	"topstitching",
	"extraDetails",
	"auxMaterials",
}

// designConstructionAspectByFold — тот же словарь, сложенный для узнавания: «Sleeve / Cuff»,
// «sleeve_cuff» и «sleeveCuff» это один ключ, а не три.
var designConstructionAspectByFold = func() map[string]string {
	m := make(map[string]string, len(designConstructionAspectKeys))
	for _, k := range designConstructionAspectKeys {
		m[designFoldToken(k)] = k
	}
	return m
}()

// designConstructionFits — СЛОВАРЬ ПОСАДКИ.
//
// ⚠ ТОЖЕ КОПИЯ КЛИЕНТСКОГО СПИСКА (components/style-facts-field.tsx, FIT_OPTIONS), и тоже потому,
// что серверного словаря посадки НЕ СУЩЕСТВУЕТ: `tech_card.fit` — свободная строка, факт стиля.
// Он нужен здесь по одной причине: посадка — это ПИКЕР, и значение вне его списка человек не
// сможет принять одним кликом. Слово, которого в списке нет, поэтому не выдумывается и не
// подставляется — оно просто не предлагается вовсе (пустая строка), и карточка остаётся при своём.
var designConstructionFits = []string{
	"regular", "slim", "loose", "relaxed", "skinny", "cropped", "tailored",
}

var designConstructionFitByFold = func() map[string]string {
	m := make(map[string]string, len(designConstructionFits))
	for _, f := range designConstructionFits {
		m[designFoldToken(f)] = f
	}
	return m
}()

// designEnumVocabulary — токены одного enum'а спецификации: карта узнавания и порядок для промпта.
//
// СТРОИТСЯ ИЗ САМОГО ENUM'А, А НЕ ВЫПИСЫВАЕТСЯ РУКАМИ. Довод дословно тот же, что у aiTokenMap:
// переписанный от руки список — это ровно то место, где словарь молча теряет значение, добавленное
// в другом файле, и правильный ответ модели становится UNKNOWN.
//
// ⚠ В КАРТУ КЛАДЁТСЯ И КОРОТКОЕ ИМЯ, И ПОЛНОЕ ИМЯ ЧЛЕНА, и это не щедрость. Коротким («fabric»)
// отвечает модель — так её просит промпт; полным («TECH_CARD_BOM_SECTION_FABRIC») отвечает НАШ
// СОБСТВЕННЫЙ канонический JSON, который тот же разбор читает на идемпотентном повторе. Приняв
// только короткое, повтор терял бы секцию у каждой строки спеки.
//
// НУЛЕВОЙ ЧЛЕН НЕ ПОПАДАЕТ НИ В КАРТУ, НИ В СПИСОК: UNKNOWN/UNSET — это «не задано», ответ, а не
// значение, и предлагать его модели значило бы просить её отвечать «не знаю» словом из словаря.
func designEnumVocabulary[E ~int32](prefix string, values map[string]int32) (map[string]E, []string) {
	byFold := make(map[string]E, len(values))
	type member struct {
		token string
		num   int32
	}
	members := make([]member, 0, len(values))
	for full, num := range values {
		if num == 0 {
			continue
		}
		token := strings.ToLower(strings.TrimPrefix(full, prefix))
		byFold[designFoldToken(token)] = E(num)
		byFold[designFoldToken(full)] = E(num)
		members = append(members, member{token: token, num: num})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].num < members[j].num })
	tokens := make([]string, 0, len(members))
	for _, m := range members {
		tokens = append(tokens, m.token)
	}
	return byFold, tokens
}

var designBomSectionByFold, designBomSectionTokens = designEnumVocabulary[pb_common.TechCardBomSection](
	"TECH_CARD_BOM_SECTION_", pb_common.TechCardBomSection_value)

var designBomPurposeByFold, designBomPurposeTokens = designEnumVocabulary[pb_common.TechCardBomPurpose](
	"TECH_CARD_BOM_PURPOSE_", pb_common.TechCardBomPurpose_value)

var designBomKindByFold, designBomKindTokens = designEnumVocabulary[pb_common.TechCardBomKind](
	"TECH_CARD_BOM_KIND_", pb_common.TechCardBomKind_value)

// designFoldToken складывает написание до узнаваемого ядра: регистр и ВСЯ пунктуация исчезают.
//
// ⚠ ИМЕННО ВСЯ, А НЕ ТОЛЬКО ПРОБЕЛЫ И ДЕФИСЫ (в отличие от соседней normalizeToken). Наши
// собственные ключи живут в camelCase (`sleeveCuff`), модель отвечает snake_case или словами
// («sleeve / cuff»), а канонический JSON — ПРОПИСНЫМИ С ПОДЧЁРКИВАНИЯМИ. Складка, оставляющая
// подчёркивание, развела бы эти три написания на три разных ключа, то есть завела бы у одного
// аспекта три строки.
func designFoldToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ─────────────────────────── системный промпт ───────────────────────────

// designConstructionSystemPrompt — РОЛЬ И ФОРМА ОТВЕТА.
//
// ⚠ ЭТО ВТОРАЯ РОЛЬ, А НЕ ПРАВКА ПЕРВОЙ. draftIdeaSystemPrompt рядом остаётся ДОСЛОВНО тем же:
// его три заголовка — контракт со старым клиентом (V-19), и клиент, который их разбирает,
// продолжает работать ровно до тех пор, пока эти байты не тронуты.
//
// РОЛЬ ГОВОРИТ ПРО КАРТИНКИ, ПОТОМУ ЧТО КАРТИНКИ ПРИЕЗЖАЮТ, и про то, что каждая записка называет
// свою картинку и место на ней: за привязку уже заплачено сборкой промпта, и роль, умалчивающая о
// ней, велела бы модели не пользоваться тем, что ей дали.
//
// ⚠ КРУГ 20, ДВЕ ПРАВКИ, И ОБЕ — ПРО ТО, ЗА ЧТО МЫ ПЛАТИМ ВЫХОДНЫМИ ТОКЕНАМИ.
//
//  1. ВЫНОСОК БОЛЬШЕ НЕ ПРОСЯТ (B-13). Владелец дословно: «DRAFT OF THE CONSTRUCTION не должен
//     добавлять коллауты все это можно добавить в CONSTRUCTION аспектами». Клиент строки выносок
//     всё равно перестал рисовать — но ВЫБРОСИТЬ ИХ ТОЛЬКО НА КЛИЕНТЕ БЫЛО БЫ НЕЧЕСТНО ДВАЖДЫ: мы
//     продолжали бы платить до пятнадцати выходных строк (~$0.01 за нажатие) за то, чего никто не
//     читает, И ТЕРЯЛИ БЫ САМИ ФАКТЫ — швы, застёжки, края, карманы, — вместо того чтобы положить
//     их туда, куда владелец их и адресовал. Поэтому правило 3 переписано на «аспекты», а не
//     удалено, и ключ ушёл из формы ответа. Поле 6 провода и его разбор ОСТАЛИСЬ: сохранённый до
//     этой волны прогон обязан читаться обратно на идемпотентном повторе.
//
//  2. КОЛОРВЕИ СПРАШИВАЮТСЯ ТЕМ ЖЕ ПЛАТНЫМ ПРОГОНОМ (B-25), правило 9. Второй прогон перечитывал бы
//     те же ≤12 картинок (≈$0.06 входных за нажатие) ради вопроса, входы которого — слоты ткани и
//     цвета доски — этот ответ уже держит. Список стоит ≈300–500 выходных токенов ≈ $0.005, и
//     ровно на эту величину подвинута база оценки (designDraftIdeaBaseUSD, 0.03 → 0.035).
const designConstructionSystemPrompt = "You are a garment technologist's assistant. " +
	"You are shown the moodboard pictures, the designer's concept & construction description, and " +
	"the notes pinned on the pictures — every note names its picture by number and the spot it " +
	"marks, so you know exactly which part of which image it refers to.\n" +
	"Answer with ONE JSON object and nothing else — no prose before or after it, no code fence. " +
	"English. The object has exactly these keys:\n" +
	"{\"silhouette\": string, \"fabric\": string, \"fit\": string, \"concept\": string, " +
	"\"aspects\": [{\"key\": string, \"text\": string}], " +
	"\"bom\": [{\"section\": string, \"purpose\": string, \"kind\": string, \"name\": string, " +
	"\"composition\": string, \"colour\": string, \"pantone\": string}], " +
	"\"colourways\": [{\"name\": string, \"color_code\": string, \"pantone\": string, " +
	"\"hex\": string, \"slots\": [{\"slot\": string, \"pantone\": string, \"hex\": string, " +
	"\"colour\": string}]}], " +
	"\"missing\": [string]}\n" +
	"Rules:\n" +
	"1. Never invent a fabric, a colour, a measurement or a piece of hardware that the pictures do " +
	"not show and the notes do not state. Leave the field empty and name what is missing under " +
	"\"missing\" instead.\n" +
	"2. Prefer the designer's own words where they say the same thing.\n" +
	"3. Construction features visible on the pictures — seams, closures, edges, pockets, bindings — " +
	"go into \"aspects\" under the fitting key (fastening, pockets, topstitching, extraDetails, or " +
	"a short custom key); do not list them separately.\n" +
	"4. \"bom\" names components BY THEIR ROLE («main fabric», «neck binding», «care label»), one " +
	"line per component. Use the section / purpose / kind tokens given in the prompt; leave a token " +
	"empty when it does not apply.\n" +
	"5. \"aspects\" use the keys given in the prompt, or a short custom key when none of them fits; " +
	"at most 60 words each.\n" +
	"6. Do not repeat what the card already says — refine it or leave the field empty.\n" +
	"7. Limits: at most 10 aspects, 15 bom lines, 8 missing notes, 4 colourways of at most 15 " +
	"slot colours each.\n" +
	"8. \"concept\" is answered ONLY when the prompt says the card has none; otherwise leave it " +
	"empty.\n" +
	"9. \"colourways\": 2 to 4 colour combinations the pictures and the description support — one " +
	"entry per combination, naming EVERY cloth slot from \"bom\" by its exact \"name\" with a " +
	"Pantone TCX code and a hex; \"color_code\" is the closest code from the colour list in the " +
	"prompt (empty when none is close); never invent a colour the board does not show."

// ─────────────────────────── пользовательский промпт ───────────────────────────

// designConstructionUserPrompt — СЛОВЕСНАЯ ЧАСТЬ ЗАПРОСА структурного черновика.
//
// ⚠ СЕКЦИИ 2 И 3 (замысел и записки, приколотые на картинки) СОБИРАЕТ designBoardPromptBody —
// ТА ЖЕ ФУНКЦИЯ, ЧТО СОБИРАЕТ ИХ ДЛЯ ПРОЗАИЧЕСКОГО ЧЕРНОВИКА. Там живёт привязка «picture N +
// место в долях кадра», ради которой был отдельный круг работы и на которую стоят пробы; вторая
// сборка тех же строк разошлась бы с первой в первый же раз, когда правят одну.
//
// ЧТО ДОБАВЛЕНО СВЕРХУ И ЗАЧЕМ КАЖДОЕ:
//   - ШАПКА ИЗДЕЛИЯ — категория, пол, размерный ряд с отмеченным базовым: без неё модель отвечает
//     про «одежду вообще», и ответ приходится править руками там, где карточка уже знает ответ.
//   - «УЖЕ НА КАРТОЧКЕ» — правило 6 системного промпта («не повторяй») невыполнимо, пока модель не
//     видит, что там написано. Без этой секции половина предложений приезжает дубликатами того,
//     что человек уже набрал, и он платит вниманием за каждую строку.
//   - ТОКЕНЫ — закрытые словари спеки. Модель, которой не показали список, отвечает синонимом, и
//     синоним превращается в UNSET у строки, которая на самом деле была верной.
//   - СЛОВАРЬ ЦВЕТА (B-25) — ровно тот же довод, но с ценником: `color_code` предложенного
//     колорвея обязан быть КОДОМ ИЗ СЛОВАРЯ, потому что CreateColorway без него отказывает.
//     Модель, которой словарь не показали, называет цвет словом, разбор обнуляет код, и человек
//     получает предложение, которое нельзя подтвердить — то есть оплаченный список, ни одна
//     строка которого не доводит до продукта.
//
// ⚠ ЦВЕТА ПРИХОДЯТ ПАРАМЕТРОМ, А НЕ ЧИТАЮТСЯ ЗДЕСЬ. Функция остаётся ЧИСТОЙ: она не ходит ни в
// стор, ни в кэш, и накрывается табличными пробами без обвязки. Тот же список хендлер отдаёт
// проверке ответа (designVerifyColourways) — один список на вопрос и на проверку ответа, потому
// что второе чтение словаря между запросом и разбором дало бы код, которого модели не показывали.
func designConstructionUserPrompt(
	card *entity.TechCard, mood *pb_common.DesignMoodSnapshot, attachedIDs []int,
	colours []entity.Color,
) string {
	var b strings.Builder

	// ─── 1. ШАПКА ИЗДЕЛИЯ ───
	if card != nil {
		if v := strings.TrimSpace(card.Name); v != "" {
			b.WriteString("Garment: " + v + "\n")
		}
		if v := strings.TrimSpace(card.Fit.String); v != "" {
			b.WriteString("Fit: " + v + "\n")
		}
		if v := designCategoryName(card); v != "" {
			b.WriteString("Category: " + v + "\n")
		}
		if v := strings.TrimSpace(card.TargetGender.String); v != "" {
			b.WriteString("Gender: " + v + "\n")
		}
		if v := designSizeRunLine(card); v != "" {
			b.WriteString("Size run: " + v + "\n")
		}
	}

	// ─── 2–3. ЗАМЫСЕЛ И ЗАПИСКИ НА КАРТИНКАХ — ОДНОЙ СБОРКОЙ НА ДВА ПРОМПТА ───
	//
	// ⚠ ВЫЗОВ, А НЕ КОПИЯ И НЕ ВЫРЕЗКА ИЗ ГОТОВОЙ СТРОКИ. Там живёт привязка «picture N + место в
	// долях кадра», ради которой был отдельный круг работы и на которую стоят пробы. Вырезка по
	// заголовку (первый вариант этой функции) держалась бы на том, что заголовок не поправят, —
	// а поправив его, мы вынули бы замысел и записки из платного запроса МОЛЧА.
	b.WriteString(designBoardPromptBody(mood, attachedIDs))

	// ─── 4. УЖЕ НА КАРТОЧКЕ ───
	if already := designCardAlreadySays(card); already != "" {
		b.WriteString("\nAlready on the card — refine, do not repeat:\n" + already)
	}

	// ⚠ ЗАМЫСЕЛ ПРЕДЛАГАЕТСЯ ТОЛЬКО ПУСТОЙ КАРТОЧКЕ, И СКАЗАНО ЭТО ЗДЕСЬ, А НЕ В РОЛИ: роль одна
	// на все карточки, а условие — про ЭТУ. Слова дизайнера старше слов модели, и предложение,
	// соперничающее с ними, попросило бы человека защищать то, что он уже написал.
	if card != nil && strings.TrimSpace(card.Concept.String) == "" {
		b.WriteString("\nThe card has no concept & construction description yet: propose one in \"concept\".\n")
	} else {
		b.WriteString("\nThe card already has a concept & construction description: leave \"concept\" empty.\n")
	}

	// ─── 5. ТОКЕНЫ ───
	b.WriteString("\nTokens — use these spellings exactly:\n")
	b.WriteString("aspect keys: " + strings.Join(designConstructionAspectKeys, ", ") +
		" (or a short custom key when none fits)\n")
	b.WriteString("bom sections: " + strings.Join(designBomSectionTokens, ", ") + "\n")
	b.WriteString("bom purposes (roll goods only): " + strings.Join(designBomPurposeTokens, ", ") + "\n")
	b.WriteString("bom kinds (hardware / trims / decoration only): " +
		strings.Join(designBomKindTokens, ", ") + "\n")
	b.WriteString("fit: " + strings.Join(designConstructionFits, ", ") + "\n")
	b.WriteString(designColourTokenLine(colours))

	// ⚠ КАТАЛОГА НЕТ, И ЭТО ГОВОРИТСЯ ВСЛУХ. Модель, которой не сказали, что артикулов ей не дали,
	// охотно придумает `material_id`; разбор его всё равно обнулит, но потраченные на выдумку
	// выходные токены обнулить нельзя.
	b.WriteString("\nNo materials catalogue is given: never invent an article id.\n")

	return strings.TrimSpace(b.String())
}

// designColourTokenLine — СТРОКА СЛОВАРЯ ЦВЕТА ДЛЯ ПРОМПТА, ИЛИ ЧЕСТНОЕ «СПИСКА НЕТ» (B-25).
//
// ⚠ ТРИ ИСХОДА, И ТОЛЬКО ОДИН ИЗ НИХ — СПИСОК. Пустой словарь и словарь длиннее потолка дают ОДНУ
// И ТУ ЖЕ строку «списка нет — оставь color_code пустым», потому что вопрос, который она закрывает,
// один: «есть ли у модели чем выбрать код». Урезанный список был бы третьим, ХУДШИМ ответом —
// модель выбирала бы «ближайший код» из набора, который мы молча обрезали, и её выбор выглядел бы
// таким же уверенным, как настоящий.
//
// ⚠ АРХИВНЫЕ ЦВЕТА НЕ ЕДУТ ВОВСЕ (ListColors(ctx,false) у вызывающего): архивный код нельзя дать
// новому продукту, и предложение с ним нельзя подтвердить.
func designColourTokenLine(colours []entity.Color) string {
	if len(colours) == 0 || len(colours) > designConstructionMaxColourRows {
		return "colours: no colour list is given; leave \"color_code\" empty\n"
	}
	rows := make([]string, 0, len(colours))
	for _, c := range colours {
		code := strings.TrimSpace(c.Code)
		if code == "" {
			continue
		}
		row := code
		if name := strings.TrimSpace(c.Name); name != "" {
			row += " · " + name
		}
		if hex := strings.TrimSpace(c.Hex.String); hex != "" {
			row += " · " + hex
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return "colours: no colour list is given; leave \"color_code\" empty\n"
	}
	return "colours (code · name · hex): " + strings.Join(rows, ", ") + "\n"
}

// designColourDictionary — СЛОВАРЬ ЦВЕТА, СЛОЖЕННЫЙ ДЛЯ УЗНАВАНИЯ: складка → канонический код.
//
// ⚠ В КАРТУ КЛАДЁТСЯ И КОД, И ИМЯ, И ЭТО НЕ ЩЕДРОСТЬ, А ЗАМЕР ТОГО, КАК ОТВЕЧАЮТ МОДЕЛИ. Промпт
// показывает «BLK · black · #000000» и просит код; модель регулярно отвечает тем, что человекообразнее
// — именем. Приняв только код, мы обнуляли бы верный ответ и заставляли человека доискивать в пикере
// то, что модель уже выбрала.
//
// ⚠ КОД СТАРШЕ ИМЕНИ, И СТОЛКНОВЕНИЕ ИМЁН УБИВАЕТ ОБЕ СТОРОНЫ. Складка, занятая КОДОМ, именем не
// переписывается: код — это идентификатор, имя — подпись. Складка, на которую претендуют ДВА разных
// имени, выбрасывается из карты вовсе: «bone» двух разных цветов — это не узнавание, а жребий, и
// жребий здесь стоит чужого продукта.
type designColourDictionary map[string]string

func designBuildColourDictionary(colours []entity.Color) designColourDictionary {
	dict := make(designColourDictionary, len(colours)*2)
	for _, c := range colours {
		code := strings.TrimSpace(c.Code)
		if code == "" {
			continue
		}
		dict[designFoldToken(code)] = code
	}
	byName := make(map[string]string, len(colours))
	ambiguous := make(map[string]struct{}, 4)
	for _, c := range colours {
		code, name := strings.TrimSpace(c.Code), designFoldToken(c.Name)
		if code == "" || name == "" {
			continue
		}
		if prev, seen := byName[name]; seen && prev != code {
			ambiguous[name] = struct{}{}
			continue
		}
		byName[name] = code
	}
	for name, code := range byName {
		if _, clash := ambiguous[name]; clash {
			continue
		}
		if _, taken := dict[name]; taken {
			continue
		}
		dict[name] = code
	}
	return dict
}

// designCardSlotFolds — СКЛАДКИ ИМЁН СТРОК СПЕКИ, УЖЕ СТОЯЩИХ НА КАРТОЧКЕ.
//
// Это половина ответа на вопрос «есть ли такой слот»; вторая половина — строки спеки САМОГО ОТВЕТА
// (см. designVerifyColourways). Обе нужны: колорвей, предложенный в одном ответе со своими слотами,
// обязан к ним привязаться ДО того, как человек их принял, а колорвей на карточку, где слоты уже
// набраны руками, — к набранным.
func designCardSlotFolds(card *entity.TechCard) map[string]struct{} {
	out := make(map[string]struct{})
	if card == nil {
		return out
	}
	for _, item := range card.BomItems {
		if fold := designFoldToken(item.Name); fold != "" {
			out[fold] = struct{}{}
		}
	}
	return out
}

// designCategoryName — имя категории карточки, если словарь его знает.
//
// ⚠ ПУСТАЯ СТРОКА — ЗАКОННЫЙ ОТВЕТ, И СТРОКА ПРОМПТА ТОГДА НЕ ПИШЕТСЯ ВОВСЕ. Кэш словарей
// наполняется стартом приложения; в пробе он пуст, и промпт, печатающий «Category: » без значения,
// сообщал бы модели пустое поле как факт.
func designCategoryName(card *entity.TechCard) string {
	if card == nil || !card.CategoryId.Valid || card.CategoryId.Int32 <= 0 {
		return ""
	}
	c, ok := cache.GetCategoryById(int(card.CategoryId.Int32))
	if !ok {
		return ""
	}
	return strings.TrimSpace(c.Name)
}

// designSizeRunLine — размерный ряд ИМЕНАМИ, с отмеченным базовым размером.
//
// Базовый отмечается словом, а не порядком: «по нему считается норма» — это факт о карточке, и
// модель, которой он нужен для ответа про пропорции, не обязана угадывать его из позиции в списке.
func designSizeRunLine(card *entity.TechCard) string {
	if card == nil || len(card.SizeIds) == 0 {
		return ""
	}
	baseID := 0
	if card.BaseSampleSizeId.Valid {
		baseID = int(card.BaseSampleSizeId.Int32)
	}
	names := make([]string, 0, len(card.SizeIds))
	for _, id := range card.SizeIds {
		s, ok := cache.GetSizeById(id)
		if !ok || strings.TrimSpace(s.Name) == "" {
			continue
		}
		name := strings.TrimSpace(s.Name)
		if id == baseID {
			name += " (base)"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// designCardAlreadySays — то, что карточка УЖЕ говорит про конструкцию, тремя списками.
//
// Пустая строка, когда карточка не говорит ничего: секция «не повторяй то, чего нет» — это
// инструкция ни о чём, и она стоила бы входных токенов на каждом нажатии.
//
// ⚠ У СЕКЦИИ ЕСТЬ ПОТОЛОК, И ОН НЕ ВКУС, А ДЕНЬГИ. Ревью круга 19: цикл обходил ВСЕ `card.Details`
// (колонка TEXT), ВСЕ неприколотые выноски и ВСЕ строки спецификации без единого предела. Карточка
// со ста выносками и шестьюдесятью строками спеки добавляет десятки килобайт (~15k входных токенов,
// ≈$0.045) к КАЖДОМУ нажатию — и невидимо для оценки, которая считает одни картинки. Соседний
// платный путь (techcardanalysis/context.go, promptField) держит «единственные ворота, через
// которые проходит каждая строка карточки»; здесь ворот не было ни одних.
//
// ТРИ ПРЕДЕЛА, И КАЖДЫЙ ОТВЕЧАЕТ НА СВОЙ ВОПРОС:
//   - СТРОКА — потолок рун на строку: одна строка не имеет права съесть секцию целиком;
//   - СПИСОК — потолок строк на список: сто выносок читаются не лучше двадцати;
//   - СЕКЦИЯ — потолок БАЙТОВ на всё: три списка вместе не имеют права вырасти без предела, даже
//     когда каждый по отдельности в своём пределе.
//
// ⚠ ОБРЕЗКА НАЗЫВАЕТ СЕБЯ ВСЛУХ. Секция велит модели «не повторяй то, что уже написано»; молча
// показав её половину, мы велели бы молчать о том, чего не показали, — и получили бы дубликаты
// именно там, где обещали их не получать. Строка «(+N more … not listed)» стоит десяток байт и
// делает список ЧЕСТНЫМ вместо ПОЛНОГО.
func designCardAlreadySays(card *entity.TechCard) string {
	if card == nil {
		return ""
	}
	var b strings.Builder
	// budget — общий потолок секции в БАЙТАХ; строки берут из него, пока он не кончится.
	budget := designConstructionMaxAlreadyBytes
	write := func(line string) bool {
		if len(line) > budget {
			return false
		}
		budget -= len(line)
		b.WriteString(line)
		return true
	}
	// tail печатает пропущенное число, если оно есть. Сам он в бюджет не входит: это НАША строка,
	// а не строка карточки, и урезать честность ради данных было бы обменом не в ту сторону.
	tail := func(kind string, skipped int) {
		if skipped > 0 {
			b.WriteString("- (+" + strconv.Itoa(skipped) + " more " + kind + " on the card, not listed)\n")
		}
	}

	shown, skipped := 0, 0
	for _, d := range card.Details {
		key := aiBoundedText(strings.TrimSpace(d.Key.String), designConstructionMaxVarchar64)
		text := aiBoundedText(designOneLine(d.Text.String), designConstructionMaxAlreadyLineRunes)
		if key == "" || text == "" {
			continue
		}
		if shown >= designConstructionMaxAlreadyRows || !write("- "+key+": "+text+"\n") {
			skipped++
			continue
		}
		shown++
	}
	tail("aspects", skipped)

	shown, skipped = 0, 0
	for _, c := range card.Callouts {
		// ВЫНОСКИ ТАБЛИЦЫ, А НЕ ДОСКИ: приколотые на картинки уже уехали секцией 3, и второй раз
		// они приехали бы как «уже сказано», то есть велели бы модели молчать о том, что она
		// как раз и должна прочитать.
		if c.MediaId.Valid && c.MediaId.Int32 > 0 {
			continue
		}
		line := aiBoundedText(designOneLine(entity.TechCardCalloutPrintedLine(c)),
			designConstructionMaxAlreadyLineRunes)
		if line == "" {
			continue
		}
		if c.Number > 0 {
			line = "#" + strconv.Itoa(c.Number) + " " + line
		}
		if shown >= designConstructionMaxAlreadyRows || !write("- callout: "+line+"\n") {
			skipped++
			continue
		}
		shown++
	}
	tail("callouts", skipped)

	shown, skipped = 0, 0
	for _, item := range card.BomItems {
		name := aiBoundedText(designOneLine(item.Name), designConstructionMaxAlreadyLineRunes)
		if name == "" {
			continue
		}
		line := "- bom: " + string(item.Section) + " · " + name
		if comp := aiBoundedText(designOneLine(item.Composition.String),
			designConstructionMaxAlreadyLineRunes); comp != "" {
			line += " · " + comp
		}
		if shown >= designConstructionMaxAlreadyRows || !write(line+"\n") {
			skipped++
			continue
		}
		shown++
	}
	tail("bom lines", skipped)

	return b.String()
}

// ─────────────────────────── разбор ответа ───────────────────────────

// designConstructionStats — ЧТО РАЗБОР ИСПРАВИЛ И ЧТО ВЫБРОСИЛ.
//
// Считается, чтобы быть НАПЕЧАТАННЫМ ОДНОЙ СТРОКОЙ ЛОГА в хендлере (дисциплина разбора тех-карты):
// счётчик без строки лога — это статистика, которую никто не видит, а строка без счётчика — это
// «что-то пошло не так» без числа. Дрейф написания, приведённый молча (регистр, пробел, дефис),
// сюда НЕ попадает: он утопил бы настоящие коэрции.
type designConstructionStats struct {
	AspectsCustom   int // ключ не из словаря — принят как самодельный
	AspectsDropped  int // пустой ключ или пустой текст
	CalloutsDropped int // строка без слов
	BomDropped      int // строка без имени
	MissingDropped  int
	EnumsUnset      int // секция/назначение/вид не узнаны — строка сохранена, токен пуст
	MaterialIDs     int // предложенный артикул обнулён (каталог не показывали)
	Truncated       int // строка обрезана потолком (рун — у TEXT, байтов — у VARCHAR)
	OverLimit       int // строки, не влезшие в потолок списка
	Deduped         int
	// PairsCleared — токен, законный сам по себе, но НЕЗАКОННЫЙ В ЭТОЙ ПАРЕ, снят со строки
	// (назначение не на рулонном, вид не в своей секции). Считается отдельно от EnumsUnset:
	// «слово не узнано» чинит промпт, «пара невозможна» чинит вопрос, который мы задали.
	PairsCleared int
	// NonScalars — на месте скаляра приехал объект или список. Раньше такой токен брался
	// ДОСЛОВНО, и технологу предлагалось значение `{"top":"tank","bottom":"none"}`.
	NonScalars int
	// FieldsDropped — поле (или элемент списка) не разобралось по типу и выброшено ПООДИНОЧКЕ.
	// До круга 19 любое несовпадение типа роняло json.Unmarshal целиком, то есть весь оплаченный
	// ответ ради одного «aspects": "none"».
	FieldsDropped int

	// ─── КРУГ 20 ───

	// CalloutsUnasked — СКОЛЬКО ВЫНОСОК МОДЕЛЬ ПРИСЛАЛА, ХОТЯ ПРОМПТ ИХ БОЛЬШЕ НЕ ПРОСИТ (B-13).
	//
	// ⚠ ЭТО НЕ CalloutsDropped, И РАЗНИЦА НЕСУЩАЯ. `CalloutsDropped` считает строку БЕЗ СЛОВ —
	// брак формы. Здесь строка ЦЕЛА, ПРИНЯТА и уедет на провод (поле 6 живо ради повтора старых
	// прогонов); посчитана она потому, что «модель отвечает на вопрос, которого ей не задавали» —
	// это факт ПРО ПРОМПТ, а узнать его можно только из лога. Ноль здесь — доказательство, что
	// правило 3 сработало; растущее число — счёт за выходные токены, которых никто не читает.
	CalloutsUnasked int
	// ColourCodesUnset — предложенный код цвета НЕ УЗНАН словарём и обнулён. Строка колорвея
	// остаётся (её можно подтвердить, выбрав код руками) — та же граница, что у designBomEnum:
	// человеку нужнее предложение без кода, чем отсутствие предложения.
	ColourCodesUnset int
	// SlotColoursUnbound — цвет назван для слота, которого нет НИ В ЭТОМ ОТВЕТЕ, НИ НА КАРТОЧКЕ.
	// Рецепт колорвея ключуется по строке спеки; цвет несуществующего слота некуда положить, и
	// нарисованный он обещал бы человеку строку, которую подтверждение молча пропустит.
	SlotColoursUnbound int
	// ColourwaysDropped — колорвей без имени и без единого привязанного цвета. Подтверждать нечего:
	// продукт требует имени или хотя бы одного цвета, чтобы отличаться от соседнего.
	ColourwaysDropped int
}

// Any говорит, было ли ХОТЬ ЧТО-ТО поправлено: уровень строки лога решается по нему.
func (s designConstructionStats) Any() bool {
	return s.AspectsCustom+s.AspectsDropped+s.CalloutsDropped+s.BomDropped+s.MissingDropped+
		s.EnumsUnset+s.MaterialIDs+s.Truncated+s.OverLimit+s.Deduped+
		s.PairsCleared+s.NonScalars+s.FieldsDropped+
		s.CalloutsUnasked+s.ColourCodesUnset+s.SlotColoursUnbound+s.ColourwaysDropped > 0
}

// designLoose — строка, принимающая ТРИ формы, в которых модели пишут скаляр: строку, число и
// null. Тот же приём и тот же довод, что у techcardanalysis.stringList: это дрейф ФОРМЫ, а не
// ложь о карточке, и ронять из-за него весь оплаченный прогон значило бы применить к форме
// наказание, придуманное для содержания.
//
// ⚠ ЧИСЛО ЗДЕСЬ НЕ ГИПОТЕТИЧЕСКОЕ: `material_id` в НАШЕМ СОБСТВЕННОМ каноническом JSON — int64,
// а protojson пишет int64 СТРОКОЙ. Одна и та же величина приезжает числом от модели и строкой с
// повтора, и жёсткий тип отверг бы ровно один из двух путей.
//
// ⚠ ОБЪЕКТ И СПИСОК — НЕ СКАЛЯР, И ДОСЛОВНО ОНИ БОЛЬШЕ НЕ БЕРУТСЯ. Ревью круга 19: прежняя ветка
// «что-то ещё скалярное — берётся как написано» брала ЛЮБОЙ нескалярный токен байтами, и модель,
// ответившая `"silhouette": {"top":"tank","bottom":"none"}`, предлагала технологу эту строку в
// поле силуэта — со скобками и кавычками, как значение. Терпимость к форме кончается там, где
// принятое перестаёт быть текстом, который человек согласится вписать в карточку: такой токен
// читается как ПУСТО и считается (NonScalars), потому что «модель отвечает объектами» — это факт
// про промпт, и узнать его можно только из лога.
type designLoose struct {
	v string
	// nonScalar помнит, что на месте скаляра приехала структура. Флаг живёт на значении, а не в
	// статистике, потому что разбор поля и его подсчёт происходят в разных местах: json.Unmarshal
	// счётчика не видит, а читающая сторона видит.
	nonScalar bool
}

func (l *designLoose) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" {
		*l = designLoose{}
		return nil
	}
	if trimmed[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*l = designLoose{v: str}
		return nil
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		*l = designLoose{nonScalar: true}
		return nil
	}
	// Число или bool — берётся как написано.
	*l = designLoose{v: trimmed}
	return nil
}

func (l designLoose) String() string { return strings.TrimSpace(l.v) }

// designTake читает скаляр и СЧИТАЕТ выброшенную структуру. Одна дверь на все чтения: пропустив
// её в одном месте, мы получили бы поле, про которое лог молчит.
func designTake(l designLoose, stats *designConstructionStats) string {
	if l.nonScalar {
		stats.NonScalars++
	}
	return l.String()
}

// ─── КЛЮЧИ ОТВЕТА ───

// designConstructionValueKeys — СЕМЬ КЛЮЧЕЙ, КОТОРЫМ ЕСТЬ КУДА ЛЕЧЬ. Присутствие хотя бы одного из
// них и есть признак «это ответ по схеме»; `missing` в семёрку не входит намеренно — это читаемый
// совет, а не значение поля, и ответ из одного совета не отвечает на заданный вопрос.
// ⚠ `colourways` В СЕМЁРКУ ДОБАВЛЕН, И ОТ ЭТОГО ОНА СТАЛА ВОСЬМЁРКОЙ. Ключ отвечает тому же
// условию, что и остальные семь: ему ЕСТЬ КУДА ЛЕЧЬ (блок предложений на студии, а оттуда —
// продукт). Ответ, содержательный одним лишь списком колорвеев, — законный и оплаченный ответ, и
// без этой строки он уходил бы в `invalid_output`.
//
// ⚠ `callouts` ИЗ СПИСКА НЕ УБРАН, ХОТЯ ПРОМПТ ИХ БОЛЬШЕ НЕ ПРОСИТ (B-13). Список отвечает на
// вопрос «это ответ по нашей схеме», а не «это то, что мы просили»: сохранённый до круга 20 прогон,
// у которого содержательными оказались одни выноски, обязан читаться обратно на повторе.
var designConstructionValueKeys = []string{
	"silhouette", "fabric", "fit", "concept", "aspects", "callouts", "bom", "colourways",
}

// designField читает ОДИН необязательный ключ, и НЕУДАЧА ОДНОГО КЛЮЧА НЕ РОНЯЕТ ОСТАЛЬНЫЕ.
//
// ⚠ ЭТО И ЕСТЬ ПОЧИНКА «ТРЕТЬЕГО ИСХОДА» (ревью круга 19). Разбор шёл одним json.Unmarshal в
// struct, поэтому ЛЮБОЕ несовпадение типа — `"aspects": "none"`, `"bom": {}` — роняло весь
// оплаченный ответ, включая шесть полей, которые приехали безупречно. Граница «коэрция против
// отказа» объявлена по форме ОТВЕТА ЦЕЛИКОМ, а не по форме каждого поля; поле, которое не
// разобралось, — это ровно та строка, которую разбор и обязан выбросить поодиночке.
func designField[T any](fields map[string]json.RawMessage, key string, stats *designConstructionStats) T {
	var out T
	raw, ok := fields[key]
	if !ok {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		stats.FieldsDropped++
		var zero T
		return zero
	}
	return out
}

// designListField — то же для списка, и ПОЭЛЕМЕНТНО: одна кривая строка спеки не уносит с собой
// четырнадцать целых. Отсутствующий ключ, `null` и пустой список читаются одинаково — «нечего
// перебирать»; отличать их нужно ровно в одном месте (проверка формы), и оно стоит выше.
func designListField[T any](fields map[string]json.RawMessage, key string, stats *designConstructionStats) []T {
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		stats.FieldsDropped++
		return nil
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		var v T
		if err := json.Unmarshal(it, &v); err != nil {
			stats.FieldsDropped++
			continue
		}
		out = append(out, v)
	}
	return out
}

type designRawAspect struct {
	Key  designLoose `json:"key"`
	Text designLoose `json:"text"`
}

type designRawCallout struct {
	Feature    designLoose `json:"feature"`
	Details    designLoose `json:"details"`
	Dimensions designLoose `json:"dimensions"`
}

type designRawBomLine struct {
	Section     designLoose `json:"section"`
	Purpose     designLoose `json:"purpose"`
	Kind        designLoose `json:"kind"`
	Name        designLoose `json:"name"`
	Composition designLoose `json:"composition"`
	Colour      designLoose `json:"colour"`
	// COLOR И COLOUR — ОДНО ПОЛЕ, ДВА НАПИСАНИЯ. Промпт просит британское, модель, обученная на
	// американских датасетах, регулярно пишет американское; принять одно значило бы терять цвет
	// у каждой второй строки по орфографии.
	ColorUS designLoose `json:"color"`
	Pantone designLoose `json:"pantone"`
	// MaterialID читается, чтобы БЫТЬ ОБНУЛЁННЫМ ГРОМКО (счётчик статистики), а не выброшенным
	// молча: «модель придумывает артикулы» — это про промпт, и узнать это можно только из лога.
	MaterialID designLoose `json:"material_id"`
}

// designLooseList — СПИСОК, ТЕРПЯЩИЙ НЕ-СПИСОК НА СВОЁМ МЕСТЕ.
//
// Тот же приём и тот же довод, что у designLoose: `"slots": {}` или `"slots": "black"` — это дрейф
// ФОРМЫ вложенного поля, и ронять из-за него ВЕСЬ колорвей (а вместе с ним имя, код и пантон,
// которые приехали безупречно) значило бы применить к форме наказание, придуманное для содержания.
// Читается как «слотов не названо»; колорвей, у которого не осталось ни имени, ни слотов, дальше
// выбрасывается своим собственным правилом и попадает в ColourwaysDropped.
type designLooseList []json.RawMessage

func (l *designLooseList) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" || trimmed[0] != '[' {
		*l = nil
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(b, &items); err != nil {
		*l = nil
		return nil
	}
	*l = items
	return nil
}

// designRawColourway — ОДНО ПРЕДЛОЖЕНИЕ КОЛОРВЕЯ, КАК ЕГО ПИШЕТ МОДЕЛЬ.
//
// COLOR_CODE И COLOUR_CODE — ОДИН КЛЮЧ, ДВА НАПИСАНИЯ, ровно как colour/color у строки спеки: наш
// собственный канонический JSON пишет `color_code` (так поле названо в схеме — колонка словаря
// называется `color_code`), а промпт, написанный по-британски, регулярно получает `colour_code`.
// Приняв одно, мы теряли бы код у каждого второго ответа по орфографии.
type designRawColourway struct {
	Name       designLoose     `json:"name"`
	ColorCode  designLoose     `json:"color_code"`
	ColourCode designLoose     `json:"colour_code"`
	Pantone    designLoose     `json:"pantone"`
	Hex        designLoose     `json:"hex"`
	Slots      designLooseList `json:"slots"`
}

type designRawSlotColour struct {
	Slot    designLoose `json:"slot"`
	Pantone designLoose `json:"pantone"`
	Hex     designLoose `json:"hex"`
	Colour  designLoose `json:"colour"`
	ColorUS designLoose `json:"color"`
}

// parseConstructionDraft — ЧИСТАЯ ФУНКЦИЯ: текст модели → проверенный черновик.
//
// Не ходит ни в стор, ни в кэш, ничего не логирует и не знает про прогон — ровно поэтому её можно
// накрыть табличными пробами, а хендлер остаётся одной веткой.
//
// ⚠ ДВА ИСХОДА, КОТОРЫЕ РОНЯЮТ ВЕСЬ ОТВЕТ, И БОЛЬШЕ НИКАКИХ:
//  1. finish_reason == "length" — ответ обрезан потолком токенов. Половина черновика неотличима
//     от полного: человек увидел бы четыре группы и решил, что остального модель не заметила.
//  2. в теле нет JSON-объекта, или в объекте НЕТ НИ ОДНОГО из семи ключей, которым есть куда лечь.
//
// ⚠ «КЛЮЧ ЕСТЬ» СПРАШИВАЕТСЯ У КАРТЫ КЛЮЧЕЙ, А НЕ У УКАЗАТЕЛЯ ПОСЛЕ РАЗБОРА, И ЭТО ПОЧИНКА, А НЕ
// СТИЛЬ. Раньше присутствие ключа читалось по «указатель не nil», но json.Unmarshal кладёт nil в
// указатель и на `"silhouette": null` — то есть ИДЕАЛЬНО ОФОРМЛЕННЫЙ ответ, где модель обычным для
// себя способом сказала «тут ничего», уходил в `invalid_output` ОПЛАЧЕННЫМ. Карта сырых кусков
// отвечает на вопрос, который здесь и задаётся: КЛЮЧ НАПИСАН ИЛИ НЕТ. «Написан и пуст» — законный
// и полезный ответ, «не написан вовсе» — не наша форма.
func parseConstructionDraft(raw, finishReason string) (*pb_common.DesignConstructionDraft, designConstructionStats, error) {
	var stats designConstructionStats

	if strings.EqualFold(strings.TrimSpace(finishReason), "length") {
		return nil, stats, fmt.Errorf("the construction draft was cut by the token ceiling (finish_reason=%q)", finishReason)
	}

	js := designExtractJSONObject(raw)
	if js == "" {
		return nil, stats, fmt.Errorf("no JSON object in the model output (%q)", aiBoundedText(raw, 200))
	}
	out, err := designParseConstructionObject(js, &stats)
	return out, stats, err
}

// designParseConstructionObject — разбор УЖЕ ВЫДЕЛЕННОГО объекта. Отдельная функция ради второго
// входа: повтор читает НАШ СОБСТВЕННЫЙ канонический JSON и не имеет права терпеть вокруг него прозу
// (см. designConstructionDraftFromRun), а живой ответ модели — обязан.
func designParseConstructionObject(js string, stats *designConstructionStats) (*pb_common.DesignConstructionDraft, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &fields); err != nil {
		return nil, fmt.Errorf("the model output is not a construction draft: %v", err)
	}
	found := false
	for _, k := range designConstructionValueKeys {
		if _, ok := fields[k]; ok {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("the model output carries none of the construction draft keys")
	}

	out := &pb_common.DesignConstructionDraft{
		Silhouette: designBoundedRunes(designScalarField(fields, "silhouette", stats), designConstructionMaxLongRunes, stats),
		Fabric:     designBoundedRunes(designScalarField(fields, "fabric", stats), designConstructionMaxLongRunes, stats),
		Concept:    designBoundedRunes(designScalarField(fields, "concept", stats), designConstructionMaxLongRunes, stats),
		Fit:        designConstructionFitByFold[designFoldToken(designScalarField(fields, "fit", stats))],
	}

	// ─── АСПЕКТЫ ───
	seenAspect := make(map[string]struct{})
	for _, a := range designListField[designRawAspect](fields, "aspects", stats) {
		key := designTake(a.Key, stats)
		text := designBoundedRunes(designTake(a.Text, stats), designConstructionMaxTextRunes, stats)
		if key == "" || text == "" {
			stats.AspectsDropped++
			continue
		}
		fold := designFoldToken(key)
		custom := false
		if canon, ok := designConstructionAspectByFold[fold]; ok {
			key = canon
		} else {
			// САМОДЕЛЬНЫЙ КЛЮЧ ПРИНИМАЕТСЯ, А НЕ ОТВЕРГАЕТСЯ: редактор аспектов принимает такие
			// от человека, и запрет ровно того же от модели был бы правилом про автора, а не
			// про данные. Обрезается ПО БАЙТАМ КОЛОНКИ (detail_key varchar(64)) — потолок в
			// рунах пропускал бы 64 кириллические руны, то есть 128 байт, и MySQL ответил бы
			// сырым 1406.
			key = designBoundedBytes(key, designConstructionMaxVarchar64, stats)
			custom = true
		}
		if _, dup := seenAspect[fold]; dup {
			stats.Deduped++
			continue
		}
		seenAspect[fold] = struct{}{}
		if len(out.Aspects) >= designConstructionMaxAspects {
			stats.OverLimit++
			continue
		}
		// ⚠ СЧИТАЕТСЯ ПРИНЯТАЯ СТРОКА, А НЕ УВИДЕННАЯ. До круга 19 счётчик стоял выше дедупа и
		// потолка и печатал 40 там, где технологу предложено 10, — то есть лог отвечал на вопрос,
		// которого никто не задавал.
		if custom {
			stats.AspectsCustom++
		}
		out.Aspects = append(out.Aspects, &pb_common.DesignConstructionAspect{Key: key, Text: text})
	}

	// ─── ВЫНОСКИ ───
	//
	// ⚠ РАЗБОР ЖИВ, ХОТЯ ПРОМПТ ВЫНОСОК БОЛЬШЕ НЕ ПРОСИТ (B-13), И ЭТО ТРЕБОВАНИЕ, А НЕ ЗАБЫВЧИВОСТЬ.
	// Идемпотентный повтор читает СОХРАНЁННЫЙ канонический JSON тем же самым разбором; выбросив
	// ключ, мы сломали бы второе нажатие на КАЖДОМ прогоне, отвеченном до этой волны, — и сломали
	// бы навсегда, потому что перезвонить модели по тому же ключу идемпотентности нельзя.
	//
	// Строки, которые модель шлёт по своей воле, ПРИНИМАЮТСЯ и считаются (CalloutsUnasked): клиент
	// их не рисует, а лог говорит, работает ли правило 3.
	seenCallout := make(map[string]struct{})
	for _, c := range designListField[designRawCallout](fields, "callouts", stats) {
		// `feature` уезжает в tech_card_callout.part (varchar(255)), `dimensions` — в одноимённую
		// varchar(255); описание — в TEXT, и там потолок смысловой.
		feature := designBoundedBytes(designTake(c.Feature, stats), designConstructionMaxVarchar255, stats)
		details := designBoundedRunes(designTake(c.Details, stats), designConstructionMaxTextRunes, stats)
		dims := designBoundedBytes(designTake(c.Dimensions, stats), designConstructionMaxVarchar255, stats)
		if feature == "" && details == "" {
			// РАЗМЕР БЕЗ СЛОВ — НЕ СТРОКА. «12 мм» без того, ЧТО двенадцать миллиметров, нельзя
			// ни принять, ни осмысленно отвергнуть.
			stats.CalloutsDropped++
			continue
		}
		fold := designFoldToken(feature + "|" + details)
		if _, dup := seenCallout[fold]; dup {
			stats.Deduped++
			continue
		}
		seenCallout[fold] = struct{}{}
		if len(out.Callouts) >= designConstructionMaxCallouts {
			stats.OverLimit++
			continue
		}
		out.Callouts = append(out.Callouts, &pb_common.DesignConstructionCallout{
			Feature: feature, Details: details, Dimensions: dims,
		})
		stats.CalloutsUnasked++
	}

	// ─── СПЕЦИФИКАЦИЯ ───
	seenBom := make(map[string]struct{})
	for _, l := range designListField[designRawBomLine](fields, "bom", stats) {
		name := designBoundedBytes(designTake(l.Name, stats), designConstructionMaxVarchar255, stats)
		if name == "" {
			// СТРОКА СПЕКИ БЕЗ ИМЕНИ НЕ СОХРАНЯЕТСЯ ВОВСЕ (свободная строка обязана нести имя),
			// поэтому её нечего и предлагать.
			stats.BomDropped++
			continue
		}
		fold := designFoldToken(name)
		if _, dup := seenBom[fold]; dup {
			stats.Deduped++
			continue
		}
		seenBom[fold] = struct{}{}
		if len(out.Bom) >= designConstructionMaxBom {
			stats.OverLimit++
			continue
		}
		colour := designTake(l.Colour, stats)
		if colour == "" {
			colour = designTake(l.ColorUS, stats)
		}
		section := designBomEnum(designTake(l.Section, stats), designBomSectionByFold, stats)
		purpose := designBomEnum(designTake(l.Purpose, stats), designBomPurposeByFold, stats)
		kind := designBomEnum(designTake(l.Kind, stats), designBomKindByFold, stats)
		purpose, kind = designPairBomTokens(section, purpose, kind, stats)
		line := &pb_common.DesignConstructionBomLine{
			Name:        name,
			Composition: designBoundedBytes(designTake(l.Composition, stats), designConstructionMaxVarchar255, stats),
			Colour:      designBoundedBytes(colour, designConstructionMaxVarchar255, stats),
			// PANTONE — varchar(64) (0363), и СВОЕГО СТОРОЖА В DTO У НЕГО НЕТ ВОВСЕ: длинная
			// строка доезжала бы до MySQL и возвращалась сырым 1406, не назвав ни строки, ни поля.
			Pantone: designBoundedBytes(designTake(l.Pantone, stats), designConstructionMaxVarchar64, stats),
			Section: section,
			Purpose: purpose,
			Kind:    kind,
		}
		// ⚠ АРТИКУЛ ОБНУЛЯЕТСЯ ВСЕГДА, И ЭТО ФАЗА, А НЕ ЗАБЫВЧИВОСТЬ: каталог в промпт не уезжает
		// (фаза 4), значит подтвердить предложенный id нечем. Строка, выглядящая связанной и
		// оценённой, но указывающая на чужой артикул, — ошибка себестоимости с ценником; строка
		// без связи — законная и полезная строка спеки.
		if v := designTake(l.MaterialID, stats); v != "" && v != "0" {
			stats.MaterialIDs++
		}
		out.Bom = append(out.Bom, line)
	}

	// ─── ЧТО СТОИТ ПРИКОЛОТЬ ───
	seenMissing := make(map[string]struct{})
	for _, m := range designListField[designLoose](fields, "missing", stats) {
		text := designBoundedRunes(designTake(m, stats), designConstructionMaxTextRunes, stats)
		if text == "" {
			stats.MissingDropped++
			continue
		}
		fold := designFoldToken(text)
		if _, dup := seenMissing[fold]; dup {
			stats.Deduped++
			continue
		}
		seenMissing[fold] = struct{}{}
		if len(out.Missing) >= designConstructionMaxMissing {
			stats.OverLimit++
			continue
		}
		out.Missing = append(out.Missing, text)
	}

	// ─── КОЛОРВЕИ (B-25) ───
	//
	// ⚠ ЗДЕСЬ ТОЛЬКО ФОРМА: потолки, границы колонок, дедуп, шестнадцатеричный цвет. КОД СЛОВАРЯ И
	// ПРИВЯЗКА СЛОТОВ ПРОВЕРЯЮТСЯ ОТДЕЛЬНЫМ ШАГОМ (designVerifyColourways), И РАЗДЕЛЕНИЕ ЭТО
	// НЕСУЩЕЕ. Этот же разбор читает СОХРАНЁННЫЙ канонический JSON на идемпотентном повторе — там
	// ни словаря, ни карточки под рукой нет и быть не должно. Сложив проверку сюда, мы получили бы
	// повтор, который сверяет вчерашний оплаченный ответ с СЕГОДНЯШНИМ словарём: архивированный за
	// ночь цвет молча обнулял бы код, а переименованная строка спеки — уносила бы цвета слотов.
	// Прогон отвечен один раз и навсегда.
	seenColourway := make(map[string]struct{})
	for _, c := range designListField[designRawColourway](fields, "colourways", stats) {
		// ИМЯ — ПОДПИСЬ: потолок в РУНАХ (смысловой, 64), затем в БАЙТАХ (колонка
		// tech_card_colorway.dev_name, varchar(255)). Порядок именно такой: рунный потолок строже
		// для латиницы, байтовый — для кириллицы, и нужны оба.
		name := designBoundedBytes(
			designBoundedRunes(designTake(c.Name, stats), designConstructionMaxColourwayNameRunes, stats),
			designConstructionMaxVarchar255, stats)
		code := designTake(c.ColorCode, stats)
		if code == "" {
			code = designTake(c.ColourCode, stats)
		}
		cw := &pb_common.DesignColourwayProposal{
			Name: name,
			// КОД ЕДЕТ СЫРЫМ И ПРОВЕРЯЕТСЯ ШАГОМ ВЫШЕ ПО МАРШРУТУ. Здесь он лишь обрезан по
			// колонке словаря (varchar(64) с запасом: настоящий код — три знака).
			ColorCode: designBoundedBytes(code, designConstructionMaxVarchar64, stats),
			Pantone:   designBoundedBytes(designTake(c.Pantone, stats), designConstructionMaxVarchar64, stats),
			Hex:       designHexColour(designTake(c.Hex, stats)),
		}
		seenSlot := make(map[string]struct{}, len(c.Slots))
		for _, rawSlot := range c.Slots {
			var s designRawSlotColour
			if err := json.Unmarshal(rawSlot, &s); err != nil {
				stats.FieldsDropped++
				continue
			}
			slot := designBoundedBytes(designTake(s.Slot, stats), designConstructionMaxVarchar255, stats)
			fold := designFoldToken(slot)
			if fold == "" {
				// ЦВЕТ БЕЗ СЛОТА НЕКУДА ПОЛОЖИТЬ: строка рецепта ключуется именем строки спеки.
				stats.SlotColoursUnbound++
				continue
			}
			if _, dup := seenSlot[fold]; dup {
				stats.Deduped++
				continue
			}
			seenSlot[fold] = struct{}{}
			if len(cw.Slots) >= designConstructionMaxColourwaySlots {
				stats.OverLimit++
				continue
			}
			colour := designTake(s.Colour, stats)
			if colour == "" {
				colour = designTake(s.ColorUS, stats)
			}
			cw.Slots = append(cw.Slots, &pb_common.DesignColourwaySlotColour{
				Slot:    slot,
				Pantone: designBoundedBytes(designTake(s.Pantone, stats), designConstructionMaxVarchar64, stats),
				Hex:     designHexColour(designTake(s.Hex, stats)),
				Colour:  designBoundedBytes(colour, designConstructionMaxVarchar255, stats),
			})
		}
		if designColourwayIsEmpty(cw) {
			stats.ColourwaysDropped++
			continue
		}
		// ДЕДУП ПО СЛОЖЕННОМУ ИМЕНИ: два «Black / Bone» в одном ответе — это одно предложение,
		// напечатанное дважды, и подтвердить их оба нельзя (второй CreateColorway отказал бы по
		// занятому коду).
		fold := designFoldToken(cw.Name + "|" + cw.ColorCode)
		if _, dup := seenColourway[fold]; dup {
			stats.Deduped++
			continue
		}
		seenColourway[fold] = struct{}{}
		if len(out.Colourways) >= designConstructionMaxColourways {
			stats.OverLimit++
			continue
		}
		out.Colourways = append(out.Colourways, cw)
	}

	return out, nil
}

// designColourwayIsEmpty — «ПОДТВЕРЖДАТЬ НЕЧЕГО».
//
// Правило дизайна дословно: колорвей без имени И без слотов выбрасывается. Именно эта пара, а не
// «пусто по всем полям»: имя без цветов — законный колорвей (цвета доставят на вкладке), цвета без
// имени — тоже (сервер подпишет его «colourway N»). Пустое И то, и другое — строка, которая не
// отличается от соседней ничем.
func designColourwayIsEmpty(c *pb_common.DesignColourwayProposal) bool {
	return c.GetName() == "" && len(c.GetSlots()) == 0
}

// designHexColour — ЭКРАННОЕ ПРИБЛИЖЕНИЕ ЦВЕТА ИЛИ ПУСТО, ТРЕТЬЕГО НЕ ДАНО.
//
// ⚠ ПРОВЕРКА, А НЕ ОБРЕЗКА, И ЭТО РАЗНЫЕ ВЕЩИ. `dev_hex` — varchar(7), то есть «#RRGGBB» ровно;
// обрезав «black» до семи знаков, мы положили бы в колонку слово и нарисовали бы человеку плашку
// цвета, которого никто не называл. Не шестнадцатеричное — это ОТСУТСТВИЕ приближения, и пустая
// строка говорит именно это; пантон рядом при этом остаётся, и клиент рисует плашку по нему
// (findPantone) — то есть по КОДУ, который человек может проверить, а не по выдумке.
//
// Сокращённая запись (#RGB) НЕ ПРИНИМАЕТСЯ: разворачивать её значило бы называть цвет, который
// модель не написала, а колонка всё равно ждёт семь знаков.
func designHexColour(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 7 || s[0] != '#' {
		return ""
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		hex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !hex {
			return ""
		}
	}
	return s
}

// designVerifyColourways — ВТОРОЙ, НЕЧИСТЫЙ ПО ВХОДАМ ШАГ: ответ сверяется С НАШИМИ СОБСТВЕННЫМИ
// ДАННЫМИ — со словарём цвета и со строками спеки.
//
// ⚠ ПОЧЕМУ ЭТО ОТДЕЛЬНАЯ ФУНКЦИЯ, А НЕ ЧАСТЬ РАЗБОРА. Разбор зовётся ДВАЖДЫ: на живом ответе
// модели и на СОХРАНЁННОЙ строке при идемпотентном повторе. Проверка обязана случиться РОВНО ОДИН
// РАЗ — на живом ответе, до записи канонического JSON. Сверка на повторе означала бы, что
// вчерашний оплаченный ответ пересматривается сегодняшним словарём: цвет, архивированный за ночь,
// молча терял бы код, а переименованная строка спеки уносила бы с собой цвета слотов. То, что
// человек уже видел и за что заплачено, менять нельзя — это тот же довод, по которому в
// `output_text` уезжает проверенный канон, а не ответ модели.
//
// ФУНКЦИЯ ОСТАЁТСЯ ЧИСТОЙ: словарь и складки карточки приходят готовыми, стор она не знает.
func designVerifyColourways(
	draft *pb_common.DesignConstructionDraft,
	dict designColourDictionary,
	cardSlots map[string]struct{},
	stats *designConstructionStats,
) {
	if draft == nil || len(draft.Colourways) == 0 {
		return
	}

	// ЧТО СЧИТАЕТСЯ СУЩЕСТВУЮЩИМ СЛОТОМ: строки спеки ЭТОГО ЖЕ ОТВЕТА плюс строки спеки КАРТОЧКИ.
	// Первое — потому что колорвей и его слоты приезжают одним ответом и человек примет их одним
	// заходом; второе — потому что на карточке со набранной руками спекой предложение обязано
	// лечь на неё, а не потребовать пересоздать слоты.
	bound := make(map[string]struct{}, len(cardSlots)+len(draft.Bom))
	for fold := range cardSlots {
		bound[fold] = struct{}{}
	}
	for _, line := range draft.Bom {
		if fold := designFoldToken(line.GetName()); fold != "" {
			bound[fold] = struct{}{}
		}
	}

	kept := draft.Colourways[:0]
	for _, cw := range draft.Colourways {
		// ─── КОД СЛОВАРЯ ───
		if cw.ColorCode != "" {
			if canon, ok := dict[designFoldToken(cw.ColorCode)]; ok {
				cw.ColorCode = canon
			} else {
				// ⚠ СТРОКА ОСТАЁТСЯ, ОБНУЛЯЕТСЯ ТОЛЬКО КОД — та же граница, что у designBomEnum.
				// Человеку нужнее предложение, у которого код надо выбрать самому, чем отсутствие
				// предложения, которое модель составила правильно и подписала своим словом.
				cw.ColorCode = ""
				stats.ColourCodesUnset++
			}
		}

		// ─── ПРИВЯЗКА ЦВЕТОВ К СЛОТАМ ───
		keptSlots := cw.Slots[:0]
		for _, s := range cw.Slots {
			if _, ok := bound[designFoldToken(s.GetSlot())]; !ok {
				stats.SlotColoursUnbound++
				continue
			}
			keptSlots = append(keptSlots, s)
		}
		cw.Slots = keptSlots

		if designColourwayIsEmpty(cw) {
			stats.ColourwaysDropped++
			continue
		}
		kept = append(kept, cw)
	}
	draft.Colourways = kept

	// ─── ПОДПИСЬ БЕЗЫМЯННОМУ ───
	//
	// ⚠ СЧИТАЕТСЯ ПО ПОРЯДКУ В ОТВЕТЕ, А НЕ ПО ЧИСЛУ БЕЗЫМЯННЫХ: «colourway 2» рядом с «Black /
	// Bone» и «colourway 3» читается как «второе и третье предложение», а два подряд «colourway 1»
	// и «colourway 2» на местах 2 и 4 не сказали бы человеку ничего.
	for i, cw := range draft.Colourways {
		if cw.Name == "" {
			cw.Name = "colourway " + strconv.Itoa(i+1)
		}
	}
}

// designScalarField — один скалярный ключ ответа: прочитан, посчитан, отдан строкой. Отсутствующий
// ключ и пустая строка дают одно и то же ЗНАЧЕНИЕ; разница между ними нужна ровно один раз — при
// проверке формы — и спрашивается там у карты ключей.
func designScalarField(fields map[string]json.RawMessage, key string, stats *designConstructionStats) string {
	return designTake(designField[designLoose](fields, key, stats), stats)
}

// designPairBomTokens СНИМАЕТ СО СТРОКИ ТОКЕН, ЗАКОННЫЙ САМ ПО СЕБЕ И НЕВОЗМОЖНЫЙ В ЭТОЙ ПАРЕ.
//
// ⚠ РАДИУС ПОРАЖЕНИЯ ЗДЕСЬ — ВСЯ КАРТОЧКА, И ИМЕННО ПОЭТОМУ ЭТО ЧИНИТСЯ В РАЗБОРЕ. Разбор клал
// section / purpose / kind независимо друг от друга, и `{"section":"hardware","purpose":"main",
// "kind":"button"}` проходил: каждый токен есть в своём словаре. Сохранение же требует ПАР —
// назначение только на рулонных (store/techcard/materials.go, rollGoodsSections) и вид только в
// своей домашней секции (validateBomKindSection), — а UpsertTechCard устроен «всё-или-ничего».
// То есть одна предложенная строка спеки отказывала в сохранении ВСЕЙ тех-карты, причём поля,
// которые её сломали, интерфейс предложения даже не рисует: человек не видит, что править.
//
// ⚠ СНИМАЕТСЯ ТОКЕН, А НЕ СТРОКА, И ЭТО ТА ЖЕ ГРАНИЦА, ЧТО У designBomEnum: технологу нужнее
// строка спеки без назначения, чем отсутствие строки, которую модель увидела правильно.
//
// ⚠ ПРАВИЛО НЕ ПЕРЕПИСАНО, А ПОЗВАНО ПО ИМЕНИ (entity.IsRollGoodsSection, entity.IsKindEligibleSection,
// entity.BomKindHomeSection). Вторая копия «какие семьи рулонные» разошлась бы с первой в тот день,
// когда семей станет пять, и разбор снова начал бы предлагать пары, которые сохранение отвергает.
func designPairBomTokens(
	section pb_common.TechCardBomSection,
	purpose pb_common.TechCardBomPurpose,
	kind pb_common.TechCardBomKind,
	stats *designConstructionStats,
) (pb_common.TechCardBomPurpose, pb_common.TechCardBomKind) {
	sec := designEntityBomSection(section)
	if purpose != pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET && !entity.IsRollGoodsSection(sec) {
		purpose = pb_common.TechCardBomPurpose_TECH_CARD_BOM_PURPOSE_UNSET
		stats.PairsCleared++
	}
	if kind != pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET {
		home, known := entity.BomKindHomeSection(designEntityBomKind(kind))
		legal := known && entity.IsKindEligibleSection(sec) &&
			(home == entity.BomKindAnySection || home == sec)
		if !legal {
			kind = pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET
			stats.PairsCleared++
		}
	}
	return purpose, kind
}

// designEntityBomSection / designEntityBomKind — ОДНО И ТО ЖЕ ЗНАЧЕНИЕ ДВУМЯ ПИСЬМАМИ.
//
// Хранимая строка выводится ИЗ ИМЕНИ ЧЛЕНА ENUM'А, а не из второй написанной от руки карты — тем же
// приёмом и по тому же доводу, что designEnumVocabulary выше: карта, переписанная руками, молча
// теряет член, добавленный в другом файле. Тождество «имя члена без префикса, строчными = строка
// колонки» прибито дрейф-пробами (TestBomKindEnumNoDrift, TestBomSectionEnumNoDrift).
//
// Нулевой член — это «не задано», а не значение: он даёт пустую строку, которую ни одна семья не
// признаёт своей, и пара с ним поэтому невозможна по построению.
func designEntityBomSection(v pb_common.TechCardBomSection) entity.TechCardBomSection {
	if v == pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_UNKNOWN {
		return ""
	}
	return entity.TechCardBomSection(strings.ToLower(strings.TrimPrefix(
		pb_common.TechCardBomSection_name[int32(v)], "TECH_CARD_BOM_SECTION_")))
}

func designEntityBomKind(v pb_common.TechCardBomKind) entity.TechCardBomKind {
	if v == pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET {
		return ""
	}
	return entity.TechCardBomKind(strings.ToLower(strings.TrimPrefix(
		pb_common.TechCardBomKind_name[int32(v)], "TECH_CARD_BOM_KIND_")))
}

// designBoundedRunes — обрезка по РУНАМ со счётчиком, для колонок TEXT и для читаемых советов, где
// потолок смысловой («одна мысль на строку»). Обрезка МАРКИРУЕТСЯ (тот же довод, что у
// aiBoundedText): оборванная фраза, прочитанная как законченная, — это другая инструкция.
func designBoundedRunes(s string, max int, stats *designConstructionStats) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		stats.Truncated++
	}
	return aiBoundedText(s, max)
}

// designBoundedBytes — обрезка по БАЙТАМ, для колонок VARCHAR(N).
//
// ⚠ БАЙТЫ, ПОТОМУ ЧТО БАЙТЫ СЧИТАЕТ СТОРОЖ, СТОЯЩИЙ ДАЛЬШЕ ПО МАРШРУТУ (internal/dto меряет
// `len()`), И ПОТОМУ ЧТО БАЙТЫ СЧИТАЕТ КОЛОНКА. Потолок в рунах пропускает ~85 кириллических рун
// в varchar(255) сверх предела, и отказ приходит либо полем DTO — то есть отказом в сохранении
// ВСЕЙ карточки, — либо сырым MySQL 1406.
//
// РЕЖЕТСЯ ПО ГРАНИЦЕ РУНЫ: половина многобайтового символа — это невалидный UTF-8, который MySQL
// встретит ошибкой 1366 вместо обрезанного слова. Многоточие-маркер (3 байта) входит В потолок, а
// не сверх него.
func designBoundedBytes(s string, maxBytes int, stats *designConstructionStats) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBytes {
		return s
	}
	stats.Truncated++
	const ellipsis = "…" // 3 байта в UTF-8
	cut := s[:maxBytes-len(ellipsis)]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut) + ellipsis
}

// designBomEnum узнаёт токен закрытого словаря. Неузнанный — UNSET, а СТРОКА ОСТАЁТСЯ: человеку
// нужнее строка спеки без секции, чем отсутствие строки, которую модель увидела правильно и
// назвала синонимом.
func designBomEnum[E ~int32](token string, byFold map[string]E, stats *designConstructionStats) E {
	var unset E
	if strings.TrimSpace(token) == "" {
		return unset
	}
	if v, ok := byFold[designFoldToken(token)]; ok {
		return v
	}
	stats.EnumsUnset++
	return unset
}

// designExtractJSONObject достаёт внешний {...}, снимая markdown-ограду и терпя прозу вокруг.
//
// ПОВТОРЯЕТ ЛОГИКУ extractAnalysisJSON, А НЕ ЗОВЁТ ЕЁ: та неэкспортируема и живёт в чужом пакете.
// Поведение обязано совпадать — расхождение разборов означало бы, что один и тот же ответ модели
// принимает один платный путь и отвергает соседний.
func designExtractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:] // строка с языковым тегом («json»)
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}

// designConstructionDraftFromRun — ВОССТАНОВЛЕНИЕ ЧЕРНОВИКА ИЗ СОХРАНЁННОЙ СТРОКИ ПРОГОНА.
//
// ⚠ ЭТО ТРЕБОВАНИЕ, А НЕ УДОБСТВО. Идемпотентный повтор отдаёт СОХРАНЁННЫЙ прогон и модель не
// зовёт; без восстановления второе нажатие той же кнопки возвращало бы пустое предложение при
// оплаченном и успешном прогоне. Разбор — ТОТ ЖЕ САМЫЙ, потому что хранится наш собственный
// канонический JSON: второй, «строгий для своих» разбор был бы вторым мнением о той же строке.
//
// nil — законный ответ и обычный: так выглядит прогон, отвеченный ПРОЗОЙ (старый клиент, флага не
// было). Прозе здесь ошибки не полагается — вопрос «структурный ли это прогон» задают именно так.
//
// ⚠ ПРОЗА БОЛЬШЕ НЕ РАЗБИРАЕТСЯ ВОВСЕ, И ЭТО ПОЧИНКА (ревью круга 19). Тот же разбор звался через
// designExtractJSONObject, который берёт от первой `{` до последней `}` и терпит прозу вокруг —
// терпимость, законная для ЖИВОГО ответа модели и НЕЗАКОННАЯ здесь. Прозаический черновик,
// содержащий фигурные скобки («Use a {"fabric": "jersey"} weight.»), давал НЕПУСТОЙ черновик: на
// прогоне, который структурного ответа не просил, человеку показывалось предложение, которого
// никто не делал. Здесь читается наш СОБСТВЕННЫЙ канонический JSON, и он всегда объект целиком,
// поэтому строгость не стоит ничего: тело обязано начинаться `{` и кончаться `}`.
//
// Эта же строгость и есть признак формы, по которому повтор отличает структурный прогон от
// прозаического (см. designReasonShapeMismatch).
func designConstructionDraftFromRun(outputText string) *pb_common.DesignConstructionDraft {
	js := strings.TrimSpace(outputText)
	if !strings.HasPrefix(js, "{") || !strings.HasSuffix(js, "}") {
		return nil
	}
	var stats designConstructionStats
	draft, err := designParseConstructionObject(js, &stats)
	if err != nil {
		return nil
	}
	return draft
}

// designConstructionMarshal — ПИСАТЕЛЬ КАНОНИЧЕСКОГО ЧЕРНОВИКА, И ОН НАМЕРЕННО НЕ ТОТ, ЧТО ПИШЕТ
// `params` и `inputs` (designJSONMarshal).
//
// ⚠ EmitUnpopulated ВКЛЮЧЁН, И БЕЗ НЕГО КРУГОВОЙ ОБХОД НЕ ДЕРЖИТСЯ. Замерено ревью круга 19: ответ,
// у которого содержательным оказался один только список `missing`, сохранялся как `{"missing":[…]}`
// — потому что protojson по умолчанию не пишет пустых полей, — а designConstructionDraftFromRun
// требует ПРИСУТСТВИЯ хотя бы одного из семи ключей и на такой строке возвращал nil. То есть
// УСПЕШНЫЙ ОПЛАЧЕННЫЙ прогон на идемпотентном повторе отдавал пустоту — ровно тот двойной клик,
// ради которого механизм и существует. Системный промпт сам ведёт к этой форме: правило 1 велит
// «оставь поле пустым, назови нехватку в missing».
//
// ЭТО ТА ЖЕ АСИММЕТРИЯ «ПИСАТЕЛЬ ПРОТИВ ЧИТАТЕЛЯ», ЧТО ОДНАЖДЫ СЛОМАЛА ПОДПИСЬ ДАЙДЖЕСТА: правило
// «не хранить выводимое» экономит байты у писателя и молча меняет ответ у читателя. Здесь писатель
// пишет ровно то, чего читатель требует.
//
// ⚠ И ИМЕННО ПОЭТОМУ ЭТО ВТОРАЯ НАСТРОЙКА, А НЕ ПРАВКА ПЕРВОЙ. У `inputs` довод против
// EmitUnpopulated живой и денежный: снимок ограничен 64 KB, и заполнение нулями тратило бы потолок.
// У черновика потолка нет — он едет в output_text (TEXT), — а восемь пустых ключей стоят около
// сотни байт. UseProtoNames обе разделяют: канонический JSON читается обратно тем же разбором, чей
// словарь узнаёт ПОЛНЫЕ имена членов enum'а (TECH_CARD_BOM_SECTION_FABRIC).
var designConstructionMarshal = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

func designMarshalConstructionDraft(d *pb_common.DesignConstructionDraft) ([]byte, error) {
	return designConstructionMarshal.Marshal(d)
}
