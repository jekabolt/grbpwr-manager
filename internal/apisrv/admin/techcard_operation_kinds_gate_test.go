package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Таблица истинности щита видов операций (0324).
//
// САМЫЕ ВАЖНЫЕ ДВЕ КЛЕТКИ, и они противоположны друг другу:
//
//   - «карточка несёт поля волны, бандл НЕ осведомлён» — то самое тихое стирание отставшей
//     вкладкой, ради которого щит написан. Отказ.
//   - «карточка несёт поля волны, бандл ОСВЕДОМЛЁН, полей не прислал» — рядовая правка: технолог
//     стёр стиль петли. ПРОПУСК. Клетка выписана отдельным тестом ниже, потому что «защита,
//     которая делает поле нестираемым» — классический дефект этой конструкции, и здесь он
//     закрыт решением не заводить парный `*_cleared`.

func okPayload(aware bool, ops ...*pb_common.TechCardOperation) *pb_common.TechCardInsert {
	return &pb_common.TechCardInsert{
		StyleNumber:         "OK-GATE",
		Name:                "gate",
		OperationKindsAware: aware,
		Operations:          ops,
	}
}

// Шаг, который шлёт и сегодняшний бандл, и вчерашний: старая пара (MACHINE, buttonhole) без единого
// поля волны. Отличить их можно ТОЛЬКО по флагу.
func okOpLegacy() *pb_common.TechCardOperation {
	return &pb_common.TechCardOperation{
		OperationNumber: 10,
		OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		Zone:            gateZone(),
		MachineType:     pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BUTTONHOLE,
	}
}

func okDec(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

func okStr(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func okInt(i int32) sql.NullInt32 { return sql.NullInt32{Int32: i, Valid: true} }

// storedOpWith строит сохранённый шаг СТАРОЙ пары (MACHINE, buttonhole) и даёт мутатору заполнить
// ровно одну колонку волны — так каждый случай ниже называет одно поле, а не «какие-то факты».
func storedOpWith(mutate func(*entity.TechCardOperation)) *entity.TechCard {
	op := entity.TechCardOperation{
		OperationNumber: okInt(10),
		OperationType:   entity.OpTypeMachine,
		Zone:            "front",
		MachineType:     okStr("buttonhole"),
	}
	mutate(&op)
	return storedCard(op)
}

// --- ПРАВИЛО 2: сохранённая карточка несёт поля волны, а запись их не объявляет -------------------
//
// ПОКОЛОНОЧНО, А НЕ ОДНИМ КЕЙСОМ. Предикат — это тридцать две дизъюнкции плюс девять глаголов, и
// пропущенная дизъюнкция молчит: карточка с ровно этим одним заполненным полем проходит щит
// насквозь и теряет его. Ровно так «список из восьми дельтовых полей» и оказался неполон — пять
// полей блока stitching сидят на ЛЮБОМ MACHINE и в тот список не попали.
func TestOperationKindsStoredGateRefusesUnawareWriteColumnByColumn(t *testing.T) {
	cases := []struct {
		column string
		mutate func(*entity.TechCardOperation)
	}{
		// Строчка (S) — любой MACHINE. Пять полей, которых НЕТ в перечне «восьми дельтовых»,
		// хотя сидят они на паре, которую сегодняшний бандл шлёт каждый день.
		{"needle_count", func(o *entity.TechCardOperation) { o.NeedleCount = okInt(2) }},
		{"needle_gauge_mm", func(o *entity.TechCardOperation) { o.NeedleGaugeMm = okDec("6.4") }},
		{"seam_securing", func(o *entity.TechCardOperation) { o.SeamSecuring = okStr("backtack") }},
		{"row_spacing_mm", func(o *entity.TechCardOperation) { o.RowSpacingMm = okDec("5") }},
		{"fullness_ratio", func(o *entity.TechCardOperation) { o.FullnessRatio = okDec("1.15") }},

		// Раскладка повторов (PL).
		{"placement_count", func(o *entity.TechCardOperation) { o.PlacementCount = okInt(6) }},
		{"pitch_mm", func(o *entity.TechCardOperation) { o.PitchMm = okDec("80") }},

		// Фурнитура (H).
		{"attach_method", func(o *entity.TechCardOperation) { o.AttachMethod = okStr("press_set") }},
		{"hole_prep", func(o *entity.TechCardOperation) { o.HolePrep = okStr("punch") }},
		{"reinforcement", func(o *entity.TechCardOperation) { o.Reinforcement = okStr("fusible_patch") }},
		{"foldback_mm", func(o *entity.TechCardOperation) { o.FoldbackMm = okDec("35") }},
		{"cycle_stitch_count", func(o *entity.TechCardOperation) { o.CycleStitchCount = okInt(21) }},

		// Печать (P).
		{"print_method", func(o *entity.TechCardOperation) { o.PrintMethod = okStr("dtf") }},
		{"peel_mode", func(o *entity.TechCardOperation) { o.PeelMode = okStr("cold") }},
		{"second_press_sec", func(o *entity.TechCardOperation) { o.SecondPressSec = okInt(5) }},
		{"pressure_scale", func(o *entity.TechCardOperation) { o.PressureScale = okStr("firm") }},

		// Сварка и проклейка (W).
		{"air_temperature_c", func(o *entity.TechCardOperation) { o.AirTemperatureC = okInt(450) }},
		{"feed_speed_m_min", func(o *entity.TechCardOperation) { o.FeedSpeedMMin = okDec("4.5") }},

		// Подрезка и выправка (T).
		{"trim_action", func(o *entity.TechCardOperation) { o.TrimAction = okStr("grade_layers") }},
		{"residual_allowance_mm", func(o *entity.TechCardOperation) { o.ResidualAllowanceMm = okDec("3") }},

		// Чистка концов ниток (F).
		{"residual_tail_max_mm", func(o *entity.TechCardOperation) { o.ResidualTailMaxMm = okDec("3") }},

		// Три финишных дискриминатора.
		{"cleaning_kind", func(o *entity.TechCardOperation) { o.CleaningKind = okStr("spot_clean") }},
		{"coverage_mode", func(o *entity.TechCardOperation) { o.CoverageMode = okStr("aql_plan") }},
		{"wet_process_kind", func(o *entity.TechCardOperation) { o.WetProcessKind = okStr("enzyme") }},

		// ВОСЕМЬ ПОЛЕЙ ДЕЛЬТЫ — те, ради которых щит и заведён: они сидят на парах, живущих в
		// проде годами, и обнулились бы молча.
		{"buttonhole_style", func(o *entity.TechCardOperation) { o.ButtonholeStyle = okStr("eyelet") }},
		{"cut_length_mm", func(o *entity.TechCardOperation) { o.CutLengthMm = okDec("18") }},
		{"buttonhole_orientation", func(o *entity.TechCardOperation) { o.ButtonholeOrientation = okStr("vertical") }},
		{"bartack_length_mm", func(o *entity.TechCardOperation) { o.BartackLengthMm = okDec("6") }},
		{"attach_pattern", func(o *entity.TechCardOperation) { o.AttachPattern = okStr("cross_x") }},
		{"zipper_application", func(o *entity.TechCardOperation) { o.ZipperApplication = okStr("invisible") }},
		{"binding_style", func(o *entity.TechCardOperation) { o.BindingStyle = okStr("double_fold") }},
		{"label_attach_stitch", func(o *entity.TechCardOperation) { o.LabelAttachStitch = okStr("four_sides") }},
	}
	if len(cases) != 32 {
		t.Fatalf("волна — 32 колонки, в таблице %d: колонка без клетки теряется молча", len(cases))
	}
	for _, tt := range cases {
		t.Run(tt.column, func(t *testing.T) {
			stored := storedOpWith(tt.mutate)
			// Неосведомлённая запись НЕ ЭХОИТ поля волны — именно так выглядит payload отставшей
			// вкладки: она выбросила то, чего не понимает, и payload выглядит невинно.
			err := operationKindsStoredGate(okPayload(false, okOpLegacy()), stored)
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("неосведомлённая запись обязана быть отвергнута, иначе %s обнулится молча; got %v",
					tt.column, err)
			}
			if msg := status.Convert(err).Message(); msg == "" {
				t.Fatal("отказ обязан назвать причину и путь выхода")
			}
			// Проводной гейт на этом payload'е молчит по построению: правило 1 читает ПРОВОД, а
			// провод здесь пуст. Именно поэтому правило 2 и существует.
			if err := operationKindsWireGate(okPayload(false, okOpLegacy())); err != nil {
				t.Fatalf("правило 1 не имеет права срабатывать на пустом проводе: %v", err)
			}
		})
	}
}

// Девять новых глаголов — тоже факт, даже когда шаг не несёт НИ ОДНОЙ из 32 колонок. FOLD и PACK
// полей не несут вовсе: без глагола в предикате бандл, выбросивший непонятный ему шаг, стёр бы его
// полной заменой, а предикат по одним колонкам ответил бы «фактов нет».
func TestOperationKindsStoredGateCountsTheNineVerbsWithoutColumns(t *testing.T) {
	verbs := []entity.TechCardOperationType{
		entity.OpTypeHardwareSet, entity.OpTypePrint, entity.OpTypeTrim,
		entity.OpTypeThreadTrim, entity.OpTypeClean, entity.OpTypeInspect,
		entity.OpTypeFold, entity.OpTypePack, entity.OpTypeWetProcess,
	}
	if len(verbs) != 9 {
		t.Fatalf("новых глаголов девять, в таблице %d", len(verbs))
	}
	for _, v := range verbs {
		t.Run(string(v), func(t *testing.T) {
			stored := storedCard(entity.TechCardOperation{
				OperationNumber: okInt(10), OperationType: v, Zone: "other",
			})
			if code := status.Code(operationKindsStoredGate(okPayload(false, okOpLegacy()), stored)); code != codes.FailedPrecondition {
				t.Fatalf("шаг %s без единой колонки обязан считаться фактом волны; got %v", v, code)
			}
		})
	}
}

// --- КЛЕТКА, БЕЗ КОТОРОЙ ЩИТ СТАНОВИТСЯ ДЕФЕКТОМ -------------------------------------------------
//
// Осведомлённая запись, не несущая полей волны, против карточки, которая их несёт, — ПРОПУСК.
// Это технолог, стёрший стиль петли. Заведи здесь бекстоп (как у узлов и снимков) — и тринадцать
// полей на старых парах стали бы НЕСТИРАЕМЫМИ навсегда: единственный способ убрать значение
// исчез бы вместе с ошибкой, которую невозможно объяснить.
func TestOperationKindsAwareEmptyWriteStillClearsTheFields(t *testing.T) {
	stored := storedOpWith(func(o *entity.TechCardOperation) {
		o.ButtonholeStyle = okStr("eyelet")
		o.CutLengthMm = okDec("18")
		o.ButtonholeOrientation = okStr("vertical")
		o.BartackLengthMm = okDec("6")
		o.AttachPattern = okStr("cross_x")
		o.ZipperApplication = okStr("invisible")
		o.BindingStyle = okStr("double_fold")
		o.LabelAttachStitch = okStr("four_sides")
	})
	// Ровно тот payload, что шлёт НОВЫЙ клиент, когда технолог очистил блок: флаг есть, блоков нет.
	empty := okPayload(true, okOpLegacy())
	if err := operationKindsWireGate(empty); err != nil {
		t.Fatalf("осведомлённая пустая запись обязана пройти правило 1: %v", err)
	}
	if err := operationKindsStoredGate(empty, stored); err != nil {
		t.Fatalf("осведомлённая пустая запись обязана пройти правило 2 и ОЧИСТИТЬ поля: %v", err)
	}
	// И симметрично: осведомлённая запись, которая поля НЕСЁТ, — обычное редактирование.
	filled := okPayload(true, &pb_common.TechCardOperation{
		OperationNumber: 10,
		OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		Zone:            gateZone(),
		MachineType:     pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BUTTONHOLE,
		Fastening: &pb_common.TechCardOperationFastening{
			ButtonholeStyle: pb_common.TechCardButtonholeStyle_TECH_CARD_BUTTONHOLE_STYLE_STRAIGHT,
		},
	})
	if err := operationKindsWireGate(filled); err != nil {
		t.Fatalf("осведомлённая запись с полями обязана пройти правило 1: %v", err)
	}
	if err := operationKindsStoredGate(filled, stored); err != nil {
		t.Fatalf("осведомлённая запись с полями обязана пройти правило 2: %v", err)
	}
}

// --- ПРАВИЛО 1: неосведомлённый бандл эхоит то, чего не понимает ----------------------------------
func TestOperationKindsWireGateRefusesUnawareEcho(t *testing.T) {
	base := func(mutate func(*pb_common.TechCardOperation)) *pb_common.TechCardInsert {
		op := okOpLegacy()
		mutate(op)
		return okPayload(false, op)
	}
	cases := []struct {
		name string
		pb   *pb_common.TechCardInsert
	}{
		{"блок stitching", base(func(o *pb_common.TechCardOperation) {
			o.Stitching = &pb_common.TechCardOperationStitching{NeedleCount: 2}
		})},
		{"блок placement_layout", base(func(o *pb_common.TechCardOperation) {
			o.PlacementLayout = &pb_common.TechCardOperationPlacement{Count: 6}
		})},
		{"блок hardware", base(func(o *pb_common.TechCardOperation) {
			o.Hardware = &pb_common.TechCardOperationHardware{}
		})},
		{"блок print", base(func(o *pb_common.TechCardOperation) {
			o.Print = &pb_common.TechCardOperationPrint{}
		})},
		{"блок weld", base(func(o *pb_common.TechCardOperation) {
			o.Weld = &pb_common.TechCardOperationWeld{}
		})},
		{"блок trim", base(func(o *pb_common.TechCardOperation) {
			o.Trim = &pb_common.TechCardOperationTrim{}
		})},
		{"блок thread_trim", base(func(o *pb_common.TechCardOperation) {
			o.ThreadTrim = &pb_common.TechCardOperationThreadTrim{}
		})},
		{"блок clean", base(func(o *pb_common.TechCardOperation) {
			o.Clean = &pb_common.TechCardOperationClean{}
		})},
		{"блок inspect", base(func(o *pb_common.TechCardOperation) {
			o.Inspect = &pb_common.TechCardOperationInspect{}
		})},
		{"блок fastening", base(func(o *pb_common.TechCardOperation) {
			o.Fastening = &pb_common.TechCardOperationFastening{}
		})},
		{"плоское print_method", base(func(o *pb_common.TechCardOperation) {
			o.PrintMethod = pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_SCREEN
		})},
		{"плоское wet_process_kind", base(func(o *pb_common.TechCardOperation) {
			o.WetProcessKind = pb_common.TechCardWetProcessKind_TECH_CARD_WET_PROCESS_KIND_ENZYME
		})},
		{"новый глагол сырым номером енума", okPayload(false, &pb_common.TechCardOperation{
			OperationNumber: 10,
			OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PACK,
			Zone:            gateZone(),
		})},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := operationKindsWireGate(tt.pb)
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("эхо обязано быть отвергнуто правилом 1; got %v", err)
			}
			if msg := status.Convert(err).Message(); msg == "" {
				t.Fatal("отказ обязан назвать причину")
			}
			// Осведомлённый — тот же payload проходит.
			aware := tt.pb
			aware.OperationKindsAware = true
			if err := operationKindsWireGate(aware); err != nil {
				t.Fatalf("осведомлённый бандл имеет право прислать это: %v", err)
			}
		})
	}
}

// Сегодняшний путь остаётся нетронутым: карточка без единого факта волны сохраняется старым
// бандлом ровно как раньше, без единой проверки. Щит, который заговорил бы здесь, объявил бы
// устаревшими вкладки, редактирующие сегодняшние карточки.
func TestOperationKindsGatesAreSilentOnCardsWithoutWaveFacts(t *testing.T) {
	pb := okPayload(false, okOpLegacy())
	if err := operationKindsWireGate(pb); err != nil {
		t.Fatalf("правило 1: %v", err)
	}
	if err := operationKindsStoredGate(pb, storedOpWith(func(*entity.TechCardOperation) {})); err != nil {
		t.Fatalf("правило 2 на карточке без фактов волны: %v", err)
	}
	if err := operationKindsStoredGate(pb, nil); err != nil {
		t.Fatalf("правило 2 на создании (карточки ещё нет): %v", err)
	}
}
