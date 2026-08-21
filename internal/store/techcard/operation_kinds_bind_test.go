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
	// ⚠️ pressure_scale выше — ИСТОРИЯ, а не живая колонка. 0324 её ДОБАВИЛА (и третья нога канона,
	// TestOperationKindMigrationAddsColumnsInCanonOrder, читает именно её ALTER), а 0327 СНЯЛА
	// вместе со словарём. Живой набор строится ниже вычитанием operationKindColumnRetired.
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

// operationKindColumnRetired — колонки волны, СНЯТЫЕ последующей миграцией, и файл, который их снял.
//
// Отдельное множество, а не вычёркивание из эталона выше, потому что у канона ДВЕ разные работы, и
// после первого же снятия они расходятся. ALTER миграции 0324 заморожен: он добавил тридцать две
// колонки, и переписать его нельзя — он применён на бете и приедет на прод как есть. А SELECT,
// db-теги entity и ключи INSERT-карты описывают СЕГОДНЯШНЮЮ таблицу, где колонки уже нет. Свести
// их в один список значило бы либо соврать про 0324, либо потребовать от кода читать снятую
// колонку.
var operationKindColumnRetired = map[string]string{
	// F3 «ложных расщеплений»: это был прижим ВТО-блока (press_pressure_n_cm2), сказанный словом
	// вместо числа, на шаге, где ВТО-блок законен.
	"pressure_scale": "0327_operation_kinds_false_splits.sql",
}

// operationKindColumnLiveCanon / operationColumnLiveCanon — то же, что каноны выше, МИНУС снятое.
// Именно с ними сверяются три ноги, смотрящие на живой код; ALTER'ы остаются на исторических.
var operationKindColumnLiveCanon = liveCanon(operationKindColumnCanon)

func liveCanon(all []string) []string {
	out := make([]string, 0, len(all))
	for _, c := range all {
		if _, gone := operationKindColumnRetired[c]; gone {
			continue
		}
		out = append(out, c)
	}
	return out
}

// operationColumnCanon — канон целиком: 32 колонки 0324 плюс 2 колонки 0325. Именно он сверяется с
// SELECT'ом, db-тегами entity и ключами INSERT-карты; ALTER'ы проверяются по файлам ПОРОЗНЬ, потому
// что каждая миграция добавляет только свою часть.
var operationColumnCanon = append(append([]string{}, operationKindColumnCanon...), pressColumnCanon...)

var operationColumnLiveCanon = liveCanon(operationColumnCanon)

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
	// И контрольное число ЖИВОГО набора: 34 минус снятое. Без него запись в operationKindColumnRetired
	// с опечаткой в имени просто ничего бы не вычла, и три ноги продолжили бы требовать от кода
	// колонку, которой в таблице нет.
	if len(operationColumnLiveCanon) != len(operationColumnCanon)-len(operationKindColumnRetired) {
		t.Fatalf("живой канон — %d колонок при %d в истории и %d снятых", len(operationColumnLiveCanon), len(operationColumnCanon), len(operationKindColumnRetired))
	}
	inHistory := make(map[string]bool, len(operationColumnCanon))
	for _, c := range operationColumnCanon {
		inHistory[c] = true
	}
	for c, by := range operationKindColumnRetired {
		if !inHistory[c] {
			t.Fatalf("колонка %q объявлена снятой (%s), но её никогда не было в каноне — опечатка в имени", c, by)
		}
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
	// Фильтр строится по ПОЛНОМУ канону, а сверка идёт с ЖИВЫМ: иначе снятая колонка, забытая в
	// SELECT'е, просто выпала бы из проверяемого набора и равенство сошлось бы. Читать её из базы
	// нельзя — её там нет, — и такой SELECT падал бы в рантайме на 1054.
	var got []string
	for _, m := range re.FindAllStringSubmatch(selectClause, -1) {
		if inCanon[m[1]] {
			got = append(got, m[1])
		}
	}
	if !reflect.DeepEqual(got, operationColumnLiveCanon) {
		t.Fatalf("SELECT операций разошёлся с живым каноном §1:\n эталон: %v\n запрос: %v", operationColumnLiveCanon, got)
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
	if !reflect.DeepEqual(got, operationColumnLiveCanon) {
		t.Fatalf("поля entity.TechCardOperation разошлись с живым каноном §1:\n эталон: %v\n entity: %v", operationColumnLiveCanon, got)
	}
}

// TestTechCardOperationKindColumnsAreNullable — NULL значит «не указано», а не ноль и не «нет».
// Ни одна из 32 не имеет права быть голым int/string/bool: голый тип не отличает «технолог молчит»
// от «технолог сказал ноль», и волна намеренно не несёт ни одного sql.NullBool.
func TestTechCardOperationKindColumnsAreNullable(t *testing.T) {
	inCanon := make(map[string]bool, len(operationColumnLiveCanon))
	for _, c := range operationColumnLiveCanon {
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
//
// СВЕРЯЕТСЯ ПОЛНЫЙ КАНОН (34), А НЕ КАНОН 0324 (32), и это не мелочь. Фильтр inCanon выбрасывает
// всё, чего в эталоне нет, ДО сравнения: с каноном 0324 press_action и press_toward выпадали бы из
// проверяемого набора, и равенство сходилось бы независимо от того, есть ли эти ключи в карте
// вообще. Нога, охраняющая ПУТЬ ЗАПИСИ, была слепа ровно на те две колонки, которые волна 0325 и
// добавила. ALTER'ы остаются на своих половинах канона — у каждой миграции свой файл, — а эта нога
// смотрит на карту целиком, потому что карта одна.
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
	inCanon := make(map[string]bool, len(operationColumnCanon))
	for _, c := range operationColumnCanon {
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
	if !reflect.DeepEqual(got, operationColumnLiveCanon) {
		t.Fatalf("ключи insertTechCardOperations разошлись с живым каноном §1:\n эталон: %v\n INSERT: %v", operationColumnLiveCanon, got)
	}
}

// TestRetiredOperationKindColumnsAreGoneEverywhere — ЧЕТВЁРТАЯ работа канона, появившаяся вместе с
// первым снятием: доказать, что снятая колонка ушла ИЗ ВСЕХ ТРЁХ живых мест, а не из одного.
//
// Сверка с живым каноном ловит это косвенно (лишний ключ разошёлся бы со списком), но сообщение
// было бы про порядок, а не про снятие, и читатель пошёл бы искать перестановку. Здесь проверка
// прямая и называет и колонку, и файл, который её снял. Плюс она ловит случай, невидимый для
// DeepEqual целиком: колонка, оставшаяся в INSERT-карте, но выпавшая заодно и из entity, — тогда
// оба списка сойдутся друг с другом, а запись упадёт на 1054 в рантайме.
func TestRetiredOperationKindColumnsAreGoneEverywhere(t *testing.T) {
	rt := reflect.TypeOf(entity.TechCardOperation{})
	for col, by := range operationKindColumnRetired {
		for i := 0; i < rt.NumField(); i++ {
			if rt.Field(i).Tag.Get("db") == col {
				t.Errorf("entity.TechCardOperation всё ещё несёт поле с db-тегом %q, снятым в %s — StructScan попросит у базы колонку, которой нет", col, by)
			}
		}
		if strings.Contains(techCardOperationsQuery, "o."+col) {
			t.Errorf("SELECT операций всё ещё читает o.%s, снятую в %s — это 1054 на чтении ЛЮБОЙ тех-карты", col, by)
		}
		body, err := os.ReadFile(filepath.Join("..", "sql", by))
		if err != nil {
			t.Fatalf("миграция %s, объявленная снявшей %s, не читается: %v", by, col, err)
		}
		up := string(body)
		if i := strings.Index(up, "-- +migrate Down"); i >= 0 {
			up = up[:i]
		}
		if !strings.Contains(up, "DROP COLUMN "+col) {
			t.Errorf("%s объявлена снявшей колонку %s, но DROP COLUMN в её Up-половине нет", by, col)
		}
	}
}
