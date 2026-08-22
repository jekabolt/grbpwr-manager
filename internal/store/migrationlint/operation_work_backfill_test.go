package migrationlint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// GUARD-ТЕСТЫ МИГРАЦИИ ДАННЫХ 0331 — ВАЙТЛИСТ БЭКФИЛЛА, НОВЫЕ РАБОТЫ, ЧЕСТНОЕ ИМЯ.
//
// ЗАЧЕМ ОНИ, ЕСЛИ ФАЙЛ ПРОЧИТАН ГЛАЗАМИ. Ошибка в этом файле не падает — она ЗАПИСЫВАЕТСЯ. Лишняя
// пара в вайтлисте разметит сто строк свалки правдоподобным враньём, и отличить его потом от
// человеческой разметки будет НЕЧЕМ: у колонки work нет отметки происхождения. Отмывание свалки
// запрещено ограничением владельца, и запрет обязан быть механическим, а не устным.
//
// ЧЕТЫРЕ СВОЙСТВА, КОТОРЫЕ СТЕРЕГУТСЯ ЗДЕСЬ:
//
//  1. СОСТАВ ВАЙТЛИСТА ЗАМОРОЖЕН. Пять пар, пятнадцать строк ожидания. Шестая пара обязана
//     потребовать правки замороженной таблицы вместе с обоснованием — а не проехать в diff'е
//     строкой SQL.
//  2. УНИВЕРСАЛЬНАЯ МАШИНКА В ВАЙТЛИСТ НЕ ПОПАДАЕТ НИКОГДА. Прямострочка, зигзаг, оверлок и
//     прочие «на чём делают что угодно» работу не выводят. Проверяется по УСЛОВИЯМ бэкфилла, а не
//     грепом по файлу: в каталожной части того же файла зигзаг стоит ЗАКОННО (машинка новой работы
//     `slit_overcast`), а в ярлыке свалки законно стоит слово lockstitch (его как раз убирают).
//     Греп по всему тексту был бы либо ложно-красным, либо (после «починки» грепа) вообще никаким.
//  3. КОГЕРЕНТНОСТЬ 0330 ВЫПОЛНЕНА ПО ПОСТРОЕНИЮ. Глагол в условии обязан равняться глаголу
//     работы, машинка в условии — входить в машинки работы. Нарушение здесь означает строку,
//     которую сервер откажется сохранять: владелец открыл бы карточку и не смог её закрыть, а
//     починить её было бы нечем — жеста «снять вид» до выкатки клиента ещё нет.
//  4. ИМЯ, КОТОРОЕ МЕНЯЮТ, ПЕРЕСТАЁТ НАЗЫВАТЬ ЖЕЛЕЗО. Тест подстановки в общем виде не
//     механизируется (Overlock, Zigzag, Coverstitch — это И названия машин, И названия ремесла:
//     запрет «в ярлыке нет слова машинки» был бы ложным расщеплением наоборот), поэтому заморожен
//     ровно тот ярлык, который эта фаза правит.
//
// ЦИТАТА + МУТАЦИЯ, ОБЕ ПОЛОВИНЫ ПОСТОЯННЫЕ. TestOperationWorkBackfillWhitelist печатает разбор
// вайтлиста и проверяет восемь правил; TestOperationWorkBackfillGuardsAreNotFalseGreen ломает
// РАЗОБРАННЫЙ вайтлист пятнадцатью способами и требует жалобу с именем виновника на каждый.
//
// ЖИВОЙ ПРОГОН МУТАЦИИ ПО САМОМУ ФАЙЛУ (2026-08-22), сверх постоянных тестов — потому что
// постоянные ломают разобранную СТРУКТУРУ, а этот доказывает, что красноту даёт и порча
// НАСТОЯЩЕГО sql-ТЕКСТА, вместе с разбором. Прогон шёл по КОПИИ каталога миграций (go test
// -overlay подменял migrationsDir; рабочее дерево не тронуто):
//
//	(1) в вайтлист дописан оператор
//	    UPDATE tech_card_operation SET work = 'join_lockstitch'
//	      WHERE work IS NULL AND operation_type = 'machine' AND machine_type = 'lockstitch';
//	    → «отбор по УНИВЕРСАЛЬНОЙ машинке lockstitch», плюс «вайтлист разошёлся с замороженным:
//	      ЛИШНЯЯ пара join_lockstitch ← machine_type=lockstitch, operation_type=machine»;
//	      мутационный тест упал на своей же преамбуле «исходный вайтлист обязан быть чистым».
//	(2) из оператора 6.1 убрано `work IS NULL AND`
//	    → «оператор buttonhole без `work IS NULL`: он перезапишет уже назначенную работу».
//	(3) ярлык свалки заменён обратно на 'Join — lockstitch'
//	    → «новый ярлык join_lockstitch называет машинку lockstitch — тест подстановки провален»,
//	      плюс расхождение с замороженным переименованием.
//	(4) в Down дописано `UPDATE tech_card_operation SET work = NULL WHERE work = 'press_open'`
//	    → «Down трогает tech_card_operation — откат стёр бы и человеческую разметку».
//	(5) в условии переименования длинное тире заменено дефисом ('Join - lockstitch')
//	    → «условие называет старый ярлык …, а в каталоге засеян … — UPDATE не найдёт ни одной
//	      строки, и переименование молча не случится». Тот самый класс ошибки, который глазами
//	      читается как правильный: миграция прошла бы зелёной и не сделала ничего.
//	Копия удалена, дерево чистое, все тесты снова зелёные.

const operationWorkBackfillMigration = "0331_operation_work_backfill.sql"

// mintedWorkCount — сколько работ минтит ИМЕННО ЭТОТ файл. Как и seededWorkCount у 0329, число
// постоянно навсегда: следующая миграция добавит свои работы своим файлом и заморозит их своим
// тестом.
const mintedWorkCount = 4

// catalogWorkCountAfterBackfill — сколько работ в каталоге ПОСЛЕ 0331. Объединённый срез нужен не
// для красоты: уникальность токена и sort'а обязана проверяться ПОПЕРЁК ФАЙЛОВ, иначе столкновение
// поймает не тест, а прод — вставкой, упавшей на дубль ключа при старте.
const catalogWorkCountAfterBackfill = seededWorkCount + mintedWorkCount

// frozenMintedTokenVerbDigest — sha256 по отсортированным парам «токен=глагол» ЧЕТЫРЁХ работ 0331.
//
// ⚠️ МЕНЯТЬ ТОЛЬКО ВМЕСТЕ С ПИСЬМЕННЫМ ОБОСНОВАНИЕМ ЗДЕСЬ ЖЕ — по тому же доводу, что и у 0329:
// токен уезжает в проекцию отпечатка строки шага, глагол входит в правило когерентности, и правка
// любого из них задним числом раздваивает подпись уже подписанной карточки. Законный повод ровно
// один: исправление опечатки ДО первого применения файла.
const frozenMintedTokenVerbDigest = "ebcebb7fac5c8a6039c2cb8d7df0d6ef7d257d8fa5cf1e012617a106e8482234"

// universalMachines — машинки, на которых делают ЧТО УГОДНО, и потому работа по ним НЕ ВЫВОДИТСЯ.
//
// ЭТО СУЖДЕНИЕ ТЕХНОЛОГА, А НЕ ВЫВОД ИЗ КАТАЛОГА, И ПОПЫТКА ВЫВЕСТИ ЕГО БЫЛА ПРОВЕРЕНА И
// ОТБРОШЕНА: «универсальная = её называет больше одной работы каталога» дало бы оверлоку
// НЕуниверсальность (его называет ровно одна работа, `overlock_serge`), а владелец на две
// оверлочные строки прода сказал «руками». Оверлок и стачивает, и обмётывает, и делает то и
// другое одним проходом — по записи «machine + overlock» не видно, какую из работ делали.
//
//	lockstitch, lockstitch_double_needle — сто строк свалки прода и есть они
//	zigzag                               — обмётка, закрепка, аппликация, прорезь (0331!)
//	overlock                             — обмётка края ИЛИ стачивание с обмёткой
//	coverstitch, chainstitch             — подгибка, отстрочка, стачивание трикотажа
//	binding_taping                       — бейка по краю ИЛИ соединение бейкой
//	other                                — «прочее» не выводит вообще ничего по определению
//
// Список СВЕРЯЕТСЯ СО СЛОВАРЁМ entity (тест ниже): опечатка в токене сделала бы запрет вакуумным —
// guard пропустил бы ровно ту машинку, которую думал сторожить.
var universalMachines = []string{
	"lockstitch", "lockstitch_double_needle", "zigzag", "overlock",
	"coverstitch", "chainstitch", "binding_taping", "other",
}

// frozenBackfillWhitelist — ВАЙТЛИСТ ЦЕЛИКОМ, замер прода 2026-08-22.
//
// ExpectRows — ОЖИДАНИЕ ДЛЯ СВЕРКИ ПОСЛЕ ПРОГОНА, а не источник отбора: отбирает условие, потому
// что база растёт под руками (99 → 105 → 115 → 126 операций за считанные дни). Число здесь затем,
// чтобы в день выкатки было с чем сравнить «сколько строк получило работу» — и чтобы расхождение
// стало вопросом, а не незамеченным фактом.
var frozenBackfillWhitelist = []struct {
	Token      string
	Conds      map[string]string
	ExpectRows int
	Why        string
}{
	{"buttonhole", map[string]string{"operation_type": "machine", "machine_type": "buttonhole"}, 3,
		"петельный автомат делает ровно одну работу"},
	{"button_attach", map[string]string{"operation_type": "machine", "machine_type": "button_attach"}, 3,
		"пуговичный автомат так же однозначен"},
	{"embroidery", map[string]string{"operation_type": "machine", "machine_type": "embroidery"}, 1,
		"вышивальная машина не шьёт швов"},
	{"press_open", map[string]string{"operation_type": "press_open"}, 5,
		"однозначен сам глагол: второе написание разутюжки снято 0327"},
	{"press_flat", map[string]string{"operation_type": "press", "press_action": "press_flat"}, 3,
		"приём ЗАПИСАН; восемь ВТО-строк без приёма остаются человеку"},
}

// frozenLabelFix — единственное переименование фазы (решение 7 плана: парадной волны нет).
var frozenLabelFix = labelUpdate{
	Token: "join_lockstitch", OldLabel: "Join — lockstitch", NewLabel: "Join / seam",
}

// frozenRetire — единственное снятие фазы: предок расщепления B4.
const frozenRetire = "gather_ease"

type backfillRule struct {
	Token      string
	WorkIsNull bool
	Conds      map[string]string
}

type labelUpdate struct{ Token, OldLabel, NewLabel string }

type catalogUpdates struct {
	Retires []string
	Labels  []labelUpdate
}

var (
	// backfillStmtRe — оператор бэкфилла целиком. `SET work = '<токен>'` и НИЧЕГО БОЛЬШЕ: запятая
	// после токена в разбор не попадёт, и оператор, трогающий второе поле, будет назван
	// неразобранным, а не пропущен.
	backfillStmtRe = regexp.MustCompile(`^UPDATE tech_card_operation SET work = '([a-z0-9_]+)' WHERE (.+)$`)
	// condRe — одно условие вида `колонка = 'значение'`.
	condRe = regexp.MustCompile(`^([a-z_]+) = '([a-z0-9_]+)'$`)
	// retireRe / labelRe — два оператора над каталогом, каждый со своим гейтом идемпотентности.
	retireRe = regexp.MustCompile(`^UPDATE operation_work SET retired_at = CURRENT_TIMESTAMP WHERE token = '([a-z0-9_]+)' AND retired_at IS NULL$`)
	labelRe  = regexp.MustCompile(`^UPDATE operation_work SET label = '([^']*)' WHERE token = '([a-z0-9_]+)' AND label = '([^']*)'$`)
	// touchesOperationsRe — ЛЮБОЕ обращение к таблице шагов, каким бы оператором оно ни было.
	touchesOperationsRe = regexp.MustCompile(`(?i)\b(UPDATE|DELETE\s+FROM|INSERT\s+INTO|REPLACE\s+INTO|ALTER\s+TABLE|DROP\s+TABLE|TRUNCATE(?:\s+TABLE)?)\s+` + "`?tech_card_operation`?" + `\b`)
	// ddlRe — схема этим файлом не правится вовсе.
	ddlRe = regexp.MustCompile(`(?i)\b(ALTER\s+TABLE|CREATE\s+TABLE|DROP\s+TABLE|TRUNCATE|ADD\s+CONSTRAINT|DROP\s+CHECK)\b`)
)

// splitStatements выбрасывает комментарии и режет секцию на операторы с нормализованными
// пробелами. Комментарии выбрасываются ПЕРВЫМИ: шапка 0331 пересказывает и вайтлист, и запрет, и
// разбор по её словам был бы разбором пересказа, а не файла.
func splitStatements(section string) []string {
	var body []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		body = append(body, line)
	}
	var out []string
	for _, stmt := range strings.Split(strings.Join(body, "\n"), ";") {
		stmt = strings.Join(strings.Fields(stmt), " ")
		if stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

// upSection возвращает секцию Up миграции (Down — инструмент разработки, вайтлистом не является).
func upSection(t *testing.T, content string) string {
	t.Helper()
	up, _, ok := strings.Cut(content, "-- +migrate Down")
	if !ok {
		t.Fatalf("%s: нет секции Down — файл переписан, и разбор ниже читал бы не то",
			operationWorkBackfillMigration)
	}
	return up
}

// parseBackfill разбирает секцию Up СТРОГО: каждый оператор, трогающий tech_card_operation,
// обязан разобраться как бэкфилл. Мягкий разбор («что не совпало — пропустим») дал бы ложную
// зелень ровно на том операторе, который испортили.
func parseBackfill(t *testing.T, content string) ([]backfillRule, catalogUpdates) {
	t.Helper()
	var rules []backfillRule
	var upd catalogUpdates

	for _, stmt := range splitStatements(upSection(t, content)) {
		switch {
		case retireRe.MatchString(stmt):
			upd.Retires = append(upd.Retires, retireRe.FindStringSubmatch(stmt)[1])
			continue
		case labelRe.MatchString(stmt):
			m := labelRe.FindStringSubmatch(stmt)
			upd.Labels = append(upd.Labels, labelUpdate{Token: m[2], OldLabel: m[3], NewLabel: m[1]})
			continue
		case !touchesOperationsRe.MatchString(stmt):
			continue // вставки каталога — их проверяет объединённый разбор сида
		}

		m := backfillStmtRe.FindStringSubmatch(stmt)
		if m == nil {
			t.Fatalf("%s: оператор трогает tech_card_operation, но не разбирается как бэкфилл "+
				"(`UPDATE tech_card_operation SET work = '<токен>' WHERE …`): %s",
				operationWorkBackfillMigration, stmt)
		}
		rule := backfillRule{Token: m[1], Conds: map[string]string{}}
		for _, cond := range strings.Split(m[2], " AND ") {
			cond = strings.TrimSpace(cond)
			if cond == "work IS NULL" {
				rule.WorkIsNull = true
				continue
			}
			c := condRe.FindStringSubmatch(cond)
			if c == nil {
				t.Fatalf("%s: условие отбора не разбирается (%q) — допустимы только `work IS NULL` "+
					"и `колонка = 'значение'`; всё прочее (OR, IN, LIKE, подзапрос) обязано "+
					"обсуждаться, а не проезжать мимо guard'а", operationWorkBackfillMigration, cond)
			}
			if _, dup := rule.Conds[c[1]]; dup {
				t.Fatalf("%s: условие по колонке %s задвоено в одном операторе",
					operationWorkBackfillMigration, c[1])
			}
			rule.Conds[c[1]] = c[2]
		}
		rules = append(rules, rule)
	}
	return rules, upd
}

// whitelistKey — канонический отпечаток одной пары вайтлиста, независимый от порядка условий.
func whitelistKey(token string, conds map[string]string) string {
	parts := make([]string, 0, len(conds))
	for col, val := range conds {
		parts = append(parts, col+"="+val)
	}
	sort.Strings(parts)
	return token + " ← " + strings.Join(parts, ", ")
}

// checkBackfill — ВСЕ ПРАВИЛА В ОДНОЙ ЧИСТОЙ ФУНКЦИИ: то, что гоняют по настоящему файлу, и то,
// что гоняют по испорченной копии, обязано быть ОДНИМ кодом. cat — ОБЪЕДИНЁННЫЙ каталог (0329 +
// 0331): работы, которые бэкфилл присваивает, живут в 0329, а проверять их приходится здесь.
func checkBackfill(cat seedCatalog, rules []backfillRule, upd catalogUpdates) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	works := make(map[string]seedWork, len(cat.Works))
	for _, w := range cat.Works {
		works[w.Token] = w
	}
	machinesOf := make(map[string]map[string]bool, len(cat.Works))
	for _, m := range cat.Machines {
		if machinesOf[m[0]] == nil {
			machinesOf[m[0]] = map[string]bool{}
		}
		machinesOf[m[0]][m[1]] = true
	}
	universal := make(map[string]bool, len(universalMachines))
	for _, m := range universalMachines {
		universal[m] = true
	}
	retired := make(map[string]bool, len(upd.Retires))
	for _, tok := range upd.Retires {
		retired[tok] = true
	}

	for _, r := range rules {
		// (1) ЕДИНСТВЕННЫЙ ЗАКОННЫЙ ПЕРЕХОД — NULL → токен.
		if !r.WorkIsNull {
			add("оператор %s без `work IS NULL`: он перезапишет уже назначенную работу, а «сломаться "+
				"можно, исчезнуть нельзя» запрещает и это", r.Token)
		}
		// (2) ОТМЫВАНИЕ СВАЛКИ — механический запрет.
		for col, val := range r.Conds {
			if universal[val] {
				add("отбор по УНИВЕРСАЛЬНОЙ машинке %s (%s = '%s' → %s): по такой записи работа НЕ "+
					"выводится, и разметка была бы утверждением о факте, которого в ней нет",
					val, col, val, r.Token)
			}
		}
		w, known := works[r.Token]
		if !known {
			add("работа %s не существует ни в 0329, ни в 0331 — внешний ключ уронит миграцию на проде", r.Token)
			continue
		}
		// (3) СНЯТУЮ РАБОТУ БЭКФИЛЛ НЕ НАЗНАЧАЕТ: пикер её не предложит, а строка окажется
		// размеченной пунктом, которого человек не выбирал и выбрать не мог.
		if retired[r.Token] {
			add("работа %s снята этим же файлом (retire) и не может назначаться бэкфиллом", r.Token)
		}
		// (4) КОГЕРЕНТНОСТЬ, ПРАВИЛО 3 (0330): глагол шага = глагол работы. Нарушение = строка,
		// которую сервер откажется сохранять.
		switch verb, ok := r.Conds["operation_type"]; {
		case !ok:
			add("оператор %s не называет глагол в условии — под отбор попадут строки любого глагола, "+
				"и правило когерентности 0330 запретит их сохранение", r.Token)
		case verb != w.Verb:
			add("оператор %s отбирает глагол %q, а работа объявлена как %q: размеченную строку "+
				"сервер откажется сохранять (work_verb_mismatch)", r.Token, verb, w.Verb)
		}
		// (5) КОГЕРЕНТНОСТЬ, ПРАВИЛО 4: названная машинка обязана быть машинкой работы. При режиме
		// fixed сервер этого не проверяет — но запись, где работа говорит одно, а машинка другое,
		// всё равно ложь, просто молчаливая.
		if machine, ok := r.Conds["machine_type"]; ok && !machinesOf[r.Token][machine] {
			add("оператор %s отбирает машинку %q, которой нет в machine-списке работы (%s)",
				r.Token, machine, strings.Join(sortedKeys(machinesOf[r.Token]), " / "))
		}
	}

	// (6) СОСТАВ ВАЙТЛИСТА ЗАМОРОЖЕН.
	got := make(map[string]bool, len(rules))
	for _, r := range rules {
		key := whitelistKey(r.Token, r.Conds)
		if got[key] {
			add("пара вайтлиста задвоена: %s", key)
		}
		got[key] = true
	}
	want := make(map[string]bool, len(frozenBackfillWhitelist))
	for _, w := range frozenBackfillWhitelist {
		want[whitelistKey(w.Token, w.Conds)] = true
	}
	for key := range got {
		if !want[key] {
			add("вайтлист разошёлся с замороженным: ЛИШНЯЯ пара %s — если она законна, её обязан "+
				"подтвердить владелец, а не diff", key)
		}
	}
	for key := range want {
		if !got[key] {
			add("вайтлист разошёлся с замороженным: ПРОПАЛА пара %s", key)
		}
	}

	// (7) ЕДИНСТВЕННОЕ ПЕРЕИМЕНОВАНИЕ ФАЗЫ, И ОНО ОБЯЗАНО ПЕРЕСТАТЬ НАЗЫВАТЬ ЖЕЛЕЗО.
	switch len(upd.Labels) {
	case 0:
		add("переименования нет вовсе: свалка обязана получить имя по работе (%s → %q)",
			frozenLabelFix.Token, frozenLabelFix.NewLabel)
	case 1:
		got := upd.Labels[0]
		if got != frozenLabelFix {
			add("переименование разошлось с замороженным: %+v против %+v", got, frozenLabelFix)
		}
		if strings.Contains(strings.ToLower(got.NewLabel), "lockstitch") {
			add("новый ярлык %s называет машинку lockstitch — тест подстановки провален: переставь "+
				"работу на другую машинку, и имя станет ложью", got.Token)
		}
		if got.OldLabel == "" {
			add("условие переименования не называет СТАРЫЙ ярлык: повтор миграции затрёт правку, " +
				"которую владелец успел сделать рукой")
		}
		// ⚠️ СТАРЫЙ ЯРЛЫК СВЕРЯЕТСЯ С ТЕМ, ЧТО РЕАЛЬНО ЗАСЕЯНО, А НЕ С КОНСТАНТОЙ ЭТОГО ТЕСТА, и
		// это не педантизм: ярлык 0329 несёт длинное тире, и стоит написать в условии дефис (или
		// другое тире), как UPDATE перестанет находить строку. Переименование при этом не упадёт —
		// оно МОЛЧА не случится, и свалка уедет на прод со своим именем по железу. Ровно тот класс
		// ошибки, который читается глазами как правильный.
		switch seeded, ok := works[got.Token]; {
		case !ok:
			// Та же ловушка, что у тире, только на оси ТОКЕНА: опечатка, повторённая и в SQL, и в
			// замороженной константе выше, прошла бы обе сверки — а UPDATE молча не нашёл бы строку.
			// Существование сверяется с ЗАСЕЯННЫМ каталогом, до которого опечатке не дотянуться.
			add("переименование называет токен %s, которого нет ни в 0329, ни в 0331 — UPDATE не "+
				"найдёт ни одной строки, и переименование молча не случится", got.Token)
		case seeded.Label != got.OldLabel:
			add("условие переименования называет старый ярлык %q, а в каталоге у %s засеян %q — "+
				"UPDATE не найдёт ни одной строки, и переименование молча не случится",
				got.OldLabel, got.Token, seeded.Label)
		}
	default:
		add("переименований %d, а фаза разрешила ровно одно (решение 7: парадной волны нет)", len(upd.Labels))
	}

	// (8) СНЯТИЕ — ровно предок расщепления, и он не должен быть работой, которую кто-то назначает.
	if len(upd.Retires) != 1 || upd.Retires[0] != frozenRetire {
		add("снятие разошлось с замороженным: %v, ожидался ровно [%s]", upd.Retires, frozenRetire)
	}
	for _, tok := range upd.Retires {
		if _, ok := works[tok]; !ok {
			// Симметрично переименованию: токен, которого нет в засеянном каталоге, — это UPDATE,
			// который пройдёт зелёным и не сделает ничего, и предок остался бы в пикере навсегда.
			add("снятие называет токен %s, которого нет ни в 0329, ни в 0331 — UPDATE не найдёт "+
				"ни одной строки, и снятие молча не случится", tok)
		}
	}

	return problems
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mintedCatalog разбирает КАТАЛОЖНУЮ часть 0331 тем же разбором, что и сид 0329.
func mintedCatalog(t *testing.T) seedCatalog {
	t.Helper()
	return parseSeedCatalog(t, operationWorkBackfillMigration,
		readMigrationFile(t, operationWorkBackfillMigration))
}

// unionCatalog склеивает 0329 и 0331 в один срез, отсортированный по sort. Сортировка нужна ровно
// для правила монотонности внутри checkSeedCatalog: в файле-то работы 0331 стоят межстрочными
// номерами (75, 141, 142, 165), потому что новая работа встаёт РЯДОМ С РОДНЁЙ, а не в конец.
func unionCatalog(t *testing.T) seedCatalog {
	t.Helper()
	seed := parseSeedCatalog(t, operationWorkMigration, readMigrationFile(t, operationWorkMigration))
	minted := mintedCatalog(t)
	union := seedCatalog{
		Works:    append(append([]seedWork(nil), seed.Works...), minted.Works...),
		Machines: append(append([][2]string(nil), seed.Machines...), minted.Machines...),
		Syns:     append(append([][2]string(nil), seed.Syns...), minted.Syns...),
	}
	sort.SliceStable(union.Works, func(i, j int) bool { return union.Works[i].Sort < union.Works[j].Sort })
	return union
}

// TestOperationWorkBackfillWhitelist — ЦИТАТА: разбор вайтлиста в лог плюс все восемь правил.
func TestOperationWorkBackfillWhitelist(t *testing.T) {
	rules, upd := parseBackfill(t, readMigrationFile(t, operationWorkBackfillMigration))
	for _, p := range checkBackfill(unionCatalog(t), rules, upd) {
		t.Error(p)
	}

	total := 0
	for _, w := range frozenBackfillWhitelist {
		total += w.ExpectRows
	}
	lines := make([]string, 0, len(rules))
	for _, r := range rules {
		lines = append(lines, whitelistKey(r.Token, r.Conds))
	}
	sort.Strings(lines)
	t.Logf("вайтлист (%d пар, ожидание прода %d строк из 126):\n  %s",
		len(rules), total, strings.Join(lines, "\n  "))
	t.Logf("переименовано: %s %q → %q; снято: %v",
		frozenLabelFix.Token, frozenLabelFix.OldLabel, frozenLabelFix.NewLabel, upd.Retires)
	t.Logf("НЕ размечается ни одной строки: %s", strings.Join(universalMachines, ", "))
}

// TestOperationWorkBackfillTouchesNoSchema — файл данных не правит схему ВООБЩЕ.
//
// Отдельным тестом, а не строкой в общем: правка схемы в файле, который люди читают как «данные»,
// проехала бы ревью именно потому, что его не читают как миграцию схемы.
func TestOperationWorkBackfillTouchesNoSchema(t *testing.T) {
	body := readMigrationFile(t, operationWorkBackfillMigration)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		if hit := ddlRe.FindString(line); hit != "" {
			t.Errorf("%s: файл данных правит схему (%q). Ни ALTER, ни CREATE, ни CHECK: словарь "+
				"работ растёт строками каталога, а не членами enum и не новыми ограничениями",
				operationWorkBackfillMigration, strings.TrimSpace(hit))
		}
	}
	// Секция Down не имеет права трогать строки шагов вовсе: после Up разметка бэкфилла
	// НЕОТЛИЧИМА от человеческой (у колонки нет отметки происхождения), и обратный UPDATE стёр бы
	// работу человека.
	_, down, _ := strings.Cut(body, "-- +migrate Down")
	for _, stmt := range splitStatements(down) {
		if touchesOperationsRe.MatchString(stmt) {
			t.Errorf("%s: Down трогает tech_card_operation (%q) — откат стёр бы и человеческую "+
				"разметку вместе с машинной", operationWorkBackfillMigration, stmt)
		}
	}
}

// TestOperationWorkMintedWorksJoinTheCatalog — четыре новые работы проходят ВСЕ правила сида, и
// проверяются они в ОБЪЕДИНЁННОМ срезе: столкновение токена или sort'а с уже засеянным поймает
// иначе не тест, а упавший старт прода.
func TestOperationWorkMintedWorksJoinTheCatalog(t *testing.T) {
	minted := mintedCatalog(t)
	if len(minted.Works) != mintedWorkCount {
		t.Fatalf("%s минтит %d работ, ожидалось %d", operationWorkBackfillMigration,
			len(minted.Works), mintedWorkCount)
	}
	for _, p := range checkSeedCatalog(unionCatalog(t), catalogWorkCountAfterBackfill) {
		t.Error(p)
	}
	prev := -1
	for _, w := range minted.Works {
		if w.Sort <= prev {
			t.Errorf("работа %s: sort %d не возрастает в файле (предыдущий %d) — читать вайтлист "+
				"глазами станет нечем", w.Token, w.Sort, prev)
		}
		prev = w.Sort
	}
	if got := tokenVerbDigest(minted); got != frozenMintedTokenVerbDigest {
		t.Errorf("пары токен→глагол 0331 изменились:\n  было %s\n  стало %s\nТокен уезжает в "+
			"отпечаток строки шага, глагол — в правило когерентности: правка задним числом "+
			"раздваивает подпись. Если правка осознанна — поменяйте константу и напишите, ПОЧЕМУ.",
			frozenMintedTokenVerbDigest, got)
	}
	names := make([]string, 0, len(minted.Works))
	for _, w := range minted.Works {
		names = append(names, fmt.Sprintf("%s (%s/%s, %s %s, sort %d) %q",
			w.Token, w.Verb, w.Stage, w.MachineMode, w.DefaultMachine, w.Sort, w.Label))
	}
	t.Logf("минтится %d работ:\n  %s", len(minted.Works), strings.Join(names, "\n  "))
	t.Logf("каталог после 0331: %d работ, %d машинок, %d синонимов; sha256(token=verb) 0331 = %s",
		catalogWorkCountAfterBackfill, len(unionCatalog(t).Machines), len(unionCatalog(t).Syns),
		tokenVerbDigest(minted))
}

// TestUniversalMachineListIsRealVocabulary — GUARD НАД GUARD'ОМ. Опечатка в этом списке сделала бы
// запрет вакуумным: тест продолжал бы зеленеть, пропуская ровно ту машинку, которую сторожит.
func TestUniversalMachineListIsRealVocabulary(t *testing.T) {
	for _, m := range universalMachines {
		if !entity.ValidMachineTypes[entity.TechCardMachineType(m)] {
			t.Errorf("«универсальная машинка» %q не существует в entity.MachineTypeTokens — запрет "+
				"по ней не сработает никогда", m)
		}
	}
	// Перепись прода 2026-08-22: 100 строк lockstitch + 2 overlock + 1 zigzag = 103 строки, ради
	// которых запрет и стоит. Если бы хоть одна из трёх выпала из списка, отмывание стало бы
	// возможным молча.
	for _, m := range []string{"lockstitch", "overlock", "zigzag"} {
		found := false
		for _, u := range universalMachines {
			if u == m {
				found = true
			}
		}
		if !found {
			t.Errorf("машинка %q, на которой стоят 103 неразмеченные строки прода, выпала из списка "+
				"универсальных — вайтлист смог бы её принять", m)
		}
	}
}

// TestOperationWorkBackfillGuardsAreNotFalseGreen — МУТАЦИЯ. Каждый пункт ломает РАЗОБРАННЫЙ
// вайтлист одним способом и требует жалобы с именем виновника. Без него зелень тестов выше
// доказывала бы только то, что регулярки компилируются.
func TestOperationWorkBackfillGuardsAreNotFalseGreen(t *testing.T) {
	union := unionCatalog(t)
	baseRules, baseUpd := parseBackfill(t, readMigrationFile(t, operationWorkBackfillMigration))
	if p := checkBackfill(union, baseRules, baseUpd); len(p) != 0 {
		t.Fatalf("исходный вайтлист обязан быть чистым, иначе мутации ничего не доказывают: %v", p)
	}

	clone := func() ([]backfillRule, catalogUpdates) {
		rules := make([]backfillRule, 0, len(baseRules))
		for _, r := range baseRules {
			conds := make(map[string]string, len(r.Conds))
			for k, v := range r.Conds {
				conds[k] = v
			}
			rules = append(rules, backfillRule{Token: r.Token, WorkIsNull: r.WorkIsNull, Conds: conds})
		}
		upd := catalogUpdates{
			Retires: append([]string(nil), baseUpd.Retires...),
			Labels:  append([]labelUpdate(nil), baseUpd.Labels...),
		}
		return rules, upd
	}

	cases := []struct {
		name   string
		mutate func(rules *[]backfillRule, upd *catalogUpdates) string
	}{
		{"свалка дописана в вайтлист", func(rules *[]backfillRule, _ *catalogUpdates) string {
			*rules = append(*rules, backfillRule{
				Token: "join_lockstitch", WorkIsNull: true,
				Conds: map[string]string{"operation_type": "machine", "machine_type": "lockstitch"},
			})
			return "УНИВЕРСАЛЬНОЙ машинке lockstitch"
		}},
		{"свалка дописана: расхождение с замороженным вайтлистом", func(rules *[]backfillRule, _ *catalogUpdates) string {
			*rules = append(*rules, backfillRule{
				Token: "join_lockstitch", WorkIsNull: true,
				Conds: map[string]string{"operation_type": "machine", "machine_type": "lockstitch"},
			})
			return "ЛИШНЯЯ пара"
		}},
		{"оверлок дописан в вайтлист", func(rules *[]backfillRule, _ *catalogUpdates) string {
			*rules = append(*rules, backfillRule{
				Token: "overlock_serge", WorkIsNull: true,
				Conds: map[string]string{"operation_type": "machine", "machine_type": "overlock"},
			})
			return "УНИВЕРСАЛЬНОЙ машинке overlock"
		}},
		{"снят гейт work IS NULL", func(rules *[]backfillRule, _ *catalogUpdates) string {
			(*rules)[0].WorkIsNull = false
			return "без `work IS NULL`"
		}},
		{"глагол в условии разошёлся с глаголом работы", func(rules *[]backfillRule, _ *catalogUpdates) string {
			(*rules)[0].Token = "press_flat"
			return "размеченную строку сервер откажется сохранять"
		}},
		{"машинка не из списка работы", func(rules *[]backfillRule, _ *catalogUpdates) string {
			(*rules)[0].Conds["machine_type"] = "bartack"
			return "которой нет в machine-списке работы"
		}},
		{"пара вайтлиста пропала", func(rules *[]backfillRule, _ *catalogUpdates) string {
			*rules = (*rules)[1:]
			return "ПРОПАЛА пара"
		}},
		{"глагол в условии не назван вовсе", func(rules *[]backfillRule, _ *catalogUpdates) string {
			delete((*rules)[0].Conds, "operation_type")
			return "не называет глагол в условии"
		}},
		{"бэкфилл назначает снятую работу", func(rules *[]backfillRule, upd *catalogUpdates) string {
			(*rules)[0].Token = "gather_ease"
			upd.Retires = []string{"gather_ease"}
			return "снята этим же файлом"
		}},
		{"ярлык свалки возвращён к имени по железу", func(_ *[]backfillRule, upd *catalogUpdates) string {
			upd.Labels[0].NewLabel = "Join — lockstitch"
			return "тест подстановки провален"
		}},
		{"условие переименования не называет старый ярлык", func(_ *[]backfillRule, upd *catalogUpdates) string {
			upd.Labels[0].OldLabel = ""
			return "не называет СТАРЫЙ ярлык"
		}},
		{"в старом ярлыке длинное тире заменено дефисом", func(_ *[]backfillRule, upd *catalogUpdates) string {
			upd.Labels[0].OldLabel = "Join - lockstitch"
			return "молча не случится"
		}},
		{"снятие предка расщепления пропало", func(_ *[]backfillRule, upd *catalogUpdates) string {
			upd.Retires = nil
			return "снятие разошлось с замороженным"
		}},
		{"снимается токен, которого нет в каталоге", func(_ *[]backfillRule, upd *catalogUpdates) string {
			upd.Retires = []string{"gather_easee"}
			return "снятие молча не случится"
		}},
		{"переименовывается токен, которого нет в каталоге", func(_ *[]backfillRule, upd *catalogUpdates) string {
			upd.Labels[0].Token = "join_lockstitchh"
			return "переименование называет токен"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules, upd := clone()
			want := tc.mutate(&rules, &upd)
			problems := checkBackfill(union, rules, upd)
			if len(problems) == 0 {
				t.Fatalf("мутация %q НЕ дала ни одной жалобы — guard ложно-зелёный", tc.name)
			}
			if !strings.Contains(strings.Join(problems, "\n"), want) {
				t.Fatalf("мутация %q дала жалобы, но ни одна не назвала %q:\n%s",
					tc.name, want, strings.Join(problems, "\n"))
			}
			t.Logf("красное, как и требуется: %s", problems[0])
		})
	}

	// Отдельно — заморозка пар: подмена глагола обязана сдвинуть хеш 0331.
	minted := mintedCatalog(t)
	minted.Works[0].Verb = "press"
	if tokenVerbDigest(minted) == frozenMintedTokenVerbDigest {
		t.Error("подмена глагола не сдвинула замороженный хеш 0331 — заморозка ничего не стережёт")
	}
}
