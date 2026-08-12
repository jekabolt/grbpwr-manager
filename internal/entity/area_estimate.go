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
}

// AreaEstimatePiece is one cut piece of the slot's scope, as the estimate needs it.
type AreaEstimatePiece struct {
	LineKey string
	// PerGarment is the piece's pieces_per_garment — how many instances of this contour the garment
	// carries. Never below 1 in practice; a zero is treated as one instance, because a declared piece
	// that is cut zero times is not a thing the card can express.
	PerGarment int
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
	sc, ok := areas[scope]
	if !ok || len(sc.Rows) == 0 {
		return decimal.Zero, AreaEstimateNoAreas
	}
	if sc.Stale {
		// Устаревшие площади — это НЕ «примерно те же». Выкройки менялись после замера, и считать по
		// ним значило бы называть числом то, чего в файлах уже нет.
		return decimal.Zero, AreaEstimateStale
	}
	if !widthCm.Valid || !widthCm.Decimal.IsPositive() {
		return decimal.Zero, AreaEstimateNoWidth
	}
	factor, ok := areaEstimateUnitFactor(unit)
	if !ok {
		return decimal.Zero, AreaEstimateUnitUnknown
	}

	// Индекс площадей скоупа: деталь → (размер → площадь), где 0 = неградуируемая.
	bySizeByPiece := make(map[string]map[int]decimal.Decimal, len(sc.Rows))
	for _, r := range sc.Rows {
		key := strings.ToUpper(strings.TrimSpace(r.PieceLineKey))
		if bySizeByPiece[key] == nil {
			bySizeByPiece[key] = map[int]decimal.Decimal{}
		}
		bySizeByPiece[key][int(r.SizeId.Int64)] = r.AreaCm2
	}

	total := decimal.Zero
	for _, p := range pieces {
		key := strings.ToUpper(strings.TrimSpace(p.LineKey))
		sizes, ok := bySizeByPiece[key]
		if !ok {
			return decimal.Zero, AreaEstimateIncomplete
		}
		area, ok := sizes[sizeID]
		if !ok {
			// Неградуируемая деталь входит в комплект КАЖДОГО размера целиком — то же правило, что у
			// MarkerSizeAreasPerGarment; второго правила не заводим.
			area, ok = sizes[0]
		}
		if !ok {
			return decimal.Zero, AreaEstimateIncomplete
		}
		mult := p.PerGarment
		if mult <= 0 {
			mult = 1
		}
		total = total.Add(area.Mul(decimal.NewFromInt(int64(mult))))
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
		return "ни одна деталь кроя не назначена на эту ткань в этом колорвее"
	case AreaEstimateNoAreas:
		return "площади деталей этой ткани не измерены — откройте вкладку выкроек и пересчитайте"
	case AreaEstimateIncomplete:
		return "у части деталей нет площади на этот размер — комплект неполон, оценка занижала бы"
	case AreaEstimateStale:
		return "выкройки менялись после замера площадей — пересчитайте площади"
	case AreaEstimateNoWidth:
		return "у ткани не заполнена ширина полотна — делить не на что"
	case AreaEstimateUnitUnknown:
		return "единица слота не переводится из длины (килограммы считаются только по раскладке)"
	case AreaEstimateNoPrice:
		return "у ткани нет цены ни в строке, ни в каталоге"
	case AreaEstimateNoBasis:
		return "нет размера, на котором считать (строка партии не называет размер)"
	case AreaEstimatePinConflict:
		return "детали одного слота пришпилены к разным артикулам — это два разных рулона"
	}
	return ""
}
