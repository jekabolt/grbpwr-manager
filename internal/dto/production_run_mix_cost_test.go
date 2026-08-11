package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// These tests pin the second half of the basis story: the two places that once priced off
// tech_card.size_quantities — the phantom «типовой тираж» — read the real numbers instead. A
// production run prices its plan on the mix of ITS OWN lines; R&D amortisation divides by the
// quantity the style's runs actually plan to make.

// runMixCard is rangeAvgCard with the numbers chosen so each size costs a round figure:
// declared range 4 / 5 / 6, shell norms 2 / 2.5 / 3 m at 10 EUR with 10% wastage, a 3-piece zip
// at 2 EUR, cmt 5. Unit cost at size s = norm(s)×10×1.1 + 6 + 5 → 33 / 38.5 / 44; the style's
// standard (the range average) = 2.5×10×1.1 + 6 + 5 = 38.5.
// Its size_quantities declare a mix skewed hard onto size 6; nothing below may move when they do.
func runMixCard() *entity.TechCard {
	return rangeAvgCard()
}

func mixLine(sizeID, qty int) entity.ProductionRunLine {
	return entity.ProductionRunLine{SizeId: sizeID, PlannedQty: qty}
}

// TestRunPlannedCostUsesItsOwnSizeMix is the finding this phase exists for: a batch of nothing but
// XL must be planned at the XL price, not at the style's standard cost.
func TestRunPlannedCostUsesItsOwnSizeMix(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := runMixCard()
	noWastageOverride := decimal.NullDecimal{}

	// The style's standard cost — the number the run used to snapshot regardless of what it cut.
	style, styleCcy := ComputeTechCardUnitCost(card, fx)
	require.True(t, style.Valid)
	require.Equal(t, "38.5", style.Decimal.String(), "standard cost = range average: 2.5×10×1.1 + 6 + 5")

	// A run of nothing but size 6: 3×10×1.1 + 6 + 5 = 44. NOT 33.
	unit, ccy := ComputeProductionRunPlannedUnitCost(card, fx, noWastageOverride,
		[]entity.ProductionRunLine{mixLine(6, 40)})
	require.True(t, unit.Valid)
	require.Equal(t, styleCcy, ccy)
	require.Equal(t, "44", unit.Decimal.String(), "a single-size batch is priced at that size")
	require.False(t, unit.Decimal.Equal(style.Decimal),
		"the whole point: a batch of one non-base size must not inherit the style figure")

	// A mixed batch is the quantity-weighted mean of its own sizes:
	// (33×10 + 38.5×20 + 44×70) ÷ 100 = (330 + 770 + 3080) ÷ 100 = 41.80.
	unit, _ = ComputeProductionRunPlannedUnitCost(card, fx, noWastageOverride,
		[]entity.ProductionRunLine{mixLine(4, 10), mixLine(5, 20), mixLine(6, 70)})
	require.True(t, unit.Valid)
	require.Equal(t, "41.8", unit.Decimal.String())

	// Weighting is by GARMENTS, not by rows: several lines on one size (one per colourway) pool.
	pooled, _ := ComputeProductionRunPlannedUnitCost(card, fx, noWastageOverride,
		[]entity.ProductionRunLine{mixLine(4, 5), mixLine(4, 5), mixLine(6, 35), mixLine(6, 35)})
	single, _ := ComputeProductionRunPlannedUnitCost(card, fx, noWastageOverride,
		[]entity.ProductionRunLine{mixLine(4, 10), mixLine(6, 70)})
	require.Equal(t, single.Decimal.String(), pooled.Decimal.String(),
		"splitting a size across colourway rows must not re-weight the mix")

	// The card's declared typical run is now inert here too — it was the only thing this figure
	// used to depend on.
	shifted := runMixCard()
	shifted.SizeQuantities = []entity.TechCardSizeQuantity{{SizeId: 4, OrderQty: 999}}
	shiftedUnit, _ := ComputeProductionRunPlannedUnitCost(shifted, fx, noWastageOverride,
		[]entity.ProductionRunLine{mixLine(6, 40)})
	require.Equal(t, "44", shiftedUnit.Decimal.String(), "size_quantities must not touch a run's plan")

	// And the run's own card must come back unmutated — the basis swap is a shallow copy.
	require.Nil(t, card.CostingSizeOverride, "pricing a run must not re-base the caller's card")
	require.EqualValues(t, 4, card.BaseSampleSizeId.Int32, "the reference base size is untouched")
}

// TestRunPlannedCostWithoutLinesIsCostedWithNoBasis is the review fix on edge 1: a header planned
// before the grid exists has stated no sizes at all, so it is priced with NO basis — explicitly.
// A graded card's empty run therefore has NO planned cost: handing it the style's range average
// would be a guess about a batch whose size mix nobody has stated, i.e. exactly the «no basis
// quietly becomes a number» fallback the three-valued override forbids. A card whose norms are all
// per-garment needs no basis and keeps its figure — the pre-existing behaviour for aux cards.
func TestRunPlannedCostWithoutLinesIsCostedWithNoBasis(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := runMixCard()

	// The style average IS computable (38.5) — that is the trap's precondition: the empty run must
	// refuse it anyway.
	style, _ := ComputeTechCardUnitCost(card, fx)
	require.True(t, style.Valid)
	require.Equal(t, "38.5", style.Decimal.String())

	for name, lines := range map[string][]entity.ProductionRunLine{
		"nil grid":        nil,
		"empty grid":      {},
		"all-zero grid":   {mixLine(4, 0), mixLine(6, 0)},
		"negative qty":    {mixLine(6, -5)},
		"zero after pool": {mixLine(5, 0)},
	} {
		unit, ccy := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{}, lines)
		require.False(t, unit.Valid,
			"%s: an empty run of a size-graded card must NOT inherit the range average", name)
		require.Empty(t, ccy, "%s", name)
	}

	// The flat-recipe half of the rule: per-garment norms need no basis, so an aux-shaped card's
	// empty header keeps the figure it always had.
	flat := runMixCard()
	flat.Colorways[0].Usages[0].SizeConsumptions = nil
	flat.Colorways[0].Usages[0].Consumption = nd("2")
	flatStyle, flatCcy := ComputeTechCardUnitCost(flat, fx)
	require.True(t, flatStyle.Valid)
	unit, ccy := ComputeProductionRunPlannedUnitCost(flat, fx, decimal.NullDecimal{}, nil)
	require.True(t, unit.Valid, "an empty run of a flat-normed card still prices — nothing needed a basis")
	require.Equal(t, flatStyle.Decimal.String(), unit.Decimal.String())
	require.Equal(t, flatCcy, ccy)
}

// TestRunPlannedCostUngradedSizeLeavesNoPlan is the guard against the defect the previous phase
// found in the retired denominator: a size the recipe does not cover must NOT be dropped from the
// average. Dropping it prices the batch as if those garments were free.
func TestRunPlannedCostUngradedSizeLeavesNoPlan(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := runMixCard()
	// The shell is graded on 4/5/6 only; size 7 is on the run but on no norm.
	card.SizeIds = append(card.SizeIds, 7)

	unit, ccy := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{mixLine(4, 90), mixLine(7, 10)})
	require.False(t, unit.Valid, "an ungraded size on the grid must leave the whole run unpriced")
	require.Empty(t, ccy)

	// Specifically NOT the average over the sizes that happen to be graded (which would be 33).
	graded, _ := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{mixLine(4, 90)})
	require.True(t, graded.Valid)
	require.Equal(t, "33", graded.Decimal.String(),
		"the graded-only batch is priced; the mixed one above must not silently collapse to this")

	// A line with no size at all is the same verdict — never quietly priced at the base size.
	unit, _ = ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{mixLine(0, 10)})
	require.False(t, unit.Valid, "a line with no size must not be priced off the base size")
}

// TestRunPlannedCostKeepsWastageOverride: the run's ACTUAL cutting wastage still overrides every BOM
// estimate, now applied at each line's own size. The two mechanisms compose; neither shadows the other.
func TestRunPlannedCostKeepsWastageOverride(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := runMixCard()

	// Size 6 with the BOM's 10% estimate: 3×10×1.1 + 11 = 44.
	// Size 6 with a run actual of 20%:     3×10×1.2 + 11 = 47.
	unit, _ := ComputeProductionRunPlannedUnitCost(card, fx, nd("20"),
		[]entity.ProductionRunLine{mixLine(6, 40)})
	require.True(t, unit.Valid)
	require.Equal(t, "47", unit.Decimal.String())

	// An explicit 0% is a value, not "unset": 3×10 + 11 = 41.
	unit, _ = ComputeProductionRunPlannedUnitCost(card, fx, nd("0"),
		[]entity.ProductionRunLine{mixLine(6, 40)})
	require.Equal(t, "41", unit.Decimal.String())

	// The no-lines branch carries the override too — visible on a FLAT card, the only shape an
	// empty run still prices (a graded card's empty run has no basis and no figure).
	flat := runMixCard()
	flat.Colorways[0].Usages[0].SizeConsumptions = nil
	flat.Colorways[0].Usages[0].Consumption = nd("2")
	fallback, _ := ComputeProductionRunPlannedUnitCost(flat, fx, nd("20"), nil)
	styleOverridden, _ := ComputeTechCardUnitCostWithWastage(flat, fx, nd("20"))
	require.True(t, fallback.Valid)
	require.Equal(t, styleOverridden.Decimal.String(), fallback.Decimal.String())

	require.Equal(t, "10", card.BomItems[0].WastagePercent.Decimal.String(), "override must not mutate the card")
}

// TestRunPlannedCostPerGarmentNormIsSizeBlind pins the untouched half: a consumption stated as one
// number per garment costs the same at every size, so the mix cannot move it. Without this, the
// weighted mean could be mistaken for a change that applies to ungraded cards too.
func TestRunPlannedCostPerGarmentNormIsSizeBlind(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := runMixCard()
	// Replace the graded shell norm with a single per-garment one: 2 m at every size.
	card.Colorways[0].Usages[0].SizeConsumptions = nil
	card.Colorways[0].Usages[0].Consumption = nd("2")

	style, _ := ComputeTechCardUnitCost(card, fx)
	require.True(t, style.Valid)

	for _, lines := range [][]entity.ProductionRunLine{
		{mixLine(4, 100)},
		{mixLine(6, 100)},
		{mixLine(4, 10), mixLine(5, 20), mixLine(6, 70)},
		nil,
	} {
		unit, _ := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{}, lines)
		require.True(t, unit.Valid)
		require.Equal(t, style.Decimal.String(), unit.Decimal.String(),
			"a per-garment norm is size-blind; the run mix must not move it")
	}
}

// TestPlannedRunQtyTotalIsTheSummaryQty locks the amortisation denominator to the quantity the style
// card prints beside it. They are the same rule or the same card shows two production quantities.
func TestPlannedRunQtyTotalIsTheSummaryQty(t *testing.T) {
	runs := []entity.ProductionRun{
		{ProductionRunInsert: entity.ProductionRunInsert{
			Lines: []entity.ProductionRunLine{mixLine(4, 10), mixLine(6, 20)},
		}},
		{ProductionRunInsert: entity.ProductionRunInsert{
			Status: entity.ProductionRunCancelled,
			Lines:  []entity.ProductionRunLine{mixLine(4, 500)},
		}},
		{ProductionRunInsert: entity.ProductionRunInsert{
			Lines: []entity.ProductionRunLine{mixLine(5, 5)},
		}},
	}
	require.EqualValues(t, 35, PlannedRunQtyTotal(runs), "10 + 20 + 5; the cancelled 500 is abandoned production")
	require.EqualValues(t, ComputeStyleProductionSummary(runs, decimal.Zero, false).PlannedQtyTotal,
		PlannedRunQtyTotal(runs), "the amortisation denominator and planned_qty_total must be one number")
	require.EqualValues(t, 0, PlannedRunQtyTotal(nil))
}

// TestDevCostAmortisesOverRuns is the second consumer: R&D per unit divides by the runs, and by
// nothing at all when there are none.
func TestDevCostAmortisesOverRuns(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := runMixCard() // standard cost 38.5 (range average)
	expenses := []entity.TechCardDevExpense{{Kind: "sample", AmountBase: nd("330")}}

	// Two live runs planning 10 + 20 = 30 garments → 330/30 = 11 per unit → 38.5 + 11 = 49.5.
	runs := []entity.ProductionRun{
		{ProductionRunInsert: entity.ProductionRunInsert{Lines: []entity.ProductionRunLine{mixLine(4, 10)}}},
		{ProductionRunInsert: entity.ProductionRunInsert{Lines: []entity.ProductionRunLine{mixLine(6, 20)}}},
	}
	sum := ComputeTechCardDevCostSummary(card, expenses, nil, runs, fx)
	require.EqualValues(t, 30, sum.OrderQty)
	require.NotNil(t, sum.UnitCostWithDev)
	require.Equal(t, "49.5", sum.UnitCostWithDev.Value)

	// The card's declared typical run (10+30+60 = 100) is no longer the denominator: were it still
	// used, the figure above would have been 38.5 + 3.30 = 41.80.
	require.NotEqual(t, "41.8", sum.UnitCostWithDev.Value)

	// A cancelled run adds no denominator — spending is not amortised over garments nobody will make.
	withCancelled := append(runs, entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{
		Status: entity.ProductionRunCancelled,
		Lines:  []entity.ProductionRunLine{mixLine(4, 970)},
	}})
	sum = ComputeTechCardDevCostSummary(card, expenses, nil, withCancelled, fx)
	require.EqualValues(t, 30, sum.OrderQty, "cancelled runs are excluded, exactly as in the plan/fact summary")
	require.Equal(t, "49.5", sum.UnitCostWithDev.Value)
}

// TestDevCostWithoutRunsIsNotAmortised: no runs → no denominator → no per-unit figure. The total and
// the rest of the roll-up still stand; only the division is withheld.
func TestDevCostWithoutRunsIsNotAmortised(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := runMixCard() // still declares size_quantities summing to 100 — which must now buy nothing
	expenses := []entity.TechCardDevExpense{{Kind: "sample", AmountBase: nd("330")}}

	for name, runs := range map[string][]entity.ProductionRun{
		"no runs at all": nil,
		"only cancelled": {{ProductionRunInsert: entity.ProductionRunInsert{
			Status: entity.ProductionRunCancelled,
			Lines:  []entity.ProductionRunLine{mixLine(4, 100)},
		}}},
		"runs with an empty grid": {{ProductionRunInsert: entity.ProductionRunInsert{}}},
	} {
		sum := ComputeTechCardDevCostSummary(card, expenses, nil, runs, fx)
		require.EqualValues(t, 0, sum.OrderQty, "%s", name)
		require.Nil(t, sum.UnitCostWithDev,
			"%s: with no planned batch the spend has nothing to divide by and must not be divided by the card's phantom run", name)
		require.Equal(t, "330", sum.TotalBase.Value, "%s: the total itself is unaffected", name)
	}
}

// TestDevCostNoUnitCostStillReportsQty: the denominator and the unit cost are independent gates —
// a style with planned runs but an uncomputable standard cost reports the quantity and withholds
// only the amortised figure.
func TestDevCostNoUnitCostStillReportsQty(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	uncovered := runMixCard()
	// A range size with no norm → the graded shell is uncosted → no standard cost. (Clearing the
	// base sample size no longer breaks anything: it is a reference field since T6.)
	uncovered.SizeIds = append(uncovered.SizeIds, 7)
	runs := []entity.ProductionRun{{ProductionRunInsert: entity.ProductionRunInsert{
		Lines: []entity.ProductionRunLine{mixLine(4, 30)},
	}}}

	sum := ComputeTechCardDevCostSummary(uncovered, []entity.TechCardDevExpense{{Kind: "sample", AmountBase: nd("330")}}, nil, runs, fx)
	require.EqualValues(t, 30, sum.OrderQty)
	require.Nil(t, sum.UnitCostWithDev, "no standard cost to add the amortised R&D to")
}
