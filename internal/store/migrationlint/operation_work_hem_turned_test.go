package migrationlint

import (
	"sort"
	"strings"
	"testing"
)

// СТРАЖ МИГРАЦИИ 0332 — ПОДГИБ ВДВОЕ.
//
// ЗАЧЕМ ОТДЕЛЬНЫЙ ФАЙЛ, А НЕ СТРОЧКА В ЧУЖОМ. Так устроен замысел стражей каталога, и он записан
// прямо в 0331: «следующая миграция добавит свои работы своим файлом и заморозит их своим тестом».
// Каждый файл морозит РОВНО СВОИ пары «токен→глагол», поэтому правка чужой миграции не может
// спрятаться за общим числом.
//
// ЭТОТ ТЕСТ ЗАВЕДЁН ПОТОМУ, ЧТО БЕЗ НЕГО ГЕЙТ БЫЛ ВАКУУМНЫМ, И ЭТО ЗАМЕРЕНО, А НЕ ПРЕДПОЛОЖЕНО.
// Сразу после написания 0332 весь пакет был зелёным. Мутация файла — глагол `machinez`, ступень
// `edges_hemz`, машинка `lockstitchz` — оставила его ЗЕЛЁНЫМ: стражи 0329 и 0331 привязаны к своим
// именам файлов и новый файл не читали вовсе. То есть миграция с несуществующим глаголом доехала
// бы до прода, где её поймал бы не тест, а старт приложения.
const operationWorkHemTurnedMigration = "0332_operation_work_hem_turned.sql"

// hemTurnedWorkCount — сколько работ минтит ИМЕННО ЭТОТ файл. Постоянно навсегда.
const hemTurnedWorkCount = 1

// catalogWorkCountAfterHemTurned — сколько работ в каталоге ПОСЛЕ 0332. Считается ОТ предыдущего
// рубежа, а не заново: столкновение токенов и sort'ов обязано проверяться поперёк ВСЕХ файлов.
const catalogWorkCountAfterHemTurned = catalogWorkCountAfterBackfill + hemTurnedWorkCount

// frozenHemTurnedTokenVerbDigest — sha256 по паре «токен=глагол» единственной работы 0332.
//
// ⚠️ МЕНЯТЬ ТОЛЬКО ВМЕСТЕ С ПИСЬМЕННЫМ ОБОСНОВАНИЕМ ЗДЕСЬ ЖЕ. Токен уезжает в проекцию отпечатка
// строки шага, глагол входит в правило когерентности 0330: правка любого из них задним числом
// раздваивает подпись уже подписанной карточки. Законный повод ровно один — исправление опечатки
// ДО первого применения файла.
const frozenHemTurnedTokenVerbDigest = "6dedfb895abdcf3dbb36e7d6723a440e5cb6cc8ae1f18d7e565f49094e2618da"

// sortWorksBySort — тот же порядок, что у unionCatalog: правило монотонности sort'а внутри
// checkSeedCatalog читает срез, а в файлах работы стоят межстрочными номерами.
func sortWorksBySort(w []seedWork) {
	sort.SliceStable(w, func(i, j int) bool { return w[i].Sort < w[j].Sort })
}

func hemTurnedCatalog(t *testing.T) seedCatalog {
	t.Helper()
	return parseSeedCatalog(t, operationWorkHemTurnedMigration,
		readMigrationFile(t, operationWorkHemTurnedMigration))
}

// unionCatalogWithHemTurned — 0329 + 0331 + 0332 одним срезом, отсортированным по sort. Union нужен
// не для красоты: уникальность токена и sort'а проверяется только поперёк файлов.
func unionCatalogWithHemTurned(t *testing.T) seedCatalog {
	t.Helper()
	prev := unionCatalog(t)
	mine := hemTurnedCatalog(t)
	union := seedCatalog{
		Works:    append(append([]seedWork(nil), prev.Works...), mine.Works...),
		Machines: append(append([][2]string(nil), prev.Machines...), mine.Machines...),
		Syns:     append(append([][2]string(nil), prev.Syns...), mine.Syns...),
	}
	sortWorksBySort(union.Works)
	return union
}

// TestOperationWorkHemTurnedJoinsTheCatalog — ЦИТАТА: работа разобрана, легальна по всем словарям
// и не сталкивается ни с одной прежней.
func TestOperationWorkHemTurnedJoinsTheCatalog(t *testing.T) {
	mine := hemTurnedCatalog(t)
	if len(mine.Works) != hemTurnedWorkCount {
		t.Fatalf("%s минтит %d работ, ожидалось %d", operationWorkHemTurnedMigration,
			len(mine.Works), hemTurnedWorkCount)
	}
	for _, p := range checkSeedCatalog(unionCatalogWithHemTurned(t), catalogWorkCountAfterHemTurned) {
		t.Error(p)
	}
	if got := tokenVerbDigest(mine); got != frozenHemTurnedTokenVerbDigest {
		t.Errorf("пары токен→глагол 0332 изменились:\n  было %s\n  стало %s\nТокен уезжает в "+
			"отпечаток строки шага, глагол — в правило когерентности: правка задним числом "+
			"раздваивает подпись. Если правка осознанна — поменяйте константу и напишите, ПОЧЕМУ.",
			frozenHemTurnedTokenVerbDigest, got)
	}
	w := mine.Works[0]
	t.Logf("минтится: %s (%s/%s, %s %s, sort %d) %q; каталог после 0332 — %d работ",
		w.Token, w.Verb, w.Stage, w.MachineMode, w.DefaultMachine, w.Sort, w.Label,
		catalogWorkCountAfterHemTurned)
}

// TestOperationWorkHemTurnedIsSearchableInBothScripts — технолог печатает «подгиб», а не «hem».
// Требование то же, что у 0329/0331: у работы обязано быть и кириллическое, и латинское слово.
func TestOperationWorkHemTurnedIsSearchableInBothScripts(t *testing.T) {
	mine := hemTurnedCatalog(t)
	var cyr, lat int
	for _, s := range mine.Syns {
		if hasCyrillic(s[1]) {
			cyr++
		}
		if hasLatin(s[1]) {
			lat++
		}
	}
	if cyr == 0 || lat == 0 {
		t.Errorf("у работ 0332 %d кириллических и %d латинских синонимов — пикер ищется словом "+
			"технолога, и обеих раскладок обязано хватать", cyr, lat)
	}
	t.Logf("синонимов %d: кириллицей %d, латиницей %d", len(mine.Syns), cyr, lat)
}

// TestOperationWorkHemTurnedGuardsAreNotFalseGreen — МУТАЦИЯ. Проверяет, что предыдущие два теста
// вообще способны покраснеть: без неё «зелено» значит лишь «файл не читали».
func TestOperationWorkHemTurnedGuardsAreNotFalseGreen(t *testing.T) {
	base := hemTurnedCatalog(t)
	if p := checkSeedCatalog(unionCatalogWithHemTurned(t), catalogWorkCountAfterHemTurned); len(p) != 0 {
		t.Fatalf("исходный каталог обязан быть чистым, иначе мутации ничего не доказывают: %v", p)
	}
	clone := func() seedCatalog {
		return seedCatalog{
			Works:    append([]seedWork(nil), base.Works...),
			Machines: append([][2]string(nil), base.Machines...),
			Syns:     append([][2]string(nil), base.Syns...),
		}
	}
	union := func(c seedCatalog) seedCatalog {
		prev := unionCatalog(t)
		u := seedCatalog{
			Works:    append(append([]seedWork(nil), prev.Works...), c.Works...),
			Machines: append(append([][2]string(nil), prev.Machines...), c.Machines...),
			Syns:     append(append([][2]string(nil), prev.Syns...), c.Syns...),
		}
		sortWorksBySort(u.Works)
		return u
	}

	cases := []struct {
		what   string
		mutate func(c *seedCatalog)
	}{
		{"глагол, которого нет в словаре", func(c *seedCatalog) { c.Works[0].Verb = "machinez" }},
		{"ступень, которой нет в словаре", func(c *seedCatalog) { c.Works[0].Stage = "edges_hemz" }},
		{"машинка, которой нет в словаре", func(c *seedCatalog) {
			c.Works[0].DefaultMachine = "lockstitchz"
			c.Machines = [][2]string{{"hem_turned", "lockstitchz"}}
		}},
		{"режим «на чём», которого нет", func(c *seedCatalog) { c.Works[0].MachineMode = "maybe" }},
		{"токен, уже занятый прежней миграцией", func(c *seedCatalog) {
			c.Works[0].Token = "moscow_hem"
			c.Machines = [][2]string{{"moscow_hem", "lockstitch"}}
			c.Syns = [][2]string{{"moscow_hem", "подгиб"}, {"moscow_hem", "hem"}}
		}},
		{"sort, уже занятый прежней миграцией", func(c *seedCatalog) { c.Works[0].Sort = 75 }},
	}
	for _, tc := range cases {
		c := clone()
		tc.mutate(&c)
		if p := checkSeedCatalog(union(c), catalogWorkCountAfterHemTurned); len(p) == 0 {
			t.Errorf("МУТАЦИЯ НЕ ПОЙМАНА (%s) — страж вакуумный", tc.what)
		} else {
			t.Logf("мутация «%s» поймана: %s", tc.what, strings.Join(p, "; "))
		}
	}
}
