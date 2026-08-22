package migrationlint

import (
	"regexp"
	"strings"
	"testing"
)

// ГАРДЫ НАД МИГРАЦИЕЙ 0330 — ОСЬ «РАБОТА» НА СТРОКЕ ШАГА.
//
// ЗАЧЕМ ОНИ, ЕСЛИ ФАЙЛ ПРОЧИТАН ГЛАЗАМИ. Три его свойства нельзя проверить ничем, кроме прогона на
// проде, а прогон на проде — это и есть тот момент, когда ошибка стоит остановленного старта:
//
//  1. НАБОР СИМВОЛОВ И СОРТИРОВКА КОЛОНКИ ВЫПИСАНЫ ЯВНО. `operation_work.token` объявлен
//     VARCHAR(32) COLLATE utf8mb4_bin; MySQL требует от сторон внешнего ключа СОВПАДЕНИЯ набора и
//     сортировки, а `tech_card_operation` на проде живёт в utf8mb3 (тесты идут в utf8mb4 — то есть
//     контейнерный прогон эту разницу МАСКИРУЕТ). Колонка, унаследовавшая сортировку таблицы, дала
//     бы на проде ошибку 3780 ровно на шаге 2.
//  2. СЛОВАРЬ ЗАКРЫТ ВНЕШНИМ КЛЮЧОМ, А НЕ CHECK'ОМ. `ADD CONSTRAINT CHECK` в MySQL 8 копирует
//     таблицу ЦЕЛИКОМ, а потолок на ВЕСЬ прогон миграций зашит пятью минутами в коде.
//  3. КАЖДЫЙ DDL ПОД СВОИМ ГЕЙТОМ information_schema. MySQL 8 автокоммитит DDL: падение в середине
//     файла оставляет схему полуприменённой БЕЗ строки в gorp_migrations, и следующий старт
//     перезапускает файл С ВЕРХА.
//
// Гард — не пересказ файла, а его несущие свойства: перепиши файл иначе, но сохрани их, и тесты
// останутся зелёными.

const operationWorkAxisMigration = "0330_operation_work_axis.sql"

var (
	// workColumnRe — объявление колонки work в ADD COLUMN, вместе с типом и всем, что за ним до
	// COMMENT или конца строки.
	workColumnRe = regexp.MustCompile(`(?i)ADD\s+COLUMN\s+work\s+([^\n]*)`)
	// workFKRe — внешний ключ с ЯВНЫМ именем на operation_work(token).
	workFKRe = regexp.MustCompile(`(?i)ADD\s+CONSTRAINT\s+(\w+)\s+FOREIGN\s+KEY\s*\(\s*work\s*\)\s*REFERENCES\s+operation_work\s*\(\s*token\s*\)`)
	// addCheckRe — любой ADD CONSTRAINT ... CHECK: алгоритм COPY, запрещён на этой таблице.
	addCheckRe = regexp.MustCompile(`(?i)ADD\s+CONSTRAINT\s+\w+\s+CHECK`)
	// infoSchemaGateRe — обращение к information_schema, то есть гейт шага.
	infoSchemaGateRe = regexp.MustCompile(`(?i)information_schema\.`)
)

// TestOperationWorkAxisColumnDeclaresItsCollation — свойство 1.
func TestOperationWorkAxisColumnDeclaresItsCollation(t *testing.T) {
	body := readMigrationFile(t, operationWorkAxisMigration)
	m := workColumnRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("%s: не найден ADD COLUMN work — миграцию переписали, и гард ниже проверял бы пустоту",
			operationWorkAxisMigration)
	}
	decl := m[1]
	for _, want := range []string{"VARCHAR(32)", "CHARACTER SET utf8mb4", "COLLATE utf8mb4_bin"} {
		if !strings.Contains(strings.ToUpper(decl), strings.ToUpper(want)) {
			t.Errorf("%s: в объявлении колонки work нет %q (объявлено: %q). Внешний ключ требует "+
				"совпадения набора символов и сортировки со стороной operation_work.token, а "+
				"tech_card_operation на проде в utf8mb3 — унаследованная сортировка даёт 3780 и "+
				"останавливает старт прода. Контейнерный тест в utf8mb4 этого НЕ ловит",
				operationWorkAxisMigration, want, strings.TrimSpace(decl))
		}
	}
	if strings.Contains(strings.ToUpper(decl), "NOT NULL") {
		t.Errorf("%s: колонка work объявлена NOT NULL — «работа не назначена» законное состояние "+
			"каждой существующей строки, и NOT NULL сделал бы миграцию неприменимой", operationWorkAxisMigration)
	}
	if strings.Contains(strings.ToUpper(decl), "DEFAULT ") {
		t.Errorf("%s: у колонки work есть DEFAULT — он стёр бы разницу между «технолог назвал работу» "+
			"и «за него назвали»", operationWorkAxisMigration)
	}
}

// TestOperationWorkAxisClosesTheVocabularyWithAForeignKey — свойство 2.
func TestOperationWorkAxisClosesTheVocabularyWithAForeignKey(t *testing.T) {
	body := readMigrationFile(t, operationWorkAxisMigration)
	fk := workFKRe.FindStringSubmatch(body)
	if fk == nil {
		t.Fatalf("%s: нет внешнего ключа work -> operation_work(token) с явным именем — словарь работ "+
			"остался бы незакрытым, а любая попытка закрыть его CHECK'ом копирует таблицу целиком",
			operationWorkAxisMigration)
	}
	if strings.HasSuffix(fk[1], "_chk_1") || strings.Contains(fk[1], "_ibfk_") {
		t.Errorf("%s: имя ограничения %q выглядит автогенерируемым — такие имена позиционны и "+
			"дрейфуют, а гейт идемпотентности стоит именно на имени", operationWorkAxisMigration, fk[1])
	}
	if hit := addCheckRe.FindString(body); hit != "" {
		t.Errorf("%s: миграция заводит %q. ADD CONSTRAINT CHECK в MySQL 8 идёт алгоритмом COPY — "+
			"копией всей tech_card_operation, — а потолок на весь прогон миграций пять минут",
			operationWorkAxisMigration, strings.TrimSpace(hit))
	}
}

// TestOperationWorkAxisIsIdempotentStepByStep — свойство 3.
//
// СЧИТАЮТСЯ ТРИ ЧИСЛА, И ОНИ ОБЯЗАНЫ СОВПАСТЬ: сколько подготовленных операторов, столько же
// исполнений, столько же освобождений и столько же гейтов information_schema. Слитый ALTER под
// одним гейтом на два изменения — ровно та ошибка, которую это ловит: повтор после падения между
// шагами (колонка есть, ключа нет) прошёл бы мимо ключа НАВСЕГДА.
func TestOperationWorkAxisIsIdempotentStepByStep(t *testing.T) {
	body := readMigrationFile(t, operationWorkAxisMigration)
	up, down, ok := strings.Cut(body, "-- +migrate Down")
	if !ok {
		t.Fatalf("%s: нет секции Down", operationWorkAxisMigration)
	}
	for _, part := range []struct {
		name string
		text string
	}{{"Up", up}, {"Down", down}} {
		prepares := strings.Count(strings.ToUpper(part.text), "PREPARE STMT FROM")
		executes := strings.Count(strings.ToUpper(part.text), "EXECUTE STMT")
		deallocs := strings.Count(strings.ToUpper(part.text), "DEALLOCATE PREPARE")
		gates := len(infoSchemaGateRe.FindAllString(part.text, -1))
		if prepares == 0 {
			t.Errorf("%s (%s): ни одного PREPARE — DDL идёт голым, и повтор файла с верха упадёт",
				operationWorkAxisMigration, part.name)
			continue
		}
		if executes != prepares || deallocs != prepares {
			t.Errorf("%s (%s): PREPARE %d, EXECUTE %d, DEALLOCATE %d — числа обязаны совпасть, иначе "+
				"подготовленный оператор остаётся висеть", operationWorkAxisMigration, part.name,
				prepares, executes, deallocs)
		}
		if gates < prepares {
			t.Errorf("%s (%s): гейтов information_schema %d на %d подготовленных операторов — "+
				"шаг без собственного гейта не идемпотентен: повтор после падения между шагами "+
				"пройдёт мимо него навсегда", operationWorkAxisMigration, part.name, gates, prepares)
		}
		// ПО ОДНОМУ ОПЕРАТОРУ НА СТРОКУ. multiStatements=true контейнерных тестов проглатывает
		// склейку, а прод на ней падает — и падает при старте.
		for _, line := range strings.Split(part.text, "\n") {
			u := strings.ToUpper(line)
			hits := 0
			for _, kw := range []string{"PREPARE STMT FROM", "EXECUTE STMT", "DEALLOCATE PREPARE"} {
				hits += strings.Count(u, kw)
			}
			if hits > 1 {
				t.Errorf("%s (%s): в одной строке несколько операторов — %q", operationWorkAxisMigration,
					part.name, strings.TrimSpace(line))
			}
		}
	}
}

// TestOperationWorkAxisGuardsAreNotFalseGreen — МУТАЦИЯ. Каждый пункт ломает ТЕКСТ миграции одним
// способом и требует, чтобы соответствующий детектор это увидел. Без него зелень трёх тестов выше
// доказывала бы только то, что регулярки компилируются.
func TestOperationWorkAxisGuardsAreNotFalseGreen(t *testing.T) {
	body := readMigrationFile(t, operationWorkAxisMigration)

	// 1. Сортировка снята с объявления колонки.
	stripped := strings.Replace(body, "CHARACTER SET utf8mb4 COLLATE utf8mb4_bin", "", 1)
	if stripped == body {
		t.Fatal("в миграции не нашлось объявления набора/сортировки — детектор проверяет не то")
	}
	if decl := workColumnRe.FindStringSubmatch(stripped); decl == nil ||
		strings.Contains(strings.ToUpper(decl[1]), "COLLATE UTF8MB4_BIN") {
		t.Error("мутация «снять COLLATE» не видна детектору колонки")
	}

	// 2. Внешний ключ заменён на CHECK.
	noFK := workFKRe.ReplaceAllString(body, "ADD CONSTRAINT ck_op_work CHECK (work IS NOT NULL)")
	if workFKRe.MatchString(noFK) {
		t.Error("мутация «убрать внешний ключ» не видна детектору ключа")
	}
	if !addCheckRe.MatchString(noFK) {
		t.Error("мутация «завести CHECK» не видна детектору CHECK'а")
	}

	// 3. Два шага слиты в один гейт: убран второй блок PREPARE/EXECUTE/DEALLOCATE.
	fused := strings.Replace(body, "PREPARE stmt FROM @ddl;\nEXECUTE stmt;\nDEALLOCATE PREPARE stmt;", "", 1)
	if strings.Count(strings.ToUpper(fused), "PREPARE STMT FROM") >=
		strings.Count(strings.ToUpper(body), "PREPARE STMT FROM") {
		t.Error("мутация «слить шаги» не изменила числа — счётчик считает не то")
	}

	// 4. Три оператора склеены в одну строку.
	oneLine := strings.Replace(body, "PREPARE stmt FROM @ddl;\nEXECUTE stmt;\nDEALLOCATE PREPARE stmt;",
		"PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;", 1)
	worst := 0
	for _, line := range strings.Split(oneLine, "\n") {
		u := strings.ToUpper(line)
		hits := strings.Count(u, "PREPARE STMT FROM") + strings.Count(u, "EXECUTE STMT") +
			strings.Count(u, "DEALLOCATE PREPARE")
		if hits > worst {
			worst = hits
		}
	}
	if worst < 2 {
		t.Error("мутация «склеить операторы в строку» не поймалась построчным счётчиком")
	}
}
