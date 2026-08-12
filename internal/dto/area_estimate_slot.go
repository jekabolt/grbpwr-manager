package dto

import (
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ОЦЕНКА СЛОТА ПО ПЛОЩАДИ (Ф1, ступень 0) — мост между геометрией и деньгами.
//
// Отвечает на один вопрос: сколько стоит рулонный слот в этом колорвее, если норму никто не вписал,
// но детали на него назначены и площади измерены.
//
// ЧТО ЭТО МЕНЯЕТ. До сих пор такой слот стоил НОЛЬ и молчал: строка рецепта, привязанная к детали,
// нормы не несёт (T8), а другой строки на слоте нет. Карточка с полной спецификацией, ценами и
// разобранными выкройками показывала «себестоимость ещё не посчитана».
//
// ЧЕГО ЭТО НЕ ДАЁТ ПРАВА ДЕЛАТЬ. Число — NETTO, нижняя граница: межлекальных выпадов и концов
// настила в нём нет. Оно показывается, но НЕ СЕЕТ каталожную себестоимость (см. costTierEstimate и
// его читателей) — занижение здесь измеряется десятками процентов.
//
// ДВЕ СТУПЕНИ ПУБЛИКАЦИИ, ОДНА СТУПЕНЬ ДЕНЕГ. Та же арифметика отвечает и на второй вопрос —
// «а насколько эта нижняя граница ниже нормы, которую вписал человек» (colorwayShadowAreaEstimates),
// — и без ответа на него первое число остаётся цифрой, про которую неизвестно, насколько она врёт.
// Множество, которое ПУБЛИКУЕТСЯ, поэтому шире множества, которое СЧИТАЕТСЯ: в деньги входит только
// слот без авторской нормы, ровно как и до этой врезки.

// slotEstimate — ПОЛНЫЙ ответ про один рулонный слот одного колорвея: сколько он съедает на изделие
// по оценке, в какой единице, во что это обходится — а когда ответа нет, какого именно факта не
// хватает и на каком замере ответ стоял бы.
//
// ОДНА СТРУКТУРА НА НОРМУ И НА ДЕНЬГИ, И В ЭТОМ ВЕСЬ СМЫСЛ. Рецепт печатает расход на изделие,
// костинг печатает цену; посчитай их двумя вызовами с двумя наборами аргументов — и рано или поздно
// на экране окажется норма от одного базиса под ценой от другого. Разойдутся они ровно на весь
// градационный разброс (стилевая цена считается по СРЕДНЕМУ размерного ряда, а не по одному
// размеру) и ничем себя не выдадут: обе цифры выглядят правдоподобно. Поэтому money здесь —
// perGarment × цена, в том же проходе, а наружу отдаются оба поля ОДНОГО ответа.
type slotEstimate struct {
	// bomLineKey — стабильный ключ строки BOM, по которому клиент сшивает оценку со своей строкой.
	// Не id: в слепке релиза у части строк его нет, а ключ есть всегда.
	bomLineKey string
	// scopeKey — скоуп площадей, на которых стоит ответ: COALESCE(назначение, line_key), тот же
	// ключ, что у TechCard.piece_area_scopes.
	scopeKey string

	// ok=true — ЕСТЬ ДЕНЬГИ: perGarment/unit/money/currency заполнены. ok=false — refusal называет
	// недостающий факт.
	ok      bool
	refusal entity.AreaEstimateRefusal

	// normDerived=true — есть ГЕОМЕТРИЯ: perGarment посчитан. Отдельно от ok намеренно: расход —
	// вопрос про выкройки и ширину рулона, и у него есть ответ даже там, где у артикула нет цены.
	// Читателю, который спрашивает «есть ли у слота оценка расхода» (материальный план), нужен
	// именно этот флаг: иначе смена валюты костинга меняла бы предписанное оператору действие, не
	// меняя ни одной выкройки. normDerived=true при ok=false — законное состояние (нет цены).
	normDerived bool

	// perGarment — норма на ИЗДЕЛИЕ в ЕДИНИЦЕ СЛОТА, на базисе костинга (у стиля — среднее по
	// объявленному размерному ряду). Значима при normDerived.
	perGarment decimal.Decimal
	unit       string
	money      decimal.Decimal
	currency   string

	// pieceCount — сколько деталей кроя колорвей назначил на этот слот. Ноль — это и есть причина
	// no_pieces_assigned; ненулевой при отказе означает «назначения есть, не хватает другого».
	pieceCount int

	// Провенанс замера, на котором ответ стоит ИЛИ СТОЯЛ БЫ. Заполняется до любого отказа: «площади
	// сняты 3 августа по слою кроя, и они устарели» — это ответ, а пустая строка — нет.
	measured        bool
	stale           bool
	contourLayer    string
	seamAllowanceMm decimal.Decimal
	parsedBy        string
	parsedAt        time.Time
}

// slotAreaEstimate answers «what does this one roll slot consume per garment by estimate, and why
// not» — the single place that assembles the arguments of entity.AreaEstimateNorm and the only
// caller of it. Money is derived here from the answer, never computed beside it.
//
// The basis follows the same three-valued rule as every other costing path: one concrete size for a
// run cell, the simple average over the declared range for the style figure, and NOTHING when there
// is no basis — a size-graded answer with no size to give it at is not an approximation, it is an
// absence.
func slotAreaEstimate(
	tc *entity.TechCard,
	cw *entity.TechCardColorway,
	bom *entity.TechCardBomItem,
	linked map[int]entity.MaterialWithPrice,
	basis entity.CostingBasis,
	baseCcy string,
) slotEstimate {
	if tc == nil || cw == nil || bom == nil || !isRollGoodsSection(bom.Section) {
		// Пустой refusal = «оценка к этому слоту неприменима вовсе», не «не посчиталась»: у нитки
		// площади нет ни в каком смысле, и назвать причину значило бы обещать, что причину можно
		// устранить.
		return slotEstimate{}
	}
	scope := entity.FabricScopeKey(bom.Purpose.String, bom.LineKey)
	out := slotEstimate{
		bomLineKey: bom.LineKey,
		scopeKey:   scope,
		// ЕДИНИЦА — СВОЙСТВО СЛОТА, А НЕ ПИНА, поэтому её можно назвать до разбора цены: пин-тень
		// (pinShadowBom) копирует строку и подменяет только цену с валютой, а расхождение единиц —
		// ровно то, из-за чего она ОТКАЗЫВАЕТСЯ ценить строку. Единица слота и единица пина в
		// посчитанном ответе поэтому всегда одна и та же.
		unit: bom.Unit.String,
	}
	if sc, ok := tc.PieceAreaScopes[scope]; ok && len(sc.Rows) > 0 {
		out.measured = true
		out.stale = sc.Stale
		// Условия и провенанс — одни на скоуп (их пишет одна транзакция), читаются с первой строки
		// ровно как в TechCardPieceAreaScopesToPb; второго правила чтения тех же полей не заводим.
		out.contourLayer = sc.Rows[0].ContourLayer
		out.seamAllowanceMm = sc.Rows[0].SeamAllowanceMm
		out.parsedBy = sc.Rows[0].ParsedBy
		out.parsedAt = sc.Rows[0].ParsedAt
	}

	pieces, pinned, pinConflict := slotAssignedPieces(tc, cw, bom)
	out.pieceCount = len(pieces)
	if pinConflict {
		// СПОРЯЩИЕ ПИНЫ — ЭТО НЕ «ДЕТАЛЕЙ НЕТ». Раньше конфликт возвращался пустым списком и читался
		// как no_pieces_assigned, то есть отправлял оператора назначать детали, которые уже
		// назначены, — а починить надо артикул. Причина существовала в словаре и не доезжала ни до
		// одного экрана.
		out.refusal = entity.AreaEstimatePinConflict
		return out
	}
	if len(pieces) == 0 {
		out.refusal = entity.AreaEstimateNoAssignments
		return out
	}
	// ГЕОМЕТРИЯ СЧИТАЕТСЯ ДО ЦЕНЫ, И ЭТО НЕ ПЕРЕСТАНОВКА РАДИ КРАСОТЫ. «Сколько ткани съедает
	// изделие» — вопрос про выкройки и ширину рулона; он имеет ответ и тогда, когда у артикула нет
	// цены. Читатели, которым нужна ИМЕННО норма (материальный план: «есть только оценка по площади»
	// против «нормы расхода нет»), обязаны получать ответ, не зависящий от валютной лестницы, —
	// иначе смена валюты костинга меняет предписанное оператору действие, ничего не меняя в цехе.
	//
	// ЛЕСТНИЦА ОТКАЗОВ ПРИ ЭТОМ НЕ ТРОНУТА: «нет цены» по-прежнему называется РАНЬШЕ геометрических
	// причин (см. ниже), поэтому деньги и флаги костинга ведут себя ровно как раньше.
	widthCm := slotCuttingWidthCm(bom, pinned, linked)
	perGarment, normRefusal := areaEstimateOnBasis(scope, pieces, tc.PieceAreaScopes, widthCm, out.unit, basis)
	if normRefusal == "" {
		out.perGarment, out.normDerived = perGarment, true
	}

	// ЦЕНА И ШИРИНА БЕРУТСЯ У ОДНОГО И ТОГО ЖЕ АРТИКУЛА. Пин колорвея меняет и то и другое разом:
	// посчитать длину по ширине слота, а деньги по цене пина (или наоборот) значило бы описать
	// рулон, которого нет ни на складе, ни в спецификации.
	// ТА ЖЕ ВАЛЮТНАЯ ЛЕСТНИЦА, ЧТО У АВТОРСКОЙ НОРМЫ. Пин выбирает цену через
	// LatestPriceForCurrencies(валюта костинга, базовая); передать сюда пустую базовую значило бы,
	// что на артикуле с ценой только в базовой валюте авторская строка считается, а оценка молча
	// отказывает — две разные правды об одном рулоне.
	priced := pinShadowBomForArticle(bom, pinned, linked, currencyOfCosting(tc), baseCcy)
	if priced == nil || !priced.UnitPrice.Valid {
		// НАЗНАЧЕНИЯ ЕСТЬ, А ЦЕНЫ НЕТ — это «строка без цены», а не «оценивать нечего»: слот входит
		// в изделие, но не входит ни в один итог. Молчаливый пропуск здесь и есть тот самый способ
		// опубликовать себестоимость с недостающим материалом.
		out.refusal = entity.AreaEstimateNoPrice
		return out
	}
	if normRefusal != "" {
		out.refusal = normRefusal
		return out
	}
	// ПРОЦЕНТ РАСКРОЯ СЛОТА НЕ НАЧИСЛЯЕТСЯ. Оценка объявлена нижней границей и показывается как
	// таковая; догрузив её процентом, мы получили бы число, которое выглядит как норма, считается
	// как норма и при этом ею не является. Ступень наверх — только через раскладку.
	out.money = perGarment.Mul(priced.UnitPrice.Decimal)
	out.currency = priced.Currency.String
	out.ok = true
	return out
}

// areaEstimateOnBasis сводит пер-размерную норму к ОДНОМУ числу на изделие по правилу базиса —
// конкретный размер для клетки прогона, простое среднее по объявленному ряду для стилевой цифры,
// и НИЧЕГО, когда базиса нет.
func areaEstimateOnBasis(
	scope string,
	pieces []entity.AreaEstimatePiece,
	areas map[string]entity.PieceAreaScope,
	widthCm decimal.NullDecimal,
	unit string,
	basis entity.CostingBasis,
) (decimal.Decimal, entity.AreaEstimateRefusal) {
	norm := func(sizeID int) (decimal.Decimal, entity.AreaEstimateRefusal) {
		return entity.AreaEstimateNorm(scope, pieces, areas, widthCm, unit, sizeID)
	}
	var perGarment decimal.Decimal
	switch basis.Mode {
	case entity.CostingBasisSize:
		n, r := norm(basis.SizeID)
		if r != "" {
			return decimal.Zero, r
		}
		perGarment = n
	case entity.CostingBasisRangeAverage:
		if len(basis.RangeSizeIds) == 0 {
			return decimal.Zero, entity.AreaEstimateIncomplete
		}
		sum := decimal.Zero
		for _, sid := range basis.RangeSizeIds {
			n, r := norm(sid)
			if r != "" {
				// НЕТ УСРЕДНЕНИЯ ПО ПОДМНОЖЕСТВУ. Средняя по тем размерам, что посчитались, — это
				// ровно то систематическое занижение, ради устранения которого T6 менял базис.
				return decimal.Zero, r
			}
			sum = sum.Add(n)
		}
		perGarment = sum.Div(decimal.NewFromInt(int64(len(basis.RangeSizeIds))))
	default:
		// Базиса нет (строка прогона без размера): пер-размерная величина без размера — это
		// отсутствие ответа, а не ноль.
		return decimal.Zero, entity.AreaEstimateNoBasis
	}
	if !perGarment.IsPositive() {
		return decimal.Zero, entity.AreaEstimateIncomplete
	}
	return perGarment, ""
}

// colorwayAreaEstimates — оценки ВСЕХ рулонных слотов колорвея, у которых нет собственной строки
// рецепта, — АКТИВНАЯ ступень. ЕДИНСТВЕННЫЙ вход и для денег (colorwayCost складывает money), и для
// той же нормы на проводе (AdminColorwayRef.area_estimates): деньги — это perGarment × цена ОДНОГО
// и того же ответа, а не второй расчёт рядом.
//
// Обе ветки передают сюда одну и ту же четвёрку (tc.BomItems, tc.LinkedMaterials, tc.CostingBasis(),
// fx.Base) — собрать её иначе значит опубликовать норму от одного базиса под ценой от другого, и
// именно это равенство закреплено тестом TestPublishedNormIsTheNumberTheMoneyUsed.
//
// СЛОТ СО СВОЕЙ СТРОКОЙ СЮДА НЕ ПОПАДАЕТ. Оценка существует ровно там, где нормы никто не вписал;
// введённое человеком число всегда сильнее выведенного, иначе правка нормы вниз молча заменялась бы
// оценкой вверх. Именно поэтому оценка не может ехать НА строке рецепта — её там нет.
//
// ЭТО МНОЖЕСТВО — ДЕНЕЖНОЕ, и расширять его нельзя. Всё, что вернётся отсюда, складывается в
// стоимость колорвея, поднимает hasEstimate, попадает в чек-лист релиза и в замороженные флаги
// слепка. Справочная (теневая) цифра для слота С нормой существует — colorwayShadowAreaEstimates, —
// и она специально живёт в ДРУГОЙ функции, чтобы её нельзя было получить, вызвав эту.
func colorwayAreaEstimates(
	tc *entity.TechCard,
	cw *entity.TechCardColorway,
	bomItems []entity.TechCardBomItem,
	linked map[int]entity.MaterialWithPrice,
	basis entity.CostingBasis,
	baseCcy string,
) []slotEstimate {
	return colorwayAreaEstimatesOfTier(tc, cw, bomItems, linked, basis, baseCcy, false)
}

// colorwayShadowAreaEstimates — ТЕНЕВЫЕ оценки: те же самые рулонные слоты, но ровно те, у которых
// норма ЗАЯВЛЕНА человеком. Справочная цифра рядом с авторской нормой — не замена ей.
//
// ЗАЧЕМ ОНА ВООБЩЕ НУЖНА. Правило «введённое сильнее выведенного» правильное и остаётся, но у него
// есть цена: ни один ответ сервера до сих пор не нёс ОБЕ цифры про один слот, а значит расстояние
// между ними нельзя было показать нигде. А это и есть единственный способ узнать, чего стоит
// нижняя граница: «на этой ткани оценка занижала на 27%» — и потому на соседней карточке, где
// нормы ещё нет, к ней можно (или нельзя) относиться серьёзно. Без этого оценка остаётся числом,
// про которое неизвестно, насколько оно врёт.
//
// В ДЕНЬГИ НЕ ВХОДИТ НИЧЕМ. Возвращённые отсюда срезы НЕ складываются ни в какой итог, не поднимают
// hasEstimate, не блокируют посев product.cost_price и не называются в чек-листе релиза: у слота
// уже есть авторская норма, и именно она — цена. Публикуемое множество шире считаемого, и это
// разделение — весь смысл двух функций вместо одной с флажком у вызывающего.
//
// С ЧЕМ ТЕНЬ СРАВНИВАЮТ. Только с АВТОРСКОЙ НОРМОЙ ТОГО ЖЕ слота того же колорвея — её клиент берёт
// из строки рецепта по тому же bomLineKey. Разность двух ОЦЕНОК не значит ничего и здесь не
// считается: каждая прячет свой объём межлекальных выпадов, и знак такой разности бывает
// противоположен знаку правды.
func colorwayShadowAreaEstimates(
	tc *entity.TechCard,
	cw *entity.TechCardColorway,
	bomItems []entity.TechCardBomItem,
	linked map[int]entity.MaterialWithPrice,
	basis entity.CostingBasis,
	baseCcy string,
) []slotEstimate {
	return colorwayAreaEstimatesOfTier(tc, cw, bomItems, linked, basis, baseCcy, true)
}

// colorwayAreaEstimatesOfTier — общее тело обеих ступеней: ОДНА арифметика, один провенанс, один
// словарь отказов; ступени различаются РОВНО тем, заявлена ли у слота норма.
//
// Одно тело намеренно: развести их в две копии значило бы, что «оценка» на активном и на теневом
// экране однажды посчитается по-разному (разная ширина, разный базис, разная лестница пина) — и
// расхождение будет выглядеть как измеренная разница ступеней, хотя это разница реализаций.
//
// wantAuthored=false — активная ступень (слот БЕЗ авторской нормы), она же денежная.
// wantAuthored=true — теневая (слот С авторской нормой), она же чисто справочная.
func colorwayAreaEstimatesOfTier(
	tc *entity.TechCard,
	cw *entity.TechCardColorway,
	bomItems []entity.TechCardBomItem,
	linked map[int]entity.MaterialWithPrice,
	basis entity.CostingBasis,
	baseCcy string,
	wantAuthored bool,
) []slotEstimate {
	if tc == nil || cw == nil {
		return nil
	}
	authored := colorwayAuthoredSlots(cw, bomItems)
	out := make([]slotEstimate, 0, len(bomItems))
	for i := range bomItems {
		b := &bomItems[i]
		if authored[b.Id] != wantAuthored || !isRollGoodsSection(b.Section) {
			continue
		}
		out = append(out, slotAreaEstimate(tc, cw, b, linked, basis, baseCcy))
	}
	return out
}

// areaRefusalBlocksTheCost — ОДНО правило на вопрос «этот отказ занижает себестоимость».
//
// Слот, на который детали НАЗНАЧЕНЫ, входит в изделие. Если посчитать его не удалось — устарели
// выкройки, нет ширины, нет цены, спорят пины, нет базиса — итог занижен ровно на этот материал, и
// молчание публикует себестоимость с недостающей тканью.
//
// ДВА ИСКЛЮЧЕНИЯ, И ОБА — «слот сюда не относится», а не «слот сломан»: «деталей не назначено» и
// «площадей нет». Второе — НОРМАЛЬНОЕ состояние всякой ещё не измеренной карточки, то есть всех
// сегодняшних: подняв на нём флаг, мы остановили бы посев себестоимости у каждой из них
// (регрессия, которую поймал TestT8ReleaseFrozenAllPieceRowsColorwayInheritsStyle). Громким отказ
// становится там, где карточку ИЗМЕРИЛИ.
//
// Предикат вынесен затем, что у него теперь ДВА читателя: флаг hasUnpriced в костинге и чек-лист
// готовности к релизу, который называет те же слоты словами. Вторая копия однажды разошлась бы, и
// экран говорил бы «всё в порядке» о карточке, чью цену сервер отказался считать.
func areaRefusalBlocksTheCost(measured bool, r entity.AreaEstimateRefusal) bool {
	if !measured || r == "" {
		return false
	}
	return r != entity.AreaEstimateNoAssignments && r != entity.AreaEstimateNoAreas
}

// TechCardCostBlockers — слоты, ИЗ-ЗА КОТОРЫХ цена карточки сегодня не считается, названные словами
// оператора: «<ткань> (<цвет>): <причина>».
//
// ЗАЧЕМ ЭТО СУЩЕСТВУЕТ. Отказ оценки делает цену НЕПОСЧИТАННОЙ, но product.cost_price при этом
// остаётся тем, чем был: его читают бухгалтерия и COGS проданного, и обнулять его задним числом
// хуже, чем оставить. Значит карточка может уехать в релиз с каталожной ценой, которая больше
// ничему не соответствует, и до сих пор ни один экран этого не говорил: чек-лист релиза спрашивал
// только «заполнен ли костинг и есть ли валюта». Пустая строка здесь = «нечего сообщить».
//
// НЕ ТРОГАЕТ ДАННЫЕ И НЕ БЛОКИРУЕТ ЗАПИСЬ: чек-лист готовности по построению советует, а не
// запрещает (GetTechCardReadiness). Это про честность экрана.
func TechCardCostBlockers(tc *entity.TechCard, fx CostingFx) []string {
	if tc == nil || len(tc.Colorways) == 0 {
		return nil
	}
	measured := len(tc.PieceAreaScopes) > 0
	if !measured {
		return nil
	}
	nameByLineKey := make(map[string]string, len(tc.BomItems))
	for i := range tc.BomItems {
		b := &tc.BomItems[i]
		name := strings.TrimSpace(b.Name)
		if name == "" {
			name = b.LineKey
		}
		nameByLineKey[b.LineKey] = name
	}
	out := make([]string, 0, len(tc.Colorways))
	for i := range tc.Colorways {
		cw := &tc.Colorways[i]
		// ТЕ ЖЕ АРГУМЕНТЫ, ЧТО У ДЕНЕГ И У ПРОВОДА — см. colorwayAreaEstimates.
		for _, e := range colorwayAreaEstimates(tc, cw, tc.BomItems, tc.LinkedMaterials, tc.CostingBasis(), fx.Base) {
			if !areaRefusalBlocksTheCost(measured, e.refusal) {
				continue
			}
			fabric := nameByLineKey[e.bomLineKey]
			if fabric == "" {
				fabric = e.bomLineKey
			}
			colour := strings.TrimSpace(cw.Name)
			if colour == "" {
				colour = cw.ColorCode
			}
			out = append(out, fabric+" ("+colour+"): "+entity.AreaEstimateRefusalText(e.refusal))
		}
	}
	return out
}

// colorwayAuthoredSlots — слоты, у которых норма ЗАЯВЛЕНА (посчиталась или отказала): к ним оценка
// по площади не применяется. Одно правило на деньги и на публикацию — вторая копия предиката
// однажды разошлась бы, и слот получил бы и норму, и оценку разом.
//
// Строка-назначение детали (IsPieceMaterialAssignment) заявлением нормы НЕ является: она говорит
// «эта деталь кроится из этой ткани» и числа не несёт (T8) — ради таких слотов оценка и заведена.
func colorwayAuthoredSlots(cw *entity.TechCardColorway, bomItems []entity.TechCardBomItem) map[int]bool {
	authored := make(map[int]bool, len(cw.Usages))
	for i := range cw.Usages {
		u := &cw.Usages[i]
		if u.IsPieceMaterialAssignment() {
			continue
		}
		// resolveUsageBom, not bomItemAtIndex: a usage authored via bom_line_key carries no
		// positional index, and a nil bom here would leave its slot open to a second (estimated)
		// figure on top of the authored one.
		if src := resolveUsageBom(bomItems, u); src != nil {
			authored[src.Id] = true
		}
	}
	return authored
}

// slotAssignedPieces lists the cut pieces this colourway assigns to this slot, with the multiplicity
// the estimate multiplies by, the article the piece rows pin (0 = none), and whether those pins
// DISAGREE.
//
// ONE PREDICATE WITH THE MONEY: entity.IsPieceMaterialAssignment. The piece rows are exactly the
// statement «this piece is cut from that fabric»; a garment-level row would carry a norm and would
// have been costed before this function was ever reached.
func slotAssignedPieces(tc *entity.TechCard, cw *entity.TechCardColorway, bom *entity.TechCardBomItem) ([]entity.AreaEstimatePiece, int64, bool) {
	byID := make(map[int]*entity.TechCardPiece, len(tc.Pieces))
	for i := range tc.Pieces {
		byID[tc.Pieces[i].Id] = &tc.Pieces[i]
	}
	out := make([]entity.AreaEstimatePiece, 0, len(cw.Usages))
	seen := map[string]bool{}
	var pin int64
	pinAgrees := true
	// ВТОРОЙ ИСТОЧНИК НАЗНАЧЕНИЙ — tech_card_piece_material (С5). Живой поток пишет строку рецепта
	// («+ добавить материал к детали» на вкладке колорвеев), но карточки, заведённые через вкладку
	// деталей кроя, держат ту же связь на самой детали. Читать один источник значило бы, что часть
	// карточек остаётся с нулём при полностью заполненной спецификации — та же болезнь, от которой
	// лечит вся фаза, только на другой популяции.
	//
	// СТРОКА РЕЦЕПТА ПОБЕЖДАЕТ: она новее, она же несёт пин, и дублирование по детали снимается по
	// одному ключу (см. seen).
	appendPiece := func(pieceID int, pinnedMaterial int64) {
		p := byID[pieceID]
		if p == nil || strings.TrimSpace(p.LineKey) == "" {
			return
		}
		// ПИН УЧИТЫВАЕТСЯ ДАЖЕ У ПОВТОРНОЙ СТРОКИ. Дедупликация ниже снимает ПЛОЩАДЬ (один контур
		// нельзя посчитать дважды), но не мнение строки об артикуле: две строки на одну деталь,
		// назвавшие разные рулоны, — это тот же спор, что две детали с разными пинами, и раньше он
		// проходил молча, потому что вторую строку отбрасывали целиком.
		if pinnedMaterial > 0 {
			if pin == 0 {
				pin = pinnedMaterial
			} else if pin != pinnedMaterial {
				pinAgrees = false
			}
		}
		if seen[p.LineKey] {
			return // one piece, one contour: a second statement about it adds no cloth
		}
		seen[p.LineKey] = true
		out = append(out, entity.AreaEstimatePiece{LineKey: p.LineKey, PerGarment: p.PiecesPerGarment})
	}
	for i := range cw.Usages {
		u := &cw.Usages[i]
		if !u.IsPieceMaterialAssignment() {
			continue
		}
		if !u.BomItemId.Valid || int(u.BomItemId.Int64) != bom.Id {
			continue
		}
		appendPiece(int(u.PieceId.Int64), u.MaterialId.Int64)
	}
	// Второй источник — после первого, поэтому строка рецепта всегда выигрывает по seen.
	colorwayKey := cw.Id
	if cw.ProductId.Valid && cw.ProductId.Int32 > 0 {
		colorwayKey = int(cw.ProductId.Int32)
	}
	for i := range tc.Pieces {
		p := &tc.Pieces[i]
		for j := range p.Materials {
			m := &p.Materials[j]
			// Назначение материала детали живёт на КОНКРЕТНОМ колорвее; нулевой id означает «на всех»
			// и относится к этому тоже.
			if m.ColorwayID != 0 && m.ColorwayID != colorwayKey {
				continue
			}
			if !m.BomItemId.Valid || int(m.BomItemId.Int64) != bom.Id {
				continue
			}
			appendPiece(p.Id, 0)
		}
	}
	// Конфликт возвращается ОТДЕЛЬНЫМ признаком, а не пустым списком, как раньше. Две детали одного
	// слота, пришпиленные к РАЗНЫМ артикулам, — это не «оценка чуть неточна», это два разных рулона
	// под одним слотом, и молча выбрать один значило бы посчитать половину изделия по чужой цене и
	// чужой ширине. Но детали при этом НАЗНАЧЕНЫ: пустой список читался бы как «назначений нет» и
	// отправлял оператора чинить то, что в порядке, вместо артикула. pin при конфликте бессмыслен —
	// вызывающий обязан отказаться до его использования.
	return out, pin, !pinAgrees
}

// slotCuttingWidthCm resolves the CUTTING width: the pinned article's, else the slot's article's,
// else the BOM line's own snapshot. Selvedge is subtracted by UsableFabricWidthCm — and is never
// charged again as a percentage, it is already paid for by dividing by this width.
func slotCuttingWidthCm(bom *entity.TechCardBomItem, pinned int64, linked map[int]entity.MaterialWithPrice) decimal.NullDecimal {
	id := pinned
	if id == 0 && bom.MaterialId.Valid {
		id = bom.MaterialId.Int64
	}
	if id > 0 {
		if m, ok := linked[int(id)]; ok {
			if w := m.UsableFabricWidthCm(); w.Valid && w.Decimal.IsPositive() {
				return w
			}
		}
	}
	return bom.FabricWidth
}

// pinShadowBomForArticle resolves the BOM line as the PINNED article prices it, reusing the one
// pin-shadow rule rather than restating it. A zero pin (or a pin equal to the slot default) returns
// the line unchanged.
func pinShadowBomForArticle(bom *entity.TechCardBomItem, pinned int64, linked map[int]entity.MaterialWithPrice, costingCcy, baseCcy string) *entity.TechCardBomItem {
	if pinned == 0 {
		return bom
	}
	shadow := entity.TechCardColorwayUsage{}
	shadow.MaterialId.Int64, shadow.MaterialId.Valid = pinned, true
	return pinShadowBom(bom, &shadow, linked, costingCcy, baseCcy)
}

func currencyOfCosting(tc *entity.TechCard) string {
	if tc.Costing != nil && tc.Costing.Currency.Valid {
		return tc.Costing.Currency.String
	}
	return ""
}

// isRollGoodsSection: only cloth is cut from a contour. A thread or a button has an area in no sense
// this estimate could use, and offering one would put a number on a slot whose norm is countable.
func isRollGoodsSection(s entity.TechCardBomSection) bool {
	switch s {
	case entity.BomSectionFabric, entity.BomSectionLining,
		entity.BomSectionInterlining, entity.BomSectionInsulation:
		return true
	}
	return false
}

// catalogAsLinked УДАЛЁН НАМЕРЕННО (ревью Ф-С). Он адаптировал прайс-каталог сметы под форму,
// которую ждёт оценка, и честно объявлял, что атрибутов ткани в нём нет, — а значит смета
// резолвила ширину по снапшоту строки BOM, тогда как костинг брал полезную ширину артикула. Это и
// было расхождение: не «две реализации формулы», а ОДНА формула с двумя разными аргументами, что
// ничуть не лучше и куда труднее заметить. Обе проекции теперь передают tc.LinkedMaterials.
