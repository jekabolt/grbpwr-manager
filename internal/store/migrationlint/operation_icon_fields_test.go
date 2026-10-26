package migrationlint

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ГАРДЫ НАД МИГРАЦИЕЙ 0435 — ИКОННЫЕ ПОЛЯ ШАГА (seam_kind, work_variant, button_holes) И ОТВЕРСТИЯ
// ПУГОВИЦЫ-АРТИКУЛА. Несущие свойства файла, которые контейнерный прогон не ловит:
//
//  1. Ровно четыре колонки, каждая NULL, без DEFAULT: «не указано» — законное состояние каждой
//     существующей строки, а DEFAULT стёр бы разницу между «технолог сказал» и «за него сказали».
//  2. Токенные колонки выписывают CHARACTER SET utf8mb4 COLLATE utf8mb4_bin: tech_card_operation на
//     проде в utf8mb3, тесты — в utf8mb4, и унаследованная сортировка маскируется контейнером.
//  3. ALGORITHM=INSTANT явно и ни одного CHECK / FK / индекса: копия tech_card_operation под
//     пятиминутным потолком миграций остановила бы старт прода.
//  4. Каждый ALTER под своим гейтом information_schema, один оператор на строку, Down симметричен.

const operationIconFieldsMigration = "0435_operation_icon_fields.sql"

type iconColumn struct {
	table, column, typ string
	token              bool // VARCHAR-токен: обязан выписать набор и сортировку
}

var operationIconColumns = []iconColumn{
	{"tech_card_operation", "seam_kind", "VARCHAR(32)", true},
	{"tech_card_operation", "work_variant", "VARCHAR(24)", true},
	{"tech_card_operation", "button_holes", "TINYINT UNSIGNED", false},
	{"material_hardware_attr", "button_holes", "TINYINT UNSIGNED", false},
}

var (
	iconAlterRe    = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(\w+)\s+ADD\s+COLUMN\s+(\w+)\s+([^']*?)\s+COMMENT\s+''.*?'',\s*ALGORITHM\s*=\s*(\w+)`)
	iconDropRe     = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(\w+)\s+DROP\s+COLUMN\s+(\w+)`)
	iconForbidRe   = regexp.MustCompile(`(?i)\b(CHECK\s*\(|FOREIGN\s+KEY|ADD\s+(UNIQUE\s+)?(INDEX|KEY)\b|DEFAULT\s|NOT\s+NULL|UPDATE\s+\w+\s+SET)`)
	iconGateColsRe = regexp.MustCompile(`(?i)TABLE_NAME\s*=\s*'(\w+)'\s+AND\s+COLUMN_NAME\s*=\s*'(\w+)'`)
)

// iconFieldsViolations — все нарушения свойств 1–4 в тексте миграции. Отдельной функцией, чтобы
// мутационный тест гонял ровно тот же детектор, что и основной.
func iconFieldsViolations(body string) []string {
	var out []string
	up, down, ok := strings.Cut(body, "-- +migrate Down")
	if !ok {
		return []string{"нет секции Down"}
	}
	upSQL, downSQL := stripSQLComments(up), stripSQLComments(down)

	got := map[[2]string]string{}
	for _, m := range iconAlterRe.FindAllStringSubmatch(upSQL, -1) {
		key := [2]string{m[1], m[2]}
		if _, dup := got[key]; dup {
			out = append(out, "колонка "+m[1]+"."+m[2]+" добавляется дважды")
		}
		got[key] = m[3]
		if !strings.EqualFold(m[4], "INSTANT") {
			out = append(out, m[1]+"."+m[2]+": ALGORITHM="+m[4]+", обязан INSTANT")
		}
	}
	if n := strings.Count(strings.ToUpper(upSQL), "ADD COLUMN"); n != len(operationIconColumns) {
		out = append(out, "ADD COLUMN в Up: "+itoa(n)+", контракт — ровно 4")
	}
	for _, c := range operationIconColumns {
		decl, ok := got[[2]string{c.table, c.column}]
		if !ok {
			out = append(out, "нет ADD COLUMN "+c.table+"."+c.column+" с COMMENT и ALGORITHM")
			continue
		}
		u := strings.ToUpper(decl)
		if !strings.HasPrefix(u, c.typ) {
			out = append(out, c.table+"."+c.column+": тип "+decl+", ожидался "+c.typ)
		}
		if !strings.HasSuffix(strings.TrimSpace(u), " NULL") {
			out = append(out, c.table+"."+c.column+": колонка не объявлена явным NULL")
		}
		if c.token && !strings.Contains(u, "CHARACTER SET UTF8MB4 COLLATE UTF8MB4_BIN") {
			out = append(out, c.table+"."+c.column+": нет CHARACTER SET utf8mb4 COLLATE utf8mb4_bin")
		}
	}
	// Запрещённое ищется в SQL БЕЗ строковых литералов комментариев колонок.
	if hit := iconForbidRe.FindString(stripQuoted(upSQL)); hit != "" {
		out = append(out, "Up заводит запрещённое: "+strings.TrimSpace(hit))
	}

	// Гейты: каждый ALTER (Up и Down) — под своим гейтом на свою колонку; один оператор на строку.
	for _, part := range []struct{ name, text string }{{"Up", upSQL}, {"Down", downSQL}} {
		u := strings.ToUpper(part.text)
		prepares := strings.Count(u, "PREPARE STMT FROM")
		if prepares != len(operationIconColumns) || strings.Count(u, "EXECUTE STMT") != prepares ||
			strings.Count(u, "DEALLOCATE PREPARE") != prepares {
			out = append(out, part.name+": PREPARE/EXECUTE/DEALLOCATE не по одному на каждую из 4 колонок")
		}
		gates := map[[2]string]bool{}
		for _, m := range iconGateColsRe.FindAllStringSubmatch(part.text, -1) {
			gates[[2]string{m[1], m[2]}] = true
		}
		for _, c := range operationIconColumns {
			if !gates[[2]string{c.table, c.column}] {
				out = append(out, part.name+": у "+c.table+"."+c.column+" нет своего гейта information_schema")
			}
		}
		for _, line := range strings.Split(u, "\n") {
			hits := strings.Count(line, "PREPARE STMT FROM") + strings.Count(line, "EXECUTE STMT") +
				strings.Count(line, "DEALLOCATE PREPARE")
			if hits > 1 {
				out = append(out, part.name+": несколько операторов в строке: "+strings.TrimSpace(line))
			}
		}
	}
	dropped := map[[2]string]bool{}
	for _, m := range iconDropRe.FindAllStringSubmatch(downSQL, -1) {
		dropped[[2]string{m[1], m[2]}] = true
	}
	for _, c := range operationIconColumns {
		if !dropped[[2]string{c.table, c.column}] {
			out = append(out, "Down не снимает "+c.table+"."+c.column)
		}
	}
	if len(dropped) != len(operationIconColumns) {
		out = append(out, "Down снимает "+itoa(len(dropped))+" колонок, Up добавляет 4")
	}
	return out
}

func stripSQLComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// stripQuoted убирает тексты COMMENT ”…” внутри динамического DDL: «NULL = not stated» в
// комментарии колонки — не объявление.
var doubledQuoteRe = regexp.MustCompile(`''[^']*''`)

func stripQuoted(s string) string { return doubledQuoteRe.ReplaceAllString(s, "''") }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	if neg {
		d = append([]byte{'-'}, d...)
	}
	return string(d)
}

func TestOperationIconFieldsMigrationShape(t *testing.T) {
	body := readMigrationFile(t, operationIconFieldsMigration)
	for _, v := range iconFieldsViolations(body) {
		t.Errorf("%s: %s", operationIconFieldsMigration, v)
	}
}

// TestOperationIconFieldsGuardsAreNotFalseGreen — МУТАЦИЯ: каждая порча текста обязана покраснить
// детектор; без этого зелень выше доказывала бы только, что регулярки компилируются.
func TestOperationIconFieldsGuardsAreNotFalseGreen(t *testing.T) {
	body := readMigrationFile(t, operationIconFieldsMigration)
	if v := iconFieldsViolations(body); len(v) != 0 {
		t.Fatalf("исходный файл обязан быть чистым: %v", v)
	}
	mutations := map[string]func(string) string{
		"снять COLLATE": func(s string) string {
			return strings.Replace(s, "VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin", "VARCHAR(32)", 1)
		},
		"COPY вместо INSTANT": func(s string) string { return strings.Replace(s, "ALGORITHM=INSTANT',", "ALGORITHM=COPY',", 1) },
		"DEFAULT": func(s string) string {
			return strings.Replace(s, "button_holes TINYINT UNSIGNED NULL", "button_holes TINYINT UNSIGNED NULL DEFAULT 4", 1)
		},
		"NOT NULL": func(s string) string {
			return strings.Replace(s, "work_variant VARCHAR(24) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL",
				"work_variant VARCHAR(24) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL", 1)
		},
		"CHECK": func(s string) string {
			return strings.Replace(s, "ALGORITHM=INSTANT',", "ADD CONSTRAINT ck_holes CHECK (button_holes IN (2,4)),\n        ALGORITHM=INSTANT',", 1)
		},
		"бэкфилл": func(s string) string {
			return strings.Replace(s, "-- +migrate Down", "UPDATE tech_card_operation SET seam_kind = 'felled';\n-- +migrate Down", 1)
		},
		"шире тип": func(s string) string { return strings.Replace(s, "VARCHAR(24)", "VARCHAR(64)", 1) },
		"слить шаги": func(s string) string {
			return strings.Replace(s, "PREPARE stmt FROM @ddl;\nEXECUTE stmt;\nDEALLOCATE PREPARE stmt;", "", 1)
		},
		"склеить операторы": func(s string) string {
			return strings.Replace(s, "PREPARE stmt FROM @ddl;\nEXECUTE stmt;", "PREPARE stmt FROM @ddl; EXECUTE stmt;", 1)
		},
		"Down забыл колонку": func(s string) string {
			return strings.Replace(s, "'ALTER TABLE material_hardware_attr DROP COLUMN button_holes'", "'SELECT 1'", 1)
		},
	}
	for name, mutate := range mutations {
		m := mutate(body)
		if m == body {
			t.Errorf("мутация %q не нашла своё место в файле — детектор проверяет не то", name)
			continue
		}
		if len(iconFieldsViolations(m)) == 0 {
			t.Errorf("мутация %q не видна детектору", name)
		}
	}
}

// --- WorkVariants ↔ каталог работ ---------------------------------------------------------------
//
// Ключ entity.WorkVariants — токен работы. Вариант для работы, которой нет в каталоге, никогда не
// пройдёт запись (parseOperationWork отвергнет саму работу), то есть рисунок варианта в клиенте был
// бы мёртвым. Гард сверяет ключи с работами, которые сеют миграции (Up-секции, все вставки в
// operation_work, любой порядок колонок).
//
// СЕМЬ НОВЫХ РАБОТ СЕЕТ 0436 (другая полоса волны). Пока ни одна из них не посеяна, они терпимы;
// как только в дереве появляется хоть одна — проверка становится строгой для ВСЕХ ключей, и
// забытая работа краснеет здесь.
var workVariantKeysAwaitingSeed = map[string]bool{
	"attach_hook": true, "attach_hook_loop": true, "enclose_magnet": true, "insert_cord": true,
	"pleat": true, "pocket_welt": true, "attach_patch": true,
}

var operationWorkInsertRe = regexp.MustCompile(`(?is)\b(?:INSERT(?:\s+IGNORE)?|REPLACE)\s+INTO\s+` + "`?" + `operation_work` + "`?" + `\s*\(([^)]*)\)\s*VALUES(.*?)(?:ON\s+DUPLICATE\s+KEY\s+UPDATE|;)`)

// seededWorkTokens — токены работ из всех Up-вставок в operation_work.
func seededWorkTokens(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read %s: %v", migrationsDir, err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		up, _, _ := strings.Cut(readMigrationFile(t, e.Name()), "-- +migrate Down")
		for _, m := range operationWorkInsertRe.FindAllStringSubmatch(stripSQLComments(up), -1) {
			tokenAt := -1
			for i, col := range strings.Split(m[1], ",") {
				if strings.Trim(strings.TrimSpace(col), "`") == "token" {
					tokenAt = i
				}
			}
			if tokenAt < 0 {
				t.Fatalf("%s: вставка в operation_work без колонки token: (%s)", e.Name(), m[1])
			}
			for _, tuple := range sqlTuples(m[2]) {
				if tokenAt >= len(tuple) {
					t.Fatalf("%s: кортеж короче списка колонок: %v", e.Name(), tuple)
				}
				out[strings.Trim(tuple[tokenAt], "'")] = true
			}
		}
	}
	return out
}

// sqlTuples режет тело VALUES на кортежи и поля: кавычки (” — экранированная), скобки внутри
// строк не считаются.
func sqlTuples(values string) [][]string {
	var (
		tuples [][]string
		cur    []string
		field  strings.Builder
		depth  int
		quoted bool
	)
	for i := 0; i < len(values); i++ {
		c := values[i]
		if quoted {
			field.WriteByte(c)
			if c == '\'' {
				if i+1 < len(values) && values[i+1] == '\'' {
					field.WriteByte('\'')
					i++
					continue
				}
				quoted = false
			}
			continue
		}
		switch {
		case c == '\'':
			quoted = true
			field.WriteByte(c)
		case c == '(':
			depth++
			if depth > 1 {
				field.WriteByte(c)
			}
		case c == ')':
			depth--
			if depth == 0 {
				cur = append(cur, strings.TrimSpace(field.String()))
				field.Reset()
				tuples = append(tuples, cur)
				cur = nil
			} else {
				field.WriteByte(c)
			}
		case c == ',' && depth == 1:
			cur = append(cur, strings.TrimSpace(field.String()))
			field.Reset()
		case depth >= 1:
			field.WriteByte(c)
		}
	}
	return tuples
}

func workVariantCatalogViolations(seeded map[string]bool) []string {
	strict := false
	for w := range workVariantKeysAwaitingSeed {
		if seeded[w] {
			strict = true
		}
	}
	var out []string
	for work := range entity.WorkVariants {
		if seeded[work] || (!strict && workVariantKeysAwaitingSeed[work]) {
			continue
		}
		out = append(out, work)
	}
	sort.Strings(out)
	return out
}

func TestWorkVariantKeysAreSeededWorks(t *testing.T) {
	seeded := seededWorkTokens(t)
	for _, w := range []string{"set_zip", "fuse", "join_lockstitch"} {
		if !seeded[w] {
			t.Fatalf("разбор сида не нашёл работу %q из 0329 — гард проверял бы пустоту", w)
		}
	}
	if missing := workVariantCatalogViolations(seeded); len(missing) != 0 {
		t.Fatalf("у entity.WorkVariants есть ключи, которых нет в каталоге работ: %v — вариант такой работы не пройдёт запись никогда", missing)
	}
	pending := 0
	for w := range workVariantKeysAwaitingSeed {
		if !seeded[w] {
			pending++
		}
	}
	t.Logf("работ с вариантами, ждущих сида 0436: %d из %d", pending, len(workVariantKeysAwaitingSeed))
}

func TestWorkVariantCatalogGuardIsNotFalseGreen(t *testing.T) {
	seeded := seededWorkTokens(t)
	drop := func(m map[string]bool, k string) map[string]bool {
		c := map[string]bool{}
		for kk, v := range m {
			if kk != k {
				c[kk] = v
			}
		}
		return c
	}
	if v := workVariantCatalogViolations(drop(seeded, "set_zip")); len(v) != 1 || v[0] != "set_zip" {
		t.Errorf("пропавшая работа из 0329 не видна гарду: %v", v)
	}
	// Один посеянный токен 0436 включает строгий режим: остальные шесть обязаны покраснеть.
	partial := drop(seeded, "")
	partial["pleat"] = true
	for w := range workVariantKeysAwaitingSeed {
		if w != "pleat" {
			delete(partial, w)
		}
	}
	if v := workVariantCatalogViolations(partial); len(v) != len(workVariantKeysAwaitingSeed)-1 {
		t.Errorf("частичный сид 0436 не включил строгий режим: %v", v)
	}
	tuples := sqlTuples("\n  ('a', 'b, (c)', 'it''s', NULL, 1),\n  ('d', 'e', '', NULL, 2)\n")
	if len(tuples) != 2 || len(tuples[0]) != 5 || tuples[0][1] != "'b, (c)'" || tuples[0][2] != "'it''s'" {
		t.Errorf("разбор кортежей сломан: %#v", tuples)
	}
}
