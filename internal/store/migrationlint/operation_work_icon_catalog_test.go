package migrationlint

import (
	"strings"
	"testing"
)

// СТРАЖ МИГРАЦИИ 0436 — РАБОТЫ ЛИСТА ПИКТОГРАММ.
//
// У миграции два рубежа. Общий union в operation_work_catalog_test.go прогоняет новые строки
// вместе с 0329/0331/0332 через закрытые словари, уникальность token/sort, арифметику machine_mode
// и обязательные RU/EN-синонимы. Этот файл морозит РОВНО новые пары token→verb: правка старого
// файла не может спрятаться за общим числом, а правка 0436 не может выдать себя за ярлык.
const operationWorkIconCatalogMigration = "0436_operation_work_icon_catalog.sql"

// iconCatalogWorkCount — сколько работ минтит ИМЕННО 0436. Постоянно навсегда: финальный список
// SPEC-B1 §6 нельзя пополнять вариантами и нельзя сокращать переименованием.
const iconCatalogWorkCount = 54

const catalogWorkCountAfterIconCatalog = catalogWorkCountAfterHemTurned + iconCatalogWorkCount

// frozenIconCatalogTokenVerbDigest — sha256 по отсортированным парам «токен=глагол» 0436.
//
// ⚠️ МЕНЯТЬ ТОЛЬКО ВМЕСТЕ С ПИСЬМЕННЫМ ОБОСНОВАНИЕМ ЗДЕСЬ ЖЕ. Токен уезжает в проекцию
// отпечатка строки шага, глагол входит в правило когерентности 0330. Законный повод ровно один —
// исправление опечатки ДО первого применения файла.
const frozenIconCatalogTokenVerbDigest = "ce812d9898a5763a897e175b06f8b09fdb2325933342bd68a5f72d0e0ea7d199"

func iconCatalog(t *testing.T) seedCatalog {
	t.Helper()
	return parseSeedCatalog(t, operationWorkIconCatalogMigration,
		readMigrationFile(t, operationWorkIconCatalogMigration))
}

// TestOperationWorkIconCatalogPairsAreFrozen — ЦИТАТА: ровно 54 пары финального списка и их
// неизменяемый sha256. Все изменяемые части строки проверяет общий union, а сюда они не входят.
func TestOperationWorkIconCatalogPairsAreFrozen(t *testing.T) {
	mine := iconCatalog(t)
	if len(mine.Works) != iconCatalogWorkCount {
		t.Fatalf("%s минтит %d работ, ожидалось %d", operationWorkIconCatalogMigration,
			len(mine.Works), iconCatalogWorkCount)
	}
	if got := tokenVerbDigest(mine); got != frozenIconCatalogTokenVerbDigest {
		t.Errorf("пары токен→глагол 0436 изменились:\n  было %s\n  стало %s\nТокен уезжает в "+
			"отпечаток строки шага, глагол — в правило когерентности: правка задним числом "+
			"раздваивает подпись. Если правка осознанна — поменяйте константу и напишите, ПОЧЕМУ.",
			frozenIconCatalogTokenVerbDigest, got)
	}
	names := make([]string, 0, len(mine.Works))
	for _, w := range mine.Works {
		names = append(names, w.Token+"="+w.Verb)
	}
	t.Logf("0436 минтит %d замороженных пар:\n  %s", len(names), strings.Join(names, "\n  "))
}

// TestOperationWorkIconCatalogFreezeIsNotFalseGreen — мутация одной пары обязана сдвинуть хеш.
// Это страж над стражем: зелёный тест должен означать, что прочитан SQL, а не пустой срез.
func TestOperationWorkIconCatalogFreezeIsNotFalseGreen(t *testing.T) {
	mine := iconCatalog(t)
	before := tokenVerbDigest(mine)
	mine.Works[0].Verb = "other"
	after := tokenVerbDigest(mine)
	if before == after {
		t.Fatal("мутация token→verb не сдвинула sha256 — freeze-тест вакуумный")
	}
}
