package migrationlint

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// bomLabelPartMigration находит миграцию label_part по суффиксу имени, а не по номеру: номер
// присваивается только перед пушем (перенумерация по origin/beta), и тест не должен ломаться от
// переименования файла. Ровно один файл — второй означал бы дубль колонки.
func bomLabelPartMigration(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(migrationsDir, "*_bom_item_label_part.sql"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one *_bom_item_label_part.sql migration, got %v", matches)
	}
	return readMigrationFile(t, filepath.Base(matches[0]))
}

// TestBomLabelPartDBCheckNoDrift — нога entity<->DB для ЧАСТИ СОСТАВНИКА:
// entity.ValidTechCardBomLabelParts <-> chk_bom_item_label_part. Нога entity<->proto —
// TestBomLabelPartEnumNoDrift в internal/dto. Словарь закрыт, потому что часть — ключ группировки
// колонок этикетки: значение, которое одна сторона принимает, а другая нет, кладёт строку в
// колонку, которую не рисует ни один экран.
func TestBomLabelPartDBCheckNoDrift(t *testing.T) {
	content := bomLabelPartMigration(t)
	dbValues := extractDBEnumValues(t, content, "chk_bom_item_label_part CHECK", 200)
	assertSameSet(t, "TechCardBomLabelPart", dbValues, mapKeysAsStrings(entity.ValidTechCardBomLabelParts))
}

// TestBomLabelPartDBCheckIsCaseClosed — половина CHECK, которую drift-тест не видит: REGEXP под
// коллациями прода и контейнера регистронезависим и принял бы 'SHELL'. Закрывает словарь от
// регистра только STRCMP по BINARY, и он обязан стоять ВНУТРИ именованного CHECK после REGEXP.
func TestBomLabelPartDBCheckIsCaseClosed(t *testing.T) {
	content := bomLabelPartMigration(t)
	const guard = "STRCMP(CAST(label_part AS BINARY), CAST(LOWER(label_part) AS BINARY)) = 0"
	stmt := strings.Index(content, "chk_bom_item_label_part CHECK")
	if stmt < 0 {
		t.Fatal("named vocabulary CHECK chk_bom_item_label_part not found")
	}
	rx := strings.Index(content[stmt:], "label_part REGEXP")
	gd := strings.Index(content[stmt:], guard)
	if rx < 0 || gd < 0 || gd < rx {
		t.Fatalf("the case guard must live inside chk_bom_item_label_part and follow the REGEXP (regexp at %d, guard at %d)", rx, gd)
	}
}
