package migrationlint

import (
	"regexp"
	"strings"
	"testing"
)

// ГАРДЫ НАД МИГРАЦИЕЙ 0372 — ИНДЕКС ПОД ЧТЕНИЕ ТЕХНИЧЕСКОГО ЛИСТА (27.09, D-57).
//
// Перезапись спрашивает в SERIALIZABLE-транзакции tech_card_media по карточке, файлу и слову листа
// (отказ technical_sheet), и замки этого чтения — ровно то, чем она сериализуется с сейвом карточки.
// Несущие свойства файла:
//
//  1. ИНДЕКС НА ТРИ КОЛОНКИ ЧТЕНИЯ, КАРТОЧКА ПЕРВОЙ. Без category равенство останавливается на
//     (карточка, файл), и чтение запирает строки мудборда с тем же файлом; без карточки первой
//     префикс не служит ни DELETE сейва, ни внешнему ключу на tech_card.
//  2. ADD ПОД ГЕЙТОМ information_schema.STATISTICS, И НИКАКОГО CHECK: DDL автокоммитится, повтор
//     файла обязан быть no-op, а ADD CONSTRAINT CHECK копирует таблицу и проверяет всю историю.
//  3. DOWN ВОЗВРАЩАЕТ ПОКРЫТИЕ FK НА tech_card_id ДО СНЯТИЯ ИНДЕКСА. MySQL вправе снять неявный
//     индекс внешнего ключа, когда появился этот; обратный порядок падает 1553.

const techCardMediaSheetIndexMigration = "0372_tech_card_media_sheet_index.sql"

var (
	// sheetIndexAddRe — ADD INDEX этого индекса со списком колонок.
	sheetIndexAddRe = regexp.MustCompile(`(?i)ADD\s+INDEX\s+idx_tech_card_media_sheet\s*\(([^)]*)\)`)
	// sheetIndexDropRe — его снятие.
	sheetIndexDropRe = regexp.MustCompile(`(?i)DROP\s+INDEX\s+idx_tech_card_media_sheet\b`)
	// cardIndexAddRe — возврат покрытия внешнего ключа на tech_card_id.
	cardIndexAddRe = regexp.MustCompile(`(?i)ADD\s+INDEX\s+\w+\s*\(\s*tech_card_id\s*\)`)
	// sheetIndexGateRe — гейт по имени индекса в information_schema.STATISTICS.
	sheetIndexGateRe = regexp.MustCompile(`(?is)information_schema\.STATISTICS.*?INDEX_NAME\s*=\s*'idx_tech_card_media_sheet'`)
)

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

// TestTechCardMediaSheetIndexCoversTheSheetRead — свойства 1 и 2.
func TestTechCardMediaSheetIndexCoversTheSheetRead(t *testing.T) {
	up, _ := sheetIndexSections(t)
	m := sheetIndexAddRe.FindStringSubmatch(up)
	if m == nil {
		t.Fatalf("%s: Up не добавляет idx_tech_card_media_sheet — миграцию переписали, и гард ниже проверял бы пустоту",
			techCardMediaSheetIndexMigration)
	}
	var cols []string
	for _, c := range strings.Split(m[1], ",") {
		cols = append(cols, strings.ToLower(strings.TrimSpace(c)))
	}
	if want := []string{"tech_card_id", "media_id", "category"}; strings.Join(cols, ",") != strings.Join(want, ",") {
		t.Errorf("%s: индекс на (%s), а чтение листа — равенство по (%s) в этом порядке: без category замки "+
			"ложатся и на мудборд того же файла, без карточки первой префикс не служит DELETE сейва и FK",
			techCardMediaSheetIndexMigration, strings.Join(cols, ", "), strings.Join(want, ", "))
	}
	if !sheetIndexGateRe.MatchString(up) {
		t.Errorf("%s: ADD не под гейтом information_schema.STATISTICS по имени индекса — повтор файла после "+
			"падения упал бы на «Duplicate key name» и остановил старт", techCardMediaSheetIndexMigration)
	}
	if addCheckRe.MatchString(up) {
		t.Errorf("%s: в Up есть ADD CONSTRAINT … CHECK — копия таблицы и проверка всей истории ради индекса",
			techCardMediaSheetIndexMigration)
	}
}

// TestTechCardMediaSheetIndexDownKeepsTheForeignKeyCovered — свойство 3.
func TestTechCardMediaSheetIndexDownKeepsTheForeignKeyCovered(t *testing.T) {
	_, down := sheetIndexSections(t)
	drop := sheetIndexDropRe.FindStringIndex(down)
	if drop == nil {
		t.Fatalf("%s: Down не снимает idx_tech_card_media_sheet", techCardMediaSheetIndexMigration)
	}
	cover := cardIndexAddRe.FindStringIndex(down)
	if cover == nil {
		t.Fatalf("%s: Down не возвращает индекс на tech_card_id — если MySQL снял неявный индекс внешнего "+
			"ключа при Up, DROP упадёт 1553", techCardMediaSheetIndexMigration)
	}
	if cover[0] > drop[0] {
		t.Errorf("%s: покрытие внешнего ключа возвращается ПОСЛЕ снятия индекса — DROP раньше него падает 1553",
			techCardMediaSheetIndexMigration)
	}
}
