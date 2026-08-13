package dto

import (
	"database/sql"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// ReleaseFrozenCosts — деньги релиза, и ТОЛЬКО деньги: во что утверждённая ревизия оценила одно
// изделие каждого колорвея. Ключ карты — colorway_id снапшота, он же product_id линии прогона:
// колорвей карточки И ЕСТЬ продукт (product.style_id = card, materials.go `c.id AS product_id`),
// та же идентичность, на которой стоит CutSpecCardFromReleaseSnapshot.
type ReleaseFrozenCosts struct {
	// Currency — валюта костинга релиза, одна на все цены ниже: колорвеи одной карточки считаются
	// одним костинг-блоком, у него ровно одна валюта.
	Currency string
	// UnitCostByColorway — СТИЛЕВАЯ цена ОДНОГО изделия колорвея (с T6 — по среднему по размерному
	// ряду; у старых релизов — по норме базового размера той эпохи), включая ручные статьи
	// (CMT/логистика/накладные) и брак — то же содержимое, что у скаляра релиза, просто по каждому
	// цвету отдельно. Размера в этой цифре НЕТ — поэтому размерная клетка партии считается не по
	// ней, а по CostingCard, и сюда возвращается только фолбэком (см.
	// ComputeReleaseRunPlannedUnitCost).
	UnitCostByColorway map[int]decimal.Decimal
	// CostingCard — замороженная КОСТИНГ-проекция снапшота: ровно те входы, которые читает
	// пер-размерная цена клетки (нормы по размерам, цены строк BOM, пины, ручные статьи, брак).
	// По ней размерная клетка релизного прогона ценится ТЕМ ЖЕ правилом, что у живой ветки
	// (runCellUnitCost), но на замороженных входах. nil, когда снапшот не читается — клетки тогда
	// сразу падают в фолбэк по UnitCostByColorway.
	CostingCard *entity.TechCard
}

// ReleaseFrozenColorwayCosts достаёт из замороженного блоба релиза цену изделия ПО КАЖДОМУ
// КОЛОРВЕЮ — посчитанную в момент релиза и замороженную вместе со спецификацией.
//
// ПОЧЕМУ ЭТО ПОЛЕ НУЖНО РЯДОМ С ПЕРЕСЧЁТОМ ПО ЗАМОРОЖЕННЫМ ВХОДАМ. Блоб несёт нормы (в том
// числе по размерам), цены строк BOM и ПИНЫ рецептов — то есть КАКОЙ артикул колорвей берёт в
// слот, — но не несёт ЦЕНУ пришпиленного артикула: она живёт в каталоге материалов и
// подставляется на лету (pinShadowBom читает её из TechCard.LinkedMaterials, а LinkedMaterials в
// контракте TechCard нет вовсе). Поэтому пересчёт по блобу (CostingCard — им размерная клетка
// партии ценится по СВОЕМУ размеру) оставляет пришпиленную строку БЕЗ цены, то есть по правилу
// «непосчитанная клетка — непосчитанная партия» обнулил бы весь прогон именно на тех карточках,
// ради которых пины и заведены. А взять цену пина из СЕГОДНЯШНЕГО каталога значило бы разморозить
// цену релиза: она поехала бы от переоценки склада, и «сегодня» разошлось бы со снимком на
// прогоне, у которого не менялось ничего. costing.colorway_costs — единственное место, где цена
// пина УЖЕ заморожена: она посчитана каталогом на момент релиза и с тех пор не двигается — и
// потому остаётся фолбэком партии, когда размерный пересчёт из блоба невозможен.
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
	//
	// «РЕЦЕПТ» ЗДЕСЬ — ПО ТОМУ ЖЕ ПРЕДИКАТУ, ЧТО У ЖИВОГО ПУТИ (T8): строка-назначение детали
	// рецептом не считается. Колорвей, у которого в снапшоте остались ОДНИ такие строки, считался
	// бы «авторским» и брал бы свою замороженную проекцию без ткани — ровно то тихое занижение,
	// от которого этот блок защищает.
	authored := make(map[int]bool, len(snap.GetColorways()))
	for _, cw := range snap.GetColorways() {
		for _, u := range cw.GetUsages() {
			if pbUsageIsPieceAssignment(u) {
				continue
			}
			authored[int(cw.GetColorwayId())] = true
			break
		}
	}
	// Цена стиля = корень костинга (первый колорвей), и наследовать её можно, только если она сама
	// полна — те же флаги, что и у колорвея.
	//
	// has_estimate ЗДЕСЬ ОБЯЗАТЕЛЕН (Ф1). Замороженная цена релиза — это то, против чего меряется вся
	// последующая разница плана и факта; вморозив в неё netto-оценку, мы бы получили базу, занижённую
	// на все межлекальные выпады, и каждое отклонение считалось бы от неё как от факта. Слепок,
	// сделанный до Ф1, флага не несёт и читается как раньше — protojson отдаёт false.
	style, err := nullDecimalFromPb(costing.GetUnitCost())
	styleOK := err == nil && style.Valid && style.Decimal.IsPositive() &&
		!costing.GetHasUnpriced() && !costing.GetHasUnconvertedCurrencies() && !costing.GetHasEstimate()

	out := &ReleaseFrozenCosts{Currency: currency, UnitCostByColorway: make(map[int]decimal.Decimal, len(costing.GetColorwayCosts()))}
	for _, cc := range costing.GetColorwayCosts() {
		id := int(cc.GetColorwayId())
		if id <= 0 || cc.GetHasUnpriced() || cc.GetHasUnconvertedCurrencies() || cc.GetHasEstimate() {
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
	out.CostingCard = releaseCostingCardFromSnapshot(snap)
	return out
}

// releaseCostingCardFromSnapshot — замороженная КОСТИНГ-проекция блоба релиза: РОВНО те поля,
// которые читает пер-размерная цена клетки (runCellUnitCost → ComputeColorwayUnitCost →
// colorwayCost → entity.UnitTotal): размерный ряд, строки BOM с ценой/валютой/процентом кроя,
// рецепты с нормами (в т.ч. по размерам), пины и привязки к деталям (предикат
// IsPieceMaterialAssignment обязан узнать строку-назначение), ручные статьи и брак. Ни медиа, ни
// операций, ни градации — частичный читатель с общим именем рано или поздно был бы вызван ради
// поля, которого не заполняет (тот же довод, что у CutSpecCardFromReleaseSnapshot; читать блоб
// через ConvertPbTechCardInsertToEntity нельзя по той же причине — тот валидирует историю).
//
// LinkedMaterials НЕ заполняются НАМЕРЕННО: цены пинов живут в каталоге материалов, в блобе их
// нет, а подставить сегодняшний каталог значило бы разморозить цену релиза. Строка с пином,
// отличным от умолчания слота, поэтому честно остаётся без цены (pinShadowBom с пустым linked), и
// клетка такого колорвея не ценится по размеру — это документированный предел снапшота, а не
// упущение читателя.
//
// КОЭФФИЦИЕНТ РАСКРОЯ — ИСКЛЮЧЕНИЕ ИЗ ЭТОГО ПРАВИЛА, И ОНО ЗАРАБОТАНО (W3). Он тоже свойство
// каталога, но с W3 входит в ДЕНЬГИ нормы, поэтому его отсутствие здесь не оставляло строку без
// цены, а тихо занижало её — и заниженная клетка ПРЕДПОЧИТАЛАСЬ замороженному скаляру, который
// коэффициент содержит. Предел, который код молча предпочитает, — не предел, а дефект. Поэтому
// коэффициент ЗАМОРОЖЕН В САМОМ БЛОБЕ (common.TechCardBomItem.cutting_coefficient), и читается он
// тремя состояниями: задан — считаем им; задан единицей — «надбавки нет»; ОТСУТСТВУЕТ (снапшот
// старше заморозки) — коэффициент неизвестен, и рулонная строка остаётся БЕЗ ЦЕНЫ, то есть уходит
// в тот же честный фолбэк, что и пин. Цену пина это не чинит и чинить не должно: она размораживала
// бы релиз, а коэффициент — нет, он у артикула один и в блобе он теперь свой.
//
// Терпимость к истории: нечитаемое decimal-значение читается как отсутствующее (строка тогда не
// ценится и уводит расчёт в фолбэк) — блоб заморожен со схемой своей эпохи, и «строка старая» не
// имеет права превращаться в «прогон не открывается».
func releaseCostingCardFromSnapshot(snap *pb_common.TechCard) *entity.TechCard {
	ins := snap.GetTechCard()
	if ins == nil {
		return nil
	}
	card := &entity.TechCard{Id: int(snap.GetId())}
	for _, id := range ins.GetSizeIds() {
		card.SizeIds = append(card.SizeIds, int(id))
	}
	for _, b := range ins.GetBomItems() {
		item := entity.TechCardBomItem{
			Id:      int(b.GetId()),
			LineKey: b.GetLineKey(),
			Name:    b.GetName(),
			// SECTION ЕДЕТ В ВОССТАНОВЛЕННУЮ СТРОКУ, и без неё эта проекция была тихо сломана: вся
			// граница рулонных секций (entity.EffectiveCuttingCoefficient) читает именно её, а
			// пустая секция не рулонная — то есть коэффициент не применился бы НИ К ОДНОЙ строке
			// блоба, сколько его туда ни заморозь.
			Section:        techCardBomSectionPbToEntity[b.GetSection()],
			Unit:           sql.NullString{String: b.GetUnit(), Valid: b.GetUnit() != ""},
			UnitPrice:      frozenDecimal(b.GetUnitPrice()),
			Currency:       sql.NullString{String: b.GetCurrency(), Valid: b.GetCurrency() != ""},
			WastagePercent: frozenDecimal(b.GetWastagePercent()),
		}
		if mid := b.GetMaterialId(); mid > 0 {
			item.MaterialId = sql.NullInt64{Int64: mid, Valid: true}
		}

		// ЗАМОРОЖЕННЫЙ КОЭФФИЦИЕНТ — ТРИ СОСТОЯНИЯ, И ТРЕТЬЕ ОТКАЗЫВАЕТ.
		//
		// Поле ЗАДАНО (в т.ч. явной единицей) — величина авторитетна, строка считается ею.
		// Поле ОТСУТСТВУЕТ — снапшот снят до заморозки, и коэффициент НЕИЗВЕСТЕН. Посчитать такую
		// рулонную строку «как есть» значило бы выдать полное на вид число, заниженное ровно на
		// коэффициент, — и пер-размерный путь ПРЕДПОЧЁЛ бы его замороженному скаляру, который
		// коэффициент содержит. Поэтому строка остаётся БЕЗ ЦЕНЫ: UnitTotal её пропустит, колорвей
		// станет непосчитанным, и клетка честно уедет в фолбэк на скаляр релиза — ровно то
		// поведение, которое у этой проекции уже есть для непроставленной цены пина.
		//
		// НЕрулонная строка отказа не требует: коэффициент к ней не применяется ни в каком случае,
		// поэтому его неизвестность ничего о её деньгах не меняет.
		if c := frozenDecimal(b.GetCuttingCoefficient()); c.Valid {
			item.CuttingCoefficient = c
		} else if entity.IsRollGoodsSection(item.Section) {
			item.UnitPrice = decimal.NullDecimal{}
			item.Currency = sql.NullString{}
		}
		card.BomItems = append(card.BomItems, item)
	}
	if c := ins.GetCosting(); c != nil {
		ccy := strings.TrimSpace(c.GetCurrency())
		card.Costing = &entity.TechCardCosting{
			Currency:      sql.NullString{String: ccy, Valid: ccy != ""},
			CmtCost:       frozenDecimal(c.GetCmtCost()),
			LogisticsCost: frozenDecimal(c.GetLogisticsCost()),
			OverheadCost:  frozenDecimal(c.GetOverheadCost()),
			DefectPercent: frozenDecimal(c.GetDefectPercent()),
		}
	}
	for _, cw := range snap.GetColorways() {
		out := entity.TechCardColorway{Id: int(cw.GetColorwayId())}
		if id := cw.GetColorwayId(); id > 0 {
			// Колорвей карточки И ЕСТЬ продукт (materials.go `c.id AS product_id`) — та же
			// идентичность, на которой стоит весь этот файл.
			out.ProductId = sql.NullInt32{Int32: id, Valid: true}
		}
		for _, u := range cw.GetUsages() {
			use := entity.TechCardColorwayUsage{
				PieceLineKey: u.GetPieceLineKey(),
				Consumption:  frozenDecimal(u.GetConsumption()),
				Quantity:     frozenDecimal(u.GetQuantity()),
			}
			if id := u.GetBomItemId(); id > 0 {
				use.BomItemId = sql.NullInt64{Int64: id, Valid: true}
			}
			if u.BomItemIndex != nil {
				use.BomItemIndex = sql.NullInt32{Int32: *u.BomItemIndex, Valid: true}
			}
			if id := u.GetPieceId(); id > 0 {
				use.PieceId = sql.NullInt64{Int64: id, Valid: true}
			}
			if u.PieceIndex != nil {
				// present = привязка, включая индекс 0 — см. pbUsageIsPieceAssignment.
				use.PieceIndex = sql.NullInt32{Int32: *u.PieceIndex, Valid: true}
			}
			if u.MaterialId != nil && *u.MaterialId > 0 {
				use.MaterialId = sql.NullInt64{Int64: *u.MaterialId, Valid: true}
			}
			if src := u.GetConsumptionSource(); src != "" {
				// marker-строки не гроссируются повторно (wastageApplies) — источник обязан пережить
				// заморозку, иначе замороженная клетка вышла бы дороже живой на процент кроя.
				use.ConsumptionSource = sql.NullString{String: src, Valid: true}
			}
			for _, sc := range u.GetSizeConsumptions() {
				n := frozenDecimal(sc.GetConsumption())
				if !n.Valid {
					continue // нечитаемая норма = нормы нет; клетка этого размера уйдёт в фолбэк
				}
				use.SizeConsumptions = append(use.SizeConsumptions, entity.TechCardBomSizeConsumption{
					SizeId: int(sc.GetSizeId()), Consumption: n.Decimal,
				})
			}
			out.Usages = append(out.Usages, use)
		}
		card.Colorways = append(card.Colorways, out)
	}
	return card
}

// frozenDecimal читает decimal из замороженного блоба ТЕРПИМО: нечитаемое значение — отсутствующее
// (история не валидируется), и расчёт, которому оно было нужно, честно не считается, вместо того
// чтобы уронить чтение прогона.
func frozenDecimal(d *pb_decimal.Decimal) decimal.NullDecimal {
	v, err := nullDecimalFromPb(d)
	if err != nil {
		return decimal.NullDecimal{}
	}
	return v
}

// pbUsageIsPieceAssignment — зеркало entity.TechCardColorwayUsage.IsPieceMaterialAssignment для
// проводной/снапшотной формы строки рецепта. Все три представления привязки, потому что снапшот
// несёт не все: у замороженного релиза может выжить только piece_line_key (id детали в контракте
// нет вовсе — см. CutSpecCardFromReleaseSnapshot), у легаси-строки — только позиционный
// piece_index (present = привязка, включая индекс 0).
func pbUsageIsPieceAssignment(u *pb_common.TechCardColorwayUsage) bool {
	return u.GetPieceId() > 0 || u.GetPieceLineKey() != "" || u.PieceIndex != nil
}

// ComputeReleaseRunPlannedUnitCost — плановая цена изделия РЕЛИЗНОГО прогона: клетки
// (колорвей, размер) СОБСТВЕННЫХ линий партии, оценённые по замороженным входам релиза и
// взвешенные её же количествами —
//
//	Σ(unit_cost_at(line.product_id, line.size_id) × line.planned_qty) ÷ Σ line.planned_qty
//
// ЗАЧЕМ. Релизный прогон ценился ОДНИМ скаляром релиза, а тот — цена ПЕРВОГО колорвея карточки
// (ComputeTechCardUnitCost). Партия из 50 чёрных по €10 и 50 белых по €30 снимала €10 вместо
// €20, которые она собирается потратить, и вся plan/fact вариация партии ехала от этого на 100%.
// Пины существуют ровно затем, чтобы колорвеи стоили по-разному; одна колонка planned_unit_cost
// значит не «один цвет», а «одно ВЗВЕШЕННОЕ число», и веса лежат на линиях прогона.
//
// РАЗМЕР КЛЕТКИ СЧИТАЕТСЯ ПО РАЗМЕРУ — тем же правилом, что у живой ветки. Стилевая цена стала
// средним по размерному ряду (T6), и партия обязана НЕ шевельнуться от этого: партия из одних M
// стоит по нормам M и в релизной ветке тоже, а не по среднему ряда. Поэтому клетка ценится
// runCellUnitCost — ОДНИМ правилом клетки на обе ветки, не второй копией — по CostingCard,
// замороженной костинг-проекции блоба: пустой CostingFx (ничего не сворачивается, цифра остаётся
// в валюте костинга релиза) и без фактического процента кроя (см. ниже). Линия без размера и тут
// считается БЕЗ базиса: градуированная норма не ценится (среднее ей не подставляется — ловушка
// T6), плоская ценится как всегда.
//
// ВСЕ ВХОДЫ ЗАМОРОЖЕНЫ. Ни живой карточки, ни каталога материалов, ни курсов: только блоб релиза
// и линии прогона. Поэтому снимок (snapshotPlannedCost) и «сегодня» (plannedUnitCostToday)
// совпадают по построению, и бейдж «карточка изменилась» на релизном прогоне молчит — ровно как
// молчал, когда там стоял скаляр.
//
// ЧЕГО ЗАМОРОЖЕННЫЕ ВХОДЫ НЕ УМЕЮТ — ЦЕН ПИНОВ. Блоб несёт, КАКОЙ артикул колорвей пришпилил, но
// не его цену (она в каталоге материалов — см. ReleaseFrozenColorwayCosts), поэтому клетка
// колорвея с пином по размеру не ценится. Тогда — и только целиком — партия падает на прежнюю
// формулу: замороженные стилевые цены колорвеев (UnitCostByColorway), взвешенные линиями. Фолбэк
// НАМЕРЕННО всё-или-ничего по прогону: посчитать часть клеток по размеру, а часть по стилевой
// цене значило бы число, чей смысл зависит от того, какие колорвеи пришпилены, и никто бы этого
// не увидел. Размерная честность фолбэка — документированный предел снапшота: чтобы клетка с
// пином считалась по размеру, релизу пришлось бы замораживать и цену пина (её нет ни в блобе, ни
// в мете релиза).
//
// invalid = «замороженные данные этой партии не оценивают»; читатель обязан вернуться к скаляру
// релиза, а не к живой карточке. Края:
//
//  1. Линий нет (или все с неположительным количеством) — заголовок прогона планируют до того, как
//     заполнят грид. Скаляр релиза и есть цифра стиля, то есть ровно прежнее поведение. (У живой
//     ветки пустой прогон с T6 честно не ценится; релизная остаётся на скаляре, потому что «любая
//     непригодность возвращает прежнее поведение» — и это прежнее поведение целиком.)
//  2. Линия без колорвея (aux-цвет или единственный выход legacy aux-карточки) — по размеру она
//     ценится собственной цифрой карточки, только когда колорвеев не больше одного (правило
//     runCellUnitCost); иначе замороженной цены цвета у неё нет, а подставить «первый колорвей»
//     это и есть исходный дефект. Вся партия остаётся на скаляре.
//  3. Колорвей, которого нет ни в снапшоте, ни в замороженных ценах (продукт привязали к стилю
//     ПОСЛЕ релиза, либо цена колорвея помечена неполной), — обнуляет ВСЮ партию, а не выпадает
//     из знаменателя: усреднение по посчитавшимся клеткам оценило бы остальные изделия в ноль.
func ComputeReleaseRunPlannedUnitCost(costs *ReleaseFrozenCosts, lines []entity.ProductionRunLine) (decimal.NullDecimal, string) {
	if costs == nil || len(costs.UnitCostByColorway) == 0 {
		return decimal.NullDecimal{}, ""
	}
	// Пул количеств по клетке (колорвей, размер) — зеркало живой ветки: взвешиваются изделия, а не
	// строки грида, и одна клетка не ценится дважды.
	qtyByCell := make(map[runMixKey]int64, len(lines))
	cellOrder := make([]runMixKey, 0, len(lines))
	totalQty := int64(0)
	for _, ln := range lines {
		if ln.PlannedQty <= 0 {
			continue
		}
		k := runMixKey{sizeID: ln.SizeId}
		if ln.ProductId.Valid {
			k.productID = int(ln.ProductId.Int32)
		}
		if _, seen := qtyByCell[k]; !seen {
			cellOrder = append(cellOrder, k)
		}
		qtyByCell[k] += int64(ln.PlannedQty)
		totalQty += int64(ln.PlannedQty)
	}
	if totalQty == 0 {
		return decimal.NullDecimal{}, "" // край 1
	}

	// Проход по размерам: каждая клетка — по норме СВОЕГО размера из замороженной карточки.
	if unit, ok := releaseRunSizedUnitCost(costs, cellOrder, qtyByCell, totalQty); ok {
		return unit, costs.Currency
	}

	// Фолбэк (всё-или-ничего): замороженные стилевые цены колорвеев, взвешенные линиями — прежняя
	// формула, для блобов, по которым размерная клетка не считается (пины, легаси-формы).
	weighted := decimal.Zero
	for _, cell := range cellOrder {
		if cell.productID <= 0 {
			return decimal.NullDecimal{}, "" // край 2
		}
		unit, ok := costs.UnitCostByColorway[cell.productID]
		if !ok {
			return decimal.NullDecimal{}, "" // край 3
		}
		weighted = weighted.Add(unit.Mul(decimal.NewFromInt(qtyByCell[cell])))
	}
	avg := roundMoney(weighted.Div(decimal.NewFromInt(totalQty)))
	if !avg.IsPositive() {
		return decimal.NullDecimal{}, ""
	}
	return decimal.NullDecimal{Decimal: avg, Valid: true}, costs.Currency
}

// releaseRunSizedUnitCost — размерный проход релизной партии: каждая клетка (колорвей, размер)
// ценится runCellUnitCost — ТЕМ ЖЕ правилом клетки, что у живой ветки, — по замороженной
// костинг-карточке релиза. Пустой CostingFx: курсы — живой вход, сворачивание ими разморозило бы
// цену, поэтому всё остаётся в валюте костинга релиза (она и есть costs.Currency; сверка — на
// случай битого блоба, зеркало края валют живой ветки). Без фактического процента кроя — он не
// входит в замороженные цены ровно как не входил в скаляр (см. releaseRunPlannedUnitCost).
//
// ok=false — «по размеру эта партия из блоба не считается» (клетка не оценилась: пин без цены в
// блобе, нет нормы на размере клетки, легаси-блоб без рецептов), и вызывающий уходит в фолбэк по
// стилевым ценам колорвеев ЦЕЛИКОМ — частичная размерность запрещена, см. заголовок формулы.
func releaseRunSizedUnitCost(costs *ReleaseFrozenCosts, cellOrder []runMixKey, qtyByCell map[runMixKey]int64, totalQty int64) (decimal.NullDecimal, bool) {
	if costs.CostingCard == nil {
		return decimal.NullDecimal{}, false
	}
	weighted := decimal.Zero
	for _, cell := range cellOrder {
		unit, ccy := runCellUnitCost(costs.CostingCard, CostingFx{}, decimal.NullDecimal{}, cell)
		if !unit.Valid || !strings.EqualFold(strings.TrimSpace(ccy), strings.TrimSpace(costs.Currency)) {
			return decimal.NullDecimal{}, false
		}
		weighted = weighted.Add(unit.Decimal.Mul(decimal.NewFromInt(qtyByCell[cell])))
	}
	avg := roundMoney(weighted.Div(decimal.NewFromInt(totalQty)))
	if !avg.IsPositive() {
		return decimal.NullDecimal{}, false
	}
	return decimal.NullDecimal{Decimal: avg, Valid: true}, true
}
