package entity

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// GUARD-ТЕСТЫ РЕЕСТРА ДЕФОЛТОВ РАБОТЫ (R3).
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ЭТОТ ФАЙЛ ПРОВЕРЕН (прогнаны и откачены):
//
//  1. Из ValidateOperationWorkDefaultField убрана ветка `operationWorkInheritedSet[field]` —
//     то есть СНЯТО САМО ПРАВИЛО, ради которого реестр существует. Отказ на `stitches_per_cm`
//     остался (поля нет в разрешённом срезе), но причина стала `unknown_field`.
//     ПОКРАСНЕЛ TestOperationWorkDefaultRefusesMachineSettingByName — он сверяет ПРИЧИНУ, а не
//     факт отказа, и это единственное, что отличает правило от совпадения. Вернул — зелёный.
//     (Тест, проверяющий только «err != nil», эту мутацию пропустил бы целиком — проверено.)
//  2. В OperationWorkDefaultFields дописан "stitches_per_cm" (машинная настройка притворилась
//     свойством вида): покраснели TestOperationWorkDefaultRegistryHasNoMachineSettings (списки
//     пересеклись) и TestOperationWorkDefaultAcceptsWorkProperties (реестр отдаёт поле, которое сам
//     же отвергает). При этом ЗАПИСЬ осталась отказом — TestRememberOperationWorkDefaultRefusesMachineSetting
//     в internal/apisrv/admin остался ЗЕЛЁНЫМ, потому что запрет стоит выше разрешения: противоречие
//     ловится и тестом, и рантаймом, а не только тестом. Вернул — зелёный.
//  3. Из OperationWorkInheritedFields убран "press_dwell_sec": покраснел
//     TestOperationWorkInheritedCoversTheEquipmentLadder — он ВЫВОДИТ лестницу из тегов db у
//     профилей 0306, а не переписывает её. Вернул — зелёный.
//  4. В OperationWorkDefaultFields дописано несуществующее "topstitch_width" (без _mm):
//     покраснел TestOperationWorkDefaultFieldsAreRealColumns. Вернул — зелёный.

// TestOperationWorkDefaultRefusesMachineSettingByName — ЦИТАТА ПРАВИЛА.
//
// Машинная настройка отвергается ПО ИМЕНИ и с собственной причиной: клиент по ней говорит «это
// живёт в парке оборудования карточки», а не «такого поля нет». Проверяется ИМЕННО ПРИЧИНА —
// проверка «отказ случился» зелена и без правила (см. мутацию 1 в шапке).
func TestOperationWorkDefaultRefusesMachineSettingByName(t *testing.T) {
	for _, field := range []string{"stitches_per_cm", "needle_size_nm", "press_temperature_c", "machine_type"} {
		ve := ValidateOperationWorkDefaultField(field)
		if ve == nil {
			t.Fatalf("%s: машинная настройка принята как дефолт вида — два механизма на одном поле", field)
		}
		if ve.Reason != OperationWorkDefaultReasonInherited {
			t.Fatalf("%s: причина отказа %q, ожидалась %q — «нет такого поля» уводит искать опечатку вместо парка оборудования",
				field, ve.Reason, OperationWorkDefaultReasonInherited)
		}
		if ve.Conflicting != field {
			t.Fatalf("%s: отказ не назвал поле (conflicting=%q)", field, ve.Conflicting)
		}
		if !strings.Contains(ve.HowToFix, "0306") {
			t.Fatalf("%s: отказ не сослался на лестницу 0306: %q", field, ve.HowToFix)
		}
	}
}

// TestOperationWorkDefaultAcceptsWorkProperties — вторая половина правила: разрешённое поле проходит.
// Без неё «правило» можно было бы удовлетворить отказом на всё подряд.
func TestOperationWorkDefaultAcceptsWorkProperties(t *testing.T) {
	for _, field := range OperationWorkDefaultFields {
		if ve := ValidateOperationWorkDefaultField(field); ve != nil {
			t.Errorf("%s: собственное свойство вида отвергнуто: %s", field, ve.Error())
		}
	}
}

func TestOperationWorkDefaultRefusesUnknownField(t *testing.T) {
	ve := ValidateOperationWorkDefaultField("no_such_field")
	if ve == nil || ve.Reason != OperationWorkDefaultReasonUnknown {
		t.Fatalf("незнакомое поле: %v", ve)
	}
	if ve := ValidateOperationWorkDefaultField("  "); ve == nil || ve.Reason != OperationWorkDefaultReasonRequired {
		t.Fatalf("пустое имя поля: %v", ve)
	}
}

// TestOperationWorkDefaultRegistryHasNoMachineSettings — ДВА СПИСКА НЕ ПЕРЕСЕКАЮТСЯ.
// Противоречие «поле и разрешено, и запрещено» иначе жило бы молча: рантайм отвечал бы отказом
// (запрет стоит выше), а клиент рисовал бы жест «запомнить как дефолт» на контроле, где он
// заведомо не сработает.
func TestOperationWorkDefaultRegistryHasNoMachineSettings(t *testing.T) {
	for _, f := range OperationWorkDefaultFields {
		if operationWorkInheritedSet[f] {
			t.Errorf("%s стоит и в разрешённых, и в наследуемых — у поля не может быть двух механизмов", f)
		}
	}
}

// TestOperationWorkInheritedCoversTheEquipmentLadder — ЗАПРЕТНЫЙ СПИСОК ВЫВЕДЕН, А НЕ ПЕРЕПИСАН.
//
// Лестница 0306 — это те колонки шага, на которые отвечает профиль оборудования карточки. Тест
// считает их ПЕРЕСЕЧЕНИЕМ тегов db у entity.TechCardOperation и у пары профилей и требует, чтобы
// каждая попала в OperationWorkInheritedFields. Появится в будущей миграции новая наследуемая
// настройка — тест покраснеет до тех пор, пока имя не приписано, и глобальный дефолт не успеет
// встать вторым механизмом поверх неё.
func TestOperationWorkInheritedCoversTheEquipmentLadder(t *testing.T) {
	// note — ЕДИНСТВЕННОЕ исключение, и оно названо вслух: у профиля это модель станка, у шага —
	// проза технолога. Одно имя, два разных факта; наследования между ними нет.
	exempt := map[string]bool{"note": true}

	opCols := dbTagSet(t, TechCardOperation{})
	ladder := map[string]bool{}
	for _, profile := range []any{TechCardMachineProfile{}, TechCardPressProfile{}} {
		for col := range dbTagSet(t, profile) {
			if opCols[col] && !exempt[col] {
				ladder[col] = true
			}
		}
	}
	if len(ladder) == 0 {
		t.Fatal("пересечение шага и профилей пусто — тест перестал что-либо доказывать")
	}
	for col := range ladder {
		if !operationWorkInheritedSet[col] {
			t.Errorf("колонка %s наследуется от профиля оборудования (0306), но не запрещена реестром дефолтов", col)
		}
	}
}

// TestOperationWorkDefaultFieldsAreRealColumns — оба списка говорят именами настоящих колонок шага.
// Опечатка в имени сделала бы разрешение бесполезным (дефолт некуда положить) или запрет —
// дырявым (запрещено имя, которого никто не шлёт).
func TestOperationWorkDefaultFieldsAreRealColumns(t *testing.T) {
	opCols := dbTagSet(t, TechCardOperation{})
	for _, list := range [][]string{OperationWorkDefaultFields, OperationWorkInheritedFields} {
		for _, f := range list {
			if !opCols[f] {
				t.Errorf("%q не колонка tech_card_operation — такого поля у шага нет", f)
			}
		}
	}
}

// TestOperationWorkDefaultSliceAndRulesAgree — СРЕЗ И КАРТА ПРАВИЛ РАВНЫ.
// Срез задаёт порядок и уезжает клиенту, карта — проверку значения. Поле, попавшее только в срез,
// разрешено и НЕ ПРОВЕРЯЕТСЯ ничем; попавшее только в карту — недостижимо.
func TestOperationWorkDefaultSliceAndRulesAgree(t *testing.T) {
	want := append([]string(nil), OperationWorkDefaultFields...)
	sort.Strings(want)
	got := OperationWorkDefaultRuleFields()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("срез и карта правил разошлись:\n срез: %v\n карта: %v", want, got)
	}
	seen := map[string]bool{}
	for _, f := range OperationWorkDefaultFields {
		if seen[f] {
			t.Errorf("дубль поля в реестре: %s", f)
		}
		seen[f] = true
	}
}

// TestOperationWorkDefaultValueRules — значение проверяется теми же краями и словарями, что и на шаге.
func TestOperationWorkDefaultValueRules(t *testing.T) {
	tests := []struct {
		field, value, wantReason string
	}{
		{"topstitch_mode", "edge", ""},
		{"topstitch_mode", "diagonal", OperationWorkDefaultReasonUnknownV},
		{"topstitch_width_mm", "2", ""},
		{"topstitch_width_mm", "2.25", OperationWorkDefaultReasonScale},
		{"topstitch_width_mm", "100", OperationWorkDefaultReasonRange}, // верх строгий: правило шага требует «меньше 100»
		{"topstitch_rows", "4", ""},
		{"topstitch_rows", "5", OperationWorkDefaultReasonRange},
		{"needle_gauge_mm", "1.6", ""},
		{"needle_gauge_mm", "1.5", OperationWorkDefaultReasonRange},
		{"fullness_ratio", "1.25", ""},
		{"fullness_ratio", "4.01", OperationWorkDefaultReasonRange},
		{"cut_length_mm", "18", ""},
		{"cut_length_mm", "3", OperationWorkDefaultReasonRange},
		{"press_action", "press_flat", ""},
		{"press_toward", "front", ""},
		{"air_temperature_c", "нет", OperationWorkDefaultReasonUnknownV},
		{"second_press_sec", "", OperationWorkDefaultReasonRequired},
		{"topstitch_mode", strings.Repeat("x", MaxOperationWorkDefaultValueLen+1), OperationWorkDefaultReasonTooLong},
	}
	for _, tt := range tests {
		ve := ValidateOperationWorkDefaultValue(tt.field, tt.value)
		got := ""
		if ve != nil {
			got = ve.Reason
		}
		if got != tt.wantReason {
			t.Errorf("%s=%q: причина %q, ожидалась %q", tt.field, tt.value, got, tt.wantReason)
		}
	}
}

// TestLatestOperationWorkSmvPicksTheLatestStep — ЦИТАТА ПОДСКАЗКИ НОРМЫ ВРЕМЕНИ.
// Из нескольких шагов с одной работой побеждает САМЫЙ СВЕЖИЙ, и порядок входа на это не влияет.
func TestLatestOperationWorkSmvPicksTheLatestStep(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	samples := []OperationWorkSmvSample{
		{WorkToken: "topstitch", SMV: decimal.RequireFromString("1.2"), CardName: "COAT", CardUpdatedAt: recent, OperationID: 7},
		{WorkToken: "topstitch", SMV: decimal.RequireFromString("0.4"), CardName: "TEE", CardUpdatedAt: old, OperationID: 900},
		{WorkToken: "buttonhole", SMV: decimal.RequireFromString("0.3"), CardName: "TEE", CardUpdatedAt: old, OperationID: 3},
		// та же карточка, что и первый: время одинаковое, различает только id строки
		{WorkToken: "topstitch", SMV: decimal.RequireFromString("9.9"), CardName: "COAT", CardUpdatedAt: recent, OperationID: 4},
		{WorkToken: "", SMV: decimal.RequireFromString("5"), CardName: "X", CardUpdatedAt: recent, OperationID: 1},
	}
	got := LatestOperationWorkSmv(samples)
	if len(got) != 2 {
		t.Fatalf("подсказок %d, ожидалось 2 (работа без токена не подсказывает ничего): %+v", len(got), got)
	}
	byToken := map[string]OperationWorkSmvHint{}
	for _, h := range got {
		byToken[h.WorkToken] = h
	}
	if h := byToken["topstitch"]; h.LastSMV.String() != "1.2" || h.CardName != "COAT" {
		t.Fatalf("отстрочка: подсказка %s (%s), ожидалось 1.2 (COAT) — взят не последний шаг", h.LastSMV, h.CardName)
	}
	if h := byToken["buttonhole"]; h.LastSMV.String() != "0.3" {
		t.Fatalf("петля: подсказка %s, ожидалось 0.3", h.LastSMV)
	}

	// Порядок входа не значим: перевёрнутый срез даёт тот же ответ.
	reversed := make([]OperationWorkSmvSample, 0, len(samples))
	for i := len(samples) - 1; i >= 0; i-- {
		reversed = append(reversed, samples[i])
	}
	for _, h := range LatestOperationWorkSmv(reversed) {
		if h.LastSMV.String() != byToken[h.WorkToken].LastSMV.String() {
			t.Fatalf("%s: ответ зависит от порядка строк из базы (%s против %s)",
				h.WorkToken, h.LastSMV, byToken[h.WorkToken].LastSMV)
		}
	}
	if LatestOperationWorkSmv(nil) != nil {
		t.Fatal("пустой вход обязан давать пустой ответ, а не пустой непустой срез")
	}
}

// dbTagSet returns the `db` struct tags of v, skipping the `-` ones (fields that are not columns).
func dbTagSet(t *testing.T, v any) map[string]bool {
	t.Helper()
	rt := reflect.TypeOf(v)
	if rt.Kind() != reflect.Struct {
		t.Fatalf("dbTagSet: %T is not a struct", v)
	}
	out := make(map[string]bool, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}
		out[tag] = true
	}
	return out
}
