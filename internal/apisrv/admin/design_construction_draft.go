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
	// designConstructionMaxKeyRunes — потолок САМОДЕЛЬНОГО ключа аспекта: колонка detail_key —
	// varchar(64), и ключ длиннее не сохранился бы.
	designConstructionMaxKeyRunes = 64

	// Потолки списков. Не вкус: предложение, которое человек обязан просмотреть по строкам, за
	// этими числами перестаёт быть предложением и становится работой.
	designConstructionMaxAspects  = 10
	designConstructionMaxCallouts = 15
	designConstructionMaxBom      = 15
	designConstructionMaxMissing  = 8
)

// designConstructionReasonInvalidOutput — машинная причина «ответ не той формы», она же
// `error_code` проваленного прогона. Одно слово на два места: клиент отличает её от провала
// поставщика, не разбирая английскую прозу, а история прогонов — по колонке.
const designConstructionReasonInvalidOutput = "invalid_output"

const (
	// Две прозы на два РАЗНЫХ исхода, и различие несущее: первый чинится повтором, второй —
	// уменьшением доски или описания. Одна фраза на оба отправила бы человека жать ту же кнопку
	// до тех пор, пока он не бросит.
	designConstructionShapeRefusalMsg = "the model did not answer in the shape asked for — draft again"
	designConstructionCutRefusalMsg   = "the answer was cut off — fewer pictures or a shorter description, then draft again"
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
const designConstructionSystemPrompt = "You are a garment technologist's assistant. " +
	"You are shown the moodboard pictures, the designer's concept & construction description, and " +
	"the notes pinned on the pictures — every note names its picture by number and the spot it " +
	"marks, so you know exactly which part of which image it refers to.\n" +
	"Answer with ONE JSON object and nothing else — no prose before or after it, no code fence. " +
	"English. The object has exactly these keys:\n" +
	"{\"silhouette\": string, \"fabric\": string, \"fit\": string, \"concept\": string, " +
	"\"aspects\": [{\"key\": string, \"text\": string}], " +
	"\"callouts\": [{\"feature\": string, \"details\": string, \"dimensions\": string}], " +
	"\"bom\": [{\"section\": string, \"purpose\": string, \"kind\": string, \"name\": string, " +
	"\"composition\": string, \"colour\": string, \"pantone\": string}], " +
	"\"missing\": [string]}\n" +
	"Rules:\n" +
	"1. Never invent a fabric, a colour, a measurement or a piece of hardware that the pictures do " +
	"not show and the notes do not state. Leave the field empty and name what is missing under " +
	"\"missing\" instead.\n" +
	"2. Prefer the designer's own words where they say the same thing.\n" +
	"3. \"callouts\" are construction features visible on the pictures — seams, closures, edges, " +
	"pockets, bindings — one row each. Fill \"dimensions\" only when a measurement is actually " +
	"stated; leave it empty otherwise.\n" +
	"4. \"bom\" names components BY THEIR ROLE («main fabric», «neck binding», «care label»), one " +
	"line per component. Use the section / purpose / kind tokens given in the prompt; leave a token " +
	"empty when it does not apply.\n" +
	"5. \"aspects\" use the keys given in the prompt, or a short custom key when none of them fits; " +
	"at most 60 words each.\n" +
	"6. Do not repeat what the card already says — refine it or leave the field empty.\n" +
	"7. Limits: at most 10 aspects, 15 callouts, 15 bom lines, 8 missing notes.\n" +
	"8. \"concept\" is answered ONLY when the prompt says the card has none; otherwise leave it empty."

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
func designConstructionUserPrompt(card *entity.TechCard, mood *pb_common.DesignMoodSnapshot, attachedIDs []int) string {
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

	// ⚠ КАТАЛОГА НЕТ, И ЭТО ГОВОРИТСЯ ВСЛУХ. Модель, которой не сказали, что артикулов ей не дали,
	// охотно придумает `material_id`; разбор его всё равно обнулит, но потраченные на выдумку
	// выходные токены обнулить нельзя.
	b.WriteString("\nNo materials catalogue is given: never invent an article id.\n")

	return strings.TrimSpace(b.String())
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
func designCardAlreadySays(card *entity.TechCard) string {
	if card == nil {
		return ""
	}
	var b strings.Builder
	for _, d := range card.Details {
		key := strings.TrimSpace(d.Key.String)
		text := designOneLine(d.Text.String)
		if key == "" || text == "" {
			continue
		}
		b.WriteString("- " + key + ": " + text + "\n")
	}
	for _, c := range card.Callouts {
		// ВЫНОСКИ ТАБЛИЦЫ, А НЕ ДОСКИ: приколотые на картинки уже уехали секцией 3, и второй раз
		// они приехали бы как «уже сказано», то есть велели бы модели молчать о том, что она
		// как раз и должна прочитать.
		if c.MediaId.Valid && c.MediaId.Int32 > 0 {
			continue
		}
		line := designOneLine(entity.TechCardCalloutPrintedLine(c))
		if line == "" {
			continue
		}
		if c.Number > 0 {
			line = "#" + strconv.Itoa(c.Number) + " " + line
		}
		b.WriteString("- callout: " + line + "\n")
	}
	for _, item := range card.BomItems {
		name := designOneLine(item.Name)
		if name == "" {
			continue
		}
		line := "- bom: " + string(item.Section) + " · " + name
		if comp := designOneLine(item.Composition.String); comp != "" {
			line += " · " + comp
		}
		b.WriteString(line + "\n")
	}
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
	Truncated       int // строка обрезана потолком рун
	OverLimit       int // строки, не влезшие в потолок списка
	Deduped         int
}

// Any говорит, было ли ХОТЬ ЧТО-ТО поправлено: уровень строки лога решается по нему.
func (s designConstructionStats) Any() bool {
	return s.AspectsCustom+s.AspectsDropped+s.CalloutsDropped+s.BomDropped+s.MissingDropped+
		s.EnumsUnset+s.MaterialIDs+s.Truncated+s.OverLimit+s.Deduped > 0
}

// designLoose — строка, принимающая ТРИ формы, в которых модели пишут скаляр: строку, число и
// null. Тот же приём и тот же довод, что у techcardanalysis.stringList: это дрейф ФОРМЫ, а не
// ложь о карточке, и ронять из-за него весь оплаченный прогон значило бы применить к форме
// наказание, придуманное для содержания.
//
// ⚠ ЧИСЛО ЗДЕСЬ НЕ ГИПОТЕТИЧЕСКОЕ: `material_id` в НАШЕМ СОБСТВЕННОМ каноническом JSON — int64,
// а protojson пишет int64 СТРОКОЙ. Одна и та же величина приезжает числом от модели и строкой с
// повтора, и жёсткий тип отверг бы ровно один из двух путей.
type designLoose string

func (l *designLoose) UnmarshalJSON(b []byte) error {
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" {
		*l = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*l = designLoose(s)
		return nil
	}
	// Число, bool или что-то ещё скалярное — берётся как написано.
	*l = designLoose(trimmed)
	return nil
}

func (l designLoose) String() string { return strings.TrimSpace(string(l)) }

// designRawConstruction — ответ модели ДО коэрции: каждое поле указателем или сырым куском.
//
// ⚠ УКАЗАТЕЛИ — ЭТО НЕ СТИЛЬ, А ЕДИНСТВЕННЫЙ СПОСОБ ОТЛИЧИТЬ «КЛЮЧА НЕТ» ОТ «КЛЮЧ ПУСТ». Первое
// значит «ответ не той формы» (и роняет прогон), второе — «модель посмотрела и ей нечего сказать»
// (и это законный, полезный ответ). Схлопнув их в пустую строку, разбор принял бы прозу за пустой
// черновик и предъявил бы человеку четыре пустые группы вместо отказа.
type designRawConstruction struct {
	Silhouette *designLoose        `json:"silhouette"`
	Fabric     *designLoose        `json:"fabric"`
	Fit        *designLoose        `json:"fit"`
	Concept    *designLoose        `json:"concept"`
	Aspects    *[]designRawAspect  `json:"aspects"`
	Callouts   *[]designRawCallout `json:"callouts"`
	Bom        *[]designRawBomLine `json:"bom"`
	Missing    *[]designLoose      `json:"missing"`
}

// designScalar читает необязательный скаляр: отсутствующий ключ и пустая строка дают одно и то же
// ЗНАЧЕНИЕ. Разница между ними нужна ровно один раз — при проверке формы — и спрашивается там
// напрямую у указателя, а не через это чтение.
func designScalar(p *designLoose) string {
	if p == nil {
		return ""
	}
	return p.String()
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

// parseConstructionDraft — ЧИСТАЯ ФУНКЦИЯ: текст модели → проверенный черновик.
//
// Не ходит ни в стор, ни в кэш, ничего не логирует и не знает про прогон — ровно поэтому её можно
// накрыть табличными пробами, а хендлер остаётся одной веткой.
//
// ⚠ ДВА ИСХОДА, КОТОРЫЕ РОНЯЮТ ВЕСЬ ОТВЕТ, И БОЛЬШЕ НИКАКИХ:
//  1. finish_reason == "length" — ответ обрезан потолком токенов. Половина черновика неотличима
//     от полного: человек увидел бы четыре группы и решил, что остального модель не заметила.
//  2. в теле нет JSON-объекта, или в объекте нет НИ ОДНОГО из семи ключей, которым есть куда
//     лечь. Это ответ не по схеме; принять его значило бы показать пустое предложение как
//     полноценный ответ. `missing` в эту семёрку не входит намеренно: это читаемый совет, а не
//     значение поля, и ответ из одного совета не отвечает на заданный вопрос.
func parseConstructionDraft(raw, finishReason string) (*pb_common.DesignConstructionDraft, designConstructionStats, error) {
	var stats designConstructionStats

	if strings.EqualFold(strings.TrimSpace(finishReason), "length") {
		return nil, stats, fmt.Errorf("the construction draft was cut by the token ceiling (finish_reason=%q)", finishReason)
	}

	js := designExtractJSONObject(raw)
	if js == "" {
		return nil, stats, fmt.Errorf("no JSON object in the model output (%q)", aiBoundedText(raw, 200))
	}
	var in designRawConstruction
	if err := json.Unmarshal([]byte(js), &in); err != nil {
		return nil, stats, fmt.Errorf("the model output is not a construction draft: %v", err)
	}
	if in.Silhouette == nil && in.Fabric == nil && in.Fit == nil && in.Concept == nil &&
		in.Aspects == nil && in.Callouts == nil && in.Bom == nil {
		return nil, stats, fmt.Errorf("the model output carries none of the construction draft keys")
	}

	out := &pb_common.DesignConstructionDraft{
		Silhouette: designBounded(designScalar(in.Silhouette), designConstructionMaxLongRunes, &stats),
		Fabric:     designBounded(designScalar(in.Fabric), designConstructionMaxLongRunes, &stats),
		Concept:    designBounded(designScalar(in.Concept), designConstructionMaxLongRunes, &stats),
		Fit:        designConstructionFitByFold[designFoldToken(designScalar(in.Fit))],
	}

	// ─── АСПЕКТЫ ───
	seenAspect := make(map[string]struct{})
	for _, a := range designDeref(in.Aspects) {
		key := a.Key.String()
		text := designBounded(a.Text.String(), designConstructionMaxTextRunes, &stats)
		if key == "" || text == "" {
			stats.AspectsDropped++
			continue
		}
		fold := designFoldToken(key)
		if canon, ok := designConstructionAspectByFold[fold]; ok {
			key = canon
		} else {
			// САМОДЕЛЬНЫЙ КЛЮЧ ПРИНИМАЕТСЯ, А НЕ ОТВЕРГАЕТСЯ: редактор аспектов принимает такие
			// от человека, и запрет ровно того же от модели был бы правилом про автора, а не
			// про данные. Обрезается по колонке, иначе строка не сохранилась бы.
			key = aiBoundedText(key, designConstructionMaxKeyRunes)
			stats.AspectsCustom++
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
		out.Aspects = append(out.Aspects, &pb_common.DesignConstructionAspect{Key: key, Text: text})
	}

	// ─── ВЫНОСКИ ───
	seenCallout := make(map[string]struct{})
	for _, c := range designDeref(in.Callouts) {
		feature := designBounded(c.Feature.String(), designConstructionMaxTextRunes, &stats)
		details := designBounded(c.Details.String(), designConstructionMaxTextRunes, &stats)
		dims := designBounded(c.Dimensions.String(), designConstructionMaxTextRunes, &stats)
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
	}

	// ─── СПЕЦИФИКАЦИЯ ───
	seenBom := make(map[string]struct{})
	for _, l := range designDeref(in.Bom) {
		name := designBounded(l.Name.String(), designConstructionMaxTextRunes, &stats)
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
		colour := l.Colour.String()
		if colour == "" {
			colour = l.ColorUS.String()
		}
		line := &pb_common.DesignConstructionBomLine{
			Name:        name,
			Composition: designBounded(l.Composition.String(), designConstructionMaxTextRunes, &stats),
			Colour:      designBounded(colour, designConstructionMaxTextRunes, &stats),
			Pantone:     designBounded(l.Pantone.String(), designConstructionMaxTextRunes, &stats),
			Section:     designBomEnum(l.Section.String(), designBomSectionByFold, &stats),
			Purpose:     designBomEnum(l.Purpose.String(), designBomPurposeByFold, &stats),
			Kind:        designBomEnum(l.Kind.String(), designBomKindByFold, &stats),
		}
		// ⚠ АРТИКУЛ ОБНУЛЯЕТСЯ ВСЕГДА, И ЭТО ФАЗА, А НЕ ЗАБЫВЧИВОСТЬ: каталог в промпт не уезжает
		// (фаза 4), значит подтвердить предложенный id нечем. Строка, выглядящая связанной и
		// оценённой, но указывающая на чужой артикул, — ошибка себестоимости с ценником; строка
		// без связи — законная и полезная строка спеки.
		if v := l.MaterialID.String(); v != "" && v != "0" {
			stats.MaterialIDs++
		}
		out.Bom = append(out.Bom, line)
	}

	// ─── ЧТО СТОИТ ПРИКОЛОТЬ ───
	seenMissing := make(map[string]struct{})
	for _, m := range designDeref(in.Missing) {
		text := designBounded(m.String(), designConstructionMaxTextRunes, &stats)
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

	return out, stats, nil
}

// designDeref разворачивает необязательный список: отсутствующий и пустой читаются одинаково —
// как «нечего перебирать». Отличать их нужно ровно в одном месте (проверка формы выше), и оно
// стоит до этого вызова.
func designDeref[T any](p *[]T) []T {
	if p == nil {
		return nil
	}
	return *p
}

// designBounded — обрезка по рунам со счётчиком. Обрезка МАРКИРУЕТСЯ (тот же довод, что у
// aiBoundedText): оборванная фраза, прочитанная как законченная, — это другая инструкция.
func designBounded(s string, max int, stats *designConstructionStats) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		stats.Truncated++
	}
	return aiBoundedText(s, max)
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
func designConstructionDraftFromRun(outputText string) *pb_common.DesignConstructionDraft {
	if strings.TrimSpace(outputText) == "" {
		return nil
	}
	draft, _, err := parseConstructionDraft(outputText, "")
	if err != nil {
		return nil
	}
	return draft
}
