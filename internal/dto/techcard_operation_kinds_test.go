package dto

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// ВИДЫ ОПЕРАЦИЙ (волна 0324) — тесты разбора и валидации.
//
// Три вещи здесь проверяются ОТДЕЛЬНО друг от друга, потому что ломаются они по-разному:
//   - круг pb → entity → pb → entity: заполненное переживает, пустое остаётся пустым;
//   - неосведомлённая запись: payload без единого нового поля сохраняется ровно как раньше, и
//     отпечаток CONSTRUCTION не двигается — это цена, которую волна обязана НЕ брать;
//   - отказы: не только «отказал», но КАКОЕ ПОЛЕ названо. Путь в FieldViolation — плоский, и
//     админка пришпиливает нарушение на контрол именно по нему; путь, который не попал, тихо
//     вырождается в неатрибутируемый тост.

const (
	opTypeMachineNew = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE
	opTypeHardware   = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_HARDWARE_SET
	opTypePrint      = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRINT
	opTypeTrimNew    = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_TRIM
	opTypeThreadTrim = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_THREAD_TRIM
	opTypeClean      = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_CLEAN
	opTypeInspect    = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_INSPECT
	opTypeFold       = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FOLD
	opTypePack       = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PACK
	opTypeWet        = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_WET_PROCESS

	// ВТО-глаголы (0325). PRESS_OPEN ЖИВОЙ и НЕ ТРОГАЕТСЯ: разутюжка стала одним из значений
	// press_action, но каноническая запись остаётся глаголом.
	opTypePress     = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS
	opTypePressOpen = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS_OPEN
	opTypeFusing    = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_FUSING

	mtButtonhole   = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BUTTONHOLE
	mtButtonAttach = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BUTTON_ATTACH
	mtBartack      = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BARTACK
	mtBinding      = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BINDING_TAPING
	mtZipper       = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_ZIPPER_SETTING
	mtSeamTaping   = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_SEAM_TAPING
	mtUltrasonic   = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_ULTRASONIC_WELDER
	mtOverlock     = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK

	// zone = other — ЗАКОННЫЙ ответ финишных глаголов: «нитки по всему изделию» области не имеет.
	zoneClosure = pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_CLOSURE
	zoneOther   = pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_OTHER
)

func kindCard(ops ...*pb_common.TechCardOperation) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{StyleNumber: "OK-1", Name: "Jacket", Operations: ops}
}

func kindParse(t *testing.T, ops ...*pb_common.TechCardOperation) *entity.TechCardInsert {
	t.Helper()
	got, err := ConvertPbTechCardInsertToEntity(kindCard(ops...))
	if err != nil {
		t.Fatalf("разбор отказал там, где обязан был пройти: %v", err)
	}
	return got
}

// kindRefusal требует ИМЕННО поле-тегированного отказа: голая ошибка доедет до админки строкой без
// поля, и оператор увидит тост вместо подсветки контрола.
func kindRefusal(t *testing.T, ops ...*pb_common.TechCardOperation) *entity.ValidationError {
	t.Helper()
	_, err := ConvertPbTechCardInsertToEntity(kindCard(ops...))
	if err == nil {
		t.Fatalf("ожидался отказ, разбор прошёл")
	}
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("ожидался *entity.ValidationError с полем, получено %T: %v", err, err)
	}
	return ve
}

func kindEmit(t *testing.T, ins *entity.TechCardInsert) *pb_common.TechCardInsert {
	t.Helper()
	out := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *ins}, CostingFx{})
	if out == nil || out.TechCard == nil {
		t.Fatalf("эмиссия вернула пустую карточку")
	}
	return out.TechCard
}

// kindFacts — все тридцать две колонки волны одной картой «колонка → значение», пустое значение =
// NULL. Порядок здесь не важен (карта), важна ПОЛНОТА: поле, забытое в этом списке, — поле, круг
// которого никто не проверяет.
func kindFacts(o entity.TechCardOperation) map[string]string {
	s := func(v sql.NullString) string {
		if v.Valid {
			return v.String
		}
		return ""
	}
	i := func(v sql.NullInt32) string {
		if v.Valid {
			return fmt.Sprint(v.Int32)
		}
		return ""
	}
	d := func(v decimal.NullDecimal) string {
		if v.Valid {
			return v.Decimal.String()
		}
		return ""
	}
	return map[string]string{
		"needle_count":    i(o.NeedleCount),
		"needle_gauge_mm": d(o.NeedleGaugeMm),
		"seam_securing":   s(o.SeamSecuring),
		"row_spacing_mm":  d(o.RowSpacingMm),
		"fullness_ratio":  d(o.FullnessRatio),

		"placement_count": i(o.PlacementCount),
		"pitch_mm":        d(o.PitchMm),

		"attach_method":      s(o.AttachMethod),
		"hole_prep":          s(o.HolePrep),
		"reinforcement":      s(o.Reinforcement),
		"foldback_mm":        d(o.FoldbackMm),
		"cycle_stitch_count": i(o.CycleStitchCount),

		"print_method":     s(o.PrintMethod),
		"peel_mode":        s(o.PeelMode),
		"second_press_sec": i(o.SecondPressSec),

		"air_temperature_c": i(o.AirTemperatureC),
		"feed_speed_m_min":  d(o.FeedSpeedMMin),

		"trim_action":           s(o.TrimAction),
		"residual_allowance_mm": d(o.ResidualAllowanceMm),

		"residual_tail_max_mm": d(o.ResidualTailMaxMm),

		"cleaning_kind":    s(o.CleaningKind),
		"coverage_mode":    s(o.CoverageMode),
		"wet_process_kind": s(o.WetProcessKind),

		"buttonhole_style":       s(o.ButtonholeStyle),
		"cut_length_mm":          d(o.CutLengthMm),
		"buttonhole_orientation": s(o.ButtonholeOrientation),
		"bartack_length_mm":      d(o.BartackLengthMm),
		"attach_pattern":         s(o.AttachPattern),
		"zipper_application":     s(o.ZipperApplication),
		"binding_style":          s(o.BindingStyle),
		"label_attach_stitch":    s(o.LabelAttachStitch),

		// ВТО (0325) — две колонки сверх тридцати двух, в том же списке: круг у них тот же.
		"press_action": s(o.PressAction),
		"press_toward": s(o.PressToward),
	}
}

// ── 1. КРУГ ─────────────────────────────────────────────────────────────────────────────────────

// kindRoundTripOps — по шагу на семейство, все поля заполнены. Один шаг всё семейство сразу не
// вмещает: binding_style, attach_pattern и zipper_application живут каждое на своей машинке, и
// собрать их в одну строку значило бы проверить круг на карточке, которую валидация обязана
// отвергнуть.
func kindRoundTripOps() []*pb_common.TechCardOperation {
	return []*pb_common.TechCardOperation{
		{ // 0: петельный автомат — строчка + петля + цикловая часть фурнитуры + раскладка
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtButtonhole,
			Stitching: &pb_common.TechCardOperationStitching{
				NeedleCount:       2,
				NeedleGaugeMm:     dec("6.4"),
				SeamSecuring:      pb_common.TechCardSeamSecuring_TECH_CARD_SEAM_SECURING_CONDENSED,
				RowSpacingMm:      dec("5.5"),
				FullnessRatio:     dec("1.25"),
				LabelAttachStitch: pb_common.TechCardLabelAttachStitch_TECH_CARD_LABEL_ATTACH_STITCH_FOUR_SIDES,
			},
			PlacementLayout: &pb_common.TechCardOperationPlacement{Count: 6, PitchMm: dec("80.5")},
			Hardware: &pb_common.TechCardOperationHardware{
				HolePrep:         pb_common.TechCardHolePrep_TECH_CARD_HOLE_PREP_PUNCH,
				Reinforcement:    pb_common.TechCardReinforcement_TECH_CARD_REINFORCEMENT_PATCH,
				CycleStitchCount: 42,
			},
			Fastening: &pb_common.TechCardOperationFastening{
				ButtonholeStyle:       pb_common.TechCardButtonholeStyle_TECH_CARD_BUTTONHOLE_STYLE_EYELET,
				CutLengthMm:           dec("25.4"),
				ButtonholeOrientation: pb_common.TechCardButtonholeOrientation_TECH_CARD_BUTTONHOLE_ORIENTATION_HORIZONTAL,
				BartackLengthMm:       dec("8.5"),
			},
		},
		{ // 1: окантовочная — единственное место binding_style
			OperationType: opTypeMachineNew, Zone: zoneHem, MachineType: mtBinding,
			Stitching: &pb_common.TechCardOperationStitching{
				BindingStyle: pb_common.TechCardBindingStyle_TECH_CARD_BINDING_STYLE_DOUBLE_FOLD,
			},
		},
		{ // 2: пуговичный автомат
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtButtonAttach,
			Fastening: &pb_common.TechCardOperationFastening{
				AttachPattern: pb_common.TechCardButtonAttachPattern_TECH_CARD_BUTTON_ATTACH_PATTERN_CROSS_X,
			},
		},
		{ // 3: молния
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtZipper,
			Fastening: &pb_common.TechCardOperationFastening{
				ZipperApplication: pb_common.TechCardZipperApplication_TECH_CARD_ZIPPER_APPLICATION_LAPPED,
			},
		},
		{ // 4: проклейка шва — горячий воздух живёт только здесь
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtSeamTaping,
			Weld: &pb_common.TechCardOperationWeld{AirTemperatureC: 450, FeedSpeedMMin: dec("1.5")},
		},
		{ // 5: ультразвук — воздуха нет, скорость есть
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtUltrasonic,
			Weld: &pb_common.TechCardOperationWeld{FeedSpeedMMin: dec("0.3")},
		},
		{ // 6: установка фурнитуры целиком
			OperationType: opTypeHardware, Zone: zoneClosure,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod:     pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_THREADED,
				HolePrep:         pb_common.TechCardHolePrep_TECH_CARD_HOLE_PREP_NONE,
				Reinforcement:    pb_common.TechCardReinforcement_TECH_CARD_REINFORCEMENT_NONE,
				FoldbackMm:       dec("40.5"),
				CycleStitchCount: 16,
			},
			PlacementLayout: &pb_common.TechCardOperationPlacement{Count: 2, PitchMm: dec("120")},
		},
		{ // 7: печать — вместе с ВТО-блоком, который при PRINT легален. Метод ПЛЁНОЧНЫЙ, а не
			// шелкография: 0327 отвергает peel_mode при screen (носителя у неё нет вовсе), и на
			// шелкографии круг не проверил бы съём носителя ни разу.
			OperationType: opTypePrint, Zone: zoneOuter,
			PrintMethod: pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_HEAT_TRANSFER,
			Print: &pb_common.TechCardOperationPrint{
				PeelMode:       pb_common.TechCardPeelMode_TECH_CARD_PEEL_MODE_HOT,
				SecondPressSec: 5,
			},
			PlacementLayout:   &pb_common.TechCardOperationPlacement{Count: 1},
			PressEquipment:    pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_PRESS,
			PressTemperatureC: 160,
			PressCloth:        pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_SILICONE_PAPER,
		},
		{ // 8: подрезка
			OperationType: opTypeTrimNew, Zone: zoneCollar,
			Trim: &pb_common.TechCardOperationTrim{
				Action:              pb_common.TechCardTrimAction_TECH_CARD_TRIM_ACTION_GRADE_LAYERS,
				ResidualAllowanceMm: dec("3.5"),
			},
		},
		{ // 9: чистка концов ниток
			OperationType: opTypeThreadTrim, Zone: zoneOther,
			ThreadTrim: &pb_common.TechCardOperationThreadTrim{ResidualTailMaxMm: dec("2.5")},
		},
		{ // 10: чистка изделия
			OperationType: opTypeClean, Zone: zoneOther,
			Clean: &pb_common.TechCardOperationClean{
				Kind: pb_common.TechCardCleaningKind_TECH_CARD_CLEANING_KIND_SPOT_CLEAN,
			},
		},
		{ // 11: контроль
			OperationType: opTypeInspect, Zone: zoneOther,
			Inspect: &pb_common.TechCardOperationInspect{
				CoverageMode: pb_common.TechCardInspectCoverage_TECH_CARD_INSPECT_COVERAGE_SAMPLE_PER_BUNDLE,
			},
		},
		{ // 12: мокрая обработка
			OperationType: opTypeWet, Zone: zoneOther,
			WetProcessKind: pb_common.TechCardWetProcessKind_TECH_CARD_WET_PROCESS_KIND_GARMENT_DYE,
		},
		// 13, 14: глаголы БЕЗ единого поля шага — они обязаны сохраняться так же, как и остальные.
		{OperationType: opTypeFold, Zone: zoneOther},
		{OperationType: opTypePack, Zone: zoneOther},
		{ // 15: ВТО с под-глаголом и стороной (0325) — вместе с ВТО-блоком 0306
			OperationType: opTypePress, Zone: zoneOuter,
			Press: &pb_common.TechCardOperationPress{
				Action: pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_TO_ONE_SIDE,
				Toward: pb_common.TechCardPressToward_TECH_CARD_PRESS_TOWARD_AWAY_FROM_CENTER,
			},
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON,
		},
		{ // 16: разутюжка — ГЛАГОЛОМ и только им; второе написание снято 0327
			OperationType: opTypePressOpen, Zone: zoneOuter,
			PressEquipment: pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON,
		},
	}
}

// TestOperationKindsRoundTrip — заполненное семейство переживает круг pb → entity → pb → entity.
// Круг, а не разбор: клон сезона строит payload именно эмиссией, и поле, которое забыли эмитить,
// исчезает там молча — без единой ошибки и без единого отказа.
func TestOperationKindsRoundTrip(t *testing.T) {
	ops := kindRoundTripOps()
	first := kindParse(t, ops...)
	second, err := ConvertPbTechCardInsertToEntity(kindEmit(t, first))
	if err != nil {
		t.Fatalf("повторный разбор эмитированного payload'а: %v", err)
	}
	if len(first.Operations) != len(ops) || len(second.Operations) != len(ops) {
		t.Fatalf("шаги потерялись на круге: %d -> %d", len(first.Operations), len(second.Operations))
	}
	for i := range ops {
		before, after := kindFacts(first.Operations[i]), kindFacts(second.Operations[i])
		for col, want := range before {
			if got := after[col]; got != want {
				t.Errorf("шаг %d: %s не пережил круг: %q -> %q", i, col, want, got)
			}
		}
	}

	// Точечно — по одному факту на семейство, чтобы «пережило круг» не означало «одинаково пусто».
	want := map[int]map[string]string{
		0:  {"needle_count": "2", "needle_gauge_mm": "6.4", "seam_securing": "condensed", "row_spacing_mm": "5.5", "fullness_ratio": "1.25", "label_attach_stitch": "four_sides", "placement_count": "6", "pitch_mm": "80.5", "hole_prep": "punch", "reinforcement": "patch", "cycle_stitch_count": "42", "buttonhole_style": "eyelet", "cut_length_mm": "25.4", "buttonhole_orientation": "horizontal", "bartack_length_mm": "8.5"},
		1:  {"binding_style": "double_fold"},
		2:  {"attach_pattern": "cross_x"},
		3:  {"zipper_application": "lapped"},
		4:  {"air_temperature_c": "450", "feed_speed_m_min": "1.5"},
		5:  {"feed_speed_m_min": "0.3", "air_temperature_c": ""},
		6:  {"attach_method": "threaded", "hole_prep": "none", "reinforcement": "none", "foldback_mm": "40.5", "cycle_stitch_count": "16", "placement_count": "2", "pitch_mm": "120"},
		7:  {"print_method": "heat_transfer", "peel_mode": "hot", "second_press_sec": "5", "placement_count": "1"},
		8:  {"trim_action": "grade_layers", "residual_allowance_mm": "3.5"},
		9:  {"residual_tail_max_mm": "2.5"},
		10: {"cleaning_kind": "spot_clean"},
		11: {"coverage_mode": "sample_per_bundle"},
		12: {"wet_process_kind": "garment_dye"},
		15: {"press_action": "to_one_side", "press_toward": "away_from_center"},
		16: {"press_action": "", "press_toward": ""},
	}
	for i, cols := range want {
		facts := kindFacts(second.Operations[i])
		for col, v := range cols {
			if facts[col] != v {
				t.Errorf("шаг %d: %s = %q, ожидалось %q", i, col, facts[col], v)
			}
		}
	}
	// ВТО-факты шага печати доехали тем же кругом — блок при PRINT легален, а не терпим.
	if p := second.Operations[7]; p.PressEquipment.String != "press" || p.PressTemperatureC.Int32 != 160 ||
		p.PressCloth.String != "silicone_paper" {
		t.Errorf("ВТО-блок шага печати не пережил круг: %+v", p)
	}
}

// TestOperationKindsAbsentBlocksStayAbsent — UNKNOWN и отсутствующий блок доезжают до NULL и
// возвращаются ОТСУТСТВУЮЩИМИ. Всегда присутствующая обёртка читалась бы клиентом как «про это
// семейство кто-то думал» на каждом шаге, где про него не думал никто.
func TestOperationKindsAbsentBlocksStayAbsent(t *testing.T) {
	ins := kindParse(t,
		&pb_common.TechCardOperation{OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock},
		// Пустые блоки и UNKNOWN-энумы: бандл говорит «полей нет», а не «поля пустые».
		&pb_common.TechCardOperation{
			OperationType:   opTypeMachineNew,
			Zone:            zoneOuter,
			MachineType:     mtOverlock,
			Stitching:       &pb_common.TechCardOperationStitching{},
			PlacementLayout: &pb_common.TechCardOperationPlacement{},
			Fastening:       &pb_common.TechCardOperationFastening{},
		},
	)
	for i, op := range ins.Operations {
		for col, v := range kindFacts(op) {
			if v != "" {
				t.Errorf("шаг %d: %s = %q, а пустой блок обязан давать NULL", i, col, v)
			}
		}
	}
	out := kindEmit(t, ins)
	for i, op := range out.Operations {
		if op.Stitching != nil || op.PlacementLayout != nil || op.Hardware != nil || op.Print != nil ||
			op.Weld != nil || op.Trim != nil || op.ThreadTrim != nil || op.Clean != nil ||
			op.Inspect != nil || op.Fastening != nil {
			t.Errorf("шаг %d: пустой блок эмитирован обёрткой вместо отсутствия", i)
		}
		if op.PrintMethod != pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_UNKNOWN ||
			op.WetProcessKind != pb_common.TechCardWetProcessKind_TECH_CARD_WET_PROCESS_KIND_UNKNOWN {
			t.Errorf("шаг %d: NULL-дискриминатор уехал не как UNKNOWN", i)
		}
	}
}

// ── 2. РЕГРЕССИЯ НЕОСВЕДОМЛЁННОЙ ЗАПИСИ ─────────────────────────────────────────────────────────

// TestOperationKindsUnawarePayloadUnchanged — payload БЕЗ единого поля волны сохраняется ровно
// как раньше: все 32 колонки NULL, эмиссия не добавляет ни одного блока, а отпечаток CONSTRUCTION
// не двигается ни на бит. Это и есть обещание «нулевой волны протухших подписей» — не рассуждением,
// а сравнением hex.
func TestOperationKindsUnawarePayloadUnchanged(t *testing.T) {
	legacy := []*pb_common.TechCardOperation{
		{
			OperationType: opTypeLock, // легаси-глагол: бандл до раскола осей
			Zone:          zoneOuter,
			StitchesPerCm: dec("4.5"),
			Smv:           dec("1.2"),
			Topstitch: &pb_common.TechCardTopstitch{
				Mode:    pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_EDGE,
				WidthMm: dec("6"),
				Rows:    2,
			},
			Note: "как раньше",
		},
		{OperationType: opTypeLock, Zone: zoneCollar},
	}
	ins := kindParse(t, legacy...)
	for i, op := range ins.Operations {
		for col, v := range kindFacts(op) {
			if v != "" {
				t.Fatalf("шаг %d: старый payload записал %s = %q — неосведомлённая запись обязана "+
					"оставлять все колонки волны NULL", i, col, v)
			}
		}
	}
	before := digestOf(constructionProjection(ins))

	out := kindEmit(t, ins)
	for i, op := range out.Operations {
		if op.Stitching != nil || op.PlacementLayout != nil || op.Hardware != nil || op.Print != nil ||
			op.Weld != nil || op.Trim != nil || op.ThreadTrim != nil || op.Clean != nil ||
			op.Inspect != nil || op.Fastening != nil ||
			op.PrintMethod != pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_UNKNOWN ||
			op.WetProcessKind != pb_common.TechCardWetProcessKind_TECH_CARD_WET_PROCESS_KIND_UNKNOWN {
			t.Errorf("шаг %d: эмиссия дописала волновые факты в карточку, которая их не знает", i)
		}
	}
	again, err := ConvertPbTechCardInsertToEntity(out)
	if err != nil {
		t.Fatalf("повторный разбор: %v", err)
	}
	if after := digestOf(constructionProjection(again)); after != before {
		t.Errorf("отпечаток CONSTRUCTION сдвинулся на круге неосведомлённой карточки:\n%s\n%s", before, after)
	}
}

// ── 3. NOT_APPLICABLE ПО ГЛАГОЛУ ────────────────────────────────────────────────────────────────

// TestOperationKindsNotApplicableByVerb — матрица «чужой глагол × блок». Отказ обязан назвать
// КОНКРЕТНОЕ заполненное поле плоским путём, а не жестикулировать блоком.
func TestOperationKindsNotApplicableByVerb(t *testing.T) {
	cases := []struct {
		name  string
		op    *pb_common.TechCardOperation
		field string
	}{
		{"строчка на подрезке", &pb_common.TechCardOperation{
			OperationType: opTypeTrimNew, Zone: zoneOuter,
			Trim:      &pb_common.TechCardOperationTrim{Action: pb_common.TechCardTrimAction_TECH_CARD_TRIM_ACTION_TRIM_EVEN},
			Stitching: &pb_common.TechCardOperationStitching{NeedleCount: 2},
		}, "operations[0].needle_count"},
		{"раскладка на чистке", &pb_common.TechCardOperation{
			OperationType: opTypeClean, Zone: zoneOther,
			Clean:           &pb_common.TechCardOperationClean{Kind: pb_common.TechCardCleaningKind_TECH_CARD_CLEANING_KIND_DUST_LINT},
			PlacementLayout: &pb_common.TechCardOperationPlacement{Count: 3},
		}, "operations[0].placement_count"},
		{"фурнитура на печати", &pb_common.TechCardOperation{
			OperationType: opTypePrint, Zone: zoneOuter,
			PrintMethod: pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_DTF,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod: pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_SEW,
			},
		}, "operations[0].attach_method"},
		{"фурнитура на обычной машинке", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			Hardware: &pb_common.TechCardOperationHardware{
				HolePrep: pb_common.TechCardHolePrep_TECH_CARD_HOLE_PREP_PUNCH,
			},
		}, "operations[0].hole_prep"},
		{"метод печати на машинке", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			PrintMethod: pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_FOIL,
		}, "operations[0].print_method"},
		{"блок печати на машинке", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			Print: &pb_common.TechCardOperationPrint{SecondPressSec: 4},
		}, "operations[0].second_press_sec"},
		{"сварка на ВТО", &pb_common.TechCardOperation{
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS, Zone: zoneOuter,
			Weld: &pb_common.TechCardOperationWeld{AirTemperatureC: 400},
		}, "operations[0].air_temperature_c"},
		{"подрезка на машинке", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			Trim: &pb_common.TechCardOperationTrim{ResidualAllowanceMm: dec("3")},
		}, "operations[0].residual_allowance_mm"},
		{"хвост нитки на чистке изделия", &pb_common.TechCardOperation{
			OperationType: opTypeClean, Zone: zoneOther,
			Clean:      &pb_common.TechCardOperationClean{Kind: pb_common.TechCardCleaningKind_TECH_CARD_CLEANING_KIND_DUST_LINT},
			ThreadTrim: &pb_common.TechCardOperationThreadTrim{ResidualTailMaxMm: dec("3")},
		}, "operations[0].residual_tail_max_mm"},
		{"вид чистки на контроле", &pb_common.TechCardOperation{
			OperationType: opTypeInspect, Zone: zoneOther,
			Inspect: &pb_common.TechCardOperationInspect{
				CoverageMode: pb_common.TechCardInspectCoverage_TECH_CARD_INSPECT_COVERAGE_EACH_UNIT,
			},
			Clean: &pb_common.TechCardOperationClean{Kind: pb_common.TechCardCleaningKind_TECH_CARD_CLEANING_KIND_SPOT_CLEAN},
		}, "operations[0].cleaning_kind"},
		{"охват контроля на чистке", &pb_common.TechCardOperation{
			OperationType: opTypeClean, Zone: zoneOther,
			Clean:   &pb_common.TechCardOperationClean{Kind: pb_common.TechCardCleaningKind_TECH_CARD_CLEANING_KIND_SPOT_CLEAN},
			Inspect: &pb_common.TechCardOperationInspect{CoverageMode: pb_common.TechCardInspectCoverage_TECH_CARD_INSPECT_COVERAGE_AQL_PLAN},
		}, "operations[0].coverage_mode"},
		{"мокрая обработка на сложить", &pb_common.TechCardOperation{
			OperationType: opTypeFold, Zone: zoneOther,
			WetProcessKind: pb_common.TechCardWetProcessKind_TECH_CARD_WET_PROCESS_KIND_RINSE,
		}, "operations[0].wet_process_kind"},
		{"застёжка на установке фурнитуры", &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneClosure,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod: pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_PRESS_SET,
			},
			Fastening: &pb_common.TechCardOperationFastening{
				ZipperApplication: pb_common.TechCardZipperApplication_TECH_CARD_ZIPPER_APPLICATION_FLY,
			},
		}, "operations[0].zipper_application"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ve := kindRefusal(t, tt.op)
			if ve.Field != tt.field || ve.Reason != "not_applicable" {
				t.Errorf("отказ назвал %q/%q, ожидалось %q/not_applicable", ve.Field, ve.Reason, tt.field)
			}
		})
	}
}

// TestOperationKindsCycleMachineKeepsThreeOfFive — на петельном / закрепочном / пуговичном автомате
// живут ровно три поля фурнитуры из пяти. attach_method и foldback_mm там неприменимы: как держится
// пуговица, решает программа автомата.
func TestOperationKindsCycleMachineKeepsThreeOfFive(t *testing.T) {
	ok := kindParse(t, &pb_common.TechCardOperation{
		OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtBartack,
		Hardware: &pb_common.TechCardOperationHardware{
			HolePrep:         pb_common.TechCardHolePrep_TECH_CARD_HOLE_PREP_AWL_PIERCE,
			Reinforcement:    pb_common.TechCardReinforcement_TECH_CARD_REINFORCEMENT_TAPE,
			CycleStitchCount: 28,
		},
	})
	if f := kindFacts(ok.Operations[0]); f["hole_prep"] != "awl_pierce" || f["reinforcement"] != "tape" ||
		f["cycle_stitch_count"] != "28" {
		t.Fatalf("три цикловых поля не доехали: %v", f)
	}
	for _, tt := range []struct {
		name  string
		hw    *pb_common.TechCardOperationHardware
		field string
	}{
		{"способ крепления", &pb_common.TechCardOperationHardware{
			AttachMethod: pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_CRIMP,
		}, "operations[0].attach_method"},
		{"подгиб стропы", &pb_common.TechCardOperationHardware{FoldbackMm: dec("30")}, "operations[0].foldback_mm"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ve := kindRefusal(t, &pb_common.TechCardOperation{
				OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtButtonhole, Hardware: tt.hw,
			})
			if ve.Field != tt.field || ve.Reason != "not_applicable" {
				t.Errorf("отказ назвал %q/%q, ожидалось %q/not_applicable", ve.Field, ve.Reason, tt.field)
			}
		})
	}
}

// ── 4. NOT_APPLICABLE ПО ЯВНОМУ MACHINE_TYPE ────────────────────────────────────────────────────

// kindProfileCard — карточка с парком, где профиль ЗНАЕТ машинку, а шаг ссылается на него ключом и
// СВОЕГО типа не называет.
func kindProfileCard(m pb_common.TechCardMachineType, op *pb_common.TechCardOperation) *pb_common.TechCardInsert {
	op.MachineProfileKey = machineKey
	return &pb_common.TechCardInsert{
		StyleNumber: "OK-2",
		Name:        "Jacket",
		Construction: &pb_common.TechCardConstruction{
			EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
				Machines: []*pb_common.TechCardMachineProfile{{
					ProfileKey: machineKey, Label: "автомат", MachineType: m,
				}},
			},
		},
		Operations: []*pb_common.TechCardOperation{op},
	}
}

// TestOperationKindsNotApplicableByExplicitMachineType — каждое поле машинного скоупа против ТРЁХ
// состояний шага: своя машинка (проходит), ЧУЖАЯ явная (отказ) и ОТСУТСТВУЮЩАЯ, но выводимая через
// machine_profile_key (тоже отказ).
//
// Третий столбец и есть правило волны: тип, унаследованный через профиль, НЕ засчитывается нигде.
// Иначе одно и то же поле было бы законным или нет в зависимости от того, что лежит в парке
// карточки, — а профиль можно удалить, не открыв ни одного шага.
func TestOperationKindsNotApplicableByExplicitMachineType(t *testing.T) {
	cases := []struct {
		name  string
		own   pb_common.TechCardMachineType
		alien pb_common.TechCardMachineType
		field string
		fill  func(op *pb_common.TechCardOperation)
	}{
		{"форма петли", mtButtonhole, mtOverlock, "buttonhole_style", func(op *pb_common.TechCardOperation) {
			op.Fastening = &pb_common.TechCardOperationFastening{
				ButtonholeStyle: pb_common.TechCardButtonholeStyle_TECH_CARD_BUTTONHOLE_STYLE_STRAIGHT,
			}
		}},
		{"прорезь петли", mtButtonhole, mtBartack, "cut_length_mm", func(op *pb_common.TechCardOperation) {
			op.Fastening = &pb_common.TechCardOperationFastening{CutLengthMm: dec("20")}
		}},
		{"положение петли", mtButtonhole, mtZipper, "buttonhole_orientation", func(op *pb_common.TechCardOperation) {
			op.Fastening = &pb_common.TechCardOperationFastening{
				ButtonholeOrientation: pb_common.TechCardButtonholeOrientation_TECH_CARD_BUTTONHOLE_ORIENTATION_VERTICAL,
			}
		}},
		{"длина закрепки", mtBartack, mtOverlock, "bartack_length_mm", func(op *pb_common.TechCardOperation) {
			op.Fastening = &pb_common.TechCardOperationFastening{BartackLengthMm: dec("12")}
		}},
		{"рисунок пришива", mtButtonAttach, mtButtonhole, "attach_pattern", func(op *pb_common.TechCardOperation) {
			op.Fastening = &pb_common.TechCardOperationFastening{
				AttachPattern: pb_common.TechCardButtonAttachPattern_TECH_CARD_BUTTON_ATTACH_PATTERN_SQUARE,
			}
		}},
		{"установка молнии", mtZipper, mtOverlock, "zipper_application", func(op *pb_common.TechCardOperation) {
			op.Fastening = &pb_common.TechCardOperationFastening{
				ZipperApplication: pb_common.TechCardZipperApplication_TECH_CARD_ZIPPER_APPLICATION_LAPPED,
			}
		}},
		{"складка бейки", mtBinding, mtOverlock, "binding_style", func(op *pb_common.TechCardOperation) {
			op.Stitching = &pb_common.TechCardOperationStitching{
				BindingStyle: pb_common.TechCardBindingStyle_TECH_CARD_BINDING_STYLE_SINGLE_FOLD,
			}
		}},
		{"горячий воздух", mtSeamTaping, mtUltrasonic, "air_temperature_c", func(op *pb_common.TechCardOperation) {
			op.Weld = &pb_common.TechCardOperationWeld{AirTemperatureC: 400}
		}},
		{"скорость подачи", mtSeamTaping, mtOverlock, "feed_speed_m_min", func(op *pb_common.TechCardOperation) {
			op.Weld = &pb_common.TechCardOperationWeld{FeedSpeedMMin: dec("2")}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name+"/своя машинка", func(t *testing.T) {
			op := &pb_common.TechCardOperation{OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: tt.own}
			tt.fill(op)
			ins := kindParse(t, op)
			if kindFacts(ins.Operations[0])[tt.field] == "" {
				t.Errorf("поле %s не сохранилось на своей машинке", tt.field)
			}
		})
		t.Run(tt.name+"/чужая явная машинка", func(t *testing.T) {
			op := &pb_common.TechCardOperation{OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: tt.alien}
			tt.fill(op)
			ve := kindRefusal(t, op)
			if ve.Field != "operations[0]."+tt.field || ve.Reason != "not_applicable" {
				t.Errorf("отказ назвал %q/%q, ожидалось operations[0].%s/not_applicable", ve.Field, ve.Reason, tt.field)
			}
		})
		t.Run(tt.name+"/тип только из профиля", func(t *testing.T) {
			op := &pb_common.TechCardOperation{OperationType: opTypeMachineNew, Zone: zoneOuter}
			tt.fill(op)
			_, err := ConvertPbTechCardInsertToEntity(kindProfileCard(tt.own, op))
			if err == nil {
				t.Fatalf("тип, унаследованный через machine_profile_key, засчитался — правило волны говорит, что не должен")
			}
			var ve *entity.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("ожидался *entity.ValidationError, получено %T: %v", err, err)
			}
			if ve.Field != "operations[0]."+tt.field || ve.Reason != "not_applicable" {
				t.Errorf("отказ назвал %q/%q, ожидалось operations[0].%s/not_applicable", ve.Field, ve.Reason, tt.field)
			}
		})
	}
}

// TestOperationKindsWeldMachineRejectsNeedleAndThread — у проклейки и ультразвука нет ни иглы, ни
// нитки: ниточно-игольные overrides на таком шаге — теневые значения, и они отвергаются по ЯВНОМУ
// типу машинки, независимо от того, заполнен ли weld-блок.
func TestOperationKindsWeldMachineRejectsNeedleAndThread(t *testing.T) {
	for _, tt := range []struct {
		name  string
		field string
		fill  func(op *pb_common.TechCardOperation)
	}{
		{"число ниток", "thread_count", func(op *pb_common.TechCardOperation) { op.ThreadCount = 4 }},
		{"тип иглы", "needle_type", func(op *pb_common.TechCardOperation) {
			op.NeedleType = pb_common.TechCardNeedleType_TECH_CARD_NEEDLE_TYPE_JEANS
		}},
		{"размер иглы", "needle_size_nm", func(op *pb_common.TechCardOperation) { op.NeedleSizeNm = 90 }},
		// Натяжение и заметка к нему едут ПАРОЙ: заметка без шкалы отвергается более старым
		// правилом (needs_thread_tension) ещё до этого, поэтому отдельного кейса у неё нет —
		// проверяется пара, и отказ обязан назвать шкалу, первую из двух.
		{"натяжение с заметкой", "thread_tension", func(op *pb_common.TechCardOperation) {
			op.ThreadTension = pb_common.TechCardThreadTension_TECH_CARD_THREAD_TENSION_LOOSER
			op.ThreadTensionNote = "туже"
		}},
		{"ширина стежка", "stitch_width_mm", func(op *pb_common.TechCardOperation) { op.StitchWidthMm = dec("4") }},
		// Четыре поля S-блока волны. Своё семейство гейтит их только «это машинный шаг», а
		// сварочная машина машинная — без этого правила на безыгольном шаге сохранялись бы «4 иглы
		// с шагом 3.2 мм и закрепка».
		{"калибр между иглами", "needle_gauge_mm", func(op *pb_common.TechCardOperation) {
			// Калибр едет ПАРОЙ с числом игл (правило 1: одиночный калибр отвергается раньше как
			// needs_needle_count), и отказ обязан назвать именно калибр — иначе кейс «число игл»
			// ниже покрывал бы оба поля разом и выпадение калибра из правила прошло бы незаметно.
			op.Stitching = &pb_common.TechCardOperationStitching{NeedleCount: 2, NeedleGaugeMm: dec("3.2")}
		}},
		{"число игл", "needle_count", func(op *pb_common.TechCardOperation) {
			op.Stitching = &pb_common.TechCardOperationStitching{NeedleCount: 4}
		}},
		{"закрепка строчки", "seam_securing", func(op *pb_common.TechCardOperation) {
			op.Stitching = &pb_common.TechCardOperationStitching{
				SeamSecuring: pb_common.TechCardSeamSecuring_TECH_CARD_SEAM_SECURING_CONDENSED,
			}
		}},
		{"шаг между рядами", "row_spacing_mm", func(op *pb_common.TechCardOperation) {
			op.Stitching = &pb_common.TechCardOperationStitching{RowSpacingMm: dec("6")}
		}},
	} {
		for _, mt := range []pb_common.TechCardMachineType{mtSeamTaping, mtUltrasonic} {
			t.Run(tt.name+"/"+mt.String(), func(t *testing.T) {
				op := &pb_common.TechCardOperation{OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mt}
				tt.fill(op)
				ve := kindRefusal(t, op)
				if ve.Field != "operations[0]."+tt.field || ve.Reason != "not_applicable" {
					t.Errorf("отказ назвал %q/%q, ожидалось operations[0].%s/not_applicable", ve.Field, ve.Reason, tt.field)
				}
				// Та же настройка на швейной машинке остаётся законной — правило про машинку, а не про поле.
				sewing := &pb_common.TechCardOperation{OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock}
				tt.fill(sewing)
				kindParse(t, sewing)
			})
		}
	}
}

// TestOperationKindsWeldMachineKeepsFullnessRatio — посадка на сварочной машине ЗАКОННА, и это
// решение, а не недосмотр: fullness_ratio — соотношение длин двух слоёв при ПОДАЧЕ, свойство подачи,
// а не иглы. Сварочная машина слои подаёт (на то у неё feed_speed_m_min), поэтому единственное поле
// S-блока, которое групповое правило безыгольности НЕ отвергает, — именно оно.
func TestOperationKindsWeldMachineKeepsFullnessRatio(t *testing.T) {
	for _, mt := range []pb_common.TechCardMachineType{mtSeamTaping, mtUltrasonic} {
		t.Run(mt.String(), func(t *testing.T) {
			ins := kindParse(t, &pb_common.TechCardOperation{
				OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mt,
				Stitching: &pb_common.TechCardOperationStitching{FullnessRatio: dec("1.15")},
			})
			if got := kindFacts(ins.Operations[0])["fullness_ratio"]; got != "1.15" {
				t.Errorf("посадка на %s сохранилась как %q, ожидалось 1.15", mt.String(), got)
			}
		})
	}
}

// ── 5. REQUIRED-ДИСКРИМИНАТОРЫ ──────────────────────────────────────────────────────────────────

// TestOperationKindsRequiredDiscriminators — шесть глаголов, каждый требует своё поле, и требует
// БЕЗУСЛОВНО: флага осведомлённости у волны нет, обязательность объявляет сам глагол.
func TestOperationKindsRequiredDiscriminators(t *testing.T) {
	for _, tt := range []struct {
		name  string
		bare  *pb_common.TechCardOperation
		full  *pb_common.TechCardOperation
		field string
	}{
		{"установка фурнитуры", &pb_common.TechCardOperation{OperationType: opTypeHardware, Zone: zoneClosure},
			&pb_common.TechCardOperation{OperationType: opTypeHardware, Zone: zoneClosure,
				Hardware: &pb_common.TechCardOperationHardware{
					AttachMethod: pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_SEW,
				}}, "operations[0].attach_method"},
		{"печать", &pb_common.TechCardOperation{OperationType: opTypePrint, Zone: zoneOuter},
			&pb_common.TechCardOperation{OperationType: opTypePrint, Zone: zoneOuter,
				PrintMethod: pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_SCREEN}, "operations[0].print_method"},
		{"подрезка", &pb_common.TechCardOperation{OperationType: opTypeTrimNew, Zone: zoneCollar},
			&pb_common.TechCardOperation{OperationType: opTypeTrimNew, Zone: zoneCollar,
				Trim: &pb_common.TechCardOperationTrim{
					Action: pb_common.TechCardTrimAction_TECH_CARD_TRIM_ACTION_CLIP_CONCAVE,
				}}, "operations[0].trim_action"},
		{"чистка", &pb_common.TechCardOperation{OperationType: opTypeClean, Zone: zoneOther},
			&pb_common.TechCardOperation{OperationType: opTypeClean, Zone: zoneOther,
				Clean: &pb_common.TechCardOperationClean{
					Kind: pb_common.TechCardCleaningKind_TECH_CARD_CLEANING_KIND_DUST_LINT,
				}}, "operations[0].cleaning_kind"},
		{"контроль", &pb_common.TechCardOperation{OperationType: opTypeInspect, Zone: zoneOther},
			&pb_common.TechCardOperation{OperationType: opTypeInspect, Zone: zoneOther,
				Inspect: &pb_common.TechCardOperationInspect{
					CoverageMode: pb_common.TechCardInspectCoverage_TECH_CARD_INSPECT_COVERAGE_AQL_PLAN,
				}}, "operations[0].coverage_mode"},
		{"мокрая обработка", &pb_common.TechCardOperation{OperationType: opTypeWet, Zone: zoneOther},
			&pb_common.TechCardOperation{OperationType: opTypeWet, Zone: zoneOther,
				WetProcessKind: pb_common.TechCardWetProcessKind_TECH_CARD_WET_PROCESS_KIND_SOFTENER}, "operations[0].wet_process_kind"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ve := kindRefusal(t, tt.bare)
			if ve.Field != tt.field || ve.Reason != "required" {
				t.Errorf("отказ назвал %q/%q, ожидалось %q/required", ve.Field, ve.Reason, tt.field)
			}
			kindParse(t, tt.full)
		})
	}
}

// TestOperationKindsDeltaFieldsAreNeverRequired — пары (глагол, machine_type) семейств FA и S живут
// в проде годами. Шаг на любой из них БЕЗ единого нового поля обязан сохраняться: старые бандлы
// пишут именно такие строки, и довод «глагол доказывает осведомлённость» к ним неприменим.
func TestOperationKindsDeltaFieldsAreNeverRequired(t *testing.T) {
	for _, m := range []pb_common.TechCardMachineType{
		mtButtonhole, mtButtonAttach, mtBartack, mtBinding, mtZipper, mtSeamTaping, mtUltrasonic,
	} {
		t.Run(m.String(), func(t *testing.T) {
			ins := kindParse(t, &pb_common.TechCardOperation{
				OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: m,
			})
			for col, v := range kindFacts(ins.Operations[0]) {
				if v != "" {
					t.Errorf("шаг без единого нового поля записал %s = %q", col, v)
				}
			}
		})
	}
}

// ── 6. ДВУХ-ПОЛЕВЫЕ ПРАВИЛА ─────────────────────────────────────────────────────────────────────

// TestOperationKindsPairRules — четыре живых двух-полевых правила и два групповых, каждое парой
// «проходит / отказывает». В БД их нет и быть не может: одноколоночный CHECK не видит соседа, а
// двухколоночный проверял бы ретроактивно всю историю таблицы.
func TestOperationKindsPairRules(t *testing.T) {
	machineOp := func(m pb_common.TechCardMachineType) *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: m}
	}
	gauge := func(count int32) *pb_common.TechCardOperation {
		op := machineOp(mtOverlock)
		op.Stitching = &pb_common.TechCardOperationStitching{NeedleCount: count, NeedleGaugeMm: dec("6.4")}
		return op
	}
	pitch := func(count int32) *pb_common.TechCardOperation {
		op := machineOp(mtOverlock)
		op.PlacementLayout = &pb_common.TechCardOperationPlacement{Count: count, PitchMm: dec("50")}
		return op
	}
	foldback := func(m pb_common.TechCardHardwareAttachMethod) *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneClosure,
			Hardware: &pb_common.TechCardOperationHardware{AttachMethod: m, FoldbackMm: dec("35")},
		}
	}
	printOp := func(method pb_common.TechCardPrintMethod, fill func(op *pb_common.TechCardOperation)) *pb_common.TechCardOperation {
		op := &pb_common.TechCardOperation{OperationType: opTypePrint, Zone: zoneOuter, PrintMethod: method}
		fill(op)
		return op
	}
	withPeel := func(op *pb_common.TechCardOperation) {
		op.Print = &pb_common.TechCardOperationPrint{PeelMode: pb_common.TechCardPeelMode_TECH_CARD_PEEL_MODE_COLD}
	}
	withPress := func(op *pb_common.TechCardOperation) { op.PressTemperatureC = 150 }

	air := func(m pb_common.TechCardMachineType) *pb_common.TechCardOperation {
		op := machineOp(m)
		op.Weld = &pb_common.TechCardOperationWeld{AirTemperatureC: 420}
		return op
	}

	for _, tt := range []struct {
		name   string
		ok     *pb_common.TechCardOperation
		bad    *pb_common.TechCardOperation
		field  string
		reason string
	}{
		{"калибр требует двух игл", gauge(2), gauge(1), "operations[0].needle_gauge_mm", "needs_needle_count"},
		{"шаг требует двух повторов", pitch(2), pitch(0), "operations[0].pitch_mm", "needs_placement_count"},
		{"подгиб требует продетой стропы",
			foldback(pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_THREADED),
			foldback(pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_SEW),
			"operations[0].foldback_mm", "needs_attach_method"},
		{"горячий воздух только у проклейки", air(mtSeamTaping), air(mtUltrasonic),
			"operations[0].air_temperature_c", "not_applicable"},
		{"у лазера нет носителя",
			printOp(pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_HEAT_TRANSFER, withPeel),
			printOp(pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_LASER_ENGRAVE, withPeel),
			"operations[0].peel_mode", "not_applicable"},
		{"у лазера нет прижима",
			printOp(pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_HEAT_TRANSFER, withPress),
			printOp(pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_LASER_ENGRAVE, withPress),
			"operations[0].press_temperature_c", "not_applicable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			kindParse(t, tt.ok)
			ve := kindRefusal(t, tt.bad)
			if ve.Field != tt.field || ve.Reason != tt.reason {
				t.Errorf("отказ назвал %q/%q, ожидалось %q/%s", ve.Field, ve.Reason, tt.field, tt.reason)
			}
		})
	}
}

// ── 7. ШИРИНА ОТСТРОЧКИ ─────────────────────────────────────────────────────────────────────────

// TestTopstitchWidthByMode — ТРИ режима × (ширина задана / пуста), шесть клеток, и ни одна не
// пустует: parallel_to_seam — это отступ ОТ ШВА и без числа не инструкция, in_ditch — строчка В
// САМОМ ШВЕ, у неё ширины нет и быть не может, edge — от КРАЯ ДЕТАЛИ, и число у него ОПЦИОНАЛЬНО
// (есть = отступ в мм, нет = вплотную).
//
// ОБЕ КЛЕТКИ `edge` ЗЕЛЁНЫЕ, И ЭТО ГЛАВНОЕ, ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ. До 0326 «в край с шириной»
// отвергалось, а число жило в отдельном режиме `width`; клиент выкатывается ПОСЛЕ сервера и шлёт
// именно такую пару. Если правило вернётся, эта клетка покраснеет раньше, чем админка.
func TestTopstitchWidthByMode(t *testing.T) {
	op := func(mode pb_common.TechCardTopstitchMode, width string) *pb_common.TechCardOperation {
		ts := &pb_common.TechCardTopstitch{Mode: mode}
		if width != "" {
			ts.WidthMm = dec(width)
		}
		return &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock, Topstitch: ts,
		}
	}
	for _, tt := range []struct {
		name   string
		mode   pb_common.TechCardTopstitchMode
		width  string
		reason string // "" = сохраняется
		token  string
	}{
		{"в край без ширины (вплотную)", pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_EDGE, "", "", "edge"},
		{"в край с шириной (отступ от края)", pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_EDGE, "6", "", "edge"},
		{"в шов без ширины", pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_IN_DITCH, "", "", "in_ditch"},
		{"в шов с шириной", pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_IN_DITCH, "3", "not_applicable", ""},
		{"параллельно шву с шириной", pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_PARALLEL_TO_SEAM, "12", "", "parallel_to_seam"},
		{"параллельно шву без ширины", pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_PARALLEL_TO_SEAM, "", "required", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.reason == "" {
				ins := kindParse(t, op(tt.mode, tt.width))
				got := ins.Operations[0]
				if got.TopstitchMode.String != tt.token {
					t.Fatalf("режим отстрочки сохранился как %q, ожидалось %q", got.TopstitchMode.String, tt.token)
				}
				if (tt.width == "") == got.TopstitchWidthMm.Valid {
					t.Errorf("ширина и режим разошлись: режим %q, ширина %+v", tt.token, got.TopstitchWidthMm)
				}
				return
			}
			ve := kindRefusal(t, op(tt.mode, tt.width))
			if ve.Field != "operations[0].topstitch_width_mm" || ve.Reason != tt.reason {
				t.Errorf("отказ назвал %q/%q, ожидалось operations[0].topstitch_width_mm/%s", ve.Field, ve.Reason, tt.reason)
			}
		})
	}
}

// TestOperationKindsRangeBands — по одному числу за каждой границей: диапазон стоит и в CHECK'е
// 0324, но CHECK отвечает голым 3819 без поля и без слов, а это числа, набранные в форме.
func TestOperationKindsRangeBands(t *testing.T) {
	for _, tt := range []struct {
		name  string
		op    *pb_common.TechCardOperation
		field string
	}{
		{"игл больше двенадцати", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			Stitching: &pb_common.TechCardOperationStitching{NeedleCount: 13},
		}, "operations[0].needle_count"},
		{"калибр уже 1.6 мм", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			Stitching: &pb_common.TechCardOperationStitching{NeedleCount: 2, NeedleGaugeMm: dec("1.5")},
		}, "operations[0].needle_gauge_mm"},
		{"посадка ниже 0.60", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			Stitching: &pb_common.TechCardOperationStitching{FullnessRatio: dec("0.59")},
		}, "operations[0].fullness_ratio"},
		{"подача быстрее 10 м/мин", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtUltrasonic,
			Weld: &pb_common.TechCardOperationWeld{FeedSpeedMMin: dec("10.5")},
		}, "operations[0].feed_speed_m_min"},
		{"воздух холоднее 100 °C", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtSeamTaping,
			Weld: &pb_common.TechCardOperationWeld{AirTemperatureC: 99},
		}, "operations[0].air_temperature_c"},
		{"цикл короче восьми стежков", &pb_common.TechCardOperation{
			OperationType: opTypeHardware, Zone: zoneClosure,
			Hardware: &pb_common.TechCardOperationHardware{
				AttachMethod:     pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_SEW,
				CycleStitchCount: 7,
			},
		}, "operations[0].cycle_stitch_count"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ve := kindRefusal(t, tt.op)
			if ve.Field != tt.field || ve.Reason != "out_of_range" {
				t.Errorf("отказ назвал %q/%q, ожидалось %q/out_of_range", ve.Field, ve.Reason, tt.field)
			}
		})
	}
}
