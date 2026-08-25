package techcardanalysis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ── БЛОК «BOM И ДЕНЬГИ»: B1–B8 (design §3.2) ────────────────────────────────────────────────────
//
// О ЧЁМ ЭТОТ БЛОК. Не о том, «правильно ли посчитана себестоимость» — этого пакет не знает и знать
// не может: цены каталога, курсы контура и нормы колорвеев живут в БД, а сюда приезжает ровно
// сохранённая карточка плюс курсы аргументом. Блок ловит РАЗРЫВЫ МЕЖДУ ПОЛОВИНАМИ карточки:
// маршрут ставит фурнитуру, которой нет в BOM; сорок четыре машинных шага при нуле ниточных линий;
// строка, которая молча выпадет из базового итога, потому что курса её валюты нет. Каждый такой
// разрыв — место, где две части карточки утверждают разное, и ни одна из них не врёт по
// отдельности.
//
// ЧТО ИЗ ЭТОГО БЛОКА — ДЕНЬГИ (Finding.Money; решено поимённо, ПО ВСЕМ ВОСЬМИ, 2026-08-24).
// Граница — та же, по которой режет stripTechCardCosting: величина, валюта и ОТНОШЕНИЕ величин —
// деньги; имя недостающего факта — нет.
//
//	B1  фурнитура в маршруте против BOM — нет: ни цены, ни валюты, ни суммы.
//	B2  швейные шаги против ниточных линий — нет: «costed at zero» это ОТСУТСТВИЕ линии, не сумма.
//	B3  треугольник дублирования — нет: три счётчика деталей и линий.
//	B4  счёт застёжек против купленного — нет: штуки. Количество — структура, костинг его и так
//	    показывает (strip снимает line_total, но не consumption).
//	B5а плейсхолдерная цена — ДА: печатает цену, валюту и процент от самой дорогой линии карточки.
//	B5б инверсия по назначению — ДА: печатает ОБЕ цены с валютами, а агрегатная форма — ещё и
//	    отношение прозой. Показать её «без цифр» нельзя: фраза «карманка дороже основной» и ЕСТЬ
//	    утечка соотношения, ради которой цену прячут.
//	B5в валюта без курса — ДА: называет ВАЛЮТУ линий поимённо, а `b.Currency` strip обнуляет.
//	B6  CMT не задан — нет, и это НЕ послабление: ровно эту фразу уже везёт оговорка сметы
//	    (`estimateCaveats`, dto/style_cost_estimate.go), которую stripStyleCostEstimate осознанно
//	    НЕ снимает. Величины в находке нет — есть имя незаполненного поля.
//	B7  CMT без опоры по SMV — нет по тому же правилу (величины нет, есть покрытие нормой, которое
//	    C7 печатает открыто). И отдельно: B6 и B7 — ДВЕ ВЕТКИ ОДНОГО предиката `CmtCost.Valid`.
//	    Пометить одну и не пометить другую нельзя: молчание обеих само сообщало бы тот самый бит
//	    «cmt задан», который пометка якобы прячет. Либо обе, либо ни одной — выбрано «ни одной»,
//	    потому что тот же бит уже опубликован оговоркой сметы.
//	B8  wastage — нет: `wastage_percent` strip НЕ снимает, аккаунт видит его на самой строке BOM.
//	    «Гроссит стоимость на треть» — отношение стоимости к самой себе, ни одной суммы.
//
// ИНВАРИАНТ, НА КОТОРОМ СТОИТ РЕДАКТИРОВАНИЕ В ОБРАБОТЧИКЕ: ни одна денежная находка не имеет
// категории readiness. Иначе на черновике она уехала бы ВНУТРЬ схлопнутой находки (§3.0), у
// которой Money=false, и подавление по флагу пронесло бы деньги мимо себя. Закреплено тестом
// TestNoMoneyFindingIsReadiness — трогать классификацию, не прочитав его, нельзя.
//
// РЕГИСТРАЦИЯ — В ЭТОМ ФАЙЛЕ (analysis.go после T2 не трогает никто; у маршрута своя строка в
// route.go, у готовности — своя в readiness.go).
var _ = register(
	needsCard(checkB1HardwareChain),
	needsCard(checkB2ThreadLines),
	needsCard(checkB3FusingTriangle),
	needsCard(checkB4FastenerCounts),
	needsCard(checkB5aPlaceholderPrice),
	needsCard(checkB5bPurposeInversion),
	needsCard(checkB5cCurrencyWithoutRate),
	needsCard(checkB6LabourNotSet),
	needsCard(checkB7CmtWithoutBacking),
	needsCard(checkB8Wastage),
)

// needsCard wraps a check that reads the card's own slices (BOM, pieces, colourways, costing) so it
// is never handed a nil card.
//
// ОДНОЙ ОБЁРТКОЙ, А НЕ СТРАЖЕМ В КАЖДОЙ ПРОВЕРКЕ. RunAudit(nil, fx) — законный вызов, и он обязан
// вернуть ноль находок, а не упасть; шестнадцать копий `if v.card == nil` — это шестнадцать мест,
// где семнадцатая проверка однажды забудет страж. Проверки маршрута (T3) читают только v.ops и
// карты вида, у которых nil-карточка даёт пустое значение, поэтому обёртка нужна именно здесь.
func needsCard(fn checkFn) checkFn {
	return func(v *cardView) []Finding {
		if v == nil || v.card == nil {
			return nil
		}
		return fn(v)
	}
}

// ── B1. ФУРНИТУРНАЯ ЦЕПОЧКА ─────────────────────────────────────────────────────────────────────
//
// bom_mismatch, error в прямую сторону и warning в обратную. Читает глагол шага
// (operation_type='hardware_set') и машину (machine_type ∈ {buttonhole, button_attach,
// zipper_setting}) против секции строки BOM (section='hardware', словарь entity:794–802).
//
// ТОКЕНА `hardware_attach` В ПРЕДИКАТЕ НЕТ — он снят 0328: он кодировал СПОСОБ крепления в поле
// «на чём», и шаг с ним больше не записывается. Писать его сюда значило бы ловить состояние,
// которого запись не производит.
//
// ЛИНК ШАГ↔ЛИНИЯ НЕ ТРЕБУЕТСЯ. Пер-линейные связи операций с BOM на проде редки (карточка 8 несёт
// их ровно две, обе на прокладку), и требование линка превратило бы проверку в проверку
// заполненности линков. Достаточно ПРИСУТСТВИЯ: любая hardware-линия удовлетворяет, в том числе с
// kind NULL — NULL здесь «не классифицировано» (0278), и угадывать за него нечего.
//
// Подавители: есть и линии, и ставящие шаги; нет ни тех, ни других.
func checkB1HardwareChain(v *cardView) []Finding {
	var setters []*entity.TechCardOperation
	for _, op := range v.ops {
		if isHardwareSettingOp(op) {
			setters = append(setters, op)
		}
	}
	lines := v.bomLinesOfSection(entity.BomSectionHardware)

	switch {
	case len(setters) > 0 && len(lines) == 0:
		return []Finding{{
			Category: CategoryBomMismatch,
			Severity: SeverityError,
			Title:    "The route sets hardware; the BOM has none",
			Detail: fmt.Sprintf("%s %s hardware (%s), and the BOM carries zero lines in "+
				"section 'hardware' — nothing is bought to be set on the garment. The garment as "+
				"written is assembled with parts that are not in its materials list.",
				capitalise(opList(opNumbersOf(setters))), verbSet(len(setters)),
				settersDescription(setters)),
			Refs:       refsCapped(opRefsOf(setters), 3),
			Suggestion: "Add the hardware lines to the BOM, or remove the steps that set them.",
		}}
	case len(setters) == 0 && len(lines) > 0:
		return []Finding{{
			Category: CategoryBomMismatch,
			Severity: SeverityWarning,
			Title:    "The BOM buys hardware; no step sets it",
			Detail: fmt.Sprintf("The BOM carries %d line(s) in section 'hardware' (%s), and no "+
				"operation of the route is a hardware_set step or runs on a buttonhole, "+
				"button_attach or zipper_setting machine — nobody puts these parts on.",
				len(lines), quotedList(bomNamesOf(lines))),
			Refs:       refsCapped(bomRefsOf(lines), 3),
			Suggestion: "Add the steps that set this hardware, or drop the lines from the BOM.",
		}}
	}
	return nil
}

// isHardwareSettingOp reports whether the step PUTS HARDWARE ON the garment — by its verb or by the
// machine it runs on. Both axes, because since 0328 a step names them both and either half alone
// leaves a real setter unseen.
func isHardwareSettingOp(op *entity.TechCardOperation) bool {
	if op.OperationType == entity.OpTypeHardwareSet {
		return true
	}
	return hardwareMachines[machineToken(op)]
}

// hardwareMachines is the «на чём» half of B1's predicate (entity.MachineTypeTokens).
var hardwareMachines = map[string]bool{
	"buttonhole": true, "button_attach": true, "zipper_setting": true,
}

// settersDescription names WHY each step counts as a setter, so the finding does not make the
// technologist guess which half of the predicate caught it.
func settersDescription(ops []*entity.TechCardOperation) string {
	seen := map[string]bool{}
	var parts []string
	for _, op := range ops {
		token := ""
		if m := machineToken(op); hardwareMachines[m] {
			token = "machine_type " + m
		} else {
			token = "operation_type hardware_set"
		}
		if seen[token] {
			continue
		}
		seen[token] = true
		parts = append(parts, token)
	}
	return strings.Join(parts, ", ")
}

// ── B2. НИТКИ ───────────────────────────────────────────────────────────────────────────────────
//
// bom_mismatch, warning. Счёт шагов operation_type='machine' против ниточных линий BOM.
//
// ЗАЧЕМ ЭТО ВООБЩЕ ПРОВЕРЯТЬ. Золотое человеческое ревью карточки 8 ниток не заметило — сорок
// четыре машинных шага и ни одной ниточной строки прочитались как норма, потому что нитку никто не
// «выбирает», её просто берут со стеллажа. Машина замечает такое всегда и стоит один проход.
//
// ДВА ИСТОЧНИКА НИТОЧНОСТИ, потому что их два в данных: секция 'thread' и вид из ниточного
// семейства (0278). Семейство ВЫВОДИТСЯ из словаря entity (BomKindHomeSection == 'thread'), а не
// перечисляется здесь: шестой вид ниток, добавленный в entity, обязан подавить эту находку сам, без
// правки списка в этом файле.
func checkB2ThreadLines(v *cardView) []Finding {
	sewing := 0
	for _, op := range v.ops {
		if op.OperationType == entity.OpTypeMachine {
			sewing++
		}
	}
	if sewing == 0 {
		return nil // подавитель: шить нечем и нечего
	}
	for i := range v.card.BomItems {
		if isThreadLine(&v.card.BomItems[i]) {
			return nil // подавитель: хоть одна ниточная линия есть
		}
	}

	return []Finding{{
		Category: CategoryBomMismatch,
		Severity: SeverityWarning,
		Title:    fmt.Sprintf("%d sewing operations, zero thread lines", sewing),
		Detail: fmt.Sprintf("The route carries %d operation(s) of type 'machine', and the BOM has no "+
			"line in section 'thread' and no line whose kind belongs to the thread family. "+
			"Thread is bought by nobody and costed at zero.", sewing),
		Refs:       []string{RefCard},
		Suggestion: "Add the thread lines the machines actually run.",
	}}
}

// isThreadLine reports whether a BOM line is thread — by section or by kind. The kind half is
// derived from entity's kind↔section table, never restated here.
func isThreadLine(b *entity.TechCardBomItem) bool {
	if b.Section == entity.BomSectionThread {
		return true
	}
	kind := entity.TechCardBomKind(strings.TrimSpace(b.Kind.String))
	if kind == "" {
		return false // NULL = «не классифицировано» (0278), а не «может быть ниткой»
	}
	home, ok := entity.BomKindHomeSection(kind)
	return ok && home == entity.BomSectionThread
}

// ── B3. FUSING-ТРЕУГОЛЬНИК ──────────────────────────────────────────────────────────────────────
//
// bom_mismatch, warning. Три счётчика: линии section='interlining', детали с fused=1, шаги
// operation_type='fusing'.
//
// НАХОДКА ЦИТИРУЕТ ВСЕ ТРИ ЧИСЛА И НЕ УТВЕРЖДАЕТ НАМЕРЕНИЯ. `tech_card_piece.fused` — NOT NULL
// DEFAULT FALSE, то есть «0 деталей с клеевой» на карточке значит либо «клеевой нет», либо «никто
// не проставил флаг», и различить их в колонке нечем. Вывод «пиджаку клеевая полагается» — работа
// модели, которая поднимет это до блокера; работа машины — положить рядом три числа, которые не
// сходятся.
//
// Подавители: все три нуля (изделию клеевая может не полагаться); все три ненулевые (треугольник
// замкнут — какая деталь какой прокладкой дублируется, судит уже не счёт).
func checkB3FusingTriangle(v *cardView) []Finding {
	interlining := v.bomLinesOfSection(entity.BomSectionInterlining)

	var fused []*entity.TechCardPiece
	for i := range v.card.Pieces {
		if v.card.Pieces[i].Fused {
			fused = append(fused, &v.card.Pieces[i])
		}
	}

	var fusingOps []*entity.TechCardOperation
	for _, op := range v.ops {
		if op.OperationType == entity.OpTypeFusing {
			fusingOps = append(fusingOps, op)
		}
	}

	nl, nf, no := len(interlining), len(fused), len(fusingOps)
	if nl == 0 && nf == 0 && no == 0 {
		return nil // подавитель: клеевой на карточке нет вовсе
	}
	if nl > 0 && nf > 0 && no > 0 {
		return nil // подавитель: треугольник замкнут
	}

	refs := make([]string, 0, 3)
	refs = append(refs, bomRefsOf(interlining)...)
	for _, p := range fused {
		refs = append(refs, RefPiece(p.Name))
	}
	refs = append(refs, opRefsOf(fusingOps)...)
	refs = refsCapped(refs, 3)
	if len(refs) == 0 {
		refs = []string{RefCard}
	}

	return []Finding{{
		Category: CategoryBomMismatch,
		Severity: SeverityWarning,
		Title:    fmt.Sprintf("Fusing is stated in %d of the 3 places that describe it", nonZeroOf(nl, nf, no)),
		Detail: fmt.Sprintf("Interlining BOM lines: %d. Cut pieces marked fused: %d of %d. Fusing "+
			"operations in the route: %d. Fusing is described by all three together, and here they "+
			"disagree. (tech_card_piece.fused is NOT NULL DEFAULT FALSE, so a zero there is 'nobody "+
			"ticked it' as readily as 'no fusing' — this finding states the counts, not the intent.)",
			nl, nf, len(v.card.Pieces), no),
		Refs: refs,
		Suggestion: "Reconcile the three: buy the interlining, mark the pieces it backs, and give the " +
			"route the step that presses it on.",
	}}
}

func nonZeroOf(counts ...int) int {
	n := 0
	for _, c := range counts {
		if c > 0 {
			n++
		}
	}
	return n
}

// ── B4. СЧЁТНАЯ СВЕРКА ЗАСТЁЖЕК ─────────────────────────────────────────────────────────────────
//
// bom_mismatch. Две независимые ветки: несовпадение счётов петель и пуговиц — warning; счёт
// установки больше закупленной счётной нормы — error.
//
// СЧЁТНУЮ НОРМУ СОБИРАЕТ ПАРА (КОЛОРВЕЙ × СЛОТ), И СПРАШИВАТЬ ЕЁ НАДО ТАМ, А НЕ У СТРОКИ РЕЦЕПТА.
// Число живёт на слоте (`tech_card_bom_item.qty_per_garment`, 0333) и может быть переопределено
// строками рецепта (`tech_card_colorway_usage.quantity`); 0295 дословно разрешает НЕСКОЛЬКИМ
// строкам одного колорвея поминать один слот с разными placement («планка» / «манжета»). Правило
// целиком — в entity.CountablePairQty, и B4 его не переписывает: своё чтение по строкам врало бы
// дважды — молчало бы на карточке, где число стоит на слоте, и на паре «4 + 2» брало бы 2 вместо
// 6, печатая ошибку про недостачу там, где карточка сходится.
//
// СВЕРКА ИДЁТ С ПРИШИВАЕМЫМ, А НЕ С ЗАКУПАЕМЫМ (CountablePairQty, не CountablePairTotal): запас
// слота уезжает в пакетик покупателю, и ставить из него — значит отдать пакетик пустым. Слот,
// покупающий пять на изделие и три в запас, при шести установках недостачу ИМЕЕТ.
//
// Проверка сверяется с САМЫМ СКУПЫМ колорвеем: карточка, у которой один колорвей покупает восемь
// пуговиц, а другой шесть, при шести установках законна, а при семи — нет, и назвать надо тот
// колорвей, который не сходится.
//
// Подавители: `placement_count` NULL (это уже находка A2 — «как написано, изделие однопуговичное»,
// и вторая находка о том же числе была бы вторым голосом об одном факте); линка шага на строку BOM
// нет; счётной нормы у линии нет ни в одном колорвее (ни на слоте, ни на строках).
//
// ЧЕГО ЗДЕСЬ НЕТ: диаметра пуговицы (`material_hardware_attr.diameter_mm`, 0157). Он живёт в
// каталоге номенклатуры, а пакет в БД не ходит — сверка «петля 20 мм под пуговицу 15 мм» возможна
// только там, где каталог читается, и выдумывать её здесь по снапшоту строки нечем.
func checkB4FastenerCounts(v *cardView) []Finding {
	var out []Finding

	// ── 1. Петель столько же, сколько пуговиц ───────────────────────────────────────────────────
	holes, holesOps, holesAllSet := placementSum(v.ops, "buttonhole")
	buttons, buttonsOps, buttonsAllSet := placementSum(v.ops, "button_attach")
	if len(holesOps) > 0 && len(buttonsOps) > 0 && holesAllSet && buttonsAllSet && holes != buttons {
		refs := refsCapped(append(opRefsOf(holesOps), opRefsOf(buttonsOps)...), 3)
		out = append(out, Finding{
			Category: CategoryBomMismatch,
			Severity: SeverityWarning,
			Title:    fmt.Sprintf("%d buttonholes against %d buttons", holes, buttons),
			Detail: fmt.Sprintf("placement_count sums to %d over the buttonhole steps (%s) and "+
				"to %d over the button steps (%s). One of the two numbers is describing a garment "+
				"nobody is making.", holes, opList(opNumbersOf(holesOps)), buttons,
				opList(opNumbersOf(buttonsOps))),
			Refs:       refs,
			Suggestion: "Make the two counts agree, or say in a note why they legitimately differ.",
		})
	}

	// ── 2. Ставим больше, чем закуплено ─────────────────────────────────────────────────────────
	applicable, missing := 0, []CoverageMiss(nil)
	for _, op := range v.ops {
		if !isHardwareSettingOp(op) || niEmpty(op.PlacementCount) {
			continue
		}
		count := op.PlacementCount.Int32
		for _, line := range v.linkedBomLines(op) {
			qty, colorway, basis, ok := v.tightestCountNorm(line)
			if !ok {
				continue // подавитель: счётной нормы у линии нет ни в одном колорвее
			}
			applicable++
			if !decimal.NewFromInt(int64(count)).GreaterThan(qty) {
				continue // запаска легальна: закупить больше, чем ставим, — нормальная практика
			}
			refs := append(opRefs(op), RefBom(line.Name))
			missing = append(missing, CoverageMiss{
				Refs: refs,
				Finding: Finding{
					Category: CategoryBomMismatch,
					Severity: SeverityError,
					Title:    aiBoundedText(fmt.Sprintf("More %s set than bought: %d against %s", line.Name, count, qty.String()), 90),
					Detail: fmt.Sprintf("%s sets %d × %q (placement_count), and %s. The "+
						"line runs short on every garment.",
						opLabel(op), count, line.Name, countNormPhrase(qty, colorway, basis)),
					Refs:       refs,
					Suggestion: "Raise the quantity on the colourway recipe, or lower the placement count on the step.",
				},
			})
		}
	}
	out = append(out, Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryBomMismatch,
			Severity: SeverityError,
			Title: fmt.Sprintf("More hardware is set than bought on %d of %d step-to-line links",
				missing, applicable),
			Detail: "placement_count exceeds the countable norm the card sews per garment " +
				"(tech_card_bom_item.qty_per_garment, or tech_card_colorway_usage.quantity where the " +
				"recipe overrides it) on these links — the lines run short on every garment.",
			Refs:       sample,
			Suggestion: "Raise the quantities on the colourway recipe, or lower the placement counts.",
		}
	})...)

	return out
}

// placementSum totals placement_count over the steps of one hardware machine and says whether EVERY
// such step states its count. Одна незаполненная строка делает сумму утверждением о карточке,
// которого никто не делал, — поэтому сравнение сумм требует полноты обеих.
func placementSum(ops []*entity.TechCardOperation, machine string) (sum int32, matched []*entity.TechCardOperation, allSet bool) {
	allSet = true
	for _, op := range ops {
		if machineToken(op) != machine {
			continue
		}
		matched = append(matched, op)
		if niEmpty(op.PlacementCount) {
			allSet = false
			continue
		}
		sum += op.PlacementCount.Int32
	}
	return sum, matched, allSet
}

// ── B5а. ЦЕНА-ЗАГЛУШКА ──────────────────────────────────────────────────────────────────────────
//
// question, warning. Рулонная линия (entity.IsRollGoodsSection — список не хардкодится) с ценой 0
// или ≤ 10% самой дорогой fabric-линии карточки.
//
// ВОПРОС, А НЕ ОШИБКА. Нулевая цена как таковая отвергнута (§16): подкладка за 1.0000 может быть
// настоящей ценой давнего рулона, и объявлять её дефектом значило бы спорить с фактом. Находка
// спрашивает — «это цена или заглушка?» — и на этом останавливается.
//
// Подавитель: `price_source='catalog'` (0244). Цена, приехавшая из каталога, — какая есть; она не
// «набрана наспех», и спрашивать про неё нечего.
//
// О ВАЛЮТАХ. §3.2(б) требует конверсии перед сравнением и молчания без курса — у (а) такого
// требования нет, и это не упущение: (а) — тест ПОРЯДКА ВЕЛИЧИНЫ на вопросительной находке, и
// подкладка за 1.0000 при основной ткани за 60 подозрительна в любой паре валют мира. Поэтому без
// курса сравнение идёт по номиналу, а текст ГОВОРИТ, что валюты разные и курса нет, — читатель
// видит, на чём основан вопрос.
func checkB5aPlaceholderPrice(v *cardView) []Finding {
	ref, hasRef := v.dearestFabricLine()

	applicable, missing := 0, []CoverageMiss(nil)
	for i := range v.card.BomItems {
		b := &v.card.BomItems[i]
		if !entity.IsRollGoodsSection(b.Section) || !b.UnitPrice.Valid {
			continue
		}
		if strings.TrimSpace(b.PriceSource.String) == entity.BomPriceSourceCatalog {
			continue // подавитель: цена из каталога
		}
		applicable++

		var reason string
		switch {
		case b.UnitPrice.Decimal.IsZero():
			reason = fmt.Sprintf("%q is priced at zero", b.Name)
		case hasRef && b != ref:
			cmp, exact, ok := v.comparePrices(b, ref)
			if !ok || cmp.GreaterThan(decimal.RequireFromString("0.1")) {
				continue
			}
			reason = fmt.Sprintf("%q costs %s %s — %s%% of the dearest fabric line of the card, %q at %s %s",
				b.Name, b.UnitPrice.Decimal.String(), currencyOf(b),
				cmp.Mul(decimal.NewFromInt(100)).Round(1).String(),
				ref.Name, ref.UnitPrice.Decimal.String(), currencyOf(ref))
			if !exact {
				reason += " (compared at face value: the two currencies differ and this run has no rate between them)"
			}
		default:
			continue
		}

		missing = append(missing, CoverageMiss{
			Refs: []string{RefBom(b.Name)},
			Finding: Finding{
				Category: CategoryQuestion,
				Severity: SeverityWarning,
				Money:    true, // цитирует закупочную цену / валюту линии — см. Finding.Money
				Title:    aiBoundedText(fmt.Sprintf("Is %q priced or is that a placeholder?", b.Name), 90),
				Detail: reason + ". A roll-goods line at that price either is a genuine bargain or is a " +
					"number somebody typed to get past the form — and the plan cost of every garment " +
					"rests on which one it is.",
				Refs:       []string{RefBom(b.Name)},
				Suggestion: "Confirm the price, or reprice the line from the catalog.",
			},
		})
	}

	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryQuestion,
			Severity: SeverityWarning,
			Money:    true, // цитирует закупочную цену / валюту линии — см. Finding.Money
			Title: fmt.Sprintf("%d of %d roll-goods lines look priced with a placeholder",
				missing, applicable),
			Detail: "These lines are priced at zero or at a tenth of the card's dearest fabric line — " +
				"the plan cost of every garment rests on whether those numbers are real.",
			Refs:       sample,
			Suggestion: "Confirm the prices, or reprice the lines from the catalog.",
		}
	})
}

// ── B5б. ИНВЕРСИЯ ПО НАЗНАЧЕНИЮ ─────────────────────────────────────────────────────────────────
//
// question, warning. Линия назначения `pocketing`/`lining` дороже за метр, чем ЛЮБАЯ линия
// назначения `main`.
//
// «ДОРОЖЕ ЛЮБОЙ ОСНОВНОЙ», А НЕ «ДОРОЖЕ КАКОЙ-ТО». У карточки может быть две основные ткани, и
// карманка, которая дороже дешёвой из них и дешевле дорогой, — обычное дело. Находка выпускается
// только когда карманка дороже САМОЙ ДОРОГОЙ основной: тогда вопрос «а точно?» действительно есть.
//
// Подавители: purpose NULL (0265 — «ещё не разложили», угадывать нечего); unit ≠ 'm' после
// TRIM/LOWER (сравнивать цену за метр с ценой за штуку бессмысленно); основных линий нет;
// БЕЗ КУРСА — МОЛЧАНИЕ (в отличие от B5а: здесь находка УТВЕРЖДАЕТ «дороже», а утверждение о
// деньгах через неизвестный курс — это выдумка, а не вопрос).
func checkB5bPurposeInversion(v *cardView) []Finding {
	var mains []*entity.TechCardBomItem
	var cheaps []*entity.TechCardBomItem
	for i := range v.card.BomItems {
		b := &v.card.BomItems[i]
		if !b.Purpose.Valid || !b.UnitPrice.Valid {
			continue
		}
		if strings.ToLower(strings.TrimSpace(b.Unit.String)) != "m" {
			continue
		}
		switch entity.TechCardBomPurpose(strings.TrimSpace(b.Purpose.String)) {
		case entity.BomPurposeMain:
			mains = append(mains, b)
		case entity.BomPurposePocketing, entity.BomPurposeLining:
			cheaps = append(cheaps, b)
		}
	}
	if len(mains) == 0 || len(cheaps) == 0 {
		return nil
	}

	applicable, missing := 0, []CoverageMiss(nil)
	for _, b := range cheaps {
		dearer := 0
		comparable := 0
		var dearest *entity.TechCardBomItem
		for _, m := range mains {
			ratio, exact, ok := v.comparePrices(b, m)
			if !ok || !exact {
				// БЕЗ КУРСА — МОЛЧАНИЕ, и именно поэтому здесь читается `exact`, а не только `ok`:
				// comparePrices умеет отвечать по номиналу, и этот ответ законен у вопроса B5а, но
				// не у утверждения «дороже». 60 USD против 55 PLN — не «дороже», а «неизвестно».
				continue
			}
			comparable++
			if ratio.GreaterThan(decimal.NewFromInt(1)) {
				dearer++
				dearest = m
			}
		}
		if comparable == 0 {
			continue
		}
		applicable++
		if dearer < comparable || dearest == nil {
			continue // дешевле хотя бы одной основной — вопроса нет
		}

		refs := []string{RefBom(b.Name), RefBom(dearest.Name)}
		missing = append(missing, CoverageMiss{
			Refs: refs,
			Finding: Finding{
				Category: CategoryQuestion,
				Severity: SeverityWarning,
				Money:    true, // цитирует закупочную цену / валюту линии — см. Finding.Money
				Title:    aiBoundedText(fmt.Sprintf("%q costs more per metre than the main fabric", b.Name), 90),
				Detail: fmt.Sprintf("%q (purpose %q) is %s %s per metre; the main fabric %q is %s %s. "+
					"The lining and the pocketing of a garment are normally the cheap half of it — either "+
					"this is deliberate, or one of the two prices sits on the wrong line.",
					b.Name, strings.TrimSpace(b.Purpose.String), b.UnitPrice.Decimal.String(), currencyOf(b),
					dearest.Name, dearest.UnitPrice.Decimal.String(), currencyOf(dearest)),
				Refs:       refs,
				Suggestion: "Confirm the two prices, or swap the purpose of the lines.",
			},
		})
	}

	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryQuestion,
			Severity: SeverityWarning,
			Money:    true, // цитирует закупочную цену / валюту линии — см. Finding.Money
			Title: fmt.Sprintf("%d of %d lining/pocketing lines cost more per metre than every main fabric",
				missing, applicable),
			Detail: "The cheap half of the garment prices dearer than the main cloth on these lines.",
			Refs:   sample,
			Suggestion: "Confirm the prices, or check that the purpose sits on the right " +
				"lines.",
		}
	})
}

// ── B5в. ВАЛЮТА БЕЗ КУРСА ───────────────────────────────────────────────────────────────────────
//
// bom_mismatch, warning. Линия, чья валюта не равна базовой и курса к базе не имеет, МОЛЧА выпадает
// из базового итога: `ComputeStyleCostEstimate` складывает только сконвертированное и вешает одну
// строку caveat'а на всю смету, без якоря на строку. Эта находка и есть тот якорь.
//
// БАЗА — ИЗ `fx.Base`, А НЕ КОНСТАНТОЙ. Дизайн §3.2(в) в примере ошибся базой (он ждал PLN и
// «EUR-линию без курса»); фактическая база контура — EUR (cache.defaultCurrency, читается
// cache.GetBaseCurrency и приезжает сюда аргументом RunAudit). На карточке 8 поэтому без курса
// остаются ТРИ PLN-линии, а EUR-подкладка и есть база. Хардкод базы здесь начал бы «находить»
// строки без курса на контуре, где вся карточка и есть база.
//
// ЕДИНИЦА ПРОПУСКА — ВАЛЮТА, А НЕ ЛИНИЯ, и это меняет число находок. Три PLN-линии лечатся ОДНОЙ
// строкой курса; три находки об одном недостающем курсе — это трижды одна и та же работа в списке.
// Поэтому применимое множество закона агрегации §3.0 здесь — множество ВАЛЮТ карточки, а якорями
// одной находки идут ВСЕ линии этой валюты.
func checkB5cCurrencyWithoutRate(v *cardView) []Finding {
	type ccy struct {
		code  string
		lines []*entity.TechCardBomItem
	}
	order := []string(nil)
	byCode := map[string]*ccy{}
	for i := range v.card.BomItems {
		b := &v.card.BomItems[i]
		if !b.UnitPrice.Valid {
			continue // цены нет — выпадать из итога нечему (об этом говорит смета своим caveat'ом)
		}
		code := currencyOf(b)
		if code == "" {
			continue // валюты нет вовсе: это не «нет курса», а «нет валюты» — другой дефект, не наш
		}
		c, ok := byCode[code]
		if !ok {
			c = &ccy{code: code}
			byCode[code] = c
			order = append(order, code)
		}
		c.lines = append(c.lines, b)
	}

	applicable, missing := 0, []CoverageMiss(nil)
	for _, code := range order {
		applicable++
		if _, known := v.fx.Rate(code); known {
			continue
		}
		c := byCode[code]
		refs := bomRefsOf(c.lines)
		missing = append(missing, CoverageMiss{
			Refs: refs,
			Finding: Finding{
				Category: CategoryBomMismatch,
				Severity: SeverityWarning,
				Money:    true, // цитирует закупочную цену / валюту линии — см. Finding.Money
				Title: aiBoundedText(fmt.Sprintf("%s has no rate to %s: %s %s out of the cost total",
					code, v.fx.Base, countedLines(len(c.lines)),
					plural(len(c.lines), "drops", "drop")), 90),
				Detail: fmt.Sprintf("%s of the BOM %s priced in %s (%s), the base currency of this "+
					"installation is %s, and costing_fx_rate carries no rate between them. The "+
					"cost estimate silently leaves those lines out of the base total and says so in one "+
					"caveat sentence for the whole card — nothing points at the lines themselves.",
					capitalise(countedLines(len(c.lines))), plural(len(c.lines), "is", "are"),
					code, quotedList(bomNamesOf(c.lines)), v.fx.Base),
				Refs:       refs,
				Suggestion: fmt.Sprintf("Add a %s → %s rate, or price these lines in %s.", code, v.fx.Base, v.fx.Base),
			},
		})
	}

	return Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryBomMismatch,
			Severity: SeverityWarning,
			Money:    true, // цитирует закупочную цену / валюту линии — см. Finding.Money
			Title: fmt.Sprintf("%d of %d BOM currencies have no rate to %s",
				missing, applicable, v.fx.Base),
			Detail: "Lines in these currencies drop out of the base-currency total of the cost estimate, " +
				"which reports the gap as one caveat sentence with no anchor on any line.",
			Refs:       sample,
			Suggestion: "Add the missing rates to costing_fx_rate.",
		}
	})
}

// ── B6. ТРУД БЕЗ CAVEAT'А ───────────────────────────────────────────────────────────────────────
//
// readiness, warning, якорь `card`. Карточка несёт костинг, `cmt_cost` в нём NULL, а маршрут
// непуст — значит смета считает МАТЕРИАЛЫ и печатает уверенный итог, в котором труда нет вовсе.
//
// ЭТО ПОЛОВИНА РАБОТЫ, ВТОРАЯ — В `internal/dto/style_cost_estimate.go`. Та функция скипала статью
// CMT голым `continue` и не поднимала ни одного флага: карточка с полным маршрутом и без CMT
// выдавала итог без единого признака, что труд не посчитан. Флаг добавлен туда же, к соседним
// caveat'ам (отдельным коммитом). Находка здесь и caveat там говорят об одном факте с двух сторон
// экрана, и обе нужны: caveat читает тот, кто открыл смету, а находку — тот, кто открыл карточку.
//
// Подавители: костинга нет вовсе (данных нет → молчание, и прогон говорит об этом в NotChecked —
// см. costingNotChecked); `cmt_cost` задан; маршрут пуст (труд нечем измерять).
func checkB6LabourNotSet(v *cardView) []Finding {
	if v.card.Costing == nil {
		v.costingNotChecked()
		return nil
	}
	if v.card.Costing.CmtCost.Valid || len(v.ops) == 0 {
		return nil
	}
	return []Finding{{
		Category: CategoryReadiness,
		Severity: SeverityWarning,
		Title:    "Cost estimate is materials-only: CMT is not set",
		Detail: fmt.Sprintf("The card has a costing row and %d operation(s) in its route, and "+
			"tech_card_costing.cmt_cost is NULL. The cost estimate therefore adds up materials, "+
			"overhead and logistics and presents the sum as the unit cost — with the labour of %d "+
			"steps costed at nothing.", len(v.ops), len(v.ops)),
		Refs:       []string{RefCard},
		Suggestion: "Set the CMT cost, or state in the costing notes that this style is quoted without it.",
		Clause:     "CMT not set",
	}}
}

// ── B7. CMT БЕЗ ОПОРЫ ───────────────────────────────────────────────────────────────────────────
//
// question, warning, одна находка. `cmt_cost` задан, а нормой времени (SMV, 0219) покрыто меньше
// пятой части шагов — откуда взято число?
//
// ПОРОГ 20% — ИЗ ДИЗАЙНА, И ОН НАМЕРЕННО НИЗКИЙ. Вопрос не «почему не все шаги нормированы» (на
// проде их не нормируют почти нигде), а «эта цифра вообще из чего-то выведена». Карточка, где
// нормировано меньше пятой части, цифру CMT ниоткуда вывести не могла.
//
// Подавители: костинга нет; `cmt_cost` не задан (тогда работает B6 — и второй находки о том же
// пустом поле быть не должно); шагов нет.
func checkB7CmtWithoutBacking(v *cardView) []Finding {
	if v.card.Costing == nil {
		return nil // NotChecked уже сказан в B6 — один голос об одном отсутствии
	}
	if !v.card.Costing.CmtCost.Valid || len(v.ops) == 0 {
		return nil
	}
	withSMV := 0
	for _, op := range v.ops {
		if !ndEmpty(op.SMV) {
			withSMV++
		}
	}
	// Целочисленно, без float: 5 × покрытых < общего числа шагов ⇔ покрытие < 20%.
	if withSMV*5 >= len(v.ops) {
		return nil
	}

	return []Finding{{
		Category: CategoryQuestion,
		Severity: SeverityWarning,
		Title:    fmt.Sprintf("CMT is quoted with SMV on %d of %d steps", withSMV, len(v.ops)),
		Detail: fmt.Sprintf("tech_card_costing.cmt_cost carries a number, and only %d of the card's %d "+
			"operations state an SMV. Where does the labour cost come from — a measured route, a "+
			"quote from the factory, or last season's figure?", withSMV, len(v.ops)),
		Refs: []string{RefCard},
		Suggestion: "Say in the costing notes where the CMT figure comes from, or set an SMV on the route so the " +
			"number can be derived.",
	}}
}

// ── B8. WASTAGE ────────────────────────────────────────────────────────────────────────────────
//
// bom_mismatch, warning на NULL и question на > 30%. Читает `wastage_percent` (0073) и провенанс
// нормы колорвея (0261/0296).
//
// NULL ≠ ЯВНЫЙ НОЛЬ, И В ЭТОМ ВСЯ ПРОВЕРКА. `grossByWastage` (dto/style_cost_estimate.go) умножает
// ТОЛЬКО не-NULL: карточка с пустым процентом считает крой в ноль отходов и печатает итог, который
// цех не подтвердит ни на одном настиле. Явный 0 — законное утверждение («отходов нет»), и он
// молчит.
//
// СТРОКИ ПРОВЕНАНСА 'marker' ИСКЛЮЧЕНЫ: норма, снятая с раскладки, уже несёт межлекальные потери в
// своей длине, и гросс-ап поверх неё посчитал бы их дважды (та же причина, по которой костинг не
// гроссит marker-нормы). DXF-строки НЕ исключаются: норма по площади выкроек — нетто, гросс-ап ей
// полагается by design (0294).
func checkB8Wastage(v *cardView) []Finding {
	markerLines := v.markerSourcedBomIDs()

	var out []Finding

	applicable, missing := 0, []CoverageMiss(nil)
	for i := range v.card.BomItems {
		b := &v.card.BomItems[i]
		if !entity.IsRollGoodsSection(b.Section) {
			continue // подавитель: счётные секции гросс-ап не гроссят вовсе
		}
		if markerLines[b.Id] {
			continue // подавитель: норма пришла с раскладки, потери уже внутри
		}
		applicable++
		if b.WastagePercent.Valid {
			continue
		}
		missing = append(missing, CoverageMiss{
			Refs: []string{RefBom(b.Name)},
			Finding: Finding{
				Category: CategoryBomMismatch,
				Severity: SeverityWarning,
				Title:    aiBoundedText(fmt.Sprintf("%q states no cutting wastage", b.Name), 90),
				Detail: fmt.Sprintf("wastage_percent is NULL on %q, and the cost estimate grosses "+
					"up ONLY non-NULL percentages — so this roll-goods line is costed as if the marker "+
					"wasted nothing. NULL is not the same as zero: an explicit 0%% is a real claim and "+
					"would silence this finding.", b.Name),
				Refs:       []string{RefBom(b.Name)},
				Suggestion: "State the cutting wastage of the line, or 0 if there genuinely is none.",
			},
		})
	}
	out = append(out, Aggregate(applicable, missing, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryBomMismatch,
			Severity: SeverityWarning,
			Title: fmt.Sprintf("Cutting wastage is not stated on %d of %d roll-goods lines",
				missing, applicable),
			Detail: "wastage_percent is NULL on these lines, and the cost estimate grosses up only " +
				"non-NULL percentages — they are costed as if the marker wasted nothing. NULL is not " +
				"the same as zero: an explicit 0% is a real claim and would silence this finding.",
			Refs:       sample,
			Suggestion: "State the cutting wastage on each of these lines, or 0 where there genuinely is none.",
		}
	})...)

	// Вторая ветка — вопрос про подозрительно большой процент. Отдельное покрытие: применимое
	// множество у неё другое (линии С процентом), и одна дробь на обе ветки соврала бы про обе.
	hi := decimal.NewFromInt(30)
	applicableHi, missingHi := 0, []CoverageMiss(nil)
	for i := range v.card.BomItems {
		b := &v.card.BomItems[i]
		if !entity.IsRollGoodsSection(b.Section) || !b.WastagePercent.Valid || markerLines[b.Id] {
			continue
		}
		applicableHi++
		if !b.WastagePercent.Decimal.GreaterThan(hi) {
			continue
		}
		missingHi = append(missingHi, CoverageMiss{
			Refs: []string{RefBom(b.Name)},
			Finding: Finding{
				Category: CategoryQuestion,
				Severity: SeverityWarning,
				Title:    aiBoundedText(fmt.Sprintf("%q wastes %s%% in the cut — is that right?", b.Name, b.WastagePercent.Decimal.String()), 90),
				Detail: fmt.Sprintf("wastage_percent on %q is %s%%, which grosses the line's cost up by "+
					"more than a third. That is a real number on a difficult marker and a typo on an "+
					"ordinary one.", b.Name, b.WastagePercent.Decimal.String()),
				Refs:       []string{RefBom(b.Name)},
				Suggestion: "Confirm the percentage against a real marker, or correct it.",
			},
		})
	}
	out = append(out, Aggregate(applicableHi, missingHi, func(missing, applicable int, sample []string) Finding {
		return Finding{
			Category: CategoryQuestion,
			Severity: SeverityWarning,
			Title: fmt.Sprintf("Cutting wastage is above 30%% on %d of %d roll-goods lines",
				missing, applicable),
			Detail: "These lines gross their cost up by more than a third — real on a difficult marker, " +
				"a typo on an ordinary one.",
			Refs:       sample,
			Suggestion: "Confirm the percentages against real markers, or correct them.",
		}
	})...)

	return out
}

// ── ОБЩЕЕ ДЛЯ БЛОКА ─────────────────────────────────────────────────────────────────────────────

// bomLinesOfSection returns the card's BOM lines of one section, in card order.
func (v *cardView) bomLinesOfSection(section entity.TechCardBomSection) []*entity.TechCardBomItem {
	var out []*entity.TechCardBomItem
	for i := range v.card.BomItems {
		if v.card.BomItems[i].Section == section {
			out = append(out, &v.card.BomItems[i])
		}
	}
	return out
}

// dearestFabricLine is B5а's reference: the priciest section='fabric' line of the card. Секция
// именно 'fabric', а не «рулонная»: подкладка дешевле основной ткани по определению, и мерить
// заглушку от подкладки значило бы мерить от того, что сама проверка и ищет.
//
// Упорядочивание допускает НОМИНАЛЬНОЕ сравнение (comparePrices при неизвестном курсе): выбрать
// «самую дорогую» надо детерминированно и всегда, а результат уходит только в B5а — вопросительную
// находку, которая сама говорит вслух, что валюты разные и курса нет.
func (v *cardView) dearestFabricLine() (*entity.TechCardBomItem, bool) {
	var best *entity.TechCardBomItem
	for i := range v.card.BomItems {
		b := &v.card.BomItems[i]
		if b.Section != entity.BomSectionFabric || !b.UnitPrice.Valid {
			continue
		}
		if best == nil {
			best = b
			continue
		}
		ratio, _, ok := v.comparePrices(b, best)
		if ok && ratio.GreaterThan(decimal.NewFromInt(1)) {
			best = b
		}
	}
	return best, best != nil
}

// comparePrices returns a/b as a ratio, whether the comparison is EXACT, and whether it could be
// made at all.
//
// Три случая, и различать их обязательно: одна валюта — точно; разные валюты с известными курсами —
// точно после конверсии; разные валюты без курса — по НОМИНАЛУ и НЕ точно. Последний случай законен
// только у вопросительных находок, которые говорят вслух, на чём стоят (B5а); утверждающая находка
// (B5б) обязана на нём замолчать.
func (v *cardView) comparePrices(a, b *entity.TechCardBomItem) (ratio decimal.Decimal, exact bool, ok bool) {
	if a == nil || b == nil || !a.UnitPrice.Valid || !b.UnitPrice.Valid {
		return decimal.Decimal{}, false, false
	}
	if b.UnitPrice.Decimal.IsZero() {
		return decimal.Decimal{}, false, false // делить на ноль нечем, и «во сколько раз» бессмысленно
	}
	ca, cb := currencyOf(a), currencyOf(b)
	if ca == cb {
		return a.UnitPrice.Decimal.Div(b.UnitPrice.Decimal), true, true
	}
	ra, okA := v.fx.Rate(ca)
	rb, okB := v.fx.Rate(cb)
	if okA && okB && !rb.IsZero() {
		base := b.UnitPrice.Decimal.Mul(rb)
		if base.IsZero() {
			return decimal.Decimal{}, false, false
		}
		return a.UnitPrice.Decimal.Mul(ra).Div(base), true, true
	}
	return a.UnitPrice.Decimal.Div(b.UnitPrice.Decimal), false, true
}

// currencyOf normalises a line's currency code the way Fx.Rate reads it.
func currencyOf(b *entity.TechCardBomItem) string {
	return strings.ToUpper(strings.TrimSpace(b.Currency.String))
}

// linkedBomLines returns the BOM lines a step is linked to, by key and by id, deduplicated.
func (v *cardView) linkedBomLines(op *entity.TechCardOperation) []*entity.TechCardBomItem {
	var out []*entity.TechCardBomItem
	seen := map[string]bool{}
	add := func(b *entity.TechCardBomItem) {
		if b == nil || seen[b.LineKey] {
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

// tightestCountNorm returns the SMALLEST countable norm any colourway SEWS on that line, the
// colourway that sews it and откуда взялось число. Самый скупой колорвей и есть тот, на котором
// линия кончится первой.
//
// ЧИСЛО СПРАШИВАЕТСЯ У ПАРЫ, А НЕ У СТРОКИ: правило («итог пары применяется один раз», «явные
// quantity строк суммируются и отменяют слот») живёт в entity.CountablePairQty и здесь не
// повторяется. Легаси-строки без bom_item_id в пару не входят по carve-out 0295 — они адресуют
// слот позиционным ключом и считаются каждая своим числом, ровно как их считает
// CountablePairRowTotal.
func (v *cardView) tightestCountNorm(line *entity.TechCardBomItem) (decimal.Decimal, string, entity.CountableBasis, bool) {
	var best decimal.Decimal
	name, basis, found := "", entity.CountableBasisNone, false
	consider := func(q decimal.Decimal, cw string, b entity.CountableBasis) {
		if !found || q.LessThan(best) {
			best, name, basis, found = q, cw, b, true
		}
	}
	for i := range v.card.Colorways {
		cw := &v.card.Colorways[i]
		if qty, b := entity.CountablePairQty(entity.CountablePairUsages(cw.Usages, line), line); b != entity.CountableBasisNone {
			consider(qty.Decimal, cw.Name, b)
		}
		for j := range cw.Usages {
			u := &cw.Usages[j]
			if !u.Quantity.Valid || u.IsPieceMaterialAssignment() {
				continue
			}
			if u.BomItemId.Valid && u.BomItemId.Int64 == int64(line.Id) {
				continue // строка состоит в паре — её число уже учтено итогом пары
			}
			if u.BomLineKey != line.LineKey {
				continue
			}
			consider(u.Quantity.Decimal, cw.Name, entity.CountableBasisRows)
		}
	}
	return best, name, basis, found
}

// countNormPhrase говорит, ОТКУДА взялось число, которому не хватило установок. Провенанс здесь не
// украшение: «слот покупает шесть» чинится на строке BOM, а «рецепт колорвея покупает шесть» — в
// рецепте, и отправить читателя не в тот экран значит отправить его чинить не то.
func countNormPhrase(qty decimal.Decimal, colorway string, basis entity.CountableBasis) string {
	if basis == entity.CountableBasisSlot {
		return fmt.Sprintf("the BOM line sews %s per garment (tech_card_bom_item.qty_per_garment)", qty.String())
	}
	return fmt.Sprintf("the recipe of colourway %q sews %s per garment (tech_card_colorway_usage.quantity)",
		colorway, qty.String())
}

// markerSourcedBomIDs is the set of BOM line ids whose consumption came from a saved раскладка.
// Their length already contains the inter-piece waste, so grossing them up by wastage_percent would
// count it twice — which is why B8 leaves them alone rather than reporting a missing percentage.
func (v *cardView) markerSourcedBomIDs() map[int]bool {
	out := map[int]bool{}
	for i := range v.card.Colorways {
		cw := &v.card.Colorways[i]
		for j := range cw.Usages {
			u := &cw.Usages[j]
			if strings.TrimSpace(u.ConsumptionSource.String) != entity.ConsumptionSourceMarker {
				continue
			}
			if u.BomItemId.Valid {
				out[int(u.BomItemId.Int64)] = true
			}
			if b := v.bomByKey[u.BomLineKey]; b != nil {
				out[b.Id] = true
			}
		}
	}
	return out
}

// costingNotChecked says out loud that this run did not look at labour cost at all. Молчание,
// неотличимое от «проверено и чисто», — самая дорогая ложь аудита: карточка без строки костинга
// ничем не отличается от карточки с полным костингом, если обе проверки просто ничего не вернули.
//
// Одна строка на обе проверки: у B6 и B7 отсутствие одно и то же.
func (v *cardView) costingNotChecked() {
	v.notCheck("labour cost (the card carries no tech_card_costing row: neither the CMT figure nor its SMV backing was checked)")
}

// opNumbersOf projects steps onto their numbers, skipping the numberless.
func opNumbersOf(ops []*entity.TechCardOperation) []int32 {
	out := make([]int32, 0, len(ops))
	for _, op := range ops {
		if n, ok := opNumber(op); ok {
			out = append(out, n)
		}
	}
	return out
}

// opRefsOf collects the anchors of several steps, deduplicated in order.
func opRefsOf(ops []*entity.TechCardOperation) []string {
	out := make([]string, 0, len(ops))
	seen := map[string]bool{}
	for _, op := range ops {
		for _, r := range opRefs(op) {
			if seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// bomRefsOf / bomNamesOf project BOM lines onto anchors and onto names.
func bomRefsOf(lines []*entity.TechCardBomItem) []string {
	out := make([]string, 0, len(lines))
	for _, b := range lines {
		out = append(out, RefBom(b.Name))
	}
	return out
}

func bomNamesOf(lines []*entity.TechCardBomItem) []string {
	out := make([]string, 0, len(lines))
	for _, b := range lines {
		out = append(out, b.Name)
	}
	return out
}

// refsCapped keeps at most n anchors, deduplicated in order — the §3.0 «три якоря-образца» rule
// applied to findings that are not coverage checks (their anchor list is a sample too, and a
// finding carrying forty anchors is a finding nobody can act on).
func refsCapped(refs []string, n int) []string {
	out := make([]string, 0, n)
	seen := map[string]bool{}
	for _, r := range refs {
		if seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
		if len(out) == n {
			break
		}
	}
	return out
}

// sortedTokenSet is a small determinism helper: a map iterated for prose must be iterated in one order.
func sortedTokenSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// verbSet agrees the verb of the B1 sentence with the number of steps that set hardware: one step
// SETS, several SET. Одна строка кода за то, чтобы находка на ЕДИНСТВЕННОМ ставящем шаге не
// читалась сломанным английским — а технолог видит её на экране при каждом открытии вкладки.
func verbSet(steps int) string { return plural(steps, "sets", "set") }

// countedLines renders «1 line» / «3 lines» instead of the clerical «%d line(s)». Заголовок
// ПИНИТСЯ голденом и едет технологу: «(s)» в нём — форма для отчёта, а не для человека.
func countedLines(n int) string { return fmt.Sprintf("%d %s", n, plural(n, "line", "lines")) }

// plural picks the singular or the plural form.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
