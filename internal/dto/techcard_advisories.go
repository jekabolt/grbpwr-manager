package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// СОВЕЩАТЕЛЬНЫЕ ЗАМЕЧАНИЯ ЧЕК-ЛИСТА ГОТОВНОСТИ — то, что стоит поправить, но что НИЧЕГО не
// запрещает: ни сохранение карточки, ни её релиз.
//
// ПОЧЕМУ ОТДЕЛЬНЫЙ СПИСОК, А НЕ СТРОКА ЧЕК-ЛИСТА. Невыполненная строка требования блокирует по
// построению — allReadinessMet считает её провалом, и release_ready гаснет. Значит замечание,
// положенное туда «просто чтобы сказать», молча становится запретом на выпуск карточки, состояние
// которой законно. Строка UNKNOWN тоже не подходит: она означает «сервер не смог ответить», а
// здесь сервер отвечает уверенно — он видит и слот, и рецепт, и сборочную ведомость.
//
// ВСЕ ПЯТЬ ГОВОРЯТ ОБ ОДНОМ КЛАССЕ ДЕФЕКТА: заполненное поле, которое НИКТО НЕ ЧИТАЕТ. Число на
// слоте, не вошедшем в рецепт; счётная норма на строке, считающейся по размерам; компонент
// сборочной ведомости, которого нет в спецификации. Ни один из них не ошибка данных — каждый
// молча стоит ноль, и до этого списка сказать об этом было негде.
//
// ФУНКЦИЯ ЧИСТАЯ: ни стора, ни транспорта — ровно как TechCardCostBlockers, и по той же причине.
// Правило, которое можно вызвать с выдуманной карточкой, пинится таблицей, а не сценарием.

// Ключи замечаний — стабильные машинные имена, по которым ветвится клиент. Константами, а не
// литералами по месту: у каждого ключа два читателя (сервер и тест), и разъехавшаяся опечатка
// выглядела бы как «замечание не поднялось».
const (
	// AdviceSpareKitMissing — в изделие кладётся запас, а пакетика на карточке нет.
	AdviceSpareKitMissing = "spare_kit_missing"
	// AdviceSpareKitEmpty — пакетик есть, класть в него нечего.
	AdviceSpareKitEmpty = "spare_kit_empty"
	// AdviceAssemblyComponentNotInBom — сборочная ведомость называет компонент, которого нет в
	// спецификации.
	AdviceAssemblyComponentNotInBom = "assembly_component_not_in_bom"
	// AdviceCountableSlotUnused — у слота задано количество, но его не поминает ни один рецепт.
	AdviceCountableSlotUnused = "countable_slot_unused"
	// AdviceCountableSlotSized — счётная норма на слоте, чья строка рецепта считается по размерам.
	AdviceCountableSlotSized = "countable_slot_sized"
	// AdviceCountableSpareWithoutQty — запас есть, а пришиваемого количества нет ни на слоте, ни на
	// строках: запас не закупается вовсе.
	AdviceCountableSpareWithoutQty = "countable_spare_without_quantity"
)

// TechCardAdvice — одно замечание: машинный ключ и фраза словами оператора, готовая к показу как
// есть. Пары «ключ + текст» достаточно: у замечания нет ни met, ни unknown, потому что оно ни во
// что не упирается — оно только называет факт.
type TechCardAdvice struct {
	Key  string
	Text string
}

// TechCardAdvisories — все пять замечаний по карточке, в устойчивом порядке (пакетик, сборочная
// ведомость, слоты в порядке спецификации). Пустой результат = сказать нечего.
//
// assembly и variantsByComponent приходят СНАРУЖИ, потому что сборочная ведомость не лежит на
// карточке: её читает отдельный запрос, и её отсутствие (nil) — законное состояние, при котором
// проверка 3 просто молчит. Так же ведёт себя чек-лист при нечитаемом индексе размеров: одна
// недоступная таблица не должна гасить остальные проверки.
func TechCardAdvisories(tc *entity.TechCard, assembly []entity.StyleAssembly,
	variantsByComponent map[int][]entity.TechCardOutputVariant) []TechCardAdvice {
	if tc == nil {
		return nil
	}
	out := make([]TechCardAdvice, 0, 4)
	out = append(out, spareKitAdvisories(tc)...)
	out = append(out, assemblyComponentAdvisories(tc, assembly, variantsByComponent)...)
	out = append(out, countableSlotAdvisories(tc)...)
	if len(out) == 0 {
		return nil
	}
	return out
}

// spareKitAdvisories — две половины одного утверждения «запас едет с изделием»: число на слоте
// говорит, СКОЛЬКО положить, строка вида spare_kit_bag — ВО ЧТО. Порознь ни одна из половин не
// исполнима, и обе стороны стоят ноль по-разному: без пакетика запас некуда положить, без запаса
// пакетик уедет пустым.
//
// ВЫВОДИТЬ ПАКЕТИК ИЗ НАЛИЧИЯ ЗАПАСА НЕЛЬЗЯ — это ровно «хранить выводимое», на котором в проекте
// уже ломали подпись: строка BOM стоит денег и закупается, и появиться она обязана рукой человека.
//
// Секцию слота здесь не смотрим намеренно: spare_qty на мерном слоте — тоже недописанное
// утверждение, и молчать о нём было бы хуже, чем назвать.
func spareKitAdvisories(tc *entity.TechCard) []TechCardAdvice {
	spare, bag := false, false
	for i := range tc.BomItems {
		b := &tc.BomItems[i]
		if b.SpareQty.Valid && b.SpareQty.Decimal.IsPositive() {
			spare = true
		}
		if b.Kind.Valid && entity.TechCardBomKind(b.Kind.String) == entity.BomKindSpareKitBag {
			bag = true
		}
	}
	switch {
	case spare && !bag:
		return []TechCardAdvice{{
			Key:  AdviceSpareKitMissing,
			Text: "spare hardware is packed with the garment, but the card has no spare-kit bag line",
		}}
	case bag && !spare:
		return []TechCardAdvice{{
			Key:  AdviceSpareKitEmpty,
			Text: "the card has a spare-kit bag, but no slot sets anything aside to go into it",
		}}
	}
	return nil
}

// assemblyComponentAdvisories — САМОЕ ДОРОГОЕ ИЗ ПЯТИ. Сборочная ведомость до костинга не доходит
// вовсе (style_assembly не читают ни style_cost_estimate, ни techcard_production), поэтому строка
// BOM — ЕДИНСТВЕННОЕ место, где вспомогательный компонент стоит денег. Своя бирка, свой кофр, свой
// пакетик, названные только в ведомости, молча стоят ноль и не попадают в закупку.
//
// ВЫХОД РЕЗОЛВИТСЯ ТОЛЬКО ЧЕРЕЗ entity.ResolveAssemblyOutput. Это единственное определение правила,
// оно цветоосознанное и намеренно отказывается угадывать; читать tech_card.output_material_id
// напрямую нельзя — у карточки с цветовыми вариантами он устарел по построению.
//
// НЕРАЗРЕШЁННЫЙ ВЫХОД → МОЛЧАНИЕ, а не обвинение. Ретайренный цвет, архивный материал, спорящие
// варианты — это «не знаю, какое ведро», а не «ведра нет в спецификации»: обвинить здесь значит
// отправить оператора заводить строку BOM на компонент, который в ней, возможно, уже есть.
//
// ЧТО ИМЕННО ПРОВЕРЯЕТСЯ — «ЕСТЬ ЛИ ВЫХОД В СПЕЦИФИКАЦИИ ВООБЩЕ», А НЕ «КУПИЛ ЛИ ЕГО ЭТОТ
// КОЛОРВЕЙ». Множество известных артикулов строится по ВСЕЙ карточке (умолчания слотов + пины всех
// колорвеев), поэтому редкий случай «пины перепутаны между цветами» — чёрный колорвей взял белый
// кофр, белый чёрный — проверка не поймает: оба артикула в спецификации есть.
//
// ЭТО ОСОЗНАННАЯ ГРАНИЦА, А НЕ НЕДОСМОТР, и она куплена замером. Поколорвейная сверка обязана
// строить множество из СТРОК РЕЦЕПТА этого колорвея — деньги считаются только по ним, — а рецептом
// сегодня поминается 10 строк BOM из 34 на бете и 6 из 28 на проде (замер 2026-08-25). То есть
// точная версия обвинила бы почти каждый вспомогательный компонент на каждой полузаполненной
// карточке, и совещательный список утонул бы в ложных фразах. Пересматривать это стоит тогда,
// когда рецепты начнут покрывать спецификацию, а не раньше.
//
// ОДНО ЗАМЕЧАНИЕ НА КОМПОНЕНТ, а не на пару (компонент × колорвей): факт один — «этого компонента
// нет в спецификации», — и повторённый по числу цветов он залил бы экран одной и той же фразой.
// Поднимается, если хоть один РАЗРЕШЁННЫЙ выход компонента отсутствует в спецификации: у карточки
// с цветовыми вариантами чёрный кофр может быть заведён, а белый — нет, и это ровно тот же молчащий
// ноль, только на одном колорвее.
//
// Выключенные строки ведомости пропускаются: ListStyleAssembly отдаёт их намеренно (редактор стиля
// обязан их показать и уметь включить обратно), но на изделие они не идут и денег не стоят.
func assemblyComponentAdvisories(tc *entity.TechCard, assembly []entity.StyleAssembly,
	variantsByComponent map[int][]entity.TechCardOutputVariant) []TechCardAdvice {
	// Без колорвеев резолвить не по чему: правило цветоосознанное, а цвет живёт на колорвее.
	// Молчим — «нет ни одного колорвея» уже сказано строкой colorway_linked, и второе слово о том
	// же факте читалось бы как вторая проблема.
	if len(assembly) == 0 || len(tc.Colorways) == 0 {
		return nil
	}
	known := cardKnownMaterialIds(tc)
	order := make([]int, 0, len(assembly))
	firstLine := make(map[int]*entity.StyleAssembly, len(assembly))
	for i := range assembly {
		a := &assembly[i]
		if !a.Active || a.ComponentTechCardId <= 0 {
			continue
		}
		if _, seen := firstLine[a.ComponentTechCardId]; seen {
			continue
		}
		firstLine[a.ComponentTechCardId] = a
		order = append(order, a.ComponentTechCardId)
	}
	out := make([]TechCardAdvice, 0, len(order))
	for _, componentID := range order {
		a := firstLine[componentID]
		resolvedAny, missing := false, false
		for i := range tc.Colorways {
			cw := &tc.Colorways[i]
			// Архивный колорвей не изделие: карточку он не удорожает и спрашивать с него нечего.
			// Загрузчик их уже отсекает — правило повторено здесь потому, что функция чистая и
			// обязана вести себя одинаково с любой карточкой, которую ей дали.
			if cw.Status == entity.ColorwayStatusArchived {
				continue
			}
			res := entity.ResolveAssemblyOutput(cw.ColorCode, variantsByComponent[componentID],
				entity.AssemblyLegacyOutput{
					MaterialId:   a.OutputMaterialId,
					MaterialName: a.OutputMaterialName,
					Archived:     a.OutputMaterialArchived.Bool,
				})
			if res.Unresolved || res.ResolvedMaterialId <= 0 {
				continue
			}
			resolvedAny = true
			if !known[res.ResolvedMaterialId] {
				missing = true
			}
		}
		if !resolvedAny || !missing {
			continue
		}
		out = append(out, TechCardAdvice{
			Key: AdviceAssemblyComponentNotInBom,
			Text: assemblyComponentLabel(a) +
				": the assembly list names a component that is not in the BOM, so it will be neither costed nor purchased",
		})
	}
	return out
}

// cardKnownMaterialIds — артикулы, за которые карточка ПЛАТИТ: артикул по умолчанию на слоте плюс
// пин колорвея на строке рецепта.
//
// ПИН ОБЯЗАТЕЛЕН В ЭТОМ МНОЖЕСТВЕ. Слот — это роль, пин — конкретный артикул (0221), и колорвей,
// взявший для «бирки» именно тот артикул, который производит aux-карта, платит за него ровно так
// же, как если бы он стоял умолчанием на слоте. Без пинов проверка 3 обвиняла бы карточки, где
// компонент заведён единственным законным способом.
func cardKnownMaterialIds(tc *entity.TechCard) map[int]bool {
	ids := make(map[int]bool, len(tc.BomItems))
	for i := range tc.BomItems {
		b := &tc.BomItems[i]
		if b.MaterialId.Valid && b.MaterialId.Int64 > 0 {
			ids[int(b.MaterialId.Int64)] = true
		}
	}
	for i := range tc.Colorways {
		cw := &tc.Colorways[i]
		for j := range cw.Usages {
			u := &cw.Usages[j]
			if id, _ := u.EffectiveMaterialId(resolveUsageBom(tc.BomItems, u)); id > 0 {
				ids[id] = true
			}
		}
	}
	return ids
}

// countableSlotAdvisories — два состояния, в которых счётное число слота НЕ ДЕЙСТВУЕТ, хотя
// оператор его написал.
//
// Д3 («не входит ни в один рецепт»): деньги считаются обходом колорвеев → их строк рецепта. Слот,
// которого не поминает ни одна строка, не даёт ни денег, ни потребности цеха — это дословно записано
// в шапке entity/countable.go как обязанность чек-листа, а не денег.
//
// Д4 («строка считается по размерам»): все читатели нормы спрашивают резолвер пары ТОЛЬКО когда
// по-размерных норм нет — LineTotal и usagePerGarmentQty выходят на len(SizeConsumptions) > 0
// раньше него, а usageNormForSize отвечает по-размерной нормой для тех размеров, что в ней есть.
// То есть на такой строке счётное число либо не применяется вовсе, либо применяется через раз, по
// размерам, которых в градации не оказалось. Состояние законное — ни парсер, ни стор, ни схема его
// не запрещают, — и молча переключать здесь деньги нельзя: по-размерная норма старше счётной, и
// переключение сдвинуло бы числа на карточках, которых волна не касалась. Поэтому — сказать вслух.
//
// МЕРНЫЙ СЛОТ СЮДА НЕ ДОХОДИТ (IsCountableSection): у ткани и нитки количество на изделие не
// заявляется вовсе, и обе проверки на ней были бы разговором ни о чём. Граница берётся тем же
// предикатом, что и деньги, — второй копии этого списка секций в проекте быть не должно.
func countableSlotAdvisories(tc *entity.TechCard) []TechCardAdvice {
	// Рецептов нет вовсе — сравнивать не с чем: обе проверки говорят о том, поминает ли слот СТРОКА
	// РЕЦЕПТА, а на карточке без колорвеев их нет ни одной. См. довод о colorway_linked выше.
	if len(tc.Colorways) == 0 {
		return nil
	}
	out := make([]TechCardAdvice, 0, 2)
	for i := range tc.BomItems {
		b := &tc.BomItems[i]
		if !entity.IsCountableSection(b.Section) || !entity.SlotCarriesCountableNorm(b) {
			continue
		}
		used, sized := false, false
		// ЗАПАС СЧИТАЕТСЯ ПО ПАРЕ, А НЕ ПО КАРТОЧКЕ. Базис (slot | rows | none) — свойство ПАРЫ
		// (колорвей × слот), и колорвей, чья строка числа не несёт, покупает НОЛЬ независимо от
		// того, что написал сосед. Глобальный флаг «где-то на карточке число есть» подавлял бы
		// замечание ровно там, где оно нужно: чёрный сказал шесть, белый не сказал ничего, и белое
		// изделие уезжает без пуговиц и без пакетика.
		spareLost, unitsLost := false, false
		for j := range tc.Colorways {
			cw := &tc.Colorways[j]
			if cw.Status == entity.ColorwayStatusArchived {
				continue
			}
			mentions, buysOwn := false, false
			for k := range cw.Usages {
				u := &cw.Usages[k]
				// СТРОКА РЕЗОЛВИТСЯ К СЛОТУ ДВУМЯ ПУТЯМИ, И СПРАШИВАТЬ НАДО ОБА. Пара
				// (CountablePairUsages) по carve-out'у 0295 намеренно исключает легаси-строки,
				// адресующие слот позиционным индексом: в ДЕНЬГАХ они не группируются, потому что
				// схлопывание смешало бы разные материалы. Но вопрос этой проверки другой —
				// «поминает ли слот хоть одна строка рецепта», — и на него легаси-строка отвечает
				// ДА: она этот слот потребляет и платит за него своим числом. Спросить только пару
				// значило бы обвинить исправную карточку в том, что её слот никто не покупает.
				// Тот же resolveUsageBom, которым идёт костинг, — второй копии правила нет.
				if resolveUsageBom(tc.BomItems, u) != b {
					continue
				}
				// Строка, привязанная к детали кроя, назначает материал и нормы не несёт (T8):
				// «поминает» слот она не в том смысле, о котором спрашивают деньги.
				if u.IsPieceMaterialAssignment() {
					continue
				}
				used, mentions = true, true
				// «Строка платит сама за себя»: собственное явное число или по-размерные нормы.
				// Это ОТДЕЛЬНЫЙ вопрос от базиса пары: легаси-строка своё число платит, но в пару
				// не входит, поэтому запас слота к ней не прибавляется НИКОГДА.
				if u.Quantity.Valid || len(u.SizeConsumptions) > 0 {
					buysOwn = true
				}
				if len(u.SizeConsumptions) > 0 {
					sized = true
				}
			}
			if !mentions || !b.SpareQty.Valid || !b.SpareQty.Decimal.IsPositive() {
				continue
			}
			// Запас прибавляется РОВНО ОДИН РАЗ и РОВНО К ПАРЕ, и только когда у пары есть базис.
			// Пустая пара (слот поминает лишь легаси-строка) базиса не имеет — значит запас этого
			// колорвея не покупает никто.
			if _, basis := entity.CountablePairQty(entity.CountablePairUsages(cw.Usages, b), b); basis == entity.CountableBasisNone {
				spareLost = true
				if !buysOwn {
					unitsLost = true
				}
			}
		}
		switch {
		case !used:
			out = append(out, TechCardAdvice{
				Key: AdviceCountableSlotUnused,
				Text: bomSlotLabel(b) +
					": the slot carries a quantity, but no colourway recipe uses it, so it will be neither costed nor purchased",
			})
		case spareLost:
			// ЗАПАС БЕЗ ОСНОВАНИЯ НЕ СТАНОВИТСЯ ЗАКУПКОЙ, и сказать об этом обязан именно чек-лист:
			// CountablePairTotal отказывается прибавлять запас к отсутствующему базису дословно по
			// этой причине («положить в пакетик запасную к ничему» — недописанное утверждение, а
			// не число), и там же записано, что назвать это должны здесь, а не деньги. До шестого
			// замечания не называл никто: пакетик на карточке есть — обе половины проверки пакетика
			// молчат; слот поминается рецептом — молчит и «не входит ни в один рецепт».
			//
			// ДВА ТЕКСТА, ПОТОМУ ЧТО ТЕРЯЕТСЯ РАЗНОЕ. Если строка платит сама за себя (собственное
			// число или по-размерные нормы), изделие свои единицы получает и теряется ТОЛЬКО запас;
			// сказать там «не покупается ничего» значило бы соврать оператору про его же деньги.
			out = append(out, TechCardAdvice{
				Key:  AdviceCountableSpareWithoutQty,
				Text: spareLostText(b, unitsLost),
			})
		case sized:
			out = append(out, TechCardAdvice{
				Key: AdviceCountableSlotSized,
				Text: bomSlotLabel(b) +
					": the slot carries a quantity, but its recipe line is graded per size, so the quantity does not apply",
			})
		}
	}
	return out
}

// spareLostText разводит два разных убытка. unitsLost — колорвей не покупает НИЧЕГО: ни своих
// единиц, ни запаса (строка слот поминает, но числа не несёт, и слот молчит тоже). Иначе единицы
// куплены — своим числом строки или по-размерной нормой, — и теряется ровно запас: он прибавляется
// один раз и только к паре с базисом.
func spareLostText(b *entity.TechCardBomItem, unitsLost bool) string {
	if unitsLost {
		return bomSlotLabel(b) +
			": the slot sets spares aside, but a colourway recipe states no per-garment quantity — that colourway purchases neither its units nor its spares"
	}
	return bomSlotLabel(b) +
		": the recipe line pays for its own units, but the slot's spares are never added to it — the spares are not purchased"
}

// bomSlotLabel — как слот назвать оператору: его имя, иначе стабильный ключ строки, иначе id. Та же
// лестница, что в TechCardCostBlockers: фраза без имени слота отправляет искать вручную по всей
// спецификации.
func bomSlotLabel(b *entity.TechCardBomItem) string {
	if name := strings.TrimSpace(b.Name); name != "" {
		return name
	}
	if b.LineKey != "" {
		return b.LineKey
	}
	return fmt.Sprintf("slot #%d", b.Id)
}

// assemblyComponentLabel — имя aux-карты, иначе её id: ведомость читается по названиям, но
// безымянный компонент лучше назвать номером, чем начать фразу с двоеточия.
func assemblyComponentLabel(a *entity.StyleAssembly) string {
	if name := strings.TrimSpace(a.ComponentName); name != "" {
		return name
	}
	return fmt.Sprintf("component #%d", a.ComponentTechCardId)
}
