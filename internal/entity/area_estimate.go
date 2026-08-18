package entity

import (
	"strings"

	"github.com/shopspring/decimal"
)

// ОЦЕНКА РАСХОДА ПО ПЛОЩАДИ ДЕТАЛЕЙ (Ф1, ступень 0) — нижняя граница нормы, выведенная из
// геометрии вместо того, чтобы требовать её ввода.
//
// ЗАЧЕМ. Карточка, у которой заполнена спецификация с ценами, разобраны выкройки и в рецепте
// колорвея каждой детали назначена ткань, до сих пор стоила НОЛЬ: деньги считает только строка
// рецепта с явно вписанной нормой. Всё, чего не хватало, — арифметика ниже.
//
//	норма(размер) = Σ (кол-во детали на изделие × площадь одного контура) ÷ раскройную ширину
//
// ЧТО В ЭТОМ ЧИСЛЕ ЕСТЬ И ЧЕГО В НЁМ НЕТ. Это NETTO: длина полотна, которая нужна, если детали
// лежат вплотную без остатка. Так не бывает никогда. Межлекальных выпадов, концов настила и обхода
// пороков здесь НЕТ и быть не может — их знает только раскладка. Поэтому результат обязан
// показываться как ОЦЕНКА СНИЗУ и не имеет права ни сеять каталожную себестоимость, ни закрывать
// гейт: занижение здесь измеряется десятками процентов (по замерам беты КПД раскладки 69.7–75%).
//
// ЕДИНИЦА — ЕДИНИЦА СЛОТА, И НЕИЗВЕСТНАЯ ЕДИНИЦА ЭТО ОТКАЗ. Площадь в см², делённая на ширину в см,
// даёт САНТИМЕТРЫ; цена стоит за единицу слота. Пропустить конверсию значит ошибиться в сто раз на
// метровом слоте — и ошибиться правдоподобно выглядящим числом.
//
// ШИРИНА — СВОЙСТВО АРТИКУЛА, А НЕ РАСКЛАДКИ. Раскройная ширина = ширина рулона минус две кромки
// (UsableFabricWidthCm); кромка НЕ доначисляется процентом сверху — она уже оплачена этим делением.
// Приоритет: пришпиленный колорвеем артикул → артикул слота → снапшот ширины на строке BOM.

// AreaEstimateRefusal names why an estimate could not be produced. Empty means it was.
//
// Every refusal is a DIFFERENT next action for the operator, which is why this is a string and not a
// bool: «нет площадей» sends them to the patterns tab, «нет ширины» to the BOM line, «единица не
// поддержана» to the slot's unit. A single «не посчитано» sent everybody to look everywhere.
type AreaEstimateRefusal string

const (
	AreaEstimateNoAssignments AreaEstimateRefusal = "no_pieces_assigned"
	AreaEstimateNoAreas       AreaEstimateRefusal = "no_measured_areas"
	AreaEstimateIncomplete    AreaEstimateRefusal = "areas_incomplete_for_size"
	AreaEstimateStale         AreaEstimateRefusal = "areas_stale"
	AreaEstimateNoWidth       AreaEstimateRefusal = "no_cutting_width"
	AreaEstimateUnitUnknown   AreaEstimateRefusal = "unit_not_convertible"
	AreaEstimateNoPrice       AreaEstimateRefusal = "no_price"
	AreaEstimateNoBasis       AreaEstimateRefusal = "no_basis"
	AreaEstimatePinConflict   AreaEstimateRefusal = "pin_conflict"
	// AreaEstimateNoPerimeter — краевое дублирование не посчитать: у детали снята площадь, но не
	// периметр (замер до 0305). ОТДЕЛЬНАЯ причина, а не AreaEstimateIncomplete, потому что действие
	// оператора другое и противоположно очевидному: комплект НЕ неполон — все детали на месте, все
	// размеры покрыты, экран замера показывает «всё сошлось». Не хватает второй меры того же контура,
	// и добывается она ровно одним движением — пересчитать площади этого скоупа новым клиентом.
	// Сказать здесь «комплект неполон» значило бы отправить искать пропавшую деталь, которой нет.
	AreaEstimateNoPerimeter AreaEstimateRefusal = "no_measured_perimeter"
	// AreaEstimateNoStripWidth — деталь размечена «по припуску», а эталона припуска нет ни на
	// карточке, ни в настройках цеха: ширины полосы не существует, считать нечем.
	//
	// ОТКАЗ, А НЕ ПОДСТАНОВКА ПОЛНОГО КОНТУРА. Соблазн «посчитать целиком, ошибка в сторону запаса»
	// здесь ложный: на экране осталось бы «по припуску», а деньги были бы посчитаны как «вся
	// деталь» — расхождение в разы между тем, что написано, и тем, что стоит, причём молчаливое.
	// Ноль был бы хуже вдвое, но и запас не оправдывает цифру, которая не соответствует подписи
	// рядом с ней.
	AreaEstimateNoStripWidth AreaEstimateRefusal = "no_seam_allowance_standard"
)

// AllAreaEstimateRefusals перечисляет причины отказа, чтобы их МОЖНО БЫЛО ПЕРЕБРАТЬ.
//
// Отказ теперь едет на провод (AdminColorwayRef.area_estimates.refusal) и там он — не диагностика,
// а ЕДИНСТВЕННОЕ, что стоит в строке рецепта вместо числа: по нему клиент называет недостающий факт
// и ведёт на вкладку, где его добавляют. Причина, которую забыли перевести на язык экрана, читается
// как пустая строка — то есть как «расхода нет», а не как «расход не посчитан». Этот список
// существует, чтобы тест мог потребовать покрытия ВСЕХ причин, а не тех, о которых вспомнили.
var AllAreaEstimateRefusals = []AreaEstimateRefusal{
	AreaEstimateNoAssignments,
	AreaEstimateNoAreas,
	AreaEstimateIncomplete,
	AreaEstimateStale,
	AreaEstimateNoWidth,
	AreaEstimateUnitUnknown,
	AreaEstimateNoPrice,
	AreaEstimateNoBasis,
	AreaEstimatePinConflict,
	AreaEstimateNoPerimeter,
	AreaEstimateNoStripWidth,
}

// AreaEstimatePiece is one cut piece of the slot's scope, as the estimate needs it.
type AreaEstimatePiece struct {
	LineKey string
	// PerGarment is the piece's pieces_per_garment — how many instances of this contour the garment
	// carries. Never below 1 in practice; a zero is treated as one instance, because a declared piece
	// that is cut zero times is not a thing the card can express.
	PerGarment int
	// StripWidthCm turns this piece from an AREA into a STRIP (0304): set, the piece contributes
	// `perimeter × width` instead of its area, because the interlining lies only along the edge.
	//
	// A WIDTH, NOT A MODE, and that is the whole point of the shape. Which mode produced the number —
	// «по припуску» reading the card's seam-allowance standard, or «полосой» reading the piece's own
	// millimetres — is a question about the CARD, and it is answered once, by the caller that holds
	// both the piece and the slot (slotAssignedPieces). Passing the mode down here would make this
	// function resolve the standard a second time, and the day the two resolutions disagreed the
	// recipe would print one norm under a cost computed from another.
	//
	// UNSET = the piece contributes its area: fusing the whole piece, and every piece of every
	// non-interlining slot. That is the pre-0304 behaviour, preserved by construction rather than by
	// a branch.
	StripWidthCm decimal.NullDecimal
	// StripWidthMissing — деталь размечена КРАЕВЫМ дублированием, но ширины полосы у неё нет:
	// «по припуску» при незаданном эталоне. Отдельное поле, а не пустая StripWidthCm, потому что
	// пустая ширина уже ЗНАЧИТ «считать площадью» (дублирование целиком), и свести к ней этот
	// случай значило бы посчитать полосу как всю деталь — молча и в разы дороже.
	StripWidthMissing bool
	// ScopeOverride names the fabric scope this piece's GEOMETRY lives in, when it is not the slot's
	// own — see AreaEstimateNorm's header on borrowed geometry. Empty = the slot's scope.
	ScopeOverride string
}

var (
	hundred = decimal.NewFromInt(100)
	ten     = decimal.NewFromInt(10)
)

// AreaEstimateNorm computes the netto consumption of ONE slot in ONE colourway at ONE size, in the
// slot's own unit.
//
// scope is the slot's fabric scope key; pieces are the cut pieces assigned to this slot in this
// colourway; areas is the card's measured geometry; widthCm is the resolved CUTTING width of the
// effective article; unit is the slot's unit of measure.
//
// A piece with no area for the requested size, but WITH a sizeless (ungraded) area, uses the
// ungraded one — that is what «не градуируется» means. A piece with neither is an INCOMPLETE set and
// refuses the whole size: a lost piece lowers the area, a lower area lowers the norm, and the
// shortfall is discovered in the warehouse rather than on the screen.
//
// ДВЕ ВЕЩИ, КОТОРЫЕ ПРИНЁС 0304, И ОБЕ ПРО КЛЕЕВУЮ.
//
// ПЕРВАЯ — ПОЛОСА ВМЕСТО ПЛОЩАДИ. Деталь с заданной StripWidthCm вносит `периметр × ширину`, а не
// свою площадь: клеевая лежит только вдоль среза. Формула остаётся одна на всех, потому что и то и
// другое — площадь в см², делённая дальше на ту же раскройную ширину; развилка ровно в том, ЧЕМ
// умножается контур.
//
// ВТОРАЯ — ЗАИМСТВОВАННАЯ ГЕОМЕТРИЯ (ScopeOverride). Площади лежат по СКОУПАМ ТКАНИ, а у клеевой
// своих выкроек не бывает: её не рисуют отдельным лекалом, её кроят по лекалу той детали, которую
// дублируют. Требовать замер клеевого скоупа значило бы требовать чертёж, которого не существует, и
// клеевой слот отказывал бы навсегда с «площади не измерены» — при полностью измеренной карточке.
// Поэтому деталь вправе назвать скоуп, где её контур ЕСТЬ, и оценка читает геометрию оттуда, а
// ширину полотна — по-прежнему у СВОЕГО артикула (клеевая уже рулон другой ширины). Скоуп выбирает
// вызывающий (slotAssignedPieces), который один держит и деталь, и слот; здесь только чтение.
//
// СВЕЖЕСТЬ ПРОВЕРЯЕТСЯ У КАЖДОГО ЗАДЕЙСТВОВАННОГО СКОУПА, а не у слотового. Заимствуя контур
// основной ткани, клеевой слот наследует и её устаревание: перерисовали полочку — устарела и норма
// клеевой на неё. Проверить только свой скоуп значило бы считать клеевую по лекалу, которого в
// файлах уже нет, и молчать об этом ровно там, где основная ткань честно скажет «пересчитайте».
func AreaEstimateNorm(
	scope string,
	pieces []AreaEstimatePiece,
	areas map[string]PieceAreaScope,
	widthCm decimal.NullDecimal,
	unit string,
	sizeID int,
) (decimal.Decimal, AreaEstimateRefusal) {
	if len(pieces) == 0 {
		return decimal.Zero, AreaEstimateNoAssignments
	}
	// Скоупы проверяются В ПОРЯДКЕ ПОЯВЛЕНИЯ, начиная со слотового, чтобы у одинаковых карточек
	// отказ был одинаковым: причина едет на экран, и «зависит от порядка обхода map» означало бы, что
	// два оператора видят разные указания починить.
	index := make(map[string]map[string]map[int]PieceAreaRow, 2)
	loadScope := func(key string) (map[string]map[int]PieceAreaRow, AreaEstimateRefusal) {
		if idx, ok := index[key]; ok {
			return idx, ""
		}
		sc, ok := areas[key]
		if !ok || len(sc.Rows) == 0 {
			return nil, AreaEstimateNoAreas
		}
		if sc.Stale {
			// Устаревшие площади — это НЕ «примерно те же». Выкройки менялись после замера, и считать по
			// ним значило бы называть числом то, чего в файлах уже нет.
			return nil, AreaEstimateStale
		}
		idx := make(map[string]map[int]PieceAreaRow, len(sc.Rows))
		for _, r := range sc.Rows {
			k := strings.ToUpper(strings.TrimSpace(r.PieceLineKey))
			if idx[k] == nil {
				idx[k] = map[int]PieceAreaRow{}
			}
			idx[k][int(r.SizeId.Int64)] = r
		}
		index[key] = idx
		return idx, ""
	}
	// Слотовый скоуп грузится первым и тогда, когда все детали заимствуют чужой: у карточки без
	// клеевых выкроек его просто нет, и отказывать за это нельзя — см. заголовок.
	var borrowedOnly = true
	for _, p := range pieces {
		if strings.TrimSpace(p.ScopeOverride) == "" {
			borrowedOnly = false
			break
		}
	}
	if !borrowedOnly {
		if _, refusal := loadScope(scope); refusal != "" {
			return decimal.Zero, refusal
		}
	}
	if !widthCm.Valid || !widthCm.Decimal.IsPositive() {
		return decimal.Zero, AreaEstimateNoWidth
	}
	factor, ok := areaEstimateUnitFactor(unit)
	if !ok {
		return decimal.Zero, AreaEstimateUnitUnknown
	}

	// ДВА ПРОХОДА, И ПОРЯДОК МЕЖДУ НИМИ — ЭТО ПОРЯДОК ОТКАЗОВ, а не стиль. Каждая причина обязана
	// называть ДЕЙСТВИЕ оператора, и «комплект неполон» старше «нет периметра»: деталь, которой нет
	// в замере вовсе, надо домерить, и пока её нет, любая жалоба на вторую меру ТОЙ ЖЕ детали
	// отправляет чинить не то. В один проход побеждала бы та причина, чья деталь попалась раньше, —
	// то есть указание оператору зависело бы от порядка строк рецепта.
	rows := make([]PieceAreaRow, 0, len(pieces))
	for _, p := range pieces {
		effScope := scope
		if s := strings.TrimSpace(p.ScopeOverride); s != "" {
			effScope = s
		}
		bySizeByPiece, refusal := loadScope(effScope)
		if refusal != "" {
			return decimal.Zero, refusal
		}
		key := strings.ToUpper(strings.TrimSpace(p.LineKey))
		sizes, ok := bySizeByPiece[key]
		if !ok {
			return decimal.Zero, AreaEstimateIncomplete
		}
		row, ok := sizes[sizeID]
		if !ok {
			// Неградуируемая деталь входит в комплект КАЖДОГО размера целиком — то же правило, что у
			// MarkerSizeAreasPerGarment; второго правила не заводим.
			row, ok = sizes[0]
		}
		if !ok {
			return decimal.Zero, AreaEstimateIncomplete
		}
		rows = append(rows, row)
	}

	total := decimal.Zero
	for i, p := range pieces {
		row := rows[i]
		contour := row.AreaCm2
		switch {
		case p.StripWidthMissing:
			// Ширины полосы не существует («по припуску» без эталона). Посчитать полным контуром
			// было бы в разы дороже подписи, которая стоит рядом на экране.
			return decimal.Zero, AreaEstimateNoStripWidth
		case p.StripWidthCm.Valid:
			// КРАЕВОЕ ДУБЛИРОВАНИЕ. Периметра нет — ОТКАЗ, а не вывод полосы из площади: у компактной
			// детали и у длинной узкой одной площади периметры отличаются вдвое, и ошибка ушла бы прямо
			// в закупку клеевой. Отказ называет ровно то одно движение, которым он лечится.
			if !row.PerimeterCm.Valid || !row.PerimeterCm.Decimal.IsPositive() {
				return decimal.Zero, AreaEstimateNoPerimeter
			}
			// ВЕРХНЯЯ ОЦЕНКА, И ЭТО ЗАЯВЛЕНО. Полоса идёт по срезам, а не по всему периметру: у сгиба
			// припуска нет и клеевой там не бывает. Различить срез от сгиба можно только поребёрной
			// разметкой лекала, которую никто не ведёт, — поэтому здесь честный периметр целиком.
			// Ошибка направлена В ЗАПАС (клеевой закажут чуть больше), в отличие от вывода из площади,
			// который врал бы в обе стороны непредсказуемо.
			contour = row.PerimeterCm.Decimal.Mul(p.StripWidthCm.Decimal)
		}
		mult := p.PerGarment
		if mult <= 0 {
			mult = 1
		}
		total = total.Add(contour.Mul(decimal.NewFromInt(int64(mult))))
	}
	if !total.IsPositive() {
		return decimal.Zero, AreaEstimateIncomplete
	}
	// см² ÷ см = СМ, дальше — в единицу слота.
	lengthCm := total.Div(widthCm.Decimal)
	return lengthCm.Mul(factor), ""
}

// areaEstimateUnitFactor converts a length in CENTIMETRES into the slot's unit.
//
// KILOGRAMS ARE DELIBERATELY REFUSED rather than approximated. A kg norm is a length converted
// through the article's density AND its roll width, and the article that carries those facts is the
// pinned one, not the slot — getting it wrong produces a plausible number in the wrong unit, which
// is the worst possible failure for a purchase figure. A kg slot keeps asking for a measured norm
// until the kg path is built for real.
func areaEstimateUnitFactor(unit string) (decimal.Decimal, bool) {
	u, ok := NormalizeMaterialUnit(unit)
	if !ok {
		return decimal.Zero, false
	}
	switch string(u) {
	case "m":
		return decimal.NewFromInt(1).Div(hundred), true
	case "cm":
		return decimal.NewFromInt(1), true
	case "mm":
		return ten, true
	}
	return decimal.Zero, false
}

// AreaEstimateRefusalText renders a refusal in the words the operator needs, not the code's.
func AreaEstimateRefusalText(r AreaEstimateRefusal) string {
	switch r {
	case AreaEstimateNoAssignments:
		return "no cut piece is assigned to this fabric in this colourway"
	case AreaEstimateNoAreas:
		return "the piece areas of this fabric are not measured — open the patterns tab and recompute"
	case AreaEstimateIncomplete:
		return "some pieces have no area for this size — the set is incomplete, the estimate would be understated"
	case AreaEstimateStale:
		return "the patterns changed after the areas were measured — recompute the areas"
	case AreaEstimateNoPerimeter:
		return "the pieces have a measured area but no perimeter — there is nothing to compute an edge fusing strip from; recompute the areas of this fabric, the measurement will add the perimeter"
	case AreaEstimateNoStripWidth:
		return "the piece is fused by seam allowance, and no seam allowance reference is set — neither on the card nor in the workshop settings; there is nowhere to take the strip width from"
	case AreaEstimateNoWidth:
		return "the fabric has no roll width filled in — there is nothing to divide by"
	case AreaEstimateUnitUnknown:
		return "the slot's unit doesn't convert from length (kilograms are computed off a marker only)"
	case AreaEstimateNoPrice:
		return "the fabric has no price, neither on the line nor in the catalogue"
	case AreaEstimateNoBasis:
		return "there is no size to compute on (the run line doesn't name a size)"
	case AreaEstimatePinConflict:
		return "pieces of one slot are pinned to different articles — these are two different rolls"
	}
	return ""
}
