package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ИКОННЫЕ ПОЛЯ ШАГА (0436): seam_kind, work_variant, fastening.button_holes — правила записи,
// транспорт, пять списков и замороженный отпечаток.

// opIconStep — минимальный машинный шаг, который проходит все соседние правила.
func opIconStep(machine pb_common.TechCardMachineType) *pb_common.TechCardOperation {
	return &pb_common.TechCardOperation{
		OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		MachineType:   machine,
		Zone:          pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_OUTER,
	}
}

func parseOneIconStep(t *testing.T, o *pb_common.TechCardOperation) (entity.TechCardOperation, error) {
	t.Helper()
	ops, err := parseTechCardOperations([]*pb_common.TechCardOperation{o}, map[int]bool{}, 0, nil, nil, true)
	if err != nil {
		return entity.TechCardOperation{}, err
	}
	if len(ops) != 1 {
		t.Fatalf("ожидался один шаг, получено %d", len(ops))
	}
	return ops[0], nil
}

func wantIconViolation(t *testing.T, err error, field, reason string) {
	t.Helper()
	ve := workFieldViolation(t, err)
	if ve.Field != field || ve.Reason != reason {
		t.Fatalf("отказ %s/%s, ожидался %s/%s: %v", ve.Field, ve.Reason, field, reason, err)
	}
}

const (
	lockstitch    = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH
	buttonAttach  = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BUTTON_ATTACH
	zipperSetting = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_ZIPPER_SETTING
)

func withStitching(o *pb_common.TechCardOperation, class pb_common.TechCardSeamClass, kind string, binding pb_common.TechCardBindingStyle) *pb_common.TechCardOperation {
	o.SeamClass = class
	o.SeamKind = kind
	if binding != pb_common.TechCardBindingStyle_TECH_CARD_BINDING_STYLE_UNKNOWN {
		o.Stitching = &pb_common.TechCardOperationStitching{BindingStyle: binding}
	}
	return o
}

func TestSeamKindWriteRules(t *testing.T) {
	const (
		bound   = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_BS_BOUND
		plain   = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_SS_PLAIN
		felled  = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_LS_FLAT_FELLED
		noClass = pb_common.TechCardSeamClass_TECH_CARD_SEAM_CLASS_UNKNOWN
		raw     = pb_common.TechCardBindingStyle_TECH_CARD_BINDING_STYLE_RAW
		single  = pb_common.TechCardBindingStyle_TECH_CARD_BINDING_STYLE_SINGLE_FOLD
		double  = pb_common.TechCardBindingStyle_TECH_CARD_BINDING_STYLE_DOUBLE_FOLD
		noFold  = pb_common.TechCardBindingStyle_TECH_CARD_BINDING_STYLE_UNKNOWN
	)
	cases := []struct {
		name        string
		op          *pb_common.TechCardOperation
		field, code string // "" — проходит
		wantKind    string
	}{
		{name: "без вида: ноль правил, NULL", op: withStitching(opIconStep(lockstitch), noClass, "", noFold)},
		{name: "пробелы = не указано", op: withStitching(opIconStep(lockstitch), noClass, "   ", noFold)},
		{name: "вид своего класса", op: withStitching(opIconStep(lockstitch), felled, "lapped_closed_a", noFold), wantKind: "lapped_closed_a"},
		{name: "незнакомый токен", op: withStitching(opIconStep(lockstitch), plain, "bound_pink", noFold),
			field: "operations[0].seam_kind", code: "unknown_value"},
		{name: "вид без класса (наследование умолчания не засчитывается)", op: withStitching(opIconStep(lockstitch), noClass, "seam_open", noFold),
			field: "operations[0].seam_class", code: "needs_seam_class"},
		{name: "вид чужого класса", op: withStitching(opIconStep(lockstitch), plain, "felled", noFold),
			field: "operations[0].seam_class", code: "needs_seam_class"},
		{name: "bound_tape = raw", op: withStitching(opIconStep(lockstitch), bound, "bound_tape", raw), wantKind: "bound_tape"},
		{name: "bound_open = single_fold", op: withStitching(opIconStep(lockstitch), bound, "bound_open", single), wantKind: "bound_open"},
		{name: "bound_closed = double_fold", op: withStitching(opIconStep(lockstitch), bound, "bound_closed", double), wantKind: "bound_closed"},
		{name: "bound_frame без сложения", op: withStitching(opIconStep(lockstitch), bound, "bound_frame", noFold), wantKind: "bound_frame"},
		{name: "bound_open со сложением raw", op: withStitching(opIconStep(lockstitch), bound, "bound_open", raw),
			field: "operations[0].binding_style", code: "contradicts_seam_kind"},
		{name: "bound_closed без сложения", op: withStitching(opIconStep(lockstitch), bound, "bound_closed", noFold),
			field: "operations[0].binding_style", code: "contradicts_seam_kind"},
		{name: "bound_frame со сложением", op: withStitching(opIconStep(lockstitch), bound, "bound_frame", double),
			field: "operations[0].binding_style", code: "contradicts_seam_kind"},
		{name: "bs_bound без вида и со сложением — как было (F22 не тронут)", op: withStitching(opIconStep(lockstitch), bound, "", single)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseOneIconStep(t, c.op)
			if c.code != "" {
				wantIconViolation(t, err, c.field, c.code)
				return
			}
			if err != nil {
				t.Fatalf("разбор отказал там, где обязан пройти: %v", err)
			}
			if c.wantKind == "" {
				if got.SeamKind.Valid {
					t.Fatalf("вид не указан, а в колонку уехало %q", got.SeamKind.String)
				}
				return
			}
			if !got.SeamKind.Valid || got.SeamKind.String != c.wantKind {
				t.Fatalf("вид %#v, ожидался %q", got.SeamKind, c.wantKind)
			}
		})
	}
}

// opIconCatalog — тестовый каталог + две настоящие работы с вариантами (set_zip, fuse — токен,
// глагол, режим и машинка дословно из 0329).
func withIconWorkCatalog(t *testing.T) func() {
	t.Helper()
	if entity.OperationWorkCatalogSnapshot() != nil {
		t.Fatal("снимок каталога уже опубликован до начала теста")
	}
	works := append(testWorkCatalog(),
		entity.OperationWork{
			Token: "set_zip", Verb: "machine", Stage: "closures", Label: "Set zip",
			MachineMode: entity.OperationWorkMachineModeFixed, Machines: []string{"zipper_setting"},
		},
		entity.OperationWork{
			Token: "fuse", Verb: "fusing", Stage: "pressing", Label: "Fuse",
			MachineMode: entity.OperationWorkMachineModeNone,
		},
	)
	entity.SetOperationWorkCatalog(works)
	return func() { entity.SetOperationWorkCatalog(nil) }
}

func TestWorkVariantWriteRules(t *testing.T) {
	restore := withIconWorkCatalog(t)
	defer restore()

	zip := func(work, variant string) *pb_common.TechCardOperation {
		o := opIconStep(zipperSetting)
		o.Work, o.WorkVariant = work, variant
		return o
	}
	cases := []struct {
		name        string
		op          *pb_common.TechCardOperation
		field, code string
		want        string
	}{
		{name: "без варианта", op: zip("set_zip", "")},
		{name: "вариант своей работы", op: zip("set_zip", "open_end"), want: "open_end"},
		{name: "вариант чужой работы", op: zip("set_zip", "edge_tape"), field: "operations[0].work_variant", code: "not_for_work"},
		{name: "вариант без работы", op: zip("", "open_end"), field: "operations[0].work_variant", code: "needs_work"},
		{name: "у работы нет вариантов вовсе", op: func() *pb_common.TechCardOperation {
			o := opIconStep(lockstitch)
			o.Work, o.WorkVariant = "join_lockstitch", "open_end"
			return o
		}(), field: "operations[0].work_variant", code: "not_for_work"},
		{name: "ошибка работы называется по полю work, а не по варианту", op: zip("no_such_work", "open_end"),
			field: "operations[0].work", code: "unknown_work"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseOneIconStep(t, c.op)
			if c.code != "" {
				wantIconViolation(t, err, c.field, c.code)
				return
			}
			if err != nil {
				t.Fatalf("разбор отказал там, где обязан пройти: %v", err)
			}
			if c.want == "" {
				if got.WorkVariant.Valid {
					t.Fatalf("вариант не указан, а в колонку уехало %q", got.WorkVariant.String)
				}
				return
			}
			if !got.WorkVariant.Valid || got.WorkVariant.String != c.want {
				t.Fatalf("вариант %#v, ожидался %q", got.WorkVariant, c.want)
			}
		})
	}
}

func TestButtonHolesWriteRules(t *testing.T) {
	button := func(holes int32, pattern pb_common.TechCardButtonAttachPattern) *pb_common.TechCardOperation {
		o := opIconStep(buttonAttach)
		o.Fastening = &pb_common.TechCardOperationFastening{ButtonHoles: holes, AttachPattern: pattern}
		return o
	}
	const (
		cross    = pb_common.TechCardButtonAttachPattern_TECH_CARD_BUTTON_ATTACH_PATTERN_CROSS_X
		parallel = pb_common.TechCardButtonAttachPattern_TECH_CARD_BUTTON_ATTACH_PATTERN_PARALLEL
		square   = pb_common.TechCardButtonAttachPattern_TECH_CARD_BUTTON_ATTACH_PATTERN_SQUARE
		uShape   = pb_common.TechCardButtonAttachPattern_TECH_CARD_BUTTON_ATTACH_PATTERN_U_SHAPE
		other    = pb_common.TechCardButtonAttachPattern_TECH_CARD_BUTTON_ATTACH_PATTERN_OTHER
		none     = pb_common.TechCardButtonAttachPattern_TECH_CARD_BUTTON_ATTACH_PATTERN_UNKNOWN
	)
	cases := []struct {
		name        string
		op          *pb_common.TechCardOperation
		field, code string
		want        int32
	}{
		{name: "0 = не указано", op: button(0, cross)},
		{name: "4 и крест", op: button(4, cross), want: 4},
		{name: "4 без рисунка", op: button(4, none), want: 4},
		{name: "2 и параллельно", op: button(2, parallel), want: 2},
		{name: "2 и другое", op: button(2, other), want: 2},
		{name: "2 и крест", op: button(2, cross), field: "operations[0].attach_pattern", code: "needs_four_holes"},
		{name: "2 и квадрат", op: button(2, square), field: "operations[0].attach_pattern", code: "needs_four_holes"},
		{name: "2 и П", op: button(2, uShape), field: "operations[0].attach_pattern", code: "needs_four_holes"},
		{name: "3 отверстия", op: button(3, none), field: "operations[0].button_holes", code: "unknown_value"},
		{name: "минус", op: button(-2, none), field: "operations[0].button_holes", code: "unknown_value"},
		{name: "не на пуговичной машинке", op: func() *pb_common.TechCardOperation {
			o := opIconStep(lockstitch)
			o.Fastening = &pb_common.TechCardOperationFastening{ButtonHoles: 4}
			return o
		}(), field: "operations[0].button_holes", code: "not_applicable"},
		{name: "не на машинном шаге", op: &pb_common.TechCardOperation{
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_HANDWORK,
			Zone:          pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_OUTER,
			Fastening:     &pb_common.TechCardOperationFastening{ButtonHoles: 4},
		}, field: "operations[0].button_holes", code: "not_applicable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseOneIconStep(t, c.op)
			if c.field != "" {
				ve := workFieldViolation(t, err)
				if ve.Field != c.field || (c.code != "" && ve.Reason != c.code) {
					t.Fatalf("отказ %s/%s, ожидался %s/%s", ve.Field, ve.Reason, c.field, c.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("разбор отказал там, где обязан пройти: %v", err)
			}
			if c.want == 0 {
				if got.ButtonHoles.Valid {
					t.Fatalf("0 обязан храниться NULL, уехало %d", got.ButtonHoles.Int32)
				}
				return
			}
			if !got.ButtonHoles.Valid || got.ButtonHoles.Int32 != c.want {
				t.Fatalf("отверстия %#v, ожидалось %d", got.ButtonHoles, c.want)
			}
		})
	}
}

// TestOperationIconFieldsRoundTrip — сущность → провод → сущность: клон сезона строит payload в
// techCardOperationsToPb, и поле, которое туда не эмитится, клон стирал бы молча.
func TestOperationIconFieldsRoundTrip(t *testing.T) {
	restore := withIconWorkCatalog(t)
	defer restore()

	in := []entity.TechCardOperation{
		{
			OperationType: entity.OpTypeMachine, MachineType: opGoldStr("lockstitch"), Zone: entity.ZoneOuter,
			SeamClass: opGoldStr("bs_bound"), BindingStyle: opGoldStr("double_fold"), SeamKind: opGoldStr("bound_closed"),
		},
		{
			OperationType: entity.OpTypeMachine, MachineType: opGoldStr("button_attach"), Zone: entity.ZoneOuter,
			AttachPattern: opGoldStr("parallel"), ButtonHoles: opGoldI32(2),
		},
		{
			OperationType: entity.OpTypeMachine, MachineType: opGoldStr("zipper_setting"), Zone: entity.ZoneOuter,
			Work: opGoldStr("set_zip"), WorkVariant: opGoldStr("two_way"),
		},
	}
	pbs := techCardOperationsToPb(in)
	if pbs[0].GetSeamKind() != "bound_closed" || pbs[1].GetFastening().GetButtonHoles() != 2 || pbs[2].GetWorkVariant() != "two_way" {
		t.Fatalf("иконные поля не эмитятся: %v / %v / %v", pbs[0].GetSeamKind(), pbs[1].GetFastening(), pbs[2].GetWorkVariant())
	}
	out, err := parseTechCardOperations(pbs, map[int]bool{}, 0, nil, nil, true)
	if err != nil {
		t.Fatalf("собственный payload сервера не разобрался: %v", err)
	}
	if out[0].SeamKind != in[0].SeamKind || out[1].ButtonHoles != in[1].ButtonHoles || out[2].WorkVariant != in[2].WorkVariant {
		t.Fatalf("круг потерял значения: %#v %#v %#v", out[0].SeamKind, out[1].ButtonHoles, out[2].WorkVariant)
	}
	// Блок fastening, в котором заполнены ТОЛЬКО отверстия, обязан эмититься: иначе шаг «2 отверстия,
	// рисунок не указан» терял бы их на клоне.
	only := operationFasteningToPb(entity.TechCardOperation{ButtonHoles: opGoldI32(4)})
	if only.GetButtonHoles() != 4 {
		t.Fatalf("блок fastening с одними отверстиями не эмитится: %v", only)
	}
}

// TestOperationIconFieldsAreInAllFiveLists — entity, INSERT, SELECT (миграция — в migrationlint) и
// хвост дайджеста; плюс симметрия записи и чтения.
func TestOperationIconFieldsAreInAllFiveLists(t *testing.T) {
	insert, sel := operationInsertColumns(t), operationSelectColumns(t)
	cols := map[string]bool{}
	for _, c := range operationDBColumns() {
		cols[c] = true
	}
	for _, col := range []string{"seam_kind", "work_variant", "button_holes"} {
		if !cols[col] {
			t.Errorf("у entity.TechCardOperation нет поля db:%q", col)
		}
		if !insert[col] {
			t.Errorf("%q нет среди ключей insertTechCardOperations — значение не пишется", col)
		}
		if !sel[col] {
			t.Errorf("%q нет в techCardOperationsQuery — значение пишется, но не читается", col)
		}
	}
	op := opIconGoldOps()
	for i := range op {
		read := readBackAsSelectWould(t, op[i], sel)
		if digestOf(constructionProjection(&entity.TechCardInsert{Operations: []entity.TechCardOperation{op[i]}})) !=
			digestOf(constructionProjection(&entity.TechCardInsert{Operations: []entity.TechCardOperation{read}})) {
			t.Errorf("шаг %d: отпечаток записи и чтения разошлись", i)
		}
	}
}

// opIconGoldOps — карточка hex-эталона иконных полей: окантовка с видом, пуговица с отверстиями,
// молния с работой и вариантом (два хвоста — fastening и work — чтобы место пары было в байтах).
//
// ОТДЕЛЬНОЙ КАРТОЧКОЙ, а не допиской в opGoldCases / opWorkNextToFastening — довод тот же, что у
// opGoldWorkDigestHex: неподвижность тех литералов и есть доказательство «волна нулевая».
func opIconGoldOps() []entity.TechCardOperation {
	return []entity.TechCardOperation{
		{
			OperationNumber: opGoldI32(40), OperationType: entity.OpTypeMachine, MachineType: opGoldStr("lockstitch"),
			Zone: entity.ZoneOuter, SeamClass: opGoldStr("bs_bound"), BindingStyle: opGoldStr("single_fold"),
			SeamKind: opGoldStr("bound_open"),
		},
		{
			OperationNumber: opGoldI32(50), OperationType: entity.OpTypeMachine, MachineType: opGoldStr("button_attach"),
			Zone: entity.ZoneOuter, AttachPattern: opGoldStr("parallel"), ButtonHoles: opGoldI32(2),
		},
		{
			OperationNumber: opGoldI32(60), OperationType: entity.OpTypeMachine, MachineType: opGoldStr("zipper_setting"),
			Zone: entity.ZoneOuter, ZipperApplication: opGoldStr("centered"),
			Work: opGoldStr("set_zip"), WorkVariant: opGoldStr("open_end"),
		},
	}
}

// opGoldIconFieldsDigestHex — ЧЕТВЁРТЫЙ ЭТАЛОН, НОВЫЙ. Посчитан НЕЗАВИСИМО от кода: sha256 от
// компактного JSON [null,[<три кортежа ниже>],[]] сторонним скриптом, тем же, что воспроизводит
// opGoldWorkDigestHex байт-в-байт. Правка литерала законна только вместе с подсчётом волны
// пере-утверждения по tech_card_signoff.
const opGoldIconFieldsDigestHex = "18947e3c40cd878d5d90369d6e59345ea4aa0b9e744148db410e1b3c19d5ec57"

func TestOperationIconFieldsTuplesGolden(t *testing.T) {
	head := func(n int, machine, class string) []any {
		return []any{n, machine, "outer", nil, nil, "", 0, "", class, "", "", "", 0, "", "", ""}
	}
	want := [][]any{
		append(head(40, "lockstitch", "bs_bound"), []any{"stitching", []any{
			[]any{"binding_style", "single_fold"},
			[]any{"seam_kind", "bound_open"},
		}}),
		append(head(50, "button_attach", ""), []any{"fastening", []any{
			[]any{"attach_pattern", "parallel"},
			[]any{"button_holes", 2},
		}}),
		// zipper_setting без legacy-близнеца: позиция 2 несёт "machine", машинка уезжает в хвост.
		append(head(60, "machine", ""),
			[]any{"machine", []any{[]any{"machine_type", "zipper_setting"}}},
			[]any{"fastening", []any{[]any{"zipper_application", "centered"}}},
			[]any{"work", []any{
				[]any{"work", "set_zip"},
				[]any{"work_variant", "open_end"},
			}}),
	}
	for i, op := range opIconGoldOps() {
		got := opGoldJSON(t, opGoldProject(t, op))
		if got != opGoldJSON(t, want[i]) {
			t.Errorf("кортеж шага %d сдвинулся:\n--- эталон ---\n%s\n--- сейчас ---\n%s", i, opGoldJSON(t, want[i]), got)
		}
	}
}

func TestOperationIconFieldsDigestHexFrozen(t *testing.T) {
	got := digestOf(constructionProjection(&entity.TechCardInsert{Operations: opIconGoldOps()}))
	if got != opGoldIconFieldsDigestHex {
		t.Errorf("отпечаток иконных полей поехал.\n--- эталон ---\n%s\n--- сейчас ---\n%s", opGoldIconFieldsDigestHex, got)
	}
}

// TestOperationIconFieldsBornNullMoveNoTail — нулевая волна как правило: NULL-поля не рождают ни
// пары, ни хвоста. Шаг без единого факта волны 0324 остаётся без хвостов вовсе.
func TestOperationIconFieldsBornNullMoveNoTail(t *testing.T) {
	op := entity.TechCardOperation{OperationType: entity.OpTypeMachine, MachineType: opGoldStr("lockstitch"), Zone: entity.ZoneOuter}
	if tags := opKindTagsOf(t, op); len(tags) != 0 {
		t.Fatalf("шаг без фактов отрастил хвосты %v", tags)
	}
	op.SeamKind, op.ButtonHoles, op.WorkVariant = sql.NullString{}, sql.NullInt32{}, sql.NullString{}
	op.Work = opGoldStr("topstitch")
	tags := opKindTagsOf(t, op)
	if len(tags) != 1 || tags[0] != "work" {
		t.Fatalf("работа без варианта: ожидался один хвост work, получено %v", tags)
	}
}

// TestMaterialHardwareButtonHoles — отверстия пуговицы-артикула (0436): 0 = NULL, 2 | 4 проходят и
// возвращаются на провод, всё прочее — отказ с именем поля.
func TestMaterialHardwareButtonHoles(t *testing.T) {
	for _, c := range []struct {
		holes int32
		ok    bool
	}{{0, true}, {2, true}, {4, true}, {1, false}, {3, false}, {6, false}, {-4, false}} {
		got, err := pbHardwareAttrs(&pb_common.MaterialHardwareAttrs{ButtonHoles: c.holes})
		if !c.ok {
			ve := workFieldViolation(t, err)
			if ve.Field != "hardware_attrs.button_holes" || ve.Reason != "unknown_value" {
				t.Fatalf("%d отверстий: отказ %s/%s", c.holes, ve.Field, ve.Reason)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%d отверстий: отказ %v", c.holes, err)
		}
		if got.ButtonHoles.Valid != (c.holes != 0) || got.ButtonHoles.Int32 != c.holes {
			t.Fatalf("%d отверстий: в колонку уехало %#v", c.holes, got.ButtonHoles)
		}
		var m entity.MaterialWithPrice
		m.MaterialClass = string(entity.MaterialClassHardware)
		m.HardwareAttr = got
		if back := ConvertEntityMaterialToPb(m).GetHardwareAttrs().GetButtonHoles(); back != c.holes {
			t.Fatalf("%d отверстий: на провод вернулось %d", c.holes, back)
		}
	}
}
