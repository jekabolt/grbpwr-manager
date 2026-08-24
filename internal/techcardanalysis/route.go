package techcardanalysis

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ── БЛОК «МАРШРУТ И ОПЕРАЦИИ»: A1–A10 (design §3.1) ─────────────────────────────────────────────
//
// ЧЕГО ЗДЕСЬ НЕТ И НЕ БУДЕТ. Ни одной топологической находки: циклы, ссылки вперёд,
// дубли-производители, двойное потребление и коллизия пространства имён в сохранённой карточке
// НЕПРЕДСТАВИМЫ — canonicalizeAssembly отвергает такой payload на каждой записи (§1,
// internal/dto/techcard_assembly.go). Проверка, которая их ищет, не сработает никогда и никогда не
// будет удалена, потому что «а вдруг». Здесь только то, где запись НЕ гейтит: словари, параметры,
// последовательность финиша и целостность мягких ссылок.
//
// РЕГИСТРАЦИЯ — В ЭТОМ ФАЙЛЕ. analysis.go после T2 не трогает никто: проверки BOM и готовности
// пишутся параллельной задачей, и общий файл-реестр был бы генератором дефектов на швах.
var _ = register(
	checkA1UnitKeyCaseCollision,
	checkA2KindDiscriminators,
	checkA3PressParameters,
	checkA4SeamClassAgainstMachine,
	checkA5NoteCarriers,
	checkA6FinishingOrder,
	checkA7FusingOnAssembledUnits,
	checkA8WorkMachineLegality,
	checkA9LegacyPieceLinkDrift,
	checkA10SensitiveBeforeWetProcess,
)

// ── A1. РЕГИСТРОВАЯ КОЛЛИЗИЯ КЛЮЧЕЙ УЗЛОВ ───────────────────────────────────────────────────────
//
// naming, warning, детерминированно. Читает output_unit_key и unit-ссылки входов (0307).
//
// Колонка ключа узла — `_bin`: для машины «Base» и «base» законно РАЗНЫЕ узлы, и в этом вся
// находка. Приведение регистра при СРАВНЕНИИ здесь стёрло бы ровно то, ради чего проверка
// существует, поэтому lower-fold используется ТОЛЬКО для группировки, а в текст находки формы
// приводятся байт-в-байт.
//
// Подавителей нет: это байтовый факт, а не догадка.
func checkA1UnitKeyCaseCollision(v *cardView) []Finding {
	type unitForm struct {
		key       string
		producers []int32
		users     []int32
		seen      int // порядок первого появления в каноническом порядке шагов
	}

	forms := map[string]*unitForm{}
	groups := map[string][]string{} // lower-fold → байтовые формы в порядке появления
	seq := 0

	touch := func(key string) *unitForm {
		f, ok := forms[key]
		if !ok {
			seq++
			f = &unitForm{key: key, seen: seq}
			forms[key] = f
			fold := strings.ToLower(key)
			groups[fold] = append(groups[fold], key)
		}
		return f
	}

	for _, op := range v.ops {
		num, hasNum := opNumber(op)
		if key := op.OutputUnitKey.String; key != "" {
			f := touch(key)
			if hasNum {
				f.producers = append(f.producers, num)
			}
		}
		for _, in := range op.AssemblyInputs {
			if in.Kind != entity.AssemblyInputUnit || in.Key == "" {
				continue
			}
			f := touch(in.Key)
			if hasNum {
				f.users = append(f.users, num)
			}
		}
	}

	folds := make([]string, 0, len(groups))
	for fold, keys := range groups {
		if len(keys) > 1 {
			folds = append(folds, fold)
		}
	}
	// Порядок групп — по первому появлению первой формы: детерминированный и читаемый вдоль
	// маршрута, а не по случайному порядку обхода карты.
	sort.Slice(folds, func(i, j int) bool {
		return forms[groups[folds[i]][0]].seen < forms[groups[folds[j]][0]].seen
	})

	out := make([]Finding, 0, len(folds))
	for _, fold := range folds {
		keys := groups[fold]
		quoted := make([]string, 0, len(keys))
		clauses := make([]string, 0, len(keys))
		refs := make([]string, 0, len(keys)*2)
		for _, k := range keys {
			f := forms[k]
			quoted = append(quoted, `"`+k+`"`)
			refs = append(refs, RefUnit(k))
			switch {
			case len(f.producers) > 0:
				clauses = append(clauses, fmt.Sprintf("%q is produced by %s", k, opList(f.producers)))
			case len(f.users) > 0:
				clauses = append(clauses, fmt.Sprintf("%q is only referenced as an input, by %s", k, opList(f.users)))
			default:
				clauses = append(clauses, fmt.Sprintf("%q appears with no producer and no user", k))
			}
		}
		for _, k := range keys {
			for _, n := range forms[k].producers {
				refs = append(refs, RefOp(n))
			}
		}

		out = append(out, Finding{
			Category: CategoryNaming,
			Severity: SeverityWarning,
			Title:    aiBoundedText("Unit keys differ only in case: "+strings.Join(quoted, " and "), 90),
			Detail: strings.Join(clauses, "; ") + ". Unit keys are compared BYTE FOR BYTE " +
				"(tech_card_operation.output_unit_key is a _bin column), so these are different units " +
				"on the table — but a technologist reading the route sees one word twice.",
			Evidence:   clauses,
			Refs:       refs,
			Suggestion: "Rename all but one of the forms so that one unit of the garment has one key.",
		})
	}
	return out
}

// ── A2. ДИСКРИМИНАТОРЫ ВИДОВ ОПЕРАЦИЙ ───────────────────────────────────────────────────────────
//
// parameter, warning. Читает колонки волны 0324 (placement_count / buttonhole_style /
// cut_length_mm / trim_action) и attach_method (0328/entity:2715).
//
// NULL-СЕМАНТИКА — ПО 0324: `placement_count NULL` читается как ОДИН повтор, а не как «не знаю».
// Поэтому находка формулируется утверждением карточки («as written this is a one-button garment»),
// а не вопросом: карточка уже сказала «одна пуговица», просто никто этого не выбирал.
//
// ВИДЫ ОПРЕДЕЛЯЮТСЯ ПО machine_type, а не по «работе»: словарь машин — entity.MachineTypeTokens
// (entity/techcard.go:2531). Токена `hardware_attach` в предикатах нет — он СНЯТ 0328.
//
// Четыре правила — четыре ОТДЕЛЬНЫХ покрытия (§3.0: применимое множество считается «для каждой
// проверки покрытия»), потому что применимые множества у них разные: петельных шагов на карточке
// может быть четыре, а trim-шагов ноль, и одна дробь на всех соврала бы про оба.
func checkA2KindDiscriminators(v *cardView) []Finding {
	var out []Finding

	// 1. Петля без стиля и без длины прорези. Фраза находки называет ОБА поля, поэтому и условие —
	//    оба пустые: заполненный стиль делает текст «no style, no cut length» ложным.
	{
		applicable, missing := 0, []CoverageMiss(nil)
		for _, op := range v.ops {
			if machineToken(op) != "buttonhole" {
				continue
			}
			applicable++
			if !nsEmpty(op.ButtonholeStyle) || !ndEmpty(op.CutLengthMm) {
				continue
			}
			missing = append(missing, CoverageMiss{
				Refs: opRefs(op),
				Finding: Finding{
					Category: CategoryParameter,
					Severity: SeverityWarning,
					Title:    "Buttonhole unspecified: no style, no cut length",
					Detail: fmt.Sprintf("%s runs on a buttonhole machine, but buttonhole_style and "+
						"cut_length_mm (0324) are both empty — the card does not say which buttonhole "+
						"is sewn, nor how long the slit is.", opLabel(op)),
					Refs:       opRefs(op),
					Suggestion: "Name the buttonhole style and the cut length on the step.",
				},
			})
		}
		out = append(out, Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
			return Finding{
				Category: CategoryParameter,
				Severity: SeverityWarning,
				Title: fmt.Sprintf("Buttonhole style and cut length missing on %d of %d buttonhole operations",
					missing, applicable),
				Detail: "buttonhole_style and cut_length_mm (0324) are empty on these steps — neither " +
					"the buttonhole nor the length of its slit is stated anywhere on the card.",
				Refs:       sample,
				Suggestion: "Name the buttonhole style and the cut length on each buttonhole step.",
			}
		})...)
	}

	// 2. Петля/пуговица без числа повторов.
	{
		applicable, missing := 0, []CoverageMiss(nil)
		for _, op := range v.ops {
			m := machineToken(op)
			if m != "buttonhole" && m != "button_attach" {
				continue
			}
			applicable++
			if !niEmpty(op.PlacementCount) {
				continue
			}
			missing = append(missing, CoverageMiss{
				Refs: opRefs(op),
				Finding: Finding{
					Category: CategoryParameter,
					Severity: SeverityWarning,
					Title:    "As written this is a one-button garment",
					Detail: fmt.Sprintf("%s names no placement_count (0324), and NULL there reads as ONE "+
						"repeat — so the card states exactly one %s for the whole garment.",
						opLabel(op), machineWord(m)),
					Refs:       opRefs(op),
					Suggestion: "Set placement_count to the number of repeats on the garment.",
				},
			})
		}
		out = append(out, Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
			return Finding{
				Category: CategoryParameter,
				Severity: SeverityWarning,
				Title: fmt.Sprintf("No placement count on %d of %d buttonhole/button steps",
					missing, applicable),
				Detail: "placement_count (0324) is NULL on these steps, and NULL reads as ONE repeat — " +
					"as written the garment carries a single buttonhole and a single button.",
				Refs:       sample,
				Suggestion: "Set placement_count on each of these steps.",
			}
		})...)
	}

	// 3. Подрезка без действия. REQUIRED у новых записей — живая поверхность только легаси-строки.
	out = append(out, discriminatorCoverage(v, entity.OpTypeTrim, "trim_action",
		func(op *entity.TechCardOperation) bool { return !nsEmpty(op.TrimAction) },
		"Trim step does not say what is trimmed",
		"trim_action (0324) is empty: the card says something is cut back, but not whether it is an "+
			"even trim, a grade, a clip, a notch or a corner.",
		"Pick the trim action on the step.")...)

	// 4. Фурнитура без способа крепления.
	out = append(out, discriminatorCoverage(v, entity.OpTypeHardwareSet, "attach_method",
		func(op *entity.TechCardOperation) bool { return !nsEmpty(op.AttachMethod) },
		"Hardware step does not say how the part is attached",
		"attach_method is empty: sewn, prong-clinched, press-set, crimped and threaded are five "+
			"different jobs with five different tools, and the card names none of them.",
		"Pick the attach method on the step.")...)

	return out
}

// discriminatorCoverage is the shared shape of «this verb owns a discriminator column and the
// column is empty»: one applicable set (the steps of that verb), one miss per empty column.
func discriminatorCoverage(v *cardView, verb entity.TechCardOperationType, column string,
	filled func(*entity.TechCardOperation) bool, title, detail, suggestion string,
) []Finding {
	applicable, missing := 0, []CoverageMiss(nil)
	for _, op := range v.ops {
		if op.OperationType != verb {
			continue
		}
		applicable++
		if filled(op) {
			continue
		}
		missing = append(missing, CoverageMiss{
			Refs: opRefs(op),
			Finding: Finding{
				Category:   CategoryParameter,
				Severity:   SeverityWarning,
				Title:      title,
				Detail:     opLabel(op) + " is a " + string(verb) + " step and " + detail,
				Refs:       opRefs(op),
				Suggestion: suggestion,
			},
		})
	}
	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryParameter,
			Severity: SeverityWarning,
			Title: aiBoundedText(fmt.Sprintf("%s is empty on %d of %d %s operations",
				column, missing, applicable, string(verb)), 90),
			Detail:     detail,
			Refs:       sample,
			Suggestion: suggestion,
		}
	})
}

// ── A3. ТЕРМОПАРАМЕТРЫ ВТО И FUSING ─────────────────────────────────────────────────────────────
//
// parameter; fusing → error, press/press_open → warning. Читает пять колонок 0306 плюс парк
// карточки (tech_card_equipment_profile kind='press', где press_operation_type NULL = УНИВЕРСАЛЬНЫЙ
// профиль).
//
// ДВА ПОКРЫТИЯ, А НЕ ОДНО, И ЭТО НЕ УКРАШЕНИЕ. Severity у клеевой и у утюга разная (клеевая либо не
// приклеится, либо сгорит), а агрегированная находка несёт РОВНО ОДНУ severity. Слить их значило бы
// либо занизить fusing до warning, либо объявить забытый пар на разутюжке ошибкой; заодно дробь
// «4 of 4 pressing operations» перестала бы быть правдой о своём множестве.
func checkA3PressParameters(v *cardView) []Finding {
	profiles := v.equipment().Presses

	// applicable — профиль ПРИМЕНИМ к шагу, если он универсальный (press_operation_type NULL) или
	// назван ровно под этот глагол. Иначе «утюг ставится наугад» осталось бы правдой при полном
	// парке термопрессов на карточке, где гладят разутюжкой.
	profilesFor := func(verb entity.TechCardOperationType) int {
		n := 0
		for i := range profiles {
			p := &profiles[i]
			if nsEmpty(p.PressOperationType) || strings.TrimSpace(p.PressOperationType.String) == string(verb) {
				n++
			}
		}
		return n
	}
	hasProfileFor := func(verb entity.TechCardOperationType) bool { return profilesFor(verb) > 0 }

	group := func(verbs []entity.TechCardOperationType, severity, noun string) []Finding {
		applicable, missing := 0, []CoverageMiss(nil)
		for _, op := range v.ops {
			if !hasVerb(op, verbs) {
				continue
			}
			applicable++
			if pressSettingsPresent(op) || hasProfileFor(op.OperationType) {
				continue
			}
			missing = append(missing, CoverageMiss{
				Refs: opRefs(op),
				Finding: Finding{
					Category: CategoryParameter,
					Severity: severity,
					Title:    aiBoundedText(capitalise(noun)+" parameters are not specified", 90),
					Detail: fmt.Sprintf("%s sets no temperature, dwell, pressure, steam or press profile "+
						"(0306), and the card carries no press profile that applies to it — the iron is "+
						"set by guess at the bench.", opLabel(op)),
					Refs:       opRefs(op),
					Suggestion: "Give the step its temperature and dwell, or add a press profile to the card and point the step at it.",
				},
			})
		}
		return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
			return Finding{
				Category: CategoryParameter,
				Severity: severity,
				Title: aiBoundedText(fmt.Sprintf("%s parameters missing on %d of %d %s operations",
					capitalise(noun), missing, applicable, noun), 90),
				Detail: fmt.Sprintf("All five ВТО columns (press_temperature_c, press_dwell_sec, "+
					"press_pressure_n_cm2, press_steam, press_profile_key — 0306) are empty on these "+
					"steps, and no press profile of the card applies to them (%d press profile(s) on "+
					"the card in total).", len(profiles)),
				Refs:       sample,
				Suggestion: "Add the card's press profiles, or set temperature and dwell on the steps themselves.",
			}
		})
	}

	var out []Finding
	// Клеевая идёт первой: её находка — error, и порядок регистрации внутри проверки хотя бы не
	// спорит с порядком чтения.
	out = append(out, group([]entity.TechCardOperationType{entity.OpTypeFusing}, SeverityError, "fusing")...)
	out = append(out, group([]entity.TechCardOperationType{entity.OpTypePress, entity.OpTypePressOpen},
		SeverityWarning, "pressing")...)
	return out
}

// pressSettingsPresent reports whether the step names ANY of the five ВТО settings. press_steam is
// three-valued: Valid=false is «не сказано», Valid+false is «без пара» — a real instruction, and it
// suppresses exactly like a temperature does.
func pressSettingsPresent(op *entity.TechCardOperation) bool {
	return !niEmpty(op.PressTemperatureC) ||
		!niEmpty(op.PressDwellSec) ||
		!ndEmpty(op.PressPressureNCm2) ||
		op.PressSteam.Valid ||
		!nsEmpty(op.PressProfileKey)
}

// ── A4. ЭФФЕКТИВНЫЙ КЛАСС ШВА × МАШИНА ──────────────────────────────────────────────────────────
//
// parameter, **error**. Читает seam_class шага (0289; NULL = НАСЛЕДУЕТСЯ),
// tech_card_construction.default_seam_class и machine_type.
//
// Эффективный класс = override ?? дефолт карточки. Правило наследования живёт ТОЛЬКО в Go — в
// строке шага унаследованное значение не пишется никогда (иначе «технолог выбрал» перестало бы
// отличаться от «досталось по умолчанию»), поэтому проверка, читающая одну колонку, не увидела бы
// ничего на карточке, где все 48 шагов наследуют.
func checkA4SeamClassAgainstMachine(v *cardView) []Finding {
	cardDefault := strings.TrimSpace(v.construction().DefaultSeamClass.String)

	applicable, missing := 0, []CoverageMiss(nil)
	for _, op := range v.ops {
		machine := machineToken(op)
		if machine == "" {
			continue // подавитель: машина не названа — сказать про неё нечего
		}
		class := strings.TrimSpace(op.SeamClass.String)
		inherited := false
		if class == "" {
			class, inherited = cardDefault, true
		}
		if class == "" {
			continue // подавитель: эффективного класса нет вовсе
		}
		applicable++
		if !impossibleSeamMachine[machine][class] {
			continue // подавитель: пары нет в курируемой таблице
		}

		origin := "set on the step"
		if inherited {
			origin = "inherited from the card default (tech_card_construction.default_seam_class)"
		}
		missing = append(missing, CoverageMiss{
			Refs: opRefs(op),
			Finding: Finding{
				Category: CategoryParameter,
				Severity: SeverityError,
				Title:    aiBoundedText(fmt.Sprintf("Seam class %s is not producible on %s", class, machine), 90),
				Detail: fmt.Sprintf("%s runs on machine_type %q with effective seam class %q, %s. "+
					"That machine cannot produce that seam.", opLabel(op), machine, class, origin),
				Refs: opRefs(op),
				Suggestion: "Either name the seam class this step really produces, or move the step to a " +
					"machine that can produce the card's default class.",
			},
		})
	}

	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryParameter,
			Severity: SeverityError,
			Title: fmt.Sprintf("Effective seam class is not producible on the named machine on %d of %d steps",
				missing, applicable),
			Detail: "The effective seam class (step override, otherwise the card default) cannot be " +
				"produced on the machine the step names.",
			Refs:       sample,
			Suggestion: "Name the seam class each step really produces, or move the steps to machines that can produce it.",
		}
	})
}

// impossibleSeamMachine is the table of pairs that CANNOT EXIST — machine → seam classes that
// machine cannot produce. Стартовое зерно §3.1: {overlock, coverlock, coverstitch} ×
// {ss_plain, ss_french, ls_lapped, ls_flat_felled}.
//
// ⚠️ КУРИРУЕТСЯ ВЛАДЕЛЬЦЕМ, НЕ ВЫВОДИТСЯ. Это не «список допустимых пар с дырками» и не функция
// словарей: таблица перечисляет только НЕВОЗМОЖНОЕ, и растёт она решением технолога, а не
// расширением словаря машин. Пара, которой здесь нет, молчит — ровно так и задумано: проверка,
// которая фырчит на всём незнакомом, стоит дороже, чем её отсутствие.
var impossibleSeamMachine = map[string]map[string]bool{
	"overlock":    {"ss_plain": true, "ss_french": true, "ls_lapped": true, "ls_flat_felled": true},
	"coverlock":   {"ss_plain": true, "ss_french": true, "ls_lapped": true, "ls_flat_felled": true},
	"coverstitch": {"ss_plain": true, "ss_french": true, "ls_lapped": true, "ls_flat_felled": true},
}

// ── A5. НОТЫ-НОСИТЕЛИ С ПУСТЫМИ БЛИЗНЕЦАМИ ──────────────────────────────────────────────────────
//
// parameter, warning. Читает note (0070) и структурные колонки-близнецы.
//
// Лучшая проверка блока: она ловит ровно то место, где карточка ТЕРЯЕТ информацию — инструкция
// написана прозой, а поле, которое эту же инструкцию несёт машинно, пусто. Ни печатный пакет, ни
// раскрой, ни костинг ноту не читают.
func checkA5NoteCarriers(v *cardView) []Finding {
	applicable, missing := 0, []CoverageMiss(nil)
	for _, op := range v.ops {
		note := strings.TrimSpace(op.Note.String)
		if note == "" {
			continue
		}
		for _, rule := range noteCarrierRules {
			if !rule.re.MatchString(note) {
				continue // подавитель: нота не матчится ни одним словом карты
			}
			applicable++
			if rule.filled(op) {
				continue // подавитель: близнец заполнен
			}
			missing = append(missing, CoverageMiss{
				Refs: opRefs(op),
				Finding: Finding{
					Category: CategoryParameter,
					Severity: SeverityWarning,
					Title:    aiBoundedText("Instruction lives only in a note: "+rule.field, 90),
					Detail: fmt.Sprintf("%s carries the note %q, which names %s — but "+
						"tech_card_operation.%s is empty. The instruction exists only as free text: no "+
						"print packet, no costing and no machine setup reads it.",
						opLabel(op), aiBoundedText(note, 300), rule.field, rule.column),
					Refs:       opRefs(op),
					Suggestion: "Move the number out of the note into " + rule.column + " and leave the note as prose.",
				},
			})
		}
	}

	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryParameter,
			Severity: SeverityWarning,
			Title: fmt.Sprintf("Notes name an empty field on %d of %d annotated steps",
				missing, applicable),
			Detail: "These notes name a setting whose own column is empty — the instruction lives only " +
				"in free text.",
			Refs:       sample,
			Suggestion: "Move each named setting into its column and leave the note as prose.",
		}
	})
}

// noteCarrierRule is one row of the curated «keyword → twin column» map.
type noteCarrierRule struct {
	field  string // как это называется в тексте находки
	column string // колонка-близнец
	re     *regexp.Regexp
	filled func(*entity.TechCardOperation) bool
}

// noteCarrierRules — КУРИРУЕМАЯ КАРТА v1 (§3.1), шесть правил, слово в слово из дизайна.
//
// ⚠️ РЕГИСТР ВЫБРАН ПО АЛЬТЕРНАТИВАМ, А НЕ ОДНИМ ФЛАГОМ НА ПАТТЕРН. `(?i)` на всём выражении
// превратил бы `\d+\s*°?C` в ловушку: нота «10 cm from the edge» матчила бы ТЕМПЕРАТУРУ и требовала
// press_temperature_c у машинного шага. Слова (`температур`, `stitches`) регистронезависимы,
// единицы (`C`, `SPI`) — нет, и это единственное отличие от текста дизайна.
var noteCarrierRules = []noteCarrierRule{
	{
		field: "thread tension", column: "thread_tension",
		re:     regexp.MustCompile(`(?i:tension|натяжен)`),
		filled: func(op *entity.TechCardOperation) bool { return !nsEmpty(op.ThreadTension) },
	},
	{
		field: "a pressing temperature", column: "press_temperature_c",
		re:     regexp.MustCompile(`\d+\s*°?C|(?i:температур|temperatur)`),
		filled: func(op *entity.TechCardOperation) bool { return !niEmpty(op.PressTemperatureC) },
	},
	{
		field: "steam", column: "press_steam",
		re: regexp.MustCompile(`(?i:steam|пар)`),
		// PressSteam трёхзначен: Valid+false — это «без пара», настоящая инструкция, и она
		// подавляет находку ровно как «с паром».
		filled: func(op *entity.TechCardOperation) bool { return op.PressSteam.Valid },
	},
	{
		field: "a number of repeats", column: "placement_count",
		re:     regexp.MustCompile(`(?i:\d+\s*(?:pcs|шт)|\d+\s*(?:петл|button))`),
		filled: func(op *entity.TechCardOperation) bool { return !niEmpty(op.PlacementCount) },
	},
	{
		field: "stitch density", column: "stitches_per_cm",
		re:     regexp.MustCompile(`SPI|(?i:ст.?/.?см|stitches)`),
		filled: func(op *entity.TechCardOperation) bool { return !ndEmpty(op.StitchesPerCm) },
	},
	{
		field: "a seam allowance", column: "seam_allowance_mm",
		re: regexp.MustCompile(`(?i:припуск|allowance)`),
		// Ноль здесь — НАСТОЯЩАЯ настройка («припуск на выкройке»), поэтому проверяется Valid, а не
		// значение.
		filled: func(op *entity.TechCardOperation) bool { return op.SeamAllowanceMm.Valid },
	},
}

// ── A6. ПОРЯДОК ФИНИШНЫХ ГЛАГОЛОВ ───────────────────────────────────────────────────────────────
//
// sequence, error (fold после упаковки — warning). Читает operation_type (словарь
// entity.OperationTypeTokens, entity/techcard.go:2350) и порядок шагов.
//
// Подавитель один и он полный: `pack` в маршруте нет — молчание. На карточке 8 финишных глаголов
// ноль, и сам этот ноль едет в VERIFIED FACTS отдельной строкой (не эта проверка).
func checkA6FinishingOrder(v *cardView) []Finding {
	firstPack := -1
	for i, op := range v.ops {
		if op.OperationType == entity.OpTypePack {
			firstPack = i
			break
		}
	}
	if firstPack < 0 {
		return nil // подавитель: упаковки в маршруте нет
	}
	packLabel := opLabel(v.ops[firstPack])

	after := v.ops[firstPack+1:]
	group := func(verbs []entity.TechCardOperationType, severity, what string) []Finding {
		applicable, missing := len(after), []CoverageMiss(nil)
		for _, op := range after {
			if !hasVerb(op, verbs) {
				continue
			}
			missing = append(missing, CoverageMiss{
				Refs: opRefs(op),
				Finding: Finding{
					Category: CategorySequence,
					Severity: severity,
					Title:    aiBoundedText(capitalise(what)+" after packing", 90),
					Detail: fmt.Sprintf("%s is a %s step and it comes after %s, which packs the garment. "+
						"%s happens to a garment that is already in its bag.",
						opLabel(op), string(op.OperationType), packLabel, capitalise(what)),
					Refs:       opRefs(op),
					Suggestion: "Move the step before the packing step, or move packing to the end of the route.",
				},
			})
		}
		return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
			return Finding{
				Category: CategorySequence,
				Severity: severity,
				Title: aiBoundedText(fmt.Sprintf("%s after packing on %d of %d steps that follow the pack",
					capitalise(what), missing, applicable), 90),
				Detail:     fmt.Sprintf("These steps come after %s, which packs the garment.", packLabel),
				Refs:       sample,
				Suggestion: "Move them before the packing step, or move packing to the end of the route.",
			}
		})
	}

	var out []Finding
	out = append(out, group([]entity.TechCardOperationType{
		entity.OpTypeMachine, entity.OpTypeHandwork, entity.OpTypeFusing,
		entity.OpTypeHardwareSet, entity.OpTypePrint, entity.OpTypeWetProcess,
	}, SeverityError, "assembly or wet work")...)
	out = append(out, group([]entity.TechCardOperationType{entity.OpTypeFold}, SeverityWarning, "folding")...)
	return out
}

// ── A7. FUSING ПО СОБРАННЫМ УЗЛАМ ───────────────────────────────────────────────────────────────
//
// sequence, **error**. Читает входы шага (0307): вход с piece_id IS NULL — это УЗЕЛ, то есть нечто
// уже сшитое. Клеевая ставится на плоскую деталь до первого шва; на собранный узел её ставят,
// когда карточку писали задним числом.
//
// Подавитель: все входы — детали.
func checkA7FusingOnAssembledUnits(v *cardView) []Finding {
	applicable, missing := 0, []CoverageMiss(nil)
	for _, op := range v.ops {
		if op.OperationType != entity.OpTypeFusing {
			continue
		}
		applicable++
		var units []string
		for _, in := range op.AssemblyInputs {
			if in.Kind == entity.AssemblyInputUnit && in.Key != "" {
				units = append(units, in.Key)
			}
		}
		if len(units) == 0 {
			continue // подавитель: все входы — детали кроя
		}
		refs := opRefs(op)
		for _, u := range units {
			refs = append(refs, RefUnit(u))
		}
		missing = append(missing, CoverageMiss{
			Refs: opRefs(op),
			Finding: Finding{
				Category: CategorySequence,
				Severity: SeverityError,
				Title:    "Fusing applied to an assembled unit",
				Detail: fmt.Sprintf("%s fuses %s, and those are assembled units, not flat pieces. "+
					"Fusing is done on flat pieces before the first seam.",
					opLabel(op), quotedList(units)),
				Refs:       refs,
				Suggestion: "Move the fusing to the cut pieces it belongs to, before the seams that assemble them.",
			},
		})
	}

	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategorySequence,
			Severity: SeverityError,
			Title: fmt.Sprintf("Fusing applied to assembled units on %d of %d fusing operations",
				missing, applicable),
			Detail: "These fusing steps take assembled units as inputs. Fusing is done on flat pieces " +
				"before the first seam.",
			Refs:       sample,
			Suggestion: "Move the fusing onto the cut pieces, before the seams that assemble them.",
		}
	})
}

// ── A8. ЛЕГАЛЬНОСТЬ ПАРЫ WORK ↔ MACHINE ─────────────────────────────────────────────────────────
//
// integrity, warning. Читает каталог работ (0329/0330) и назначенную работу шага.
//
// ОСВЕДОМЛЁННАЯ ЗАПИСЬ ЭТО УЖЕ ГЕЙТИТ (internal/dto/techcard_operation_work.go): глагол работы
// сверяется с глаголом шага, а машина — со списком `operation_work_machine`. Живая поверхность у
// проверки поэтому узкая: строки, записанные до правил, и правки САМОГО КАТАЛОГА (миграция сузила
// список машин работы — сохранённая строка не переписывается задним числом). Страховка целостности
// стоит десяти строк и остаётся.
//
// КАТАЛОГ БЕРЁТСЯ ИЗ СНИМКА ПРОЦЕССА (entity.OperationWorkCatalogSnapshot), а не из БД: пакет в базу
// не ходит вовсе, а снимок публикуется один раз в store.New и за жизнь процесса не меняется.
// НЕЗАГРУЖЕННЫЙ каталог — это не «нарушений нет», а «этот прогон не проверял»: молчание,
// неотличимое от чистоты, — самая дорогая ложь аудита, поэтому оно уходит в NotChecked.
func checkA8WorkMachineLegality(v *cardView) []Finding {
	assigned := 0
	for _, op := range v.ops {
		if !nsEmpty(op.Work) {
			assigned++
		}
	}
	if assigned == 0 {
		return nil // подавитель: вида работы не назначено ни одному шагу — правилам не за что зацепиться
	}

	catalog := entity.OperationWorkCatalogSnapshot()
	if catalog == nil || catalog.Size() == 0 {
		v.notCheck("work ↔ machine legality (this process has not loaded the work catalog of migration 0329)")
		return nil
	}

	var unknown []string
	applicable, missing := 0, []CoverageMiss(nil)
	for _, op := range v.ops {
		token := strings.TrimSpace(op.Work.String)
		if token == "" {
			continue
		}
		work, ok := catalog.Lookup(token)
		if !ok {
			// Токен работы, которого каталог не знает, — не «незаконная пара», а строка, о которой
			// этому процессу сказать нечего. Называем это вслух и не выдумываем находку.
			unknown = append(unknown, fmt.Sprintf("%s (work %q)", opLabel(op), token))
			continue
		}
		applicable++

		machine := machineToken(op)
		switch {
		case string(op.OperationType) != work.Verb:
			missing = append(missing, CoverageMiss{
				Refs: opRefs(op),
				Finding: Finding{
					Category: CategoryIntegrity,
					Severity: SeverityWarning,
					Title:    aiBoundedText(fmt.Sprintf("Work %q does not belong to a %s step", token, string(op.OperationType)), 90),
					Detail: fmt.Sprintf("%s carries work %q, which the catalog (0329) declares a %q step, "+
						"while the step itself is %q.", opLabel(op), token, work.Verb, string(op.OperationType)),
					Refs:       opRefs(op),
					Suggestion: "Pick the work that matches the step, or change the step's type.",
				},
			})
		// Подавители машинной половины: работа не задаёт машин вовсе (machine_mode = none — ось «на
		// чём» у этого глагола не машинная), либо шаг машину не называет. Пустая машина — это
		// «не сказано», и утверждать по ней незаконность нельзя.
		case len(work.Machines) > 0 && machine != "" && !work.AllowsMachine(machine):
			missing = append(missing, CoverageMiss{
				Refs: opRefs(op),
				Finding: Finding{
					Category: CategoryIntegrity,
					Severity: SeverityWarning,
					Title:    aiBoundedText(fmt.Sprintf("Work %q does not run on a %s", token, machine), 90),
					Detail: fmt.Sprintf("%s names machine_type %q, and the catalog (0330) lists work %q as "+
						"running on %s.", opLabel(op), machine, token, strings.Join(work.Machines, " / ")),
					Refs:       opRefs(op),
					Suggestion: "Pick a machine from the work's list, or pick a work that runs on this machine.",
				},
			})
		}
	}

	if len(unknown) > 0 {
		v.notCheck("work ↔ machine legality on " + strings.Join(unknown, ", ") +
			": the work catalog of this process does not know that token")
	}

	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryIntegrity,
			Severity: SeverityWarning,
			Title: fmt.Sprintf("Work and machine disagree with the catalog on %d of %d steps that name a work",
				missing, applicable),
			Detail: "The assigned work names a verb or a machine that the work catalog (0329/0330) does " +
				"not allow on these steps.",
			Refs:       sample,
			Suggestion: "Re-pick the work, the step type or the machine on each of these steps.",
		}
	})
}

// ── A9. ЛЕГАСИ-ДРЕЙФ OP-PIECE ───────────────────────────────────────────────────────────────────
//
// integrity, warning, ОДНА агрегированная находка при любом расхождении. Читает легаси-связи
// tech_card_operation_piece (0199) против piece-входов tech_card_operation_input (0307).
//
// ⚠️ ЭТО СТОРОЖ У ДВЕРИ, КОТОРАЯ СЕГОДНЯ ЗАПЕРТА, И ЗНАТЬ ЭТО ВАЖНЕЕ, ЧЕМ ИМЕТЬ САМУ ПРОВЕРКУ.
// На пути ЧТЕНИЯ (store/techcard/production.go) PieceIds и PieceLineKeys наполняются ИЗ НОВОЙ
// таблицы — они её проекция, а 0199 читается только как ФОЛБЭК для операций, у которых новых строк
// нет вовсе. Значит на карточке, приехавшей из GetTechCardById, расхождение невозможно, и проверка
// молчит структурно, а не потому, что данные чисты. Она остаётся ровно затем, зачем задуман весь
// пересчёт §1: единственный писатель держит две таблицы в локстепе, и дрейф означал бы запись мимо
// конвертера или баг проекции — то есть ровно тот случай, когда на «оно же согласовано» полагаться
// нельзя. Дешевле десяти строк такая страховка не бывает.
//
// Агрегация §3.0 здесь НЕ применяется, и это не забывчивость: дизайн требует ОДНУ находку при
// расхождении хоть на одной операции, а Aggregate на двух расхождениях выдал бы две
// пер-операционные. Дробь «2 of 48» тут ничего не значила бы: дрейф — это состояние карточки, а не
// покрытие множества.
func checkA9LegacyPieceLinkDrift(v *cardView) []Finding {
	var (
		drifted []*entity.TechCardOperation
		samples []string
	)
	for _, op := range v.ops {
		legacy := legacyPieceKeys(v, op)
		inputs := inputPieceKeys(op)
		if equalKeySets(legacy, inputs) {
			continue
		}
		drifted = append(drifted, op)
		if len(samples) < 3 {
			samples = append(samples, fmt.Sprintf("%s: legacy links %s, assembly inputs %s",
				opLabel(op), quotedNames(v, legacy), quotedNames(v, inputs)))
		}
	}
	if len(drifted) == 0 {
		return nil
	}

	refs := make([]string, 0, 4)
	for _, op := range drifted {
		if len(refs) == 3 {
			break
		}
		refs = append(refs, opRefs(op)...)
	}
	if len(refs) == 0 {
		refs = []string{RefCard}
	}

	return []Finding{{
		Category: CategoryIntegrity,
		Severity: SeverityWarning,
		Title:    "Legacy piece links diverge from assembly inputs",
		Detail: fmt.Sprintf("Internal inconsistency on %d operation(s): the cut pieces linked through "+
			"tech_card_operation_piece (0199) are not the piece inputs of tech_card_operation_input "+
			"(0307). One writer keeps the two tables in lockstep, so a divergence means a write that "+
			"went around the converter, or a bug — the assembly graph shown on the card and the legacy "+
			"links two other features still read no longer describe the same route.", len(drifted)),
		Evidence:   samples,
		Refs:       refs,
		Suggestion: "Re-save the card through the current bundle and re-run the audit; if it survives that, report it.",
	}}
}

// legacyPieceKeys is the 0199 side: PieceIds resolved to line keys. PieceLineKeys is used when the
// ids are absent — the store fills both together, but a card converted from the wire and not yet
// saved carries only the keys, and reading that as «the legacy table is empty» would be an
// accusation against a card that was never stored.
func legacyPieceKeys(v *cardView, op *entity.TechCardOperation) []string {
	if len(op.PieceIds) == 0 && len(op.PieceLineKeys) > 0 {
		return append([]string(nil), op.PieceLineKeys...)
	}
	out := make([]string, 0, len(op.PieceIds))
	for _, id := range op.PieceIds {
		if p := v.pieceByID[id]; p != nil && p.LineKey != "" {
			out = append(out, p.LineKey)
			continue
		}
		// Ссылка в никуда сама по себе расхождение: она не совпадёт ни с одним входом.
		out = append(out, fmt.Sprintf("piece_id:%d", id))
	}
	return out
}

// inputPieceKeys is the 0307 side: the piece half of the canonical input list.
func inputPieceKeys(op *entity.TechCardOperation) []string {
	out := make([]string, 0, len(op.AssemblyInputs))
	for _, in := range op.AssemblyInputs {
		if in.Kind == entity.AssemblyInputPiece && in.Key != "" {
			out = append(out, in.Key)
		}
	}
	return out
}

// ── A10. WET-PROCESS ПРОТИВ ЧУВСТВИТЕЛЬНЫХ КОМПОНЕНТОВ ──────────────────────────────────────────
//
// sequence, warning. Читает порядок шагов, связи шаг→строка BOM (0200) и вид строки (kind, 0278).
//
// Фольга, термоперенос, стразы и пайетки ставятся ПОСЛЕ мокрой обработки: стирка, энзим и покраска
// снимают их с изделия. Подавители: wet_process в маршруте нет; у шага нет линка на строку BOM;
// вид строки NULL (не классифицировано — утверждать по нему нельзя) или не из набора.
func checkA10SensitiveBeforeWetProcess(v *cardView) []Finding {
	// Индекс ПОСЛЕДНЕЙ мокрой обработки: находка требует «раньше wet_process», а значит достаточно
	// одной мокрой обработки после шага — самая поздняя отвечает на это одним сравнением.
	lastWet := -1
	for i, op := range v.ops {
		if op.OperationType == entity.OpTypeWetProcess {
			lastWet = i
		}
	}
	if lastWet < 0 {
		return nil // подавитель: мокрой обработки в маршруте нет
	}
	wetLabel := opLabel(v.ops[lastWet])

	applicable, missing := 0, []CoverageMiss(nil)
	for i, op := range v.ops {
		if op.OperationType != entity.OpTypeHardwareSet && op.OperationType != entity.OpTypePrint {
			continue
		}
		sensitive := sensitiveBomLines(v, op)
		if len(sensitive) == 0 {
			continue // подавители: линка нет, либо kind NULL, либо kind не из набора
		}
		applicable++
		if i >= lastWet {
			continue
		}

		refs := opRefs(op)
		names := make([]string, 0, len(sensitive))
		for _, b := range sensitive {
			refs = append(refs, RefBom(b.Name))
			names = append(names, fmt.Sprintf("%q (kind %s)", b.Name, strings.TrimSpace(b.Kind.String)))
		}
		refs = append(refs, opRefs(v.ops[lastWet])...)
		missing = append(missing, CoverageMiss{
			Refs: opRefs(op),
			Finding: Finding{
				Category: CategorySequence,
				Severity: SeverityWarning,
				Title:    "Sensitive component set before wet processing",
				Detail: fmt.Sprintf("%s applies %s, and %s runs after it. Wet processing takes foil, "+
					"heat transfers, rhinestones and sequins off the garment.",
					opLabel(op), strings.Join(names, ", "), wetLabel),
				Refs:       refs,
				Suggestion: "Move the step after the wet process, or state why this component survives it.",
			},
		})
	}

	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategorySequence,
			Severity: SeverityWarning,
			Title: fmt.Sprintf("Sensitive components set before wet processing on %d of %d steps",
				missing, applicable),
			Detail: fmt.Sprintf("These steps apply foil, heat transfer, rhinestone or sequin lines "+
				"before %s.", wetLabel),
			Refs:       sample,
			Suggestion: "Move them after the wet process, or state why those components survive it.",
		}
	})
}

// wetSensitiveBomKinds — виды строк BOM, которые мокрая обработка снимает с изделия (§3.1 A10).
var wetSensitiveBomKinds = map[string]bool{
	string(entity.BomKindFoil):         true,
	string(entity.BomKindHeatTransfer): true,
	string(entity.BomKindRhinestone):   true,
	string(entity.BomKindSequin):       true,
}

// sensitiveBomLines returns the step's BOM links that point at a sensitive line. Строка с kind NULL
// НЕ СЧИТАЕТСЯ чувствительной: NULL здесь значит «не классифицировано», а не «может быть чем
// угодно», и предупреждение по ней было бы предупреждением про неизвестность.
func sensitiveBomLines(v *cardView, op *entity.TechCardOperation) []*entity.TechCardBomItem {
	var out []*entity.TechCardBomItem
	seen := map[string]bool{}
	add := func(b *entity.TechCardBomItem) {
		if b == nil || seen[b.LineKey] {
			return
		}
		if !wetSensitiveBomKinds[strings.TrimSpace(b.Kind.String)] {
			return
		}
		seen[b.LineKey] = true
		out = append(out, b)
	}
	for _, key := range op.BomLineKeys {
		add(v.bomByKey[key])
	}
	for _, id := range op.BomIds {
		add(v.bomByID[id])
	}
	return out
}

// ── ОБЩЕЕ ───────────────────────────────────────────────────────────────────────────────────────

// opNumber returns the step's number and whether it has one. Легаси-строка без номера существует
// как класс, и якоря у неё нет.
func opNumber(op *entity.TechCardOperation) (int32, bool) {
	if op == nil || !op.OperationNumber.Valid {
		return 0, false
	}
	return op.OperationNumber.Int32, true
}

// opRefs is the anchor list of a per-operation finding. Шаг без номера якорится на карточку: находка
// без единого разрешившегося якоря дропается (§8 п.2), и потерять её молча хуже, чем показать на
// уровне карточки.
func opRefs(op *entity.TechCardOperation) []string {
	if n, ok := opNumber(op); ok {
		return []string{RefOp(n)}
	}
	return []string{RefCard}
}

// opLabel names the step the way the finding text does.
func opLabel(op *entity.TechCardOperation) string {
	if n, ok := opNumber(op); ok {
		return fmt.Sprintf("Operation %d", n)
	}
	return "An operation with no number"
}

// opList renders a list of operation numbers for prose.
func opList(nums []int32) string {
	if len(nums) == 0 {
		return "no operation"
	}
	parts := make([]string, 0, len(nums))
	for _, n := range nums {
		parts = append(parts, itoa32(n))
	}
	if len(parts) == 1 {
		return "op " + parts[0]
	}
	return "ops " + strings.Join(parts, ", ")
}

// machineToken is the step's EXPLICIT machine token. Резолв через machine_profile_key сюда не
// доходит намеренно — ровно та же граница, что у правил записи (dto/techcard_operation_work.go):
// иначе законность поля зависела бы от парка карточки, который можно вычистить, не открыв ни
// одного шага.
func machineToken(op *entity.TechCardOperation) string {
	return strings.TrimSpace(op.MachineType.String)
}

// machineWord renders a machine token as the thing it makes, for prose.
func machineWord(machine string) string {
	switch machine {
	case "button_attach":
		return "button"
	case "buttonhole":
		return "buttonhole"
	default:
		return "repeat"
	}
}

func hasVerb(op *entity.TechCardOperation, verbs []entity.TechCardOperationType) bool {
	for _, v := range verbs {
		if op.OperationType == v {
			return true
		}
	}
	return false
}

func nsEmpty(v sql.NullString) bool { return !v.Valid || strings.TrimSpace(v.String) == "" }

func niEmpty(v sql.NullInt32) bool { return !v.Valid }

func ndEmpty(v decimal.NullDecimal) bool { return !v.Valid }

func capitalise(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func quotedList(items []string) string {
	parts := make([]string, 0, len(items))
	for _, i := range items {
		parts = append(parts, `"`+i+`"`)
	}
	return strings.Join(parts, ", ")
}

// quotedNames renders piece line keys as the names a technologist knows.
func quotedNames(v *cardView, keys []string) string {
	if len(keys) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, `"`+v.pieceName(k)+`"`)
	}
	return strings.Join(parts, ", ")
}

// equalKeySets compares two key lists as SETS: order is display order on one side and link order on
// the other, and the two were never promised to agree on order — only on membership.
func equalKeySets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
