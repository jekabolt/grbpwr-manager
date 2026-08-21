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
// Это технолог, стёрший стиль петли. Заведи здесь бекстоп (как у узлов и снимков) — и восемнадцать
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

// --- РАСШИРЕННЫЕ СЛОВАРИ ЖИВЫХ КОЛОНОК (шаги 4..9 миграции 0324) ---------------------------------
//
// Волна добавила не только 32 колонки и девять глаголов: шесть словарей КОЛОНОК, существующих
// годами, получили новые токены. Карточка, несущая РОВНО ОДИН такой токен и НИ ОДНОЙ из 32 колонок,
// для предиката «по колонкам и глаголам» выглядит пустой — и запись отставшей вкладки стирала бы
// токен молча. Поэтому клетка на КАЖДЫЙ токен, по одному на кейс: пропущенный токен не падает, он
// просто тихо перестаёт защищаться.

// storedCardWith даёт собрать карточку с фактом ВНЕ шага — парк оборудования или строка BOM.
func storedCardWith(mutate func(*entity.TechCard)) *entity.TechCard {
	card := storedOpWith(func(*entity.TechCardOperation) {})
	mutate(card)
	return card
}

func TestOperationKindsStoredGateCountsExtendedVocabularyTokens(t *testing.T) {
	cases := []struct {
		token  string
		stored *entity.TechCard
	}{
		// Шаг 4: machine_type шага. Безыгольные машины легаси-двойника не имеют, поэтому старый
		// бандл такого шага не строил и токен доказывает нового клиента.
		{"machine_type=seam_taping", storedOpWith(func(o *entity.TechCardOperation) {
			o.MachineType = okStr("seam_taping")
		})},
		{"machine_type=ultrasonic_welder", storedOpWith(func(o *entity.TechCardOperation) {
			o.MachineType = okStr("ultrasonic_welder")
		})},

		// Шаг 6: topstitch_mode. Потеря здесь самая дорогая — parseTopstitch при UNKNOWN обнуляет
		// заодно ширину и число рядов, то есть за одним токеном уходят три колонки.
		{"topstitch_mode=in_ditch", storedOpWith(func(o *entity.TechCardOperation) {
			o.TopstitchMode = okStr("in_ditch")
		})},
		{"topstitch_mode=parallel_to_seam", storedOpWith(func(o *entity.TechCardOperation) {
			o.TopstitchMode = okStr("parallel_to_seam")
			o.TopstitchWidthMm = okDec("6")
		})},

		// Шаг 5: press_cloth шага.
		{"press_cloth=silicone_paper", storedOpWith(func(o *entity.TechCardOperation) {
			o.PressCloth = okStr("silicone_paper")
		})},

		// Шаг 7: equipment профиля парка. Сам факт профиля НЕ считается — его закрывает щит 0306,
		// а бандл между волнами объявляет machine_fields_aware = true и проходит его честно.
		{"profile.equipment=seam_taping", storedCardWith(func(c *entity.TechCard) {
			c.Construction = &entity.TechCardConstruction{EquipmentDefaults: &entity.TechCardEquipmentDefaults{
				Machines: []entity.TechCardMachineProfile{{ProfileKey: "P1", MachineType: "seam_taping"}},
			}}
		})},
		{"profile.equipment=ultrasonic_welder", storedCardWith(func(c *entity.TechCard) {
			c.Construction = &entity.TechCardConstruction{EquipmentDefaults: &entity.TechCardEquipmentDefaults{
				Machines: []entity.TechCardMachineProfile{{ProfileKey: "P1", MachineType: "ultrasonic_welder"}},
			}}
		})},

		// Шаг 8: press_cloth профиля парка — тот же словарь, что у шага, потому что шаг его
		// наследует.
		{"profile.press_cloth=silicone_paper", storedCardWith(func(c *entity.TechCard) {
			c.Construction = &entity.TechCardConstruction{EquipmentDefaults: &entity.TechCardEquipmentDefaults{
				Presses: []entity.TechCardPressProfile{{
					ProfileKey: "P2", PressEquipment: "press", PressCloth: okStr("silicone_paper"),
				}},
			}}
		})},

		// Шаг 9: вид позиции BOM.
		{"bom.kind=seam_sealing_tape", storedCardWith(func(c *entity.TechCard) {
			c.BomItems = []entity.TechCardBomItem{{LineKey: "B1", Kind: okStr("seam_sealing_tape")}}
		})},
		{"bom.kind=embroidery_stabilizer", storedCardWith(func(c *entity.TechCard) {
			c.BomItems = []entity.TechCardBomItem{{LineKey: "B1", Kind: okStr("embroidery_stabilizer")}}
		})},
	}
	if len(cases) != 10 {
		t.Fatalf("волна дописала десять токенов в шесть живых словарей, в таблице %d", len(cases))
	}
	for _, tt := range cases {
		t.Run(tt.token, func(t *testing.T) {
			err := operationKindsStoredGate(okPayload(false, okOpLegacy()), tt.stored)
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("неосведомлённая запись обязана быть отвергнута, иначе %s сотрётся молча; got %v",
					tt.token, err)
			}
			// И симметрично: осведомлённый бандл редактирует такую карточку как обычно.
			if err := operationKindsStoredGate(okPayload(true, okOpLegacy()), tt.stored); err != nil {
				t.Fatalf("осведомлённая запись обязана пройти: %v", err)
			}
		})
	}
}

// Правило 1 на тех же токенах: неосведомлённый бандл, ЭХОЯЩИЙ новый токен, отвергается ещё до
// конверсии.
func TestOperationKindsWireGateRefusesUnawareExtendedVocabularyEcho(t *testing.T) {
	withOp := func(mutate func(*pb_common.TechCardOperation)) *pb_common.TechCardInsert {
		op := okOpLegacy()
		mutate(op)
		return okPayload(false, op)
	}
	withCard := func(mutate func(*pb_common.TechCardInsert)) *pb_common.TechCardInsert {
		pb := okPayload(false, okOpLegacy())
		mutate(pb)
		return pb
	}
	cases := []struct {
		token string
		pb    *pb_common.TechCardInsert
	}{
		{"machine_type=seam_taping", withOp(func(o *pb_common.TechCardOperation) {
			o.MachineType = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_SEAM_TAPING
		})},
		{"machine_type=ultrasonic_welder", withOp(func(o *pb_common.TechCardOperation) {
			o.MachineType = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_ULTRASONIC_WELDER
		})},
		{"topstitch_mode=in_ditch", withOp(func(o *pb_common.TechCardOperation) {
			o.Topstitch = &pb_common.TechCardTopstitch{
				Mode: pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_IN_DITCH,
			}
		})},
		{"topstitch_mode=parallel_to_seam", withOp(func(o *pb_common.TechCardOperation) {
			o.Topstitch = &pb_common.TechCardTopstitch{
				Mode: pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_PARALLEL_TO_SEAM,
			}
		})},
		{"press_cloth=silicone_paper", withOp(func(o *pb_common.TechCardOperation) {
			o.PressCloth = pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_SILICONE_PAPER
		})},
		{"profile.equipment=seam_taping", withCard(func(pb *pb_common.TechCardInsert) {
			pb.Construction = &pb_common.TechCardConstruction{
				EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
					Machines: []*pb_common.TechCardMachineProfile{{
						ProfileKey:  "P1",
						MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_SEAM_TAPING,
					}},
				},
			}
		})},
		{"profile.equipment=ultrasonic_welder", withCard(func(pb *pb_common.TechCardInsert) {
			pb.Construction = &pb_common.TechCardConstruction{
				EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
					Machines: []*pb_common.TechCardMachineProfile{{
						ProfileKey:  "P1",
						MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_ULTRASONIC_WELDER,
					}},
				},
			}
		})},
		{"profile.press_cloth=silicone_paper", withCard(func(pb *pb_common.TechCardInsert) {
			pb.Construction = &pb_common.TechCardConstruction{
				EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
					Presses: []*pb_common.TechCardPressProfile{{
						ProfileKey: "P2",
						PressCloth: pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_SILICONE_PAPER,
					}},
				},
			}
		})},
		{"bom.kind=seam_sealing_tape", withCard(func(pb *pb_common.TechCardInsert) {
			kind := pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_SEAM_SEALING_TAPE
			pb.BomItems = []*pb_common.TechCardBomItem{{Kind: &kind}}
		})},
		{"bom.kind=embroidery_stabilizer", withCard(func(pb *pb_common.TechCardInsert) {
			kind := pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_EMBROIDERY_STABILIZER
			pb.BomItems = []*pb_common.TechCardBomItem{{Kind: &kind}}
		})},
	}
	if len(cases) != 10 {
		t.Fatalf("волна дописала десять токенов в шесть живых словарей, в таблице %d", len(cases))
	}
	for _, tt := range cases {
		t.Run(tt.token, func(t *testing.T) {
			if code := status.Code(operationKindsWireGate(tt.pb)); code != codes.FailedPrecondition {
				t.Fatalf("эхо токена %s обязано быть отвергнуто правилом 1; got %v", tt.token, code)
			}
			aware := tt.pb
			aware.OperationKindsAware = true
			if err := operationKindsWireGate(aware); err != nil {
				t.Fatalf("осведомлённый бандл имеет право прислать это: %v", err)
			}
		})
	}
}

// --- КОНТРОЛЬ, КОТОРЫЙ ВАЖНЕЕ ВСЕХ КЛЕТОК ВЫШЕ ---------------------------------------------------
//
// Предикаты обязаны считать ТОКЕНЫ, а не «поле заполнено». Проверка вида `machine_type != UNKNOWN`
// закрыла бы дыру и одновременно объявила бы фактом волны КАЖДЫЙ обычный MACHINE-шаг — то есть
// заблокировала бы сегодняшнюю рабочую админку целиком, на каждой карточке, живущей в проде.
// Ложное срабатывание здесь дороже пропуска, поэтому клетка отдельная и явная.
func TestOperationKindsGatesDoNotBlockAnOrdinaryPreWaveCard(t *testing.T) {
	// Сохранённая карточка ДО волны: старая машинка, старый режим отстрочки с шириной и рядами,
	// старый проутюжильник, старый вид BOM, старые профили парка. Ни одного токена волны.
	stored := storedCardWith(func(c *entity.TechCard) {
		c.Operations = []entity.TechCardOperation{{
			OperationNumber:  okInt(10),
			OperationType:    entity.OpTypeMachine,
			Zone:             "front",
			MachineType:      okStr("lockstitch"),
			TopstitchMode:    okStr("width"),
			TopstitchWidthMm: okDec("6"),
			TopstitchRows:    okInt(2),
			PressCloth:       okStr("teflon_sheet"),
		}}
		c.Construction = &entity.TechCardConstruction{EquipmentDefaults: &entity.TechCardEquipmentDefaults{
			Machines: []entity.TechCardMachineProfile{{ProfileKey: "P1", MachineType: "lockstitch"}},
			Presses: []entity.TechCardPressProfile{{
				ProfileKey: "P2", PressEquipment: "press", PressCloth: okStr("press_cloth"),
			}},
		}}
		c.BomItems = []entity.TechCardBomItem{{LineKey: "B1", Kind: okStr("button")}}
	})
	// И ровно тот payload, что шлёт сегодняшняя админка: флага волны нет, зато есть всё, что она
	// умела до неё.
	kind := pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_BUTTON
	pb := okPayload(false, &pb_common.TechCardOperation{
		OperationNumber: 10,
		OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		Zone:            gateZone(),
		MachineType:     pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
		PressCloth:      pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_TEFLON_SHEET,
		Topstitch: &pb_common.TechCardTopstitch{
			Mode: pb_common.TechCardTopstitchMode_TECH_CARD_TOPSTITCH_MODE_WIDTH,
			Rows: 2,
		},
	})
	pb.BomItems = []*pb_common.TechCardBomItem{{Kind: &kind}}
	pb.Construction = &pb_common.TechCardConstruction{
		EquipmentDefaults: &pb_common.TechCardEquipmentDefaults{
			Machines: []*pb_common.TechCardMachineProfile{{
				ProfileKey:  "P1",
				MachineType: pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
			}},
			Presses: []*pb_common.TechCardPressProfile{{
				ProfileKey: "P2",
				PressCloth: pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_PRESS_CLOTH,
			}},
		},
	}
	if err := operationKindsWireGate(pb); err != nil {
		t.Fatalf("правило 1 объявило обычный до-волновой payload эхом волны — это блокирует сегодняшнюю админку: %v", err)
	}
	if err := operationKindsStoredGate(pb, stored); err != nil {
		t.Fatalf("правило 2 объявило обычную до-волновую карточку несущей факты волны — это блокирует сегодняшнюю админку: %v", err)
	}
	// И по отдельности, чтобы отказ было видно на конкретной половине.
	if payloadSpeaksOperationKinds(pb) {
		t.Fatal("проводной предикат считает обычный MACHINE + lockstitch фактом волны")
	}
	if storedHasOperationKindFacts(stored) {
		t.Fatal("предикат хранилища считает обычную до-волновую карточку несущей факты волны")
	}
}
