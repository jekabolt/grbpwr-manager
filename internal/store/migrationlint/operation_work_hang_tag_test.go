package migrationlint

import "testing"

// СТРАЖ МИГРАЦИИ 0458 — НАВЕСИТЬ ЭТИКЕТКУ. Общий union (operation_work_catalog_test.go) прогоняет
// строку через закрытые словари, уникальность token/sort, machine_mode и RU/EN-синонимы; этот файл
// морозит РОВНО свою пару token→verb.
const operationWorkHangTagMigration = "0458_operation_work_hang_tag.sql"

const hangTagWorkCount = 1

const catalogWorkCountAfterHangTag = catalogWorkCountAfterIconCatalog + hangTagWorkCount

// frozenHangTagTokenVerbDigest — sha256 пары «hang_tag=handwork».
//
// ⚠️ МЕНЯТЬ ТОЛЬКО ВМЕСТЕ С ПИСЬМЕННЫМ ОБОСНОВАНИЕМ ЗДЕСЬ ЖЕ: токен уезжает в отпечаток строки шага,
// глагол — в правило когерентности 0330.
const frozenHangTagTokenVerbDigest = "a92cce55c9a8faa1180d3e270b5d1880b71d1abb25713b3818a0918b67395e4f"

func hangTagCatalog(t *testing.T) seedCatalog {
	t.Helper()
	return parseSeedCatalog(t, operationWorkHangTagMigration,
		readMigrationFile(t, operationWorkHangTagMigration))
}

func TestOperationWorkHangTagPairIsFrozen(t *testing.T) {
	mine := hangTagCatalog(t)
	if len(mine.Works) != hangTagWorkCount {
		t.Fatalf("%s минтит %d работ, ожидалось %d", operationWorkHangTagMigration,
			len(mine.Works), hangTagWorkCount)
	}
	if got := tokenVerbDigest(mine); got != frozenHangTagTokenVerbDigest {
		t.Errorf("пара токен→глагол 0458 изменилась:\n  было %s\n  стало %s",
			frozenHangTagTokenVerbDigest, got)
	}
	w := mine.Works[0]
	if w.Token != "hang_tag" || w.Verb != "handwork" {
		t.Errorf("0458 минтит %s=%s, клиент ждёт hang_tag=handwork (пункт J3)", w.Token, w.Verb)
	}
}

func TestOperationWorkHangTagFreezeIsNotFalseGreen(t *testing.T) {
	mine := hangTagCatalog(t)
	before := tokenVerbDigest(mine)
	mine.Works[0].Verb = "other"
	if before == tokenVerbDigest(mine) {
		t.Fatal("мутация token→verb не сдвинула sha256 — freeze-тест вакуумный")
	}
}
