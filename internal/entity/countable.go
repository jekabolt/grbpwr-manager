package entity

import "github.com/shopspring/decimal"

// СЧЁТНАЯ НОРМА — сколько ШТУК артикула на изделии, и ЕДИНСТВЕННОЕ место, где это число собирается.
//
// Число живёт на СЛОТЕ спецификации (tech_card_bom_item.qty_per_garment, 0333), потому что оно не
// меняется ни по размеру, ни по колорвею; строка рецепта колорвея может его ПЕРЕОПРЕДЕЛИТЬ своим
// quantity (0079), и переопределение НИКОГДА не записывается обратно в строку — ровно та же
// дисциплина, что у блока машинки на шаге и у пина артикула 0221.
//
// ЕДИНИЦА СЧЁТА — ПАРА (КОЛОРВЕЙ × СЛОТ), А НЕ СТРОКА РЕЦЕПТА. Это не оформление, это защита от
// умножения денег: 0295 дословно разрешает строкам «на изделие» (piece_id NULL) ПОВТОРЯТЬ слот с
// разными placement («пуговицы — планка» / «пуговицы — манжета»), потому что составной UNIQUE
// кортежи с NULL не ловит. Построчное COALESCE(usage.quantity, bom.qty_per_garment) дало бы на
// такой карточке 6 + 6 = 12 пуговиц и прибавило бы запас дважды — молча и линейно по числу
// размещений.
//
// ПРАВИЛО ЦЕЛИКОМ, и его читает КАЖДЫЙ читатель, ничего не досчитывая сверху (тот же принцип, по
// которому существуют grossNorm/NormGrossUp — см. их шапки):
//
//   - итог пары применяется ОДИН РАЗ;
//   - если хоть одна строка пары несёт ЯВНОЕ quantity, итог = Σ явных, а значение слота не
//     читается вовсе (человек уже сказал число на этой паре — слот не спорит с ним и не
//     добавляется к нему);
//   - запас (spare_qty) прибавляется ОДИН РАЗ на пару, поверх любого из двух базисов;
//   - пара, которую не поминает ни одна строка рецепта, НЕ СТОИТ НИЧЕГО: слот с количеством, не
//     вошедший в рецепт колорвея, не покупается и не считается (об этом обязан сказать чек-лист
//     готовности, а не деньги);
//   - ЛЕГАСИ-СТРОКИ БЕЗ bom_item_id в пару не группируются — явный carve-out, дословно тот же, что
//     в шапке 0295: такие строки адресуют слот позиционным индексом, и молчаливое схлопывание всех
//     их в одну пару смешало бы разные материалы. Они считаются как считались, по строке.
//
// ГРАНИЦА «СЧЁТНОЕ / МЕРНОЕ» ДЕРЖИТСЯ ЗДЕСЬ, А НЕ У ЗАПОЛНЯЮЩЕГО, чтобы её нельзя было обойти,
// забыв про неё, — тот же довод, что у EffectiveCuttingCoefficient.

// MeasuredSectionList — семьи, которые продаются ДЛИНОЙ или ВЕСОМ и потому считаются нормой, а не
// штуками. Четыре рулонных берутся из RollGoodsSectionList, а не переписываются: пятая рулонная
// семья, добавленная там, обязана приехать сюда сама (ровно то, ради чего список переехал в
// entity). Нитка и trim добавлены к ним потому, что они мерные и НЕ рулонные: у нитки метраж
// мотка, у trim — метраж бейки, тесьмы, канта, ленты и резинки (словарь kind кладёт в trim ровно
// их). Счётная фурнитура живёт в hardware, и она сюда не попадает.
var MeasuredSectionList = func() []TechCardBomSection {
	out := make([]TechCardBomSection, 0, len(RollGoodsSectionList)+2)
	out = append(out, RollGoodsSectionList...)
	return append(out, BomSectionThread, BomSectionTrim)
}()

var measuredSectionSet = func() map[TechCardBomSection]bool {
	m := make(map[TechCardBomSection]bool, len(MeasuredSectionList))
	for _, s := range MeasuredSectionList {
		m[s] = true
	}
	return m
}()

// IsCountableSection reports whether a BOM section is bought BY THE PIECE — the complement of
// MeasuredSectionList (hardware, label, packaging, decoration, other). ЕДИНСТВЕННЫЙ предикат:
// клиентский список MEASURED_SECTIONS (colorway-recipe.tsx) — его зеркало, и второй серверной
// копии быть не должно.
func IsCountableSection(s TechCardBomSection) bool { return !measuredSectionSet[s] }

// CountableBasis — ОТКУДА взялся итог пары. Читателям (бейдж провенанса, сверка с маршрутом) это
// нужно знать, и выводить его повторно они не должны: «слот» и «строки» ведут себя одинаково в
// деньгах и по-разному на экране.
type CountableBasis string

const (
	// CountableBasisNone — числа нет ни на строках пары, ни на слоте. Не ноль: ноль — утверждение.
	CountableBasisNone CountableBasis = ""
	// CountableBasisRows — итог собран из ЯВНЫХ quantity строк пары; значение слота не читалось.
	CountableBasisRows CountableBasis = "rows"
	// CountableBasisSlot — итог взят со слота: ни одна строка пары числа не несёт.
	CountableBasisSlot CountableBasis = "slot"
)

// CountablePairUsages — строки рецепта, составляющие ПАРУ (этот колорвей × этот слот).
//
// Берёт ВЕСЬ рецепт колорвея и возвращает указатели В ТОТ ЖЕ СРЕЗ: читатель, который идёт по
// usages и спрашивает CountablePairRowTotal, обязан передавать пару, построенную из того же среза,
// иначе носитель итога (первая строка пары) не опознается по указателю. Отдельного ключа у строки
// рецепта нет, а копия среза сделала бы «носителя» неопределимым молча.
//
// Из пары исключены:
//   - строки, привязанные к детали кроя (IsPieceMaterialAssignment): они назначают материал детали
//     и нормы не несут вовсе (T8), поэтому и в счётный итог не входят;
//   - легаси-строки без разрешённого bom_item_id (carve-out 0295, см. шапку файла).
//
// Пустой результат означает «этот колорвей слот не поминает» — и это ЗНАЧИМОЕ состояние: счётная
// норма слота тогда не применяется вовсе.
func CountablePairUsages(usages []TechCardColorwayUsage, bom *TechCardBomItem) []*TechCardColorwayUsage {
	if bom == nil || bom.Id <= 0 {
		return nil
	}
	out := make([]*TechCardColorwayUsage, 0, 2)
	for i := range usages {
		u := &usages[i]
		if u.IsPieceMaterialAssignment() {
			continue
		}
		if !u.BomItemId.Valid || u.BomItemId.Int64 != int64(bom.Id) {
			continue
		}
		out = append(out, u)
	}
	return out
}

// CountablePairQty — сколько штук ПРИШИВАЕТСЯ на изделие на паре (колорвей × слот), и на каком
// основании. INVALID + CountableBasisNone, когда числа нет ни у строк, ни у слота, когда слот
// мерный или когда пары нет вовсе (см. шапку файла — все четыре случая разные и ни один из них не
// ноль).
func CountablePairQty(pair []*TechCardColorwayUsage, bom *TechCardBomItem) (decimal.NullDecimal, CountableBasis) {
	if bom == nil || !IsCountableSection(bom.Section) || len(pair) == 0 {
		return decimal.NullDecimal{}, CountableBasisNone
	}
	sum := decimal.Zero
	explicit := false
	for _, u := range pair {
		if u == nil || !u.Quantity.Valid {
			continue
		}
		sum = sum.Add(u.Quantity.Decimal)
		explicit = true
	}
	if explicit {
		return decimal.NullDecimal{Decimal: sum, Valid: true}, CountableBasisRows
	}
	if bom.QtyPerGarment.Valid {
		return decimal.NullDecimal{Decimal: bom.QtyPerGarment.Decimal, Valid: true}, CountableBasisSlot
	}
	return decimal.NullDecimal{}, CountableBasisNone
}

// CountablePairTotal — сколько штук ЗАКУПАЕТСЯ на изделие на паре: пришиваемое ПЛЮС запас слота,
// прибавленный ОДИН РАЗ. Это число читают и деньги, и потребность — разойтись им нельзя ни в одну
// сторону (шапка NormGrossUp описывает этот класс дефекта дословно: цех получает не то количество,
// которое оплачено).
//
// Запас БЕЗ пришиваемого количества (spare_qty есть, а числа нет ни на строках, ни на слоте) не
// становится закупкой: «положить в пакетик запасную к ничему» — недописанное утверждение, а не
// число. Назвать его обязан чек-лист готовности, а не молча посчитать деньги.
func CountablePairTotal(pair []*TechCardColorwayUsage, bom *TechCardBomItem) (decimal.NullDecimal, CountableBasis) {
	qty, basis := CountablePairQty(pair, bom)
	if basis == CountableBasisNone {
		return qty, basis
	}
	if bom.SpareQty.Valid {
		qty.Decimal = qty.Decimal.Add(bom.SpareQty.Decimal)
	}
	return qty, basis
}

// CountablePairRowTotal — доля ОДНОЙ строки в закупаемом итоге пары, то есть то, что эта строка
// вносит в деньги и в потребность.
//
// ИНВАРИАНТ, РАДИ КОТОРОГО ФУНКЦИЯ СУЩЕСТВУЕТ И КОТОРЫЙ ЗАКРЫТ ТЕСТОМ:
//
//	Σ CountablePairRowTotal(pair, u, bom) по всем строкам пары == CountablePairTotal(pair, bom)
//
// ровно, без остатка и без повтора. Всё, чего строки не несут сами (итог слота и/или запас),
// лежит на ПЕРВОЙ строке пары — носителе; остальные строки пары получают ноль, а не «ничего»:
// валидный ноль говорит читателю «эта строка посчитана, число пары лежит на соседке», тогда как
// INVALID означал бы «нормы нет» и поднял бы ложное замечание о непосчитанной строке.
//
// ПОЧЕМУ НОСИТЕЛЬ, А НЕ ДЕЛЁЖ. Итог — свойство ПАРЫ; поделить 7 пуговиц между «планкой» и
// «манжетой» может только тот, кто знает, сколько куда, а это ровно то, чего в данных нет.
// Придуманная дробь читалась бы как утверждение.
//
// МЕРНАЯ СЕКЦИЯ СЮДА НЕ ДОХОДИТ: на ней возвращается собственное quantity строки, как и до 0333 —
// легаси-строка ткани, на которой когда-то набрали «штуки», продолжает считаться ровно так же.
func CountablePairRowTotal(pair []*TechCardColorwayUsage, u *TechCardColorwayUsage, bom *TechCardBomItem) decimal.NullDecimal {
	if u == nil {
		return decimal.NullDecimal{}
	}
	if u.IsPieceMaterialAssignment() {
		// Строка, привязанная к детали, нормы не несёт (T8) — ни своей, ни доли пары.
		return decimal.NullDecimal{}
	}
	if bom == nil || !IsCountableSection(bom.Section) {
		return u.Quantity
	}
	if !bom.QtyPerGarment.Valid && !bom.SpareQty.Valid {
		// СЛОТУ НЕЧЕГО СКАЗАТЬ — ЗНАЧИТ ПАРА НЕ ВМЕШИВАЕТСЯ, и строка считается ровно как до 0333.
		//
		// Это граница, а не оптимизация. Без неё правило пары молча переписывало ИСТОРИЮ: на
		// счётном слоте без единой новой колонки, где одна строка несёт явное quantity, а вторая —
		// свой Consumption, наличие явного числа задавало базис ВСЕЙ паре, и вторая строка
		// получала валидный НОЛЬ вместо своего расхода. Деньги исчезали на данных, которых волна
		// не касалась вовсе, — то есть ровно то, чего вся конструкция должна была избежать.
		//
		// Правило пары существует затем, чтобы число СЛОТА и запас не умножились по размещениям.
		// Нет ни числа, ни запаса — умножать нечего, и права зануления соседней строки у пары нет.
		return u.Quantity
	}
	if !countablePairContains(pair, u) {
		// СТРОКА НЕ ИЗ ЭТОЙ ПАРЫ — считается как до 0333, своим числом. Случай законный, а не
		// защитный: planBomLine резолвит строку рецепта к слоту ДВУМЯ путями (bom_item_id и
		// легаси-позиция bom_item_index), а пара собирается только по первому (carve-out 0295 в
		// шапке файла). Значит на слоте, где одно размещение заведено ссылкой, а другое позицией,
		// читатель законно приходит сюда со строкой вне пары. Ответить ей долей пары нельзя — она
		// в паре не состоит; ответить «валидным нулём» тем более: костинг молча потерял бы её
		// деньги, а готовность увидела бы норму там, где строка её не несёт.
		return u.Quantity
	}
	total, basis := CountablePairTotal(pair, bom)
	if basis == CountableBasisNone {
		// Ни слот, ни строки числа не дают: строка отвечает ровно тем, что на ней есть (обычно
		// INVALID — «нормы нет», и читатель обязан это заметить).
		return u.Quantity
	}
	own := decimal.Zero
	if u.Quantity.Valid {
		own = u.Quantity.Decimal
	}
	if isCountablePairCarrier(pair, u) {
		own = own.Add(countablePairResidual(pair, total.Decimal, basis))
	}
	return decimal.NullDecimal{Decimal: own, Valid: true}
}

// countablePairResidual — та часть закупаемого итога пары, которой не несёт НИ ОДНА строка:
// на базисе «слот» это весь итог слота плюс запас, на базисе «строки» — только запас (Σ явных уже
// лежит на самих строках). Отдельной публичной функцией не выносится намеренно: единственный
// законный способ применить остаток — через CountablePairRowTotal, который знает, КОМУ его отдать.
func countablePairResidual(pair []*TechCardColorwayUsage, total decimal.Decimal, basis CountableBasis) decimal.Decimal {
	if basis != CountableBasisRows {
		return total
	}
	explicit := decimal.Zero
	for _, u := range pair {
		if u != nil && u.Quantity.Valid {
			explicit = explicit.Add(u.Quantity.Decimal)
		}
	}
	return total.Sub(explicit)
}

// countablePairContains — состоит ли строка в паре. Сравнение по указателю, по той же причине и с
// тем же требованием к вызывающему, что у isCountablePairCarrier ниже.
func countablePairContains(pair []*TechCardColorwayUsage, u *TechCardColorwayUsage) bool {
	for _, p := range pair {
		if p == u {
			return true
		}
	}
	return false
}

// isCountablePairCarrier — носитель остатка пары: её ПЕРВАЯ строка в порядке рецепта. Сравнение по
// указателю, и это единственное, что здесь можно сравнивать: своего ключа у строки рецепта нет, а
// id равен нулю на всём пути записи и в тестах. Отсюда требование к вызывающему (см.
// CountablePairUsages): пара обязана быть построена из ТОГО ЖЕ среза, по которому он идёт.
func isCountablePairCarrier(pair []*TechCardColorwayUsage, u *TechCardColorwayUsage) bool {
	return len(pair) > 0 && pair[0] == u
}
