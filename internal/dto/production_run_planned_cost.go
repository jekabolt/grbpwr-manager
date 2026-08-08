package dto

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ComputeProductionRunPlannedUnitCost is a production run's planned unit cost: the average garment
// cost of THIS batch, weighted by the batch's own size mix —
//
//	Σ(unit_cost_at(line.size_id) × line.planned_qty) ÷ Σ line.planned_qty
//
// A size-graded consumption is therefore taken at the size being cut, not at the style's base size;
// a consumption stated as one number per garment is size-independent and contributes the same figure
// at every size, exactly as before.
//
// WHY THE RUN CANNOT REUSE THE STYLE FIGURE. The snapshot froze on the run is what the whole
// plan/fact variance is measured against, and it used to be the style's standard cost. While that
// standard was a quantity-weighted average over tech_card.size_quantities the mismatch was invisible
// — an invented mix hid inside a number nobody could trace — so a batch of nothing but XL was
// planned at the price of a mix it had no part in, and the variance carried that error silently for
// the run's whole life. Now that the style's standard is the BASE SIZE's own norm (see
// entity.TechCardColorwayUsage.UnitTotal), the same reuse would be an obvious lie: an XL batch would
// be planned at the M price. The run has the real numbers on its own lines; it must use them.
//
// THREE EDGES, each answered on purpose:
//
//  1. A run with NO lines (or none carrying a positive quantity) — the header is planned before the
//     grid is filled — falls back to the style's standard cost on its base size. This is a conscious
//     default and not a mix computed from nothing: the run has stated no sizes, so the only quantity
//     information in the system is the style's own basis. It is the pre-existing behaviour, kept.
//  2. A size on the grid with NO computable cost (typically: the recipe carries no norm for it)
//     leaves the WHOLE run unpriced — invalid result, and the caller stores NULL. It is never
//     dropped from the denominator: averaging over just the sizes that happen to be graded quietly
//     prices the batch as if the ungraded sizes were free, which is precisely the defect the base-size
//     phase found in the retired denominator. A run with no planned cost is a run that reports no
//     variance, and that is the honest outcome.
//  3. Sizes that resolve in DIFFERENT currencies cannot be averaged into one figure, so the result is
//     invalid. (In practice they agree — the currency depends on the costing row, not the size — but
//     a weighted mean across two units of measure would be a meaningless number, not an approximate one.)
//
// COLOURWAY IS OUT OF SCOPE, deliberately. Lines also name a colourway, and pinned articles can make
// colourways cost differently, but a run carries exactly ONE planned_unit_cost column; there is
// nowhere to put a per-colourway plan. The snapshot has always been the card's primary-colourway
// figure and stays so. This phase changes the SIZE basis only.
func ComputeProductionRunPlannedUnitCost(tc *entity.TechCard, fx CostingFx, wastageOverride decimal.NullDecimal, lines []entity.ProductionRunLine) (decimal.NullDecimal, string) {
	// Several lines can name one size (one per colourway/aux colour), so quantities are pooled per
	// size before pricing: costing the same size twice would be wasted work, and — more importantly —
	// weighting is by garments, not by rows.
	qtyBySize := make(map[int]int64, len(lines))
	sizeOrder := make([]int, 0, len(lines))
	totalQty := int64(0)
	for _, ln := range lines {
		if ln.PlannedQty <= 0 {
			continue
		}
		if _, seen := qtyBySize[ln.SizeId]; !seen {
			sizeOrder = append(sizeOrder, ln.SizeId)
		}
		qtyBySize[ln.SizeId] += int64(ln.PlannedQty)
		totalQty += int64(ln.PlannedQty)
	}
	if totalQty == 0 {
		return ComputeTechCardUnitCostWithWastage(tc, fx, wastageOverride) // edge 1
	}

	weighted := decimal.Zero
	currency := ""
	for _, sizeID := range sizeOrder {
		unit, ccy := ComputeTechCardUnitCostOnSize(tc, fx, wastageOverride, sizeID)
		if !unit.Valid {
			return decimal.NullDecimal{}, "" // edge 2
		}
		if currency == "" {
			currency = ccy
		} else if !strings.EqualFold(currency, ccy) {
			return decimal.NullDecimal{}, "" // edge 3
		}
		weighted = weighted.Add(unit.Decimal.Mul(decimal.NewFromInt(qtyBySize[sizeID])))
	}
	avg := roundMoney(weighted.Div(decimal.NewFromInt(totalQty)))
	if !avg.IsPositive() {
		// Mirrors ComputeTechCardUnitCost's own rule: a non-positive cost is not a cost.
		return decimal.NullDecimal{}, ""
	}
	return decimal.NullDecimal{Decimal: avg, Valid: true}, currency
}
