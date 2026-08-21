package techcard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jmoiron/sqlx"
)

// operationKindColumnCanon — 32 колонки волны 0324 в КАНОНИЧЕСКОМ порядке (F2-FINAL §1: ядро
// S -> PL -> H -> P -> W -> T -> F -> C -> Q -> WP, затем дельта FA -> S14 -> S17).
//
// Этот же порядок обязан стоять в четырёх местах: ALTER'е миграции 0324, named-map
// insertTechCardOperations, techCardOperationsQuery и полях entity.TechCardOperation. Компилятор
// разъезд между ними не видит: map'а ключи не упорядочивает, а SELECT — просто строка. Поэтому
// список продублирован здесь как независимый эталон, и тесты ниже сверяют с ним ВСЕ ЧЕТЫРЕ списка
// БЕЗ БАЗЫ: SELECT и db-теги — из пакета, ALTER — из текста миграции, ключи INSERT-карты — из
// исходника через go/parser. Round-trip на контейнере остаётся, но канон на нём больше не висит:
// он живёт в пакете, который вне CI роняет таблицы, и потому не гоняется.
var operationKindColumnCanon = []string{
	// Строчка (S).
	"needle_count", "needle_gauge_mm", "seam_securing", "row_spacing_mm", "fullness_ratio",
	// Раскладка повторов (PL).
	"placement_count", "pitch_mm",
	// Фурнитура (H).
	"attach_method", "hole_prep", "reinforcement", "foldback_mm", "cycle_stitch_count",
	// Печать (P).
	"print_method", "peel_mode", "second_press_sec", "pressure_scale",
	// Сварка и проклейка (W).
	"air_temperature_c", "feed_speed_m_min",
	// Подрезка и выправка (T), чистка концов ниток (F).
	"trim_action", "residual_allowance_mm", "residual_tail_max_mm",
	// Дискриминаторы финишных глаголов (C, Q, WP).
	"cleaning_kind", "coverage_mode", "wet_process_kind",
	// Петли, закрепки, пуговицы, молнии (FA) и два поля строчки из дельты (S14, S17).
	"buttonhole_style", "cut_length_mm", "buttonhole_orientation", "bartack_length_mm",
	"attach_pattern", "zipper_application", "binding_style", "label_attach_stitch",
}

// pressColumnCanon — две колонки ВТО (0325), дописанные в канон ПОСЛЕ тридцати двух.
//
// Дописыванием, а не вклиниванием в ВТО-блок 0306 «по смыслу»: канон — это ОДИН порядок на четыре
// списка, и вклинивание развело бы порядок entity с порядком ALTER'а, который добавляет колонки в
// конец таблицы. Читаемость купленная разъездом четырёх списков — плохая покупка.
var pressColumnCanon = []string{"press_action", "press_toward"}

// operationColumnCanon — канон целиком: 32 колонки 0324 плюс 2 колонки 0325. Именно он сверяется с
// SELECT'ом, db-тегами entity и ключами INSERT-карты; ALTER'ы проверяются по файлам ПОРОЗНЬ, потому
// что каждая миграция добавляет только свою часть.
var operationColumnCanon = append(append([]string{}, operationKindColumnCanon...), pressColumnCanon...)

// TestOperationKindColumnCanonSize — контрольное число волны (§1): ровно 32, не 31 и не 33.
func TestOperationKindColumnCanonSize(t *testing.T) {
	if len(operationKindColumnCanon) != 32 {
		t.Fatalf("волна 0324 несёт 32 колонки, в эталоне %d", len(operationKindColumnCanon))
	}
	if len(pressColumnCanon) != 2 {
		t.Fatalf("волна 0325 несёт 2 колонки ВТО, в эталоне %d", len(pressColumnCanon))
	}
	if len(operationColumnCanon) != 34 {
		t.Fatalf("канон целиком — 34 колонки, в эталоне %d", len(operationColumnCanon))
	}
	seen := make(map[string]bool, len(operationColumnCanon))
	for _, c := range operationColumnCanon {
		if seen[c] {
			t.Fatalf("колонка %q продублирована в эталоне", c)
		}
		seen[c] = true
	}
}

// TestTechCardOperationsQueryBinds — та же ловушка, что у readiness: ':' в тексте запроса, в том
// числе внутри '--' комментария, sqlx разбирает как именованный параметр и роняет bind в рантайме,
// унося чтение ЛЮБОЙ тех-карты. Базы для воспроизведения не нужно, достаточно sqlx.Named.
func TestTechCardOperationsQueryBinds(t *testing.T) {
	q, args, err := sqlx.Named(techCardOperationsQuery, map[string]any{"ids": []int{1, 2}})
	if err != nil {
		t.Fatalf("operations query does not bind: %v", err)
	}
	if len(args) == 0 {
		t.Fatal("expected bound args")
	}
	if strings.Contains(q, ":") {
		t.Fatalf("после bind в запросе не должно остаться двоеточий: %s", q)
	}
}

// TestTechCardOperationsQueryCarriesKindColumnsInCanonOrder сверяет SELECT с эталоном. Ловится и
// пропущенная колонка (шаг молча читается пустым), и лишняя, и переставленная — перестановка сама по
// себе безвредна для StructScan, но означает, что кто-то из четырёх исполнителей разошёлся с §1, а
// разъезд ищется потом неделю.
func TestTechCardOperationsQueryCarriesKindColumnsInCanonOrder(t *testing.T) {
	selectClause := techCardOperationsQuery
	if i := strings.Index(selectClause, "FROM tech_card_operation"); i >= 0 {
		selectClause = selectClause[:i]
	}
	re := regexp.MustCompile(`o\.([a-z_]+)`)
	inCanon := make(map[string]bool, len(operationColumnCanon))
	for _, c := range operationColumnCanon {
		inCanon[c] = true
	}
	var got []string
	for _, m := range re.FindAllStringSubmatch(selectClause, -1) {
		if inCanon[m[1]] {
			got = append(got, m[1])
		}
	}
	if !reflect.DeepEqual(got, operationColumnCanon) {
		t.Fatalf("SELECT операций разошёлся с каноном §1:\n эталон: %v\n запрос: %v", operationColumnCanon, got)
	}
}

// TestTechCardOperationEntityCarriesKindColumnsInCanonOrder — тот же эталон против db-тегов entity.
// Это второй из четырёх списков; вместе с предыдущим тестом он делает канон проверяемым без базы.
func TestTechCardOperationEntityCarriesKindColumnsInCanonOrder(t *testing.T) {
	inCanon := make(map[string]bool, len(operationColumnCanon))
	for _, c := range operationColumnCanon {
		inCanon[c] = true
	}
	rt := reflect.TypeOf(entity.TechCardOperation{})
	var got []string
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("db")
		if inCanon[tag] {
			got = append(got, tag)
		}
	}
	if !reflect.DeepEqual(got, operationColumnCanon) {
		t.Fatalf("поля entity.TechCardOperation разошлись с каноном §1:\n эталон: %v\n entity: %v", operationColumnCanon, got)
	}
}

// TestTechCardOperationKindColumnsAreNullable — NULL значит «не указано», а не ноль и не «нет».
// Ни одна из 32 не имеет права быть голым int/string/bool: голый тип не отличает «технолог молчит»
// от «технолог сказал ноль», и волна намеренно не несёт ни одного sql.NullBool.
func TestTechCardOperationKindColumnsAreNullable(t *testing.T) {
	inCanon := make(map[string]bool, len(operationColumnCanon))
	for _, c := range operationColumnCanon {
		inCanon[c] = true
	}
	allowed := map[string]bool{
		"sql.NullInt32": true, "sql.NullString": true, "decimal.NullDecimal": true,
	}
	rt := reflect.TypeOf(entity.TechCardOperation{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("db")
		if !inCanon[tag] {
			continue
		}
		name := f.Type.String()
		if !allowed[name] {
			t.Errorf("%s (%s): тип %s не отличает «не указано» от нуля", tag, f.Name, name)
		}
		if name == "sql.NullBool" {
			t.Errorf("%s: волна 0324 не несёт ни одного tri-state поля — все четыре кандидата отложены", tag)
		}
	}
}

// TestOperationKindMigrationAddsColumnsInCanonOrder — ТРЕТЬЯ нога канона: ALTER миграции 0324.
//
// До этого теста порядок ALTER'а сверялся только глазами и round-trip'ом на контейнере, а тот живёт
// в пакете, который вне CI роняет таблицы, — то есть на практике его не гоняет никто. Здесь базы не
// нужно вовсе: имена вынимаются из текста файла.
//
// Сканируется ТОЛЬКО Up-половина: Down снимает те же колонки обратным порядком, и подмешивать его в
// сверку значило бы требовать от отката совпадения с каноном, чего он по устройству не даёт.
func TestOperationKindMigrationAddsColumnsInCanonOrder(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "sql", "0324_operation_kinds.sql"))
	if err != nil {
		t.Fatalf("миграция волны не читается: %v", err)
	}
	up := string(body)
	if i := strings.Index(up, "-- +migrate Down"); i >= 0 {
		up = up[:i]
	}
	// '--'-комментарии под шаблон не попадают: у них строка начинается с дефисов, а не с ADD COLUMN.
	re := regexp.MustCompile(`(?m)^\s*ADD COLUMN\s+([a-z_]+)`)
	var got []string
	for _, m := range re.FindAllStringSubmatch(up, -1) {
		got = append(got, m[1])
	}
	if !reflect.DeepEqual(got, operationKindColumnCanon) {
		t.Fatalf("ALTER миграции 0324 разошёлся с каноном §1:\n эталон: %v\n ALTER:  %v", operationKindColumnCanon, got)
	}
}

// TestPressMigrationAddsColumnsInCanonOrder — та же третья нога для 0325. Файл свой, потому что и
// ALTER свой: 0324 добавляет тридцать две колонки, 0325 — две, и требовать от каждого файла всего
// канона значило бы сверять миграцию со списком, половины которого в ней нет.
func TestPressMigrationAddsColumnsInCanonOrder(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "sql", "0325_press_action_toward.sql"))
	if err != nil {
		t.Fatalf("миграция ВТО не читается: %v", err)
	}
	up := string(body)
	if i := strings.Index(up, "-- +migrate Down"); i >= 0 {
		up = up[:i]
	}
	re := regexp.MustCompile(`(?m)^\s*ADD COLUMN\s+([a-z_]+)`)
	var got []string
	for _, m := range re.FindAllStringSubmatch(up, -1) {
		got = append(got, m[1])
	}
	if !reflect.DeepEqual(got, pressColumnCanon) {
		t.Fatalf("ALTER миграции 0325 разошёлся с каноном:\n эталон: %v\n ALTER:  %v", pressColumnCanon, got)
	}
}

// TestOperationKindInsertMapCarriesKindColumnsInCanonOrder — ЧЕТВЁРТАЯ нога: ключи named-map'ы
// insertTechCardOperations. Ключи map'ы порядка не имеют, поэтому проверить их можно только по
// ИСХОДНИКУ: он и читается — go/parser достаёт литерал ровно из тела нужной функции, так что ни
// комментарий, ни одноимённый ключ из соседней map'ы под сверку не попадут.
func TestOperationKindInsertMapCarriesKindColumnsInCanonOrder(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "production.go", nil, 0)
	if err != nil {
		t.Fatalf("production.go не разбирается: %v", err)
	}
	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "insertTechCardOperations" {
			fn = f
			break
		}
	}
	if fn == nil {
		t.Fatal("insertTechCardOperations не найдена — тест сверяет несуществующий список")
	}
	inCanon := make(map[string]bool, len(operationKindColumnCanon))
	for _, c := range operationKindColumnCanon {
		inCanon[c] = true
	}
	var got []string
	ast.Inspect(fn, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		lit, ok := kv.Key.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		key, err := strconv.Unquote(lit.Value)
		if err == nil && inCanon[key] {
			got = append(got, key)
		}
		return true
	})
	if !reflect.DeepEqual(got, operationKindColumnCanon) {
		t.Fatalf("ключи insertTechCardOperations разошлись с каноном §1:\n эталон: %v\n INSERT: %v", operationKindColumnCanon, got)
	}
}
