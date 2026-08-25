package techcardanalysis

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ── БЛОК «ГОТОВНОСТЬ И ОБВЯЗКА»: C1–C6 (design §3.3) ────────────────────────────────────────────
//
// ЧТО ОБЩЕГО У ЭТИХ ШЕСТИ. Они не про то, что в карточке НЕВЕРНО, а про то, чего в ней ЕЩЁ НЕТ, —
// и потому почти все несут класс CategoryReadiness, единственный, который на черновике схлопывается
// в одну строку (§3.0, CollapseReadiness). На черновике шесть отдельных «нет лейблов», «нет
// профилей», «нет эскиза» вытеснили бы с экрана единственную настоящую находку; на карточке, вышедшей
// из черновика, тот же список — чек-лист выпуска, и он разворачивается целиком.
//
// ПОЭТОМУ У КАЖДОЙ ЗДЕСЬ ЗАПОЛНЕНО `Clause`. Это короткая фраза («no labels», «no equipment
// profiles»), из которых собирается перечисление схлопнутой находки. Заголовки у находок —
// предложения; склеенные через « · » шесть предложений читались бы как поток, а не как список.
// Находка класса readiness с пустым Clause молча выпадает из перечисления — то есть исчезает с
// черновика совсем.
//
// ДВА ИСКЛЮЧЕНИЯ ИЗ КЛАССА, ОБА НАМЕРЕННЫЕ:
//   - битая мягкая ссылка на профиль (C4) — CategoryIntegrity: на черновике она такой же дефект,
//     как на релизе, и схлопывать её вместе с «ещё не заполнено» значило бы спрятать;
//   - предсказание отказа релиза (C6) — CategoryAssembly (константа объявлена в T2 ровно для этого
//     места): §3.3 называет класс C6 словом «assembly», и перекрашивать предсказание отказа в
//     «ещё не готово» было бы неправдой — это не работа, которую не сделали, а работа, которую
//     сделали не сходящейся.
//
// О СЛОВЕ «info» В §3.3. Дизайн дважды просит регистр «info» (спека лейбла без строки BOM; два
// профиля одного вида при пустом ключе шага), но severity — ЗАКРЫТЫЙ список из трёх
// (blocker|error|warning, §4/§7.1 п.3, ValidSeverities заморожена T2), и четвёртого члена в нём
// нет. Четвёртая severity поехала бы на провод и в клиент, где её никто не рисует. Поэтому «info»
// здесь — регистр ТЕКСТА, а не поля: находка выпускается warning'ом, а её формулировка не
// утверждает дефекта («the link is legal, and it means…»). Тот же приём, которым §8 коэрцит
// модельное severity="question" в warning + категорию.
var _ = register(
	needsCard(checkC1Floor),
	needsCard(checkC2PrintPacketDryRun),
	needsCard(checkC3LabelBridge),
	needsCard(checkC4EquipmentPark),
	needsCard(checkC5TechnicalSketch),
	needsCard(checkC6ReleaseRuleFour),
	needsCard(checkC7SmvCoverage),
	needsCard(checkC8WorkCoverage),
	needsCard(checkC9FinishingBlock),
)

// ── C1. ПОЛ ПОД ВСЕМ ────────────────────────────────────────────────────────────────────────────
//
// readiness, **error**. Ноль операций / ноль деталей / нет размерного ряда.
//
// ERROR, А НЕ WARNING, И ЭТО ВИДНО ИЗ МЕХАНИКИ СХЛОПЫВАНИЯ: схлопнутая находка берёт МАКСИМУМ
// severity, и если бы «операций ноль» было warning'ом, черновик с пустым маршрутом читался бы ровно
// так же, как черновик, которому не хватает эскиза.
//
// ФАКТЫ СЧИТАЮТСЯ В ФОРМЕ entity.TechCardReadinessFacts (entity:4049) — той же, которой их считает
// стор одним запросом. Здесь заполняются ТОЛЬКО три поля, которые эта проверка читает: пакет в БД
// не ходит, а остальные девятнадцать полей пришлось бы выдумать, и выдуманный HasCosting=false
// однажды прочитали бы как факт. Значение локальное и наружу не уходит.
func checkC1Floor(v *cardView) []Finding {
	facts := entity.TechCardReadinessFacts{
		Operations: len(v.card.Operations),
		Pieces:     len(v.card.Pieces),
		Sizes:      len(v.card.SizeIds),
	}

	var out []Finding
	add := func(title, detail, suggestion, clause string) {
		out = append(out, Finding{
			Category:   CategoryReadiness,
			Severity:   SeverityError,
			Title:      title,
			Detail:     detail,
			Refs:       []string{RefCard},
			Suggestion: suggestion,
			Clause:     clause,
		})
	}

	if facts.Operations == 0 {
		add("The card has no operations",
			"tech_card_operation is empty: the card describes no route at all, so nothing about "+
				"assembly, machines, time or labour cost can be said about it — by this analysis or by anyone.",
			"Enter the route, step by step.", "no operations")
	}
	if facts.Pieces == 0 {
		add("The card has no cut pieces",
			"tech_card_piece is empty: nothing is cut, so no step has anything to sew, no marker has "+
				"anything to lay and no norm has anything to measure.",
			"Load the pattern pieces (DXF) so the card knows what is cut.", "no cut pieces")
	}
	if facts.Sizes == 0 {
		add("The card declares no size range",
			"tech_card_size is empty: the style is not made in any size, so grading, the size-averaged "+
				"cost basis and every per-size norm have no set to work over.",
			"Declare the size range of the style.", "no size range")
	}
	return out
}

// ── C2. СУХОЙ ПРОГОН ПЕЧАТНОГО ПАКЕТА ───────────────────────────────────────────────────────────
//
// readiness, warning, ОДНА находка со списком секций, которые печатный пакет напечатает пустыми.
//
// ОДНА, А НЕ ПЯТЬ. Это не пять разных дефектов, а один: пакет, который уедет в цех с пустыми
// разделами. Пять находок вытеснили бы всё остальное, а список из пяти строк внутри одной читается
// за секунду.
//
// БАЗОВЫЙ РАЗМЕР ВХОДИТ В СПИСОК. §3.3 перечисляет его в правиле, но в примере по карточке 8
// называет только четыре пустоты и о нём забывает; на проде `tech_card.base_sample_size_id` карточки
// 8 — NULL при объявленном ряде из четырёх размеров, то есть пятая пустота реальна. Печатный пакет
// печатает «размер образца» строкой шапки, и пустая она там ровно так же, как пустой hem_finish.
//
// Подавитель: секция заполнена. Лейблы спрашиваются только у sellable-карточки (у вспомогательной —
// пакета, вешалки — лейблов не бывает по определению, NF-07).
func checkC2PrintPacketDryRun(v *cardView) []Finding {
	c := v.construction()

	type gap struct{ label, column string }
	var gaps []gap

	if nsEmpty(c.HemFinish) {
		gaps = append(gaps, gap{"hem finish", "tech_card_construction.hem_finish"})
	}
	if nsEmpty(c.Notes) {
		gaps = append(gaps, gap{"construction notes", "tech_card_construction.notes"})
	}
	if v.card.Purpose == entity.TechCardPurposeSellable && len(v.card.Labels) == 0 {
		gaps = append(gaps, gap{"labels", "tech_card_label (0 rows on a sellable card)"})
	}
	if v.card.Packaging == nil || isEmptyPackaging(v.card.Packaging) {
		gaps = append(gaps, gap{"packaging", "tech_card_packaging"})
	}
	if !v.card.BaseSampleSizeId.Valid {
		gaps = append(gaps, gap{"base sample size", "tech_card.base_sample_size_id"})
	}
	if len(gaps) == 0 {
		return nil
	}

	labels := make([]string, 0, len(gaps))
	columns := make([]string, 0, len(gaps))
	for _, g := range gaps {
		labels = append(labels, g.label)
		columns = append(columns, g.column)
	}

	return []Finding{{
		Category: CategoryReadiness,
		Severity: SeverityWarning,
		Title:    fmt.Sprintf("The print packet would go out with %d empty sections", len(gaps)),
		Detail: fmt.Sprintf("Printed today, the tech pack would go out with these sections empty: %s. "+
			"The columns behind them: %s.", joinAnd(labels), strings.Join(columns, ", ")),
		Refs:       []string{RefCard},
		Suggestion: "Fill the sections that the factory reads off the printed packet.",
		Clause:     fmt.Sprintf("print packet has %d empty sections", len(gaps)),
	}}
}

// isEmptyPackaging reports whether the packaging block says nothing at all. Присутствие СТРОКИ не
// равно заполненности: строка, созданная сохранением соседней секции, печатается такой же пустой,
// как её отсутствие.
func isEmptyPackaging(p *entity.TechCardPackaging) bool {
	return nsEmpty(p.FoldingMethod) && nsEmpty(p.Polybag) && nsEmpty(p.BagSticker) &&
		nsEmpty(p.Inserts) && niEmpty(p.UnitsPerBox) && nsEmpty(p.BoxMarking) &&
		nsEmpty(p.BoxDimensions) && niEmpty(p.WeightNetGrams) && niEmpty(p.WeightGrossGrams) &&
		nsEmpty(p.Notes)
}

// ── C3. МОСТ ЛЕЙБЛЫ ↔ BOM ───────────────────────────────────────────────────────────────────────
//
// readiness, warning; ГЕЙТ СТАДИИ ≥ sms.
//
// ЗАЧЕМ ГЕЙТ. Лейблы заводят к продажному образцу, а не к прототипу: спрашивать их у карточки на
// стадии proto значит спрашивать работу, которой на этой стадии не бывает, — и получать шум на
// каждой ранней карточке студии. Карточка 8 стоит на proto и поэтому здесь МОЛЧИТ, хотя лейблов у
// неё нет нигде.
//
// ДВЕ СТОРОНЫ МОСТА. `tech_card_label` — это СПЕЦИФИКАЦИЯ («что написано на бирке, где она стоит»),
// а `tech_card_bom_item` section='label' — МАТЕРИАЛ, который за неё платят. Связывает их мягкий
// линк `tech_card_label.bom_item_id` (0174, ON DELETE SET NULL — разрыв легален и происходит сам).
// Нет ни одной половины → находка; половина без пары → тот самый регистр «info» (см. шапку файла).
//
// Подавители: стадия ниже sms; карточка не sellable; обе половины на месте и связаны.
func checkC3LabelBridge(v *cardView) []Finding {
	if v.card.Purpose != entity.TechCardPurposeSellable {
		return nil
	}
	stage, known := entity.TechCardStageOrdinal(v.card.Stage)
	sms, _ := entity.TechCardStageOrdinal(entity.TechCardStageSMS)
	if !known || stage < sms {
		return nil
	}

	lines := v.bomLinesOfSection(entity.BomSectionLabel)
	specs := v.card.Labels

	if len(specs) == 0 && len(lines) == 0 {
		return []Finding{{
			Category: CategoryReadiness,
			Severity: SeverityWarning,
			Title:    "A sellable style at " + string(v.card.Stage) + " with no labels anywhere",
			Detail: "There is no tech_card_label spec and no BOM line in section 'label'. A sellable " +
				"garment carries at least a care label (label_type='care') — the one the customer is " +
				"entitled to and the factory is obliged to sew in — and this card neither describes one " +
				"nor buys one.",
			Refs:       []string{RefCard},
			Suggestion: "Add the label specs the style carries, and the BOM lines that pay for them.",
			Clause:     "no labels anywhere",
		}}
	}

	var out []Finding

	// Спека без линии: описано, но не куплено.
	{
		applicable, missing := 0, []CoverageMiss(nil)
		for i := range specs {
			s := &specs[i]
			applicable++
			if s.BomItemId.Valid && v.bomByID[int(s.BomItemId.Int32)] != nil {
				continue
			}
			name := labelSpecName(s)
			missing = append(missing, CoverageMiss{
				Refs: []string{RefCard},
				Finding: Finding{
					Category: CategoryReadiness,
					Severity: SeverityWarning,
					Title:    aiBoundedText(fmt.Sprintf("The %s label spec is not linked to a BOM line", name), 90),
					Detail: fmt.Sprintf("The %s label is described on the card and no BOM line pays for it "+
						"(tech_card_label.bom_item_id is unset, or points at a line that no longer exists — "+
						"the link is ON DELETE SET NULL, so it breaks by itself and legally). The label is "+
						"then sewn in and costed at nothing.", name),
					Refs:       []string{RefCard},
					Suggestion: "Link the spec to the BOM line that buys the label, or add that line.",
					Clause:     fmt.Sprintf("%s label not costed", name),
				},
			})
		}
		out = append(out, Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
			return Finding{
				Category: CategoryReadiness,
				Severity: SeverityWarning,
				Title: fmt.Sprintf("%d of %d label specs are not linked to a BOM line",
					missing, applicable),
				Detail: "These labels are described on the card and nothing in the BOM pays for them " +
					"(tech_card_label.bom_item_id) — they are sewn in and costed at nothing.",
				Refs:       sample,
				Suggestion: "Link each spec to the BOM line that buys it.",
				Clause:     fmt.Sprintf("%d label specs not costed", missing),
			}
		})...)
	}

	// Линия без спеки: куплено, но не описано.
	{
		linked := map[int]bool{}
		for i := range specs {
			if specs[i].BomItemId.Valid {
				linked[int(specs[i].BomItemId.Int32)] = true
			}
		}
		applicable, missing := 0, []CoverageMiss(nil)
		for _, b := range lines {
			applicable++
			if linked[b.Id] {
				continue
			}
			missing = append(missing, CoverageMiss{
				Refs: []string{RefBom(b.Name)},
				Finding: Finding{
					Category: CategoryReadiness,
					Severity: SeverityWarning,
					Title:    aiBoundedText(fmt.Sprintf("BOM line %q is a label nothing describes", b.Name), 90),
					Detail: fmt.Sprintf("%q sits in section 'label' of the BOM and no tech_card_label spec "+
						"points at it. The label is bought, and what is printed on it, where it goes and how "+
						"it is attached are stated nowhere.", b.Name),
					Refs:       []string{RefBom(b.Name)},
					Suggestion: "Describe the label in the labels section and link it to this line.",
					Clause:     fmt.Sprintf("label line %q undescribed", b.Name),
				},
			})
		}
		out = append(out, Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
			return Finding{
				Category: CategoryReadiness,
				Severity: SeverityWarning,
				Title: fmt.Sprintf("%d of %d label BOM lines have no spec describing them",
					missing, applicable),
				Detail: "These lines buy labels that no tech_card_label spec describes — what is printed " +
					"on them, where they go and how they are attached is stated nowhere.",
				Refs:       sample,
				Suggestion: "Describe each label and link the spec to its line.",
				Clause:     fmt.Sprintf("%d label lines undescribed", missing),
			}
		})...)
	}

	return out
}

// labelSpecName names a label spec for prose: its type, falling back to the placement.
func labelSpecName(s *entity.TechCardLabel) string {
	if t := strings.TrimSpace(string(s.LabelType)); t != "" {
		return t
	}
	if p := strings.TrimSpace(s.Placement.String); p != "" {
		return p
	}
	return "unnamed"
}

// ── C4. ПАРК ОБОРУДОВАНИЯ ───────────────────────────────────────────────────────────────────────
//
// Три ветви, и они РАЗНОГО КЛАССА — это не педантизм, а разница в том, что делать дальше.
//
//  1. профилей нет вовсе, а машины карточка называет  → readiness / warning: работа не сделана;
//  2. ключ профиля указывает в никуда                 → integrity / **error**: ссылка битая, и на
//     черновике это такой же дефект, как на релизе (потому и не readiness — схлопывать нельзя);
//  3. подходящих профилей два и больше, а шаг не выбрал → регистр «info» (см. шапку): наследование
//     сработает, но какой именно режим достанется — не решено.
//
// ВЕТКА 3 СУЖЕНА ПРОТИВ БУКВЫ §3.3, И НАМЕРЕННО. Дизайн говорит «≥2 профилей одного kind при пустом
// ключе шага». Но карточка с оверлочным и петельным профилем — это два профиля вида machine, а
// петельный шаг наследует ровно один из них: неоднозначности нет, а находка была бы. Поэтому
// применимость считается ПО ПОДХОДЯЩИМ профилям: у машинного шага — профили той же машины, у
// ВТО-шага — универсальные плюс названные под его глагол (то же правило применимости, что у A3).
// Это строгое сужение: там, где молчит буква дизайна, молчит и этот код.
func checkC4EquipmentPark(v *cardView) []Finding {
	eq := v.equipment()
	var out []Finding

	// ── 1. Парка нет вовсе ──────────────────────────────────────────────────────────────────────
	machineTypes := map[string]bool{}
	for _, op := range v.ops {
		if m := machineToken(op); m != "" {
			machineTypes[m] = true
		}
	}
	if len(eq.Machines) == 0 && len(eq.Presses) == 0 && len(machineTypes) > 0 {
		types := sortedTokenSet(machineTypes)
		out = append(out, Finding{
			Category: CategoryReadiness,
			Severity: SeverityWarning,
			Title:    fmt.Sprintf("No equipment profiles on a card that names %d machine types", len(types)),
			Detail: fmt.Sprintf("The route runs on %s, and tech_card_equipment_profile is empty "+
				"for this card. Every step therefore inherits nothing: needle, thread count, tension, "+
				"stitch density and every pressing setting are decided at the bench, differently on each shift.",
				quotedList(types)),
			Refs:       []string{RefCard},
			Suggestion: "Add the machine and press profiles this style is sewn and pressed on.",
			Clause:     "no equipment profiles",
		})
	}

	// ── 2. Мягкая ссылка в никуда ───────────────────────────────────────────────────────────────
	machineKeys := map[string]bool{}
	for i := range eq.Machines {
		if k := strings.TrimSpace(eq.Machines[i].ProfileKey); k != "" {
			machineKeys[k] = true
		}
	}
	pressKeys := map[string]bool{}
	for i := range eq.Presses {
		if k := strings.TrimSpace(eq.Presses[i].ProfileKey); k != "" {
			pressKeys[k] = true
		}
	}

	applicable, missing := 0, []CoverageMiss(nil)
	for _, op := range v.ops {
		for _, ref := range []struct {
			column string
			key    string
			known  map[string]bool
			kind   string
		}{
			{"machine_profile_key", strings.TrimSpace(op.MachineProfileKey.String), machineKeys, "machine"},
			{"press_profile_key", strings.TrimSpace(op.PressProfileKey.String), pressKeys, "press"},
		} {
			if ref.key == "" {
				continue
			}
			applicable++
			if ref.known[ref.key] {
				continue
			}
			missing = append(missing, CoverageMiss{
				Refs: opRefs(op),
				Finding: Finding{
					Category: CategoryIntegrity,
					Severity: SeverityError,
					Title:    aiBoundedText(fmt.Sprintf("%s points at a %s profile the card does not have", ref.column, ref.kind), 90),
					Detail: fmt.Sprintf("%s names %s %q, and no %s profile of this card carries that key. "+
						"The reference is soft — there is no FK, deliberately — so nothing stopped the "+
						"profile from being deleted or renamed, and the step now inherits nothing while "+
						"claiming to inherit something.", opLabel(op), ref.column, ref.key, ref.kind),
					Refs:       opRefs(op),
					Suggestion: "Point the step at an existing profile, or clear the key so it stops claiming one.",
				},
			})
		}
	}
	out = append(out, Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryIntegrity,
			Severity: SeverityError,
			Title: fmt.Sprintf("%d of %d equipment profile references point at nothing",
				missing, applicable),
			Detail: "These steps name a machine_profile_key or press_profile_key that no profile of the " +
				"card carries (soft reference with no FK) — they inherit nothing while claiming to " +
				"inherit something.",
			Refs:       sample,
			Suggestion: "Re-point or clear the broken keys.",
		}
	})...)

	// ── 3. Подходящих профилей несколько, а шаг не выбрал ───────────────────────────────────────
	ambApplicable, ambMissing := 0, []CoverageMiss(nil)
	for _, op := range v.ops {
		kind, candidates := applicableProfiles(op, eq)
		if kind == "" || candidates < 1 {
			continue
		}
		ambApplicable++
		key := op.MachineProfileKey
		if kind == "press" {
			key = op.PressProfileKey
		}
		if !nsEmpty(key) || candidates < 2 {
			continue
		}
		ambMissing = append(ambMissing, CoverageMiss{
			Refs: opRefs(op),
			Finding: Finding{
				Category: CategoryReadiness,
				Severity: SeverityWarning,
				Title:    aiBoundedText(fmt.Sprintf("%s could inherit any of %d %s profiles", opLabel(op), candidates, kind), 90),
				Detail: fmt.Sprintf("The card carries %d %s profiles this step could take its settings "+
					"from, and the step names none of them. Inheritance is legal and the step will run — "+
					"but which set of settings it runs with is not written down anywhere.",
					candidates, kind),
				Refs:       opRefs(op),
				Suggestion: "Point the step at the profile it actually runs on.",
				Clause:     fmt.Sprintf("%s: %s profile not chosen", opLabel(op), kind),
			},
		})
	}
	out = append(out, Aggregate(ambApplicable, ambMissing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryReadiness,
			Severity: SeverityWarning,
			Title: fmt.Sprintf("%d of %d steps could inherit from more than one equipment profile",
				missing, applicable),
			Detail: "Several profiles of the card apply to each of these steps and the step names none — " +
				"inheritance is legal, but which settings each step runs with is written down nowhere.",
			Refs:       sample,
			Suggestion: "Point each step at the profile it actually runs on.",
			Clause:     fmt.Sprintf("%d steps have not chosen an equipment profile", missing),
		}
	})...)

	return out
}

// applicableProfiles returns which park a step inherits from ("machine" | "press" | "") and how many
// profiles of that park could apply to it.
//
// Правило применимости у ВТО — то же, что у A3: универсальный профиль (press_operation_type NULL)
// применим ко всякому термошагу, названный — только к своему глаголу.
//
// ВТОРОЙ ЭКЗЕМПЛЯР ТОГО ЖЕ ПРАВИЛА живёт в profilesFor (route.go, checkA3PressParameters), и
// удерживает их от расхождения РОВНО ОДИН тест — TestC4AppliesThePressRuleOfA3 в этом файле.
// Правишь здесь — правь и там; тест обязан покраснеть, если правки разошлись.
func applicableProfiles(op *entity.TechCardOperation, eq *entity.TechCardEquipmentDefaults) (string, int) {
	switch op.OperationType {
	case entity.OpTypePress, entity.OpTypePressOpen, entity.OpTypeFusing:
		n := 0
		for i := range eq.Presses {
			p := &eq.Presses[i]
			if nsEmpty(p.PressOperationType) || strings.TrimSpace(p.PressOperationType.String) == string(op.OperationType) {
				n++
			}
		}
		return "press", n
	}
	machine := machineToken(op)
	if machine == "" {
		return "", 0
	}
	n := 0
	for i := range eq.Machines {
		if strings.TrimSpace(eq.Machines[i].MachineType) == machine {
			n++
		}
	}
	return "machine", n
}

// ── C5. ЭСКИЗ ───────────────────────────────────────────────────────────────────────────────────
//
// readiness, warning. Ноль строк `tech_card_media` с category='technical' (0092 — КАТЕГОРИЯ, а не
// kind: kind описывает, чем файл является, а category — в каком из двух списков карточки он лежит).
//
// ЧТО ЭТА НАХОДКА ЗНАЧИТ НА САМОМ ДЕЛЕ: не «не приложили картинку», а «эскизных проверок не будет
// ни у кого» — ни у машины, ни у модели, ни у технолога, читающего пакет. Выносок карточка 8 не
// несёт вовсе (48/48 callout_number NULL), но это контекст для модели, а не вторая находка.
//
// СТРОКИ В NotChecked ЗДЕСЬ НЕ ДОБАВЛЯЕТСЯ: §3.3 просит «заодно статическую строку», и она уже
// статически едет в каждом прогоне (staticNotChecked, T2) — «sketch (not reviewed: the analysis path
// is text-only)». Вторая строка о том же говорила бы то же самое дважды.
func checkC5TechnicalSketch(v *cardView) []Finding {
	for i := range v.card.Media {
		if v.card.Media[i].Category == entity.TechCardMediaCategoryTechnical {
			return nil
		}
	}
	return []Finding{{
		Category: CategoryReadiness,
		Severity: SeverityWarning,
		Title:    "The card carries no technical sketch",
		Detail: "tech_card_media holds no row with category='technical'. Nothing on this card can " +
			"be checked against a drawing — not by this analysis, which is text-only in any case, and not " +
			"by the technologist reading the printed packet.",
		Refs:       []string{RefCard},
		Suggestion: "Attach the technical sketch of the style.",
		Clause:     "no technical sketch",
	}}
}

// ── C6. ПРАВИЛО 4 НА ЧЕРНОВИКЕ ──────────────────────────────────────────────────────────────────
//
// assembly, warning. ЕДИНСТВЕННЫЕ подлинно топологические находки этого пакета (§1): всё остальное —
// циклы, ссылки вперёд, дубли-производители, двойное потребление — запись отвергает на каждом
// сохранении, и искать их в сохранённой карточке значит сторожить запертую дверь.
//
// А ЭТИ ДВА — НАХОДИМЫ, потому что правило 4 (ровно один терминал; каждая деталь в него попадает)
// включается ТОЛЬКО НА РЕЛИЗЕ (assemblyReleaseCheck, dto/techcard_assembly.go). До релиза карточку
// с двумя терминалами сохранить можно, и она сохраняется.
//
// ТЕКСТ ОБЯЗАН НАЗЫВАТЬ РЕЛИЗНЫЙ ГЕЙТ. Это не мнение аудита о том, как надо собирать пиджак, — это
// предсказание того, что релиз откажет; без этой фразы находка читается как вкусовщина, и её
// закроют.
//
// СТАДИЕЙ НЕ ГЕЙТИТСЯ. Соблазн «показывать только на черновике» есть (§3.3 так и называется), но
// релиз-чек стоит на РЕЛИЗЕ, а не на выходе из черновика: карточка в in_review с двумя терминалами —
// это карточка, которую вот-вот попробуют выпустить, и молчать на ней хуже всего. На выпущенной
// карточке проверка вакуумна сама собой — состояние, которое релиз отверг, до неё не доезжает.
//
// Подавитель: ни одного производящего шага (gt.Marked) — неразмеченная карточка проходит релиз
// вакуумно, и предсказывать по ней нечего.
func checkC6ReleaseRuleFour(v *cardView) []Finding {
	if !v.gt.Marked {
		return nil
	}
	var out []Finding

	if n := v.gt.TerminalCount(); n != 1 {
		refs := make([]string, 0, 3)
		for _, key := range v.gt.Terminals {
			refs = append(refs, RefUnit(key))
		}
		refs = refsCapped(refs, 3)
		if len(refs) == 0 {
			refs = []string{RefCard}
		}
		detail := fmt.Sprintf("The recomputed frontier leaves %d terminal unit(s) on the table: %s. "+
			"Release requires exactly one — release will refuse this card.",
			n, quotedList(v.gt.Terminals))
		if n == 0 {
			detail = "The recomputed frontier leaves no terminal unit at all: every producing step's " +
				"output is consumed by a later one, so nothing is the finished garment. Release requires " +
				"exactly one terminal — release will refuse this card."
		}
		out = append(out, Finding{
			Category:   CategoryAssembly,
			Severity:   SeverityWarning,
			Title:      fmt.Sprintf("Assembly does not converge into one garment (%d terminals)", n),
			Detail:     detail,
			Refs:       refs,
			Suggestion: "Join the loose units into one, so the route ends in a single finished unit.",
		})
	}

	applicable, missing := len(v.card.Pieces), []CoverageMiss(nil)
	for _, key := range v.gt.UnconsumedPieces {
		name := v.pieceName(key)
		missing = append(missing, CoverageMiss{
			Refs: []string{RefPiece(name)},
			Finding: Finding{
				Category: CategoryAssembly,
				Severity: SeverityWarning,
				Title:    aiBoundedText(fmt.Sprintf("Cut piece %q is never sewn into anything", name), 90),
				Detail: fmt.Sprintf("%q is declared on the card and no operation takes it as an input. "+
					"It is cut, it is paid for, and it goes nowhere — release requires every declared piece "+
					"to reach the finished garment, so release will refuse this card.", name),
				Refs:       []string{RefPiece(name)},
				Suggestion: "Give the piece the step that sews it in, or drop it from the card.",
			},
		})
	}
	out = append(out, Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryAssembly,
			Severity: SeverityWarning,
			Title: fmt.Sprintf("%d of %d cut pieces are never sewn into anything",
				missing, applicable),
			Detail: "These pieces are declared and no operation takes them as an input — they are cut, " +
				"paid for and go nowhere. Release requires every declared piece to reach the finished " +
				"garment, so release will refuse this card.",
			Refs:       sample,
			Suggestion: "Give each piece the step that sews it in, or drop it from the card.",
		}
	})...)

	return out
}

// joinAnd renders a list the way prose does: «a, b and c».
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// ── C7–C9. ТРИ КЛАУЗЫ, КОТОРЫЕ §3.0 ОБЕЩАЕТ, А ПРОИЗВОДИТЕЛЯ ИМ НИКТО НЕ НАПИСАЛ ─────────────────
//
// Образец схлопнутой черновой находки §3.0 читается так: «Not yet ready for release: SMV 0/48 ·
// works 5/48 · no equipment profiles · no finishing block · no labels · hem finish not specified».
// Профили даёт C4, лейблы — C3, hem finish — C2. ПЕРВЫЕ ДВЕ КЛАУЗЫ И ЧЕТВЁРТУЮ не давал никто:
// после C1–C6 машинный слой на черновике карточки 8 не говорил про пустой SMV НИЧЕГО.
//
// ПОЧЕМУ ЭТО НЕ ДУБЛИРУЕТ ПРОМПТ. §7.2 шлёт покрытие SMV фактом в промпт, и для Ф1 этого
// достаточно: модель судит ПОСЛЕДСТВИЕ, а не считает строки. Но Ф0 уезжает на бету ОДНА, работает
// всегда и без ключа OpenRouter, а «ни у одной из 48 операций нет нормы времени» — это карточка,
// которую нельзя ни просчитать, ни поставить в план (золотой эталон, «чего не хватает», п. 9).
// B7 про то же заговаривает ТОЛЬКО при заданном cmt_cost, которого на карточке 8 нет, — то есть
// ровно там, где вопрос уже поздно задавать.
//
// ФОРМА — закон покрытия §3.0, как у любой проверки покрытия: одна находка с дробью и ≤3
// якорями-образцами, никогда 48 находок. Класс readiness — значит, на черновике всё это
// схлопывается в одну строку, а разворачивается только там, где карточку собираются выпускать.

// checkC7SmvCoverage — readiness, warning. Нормы времени нет ни у одного шага → планировать нечем.
//
// Читает tech_card_operation.smv. Подавители: SMV задан у всех шагов; маршрута нет вовсе (тогда
// говорит C1, а «0 of 0» было бы шумом — Aggregate на пустом множестве молчит сам).
func checkC7SmvCoverage(v *cardView) []Finding {
	applicable, missing := 0, []CoverageMiss(nil)
	for _, op := range v.ops {
		applicable++
		if !ndEmpty(op.SMV) {
			continue
		}
		missing = append(missing, CoverageMiss{
			Refs: opRefs(op),
			Finding: Finding{
				Category: CategoryReadiness,
				Severity: SeverityWarning,
				Title:    aiBoundedText(opLabel(op)+" has no SMV", 90),
				Detail: opLabel(op) + " carries no standard minute value, so its share of the labour " +
					"cost is zero and it takes no time in any plan.",
				Refs:       opRefs(op),
				Suggestion: "Measure or estimate the standard time of the step.",
				Clause:     "no SMV on " + strings.ToLower(opLabel(op)),
			},
		})
	}
	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryReadiness,
			Severity: SeverityWarning,
			Title: fmt.Sprintf("No standard time on %d of %d operations",
				missing, applicable),
			Detail: fmt.Sprintf("tech_card_operation.smv is empty on %d of the %d steps of the route. "+
				"A card without standard times cannot be costed for labour, cannot be scheduled and "+
				"cannot be balanced across a line — every planning number it feeds is a zero that "+
				"looks like a measurement.", missing, applicable),
			Refs:       sample,
			Suggestion: "Measure the route, or carry the times over from the closest measured style.",
			Clause:     smvClause(applicable-missing, applicable),
		}
	})
}

// smvClause renders the clause of the collapsed draft finding the way §3.0 writes it: «SMV 0/48» —
// СКОЛЬКО ЕСТЬ из скольких, а не сколько пропущено. Дробь в клаузе и дробь в заголовке считают
// разное намеренно: заголовок называет пропуск («no standard time on 48 of 48»), клауза — покрытие.
//
// Форма покрытия живёт ТОЛЬКО на агрегированной ветке. Пер-операционные находки (≤3 пропусков)
// именуют шаг: три находки, каждая со своей копией «SMV 45/48», схлопнулись бы в перечисление
// одной и той же дроби трижды.
func smvClause(filled, total int) string { return fmt.Sprintf("SMV %d/%d", filled, total) }

// checkC8WorkCoverage — readiness, warning. Шаг без назначенной работы.
//
// Читает tech_card_operation.work. Работа — это то, ЧТО делает шаг, в словарных терминах: без неё
// ни нормировщик не найдёт замер той же работы на других стилях (LatestOperationWorkSmv), ни
// каталог 0329 не скажет, на какой машине она законна (A8 молчит структурно), ни свод по цеху не
// сложится. Золотой эталон, ошибка 8: «work не назначен у 43 из 48 операций».
//
// Подавители: работа задана у всех шагов; маршрута нет вовсе.
func checkC8WorkCoverage(v *cardView) []Finding {
	applicable, missing := 0, []CoverageMiss(nil)
	for _, op := range v.ops {
		applicable++
		if !nsEmpty(op.Work) {
			continue
		}
		missing = append(missing, CoverageMiss{
			Refs: opRefs(op),
			Finding: Finding{
				Category: CategoryReadiness,
				Severity: SeverityWarning,
				Title:    aiBoundedText(opLabel(op)+" names no work", 90),
				Detail: opLabel(op) + " says which machine it runs on, but not what work is done on " +
					"it — so nothing links the step to a measured time or to the shop-floor dictionary.",
				Refs:       opRefs(op),
				Suggestion: "Pick the work of the step from the catalog.",
				Clause:     "no work on " + strings.ToLower(opLabel(op)),
			},
		})
	}
	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryReadiness,
			Severity: SeverityWarning,
			Title:    fmt.Sprintf("No work assigned on %d of %d operations", missing, applicable),
			Detail: fmt.Sprintf("tech_card_operation.work is empty on %d of the %d steps. The work is "+
				"what the step DOES in dictionary terms: without it the step cannot borrow a measured "+
				"time from the same work on another style, the work-machine legality check has nothing "+
				"to check, and no summary by work adds up.", missing, applicable),
			Refs:       sample,
			Suggestion: "Assign the work of each step from the catalog.",
			Clause:     worksClause(applicable-missing, applicable),
		}
	})
}

func worksClause(filled, total int) string { return fmt.Sprintf("works %d/%d", filled, total) }

// finishingVerbs is the closing block of any garment route: cut the thread ends, clean the garment,
// press it for the last time, inspect it, fold it, pack it.
//
// СПИСОК КУРИРУЕТСЯ, А НЕ ВЫВОДИТСЯ. Это ровно те глаголы словаря, которые делают изделие товаром
// после того, как оно сшито; press НАМЕРЕННО не входит — утюг работает по всему маршруту, и его
// присутствие ничего не говорит об окончательной ВТО.
var finishingVerbs = []entity.TechCardOperationType{
	entity.OpTypeThreadTrim, entity.OpTypeClean, entity.OpTypeInspect,
	entity.OpTypeFold, entity.OpTypePack,
}

// checkC9FinishingBlock — readiness, warning. Маршрут кончается там, где сшили, и не кончается там,
// где изделие стало товаром.
//
// НЕ ПОКРЫТИЕ, А ФАКТ: считать «0 из 48 шагов финишные» бессмысленно — финишных шагов не бывает
// сорок восемь. Поэтому одна находка с якорем card, как у C2/C4/C5.
//
// Подавители: в маршруте есть хоть один финишный глагол; маршрута нет вовсе (говорит C1).
func checkC9FinishingBlock(v *cardView) []Finding {
	if len(v.ops) == 0 {
		return nil
	}
	for _, op := range v.ops {
		if hasVerb(op, finishingVerbs) {
			return nil
		}
	}
	names := make([]string, 0, len(finishingVerbs))
	for _, verb := range finishingVerbs {
		names = append(names, string(verb))
	}
	return []Finding{{
		Category: CategoryReadiness,
		Severity: SeverityWarning,
		Title:    "The route ends with the last seam and has no finishing block",
		Detail: fmt.Sprintf("Not one of the %d steps carries a finishing verb (%s). As written the "+
			"garment leaves the line with its thread ends on, unpressed, uninspected and unpacked — "+
			"work that is done in every factory and costed in none of this card.",
			len(v.ops), strings.Join(names, ", ")),
		Refs:       []string{RefCard},
		Suggestion: "Add the closing block: thread trimming, cleaning, final pressing, inspection, folding and packing.",
		Clause:     "no finishing block",
	}}
}
