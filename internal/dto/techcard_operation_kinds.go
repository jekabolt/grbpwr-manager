package dto

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// ВИДЫ ОПЕРАЦИЙ (волна 0324): десять семейств фактов шага, тридцать две колонки.
//
// Шаг перестал быть «машинной строчкой с вариациями»: установка фурнитуры, печать, подрезка,
// чистка концов, контроль и мокрая обработка — это разные вопросы, и каждый из них задаётся
// СВОИМ блоком. Блок — отдельное сообщение, а не набор плоских полей, ровно по одной причине:
// отсутствующий блок значит «бандл про это семейство молчит», и это единственный способ дать
// неосведомлённому клиенту сохранить шаг, не стерев того, чего он не показывает.
//
// Здесь живёт вся валидация волны, тремя эшелонами:
//
//  1. ШЕСТЬ REQUIRED-ДИСКРИМИНАТОРОВ, безусловных. Обязательность объявляет САМ ГЛАГОЛ, а не
//     флаг осведомлённости бандла: старый бандл физически не может прислать новый глагол (токена
//     нет в его словаре), а серверный round-trip строит payload из строк БД, где строки без
//     дискриминатора существовать не может — он был REQUIRED в момент записи.
//  2. NOT_APPLICABLE — по глаголу (блок целиком) и по ЯВНОМУ machine_type (отдельные поля).
//  3. Двух-полевые правила, которых в БД нет и быть не может: одноколоночный CHECK не видит
//     соседа, а двухколоночный ретроактивно проверяет всю историю таблицы.
//
// НИ ОДНО ПОЛЕ ВОЛНЫ, КРОМЕ ШЕСТИ ДИСКРИМИНАТОРОВ, НЕ REQUIRED. Пары (глагол, machine_type)
// семейств FA и S — buttonhole, button_attach, bartack, zipper_setting, binding_taping — живут в
// проде годами, старые бандлы легально пишут такие шаги без единого нового поля, и довод «глагол
// доказывает осведомлённость» к ним неприменим.
//
// Пути в FieldViolation — ПЛОСКИЕ (`operations[0].attach_method`), как у topstitch, и это не
// косметика: админка пришпиливает нарушение на контрол, конвертируя путь в имя поля формы, а
// форма держит эти поля плоскими. Вложенный путь (`operations[0].hardware.attachMethod`) не
// сматчил бы ничего и выродил бы точное сообщение в неатрибутируемый тост — см. field-errors.ts.
// Ключ пути = ИМЯ КОЛОНКИ БД, поэтому `placement_count`, `trim_action` и `cleaning_kind`, а не
// `count`, `action` и `kind`, как поля зовутся на проводе.

// Семнадцать карт proto↔токен волны. Как и всё остальное в этом пакете — СТРОЯТСЯ из слайса
// entity через enumTokenMap, который панике́т на init, если токену не нашлось члена enum. Ни один
// словарь здесь не выписан руками: словарь, записанный дважды, — словарь, который разъедется, а
// разъезжаются всегда те значения, которые никто не проверял.
var seamSecuringPbToToken = enumTokenMap[pb_common.TechCardSeamSecuring]("TECH_CARD_SEAM_SECURING_", entity.SeamSecuringTokens, pb_common.TechCardSeamSecuring_value)
var seamSecuringTokenToPb = invertTokenMap(seamSecuringPbToToken)

var hardwareAttachMethodPbToToken = enumTokenMap[pb_common.TechCardHardwareAttachMethod]("TECH_CARD_HARDWARE_ATTACH_METHOD_", entity.HardwareAttachMethodTokens, pb_common.TechCardHardwareAttachMethod_value)
var hardwareAttachMethodTokenToPb = invertTokenMap(hardwareAttachMethodPbToToken)

var holePrepPbToToken = enumTokenMap[pb_common.TechCardHolePrep]("TECH_CARD_HOLE_PREP_", entity.HolePrepTokens, pb_common.TechCardHolePrep_value)
var holePrepTokenToPb = invertTokenMap(holePrepPbToToken)

var reinforcementPbToToken = enumTokenMap[pb_common.TechCardReinforcement]("TECH_CARD_REINFORCEMENT_", entity.ReinforcementTokens, pb_common.TechCardReinforcement_value)
var reinforcementTokenToPb = invertTokenMap(reinforcementPbToToken)

var printMethodPbToToken = enumTokenMap[pb_common.TechCardPrintMethod]("TECH_CARD_PRINT_METHOD_", entity.PrintMethodTokens, pb_common.TechCardPrintMethod_value)
var printMethodTokenToPb = invertTokenMap(printMethodPbToToken)

var peelModePbToToken = enumTokenMap[pb_common.TechCardPeelMode]("TECH_CARD_PEEL_MODE_", entity.PeelModeTokens, pb_common.TechCardPeelMode_value)
var peelModeTokenToPb = invertTokenMap(peelModePbToToken)

var pressureScalePbToToken = enumTokenMap[pb_common.TechCardPressureScale]("TECH_CARD_PRESSURE_SCALE_", entity.PressureScaleTokens, pb_common.TechCardPressureScale_value)
var pressureScaleTokenToPb = invertTokenMap(pressureScalePbToToken)

var trimActionPbToToken = enumTokenMap[pb_common.TechCardTrimAction]("TECH_CARD_TRIM_ACTION_", entity.TrimActionTokens, pb_common.TechCardTrimAction_value)
var trimActionTokenToPb = invertTokenMap(trimActionPbToToken)

var cleaningKindPbToToken = enumTokenMap[pb_common.TechCardCleaningKind]("TECH_CARD_CLEANING_KIND_", entity.CleaningKindTokens, pb_common.TechCardCleaningKind_value)
var cleaningKindTokenToPb = invertTokenMap(cleaningKindPbToToken)

var inspectCoveragePbToToken = enumTokenMap[pb_common.TechCardInspectCoverage]("TECH_CARD_INSPECT_COVERAGE_", entity.InspectCoverageTokens, pb_common.TechCardInspectCoverage_value)
var inspectCoverageTokenToPb = invertTokenMap(inspectCoveragePbToToken)

var wetProcessKindPbToToken = enumTokenMap[pb_common.TechCardWetProcessKind]("TECH_CARD_WET_PROCESS_KIND_", entity.WetProcessKindTokens, pb_common.TechCardWetProcessKind_value)
var wetProcessKindTokenToPb = invertTokenMap(wetProcessKindPbToToken)

var buttonholeStylePbToToken = enumTokenMap[pb_common.TechCardButtonholeStyle]("TECH_CARD_BUTTONHOLE_STYLE_", entity.ButtonholeStyleTokens, pb_common.TechCardButtonholeStyle_value)
var buttonholeStyleTokenToPb = invertTokenMap(buttonholeStylePbToToken)

var buttonholeOrientationPbToToken = enumTokenMap[pb_common.TechCardButtonholeOrientation]("TECH_CARD_BUTTONHOLE_ORIENTATION_", entity.ButtonholeOrientationTokens, pb_common.TechCardButtonholeOrientation_value)
var buttonholeOrientationTokenToPb = invertTokenMap(buttonholeOrientationPbToToken)

var buttonAttachPatternPbToToken = enumTokenMap[pb_common.TechCardButtonAttachPattern]("TECH_CARD_BUTTON_ATTACH_PATTERN_", entity.ButtonAttachPatternTokens, pb_common.TechCardButtonAttachPattern_value)
var buttonAttachPatternTokenToPb = invertTokenMap(buttonAttachPatternPbToToken)

var zipperApplicationPbToToken = enumTokenMap[pb_common.TechCardZipperApplication]("TECH_CARD_ZIPPER_APPLICATION_", entity.ZipperApplicationTokens, pb_common.TechCardZipperApplication_value)
var zipperApplicationTokenToPb = invertTokenMap(zipperApplicationPbToToken)

var bindingStylePbToToken = enumTokenMap[pb_common.TechCardBindingStyle]("TECH_CARD_BINDING_STYLE_", entity.BindingStyleTokens, pb_common.TechCardBindingStyle_value)
var bindingStyleTokenToPb = invertTokenMap(bindingStylePbToToken)

var labelAttachStitchPbToToken = enumTokenMap[pb_common.TechCardLabelAttachStitch]("TECH_CARD_LABEL_ATTACH_STITCH_", entity.LabelAttachStitchTokens, pb_common.TechCardLabelAttachStitch_value)
var labelAttachStitchTokenToPb = invertTokenMap(labelAttachStitchPbToToken)

// Две карты ВТО-под-глагола (0325), тем же способом и по той же причине, что семнадцать выше.
var pressActionPbToToken = enumTokenMap[pb_common.TechCardPressAction]("TECH_CARD_PRESS_ACTION_", entity.PressActionTokens, pb_common.TechCardPressAction_value)
var pressActionTokenToPb = invertTokenMap(pressActionPbToToken)

var pressTowardPbToToken = enumTokenMap[pb_common.TechCardPressToward]("TECH_CARD_PRESS_TOWARD_", entity.PressTowardTokens, pb_common.TechCardPressToward_value)
var pressTowardTokenToPb = invertTokenMap(pressTowardPbToToken)

// Машинки, которые НАЗЫВАЮТ ПРАВИЛА этой волны. Литералами — как attachmentKindNone рядом, — но с
// проверкой на init: опечатка в такой строке не ломает сборку, она просто НИКОГДА не совпадает, и
// правило тихо перестаёт существовать. Сверка со словарём entity это ловит в первую же секунду
// жизни процесса.
const (
	machineButtonhole       = "buttonhole"
	machineButtonAttach     = "button_attach"
	machineBartack          = "bartack"
	machineBindingTaping    = "binding_taping"
	machineZipperSetting    = "zipper_setting"
	machineSeamTaping       = "seam_taping"
	machineUltrasonicWelder = "ultrasonic_welder"
)

var _ = func() struct{} {
	for _, m := range []string{
		machineButtonhole, machineButtonAttach, machineBartack, machineBindingTaping,
		machineZipperSetting, machineSeamTaping, machineUltrasonicWelder,
	} {
		if !entity.ValidMachineTypes[entity.TechCardMachineType(m)] {
			panic("operation kinds name a machine that is not in the vocabulary: " + m)
		}
	}
	return struct{}{}
}()

// Три дробных края диапазонов волны, разобранные один раз. Строкой из entity, а не float64:
// 0.3 в двоичном float — не 0.3, и граница начала бы отвергать ровно своё собственное значение.
var (
	needleGaugeMinMm = decimal.RequireFromString(entity.MinNeedleGaugeMm)
	needleGaugeMaxMm = decimal.RequireFromString(entity.MaxNeedleGaugeMm)
	fullnessRatioMin = decimal.RequireFromString(entity.MinFullnessRatio)
	fullnessRatioMax = decimal.RequireFromString(entity.MaxFullnessRatio)
	feedSpeedMinMMin = decimal.RequireFromString(entity.MinFeedSpeedMMin)
	feedSpeedMaxMMin = decimal.RequireFromString(entity.MaxFeedSpeedMMin)
)

// parseRangedDecimalBand — тот же parseRangedDecimal, но с ДРОБНЫМИ краями диапазона. Три поля
// волны (needle_gauge_mm 1.6..25.4, fullness_ratio 0.60..4.00, feed_speed_m_min 0.3..10.0) в
// целочисленные границы не укладываются, а округлить край до целого значило бы поменять правило.
func parseRangedDecimalBand(v *pb_decimal.Decimal, field string, maxFrac int32, min, max decimal.Decimal, hint string) (decimal.NullDecimal, error) {
	nd, err := nullDecimalFromPb(v)
	if err != nil {
		return decimal.NullDecimal{}, entity.NewFieldViolation(field, "invalid_number", v.GetValue(),
			"enter a plain number, e.g. 4.5")
	}
	if !nd.Valid {
		return nd, nil
	}
	if nd.Decimal.Exponent() < -maxFrac {
		return decimal.NullDecimal{}, entity.NewFieldViolation(field, "too_many_decimal_places", nd.Decimal.String(),
			fmt.Sprintf("round to at most %d decimal place(s) — the column stores no more, so the rest would be dropped silently", maxFrac))
	}
	if nd.Decimal.LessThan(min) || nd.Decimal.GreaterThan(max) {
		return decimal.NullDecimal{}, entity.NewFieldViolation(field, "out_of_range", nd.Decimal.String(),
			fmt.Sprintf("%s (%s to %s); clear the field to leave it unset", hint, min.String(), max.String()))
	}
	return nd, nil
}

// operationKindFields — все тридцать две колонки волны одним значением, чтобы цикл разбора не
// оброс тридцатью двумя нулевыми литералами на каждом шаге, которому семейство не принадлежит.
// ПОРЯДОК ПОЛЕЙ — КАНОН §1, тот же, что в entity.TechCardOperation, в ALTER'е миграции и в
// INSERT/SELECT стора: четыре списка, расхождение между которыми молчит до первого сохранения.
type operationKindFields struct {
	needleCount   sql.NullInt32
	needleGaugeMm decimal.NullDecimal
	seamSecuring  sql.NullString
	rowSpacingMm  decimal.NullDecimal
	fullnessRatio decimal.NullDecimal

	placementCount sql.NullInt32
	pitchMm        decimal.NullDecimal

	attachMethod     sql.NullString
	holePrep         sql.NullString
	reinforcement    sql.NullString
	foldbackMm       decimal.NullDecimal
	cycleStitchCount sql.NullInt32

	printMethod    sql.NullString
	peelMode       sql.NullString
	secondPressSec sql.NullInt32
	pressureScale  sql.NullString

	airTemperatureC sql.NullInt32
	feedSpeedMMin   decimal.NullDecimal

	trimAction          sql.NullString
	residualAllowanceMm decimal.NullDecimal

	residualTailMaxMm decimal.NullDecimal

	cleaningKind   sql.NullString
	coverageMode   sql.NullString
	wetProcessKind sql.NullString

	buttonholeStyle       sql.NullString
	cutLengthMm           decimal.NullDecimal
	buttonholeOrientation sql.NullString
	bartackLengthMm       decimal.NullDecimal
	attachPattern         sql.NullString
	zipperApplication     sql.NullString
	bindingStyle          sql.NullString
	labelAttachStitch     sql.NullString

	// ВТО (0325) — продолжение канона дописыванием, последними.
	pressAction sql.NullString
	pressToward sql.NullString
}

// verbNotApplicable — отказ «это семейство не про этот глагол». Значение нарушения — САМ ГЛАГОЛ:
// именно он делает поле неприменимым, и именно его оператор либо меняет, либо очищает поле.
func verbNotApplicable(step, field string, opType entity.TechCardOperationType, belongs string) error {
	return entity.NewFieldViolation(step+"."+field, "not_applicable", string(opType),
		fmt.Sprintf("%s; this step is %q — clear the field or switch the step type", belongs, string(opType)))
}

// machineNotApplicable — отказ «это поле не про эту машинку». Применимость считается ТОЛЬКО по
// ЯВНОМУ machine_type НА ШАГЕ: тип, унаследованный через machine_profile_key, НЕ засчитывается —
// иначе одно и то же поле было бы законным или нет в зависимости от того, что лежит в парке
// карточки, а профиль можно удалить, не открыв ни одного шага. Правило одно на все семейства:
// валидация читает одно место, форма материализует machine_type при заполнении зависящего поля.
func machineNotApplicable(step, field string, machineType sql.NullString, wants []string) error {
	if !machineType.Valid {
		return entity.NewFieldViolation(step+"."+field, "not_applicable", "",
			fmt.Sprintf("this setting belongs to a %s step and this step names no machine of its own — a type inherited through machine_profile_key does not count, so set machine_type here or clear the field",
				strings.Join(wants, " / ")))
	}
	return entity.NewFieldViolation(step+"."+field, "not_applicable", machineType.String,
		fmt.Sprintf("this setting belongs to a %s step; this one runs on %q — clear the field or change the machine",
			strings.Join(wants, " / "), machineType.String))
}

func machineIsOneOf(machineType sql.NullString, wants ...string) bool {
	if !machineType.Valid {
		return false
	}
	for _, w := range wants {
		if machineType.String == w {
			return true
		}
	}
	return false
}

// parseOperationKindFields разбирает десять блоков шага и все правила волны. Вызывается из
// parseTechCardOperations рядом с parseOperationEquipment и по его же образцу: сначала отказ
// «блок не про этот глагол» (он называет КОНКРЕТНОЕ заполненное поле, а не жестикулирует блоком),
// потом REQUIRED-дискриминатор, потом разбор с диапазонами, потом двух-полевые правила.
//
// machineType — ЯВНЫЙ тип шага, каким его вернула canonicaliseOperationType (легаси-глагол,
// назвавший машинку, тоже явный: он материализуется в ту же колонку). Резолв через
// machine_profile_key сюда не доходит вовсе и по правилу волны не засчитывается.
func parseOperationKindFields(o *pb_common.TechCardOperation, opType entity.TechCardOperationType,
	machineType sql.NullString, step string,
) (operationKindFields, error) {
	var f operationKindFields
	var err error

	st := o.GetStitching()
	pl := o.GetPlacementLayout()
	hw := o.GetHardware()
	pr := o.GetPrint()
	wl := o.GetWeld()
	tr := o.GetTrim()
	tt := o.GetThreadTrim()
	cl := o.GetClean()
	in := o.GetInspect()
	fa := o.GetFastening()

	isMachine := opType == entity.OpTypeMachine
	isHardware := opType == entity.OpTypeHardwareSet
	isPrint := opType == entity.OpTypePrint
	isTrim := opType == entity.OpTypeTrim
	isThreadTrim := opType == entity.OpTypeThreadTrim
	isClean := opType == entity.OpTypeClean
	isInspect := opType == entity.OpTypeInspect
	isWetProcess := opType == entity.OpTypeWetProcess

	// --- S: строчка (только MACHINE) ------------------------------------------------------------
	if !isMachine {
		if fld := firstPopulated([]populatedField{
			{"needle_count", st.GetNeedleCount() != 0},
			{"needle_gauge_mm", decimalSent(st.GetNeedleGaugeMm())},
			{"seam_securing", st.GetSeamSecuring() != pb_common.TechCardSeamSecuring_TECH_CARD_SEAM_SECURING_UNKNOWN},
			{"row_spacing_mm", decimalSent(st.GetRowSpacingMm())},
			{"fullness_ratio", decimalSent(st.GetFullnessRatio())},
			{"binding_style", st.GetBindingStyle() != pb_common.TechCardBindingStyle_TECH_CARD_BINDING_STYLE_UNKNOWN},
			{"label_attach_stitch", st.GetLabelAttachStitch() != pb_common.TechCardLabelAttachStitch_TECH_CARD_LABEL_ATTACH_STITCH_UNKNOWN},
		}); fld != "" {
			return f, verbNotApplicable(step, fld, opType, "the stitch-line settings belong to a machine step")
		}
	} else {
		f.needleCount, err = parseRangedInt32(st.GetNeedleCount(), entity.MinNeedleCount, entity.MaxNeedleCount,
			step+".needle_count", "needles in ONE stitch line")
		if err != nil {
			return f, err
		}
		f.needleGaugeMm, err = parseRangedDecimalBand(st.GetNeedleGaugeMm(), step+".needle_gauge_mm", 1,
			needleGaugeMinMm, needleGaugeMaxMm, "the gap BETWEEN NEEDLES in mm")
		if err != nil {
			return f, err
		}
		// Двух-полевое правило 1. Калибр — это расстояние МЕЖДУ иглами: у одной иглы соседа нет, и
		// число рядом с ней описывает ничто, а на печатном листе выглядит как настройка.
		if f.needleGaugeMm.Valid && !(f.needleCount.Valid && f.needleCount.Int32 >= 2) {
			return f, entity.NewFieldViolation(step+".needle_gauge_mm", "needs_needle_count", f.needleGaugeMm.Decimal.String(),
				"a needle gauge is the gap BETWEEN two needles — set needle_count to 2 or more, or clear the gauge")
		}
		f.seamSecuring, err = parseEquipmentEnum(st.GetSeamSecuring(), seamSecuringPbToToken,
			step+".seam_securing", "pick how the line is secured, or «none» for no securing at all")
		if err != nil {
			return f, err
		}
		f.rowSpacingMm, err = parseRangedDecimal(st.GetRowSpacingMm(), step+".row_spacing_mm", 1,
			entity.MinRowSpacingMm, entity.MaxRowSpacingMm, "the gap between stitch ROWS in mm")
		if err != nil {
			return f, err
		}
		f.fullnessRatio, err = parseRangedDecimalBand(st.GetFullnessRatio(), step+".fullness_ratio", 2,
			fullnessRatioMin, fullnessRatioMax, "ease / gathering as a RATIO, not a percentage (1.0 = layers run one to one)")
		if err != nil {
			return f, err
		}
		f.bindingStyle, err = parseEquipmentEnum(st.GetBindingStyle(), bindingStylePbToToken,
			step+".binding_style", "pick how the binding is folded")
		if err != nil {
			return f, err
		}
		if f.bindingStyle.Valid && !machineIsOneOf(machineType, machineBindingTaping) {
			return f, machineNotApplicable(step, "binding_style", machineType, []string{machineBindingTaping})
		}
		// label_attach_stitch — единственное поле дельты БЕЗ правила по типу машинки: этикетку
		// сажают на чём угодно, и требование назвать машинку сделало бы поле недоступным ровно там,
		// где оно чаще всего и нужно.
		f.labelAttachStitch, err = parseEquipmentEnum(st.GetLabelAttachStitch(), labelAttachStitchPbToToken,
			step+".label_attach_stitch", "pick which sides the label is stitched by")
		if err != nil {
			return f, err
		}
	}

	// --- PL: раскладка повторов (MACHINE | HARDWARE_SET | PRINT) ---------------------------------
	if !(isMachine || isHardware || isPrint) {
		if fld := firstPopulated([]populatedField{
			{"placement_count", pl.GetCount() != 0},
			{"pitch_mm", decimalSent(pl.GetPitchMm())},
		}); fld != "" {
			return f, verbNotApplicable(step, fld, opType,
				"the repeat layout belongs to a machine, hardware_set or print step")
		}
	} else {
		f.placementCount, err = parseRangedInt32(pl.GetCount(), entity.MinPlacementCount, entity.MaxPlacementCount,
			step+".placement_count", "repeats on the garment")
		if err != nil {
			return f, err
		}
		f.pitchMm, err = parseRangedDecimal(pl.GetPitchMm(), step+".pitch_mm", 1,
			entity.MinPitchMm, entity.MaxPitchMm, "the gap between repeats in mm")
		if err != nil {
			return f, err
		}
		// Двух-полевое правило 2, тот же довод, что у калибра: шаг существует между повторами.
		if f.pitchMm.Valid && !(f.placementCount.Valid && f.placementCount.Int32 >= 2) {
			return f, entity.NewFieldViolation(step+".pitch_mm", "needs_placement_count", f.pitchMm.Decimal.String(),
				"a pitch is the gap BETWEEN repeats — set the count to 2 or more, or clear the pitch")
		}
	}

	// --- H: фурнитура (HARDWARE_SET целиком; MACHINE + цикловые — три поля из пяти) ---------------
	//
	// Три поля семейства живут на ДВУХ разных глаголах, и это не оплошность: петельный, закрепочный
	// и пуговичный автоматы готовят отверстие, требуют усилителя и считают стежки цикла ровно так
	// же, как ручная установка фурнитуры. А вот attach_method и foldback_mm на них бессмысленны:
	// как держится пуговица, решает программа автомата, а подгиб стропы — вопрос пряжки.
	cycleMachine := isMachine && machineIsOneOf(machineType, machineButtonhole, machineButtonAttach, machineBartack)
	// Две половины семейства названы ОТДЕЛЬНО, а не индексами общего списка: перестановка полей в
	// одном списке молча поменяла бы, что именно отвергается на цикловом автомате.
	hardwareSetOnly := []populatedField{
		{"attach_method", hw.GetAttachMethod() != pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_UNKNOWN},
		{"foldback_mm", decimalSent(hw.GetFoldbackMm())},
	}
	hardwareShared := []populatedField{
		{"hole_prep", hw.GetHolePrep() != pb_common.TechCardHolePrep_TECH_CARD_HOLE_PREP_UNKNOWN},
		{"reinforcement", hw.GetReinforcement() != pb_common.TechCardReinforcement_TECH_CARD_REINFORCEMENT_UNKNOWN},
		{"cycle_stitch_count", hw.GetCycleStitchCount() != 0},
	}
	switch {
	case !isHardware && !cycleMachine:
		if fld := firstPopulated(append(append([]populatedField{}, hardwareSetOnly...), hardwareShared...)); fld != "" {
			return f, verbNotApplicable(step, fld, opType,
				"the hardware settings belong to a hardware_set step (and, in part, to a buttonhole / button_attach / bartack machine)")
		}
	case isHardware:
		f.attachMethod, err = parseEquipmentEnum(hw.GetAttachMethod(), hardwareAttachMethodPbToToken,
			step+".attach_method", "pick how the hardware is held — sewn, prong-clinched, press-set, crimped or threaded")
		if err != nil {
			return f, err
		}
		// REQUIRED-дискриминатор 1 из 6. Проверяется БЕЗУСЛОВНО: глагол hardware_set умеет выбрать
		// только бандл, который знает и это поле.
		if !f.attachMethod.Valid {
			return f, entity.NewFieldViolation(step+".attach_method", "required", "",
				"say HOW the hardware is held on — it decides the tool, the hole and the reinforcement")
		}
		f.foldbackMm, err = parseRangedDecimal(hw.GetFoldbackMm(), step+".foldback_mm", 1,
			entity.MinFoldbackMm, entity.MaxFoldbackMm, "the webbing fold-back through the buckle in mm")
		if err != nil {
			return f, err
		}
		// Двух-полевое правило 3: подгиб бывает только у продетой стропы.
		if f.foldbackMm.Valid && f.attachMethod.String != string(entity.HardwareAttachThreaded) {
			return f, entity.NewFieldViolation(step+".foldback_mm", "needs_attach_method", f.foldbackMm.Decimal.String(),
				fmt.Sprintf("a fold-back exists only on webbing threaded through the part; this step attaches by %q — clear it or switch to «threaded»",
					f.attachMethod.String))
		}
	default: // цикловой автомат: attach_method и foldback_mm неприменимы
		if fld := firstPopulated(hardwareSetOnly); fld != "" {
			return f, entity.NewFieldViolation(step+"."+fld, "not_applicable", machineType.String,
				fmt.Sprintf("a %s machine sets this by its own program; only the hole prep, the reinforcement and the cycle stitch count belong here — clear the field or make it a hardware_set step",
					machineType.String))
		}
	}
	if isHardware || cycleMachine {
		f.holePrep, err = parseEquipmentEnum(hw.GetHolePrep(), holePrepPbToToken,
			step+".hole_prep", "pick how the hole is made, or «none» when nothing is pierced")
		if err != nil {
			return f, err
		}
		f.reinforcement, err = parseEquipmentEnum(hw.GetReinforcement(), reinforcementPbToToken,
			step+".reinforcement", "pick what backs the spot, or «none» for nothing at all")
		if err != nil {
			return f, err
		}
		f.cycleStitchCount, err = parseRangedInt32(hw.GetCycleStitchCount(), entity.MinCycleStitchCount, entity.MaxCycleStitchCount,
			step+".cycle_stitch_count", "stitches in one machine cycle")
		if err != nil {
			return f, err
		}
	}

	// --- P: печать (только PRINT) ---------------------------------------------------------------
	printAll := []populatedField{
		{"print_method", o.GetPrintMethod() != pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_UNKNOWN},
		{"peel_mode", pr.GetPeelMode() != pb_common.TechCardPeelMode_TECH_CARD_PEEL_MODE_UNKNOWN},
		{"second_press_sec", pr.GetSecondPressSec() != 0},
		{"pressure_scale", pr.GetPressureScale() != pb_common.TechCardPressureScale_TECH_CARD_PRESSURE_SCALE_UNKNOWN},
	}
	if !isPrint {
		if fld := firstPopulated(printAll); fld != "" {
			return f, verbNotApplicable(step, fld, opType, "the print settings belong to a print step")
		}
	} else {
		f.printMethod, err = parseEquipmentEnum(o.GetPrintMethod(), printMethodPbToToken,
			step+".print_method", "pick the method — screen, DTF, heat transfer, foil or laser engraving")
		if err != nil {
			return f, err
		}
		// REQUIRED-дискриминатор 2 из 6.
		if !f.printMethod.Valid {
			return f, entity.NewFieldViolation(step+".print_method", "required", "",
				"say HOW it is printed — the method decides the carrier, the peel and the press")
		}
		// Групповое правило: у лазерной гравировки нет ни носителя, ни прижима. Поэтому вместе с
		// тремя полями печати отвергается и ВЕСЬ ВТО-блок шага — при остальных методах он законен
		// (термопресс это то же оборудование, второй комплект тех же полей был бы дублём).
		if f.printMethod.String == string(entity.PrintMethodLaserEngrave) {
			if fld := firstPopulated(printAll[1:]); fld != "" {
				return f, entity.NewFieldViolation(step+"."+fld, "not_applicable", f.printMethod.String,
					"laser engraving burns the cloth itself — there is no carrier to peel and no press to set; clear the field or change the method")
			}
			if fld := firstPopulated([]populatedField{
				{"press_equipment", o.GetPressEquipment() != pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_UNKNOWN},
				{"press_profile_key", strings.TrimSpace(o.GetPressProfileKey()) != ""},
				{"press_temperature_c", o.GetPressTemperatureC() != 0},
				{"press_dwell_sec", o.GetPressDwellSec() != 0},
				{"press_pressure_n_cm2", decimalSent(o.GetPressPressureNCm2())},
				{"press_steam", o.PressSteam != nil},
				{"press_cloth", o.GetPressCloth() != pb_common.TechCardPressCloth_TECH_CARD_PRESS_CLOTH_UNKNOWN},
			}); fld != "" {
				return f, entity.NewFieldViolation(step+"."+fld, "not_applicable", f.printMethod.String,
					"laser engraving involves no pressing at all — clear the pressing settings or change the method")
			}
		}
		f.peelMode, err = parseEquipmentEnum(pr.GetPeelMode(), peelModePbToToken,
			step+".peel_mode", "pick when the carrier comes off, or «none» when there is no carrier")
		if err != nil {
			return f, err
		}
		f.secondPressSec, err = parseRangedInt32(pr.GetSecondPressSec(), entity.MinSecondPressSec, entity.MaxSecondPressSec,
			step+".second_press_sec", "the second press in seconds")
		if err != nil {
			return f, err
		}
		f.pressureScale, err = parseEquipmentEnum(pr.GetPressureScale(), pressureScalePbToToken,
			step+".pressure_scale", "pick the pressure on the scale — light, medium or firm")
		if err != nil {
			return f, err
		}
	}

	// --- W: сварка и проклейка (MACHINE + ЯВНЫЙ seam_taping | ultrasonic_welder) ------------------
	weldMachines := []string{machineSeamTaping, machineUltrasonicWelder}
	weldMachine := isMachine && machineIsOneOf(machineType, weldMachines...)
	weldAll := []populatedField{
		{"air_temperature_c", wl.GetAirTemperatureC() != 0},
		{"feed_speed_m_min", decimalSent(wl.GetFeedSpeedMMin())},
	}
	switch {
	case !isMachine:
		if fld := firstPopulated(weldAll); fld != "" {
			return f, verbNotApplicable(step, fld, opType, "the welding settings belong to a machine step")
		}
	case !weldMachine:
		if fld := firstPopulated(weldAll); fld != "" {
			return f, machineNotApplicable(step, fld, machineType, weldMachines)
		}
	default:
		f.airTemperatureC, err = parseRangedInt32(wl.GetAirTemperatureC(), entity.MinAirTemperatureC, entity.MaxAirTemperatureC,
			step+".air_temperature_c", "hot-air temperature in °C")
		if err != nil {
			return f, err
		}
		// Двух-полевое правило 4: у ультразвука горячего воздуха нет вовсе — шов варится
		// колебаниями, и температура рядом с ним описывает несуществующий узел машины.
		if f.airTemperatureC.Valid && !machineIsOneOf(machineType, machineSeamTaping) {
			return f, machineNotApplicable(step, "air_temperature_c", machineType, []string{machineSeamTaping})
		}
		f.feedSpeedMMin, err = parseRangedDecimalBand(wl.GetFeedSpeedMMin(), step+".feed_speed_m_min", 1,
			feedSpeedMinMMin, feedSpeedMaxMMin, "feed speed in m/min")
		if err != nil {
			return f, err
		}
	}
	// Групповое правило (правка живого 0306): у обеих машин волны НЕТ НИ ИГЛЫ, НИ НИТКИ. Ниточно-
	// игольные overrides на таком шаге — теневые значения: ничего их не читает, ничего не печатает,
	// а следующий редактор им верит. Проверка висит на ЯВНОМ типе шага, а не на заполненности
	// weld-блока: шаг на сварочной машине остаётся сварочным и без единого поля семейства.
	//
	// Четыре поля S-блока волны попадают сюда по тому же признаку: своё семейство гейтит их только
	// «это машинный шаг», а сварочная машина машинная — без этого правила на безыгольном шаге
	// сохранились бы «4 иглы с шагом 3.2 мм и закрепка».
	//
	// fullness_ratio из S-блока НЕ отвергается СОЗНАТЕЛЬНО: посадка — соотношение длин двух слоёв
	// при подаче, свойство ПОДАЧИ, а не иглы. Сварочная машина слои подаёт (у неё на то и есть
	// feed_speed_m_min), и посаженный шов на ней осмыслен ровно так же, как на ниточной.
	if isMachine && machineIsOneOf(machineType, weldMachines...) {
		if fld := firstPopulated([]populatedField{
			{"thread_count", o.GetThreadCount() != 0},
			{"needle_type", o.GetNeedleType() != pb_common.TechCardNeedleType_TECH_CARD_NEEDLE_TYPE_UNKNOWN},
			{"needle_size_nm", o.GetNeedleSizeNm() != 0},
			{"thread_tension", o.GetThreadTension() != pb_common.TechCardThreadTension_TECH_CARD_THREAD_TENSION_UNKNOWN},
			{"thread_tension_note", strings.TrimSpace(o.GetThreadTensionNote()) != ""},
			{"stitch_width_mm", decimalSent(o.GetStitchWidthMm())},
			// Калибр стоит ПЕРЕД числом игл намеренно: одиночный калибр до этого правила не доходит
			// вовсе — его раньше отвергает правило 1 (needs_needle_count), — и при обратном порядке
			// отказ на законной паре «2 иглы + калибр» никогда бы калибр не назвал.
			{"needle_gauge_mm", decimalSent(st.GetNeedleGaugeMm())},
			{"needle_count", st.GetNeedleCount() != 0},
			{"seam_securing", st.GetSeamSecuring() != pb_common.TechCardSeamSecuring_TECH_CARD_SEAM_SECURING_UNKNOWN},
			{"row_spacing_mm", decimalSent(st.GetRowSpacingMm())},
		}); fld != "" {
			return f, entity.NewFieldViolation(step+"."+fld, "not_applicable", machineType.String,
				fmt.Sprintf("a %s machine has neither needle nor thread — the seam is welded, not sewn; clear the field or change the machine",
					machineType.String))
		}
	}

	// --- T: подрезка и выправка (только TRIM) ----------------------------------------------------
	if !isTrim {
		if fld := firstPopulated([]populatedField{
			{"trim_action", tr.GetAction() != pb_common.TechCardTrimAction_TECH_CARD_TRIM_ACTION_UNKNOWN},
			{"residual_allowance_mm", decimalSent(tr.GetResidualAllowanceMm())},
		}); fld != "" {
			return f, verbNotApplicable(step, fld, opType, "the trimming settings belong to a trim step")
		}
	} else {
		f.trimAction, err = parseEquipmentEnum(tr.GetAction(), trimActionPbToToken,
			step+".trim_action", "pick what the trimming DOES — even up, grade, clip, notch, corner or turn out")
		if err != nil {
			return f, err
		}
		// REQUIRED-дискриминатор 3 из 6.
		if !f.trimAction.Valid {
			return f, entity.NewFieldViolation(step+".trim_action", "required", "",
				"say WHAT is being trimmed — «подрезать» on its own is six different operations")
		}
		f.residualAllowanceMm, err = parseRangedDecimal(tr.GetResidualAllowanceMm(), step+".residual_allowance_mm", 1,
			entity.MinResidualAllowanceMm, entity.MaxResidualAllowanceMm,
			"how much allowance is LEFT after the cut, in mm — this is not the seam allowance the piece was cut with")
		if err != nil {
			return f, err
		}
	}

	// --- F: чистка концов ниток (только THREAD_TRIM) ---------------------------------------------
	if !isThreadTrim {
		if fld := firstPopulated([]populatedField{
			{"residual_tail_max_mm", decimalSent(tt.GetResidualTailMaxMm())},
		}); fld != "" {
			return f, verbNotApplicable(step, fld, opType, "the thread-tail limit belongs to a thread_trim step")
		}
	} else {
		f.residualTailMaxMm, err = parseRangedDecimal(tt.GetResidualTailMaxMm(), step+".residual_tail_max_mm", 1,
			entity.MinResidualTailMaxMm, entity.MaxResidualTailMaxMm,
			"the longest thread tail allowed, in mm; leave it unset for the shop standard")
		if err != nil {
			return f, err
		}
	}

	// --- C: чистка изделия (только CLEAN) --------------------------------------------------------
	if !isClean {
		if fld := firstPopulated([]populatedField{
			{"cleaning_kind", cl.GetKind() != pb_common.TechCardCleaningKind_TECH_CARD_CLEANING_KIND_UNKNOWN},
		}); fld != "" {
			return f, verbNotApplicable(step, fld, opType, "the cleaning kind belongs to a clean step")
		}
	} else {
		f.cleaningKind, err = parseEquipmentEnum(cl.GetKind(), cleaningKindPbToToken,
			step+".cleaning_kind", "pick what is cleaned off — a spot, dust and lint, chalk or adhesive")
		if err != nil {
			return f, err
		}
		// REQUIRED-дискриминатор 4 из 6.
		if !f.cleaningKind.Valid {
			return f, entity.NewFieldViolation(step+".cleaning_kind", "required", "",
				"say WHAT is cleaned off — the agent and the risk differ for every answer")
		}
	}

	// --- Q: контроль (только INSPECT) ------------------------------------------------------------
	if !isInspect {
		if fld := firstPopulated([]populatedField{
			{"coverage_mode", in.GetCoverageMode() != pb_common.TechCardInspectCoverage_TECH_CARD_INSPECT_COVERAGE_UNKNOWN},
		}); fld != "" {
			return f, verbNotApplicable(step, fld, opType, "the inspection coverage belongs to an inspect step")
		}
	} else {
		f.coverageMode, err = parseEquipmentEnum(in.GetCoverageMode(), inspectCoveragePbToToken,
			step+".coverage_mode", "pick what is inspected — every unit, a sample per bundle, an AQL plan or the first output")
		if err != nil {
			return f, err
		}
		// REQUIRED-дискриминатор 5 из 6.
		if !f.coverageMode.Valid {
			return f, entity.NewFieldViolation(step+".coverage_mode", "required", "",
				"say HOW MUCH is inspected — «проверить» without the coverage is not an instruction, it is a wish")
		}
	}

	// --- WP: мокрая обработка (только WET_PROCESS) -----------------------------------------------
	if !isWetProcess {
		if fld := firstPopulated([]populatedField{
			{"wet_process_kind", o.GetWetProcessKind() != pb_common.TechCardWetProcessKind_TECH_CARD_WET_PROCESS_KIND_UNKNOWN},
		}); fld != "" {
			return f, verbNotApplicable(step, fld, opType, "the wet-process kind belongs to a wet_process step")
		}
	} else {
		f.wetProcessKind, err = parseEquipmentEnum(o.GetWetProcessKind(), wetProcessKindPbToToken,
			step+".wet_process_kind", "pick the wet process — rinse, enzyme, garment dye or softener")
		if err != nil {
			return f, err
		}
		// REQUIRED-дискриминатор 6 из 6.
		if !f.wetProcessKind.Valid {
			return f, entity.NewFieldViolation(step+".wet_process_kind", "required", "",
				"say WHICH wet process — they differ in the machine, the chemistry and the shrinkage")
		}
	}

	// --- FA: петли, закрепки, пуговицы, молнии (только MACHINE, каждое поле — при своей машинке) --
	//
	// REQUIRED здесь нет ни одного, и это отдельное решение: эти глаголы и эти машинки живут в
	// проде годами, старые бандлы пишут такие шаги без единого нового поля, и существующая строка
	// обязана пройти круг без отказа.
	if !isMachine {
		if fld := firstPopulated([]populatedField{
			{"buttonhole_style", fa.GetButtonholeStyle() != pb_common.TechCardButtonholeStyle_TECH_CARD_BUTTONHOLE_STYLE_UNKNOWN},
			{"cut_length_mm", decimalSent(fa.GetCutLengthMm())},
			{"buttonhole_orientation", fa.GetButtonholeOrientation() != pb_common.TechCardButtonholeOrientation_TECH_CARD_BUTTONHOLE_ORIENTATION_UNKNOWN},
			{"bartack_length_mm", decimalSent(fa.GetBartackLengthMm())},
			{"attach_pattern", fa.GetAttachPattern() != pb_common.TechCardButtonAttachPattern_TECH_CARD_BUTTON_ATTACH_PATTERN_UNKNOWN},
			{"zipper_application", fa.GetZipperApplication() != pb_common.TechCardZipperApplication_TECH_CARD_ZIPPER_APPLICATION_UNKNOWN},
		}); fld != "" {
			return f, verbNotApplicable(step, fld, opType, "the fastening settings belong to a machine step")
		}
	} else {
		f.buttonholeStyle, err = parseEquipmentEnum(fa.GetButtonholeStyle(), buttonholeStylePbToToken,
			step+".buttonhole_style", "pick the buttonhole shape")
		if err != nil {
			return f, err
		}
		if f.buttonholeStyle.Valid && !machineIsOneOf(machineType, machineButtonhole) {
			return f, machineNotApplicable(step, "buttonhole_style", machineType, []string{machineButtonhole})
		}
		f.cutLengthMm, err = parseRangedDecimal(fa.GetCutLengthMm(), step+".cut_length_mm", 1,
			entity.MinCutLengthMm, entity.MaxCutLengthMm, "the buttonhole cut in mm")
		if err != nil {
			return f, err
		}
		if f.cutLengthMm.Valid && !machineIsOneOf(machineType, machineButtonhole) {
			return f, machineNotApplicable(step, "cut_length_mm", machineType, []string{machineButtonhole})
		}
		f.buttonholeOrientation, err = parseEquipmentEnum(fa.GetButtonholeOrientation(), buttonholeOrientationPbToToken,
			step+".buttonhole_orientation", "pick how the buttonhole lies")
		if err != nil {
			return f, err
		}
		if f.buttonholeOrientation.Valid && !machineIsOneOf(machineType, machineButtonhole) {
			return f, machineNotApplicable(step, "buttonhole_orientation", machineType, []string{machineButtonhole})
		}
		f.bartackLengthMm, err = parseRangedDecimal(fa.GetBartackLengthMm(), step+".bartack_length_mm", 1,
			entity.MinBartackLengthMm, entity.MaxBartackLengthMm, "the bartack length in mm")
		if err != nil {
			return f, err
		}
		// Закрепка живёт на двух машинках: петельная закрепляет концы прорези, закрепочный автомат
		// ставит её отдельным шагом.
		if f.bartackLengthMm.Valid && !machineIsOneOf(machineType, machineButtonhole, machineBartack) {
			return f, machineNotApplicable(step, "bartack_length_mm", machineType, []string{machineButtonhole, machineBartack})
		}
		f.attachPattern, err = parseEquipmentEnum(fa.GetAttachPattern(), buttonAttachPatternPbToToken,
			step+".attach_pattern", "pick the button stitch pattern")
		if err != nil {
			return f, err
		}
		if f.attachPattern.Valid && !machineIsOneOf(machineType, machineButtonAttach) {
			return f, machineNotApplicable(step, "attach_pattern", machineType, []string{machineButtonAttach})
		}
		f.zipperApplication, err = parseEquipmentEnum(fa.GetZipperApplication(), zipperApplicationPbToToken,
			step+".zipper_application", "pick how the zip is set")
		if err != nil {
			return f, err
		}
		if f.zipperApplication.Valid && !machineIsOneOf(machineType, machineZipperSetting) {
			return f, machineNotApplicable(step, "zipper_application", machineType, []string{machineZipperSetting})
		}
	}

	// --- ВТО: под-глагол и направление припуска (0325) -------------------------------------------
	//
	// НИ ОДНО ИЗ ДВУХ ПОЛЕЙ НЕ REQUIRED САМО ПО СЕБЕ, в отличие от шести дискриминаторов выше.
	// press_toward обязателен ТОЛЬКО в присутствии press_action = to_one_side — значения, которого
	// ни одна сохранённая строка иметь не может (обе колонки рождаются миграцией 0325). Поэтому
	// ретроактивной обязательности здесь не возникает НИГДЕ: старый шаг press без под-глагола
	// проходит этот блок без единого отказа, как проходил до волны.
	ps := o.GetPress()
	isPress := opType == entity.OpTypePress
	isPressOpen := opType == entity.OpTypePressOpen
	if !(isPress || isPressOpen) {
		// FUSING и PRINT сюда попадают НАМЕРЕННО, хотя ВТО-блок 39..45 при них законен: там
		// оборудование и режим прижима, а здесь — ЧТО ДЕЛАЮТ С ПРИПУСКОМ. У дублирования и у
		// печати припуска нет вовсе, и «заутюжить на полочку» на таком шаге описывает ничто.
		if fld := firstPopulated([]populatedField{
			{"press_action", ps.GetAction() != pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_UNKNOWN},
			{"press_toward", ps.GetToward() != pb_common.TechCardPressToward_TECH_CARD_PRESS_TOWARD_UNKNOWN},
		}); fld != "" {
			return f, verbNotApplicable(step, fld, opType,
				"the pressing sub-verb and the allowance direction belong to a press or press_open step")
		}
	} else {
		f.pressAction, err = parseEquipmentEnum(ps.GetAction(), pressActionPbToToken,
			step+".press_action", "pick WHAT the pressing does — press flat, to one side, open, steam, final, ease in, stretch or mould")
		if err != nil {
			return f, err
		}
		// PRESS_OPEN — САМ СЕБЕ ПОД-ГЛАГОЛ. Разутюжка уже названа глаголом, и второй ответ на тот
		// же вопрос был бы двумя ответами. Пустой press_action здесь законен и КАНОНИЧЕН: пикер на
		// этом шаге его не пишет вовсе, а `open` принимается только на чтение — форма не
		// переписывает одно написание в другое, иначе подписанная карточка протухла бы без единой
		// человеческой правки.
		if isPressOpen && f.pressAction.Valid && f.pressAction.String != string(entity.PressActionOpen) {
			return f, entity.NewFieldViolation(step+".press_action", "not_applicable", f.pressAction.String,
				fmt.Sprintf("this step is «press_open» — the verb already says the allowance is pressed open; %q is a different technique, so switch the step to «press» or clear the field",
					f.pressAction.String))
		}
		f.pressToward, err = parseEquipmentEnum(ps.GetToward(), pressTowardPbToToken,
			step+".press_toward", "pick WHERE the allowance ends up — the dictionary names its destination, so every seam has exactly one answer")
		if err != nil {
			return f, err
		}
		// Двух-полевое правило: направление есть только у заутюженного припуска. У приутюженного,
		// разутюженного и отпаренного стороны нет вовсе, и «на полочку» рядом с ними — теневое
		// значение: ничего его не читает, ничего не печатает, а следующий редактор ему верит.
		if f.pressToward.Valid && !(f.pressAction.Valid && f.pressAction.String == string(entity.PressActionToOneSide)) {
			said := ""
			if f.pressAction.Valid {
				said = f.pressAction.String
			}
			return f, entity.NewFieldViolation(step+".press_toward", "needs_press_action", f.pressToward.String,
				fmt.Sprintf("only an allowance pressed TO ONE SIDE has a side; this step says %q — set the action to «to one side» or clear the direction",
					said))
		}
		// И обратное: «заутюжить» без стороны — ровно та дыра, ради которой заведены обе колонки.
		// Обязательность УСЛОВНАЯ и потому не ретроактивная: to_one_side в базе сегодня нет.
		if f.pressAction.Valid && f.pressAction.String == string(entity.PressActionToOneSide) && !f.pressToward.Valid {
			return f, entity.NewFieldViolation(step+".press_toward", "required", "",
				"say WHICH SIDE the allowance is pressed to — «заутюжить» without the side is the very gap this field exists to close")
		}
	}

	return f, nil
}

// --- эмиссия entity -> pb ------------------------------------------------------------------------
//
// Блок эмитится ТОЛЬКО когда в нём есть хоть один заполненный факт. Всегда присутствующая обёртка
// с одними нулями читалась бы клиентом как «кто-то про это семейство думал» на каждом шаге, где
// про него не думал никто, — та же причина, по которой topstitchToPb возвращает nil без режима.
// Обратное тоже держится этим: absent -> NULL -> absent, круг без единого лишнего байта.

func operationStitchingToPb(o entity.TechCardOperation) *pb_common.TechCardOperationStitching {
	if !(o.NeedleCount.Valid || o.NeedleGaugeMm.Valid || o.SeamSecuring.Valid || o.RowSpacingMm.Valid ||
		o.FullnessRatio.Valid || o.BindingStyle.Valid || o.LabelAttachStitch.Valid) {
		return nil
	}
	return &pb_common.TechCardOperationStitching{
		NeedleCount:       pbInt32FromNull(o.NeedleCount),
		NeedleGaugeMm:     pbDecimalFromNull(o.NeedleGaugeMm),
		SeamSecuring:      seamSecuringTokenToPb[o.SeamSecuring.String],
		RowSpacingMm:      pbDecimalFromNull(o.RowSpacingMm),
		FullnessRatio:     pbDecimalFromNull(o.FullnessRatio),
		BindingStyle:      bindingStyleTokenToPb[o.BindingStyle.String],
		LabelAttachStitch: labelAttachStitchTokenToPb[o.LabelAttachStitch.String],
	}
}

func operationPlacementToPb(o entity.TechCardOperation) *pb_common.TechCardOperationPlacement {
	if !(o.PlacementCount.Valid || o.PitchMm.Valid) {
		return nil
	}
	return &pb_common.TechCardOperationPlacement{
		Count:   pbInt32FromNull(o.PlacementCount),
		PitchMm: pbDecimalFromNull(o.PitchMm),
	}
}

func operationHardwareToPb(o entity.TechCardOperation) *pb_common.TechCardOperationHardware {
	if !(o.AttachMethod.Valid || o.HolePrep.Valid || o.Reinforcement.Valid || o.FoldbackMm.Valid ||
		o.CycleStitchCount.Valid) {
		return nil
	}
	return &pb_common.TechCardOperationHardware{
		AttachMethod:     hardwareAttachMethodTokenToPb[o.AttachMethod.String],
		HolePrep:         holePrepTokenToPb[o.HolePrep.String],
		Reinforcement:    reinforcementTokenToPb[o.Reinforcement.String],
		FoldbackMm:       pbDecimalFromNull(o.FoldbackMm),
		CycleStitchCount: pbInt32FromNull(o.CycleStitchCount),
	}
}

// print_method в блок НЕ входит — он поле самого шага (51), потому что обязательное поле не прячут
// внутрь необязательного сообщения, которого может не быть вовсе.
func operationPrintToPb(o entity.TechCardOperation) *pb_common.TechCardOperationPrint {
	if !(o.PeelMode.Valid || o.SecondPressSec.Valid || o.PressureScale.Valid) {
		return nil
	}
	return &pb_common.TechCardOperationPrint{
		PeelMode:       peelModeTokenToPb[o.PeelMode.String],
		SecondPressSec: pbInt32FromNull(o.SecondPressSec),
		PressureScale:  pressureScaleTokenToPb[o.PressureScale.String],
	}
}

func operationWeldToPb(o entity.TechCardOperation) *pb_common.TechCardOperationWeld {
	if !(o.AirTemperatureC.Valid || o.FeedSpeedMMin.Valid) {
		return nil
	}
	return &pb_common.TechCardOperationWeld{
		AirTemperatureC: pbInt32FromNull(o.AirTemperatureC),
		FeedSpeedMMin:   pbDecimalFromNull(o.FeedSpeedMMin),
	}
}

func operationTrimToPb(o entity.TechCardOperation) *pb_common.TechCardOperationTrim {
	if !(o.TrimAction.Valid || o.ResidualAllowanceMm.Valid) {
		return nil
	}
	return &pb_common.TechCardOperationTrim{
		Action:              trimActionTokenToPb[o.TrimAction.String],
		ResidualAllowanceMm: pbDecimalFromNull(o.ResidualAllowanceMm),
	}
}

func operationThreadTrimToPb(o entity.TechCardOperation) *pb_common.TechCardOperationThreadTrim {
	if !o.ResidualTailMaxMm.Valid {
		return nil
	}
	return &pb_common.TechCardOperationThreadTrim{
		ResidualTailMaxMm: pbDecimalFromNull(o.ResidualTailMaxMm),
	}
}

func operationCleanToPb(o entity.TechCardOperation) *pb_common.TechCardOperationClean {
	if !o.CleaningKind.Valid {
		return nil
	}
	return &pb_common.TechCardOperationClean{Kind: cleaningKindTokenToPb[o.CleaningKind.String]}
}

func operationInspectToPb(o entity.TechCardOperation) *pb_common.TechCardOperationInspect {
	if !o.CoverageMode.Valid {
		return nil
	}
	return &pb_common.TechCardOperationInspect{CoverageMode: inspectCoverageTokenToPb[o.CoverageMode.String]}
}

// operationPressToPb — блок ВТО-под-глагола (0325). Отсутствует, когда не сказано ни одного из
// двух: пустая обёртка читалась бы как «про ВТО тут думали» на каждом шаге, где не думал никто.
//
// ОСТАЛЬНЫЕ ВТО-факты шага (оборудование, температура, выдержка, давление, пар, проутюжильник)
// эмитятся полями 39..45 самого шага и сюда НЕ дублируются: один факт живёт в одном месте.
func operationPressToPb(o entity.TechCardOperation) *pb_common.TechCardOperationPress {
	if !(o.PressAction.Valid || o.PressToward.Valid) {
		return nil
	}
	return &pb_common.TechCardOperationPress{
		Action: pressActionTokenToPb[o.PressAction.String],
		Toward: pressTowardTokenToPb[o.PressToward.String],
	}
}

func operationFasteningToPb(o entity.TechCardOperation) *pb_common.TechCardOperationFastening {
	if !(o.ButtonholeStyle.Valid || o.CutLengthMm.Valid || o.ButtonholeOrientation.Valid ||
		o.BartackLengthMm.Valid || o.AttachPattern.Valid || o.ZipperApplication.Valid) {
		return nil
	}
	return &pb_common.TechCardOperationFastening{
		ButtonholeStyle:       buttonholeStyleTokenToPb[o.ButtonholeStyle.String],
		CutLengthMm:           pbDecimalFromNull(o.CutLengthMm),
		ButtonholeOrientation: buttonholeOrientationTokenToPb[o.ButtonholeOrientation.String],
		BartackLengthMm:       pbDecimalFromNull(o.BartackLengthMm),
		AttachPattern:         buttonAttachPatternTokenToPb[o.AttachPattern.String],
		ZipperApplication:     zipperApplicationTokenToPb[o.ZipperApplication.String],
	}
}
