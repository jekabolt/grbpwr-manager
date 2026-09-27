package migrationlint

import (
	"regexp"
	"strings"
	"testing"
)

// ГАРДЫ НАД МИГРАЦИЕЙ 0372 — ДВА ИНДЕКСА ПОД ДВЕ ДВЕРИ ИНВАРИАНТА ЛИСТА (27.09, D-57).
//
// ЭТО СТРУКТУРНАЯ СИГНАЛИЗАЦИЯ, А НЕ ДОКАЗАТЕЛЬСТВО ПОВЕДЕНИЯ. Гарды читают текст файла: они краснеют,
// когда из него пропадает несущее свойство, но не применяют Up и Down и не спрашивают план запроса —
// это делает только прогон миграций на контейнере (автомиграция живых проб, CI=1). Несущие свойства:
//
//  1. ИНДЕКС ЛИСТА НА ТРИ КОЛОНКИ ЧТЕНИЯ, КАРТОЧКА ПЕРВОЙ: tech_card_media (tech_card_id, media_id,
//     category). Без category равенство останавливается на (карточка, файл), и чтения запирают строки
//     мудборда с тем же файлом; без карточки первой префикс не служит ни DELETE сейва, ни FK.
//  2. ИНДЕКС КАДРОВ ПОД ЧТЕНИЕ СЕЙВА: design_picture (tech_card_id, media_id, id) — оба равенства
//     чтения заменённых кадров, в этом порядке; иначе скан и замки уходят на все кадры карточки или
//     на все использования файла.
//  3. КАЖДЫЙ ADD — ПОД ГЕЙТОМ information_schema.STATISTICS ПО ИМЕНИ, И НИКАКОГО CHECK: DDL
//     автокоммитится, повтор файла обязан быть no-op, а ADD CONSTRAINT CHECK копирует таблицу.
//  4. DOWN ВОЗВРАЩАЕТ ПОКРЫТИЕ FK НА tech_card_id ДО СНЯТИЯ ИНДЕКСА — на каждой таблице. MySQL вправе
//     снять неявный индекс внешнего ключа, когда появился способный его заменить; обратный порядок
//     падает 1553.

const techCardMediaSheetIndexMigration = "0372_tech_card_media_sheet_index.sql"

// sheetIndexSpec — один индекс файла: таблица, имя, колонки по порядку.
type sheetIndexSpec struct {
	table, name string
	columns     []string
}

var sheetIndexSpecs = []sheetIndexSpec{
	{"tech_card_media", "idx_tech_card_media_sheet", []string{"tech_card_id", "media_id", "category"}},
	{"design_picture", "idx_design_picture_card_media", []string{"tech_card_id", "media_id", "id"}},
}

// sqlOnly — тело без строк-комментариев: слова в шапке файла не должны ни засчитываться, ни мешать.
func sqlOnly(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// sheetIndexSections — Up и Down файла, без комментариев.
func sheetIndexSections(t *testing.T) (up, down string) {
	t.Helper()
	body := readMigrationFile(t, techCardMediaSheetIndexMigration)
	_, rest, ok := strings.Cut(body, "-- +migrate Up")
	if !ok {
		t.Fatalf("%s: нет -- +migrate Up", techCardMediaSheetIndexMigration)
	}
	up, down, ok = strings.Cut(rest, "-- +migrate Down")
	if !ok {
		t.Fatalf("%s: нет -- +migrate Down", techCardMediaSheetIndexMigration)
	}
	return sqlOnly(up), sqlOnly(down)
}

// TestTechCardMediaSheetIndexCoversTheSheetRead — свойства 1, 2 и 3.
func TestTechCardMediaSheetIndexCoversTheSheetRead(t *testing.T) {
	up, _ := sheetIndexSections(t)
	for _, spec := range sheetIndexSpecs {
		add := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+` + spec.table + `\s+ADD\s+INDEX\s+` + spec.name + `\s*\(([^)]*)\)`)
		m := add.FindStringSubmatch(up)
		if m == nil {
			t.Errorf("%s: Up не добавляет %s на %s — гард ниже проверял бы пустоту",
				techCardMediaSheetIndexMigration, spec.name, spec.table)
			continue
		}
		var cols []string
		for _, c := range strings.Split(m[1], ",") {
			cols = append(cols, strings.ToLower(strings.TrimSpace(c)))
		}
		if strings.Join(cols, ",") != strings.Join(spec.columns, ",") {
			t.Errorf("%s: %s на (%s), а чтение, которому он служит, — равенства по (%s) в этом порядке",
				techCardMediaSheetIndexMigration, spec.name, strings.Join(cols, ", "), strings.Join(spec.columns, ", "))
		}
		gate := regexp.MustCompile(`(?is)information_schema\.STATISTICS.*?TABLE_NAME\s*=\s*'` + spec.table +
			`'.*?INDEX_NAME\s*=\s*'` + spec.name + `'`)
		if !gate.MatchString(up) {
			t.Errorf("%s: ADD %s не под гейтом information_schema.STATISTICS по имени — повтор файла после "+
				"падения упал бы на «Duplicate key name» и остановил старт", techCardMediaSheetIndexMigration, spec.name)
		}
	}
	if addCheckRe.MatchString(up) {
		t.Errorf("%s: в Up есть ADD CONSTRAINT … CHECK — копия таблицы и проверка всей истории ради индекса",
			techCardMediaSheetIndexMigration)
	}
}

// TestTechCardMediaSheetIndexDownKeepsTheForeignKeyCovered — свойство 4.
func TestTechCardMediaSheetIndexDownKeepsTheForeignKeyCovered(t *testing.T) {
	_, down := sheetIndexSections(t)
	for _, spec := range sheetIndexSpecs {
		drop := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+` + spec.table + `\s+DROP\s+INDEX\s+` + spec.name + `\b`).FindStringIndex(down)
		if drop == nil {
			t.Errorf("%s: Down не снимает %s", techCardMediaSheetIndexMigration, spec.name)
			continue
		}
		cover := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+` + spec.table + `\s+ADD\s+INDEX\s+\w+\s*\(\s*tech_card_id\s*[,)]`).FindStringIndex(down)
		if cover == nil {
			t.Errorf("%s: Down не возвращает на %s индекс с tech_card_id первым — если MySQL снял неявный индекс "+
				"внешнего ключа при Up, DROP упадёт 1553", techCardMediaSheetIndexMigration, spec.table)
			continue
		}
		if cover[0] > drop[0] {
			t.Errorf("%s: покрытие внешнего ключа на %s возвращается ПОСЛЕ снятия %s — DROP раньше него падает 1553",
				techCardMediaSheetIndexMigration, spec.table, spec.name)
		}
	}
}
