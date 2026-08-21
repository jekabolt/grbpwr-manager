package migrationlint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// GUARD-ТЕСТЫ СИДА КАТАЛОГА РАБОТ (0329_operation_work_catalog.sql).
//
// ЗАЧЕМ ОНИ ВООБЩЕ. Словари каталога — стадия, режим машинки, глагол, машинка — НЕ ЗАКРЫТЫ
// CHECK'ом в схеме, и это решение фазы, а не забывчивость: `ADD CONSTRAINT CHECK` в MySQL 8
// копирует таблицу целиком, потолок на весь прогон миграций зашит в internal/store/store.go пятью
// минутами, а заводить справочную таблицу под каждый словарик из трёх членов план запретил прямо
// («4 таблицы, и только они»). Значит единственное, что стоит между опечаткой в сиде и продом, —
// эти тесты. Они читают ТЕКСТ миграции; базы не касаются вовсе.
//
// ЦИТАТА + МУТАЦИЯ — ОБЕ ПОЛОВИНЫ ЗДЕСЬ, И ВТОРАЯ ПОСТОЯННАЯ, А НЕ РАЗОВАЯ.
// Цитата: TestOperationWorkCatalogSeed печатает перепись сида (53 / 31 / 254) и проверяет восемь
// правил. Мутация: TestOperationWorkCatalogGuardsAreNotFalseGreen ломает РАЗОБРАННЫЙ сид семью
// способами (удалить кириллический синоним, задвоить токен, подменить глагол, стадию, машинку,
// перевести fixed в none, сдвинуть sort) и ТРЕБУЕТ, чтобы каждая мутация дала жалобу с именем
// виновника. Разовый прогон «я поломал, оно покраснело» доказывает это на один день; этот тест —
// на каждый прогон CI.
//
// ЗАМОРОЖЕННЫЙ ХЕШ ПАР token→verb (TestOperationWorkTokenVerbPairsAreFrozen) — не педантизм.
// Токен уезжает в проекцию дайджеста строки шага, глагол входит в правило когерентности; правка
// любого из них задним числом раздваивает отпечаток УЖЕ ПОДПИСАННОЙ карточки. Поменять константу
// можно — но только осознанно и с обоснованием рядом, а не мимоходом вместе с правкой ярлыка.
//
// ПОЧЕМУ РАЗБОР СТРОГИЙ ДО ПРИДИРЧИВОСТИ: каждая непустая строка внутри INSERT'а ОБЯЗАНА
// разобраться, иначе тест жалуется. Мягкий разбор («что не совпало — пропустим») дал бы ложную
// зелень ровно на той строке, которую испортили: пропущенная строка не проверяется ничем.

const operationWorkMigration = "0329_operation_work_catalog.sql"

// seededWorkCount — сколько работ сеет ИМЕННО ЭТОТ файл. Число не «сколько работ в каталоге»:
// следующие миграции (0331 и далее) добавляют свои работы своими файлами и замораживают свои пары
// своими тестами. Здесь оно постоянно навсегда.
const seededWorkCount = 53

// frozenTokenVerbDigest — sha256 по отсортированным парам «токен=глагол» сида 0329.
//
// ⚠️ МЕНЯТЬ ТОЛЬКО ВМЕСТЕ С ПИСЬМЕННЫМ ОБОСНОВАНИЕМ ЗДЕСЬ ЖЕ. Изменение этой константы означает,
// что у существующей работы поменялся глагол, то есть что отпечатки строк шагов, несущих её токен,
// перестали совпадать с теми, что были подписаны. Добавление НОВОЙ работы в 0329 тоже сюда попадёт
// — но 0329 применён и неизменяем, так что законный повод ровно один: исправление опечатки ДО
// первого применения файла.
const frozenTokenVerbDigest = "a60e30bfd0b4602f2d57c528397e2bab58cb9d0f7cc650749db3267f47289709"

type seedWork struct {
	Token          string
	Verb           string
	Stage          string
	Label          string
	MachineMode    string
	DefaultMachine string // "" == NULL
	Sort           int
}

type seedCatalog struct {
	Works    []seedWork
	Machines [][2]string // work_token, machine_type
	Syns     [][2]string // work_token, syn
}

var (
	workTupleRe = regexp.MustCompile(
		`^\('([a-z0-9_]+)', '([a-z0-9_]+)', '([a-z0-9_]+)', '([^']*)', '([a-z]+)', (?:'([a-z0-9_]+)'|NULL), (\d+)\),?$`)
	pairTupleRe = regexp.MustCompile(`^\('([a-z0-9_]+)', '([^']+)'\),?$`)
)

// insertBody returns the VALUES tuples of the one INSERT whose header line is `header`, cut at the
// `ON DUPLICATE KEY UPDATE` clause. It fails loudly when the header is absent or appears twice —
// a silently-empty body is exactly how this whole file would go false-green.
func insertBody(t *testing.T, content, header string) []string {
	t.Helper()
	idx := strings.Index(content, header)
	if idx < 0 {
		t.Fatalf("%s: не найден INSERT с заголовком %q", operationWorkMigration, header)
	}
	if strings.Contains(content[idx+len(header):], header) {
		t.Fatalf("%s: заголовок %q встречается дважды — тест проверил бы только первый",
			operationWorkMigration, header)
	}
	rest := content[idx+len(header):]
	end := strings.Index(rest, "ON DUPLICATE KEY UPDATE")
	if end < 0 {
		t.Fatalf("%s: у INSERT %q нет клаузы ON DUPLICATE KEY UPDATE — повтор миграции упадёт на 1062",
			operationWorkMigration, header)
	}
	var out []string
	for _, line := range strings.Split(rest[:end], "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s: тело INSERT %q пусто", operationWorkMigration, header)
	}
	return out
}

func parseSeedCatalog(t *testing.T, content string) seedCatalog {
	t.Helper()
	var cat seedCatalog

	for _, line := range insertBody(t, content,
		"INSERT INTO operation_work (token, verb, stage, label, machine_mode, default_machine, sort) VALUES") {
		m := workTupleRe.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("%s: строка сида работ не разбирается: %s", operationWorkMigration, line)
		}
		n, err := strconv.Atoi(m[7])
		if err != nil {
			t.Fatalf("%s: sort не число в строке %s", operationWorkMigration, line)
		}
		cat.Works = append(cat.Works, seedWork{
			Token: m[1], Verb: m[2], Stage: m[3], Label: m[4],
			MachineMode: m[5], DefaultMachine: m[6], Sort: n,
		})
	}

	for _, line := range insertBody(t, content, "INSERT INTO operation_work_machine (work_token, machine_type) VALUES") {
		m := pairTupleRe.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("%s: строка сида машинок не разбирается: %s", operationWorkMigration, line)
		}
		cat.Machines = append(cat.Machines, [2]string{m[1], m[2]})
	}

	for _, line := range insertBody(t, content, "INSERT INTO operation_work_syn (work_token, syn) VALUES") {
		m := pairTupleRe.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("%s: строка сида синонимов не разбирается: %s", operationWorkMigration, line)
		}
		cat.Syns = append(cat.Syns, [2]string{m[1], m[2]})
	}
	return cat
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

func hasLatin(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

// checkSeedCatalog — ВСЕ ПРАВИЛА В ОДНОЙ ЧИСТОЙ ФУНКЦИИ, и это условие мутационного теста: то, что
// гоняют по настоящему файлу, и то, что гоняют по испорченной копии, обязано быть ОДНИМ кодом.
// Возвращает список жалоб; пустой список = сид законен.
func checkSeedCatalog(cat seedCatalog) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	validVerbs := make(map[string]bool, len(entity.OperationTypeTokens))
	for _, v := range entity.OperationTypeTokens {
		if v != "unknown" { // «не назначен» — законное состояние строки, но не работа каталога
			validVerbs[v] = true
		}
	}

	// (а) количество, уникальность и форма токена
	if len(cat.Works) != seededWorkCount {
		add("работ в сиде %d, ожидалось %d", len(cat.Works), seededWorkCount)
	}
	tokens := make(map[string]bool, len(cat.Works))
	sorts := make(map[int]string, len(cat.Works))
	tokenRe := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	prevSort := -1
	for _, w := range cat.Works {
		if tokens[w.Token] {
			add("дубль токена работы: %s", w.Token)
		}
		tokens[w.Token] = true
		if !tokenRe.MatchString(w.Token) || len(w.Token) > 32 {
			add("токен %q не snake_case или длиннее 32 символов", w.Token)
		}
		// (б) глагол из словаря entity — он же ключ правила когерентности 0330
		if !validVerbs[w.Verb] {
			add("работа %s: глагол %q не входит в entity.OperationTypeTokens", w.Token, w.Verb)
		}
		// (в) стадия из закрытого списка восьми
		if !entity.ValidOperationWorkStages[w.Stage] {
			add("работа %s: стадия %q не входит в entity.OperationWorkStageTokens", w.Token, w.Stage)
		}
		if !entity.ValidOperationWorkMachineModes[w.MachineMode] {
			add("работа %s: режим машинки %q не входит в fixed|ask|none", w.Token, w.MachineMode)
		}
		if w.Label == "" || len(w.Label) > 64 {
			add("работа %s: ярлык пуст или длиннее 64 байт", w.Token)
		}
		if other, dup := sorts[w.Sort]; dup {
			add("работа %s: sort %d уже занят работой %s", w.Token, w.Sort, other)
		}
		sorts[w.Sort] = w.Token
		if w.Sort <= prevSort {
			add("работа %s: sort %d не возрастает (предыдущий %d)", w.Token, w.Sort, prevSort)
		}
		prevSort = w.Sort
	}

	// (д) машинки: словарь entity, ссылка на существующую работу, отсутствие дублей
	machinesOf := make(map[string][]string, len(cat.Works))
	seenMachine := make(map[[2]string]bool, len(cat.Machines))
	for _, m := range cat.Machines {
		if !tokens[m[0]] {
			add("строка машинки ссылается на неизвестную работу %s", m[0])
			continue
		}
		if seenMachine[m] {
			add("дубль машинки: %s + %s", m[0], m[1])
		}
		seenMachine[m] = true
		if !entity.ValidMachineTypes[entity.TechCardMachineType(m[1])] {
			add("работа %s: машинка %q не входит в entity.MachineTypeTokens", m[0], m[1])
		}
		machinesOf[m[0]] = append(machinesOf[m[0]], m[1])
	}

	// связка режим ↔ машинки ↔ default_machine: три состояния, каждое со своей арифметикой
	for _, w := range cat.Works {
		ms := machinesOf[w.Token]
		switch w.MachineMode {
		case entity.OperationWorkMachineModeFixed:
			if len(ms) != 1 {
				add("работа %s (fixed): машинок %d, должна быть ровно одна", w.Token, len(ms))
			}
			if w.DefaultMachine == "" {
				add("работа %s (fixed): default_machine пуст", w.Token)
			}
		case entity.OperationWorkMachineModeAsk:
			if len(ms) < 2 {
				add("работа %s (ask): машинок %d — спрашивать не о чем", w.Token, len(ms))
			}
			if w.DefaultMachine == "" {
				add("работа %s (ask): default_machine пуст", w.Token)
			}
		case entity.OperationWorkMachineModeNone:
			if len(ms) != 0 {
				add("работа %s (none): машинок %d, должно быть ноль", w.Token, len(ms))
			}
			if w.DefaultMachine != "" {
				add("работа %s (none): default_machine заполнен (%s)", w.Token, w.DefaultMachine)
			}
		}
		if w.DefaultMachine != "" {
			found := false
			for _, m := range ms {
				if m == w.DefaultMachine {
					found = true
				}
			}
			if !found {
				add("работа %s: default_machine %q не перечислен в operation_work_machine", w.Token, w.DefaultMachine)
			}
		}
	}

	// (г) синонимы: у каждой работы хотя бы одно кириллическое и хотя бы одно латинское слово
	cyr := make(map[string]int, len(cat.Works))
	lat := make(map[string]int, len(cat.Works))
	seenSyn := make(map[[2]string]bool, len(cat.Syns))
	for _, s := range cat.Syns {
		if !tokens[s[0]] {
			add("синоним %q ссылается на неизвестную работу %s", s[1], s[0])
			continue
		}
		if seenSyn[s] {
			add("дубль синонима: %s + %q", s[0], s[1])
		}
		seenSyn[s] = true
		if hasCyrillic(s[1]) {
			cyr[s[0]]++
		}
		if hasLatin(s[1]) {
			lat[s[0]]++
		}
	}
	for _, w := range cat.Works {
		if cyr[w.Token] == 0 {
			add("работа %s: нет ни одного кириллического синонима — технолог не найдёт её своим словом", w.Token)
		}
		if lat[w.Token] == 0 {
			add("работа %s: нет ни одного латинского синонима", w.Token)
		}
	}
	return problems
}

func tokenVerbDigest(cat seedCatalog) string {
	pairs := make([]string, 0, len(cat.Works))
	for _, w := range cat.Works {
		pairs = append(pairs, w.Token+"="+w.Verb)
	}
	sort.Strings(pairs)
	sum := sha256.Sum256([]byte(strings.Join(pairs, "\n")))
	return hex.EncodeToString(sum[:])
}

// TestOperationWorkCatalogSeed — ЦИТАТА: перепись сида в лог плюс восемь правил.
func TestOperationWorkCatalogSeed(t *testing.T) {
	cat := parseSeedCatalog(t, readMigrationFile(t, operationWorkMigration))
	for _, p := range checkSeedCatalog(cat) {
		t.Error(p)
	}

	stages := map[string]int{}
	modes := map[string]int{}
	for _, w := range cat.Works {
		stages[w.Stage]++
		modes[w.MachineMode]++
	}
	byStage := make([]string, 0, len(entity.OperationWorkStageTokens))
	for _, s := range entity.OperationWorkStageTokens {
		byStage = append(byStage, fmt.Sprintf("%s=%d", s, stages[s]))
	}
	t.Logf("(а) работ %d, токены уникальны; (д) машинок %d; (г) синонимов %d",
		len(cat.Works), len(cat.Machines), len(cat.Syns))
	t.Logf("(в) по стадиям: %s", strings.Join(byStage, " "))
	t.Logf("режим машинки: fixed=%d ask=%d none=%d",
		modes[entity.OperationWorkMachineModeFixed],
		modes[entity.OperationWorkMachineModeAsk],
		modes[entity.OperationWorkMachineModeNone])
	t.Logf("(е) sha256(token=verb) = %s", tokenVerbDigest(cat))
}

// TestOperationWorkTokenVerbPairsAreFrozen — (е) неизменяемость связки токен↔глагол.
func TestOperationWorkTokenVerbPairsAreFrozen(t *testing.T) {
	cat := parseSeedCatalog(t, readMigrationFile(t, operationWorkMigration))
	if got := tokenVerbDigest(cat); got != frozenTokenVerbDigest {
		t.Errorf("пары токен→глагол сида 0329 изменились:\n  было %s\n  стало %s\n"+
			"Токен уезжает в дайджест строки шага, глагол — в правило когерентности: правка задним "+
			"числом раздваивает отпечаток подписанной карточки. Если правка осознанна — поменяйте "+
			"frozenTokenVerbDigest и напишите рядом, ПОЧЕМУ.", frozenTokenVerbDigest, got)
	}
}

// TestOperationWorkCatalogGuardsAreNotFalseGreen — МУТАЦИЯ. Каждый пункт ломает разобранный сид
// одним способом и требует жалобы с именем виновника. Без этого теста «зелёный checkSeedCatalog»
// не доказывает ничего: сломанный разбор дал бы ту же зелень.
func TestOperationWorkCatalogGuardsAreNotFalseGreen(t *testing.T) {
	base := parseSeedCatalog(t, readMigrationFile(t, operationWorkMigration))
	if p := checkSeedCatalog(base); len(p) != 0 {
		t.Fatalf("исходный сид обязан быть чистым, иначе мутации ничего не доказывают: %v", p)
	}

	clone := func() seedCatalog {
		c := seedCatalog{
			Works:    append([]seedWork(nil), base.Works...),
			Machines: append([][2]string(nil), base.Machines...),
			Syns:     append([][2]string(nil), base.Syns...),
		}
		return c
	}
	// firstOf возвращает индекс первой работы с заданным режимом — мутации не должны зависеть от
	// того, какая работа стоит в файле первой.
	firstOf := func(c seedCatalog, mode string) int {
		for i, w := range c.Works {
			if w.MachineMode == mode {
				return i
			}
		}
		t.Fatalf("в сиде нет ни одной работы с режимом %s", mode)
		return -1
	}

	cases := []struct {
		name   string
		mutate func(c *seedCatalog) string // возвращает подстроку, которую обязана назвать жалоба
	}{
		{"удалён кириллический синоним", func(c *seedCatalog) string {
			victim := c.Works[0].Token
			kept := c.Syns[:0]
			for _, s := range c.Syns {
				if s[0] == victim && hasCyrillic(s[1]) {
					continue
				}
				kept = append(kept, s)
			}
			c.Syns = kept
			return "кириллического синонима"
		}},
		{"удалён латинский синоним", func(c *seedCatalog) string {
			victim := c.Works[0].Token
			kept := c.Syns[:0]
			for _, s := range c.Syns {
				if s[0] == victim && hasLatin(s[1]) {
					continue
				}
				kept = append(kept, s)
			}
			c.Syns = kept
			return "латинского синонима"
		}},
		{"задвоен токен", func(c *seedCatalog) string {
			dup := c.Works[0]
			dup.Sort = c.Works[len(c.Works)-1].Sort + 10
			c.Works = append(c.Works, dup)
			return "дубль токена работы"
		}},
		{"глагол вне словаря entity", func(c *seedCatalog) string {
			c.Works[0].Verb = "stitching"
			return "не входит в entity.OperationTypeTokens"
		}},
		{"стадия вне закрытого списка", func(c *seedCatalog) string {
			c.Works[0].Stage = "automats"
			return "не входит в entity.OperationWorkStageTokens"
		}},
		{"машинка вне словаря entity", func(c *seedCatalog) string {
			for i, m := range c.Machines {
				if m[0] == c.Works[firstOf(*c, entity.OperationWorkMachineModeFixed)].Token {
					c.Machines[i][1] = "hardware_attach" // снят 0328
					break
				}
			}
			return "не входит в entity.MachineTypeTokens"
		}},
		{"fixed переведён в none, машинка осталась", func(c *seedCatalog) string {
			i := firstOf(*c, entity.OperationWorkMachineModeFixed)
			c.Works[i].MachineMode = entity.OperationWorkMachineModeNone
			return "должно быть ноль"
		}},
		{"default_machine не перечислен", func(c *seedCatalog) string {
			i := firstOf(*c, entity.OperationWorkMachineModeAsk)
			c.Works[i].DefaultMachine = "bartack"
			return "не перечислен в operation_work_machine"
		}},
		{"sort задвоен", func(c *seedCatalog) string {
			c.Works[1].Sort = c.Works[0].Sort
			return "уже занят работой"
		}},
		{"синоним ссылается в пустоту", func(c *seedCatalog) string {
			c.Syns = append(c.Syns, [2]string{"no_such_work", "нет такой"})
			return "ссылается на неизвестную работу"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := clone()
			want := tc.mutate(&c)
			problems := checkSeedCatalog(c)
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

	// Отдельно — заморозка: подмена глагола обязана сдвинуть хеш.
	c := clone()
	c.Works[0].Verb = "handwork"
	if tokenVerbDigest(c) == frozenTokenVerbDigest {
		t.Error("подмена глагола не сдвинула замороженный хеш — заморозка ничего не стережёт")
	}
}
