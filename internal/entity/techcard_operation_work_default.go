package entity

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// ГЛОБАЛЬНЫЕ ДЕФОЛТЫ СВОЙСТВ РАБОТЫ (R3) — РЕЕСТР ПОЛЕЙ И ПРАВИЛО, КОТОРОЕ ЕГО СТЕРЕЖЁТ.
//
// ЖЕСТ, РАДИ КОТОРОГО ЭТО СУЩЕСТВУЕТ. Технолог заполнил у отстрочки ширину 2 мм и говорит: «у меня
// она всегда такая». `operation_work_default` запоминает пару (работа, поле) → значение, и
// следующий выбор этой работы наливает значение в ПУСТОЕ поле формы с меткой «подставлено».
// Дефолт — данные пользователя, а не идентичность каталога: он пишется RPC, а не миграцией
// (граница Р-А плана), и в отпечаток карточки не входит НИКОГДА — в строке шага лежит уже
// скопированное значение, и хешируется оно как обычное поле.
//
// ⚠️ ГЛАВНОЕ ПРАВИЛО ЭТОГО ФАЙЛА, И ОНО НЕ ПЕРЕИГРЫВАЕТСЯ: СВОЙСТВА РАБОТЫ — МОЖНО, МАШИННЫЕ И
// ВТО-НАСТРОЙКИ — НЕЛЬЗЯ.
//
// У машинных настроек УЖЕ ЕСТЬ лестница наследования, построенная миграцией 0306:
// шаг → профиль оборудования карточки → «не задано». NULL на шаге там значит «возьми из профиля»,
// и это несущий смысл, а не пустота. Второй механизм поверх той же колонки дал бы ДВА ответа на
// один вопрос — «сколько стежков на сантиметре у этого шага»: один от профиля станка, другой от
// глобального дефолта работы, — и различить их было бы нечем: в колонке лежало бы одно и то же
// число, а откуда оно пришло, не сказал бы никто. Ровно то расхождение, которое волна 0289 в этой
// же таблице УЖЕ вычищала («технолог выбрал 4 ст/см» перестало отличаться от «подставилось 4»).
//
// Поэтому имена машинных настроек отвергаются ПО ИМЕНИ и ПЕРВЫМИ — раньше проверки «есть ли поле в
// реестре». Это не украшение сообщения: порядок делает правило РАБОЧИМ, а не декоративным. Допиши
// кто-нибудь `stitches_per_cm` в OperationWorkDefaultFields — отказ всё равно останется, потому что
// запрет стоит выше разрешения; а противоречие двух списков поймает guard-тест
// (TestOperationWorkDefaultRegistryHasNoMachineSettings), который выводит запретный список ИЗ
// ЛЕСТНИЦЫ 0306 по тегам db у профилей, а не переписывает его руками.
//
// ПОЧЕМУ ИМЕНА — КОЛОНКИ tech_card_operation, А НЕ ПОЛЯ ФОРМЫ КЛИЕНТА. Список ниже — порт
// клиентского `KIND_PROPERTY_FIELDS` (operation-kinds.ts), и порт СОДЕРЖИМОГО, а не написания:
// клиент называет поля своим camelCase (`topstitchWidthMm`), сервер — именем колонки
// (`topstitch_width_mm`). Взято серверное, потому что (1) в этом написании имя ПРОВЕРЯЕМО — оно
// обязано быть настоящей колонкой, и guard-тест сверяет каждое имя с тегами db у
// entity.TechCardOperation, то есть опечатка невозможна; (2) в этом же написании стоят все
// FieldViolation'ы шага, и отказ «topstitch_width_mm вне диапазона» читается одинаково откуда бы
// он ни пришёл; (3) у клиента перевод «поле формы ↔ провод» уже есть и живёт одним слоем
// (schema.ts), а на сервере второго такого слоя нет вовсе.

// OperationWorkDefaultFields — ЗАКРЫТЫЙ реестр полей, у которых бывает глобальный дефолт работы.
// Порядок — порядок клиентского KIND_PROPERTY_FIELDS (по семействам свойств), потому что этот
// срез уезжает клиенту в ответе каталога: пусть жест «запомнить как дефолт» рисуется по ОДНОМУ
// списку — серверному, — а не по второму, своему, который разошёлся бы с первым молча.
//
// ЧЕГО ЗДЕСЬ НЕТ И ПОЧЕМУ:
//   - дискриминаторы глагола (attach_method, print_method, trim_action, cleaning_kind,
//     coverage_mode, wet_process_kind) — это ЛИЧНОСТЬ вида, её пишет сам выбор работы, а не дефолт;
//   - seam_class — якорь, который пикер ставит и снимает сам (KIND_ANCHOR_FIELDS у клиента);
//   - seam_allowance_mm — наследуется от конструкции карточки, у него своя лестница;
//   - всё из OperationWorkInheritedFields — см. шапку.
var OperationWorkDefaultFields = []string{
	// отстрочка
	"topstitch_mode",
	"topstitch_width_mm",
	"topstitch_rows",
	// строчка (S)
	"needle_count",
	"needle_gauge_mm",
	"seam_securing",
	"row_spacing_mm",
	"fullness_ratio",
	"binding_style",
	"label_attach_stitch",
	// раскладка повторов (PL)
	"placement_count",
	"pitch_mm",
	// фурнитура (H)
	"hole_prep",
	"reinforcement",
	"cycle_stitch_count",
	"foldback_mm",
	// печать (P)
	"peel_mode",
	"second_press_sec",
	// сварка и проклейка (W)
	"air_temperature_c",
	"feed_speed_m_min",
	// подрезка и хвосты (T / F)
	"residual_allowance_mm",
	"residual_tail_max_mm",
	// петли, закрепки, пуговицы, молнии (FA)
	"buttonhole_style",
	"cut_length_mm",
	"buttonhole_orientation",
	"bartack_length_mm",
	"attach_pattern",
	"zipper_application",
	// ВТО: под-глагол и направление припуска (0325) — СВОЙСТВА ПРИЁМА, а не настройки утюга.
	// Граница здесь ровно та же, что у всего файла: «как гладим» (разутюжить, на сторону, куда
	// заутюжить) — свойство работы; «на чём и при какой температуре» — лестница 0306.
	"press_action",
	"press_toward",
}

// OperationWorkInheritedFields — поля, у которых СВОЯ лестница наследования (миграция 0306), и
// поэтому глобального дефолта работы у них не бывает никогда.
//
// Список НЕ произвольный: это ровно те колонки шага, которым отвечает профиль оборудования
// карточки, плюс четыре, которыми шаг на профиль СМОТРИТ (тип машинки, тип оборудования ВТО и две
// мягкие ссылки на профиль по ключу). Первые тринадцать выводятся из пересечения тегов db у
// TechCardOperation и у пары TechCardMachineProfile / TechCardPressProfile — и guard-тест это
// пересечение считает, так что появление в 0306+ новой наследуемой настройки красит тест до тех
// пор, пока имя не приписано сюда.
var OperationWorkInheritedFields = []string{
	// «на чём» — ось, а не свойство; её пишет сама работа (machine_mode каталога).
	"machine_type",
	"press_equipment",
	// мягкие ссылки на профиль по ключу — идентичность станка карточки, глобальной не бывает.
	"machine_profile_key",
	"press_profile_key",
	// настройки станка: шаг → профиль → не задано
	"thread_count",
	"needle_type",
	"needle_size_nm",
	"thread_tension",
	"thread_tension_note",
	"stitch_width_mm",
	"stitches_per_cm",
	"attachment_kind",
	// настройки ВТО: та же лестница
	"press_temperature_c",
	"press_dwell_sec",
	"press_pressure_n_cm2",
	"press_steam",
	"press_cloth",
}

// Причины отказа — токенами, потому что их читает не только человек: клиент по ним решает, на каком
// контроле подсветить отказ, а тесты — по ним отличают ПРАВИЛО от простого «нет такого поля».
const (
	// OperationWorkDefaultReasonInherited — тот самый запрет. Отдельный токен, а не unknown_field:
	// «поля не существует» и «поле существует, но у него другой механизм» — разные диагнозы, и по
	// первому пошли бы искать опечатку.
	OperationWorkDefaultReasonInherited = "inherits_from_equipment_profile"
	OperationWorkDefaultReasonUnknown   = "unknown_field"
	OperationWorkDefaultReasonRequired  = "required"
	OperationWorkDefaultReasonTooLong   = "too_long"
	OperationWorkDefaultReasonUnknownV  = "unknown_value"
	OperationWorkDefaultReasonRange     = "out_of_range"
	OperationWorkDefaultReasonScale     = "too_precise"
)

// MaxOperationWorkDefaultValueLen — ширина колонки operation_work_default.value (0329).
const MaxOperationWorkDefaultValueLen = 64

// operationWorkDefaultRule — как проверяется ЗНАЧЕНИЕ одного поля.
//
// Диапазоны и словари НЕ ПЕРЕПИСАНЫ, а взяты теми же константами и теми же срезами токенов, что
// проверяют это поле на шаге: разойтись они не могут по построению, а дефолт, который нельзя
// набрать в форме, был бы ловушкой — человек нажал «запомнить», а следующая карточка отказалась
// сохраняться.
type operationWorkDefaultRule struct {
	// dict — закрытый словарь токенов; пусто у числовых полей.
	dict []string
	// maxFrac — сколько знаков после запятой допустимо (0 = целое).
	maxFrac int32
	// min / max — края диапазона, ВКЛЮЧИТЕЛЬНО, десятичной строкой.
	min, max string
	// exclusiveMax — верх строгий (< max). Стоит у ширины отстрочки: правило шага
	// (validateDecimalScale) требует «меньше 100», а не «не больше 100».
	exclusiveMax bool
	// hint — как это чинить, словами человека.
	hint string
}

func dictRule(tokens []string, hint string) operationWorkDefaultRule {
	return operationWorkDefaultRule{dict: tokens, hint: hint}
}

func numRule(maxFrac int32, min, max, hint string) operationWorkDefaultRule {
	return operationWorkDefaultRule{maxFrac: maxFrac, min: min, max: max, hint: hint}
}

// exclusiveTop makes the upper bound strict (< max), the way validateDecimalScale reads a column
// limit on a step.
func exclusiveTop(r operationWorkDefaultRule) operationWorkDefaultRule {
	r.exclusiveMax = true
	return r
}

// operationWorkDefaultRules — правило значения на каждое поле реестра. Ключи этой карты и элементы
// OperationWorkDefaultFields обязаны совпадать множество-в-множество: срез задаёт ПОРЯДОК (он
// уезжает клиенту), карта задаёт ПРОВЕРКУ, и равенство их держит guard-тест, а не внимательность.
var operationWorkDefaultRules = map[string]operationWorkDefaultRule{
	"topstitch_mode": dictRule(TopstitchModeTokens,
		"where the line runs: from the edge of the piece, in the ditch of the seam, or parallel to it"),
	// Верх СТРОГИЙ: правило шага (validateDecimalScale) требует «меньше 100», а не «не больше».
	"topstitch_width_mm": exclusiveTop(numRule(1, "0", fmt.Sprint(MaxSeamAllowanceMm),
		"the offset of the topstitch from the edge or the seam, in mm")),
	"topstitch_rows": numRule(0, "1", fmt.Sprint(MaxTopstitchRows), "how many parallel rows of topstitching"),

	"needle_count": numRule(0, fmt.Sprint(MinNeedleCount), fmt.Sprint(MaxNeedleCount),
		"how many needles the stitch runs on"),
	"needle_gauge_mm": numRule(1, MinNeedleGaugeMm, MaxNeedleGaugeMm,
		"the gap BETWEEN NEEDLES in mm — not the width of the stitch"),
	"seam_securing": dictRule(SeamSecuringTokens, "how the seam ends are secured; «none» is a real answer"),
	"row_spacing_mm": numRule(1, fmt.Sprint(MinRowSpacingMm), fmt.Sprint(MaxRowSpacingMm),
		"the gap between stitch ROWS in mm"),
	"fullness_ratio": numRule(2, MinFullnessRatio, MaxFullnessRatio,
		"ease / gathering as a RATIO, not a percentage (1.0 = layers run one to one)"),
	"binding_style":       dictRule(BindingStyleTokens, "how the binding is folded"),
	"label_attach_stitch": dictRule(LabelAttachStitchTokens, "how a label is caught by the stitch"),

	"placement_count": numRule(0, fmt.Sprint(MinPlacementCount), fmt.Sprint(MaxPlacementCount),
		"how many repeats"),
	"pitch_mm": numRule(1, fmt.Sprint(MinPitchMm), fmt.Sprint(MaxPitchMm), "the gap between repeats in mm"),

	"hole_prep":     dictRule(HolePrepTokens, "how the hole is prepared before the hardware goes in"),
	"reinforcement": dictRule(ReinforcementTokens, "what backs the hardware up"),
	"cycle_stitch_count": numRule(0, fmt.Sprint(MinCycleStitchCount), fmt.Sprint(MaxCycleStitchCount),
		"stitches in the automat's cycle"),
	"foldback_mm": numRule(1, fmt.Sprint(MinFoldbackMm), fmt.Sprint(MaxFoldbackMm),
		"the webbing fold-back through the buckle in mm"),

	"peel_mode": dictRule(PeelModeTokens, "when the carrier is peeled off"),
	"second_press_sec": numRule(0, fmt.Sprint(MinSecondPressSec), fmt.Sprint(MaxSecondPressSec),
		"the second press in seconds"),

	"air_temperature_c": numRule(0, fmt.Sprint(MinAirTemperatureC), fmt.Sprint(MaxAirTemperatureC),
		"hot air temperature in °C"),
	"feed_speed_m_min": numRule(1, MinFeedSpeedMMin, MaxFeedSpeedMMin, "feed speed in m/min"),

	"residual_allowance_mm": numRule(1, fmt.Sprint(MinResidualAllowanceMm), fmt.Sprint(MaxResidualAllowanceMm),
		"how much seam allowance is LEFT after trimming, in mm"),
	"residual_tail_max_mm": numRule(1, fmt.Sprint(MinResidualTailMaxMm), fmt.Sprint(MaxResidualTailMaxMm),
		"the longest thread tail allowed, in mm"),

	"buttonhole_style": dictRule(ButtonholeStyleTokens, "the shape of the buttonhole"),
	"cut_length_mm": numRule(1, fmt.Sprint(MinCutLengthMm), fmt.Sprint(MaxCutLengthMm),
		"the buttonhole cut in mm"),
	"buttonhole_orientation": dictRule(ButtonholeOrientationTokens, "how the buttonhole lies"),
	"bartack_length_mm": numRule(1, fmt.Sprint(MinBartackLengthMm), fmt.Sprint(MaxBartackLengthMm),
		"the bartack length in mm"),
	"attach_pattern":     dictRule(ButtonAttachPatternTokens, "the pattern the button is sewn with"),
	"zipper_application": dictRule(ZipperApplicationTokens, "how the zip is set in"),

	"press_action": dictRule(PressActionTokens, "what the pressing DOES; opening a seam is its own verb"),
	"press_toward": dictRule(PressTowardTokens, "which way the allowance goes"),
}

// operationWorkInheritedSet / operationWorkDefaultSet — множества для проверки. Выведены из срезов
// выше, никогда не набраны отдельно: имя, добавленное в срез, не может отсутствовать в множестве.
var (
	operationWorkInheritedSet = stringTokenSet(OperationWorkInheritedFields)
	operationWorkDefaultSet   = stringTokenSet(OperationWorkDefaultFields)
)

// ValidateOperationWorkDefaultField — ПРАВИЛО РЕЕСТРА, отдельной функцией, потому что оно нужно и
// на записи дефолта, и на его снятии: снять дефолт машинной настройки тоже нельзя, иначе жест
// «очистить» стал бы способом узнать, что такая запись где-то есть.
//
// ПОРЯДОК ПРОВЕРОК — ЧАСТЬ ПРАВИЛА (см. шапку): запрет стоит ВЫШЕ разрешения.
func ValidateOperationWorkDefaultField(field string) *ValidationError {
	field = strings.TrimSpace(field)
	if field == "" {
		return NewFieldViolation("field", OperationWorkDefaultReasonRequired, "",
			"name the step field the default belongs to, for example topstitch_width_mm")
	}
	if operationWorkInheritedSet[field] {
		return NewFieldViolation("field", OperationWorkDefaultReasonInherited, field,
			"machine and pressing settings already inherit — step → the card's equipment profile → unset (migration 0306). "+
				"A global default on top of that ladder would answer the same question twice with nothing to tell the two apart; "+
				"set it on the card's equipment profile instead")
	}
	if !operationWorkDefaultSet[field] {
		return NewFieldViolation("field", OperationWorkDefaultReasonUnknown, field,
			"only the work's own property fields carry a global default; "+
				"the picker writes the identity of the work itself (its verb, its machine, its discriminator), and that is not a default")
	}
	return nil
}

// ValidateOperationWorkDefaultValue проверяет ЗНАЧЕНИЕ дефолта теми же правилами, что проверяют это
// поле на шаге. Поле обязано быть уже разрешено (ValidateOperationWorkDefaultField).
func ValidateOperationWorkDefaultValue(field, value string) *ValidationError {
	value = strings.TrimSpace(value)
	if value == "" {
		return NewFieldViolation("value", OperationWorkDefaultReasonRequired, "",
			"a default with no value is not a default — send clear=true to forget the one that is stored")
	}
	if len(value) > MaxOperationWorkDefaultValueLen {
		return NewFieldViolation("value", OperationWorkDefaultReasonTooLong, value,
			fmt.Sprintf("at most %d bytes", MaxOperationWorkDefaultValueLen))
	}
	rule, ok := operationWorkDefaultRules[field]
	if !ok {
		// Недостижимо через RPC (поле уже прошло реестр), но молчать здесь нельзя: расхождение
		// среза и карты означало бы, что поле разрешено и НЕ ПРОВЕРЯЕТСЯ ничем.
		return NewFieldViolation("field", OperationWorkDefaultReasonUnknown, field,
			"no value rule is registered for this field — report it, the field registry and its rules have drifted")
	}
	if len(rule.dict) > 0 {
		for _, t := range rule.dict {
			if t == value {
				return nil
			}
		}
		return NewFieldViolation("value", OperationWorkDefaultReasonUnknownV, value,
			fmt.Sprintf("%s — one of: %s", rule.hint, strings.Join(rule.dict, " / ")))
	}

	d, err := decimal.NewFromString(value)
	if err != nil {
		return NewFieldViolation("value", OperationWorkDefaultReasonUnknownV, value,
			rule.hint+" — send it as a plain number")
	}
	if d.Exponent() < -rule.maxFrac {
		return NewFieldViolation("value", OperationWorkDefaultReasonScale, value,
			fmt.Sprintf("%s; at most %d decimal place(s)", rule.hint, rule.maxFrac))
	}
	min := decimal.RequireFromString(rule.min)
	max := decimal.RequireFromString(rule.max)
	tooBig := d.GreaterThan(max)
	if rule.exclusiveMax {
		tooBig = d.GreaterThanOrEqual(max)
	}
	if d.LessThan(min) || tooBig {
		bound := "to"
		if rule.exclusiveMax {
			bound = "up to (but not) "
		}
		return NewFieldViolation("value", OperationWorkDefaultReasonRange, value,
			fmt.Sprintf("%s; %s %s %s", rule.hint, rule.min, bound, rule.max))
	}
	return nil
}

// OperationWorkDefaultRuleFields returns the registry keys that carry a value rule, sorted. Exists
// for the guard test that pins the slice and the map to one another; production code reads the
// ordered slice.
func OperationWorkDefaultRuleFields() []string {
	out := make([]string, 0, len(operationWorkDefaultRules))
	for f := range operationWorkDefaultRules {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
