package dto

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// ReleaseFrozenCosts — деньги релиза, и ТОЛЬКО деньги: во что утверждённая ревизия оценила одно
// изделие каждого колорвея. Ключ карты — colorway_id снапшота, он же product_id линии прогона:
// колорвей карточки И ЕСТЬ продукт (product.style_id = card, materials.go `c.id AS product_id`),
// та же идентичность, на которой стоит CutSpecCardFromReleaseSnapshot.
type ReleaseFrozenCosts struct {
	// Currency — валюта костинга релиза, одна на все цены ниже: колорвеи одной карточки считаются
	// одним костинг-блоком, у него ровно одна валюта.
	Currency string
	// UnitCostByColorway — цена ОДНОГО изделия колорвея на БАЗОВОМ размере карточки, включая
	// ручные статьи (CMT/логистика/накладные) и брак — то же содержимое, что у скаляра релиза,
	// просто по каждому цвету отдельно.
	UnitCostByColorway map[int]decimal.Decimal
}

// ReleaseFrozenColorwayCosts достаёт из замороженного блоба релиза цену изделия ПО КАЖДОМУ
// КОЛОРВЕЮ — посчитанную в момент релиза и замороженную вместе со спецификацией.
//
// ПОЧЕМУ ИМЕННО ЭТО ПОЛЕ, А НЕ ПЕРЕСЧЁТ ПО ЗАМОРОЖЕННЫМ ВХОДАМ. Пересчитать цену колорвея из
// блоба НЕЛЬЗЯ, и это факт о снапшоте, а не о лени. Блоб несёт нормы (в том числе по размерам),
// цены строк BOM и ПИНЫ рецептов — то есть КАКОЙ артикул колорвей берёт в слот, — но не несёт
// ЦЕНУ пришпиленного артикула: она живёт в каталоге материалов и подставляется на лету
// (pinShadowBom читает её из TechCard.LinkedMaterials, а LinkedMaterials в контракте TechCard
// нет вовсе). Пересчёт по блобу оставил бы пришпиленную строку БЕЗ цены, то есть по правилу
// «непосчитанная клетка — непосчитанная партия» обнулил бы весь прогон именно на тех карточках,
// ради которых пины и заведены. А взять цену пина из СЕГОДНЯШНЕГО каталога значило бы разморозить
// цену релиза: она поехала бы от переоценки склада, и «сегодня» разошлось бы со снимком на
// прогоне, у которого не менялось ничего. costing.colorway_costs — единственное место, где цена
// пина УЖЕ заморожена: она посчитана каталогом на момент релиза и с тех пор не двигается.
//
// КОЛОРВЕЙ С ФЛАГОМ НЕПОЛНОТЫ ПРОПУСКАЕТСЯ. has_unpriced значит «хотя бы одна строка рецепта не
// посчиталась вовсе», has_unconverted_currencies — «строка в другой валюте не попала в сумму»:
// в обоих случаях число ЗАНИЖЕНО на целый материал. Это ровно та неполнота, по которой костинг
// отказывается засеивать product.cost_price (completeForSeed), и плановая цена партии — не то
// место, где её стоит впервые принять за правду. Пропущенный колорвей = карты для него нет =
// читатель остаётся на замороженном скаляре релиза.
//
// ЧЕГО ФЛАГИ НЕ ЛОВЯТ — СТАРЫЕ БЛОБЫ. has_unpriced приехал на провод позже самих релизов, а
// proto3 не отличает «поля не было» от явного false, поэтому у релиза, снятого до этого поля,
// неполная цена колорвея прочитается как полная. Сделать с этим из снапшота нечего: пин в блобе
// выглядит неоценённым ВСЕГДА (цены артикула там нет), так что «неполно» и «пришпилено» по самому
// блобу неразличимы. Ограничен тут не размер ошибки (непосчитанных строк могло быть сколько
// угодно), а её население: релизы той эпохи, у которых костинг уже тогда не считался целиком.
//
// nil = снапшот денег не несёт (нет костинг-блока, нет валюты, ни один колорвей не пригоден).
func ReleaseFrozenColorwayCosts(snap *pb_common.TechCard) *ReleaseFrozenCosts {
	if snap == nil {
		return nil
	}
	costing := snap.GetTechCard().GetCosting()
	if costing == nil {
		return nil
	}
	currency := strings.TrimSpace(costing.GetCurrency())
	if currency == "" {
		// Без валюты число не деньги: сравнить его с базовой валютой (а на этой отсечке стоит вся
		// запись снапшота плановой цены) было бы не с чем.
		return nil
	}

	// КОЛОРВЕЙ БЕЗ РЕЦЕПТА НАСЛЕДУЕТ ЦЕНУ СТИЛЯ, а не свою замороженную цифру, и это не
	// вольность — это правило ComputeColorwayUnitCost, без которого замороженная цифра лжёт.
	// Проекция костинга считает пустой рецепт нулём материалов, поэтому у колорвея, которому
	// рецепт никогда не писали (легаси-стили с одним авторским рецептом на все цвета), в
	// colorway_costs лежат ОДНИ РУЧНЫЕ СТАТЬИ: цена без ткани. Взвесить партию такой цифрой значит
	// уронить план на всю материальную составляющую — тихо и на самых обычных карточках.
	authored := make(map[int]bool, len(snap.GetColorways()))
	for _, cw := range snap.GetColorways() {
		if len(cw.GetUsages()) > 0 {
			authored[int(cw.GetColorwayId())] = true
		}
	}
	// Цена стиля = корень костинга (первый колорвей), и наследовать её можно, только если она сама
	// полна — те же два флага, что и у колорвея.
	style, err := nullDecimalFromPb(costing.GetUnitCost())
	styleOK := err == nil && style.Valid && style.Decimal.IsPositive() &&
		!costing.GetHasUnpriced() && !costing.GetHasUnconvertedCurrencies()

	out := &ReleaseFrozenCosts{Currency: currency, UnitCostByColorway: make(map[int]decimal.Decimal, len(costing.GetColorwayCosts()))}
	for _, cc := range costing.GetColorwayCosts() {
		id := int(cc.GetColorwayId())
		if id <= 0 || cc.GetHasUnpriced() || cc.GetHasUnconvertedCurrencies() {
			continue
		}
		unit, err := nullDecimalFromPb(cc.GetUnitCost())
		if err != nil || !unit.Valid || !unit.Decimal.IsPositive() {
			// Та же отсечка, что у ComputeTechCardUnitCost: неположительная цена — не цена.
			continue
		}
		if !authored[id] {
			if !styleOK {
				continue
			}
			unit = style
		}
		out.UnitCostByColorway[id] = unit.Decimal
	}
	if len(out.UnitCostByColorway) == 0 {
		return nil
	}
	return out
}

// ComputeReleaseRunPlannedUnitCost — плановая цена изделия РЕЛИЗНОГО прогона: замороженные цены
// колорвеев релиза, взвешенные СОБСТВЕННЫМ миксом линий этой партии —
//
//	Σ(frozen_unit_cost(line.product_id) × line.planned_qty) ÷ Σ line.planned_qty
//
// ЗАЧЕМ. Релизный прогон ценился ОДНИМ скаляром релиза, а тот — цена ПЕРВОГО колорвея карточки
// (ComputeTechCardUnitCost). Партия из 50 чёрных по €10 и 50 белых по €30 снимала €10 вместо
// €20, которые она собирается потратить, и вся plan/fact вариация партии ехала от этого на 100%.
// Пины существуют ровно затем, чтобы колорвеи стоили по-разному; одна колонка planned_unit_cost
// значит не «один цвет», а «одно ВЗВЕШЕННОЕ число», и веса лежат на линиях прогона.
//
// ВСЕ ВХОДЫ ЗАМОРОЖЕНЫ. Ни живой карточки, ни каталога материалов, ни курсов: только блоб релиза
// и линии прогона. Поэтому снимок (snapshotPlannedCost) и «сегодня» (plannedUnitCostToday)
// совпадают по построению, и бейдж «карточка изменилась» на релизном прогоне молчит — ровно как
// молчал, когда там стоял скаляр.
//
// ЧЕГО ЭТА ФОРМУЛА НЕ ДЕЛАЕТ — РАЗМЕРОВ. Живая ветка ценит клетку (колорвей, размер) и потому
// планирует партию из одних XL по нормам XL; замороженная цена в блобе есть только на БАЗОВОМ
// размере карточки, а пересчитать её на другой размер невозможно по причине из
// ReleaseFrozenColorwayCosts (цены пина в блобе нет). Считать часть клеток по размеру, а часть —
// по базовому размеру было бы хуже обоих вариантов: смысл среднего зависел бы от того, какие
// колорвеи оказались пришпилены, и никто бы этого не увидел. Так что релизная партия
// взвешивается по цвету и остаётся на ценах базового размера — осознанный остаток.
//
// invalid = «замороженные данные этой партии не оценивают»; читатель обязан вернуться к скаляру
// релиза, а не к живой карточке. Края:
//
//  1. Линий нет (или все с неположительным количеством) — заголовок прогона планируют до того, как
//     заполнят грид. Скаляр релиза и есть цифра стиля, то есть ровно прежнее поведение.
//  2. Линия без колорвея (aux-цвет или единственный выход legacy aux-карточки) — замороженной цены
//     цвета у неё нет, а подставить «первый колорвей» это и есть исходный дефект. Вся партия
//     остаётся на скаляре: у aux-карточки скаляр и так её единственная цена.
//  3. Колорвей, которого в замороженных ценах нет (продукт привязали к стилю ПОСЛЕ релиза, либо
//     цена колорвея помечена неполной), — обнуляет ВСЮ партию, а не выпадает из знаменателя:
//     усреднение по посчитавшимся клеткам оценило бы остальные изделия в ноль.
func ComputeReleaseRunPlannedUnitCost(costs *ReleaseFrozenCosts, lines []entity.ProductionRunLine) (decimal.NullDecimal, string) {
	if costs == nil || len(costs.UnitCostByColorway) == 0 {
		return decimal.NullDecimal{}, ""
	}
	weighted := decimal.Zero
	totalQty := int64(0)
	for _, ln := range lines {
		if ln.PlannedQty <= 0 {
			continue
		}
		if !ln.ProductId.Valid || ln.ProductId.Int32 <= 0 {
			return decimal.NullDecimal{}, "" // край 2
		}
		unit, ok := costs.UnitCostByColorway[int(ln.ProductId.Int32)]
		if !ok {
			return decimal.NullDecimal{}, "" // край 3
		}
		qty := int64(ln.PlannedQty)
		weighted = weighted.Add(unit.Mul(decimal.NewFromInt(qty)))
		totalQty += qty
	}
	if totalQty == 0 {
		return decimal.NullDecimal{}, "" // край 1
	}
	avg := roundMoney(weighted.Div(decimal.NewFromInt(totalQty)))
	if !avg.IsPositive() {
		return decimal.NullDecimal{}, ""
	}
	return decimal.NullDecimal{Decimal: avg, Valid: true}, costs.Currency
}
