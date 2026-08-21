package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ХВОСТЫ ВИДОВ ОПЕРАЦИЙ (волна 0324) — тесты формы, а не значений.
//
// Голден-эталон рядом (techcard_operation_digest_golden_test.go) морозит ГОЛОВУ кортежа и четыре
// старых позиционных хвоста. Здесь заморожено другое: СВОЙСТВО новой формы записи. Одиннадцать
// хвостов волны пишутся списком пар ["тег", [[ключ, значение], …]] именно затем, чтобы отложенные
// 32 поля тех же семейств можно было дописать позже, не тронув подписи уже подписанных карточек.
// Свойство «дописка пустого поля не меняет отпечаток» и есть содержание волны; проверяется оно
// ниже ПРЯМО, сравнением hex, а не рассуждением в комментарии.

// opKindHead — длина головы кортежа шага. Та же константа, что в голден-тесте, и по той же
// причине: всё, что правее, — тегированные хвосты.
const opKindHead = 16

// opKindTailsOf достаёт хвосты одного спроецированного шага (всё, что правее головы).
func opKindTailsOf(t *testing.T, op entity.TechCardOperation) []any {
	t.Helper()
	row, ok := opGoldProject(t, op).([]any)
	if !ok {
		t.Fatalf("кортеж операции перестал быть []any")
	}
	if len(row) < opKindHead {
		t.Fatalf("голова кортежа короче %d позиций (%d)", opKindHead, len(row))
	}
	return row[opKindHead:]
}

// opKindTagsOf — теги хвостов по порядку.
func opKindTagsOf(t *testing.T, op entity.TechCardOperation) []string {
	t.Helper()
	tails := opKindTailsOf(t, op)
	tags := make([]string, 0, len(tails))
	for i, tail := range tails {
		tagged, ok := tail.([]any)
		if !ok || len(tagged) == 0 {
			t.Fatalf("хвост %d не тегированный кортеж: %#v", i, tail)
		}
		tag, ok := tagged[0].(string)
		if !ok || tag == "" {
			t.Fatalf("хвост %d начинается не с имени-тега: %#v", i, tagged[0])
		}
		tags = append(tags, tag)
	}
	return tags
}

// opKindIsPairTail — ХВОСТ ЛИ ЭТО ПАРНОЙ ФОРМЫ, по его СТРУКТУРЕ, а не по тегу.
//
// Тег "press" носят ДВА хвоста: позиционный ВТО-хвост 0306 (оборудование, температура, выдержка,
// давление, пар, проутюжильник — восемь элементов) и парный хвост 0325 (под-глагол и направление
// припуска). Тег у них общий намеренно: он отвечает на вопрос, ЧЕЙ хвост, и оба — ВТО; второе имя
// («press2») было бы ровно тем вечным костылём, ради отказа от которого волна пишется парами.
//
// Отсюда и этот предикат: различать хвосты по одному лишь тегу больше нельзя, а по форме — можно и
// однозначно. Парный хвост — РОВНО ДВА элемента, второй из которых список двухэлементных пар со
// СТРОКОВЫМ ключом. Позиционные хвосты этой формы не имеют: machine — девять элементов, press —
// восемь, assembly — три, media — два, но его записи трёхэлементны.
func opKindIsPairTail(tail []any) bool {
	if len(tail) != 2 {
		return false
	}
	pairs, ok := tail[1].([]any)
	if !ok || len(pairs) == 0 {
		return false
	}
	for _, p := range pairs {
		pair, ok := p.([]any)
		if !ok || len(pair) != 2 {
			return false
		}
		if _, ok := pair[0].(string); !ok {
			return false
		}
	}
	return true
}

// opKindPairTagsOf — теги ТОЛЬКО парных хвостов шага, по порядку.
func opKindPairTagsOf(t *testing.T, op entity.TechCardOperation) []string {
	t.Helper()
	var tags []string
	for i, tail := range opKindTailsOf(t, op) {
		tagged, ok := tail.([]any)
		if !ok || len(tagged) == 0 {
			t.Fatalf("хвост %d не тегированный кортеж: %#v", i, tail)
		}
		if !opKindIsPairTail(tagged) {
			continue
		}
		tags = append(tags, tagged[0].(string))
	}
	return tags
}

// opKindPairKeys — ключи пар одного хвоста, по порядку записи.
func opKindPairKeys(t *testing.T, tail []any) []string {
	t.Helper()
	if len(tail) != 2 {
		t.Fatalf("хвост семейства не пара [тег, пары]: %#v", tail)
	}
	pairs, ok := tail[1].([]any)
	if !ok {
		t.Fatalf("второй элемент хвоста не список пар: %#v", tail[1])
	}
	keys := make([]string, 0, len(pairs))
	for i, p := range pairs {
		pair, ok := p.([]any)
		if !ok || len(pair) != 2 {
			t.Fatalf("пара %d не двухэлементна: %#v", i, p)
		}
		key, ok := pair[0].(string)
		if !ok || key == "" {
			t.Fatalf("ключ пары %d не непустая строка: %#v", i, pair[0])
		}
		keys = append(keys, key)
	}
	return keys
}

// opKindWaveTags — теги парных хвостов в замороженном порядке: одиннадцать волны 0324 и
// двенадцатый, "press" (0325), дописанный последним. "handwork" по-прежнему не рождается — оба его
// поля отложены, — и закреплённое за ним последнее место сдвигается за "press": сегодня это
// безопасно по построению, ни одна строка не эмитит ни того, ни другого.
//
// ТЕГ "press" СОВПАДАЕТ С ТЕГОМ ПОЗИЦИОННОГО ВТО-ХВОСТА НАМЕРЕННО: тег отвечает на вопрос, ЧЕЙ
// хвост, и оба — ВТО. Второе имя было бы ровно тем вечным костылём ("press2"), ради отказа от
// которого волна и пишется парами.
var opKindWaveTags = []string{
	"stitching", "placement", "hardware", "print", "weld", "trim",
	"thread_trim", "clean", "inspect", "wet", "fastening", "press",
}

// ── 1. НУЛЕВАЯ ВОЛНА ────────────────────────────────────────────────────────────────────────────

// TestOperationKindTailsAbsentWhenAllNull — ни один шаг, у которого все 32 колонки волны NULL, не
// эмитит ни одного нового хвоста.
//
// Это половина довода «выкатка не протухает ни одной подписи»: вторая половина — замороженный hex
// в голден-тесте, который считается по ТЕМ ЖЕ кейсам и остался прежним. Вместе они говорят: байты
// сегодняшних карточек не двинулись.
func TestOperationKindTailsAbsentWhenAllNull(t *testing.T) {
	wave := make(map[string]bool, len(opKindWaveTags))
	for _, tag := range opKindWaveTags {
		wave[tag] = true
	}
	for _, tt := range opGoldCases() {
		t.Run(tt.name, func(t *testing.T) {
			// ПО ФОРМЕ, А НЕ ПО ТЕГУ: голден-кейс «блок ВТО» эмитит ПОЗИЦИОННЫЙ хвост "press"
			// 0306, и по одному тегу он был бы неотличим от парного хвоста 0325. Проверяется здесь
			// именно рождение ПАРНОГО хвоста на строке, где все колонки волны NULL.
			for _, tag := range opKindPairTagsOf(t, tt.op) {
				if wave[tag] {
					t.Errorf("у шага без единой заполненной колонки волны родился хвост %q — "+
						"значит выкатка сдвинет отпечаток каждой карточки в базе", tag)
				}
			}
		})
	}
	// И то же самое напрямую: у пустой строки набор хвостов волны ПУСТ, а не «пуст по составу».
	if tails := operationKindTails(&entity.TechCardOperation{}); len(tails) != 0 {
		t.Errorf("operationKindTails на пустом шаге вернул %d хвостов, ожидалось 0: %#v",
			len(tails), tails)
	}
}

// ── 2. СВОЙСТВО, РАДИ КОТОРОГО ВЫБРАНЫ ПАРЫ ─────────────────────────────────────────────────────

// opKindSwapTail подменяет хвост с тегом tag на «завтрашний» и возвращает проекцию целиком.
//
// Так выглядит будущая append-волна с точки зрения отпечатка: тот же шаг, тот же набор
// заполненных полей, но набор КЛЮЧЕЙ семейства расширен полем, которого сегодня нет. Позиционный
// хвост от такой правки поехал бы всегда; хвост-пары обязан остаться байт в байт.
func opKindSwapTail(t *testing.T, tc *entity.TechCardInsert, tag string, tomorrow []any) any {
	t.Helper()
	outer, ok := constructionProjection(tc).([]any)
	if !ok {
		t.Fatalf("внешний кортеж CONSTRUCTION перестал быть []any")
	}
	ops, ok := outer[1].([]any)
	if !ok || len(ops) != 1 {
		t.Fatalf("список операций не единичный: %#v", outer[1])
	}
	row := append([]any(nil), ops[0].([]any)...)
	found := false
	for i := opKindHead; i < len(row); i++ {
		if tagged, ok := row[i].([]any); ok && len(tagged) > 0 && tagged[0] == tag {
			row[i] = tomorrow
			found = true
		}
	}
	if !found {
		t.Fatalf("хвоста %q в кортеже нет — подменять нечего", tag)
	}
	newOps := append([]any(nil), ops...)
	newOps[0] = row
	newOuter := append([]any(nil), outer...)
	newOuter[1] = newOps
	return newOuter
}

// TestOperationKindEmptyFieldDoesNotMoveDigest — ГЛАВНЫЙ тест волны.
//
// Берём шаг с одним заполненным полем семейства, считаем hex карточки. Затем собираем тот же
// хвост «как он будет выглядеть после append-волны»: те же заполненные поля плюс поля, которых
// сегодня нет вовсе и которые остались пустыми. Hex обязан СОВПАСТЬ — иначе форма пар не даёт
// ничего сверх позиционной, и восемь костылей "stitching2"/"hardware2"/… были бы неизбежны.
func TestOperationKindEmptyFieldDoesNotMoveDigest(t *testing.T) {
	cases := []struct {
		name     string
		op       entity.TechCardOperation
		tag      string
		tomorrow []any
	}{
		{
			name: "stitching: сегодня needle_count, завтра плюс два пустых поля из отложенных",
			op:   entity.TechCardOperation{NeedleCount: opGoldI32(2)},
			tag:  "stitching",
			tomorrow: operationKindTail("stitching",
				opKindInt("needle_count", opGoldI32(2)),
				opKindStr("eased_ply", sql.NullString{}),                   // S6, отложено
				opKindInt("row_count", sql.NullInt32{}),                    // S16, отложено
				opKindDec("elastic_elongation_pct", decimal.NullDecimal{}), // S7, отложено
			),
		},
		{
			name: "fastening: сегодня zipper_application, завтра плюс пустые fly_side и corded",
			op:   entity.TechCardOperation{ZipperApplication: opGoldStr("invisible")},
			tag:  "fastening",
			tomorrow: operationKindTail("fastening",
				opKindStr("zipper_application", opGoldStr("invisible")),
				opKindStr("fly_side", sql.NullString{}),                 // FA14, отложено
				opKindDec("taper_bar_length_mm", decimal.NullDecimal{}), // FA6, отложено
			),
		},
		{
			name: "press: сегодня press_action, завтра плюс пустое поле того же семейства",
			op:   entity.TechCardOperation{PressAction: opGoldStr("press_flat")},
			tag:  "press",
			tomorrow: operationKindTail("press",
				opKindStr("press_action", opGoldStr("press_flat")),
				opKindStr("press_toward", sql.NullString{}),
				opKindStr("press_pass", sql.NullString{}), // ещё не заведено; пустое обязано молчать
			),
		},
		{
			name: "clean: единственное поле сегодня, завтра рядом с ним пустой clean_agent",
			op:   entity.TechCardOperation{CleaningKind: opGoldStr("spot_clean")},
			tag:  "clean",
			tomorrow: operationKindTail("clean",
				opKindStr("cleaning_kind", opGoldStr("spot_clean")),
				opKindStr("clean_agent", sql.NullString{}), // C2, отложено
			),
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tc := &entity.TechCardInsert{Operations: []entity.TechCardOperation{tt.op}}
			today := digestOf(constructionProjection(tc))
			tomorrow := digestOf(opKindSwapTail(t, tc, tt.tag, tt.tomorrow))
			if today != tomorrow {
				t.Errorf("дописка ПУСТОГО поля в семейство %q сдвинула отпечаток — форма пар "+
					"перестала защищать подписи, и append-волна протухнет всю базу.\n"+
					"сегодня: %s\nзавтра:  %s", tt.tag, today, tomorrow)
			}
		})
	}
}

// TestOperationKindFilledFieldMovesDigest — обратная половина того же свойства. Пустое поле
// молчит, ЗАПОЛНЕННОЕ обязано говорить: это новый факт цеха, и подпись под старым содержанием
// должна протухнуть. Хвост, который не меняет отпечаток при новом факте, был бы хуже отсутствия
// хвоста — он бы врал.
func TestOperationKindFilledFieldMovesDigest(t *testing.T) {
	base := &entity.TechCardInsert{Operations: []entity.TechCardOperation{
		{NeedleCount: opGoldI32(2)},
	}}
	filled := &entity.TechCardInsert{Operations: []entity.TechCardOperation{
		{NeedleCount: opGoldI32(2), SeamSecuring: opGoldStr("backtack")},
	}}
	if digestOf(constructionProjection(base)) == digestOf(constructionProjection(filled)) {
		t.Errorf("заполненная закрепка не сдвинула отпечаток — подпись под «без закрепки» " +
			"читалась бы как действительная под «с закрепкой»")
	}
}

// TestOperationKindRenamedKeyMovesDigest — ЦЕНА ВЫБОРА, зафиксированная намеренно.
//
// Ключи пар входят в отпечаток, поэтому переименовать ключ после первой подписи нельзя: карточки
// протухнут все разом. Тест существует ровно затем, чтобы «причёсывание» имён колонок задним
// числом упиралось в красное, а не в тишину.
func TestOperationKindRenamedKeyMovesDigest(t *testing.T) {
	asIs := operationKindTail("clean", opKindStr("cleaning_kind", opGoldStr("spot_clean")))
	renamed := operationKindTail("clean", opKindStr("kind", opGoldStr("spot_clean")))
	if digestOf(asIs) == digestOf(renamed) {
		t.Errorf("переименование ключа не сдвинуло отпечаток — значит ключ в него не входит, " +
			"и прочесть подписанный хвост однозначно нельзя")
	}
}

// ── 3. ПОРЯДОК ПАР — ПОБАЙТНЫЙ ──────────────────────────────────────────────────────────────────

// TestOperationKindPairsSortedByBytes — сортировка ключей побайтная, а не локале-зависимая.
//
// Ключи подобраны так, что байтовый порядок и любой человекочитаемый расходятся: 'A' = 0x41,
// '_' = 0x5F, 'b' = 0x62, поэтому побайтно выходит A_key < _key < b_key, а ICU-подобная сортировка
// поставила бы подчёркивание первым и не различала бы регистр. Если кто-нибудь заменит сравнение
// на collation-зависимое, отпечаток начнёт зависеть от машины и локали — этот тест обязан упасть
// раньше, чем это доедет до прода.
func TestOperationKindPairsSortedByBytes(t *testing.T) {
	tail := operationKindTail("probe",
		opKindStr("b_key", opGoldStr("v")),
		opKindStr("A_key", opGoldStr("v")),
		opKindStr("_key", opGoldStr("v")),
	)
	got := opKindPairKeys(t, tail)
	want := []string{"A_key", "_key", "b_key"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("порядок пар не побайтный: %v, ожидалось %v", got, want)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("пар %d, ожидалось %d: %v", len(got), len(want), got)
	}
}

// TestOperationKindPairsSortedInRealFamily — та же проверка на настоящем семействе, где порядок
// объявления полей заведомо не совпадает с байтовым: объявлены buttonhole_style, cut_length_mm,
// buttonhole_orientation, bartack_length_mm, attach_pattern, zipper_application.
func TestOperationKindPairsSortedInRealFamily(t *testing.T) {
	op := entity.TechCardOperation{
		ButtonholeStyle:       opGoldStr("eyelet"),
		CutLengthMm:           opGoldDec("18.5"),
		ButtonholeOrientation: opGoldStr("vertical"),
		BartackLengthMm:       opGoldDec("6"),
		AttachPattern:         opGoldStr("cross_x"),
		ZipperApplication:     opGoldStr("invisible"),
	}
	tails := opKindTailsOf(t, op)
	if len(tails) != 1 {
		t.Fatalf("ожидался ровно один хвост, получено %d: %#v", len(tails), tails)
	}
	got := opKindPairKeys(t, tails[0].([]any))
	want := []string{
		"attach_pattern", "bartack_length_mm", "buttonhole_orientation",
		"buttonhole_style", "cut_length_mm", "zipper_application",
	}
	if len(got) != len(want) {
		t.Fatalf("пар %d, ожидалось %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("порядок пар семейства fastening: %v, ожидалось %v", got, want)
		}
	}
}

// ── 4. ПО ОДНОМУ КЕЙСУ НА КАЖДЫЙ ИЗ ОДИННАДЦАТИ ХВОСТОВ ─────────────────────────────────────────

// TestOperationKindSingleFieldTail — заполнено ровно одно поле семейства ⇒ рождается ровно один
// хвост с ровно одной парой, и ключ этой пары — имя КОЛОНКИ БД.
//
// Три кейса ниже расходятся с именем поля proto намеренно и по-крупному: placement_count (а не
// count), trim_action (а не action), cleaning_kind (а не kind). Имя proto заморозило бы навсегда
// ключи, которые без семейства в имени читаются как что угодно.
func TestOperationKindSingleFieldTail(t *testing.T) {
	cases := []struct {
		name  string
		op    entity.TechCardOperation
		tag   string
		key   string
		value any
	}{
		{"stitching", entity.TechCardOperation{NeedleCount: opGoldI32(2)},
			"stitching", "needle_count", int32(2)},
		{"placement — ключ placement_count, а не count", entity.TechCardOperation{PlacementCount: opGoldI32(6)},
			"placement", "placement_count", int32(6)},
		{"hardware", entity.TechCardOperation{AttachMethod: opGoldStr("prong_clinch")},
			"hardware", "attach_method", "prong_clinch"},
		{"print", entity.TechCardOperation{PrintMethod: opGoldStr("screen")},
			"print", "print_method", "screen"},
		{"weld", entity.TechCardOperation{AirTemperatureC: opGoldI32(520)},
			"weld", "air_temperature_c", int32(520)},
		{"trim — ключ trim_action, а не action", entity.TechCardOperation{TrimAction: opGoldStr("grade_layers")},
			"trim", "trim_action", "grade_layers"},
		{"thread_trim", entity.TechCardOperation{ResidualTailMaxMm: opGoldDec("2.5")},
			"thread_trim", "residual_tail_max_mm", "2.5"},
		{"clean — ключ cleaning_kind, а не kind", entity.TechCardOperation{CleaningKind: opGoldStr("spot_clean")},
			"clean", "cleaning_kind", "spot_clean"},
		{"inspect", entity.TechCardOperation{CoverageMode: opGoldStr("aql_plan")},
			"inspect", "coverage_mode", "aql_plan"},
		{"wet", entity.TechCardOperation{WetProcessKind: opGoldStr("enzyme")},
			"wet", "wet_process_kind", "enzyme"},
		{"fastening", entity.TechCardOperation{ZipperApplication: opGoldStr("invisible")},
			"fastening", "zipper_application", "invisible"},
		{"press — под-глагол ВТО", entity.TechCardOperation{PressAction: opGoldStr("press_flat")},
			"press", "press_action", "press_flat"},
	}
	if len(cases) != len(opKindWaveTags) {
		t.Fatalf("кейсов %d, а хвостов волны %d — семейство осталось без теста", len(cases), len(opKindWaveTags))
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tails := opKindTailsOf(t, tt.op)
			want := []any{tt.tag, []any{[]any{tt.key, tt.value}}}
			got := opGoldJSON(t, tails)
			if got != opGoldJSON(t, []any{want}) {
				t.Errorf("хвост семейства %q не тот.\n--- ожидалось ---\n%s\n--- получено ---\n%s",
					tt.tag, opGoldJSON(t, []any{want}), got)
			}
		})
	}
}

// TestOperationKindTailOrderFrozen — порядок одиннадцати хвостов в кортеже. Он заморожен навсегда:
// перестановка хвостов местами меняет байты у каждой строки, которая эмитит больше одного.
func TestOperationKindTailOrderFrozen(t *testing.T) {
	// Шаг, у которого заполнено по одному полю КАЖДОГО семейства и ни одного факта из старых
	// четырёх хвостов: MachineType пуст, значит machine-хвост не рождается.
	op := entity.TechCardOperation{
		OperationType:     entity.OpTypeMachine,
		NeedleCount:       opGoldI32(2),
		PlacementCount:    opGoldI32(6),
		AttachMethod:      opGoldStr("prong_clinch"),
		PrintMethod:       opGoldStr("screen"),
		AirTemperatureC:   opGoldI32(520),
		TrimAction:        opGoldStr("grade_layers"),
		ResidualTailMaxMm: opGoldDec("2.5"),
		CleaningKind:      opGoldStr("spot_clean"),
		CoverageMode:      opGoldStr("aql_plan"),
		WetProcessKind:    opGoldStr("enzyme"),
		ZipperApplication: opGoldStr("invisible"),
		PressAction:       opGoldStr("press_flat"),
	}
	got := opKindPairTagsOf(t, op)
	if len(got) != len(opKindWaveTags) {
		t.Fatalf("хвостов %d, ожидалось %d: %v", len(got), len(opKindWaveTags), got)
	}
	for i := range opKindWaveTags {
		if got[i] != opKindWaveTags[i] {
			t.Fatalf("порядок хвостов волны поехал: %v, ожидалось %v", got, opKindWaveTags)
		}
	}
}

// ── 5. ДЕЦИМАЛЫ — СТРОКОЙ, ЧЕРЕЗ digestDecimal ──────────────────────────────────────────────────

// TestOperationKindDecimalsGoThroughDigestDecimal — децимал попадает в пару СТРОКОЙ, и 1.50 против
// 1.5 дают один отпечаток. Через float64 они дали бы разные байты в зависимости от того, что
// вернула БД, и подпись протухала бы от формата колонки, а не от правки технолога.
func TestOperationKindDecimalsGoThroughDigestDecimal(t *testing.T) {
	tail := operationKindTail("stitching", opKindDec("needle_gauge_mm", opGoldDec("1.50")))
	tagged := tail[1].([]any)
	pair := tagged[0].([]any)
	if v, ok := pair[1].(string); !ok || v != "1.5" {
		t.Fatalf("децимал попал в пару не строкой digestDecimal: %#v", pair[1])
	}
	one := &entity.TechCardInsert{Operations: []entity.TechCardOperation{
		{NeedleGaugeMm: opGoldDec("1.50")},
	}}
	two := &entity.TechCardInsert{Operations: []entity.TechCardOperation{
		{NeedleGaugeMm: opGoldDec("1.5")},
	}}
	if digestOf(constructionProjection(one)) != digestOf(constructionProjection(two)) {
		t.Errorf("1.50 и 1.5 дали разные отпечатки — подпись зависит от формата колонки, " +
			"а не от содержания")
	}
}

// ── 6. ДВА ХВОСТА ПОД ОДНИМ ТЕГОМ "press" (0325) ────────────────────────────────────────────────

// TestPressTailsCoexistUnderOneTag пришпиливает решение, которое иначе выглядело бы недосмотром.
//
// ВТО-факты шага живут в ДВУХ хвостах: позиционном (0306 — оборудование, температура, выдержка,
// давление, пар, проутюжильник) и парном (0325 — под-глагол и направление припуска). Тег у них
// ОДИН, "press", и это выбор: тег отвечает на вопрос, ЧЕЙ хвост, оба — ВТО, а второе имя навсегда
// заморозило бы «press2» — ровно тот вечный костыль, ради отказа от которого волна пишется парами.
//
// Дописать два новых факта В ПОЗИЦИОННЫЙ хвост было нельзя: длина массива входит в байты, и каждая
// карточка, где ВТО-факты уже заполнены, протухла бы подписью в момент выкатки.
//
// Цена решения — она же и предел: по ОДНОМУ ТЕГУ хвосты больше не различить, различает их ФОРМА.
// Тест фиксирует и то, и другое: оба хвоста рождаются, оба несут тег "press", позиционный идёт
// первым (он левее media), и формы у них разные.
func TestPressTailsCoexistUnderOneTag(t *testing.T) {
	op := entity.TechCardOperation{
		OperationType:  entity.OpTypePress,
		PressEquipment: opGoldStr("iron"),
		PressAction:    opGoldStr("to_one_side"),
		PressToward:    opGoldStr("front"),
	}
	var pressTails [][]any
	for i, tail := range opKindTailsOf(t, op) {
		tagged, ok := tail.([]any)
		if !ok || len(tagged) == 0 {
			t.Fatalf("хвост %d не тегированный кортеж: %#v", i, tail)
		}
		if tagged[0] == "press" {
			pressTails = append(pressTails, tagged)
		}
	}
	if len(pressTails) != 2 {
		t.Fatalf("ожидались ДВА хвоста с тегом press (позиционный 0306 и парный 0325), получено %d", len(pressTails))
	}
	if opKindIsPairTail(pressTails[0]) {
		t.Error("первым обязан идти ПОЗИЦИОННЫЙ ВТО-хвост: он стоит левее media, парный — правее")
	}
	if !opKindIsPairTail(pressTails[1]) {
		t.Fatalf("второй хвост press не парной формы: %#v", pressTails[1])
	}
	if got, want := opKindPairKeys(t, pressTails[1]), []string{"press_action", "press_toward"}; len(got) != len(want) {
		t.Fatalf("пар в парном ВТО-хвосте %d, ожидалось %d: %v", len(got), len(want), got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("ключи парного ВТО-хвоста: %v, ожидалось %v", got, want)
			}
		}
	}
	// И то, ради чего вся форма: заполнение ПАРНОГО хвоста не трогает ПОЗИЦИОННЫЙ. Шаг с теми же
	// ВТО-фактами 0306 и без под-глагола обязан дать тот же позиционный хвост байт в байт.
	before := entity.TechCardOperation{
		OperationType:  entity.OpTypePress,
		PressEquipment: opGoldStr("iron"),
	}
	beforeTails := opKindTailsOf(t, before)
	if len(beforeTails) != 1 {
		t.Fatalf("у шага без под-глагола ожидался ровно один хвост, получено %d", len(beforeTails))
	}
	if opGoldJSON(t, beforeTails[0]) != opGoldJSON(t, pressTails[0]) {
		t.Errorf("позиционный ВТО-хвост поехал от появления под-глагола:\n до:    %s\n после: %s",
			opGoldJSON(t, beforeTails[0]), opGoldJSON(t, pressTails[0]))
	}
	// И обратное: сам под-глагол отпечаток обязан двигать — это новый факт цеха.
	plain := &entity.TechCardInsert{Operations: []entity.TechCardOperation{before}}
	withAction := &entity.TechCardInsert{Operations: []entity.TechCardOperation{op}}
	if digestOf(constructionProjection(plain)) == digestOf(constructionProjection(withAction)) {
		t.Error("«заутюжить на полочку» не сдвинуло отпечаток — подпись под шагом без стороны читалась бы как действительная под шагом со стороной")
	}
}

// TestPressTailAbsentOnEveryStoredRow — ОБЕЩАНИЕ НУЛЕВОЙ ВОЛНЫ, отдельной клеткой.
//
// Обе колонки 0325 рождаются NULL на КАЖДОЙ существующей строке, поэтому парный ВТО-хвост не
// рождается нигде, и байты кортежа сегодняшнего шага не двигаются. Проверяется прямо: у шага, где
// заполнены ВСЕ старые ВТО-факты и ни одной новой колонки, хвост с парами отсутствует.
func TestPressTailAbsentOnEveryStoredRow(t *testing.T) {
	op := entity.TechCardOperation{
		OperationType:     entity.OpTypePress,
		PressEquipment:    opGoldStr("steam_press"),
		PressProfileKey:   opGoldStr("press-1"),
		PressTemperatureC: opGoldI32(150),
		PressDwellSec:     opGoldI32(12),
		PressPressureNCm2: opGoldDec("3.5"),
		PressSteam:        sql.NullBool{Bool: false, Valid: true},
		PressCloth:        opGoldStr("cotton"),
	}
	for _, tag := range opKindPairTagsOf(t, op) {
		t.Errorf("у шага без единой колонки 0325 родился парный хвост %q — значит выкатка сдвинет отпечаток каждой ВТО-строки в базе", tag)
	}
}
