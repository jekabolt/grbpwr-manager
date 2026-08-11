package dto

import (
	"strings"

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

// slotAreaEstimate is the per-garment money one roll slot contributes by ESTIMATE, in the article's
// own currency. ok=false means no estimate is available, and the refusal says why.
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
) (money decimal.Decimal, currency string, ok bool, refusal entity.AreaEstimateRefusal) {
	if tc == nil || cw == nil || bom == nil || !isRollGoodsSection(bom.Section) {
		return decimal.Zero, "", false, ""
	}
	pieces, pinned := slotAssignedPieces(tc, cw, bom)
	if len(pieces) == 0 {
		return decimal.Zero, "", false, entity.AreaEstimateNoAssignments
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
		return decimal.Zero, "", false, entity.AreaEstimateNoPrice
	}
	widthCm := slotCuttingWidthCm(bom, pinned, linked)
	scope := entity.FabricScopeKey(bom.Purpose.String, bom.LineKey)
	unit := priced.Unit.String

	norm := func(sizeID int) (decimal.Decimal, entity.AreaEstimateRefusal) {
		return entity.AreaEstimateNorm(scope, pieces, tc.PieceAreaScopes, widthCm, unit, sizeID)
	}

	var perGarment decimal.Decimal
	switch basis.Mode {
	case entity.CostingBasisSize:
		n, r := norm(basis.SizeID)
		if r != "" {
			return decimal.Zero, "", false, r
		}
		perGarment = n
	case entity.CostingBasisRangeAverage:
		if len(basis.RangeSizeIds) == 0 {
			return decimal.Zero, "", false, entity.AreaEstimateIncomplete
		}
		sum := decimal.Zero
		for _, sid := range basis.RangeSizeIds {
			n, r := norm(sid)
			if r != "" {
				// НЕТ УСРЕДНЕНИЯ ПО ПОДМНОЖЕСТВУ. Средняя по тем размерам, что посчитались, — это
				// ровно то систематическое занижение, ради устранения которого T6 менял базис.
				return decimal.Zero, "", false, r
			}
			sum = sum.Add(n)
		}
		perGarment = sum.Div(decimal.NewFromInt(int64(len(basis.RangeSizeIds))))
	default:
		// Базиса нет (строка прогона без размера): пер-размерная величина без размера — это
		// отсутствие ответа, а не ноль.
		return decimal.Zero, "", false, entity.AreaEstimateNoBasis
	}
	if !perGarment.IsPositive() {
		return decimal.Zero, "", false, entity.AreaEstimateIncomplete
	}
	total := perGarment.Mul(priced.UnitPrice.Decimal)
	// ПРОЦЕНТ РАСКРОЯ СЛОТА НЕ НАЧИСЛЯЕТСЯ. Оценка объявлена нижней границей и показывается как
	// таковая; догрузив её процентом, мы получили бы число, которое выглядит как норма, считается
	// как норма и при этом ею не является. Ступень наверх — только через раскладку.
	return total, priced.Currency.String, true, ""
}

// slotAssignedPieces lists the cut pieces this colourway assigns to this slot, with the multiplicity
// the estimate multiplies by, and the article the piece rows pin (0 = none, or disagreement).
//
// ONE PREDICATE WITH THE MONEY: entity.IsPieceMaterialAssignment. The piece rows are exactly the
// statement «this piece is cut from that fabric»; a garment-level row would carry a norm and would
// have been costed before this function was ever reached.
func slotAssignedPieces(tc *entity.TechCard, cw *entity.TechCardColorway, bom *entity.TechCardBomItem) ([]entity.AreaEstimatePiece, int64) {
	byID := make(map[int]*entity.TechCardPiece, len(tc.Pieces))
	for i := range tc.Pieces {
		byID[tc.Pieces[i].Id] = &tc.Pieces[i]
	}
	out := make([]entity.AreaEstimatePiece, 0, len(cw.Usages))
	seen := map[string]bool{}
	var pin int64
	pinAgrees := true
	for i := range cw.Usages {
		u := &cw.Usages[i]
		if !u.IsPieceMaterialAssignment() {
			continue
		}
		if !u.BomItemId.Valid || int(u.BomItemId.Int64) != bom.Id {
			continue
		}
		p := byID[int(u.PieceId.Int64)]
		if p == nil || strings.TrimSpace(p.LineKey) == "" {
			continue
		}
		if seen[p.LineKey] {
			continue // one piece, one contour: a second row for the same piece adds no cloth
		}
		seen[p.LineKey] = true
		out = append(out, entity.AreaEstimatePiece{LineKey: p.LineKey, PerGarment: p.PiecesPerGarment})
		if u.MaterialId.Valid && u.MaterialId.Int64 > 0 {
			if pin == 0 {
				pin = u.MaterialId.Int64
			} else if pin != u.MaterialId.Int64 {
				pinAgrees = false
			}
		}
	}
	if !pinAgrees {
		// см. AreaEstimatePinConflict у вызывающего: возврат пустого списка означает именно это.
		// Две детали одного слота пришпилены к РАЗНЫМ артикулам — это не «оценка чуть неточна», это
		// два разных рулона под одним слотом. Молча выбрать один значило бы посчитать половину
		// изделия по чужой цене и чужой ширине.
		return nil, 0
	}
	return out, pin
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
