package dto

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// runMixKey is one priced cell of a run's plan: a colourway at a size. Both halves are load-bearing
// — see ComputeProductionRunPlannedUnitCost for what pooling on either one alone costs.
type runMixKey struct {
	productID int // the line's colourway (product id); 0 = a line that names no product
	sizeID    int
}

// ComputeProductionRunPlannedUnitCost is a production run's planned unit cost: the average garment
// cost of THIS batch, weighted by the batch's own mix of COLOURWAYS AND SIZES —
//
//	Σ(unit_cost_at(line.product_id, line.size_id) × line.planned_qty) ÷ Σ line.planned_qty
//
// A size-graded consumption is therefore taken at the size being cut, not averaged over the style's
// declared range; a consumption stated as one number per garment is size-independent and contributes
// the same figure at every size. A pinned article is likewise taken at the colourway that pins it,
// not at the card's primary colourway.
//
// WHY THE RUN CANNOT REUSE THE STYLE FIGURE. The snapshot froze on the run is what the whole
// plan/fact variance is measured against, and it used to be the style's standard cost. While that
// standard was a quantity-weighted average over tech_card.size_quantities the mismatch was invisible
// — an invented mix hid inside a number nobody could trace — so a batch of nothing but XL was
// planned at the price of a mix it had no part in, and the variance carried that error silently for
// the run's whole life. The style's standard is now the simple average over the declared size
// range (see entity.TechCardColorwayUsage.UnitTotal), and the same reuse would still be a lie —
// an all-XL batch is not an average batch. The run has the real numbers on its own lines; it
// must use them.
//
// WHY COLOURWAY IS IN THE MIX. It was once left out on the grounds that a run carries exactly ONE
// planned_unit_cost column and the card's primary colourway was as good a basis as any. It is not:
// pinning exists precisely to make colourways cost differently, and a batch of 50 black on a €10 pin
// plus 50 white on a €30 pin was snapshotted at €10 — the first colourway's price — instead of the
// €20 it actually plans to spend. One column does not mean one colour: it means one WEIGHTED figure,
// and the weights are on the lines. Each cell is priced by ComputeColorwayUnitCost, the same
// per-colourway function the cost seed uses (never a second copy of that math), evaluated on a card
// re-based to the cell's size.
//
// FIVE EDGES, each answered on purpose:
//
//  1. A run with NO lines (or none carrying a positive quantity) — the header is planned before the
//     grid is filled — is priced with NO basis, set EXPLICITLY (cardCostedOnSize's zero), never by
//     falling into the style default. A flat per-garment norm needs no basis and prices exactly as
//     it always did (an aux card's empty header keeps its figure); a size-graded norm prices
//     nothing, so a graded card's empty run has no planned cost — the pre-T6 outcome, kept. The
//     range average is NOT inherited here on purpose: the average is a figure of the MODEL, and an
//     empty run has stated no size mix at all — handing it the average would be a guess about the
//     batch dressed up as a plan, the exact «no basis quietly becomes a number» fallback the
//     three-valued override exists to kill (see cardCostedOnSize). Explicit rather than implied,
//     so a future change of the override's default cannot re-open this hole.
//  2. A cell with NO computable cost (typically: the recipe carries no norm for that size) leaves the
//     WHOLE run unpriced — invalid result, and the caller stores NULL. It is never dropped from the
//     denominator: averaging over just the cells that happen to price quietly costs the batch as if
//     the rest were free, which is precisely the defect the base-size phase found in the retired
//     denominator. A run with no planned cost is a run that reports no variance, and that is the
//     honest outcome.
//  3. Cells that resolve in DIFFERENT currencies cannot be averaged into one figure, so the result is
//     invalid. (In practice they agree — the currency depends on the costing row, not the size — but
//     a weighted mean across two units of measure would be a meaningless number, not an approximate one.)
//  4. A line naming a product the card has NO colourway for prices nothing, so the whole run goes
//     unpriced (edge 2's rule, reached through ComputeColorwayUnitCost's own "unknown id → invalid").
//     Falling back to the style figure there would price a batch by a colour it is not making — the
//     very substitution this phase removed, just one level up.
//  5. A line with NO product (an aux colour's line, or the single output of a legacy aux card) has no
//     colourway to be priced by. It is priced by the card's own figure ONLY when the card carries at
//     most one colourway — then that one recipe IS the card's price and nothing is being borrowed
//     from a neighbour. On a card with two or more colourways the same line would have to pick one of
//     them, and picking is exactly the defect; the whole batch goes unpriced instead. NULL on the
//     batch is recoverable (somebody names the colour and re-plans); a plausible number costed off
//     the wrong colour is not — it silently becomes the baseline every later variance is read against.
func ComputeProductionRunPlannedUnitCost(tc *entity.TechCard, fx CostingFx, wastageOverride decimal.NullDecimal, lines []entity.ProductionRunLine) (decimal.NullDecimal, string) {
	// Several lines can name one (colourway, size) cell — the grid is diffed by line_key and holds no
	// uniqueness on the pair — so quantities are pooled per cell before pricing: costing the same cell
	// twice would be wasted work, and — more importantly — weighting is by garments, not by rows.
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
		// Edge 1: explicit NO basis (sizeID 0), never the style default — see the header.
		return ComputeTechCardUnitCostOnSize(tc, fx, wastageOverride, 0)
	}

	weighted := decimal.Zero
	currency := ""
	for _, cell := range cellOrder {
		unit, ccy := runCellUnitCost(tc, fx, wastageOverride, cell)
		if !unit.Valid {
			return decimal.NullDecimal{}, "" // edges 2, 4 and 5
		}
		if currency == "" {
			currency = ccy
		} else if !strings.EqualFold(currency, ccy) {
			return decimal.NullDecimal{}, "" // edge 3
		}
		weighted = weighted.Add(unit.Decimal.Mul(decimal.NewFromInt(qtyByCell[cell])))
	}
	avg := roundMoney(weighted.Div(decimal.NewFromInt(totalQty)))
	if !avg.IsPositive() {
		// Mirrors ComputeTechCardUnitCost's own rule: a non-positive cost is not a cost.
		return decimal.NullDecimal{}, ""
	}
	return decimal.NullDecimal{Decimal: avg, Valid: true}, currency
}

// runCellUnitCost is what one garment of a (colourway, size) cell costs: the colourway's own recipe
// — pins included — evaluated at that size, with the run's actual cutting wastage applied.
//
// The size is applied by re-basing the card (cardCostedOnSize) rather than by threading a size
// parameter through the costing: the whole costing path reads the basis through
// TechCardInsert.CostingBasis, so this is the same single rule evaluated elsewhere, not a second
// one. sizeID <= 0 leaves the card with NO basis — deliberately not the style default: a
// size-graded norm then prices nothing (never the range average, which would price the line off
// sizes it does not name), and a flat per-garment norm (an aux card's usual shape) prices
// exactly as it always did.
//
// The product-less branch is edge 5 of ComputeProductionRunPlannedUnitCost: the card's own figure
// when there is at most one colourway to take it from, nothing at all otherwise.
func runCellUnitCost(tc *entity.TechCard, fx CostingFx, wastageOverride decimal.NullDecimal, cell runMixKey) (decimal.NullDecimal, string) {
	if cell.productID <= 0 {
		if tc != nil && len(tc.Colorways) > 1 {
			return decimal.NullDecimal{}, ""
		}
		return ComputeTechCardUnitCostOnSize(tc, fx, wastageOverride, cell.sizeID)
	}
	return ComputeColorwayUnitCost(
		cardCostedOnSize(cardWithRunWastage(tc, wastageOverride), cell.sizeID), cell.productID, fx)
}
