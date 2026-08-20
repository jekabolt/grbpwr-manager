package techcard

import (
	"reflect"
	"regexp"
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
// список продублирован здесь как независимый эталон, а тесты ниже сверяют с ним два из четырёх
// списков (третий — миграция, четвёртый проверяется round-trip'ом на контейнере).
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

// TestOperationKindColumnCanonSize — контрольное число волны (§1): ровно 32, не 31 и не 33.
func TestOperationKindColumnCanonSize(t *testing.T) {
	if len(operationKindColumnCanon) != 32 {
		t.Fatalf("волна 0324 несёт 32 колонки, в эталоне %d", len(operationKindColumnCanon))
	}
	seen := make(map[string]bool, len(operationKindColumnCanon))
	for _, c := range operationKindColumnCanon {
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
	inCanon := make(map[string]bool, len(operationKindColumnCanon))
	for _, c := range operationKindColumnCanon {
		inCanon[c] = true
	}
	var got []string
	for _, m := range re.FindAllStringSubmatch(selectClause, -1) {
		if inCanon[m[1]] {
			got = append(got, m[1])
		}
	}
	if !reflect.DeepEqual(got, operationKindColumnCanon) {
		t.Fatalf("SELECT операций разошёлся с каноном §1:\n эталон: %v\n запрос: %v", operationKindColumnCanon, got)
	}
}

// TestTechCardOperationEntityCarriesKindColumnsInCanonOrder — тот же эталон против db-тегов entity.
// Это второй из четырёх списков; вместе с предыдущим тестом он делает канон проверяемым без базы.
func TestTechCardOperationEntityCarriesKindColumnsInCanonOrder(t *testing.T) {
	inCanon := make(map[string]bool, len(operationKindColumnCanon))
	for _, c := range operationKindColumnCanon {
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
	if !reflect.DeepEqual(got, operationKindColumnCanon) {
		t.Fatalf("поля entity.TechCardOperation разошлись с каноном §1:\n эталон: %v\n entity: %v", operationKindColumnCanon, got)
	}
}

// TestTechCardOperationKindColumnsAreNullable — NULL значит «не указано», а не ноль и не «нет».
// Ни одна из 32 не имеет права быть голым int/string/bool: голый тип не отличает «технолог молчит»
// от «технолог сказал ноль», и волна намеренно не несёт ни одного sql.NullBool.
func TestTechCardOperationKindColumnsAreNullable(t *testing.T) {
	inCanon := make(map[string]bool, len(operationKindColumnCanon))
	for _, c := range operationKindColumnCanon {
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
