package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// These tests pin the costing BASIS end-to-end (T6): a style's standard cost takes each
// size-graded recipe line as the SIMPLE AVERAGE of its norms over the card's DECLARED size range
// — the whole range or nothing — and base_sample_size_id is a reference «размер образца» that no
// longer moves any figure. The two retired bases (the qty-weighted average over size_quantities,
// then the base sample size's own norm) each died for a named reason; the assertions below keep
// their fallbacks from creeping back in, and keep «no size» (a run line naming none) from ever
// relaxing into the new default.

func bsSizeCons(sizeID int, v string) entity.TechCardBomSizeConsumption {
	return entity.TechCardBomSizeConsumption{SizeId: sizeID, Consumption: decimal.RequireFromString(v)}
}

// rangeAvgCard: declared range 4/5/6, a graded fabric line (2 / 2.5 / 3 m) and a countable trim,
// EUR throughout. base_sample_size_id is deliberately SET (size 4) — if any figure below moves
// when it moves, the reference field has crept back into the costing. size_quantities declare a
// mix skewed onto size 6 — if any figure moves when those numbers move, the typical-run
// denominator is back. Unit cost: shell (2+2.5+3)/3 × 10 × 1.1 = 27.5; zip 3 × 2 = 6; cmt 5 →
// 38.5.
func rangeAvgCard() *entity.TechCard {
	c := &entity.TechCard{Id: 42}
	c.Name = "Parka"
	c.StyleNumber = nstr("S-42")
	c.SizeIds = []int{4, 5, 6}
	c.BaseSampleSizeId = sql.NullInt32{Int32: 4, Valid: true}
	c.SizeQuantities = []entity.TechCardSizeQuantity{
		{SizeId: 4, OrderQty: 10}, {SizeId: 5, OrderQty: 30}, {SizeId: 6, OrderQty: 60},
	}
	c.BomItems = []entity.TechCardBomItem{
		{Id: 1, Name: "Shell", Section: entity.BomSectionFabric, Unit: nstr("m"),
			UnitPrice: nd("10"), Currency: nstr("EUR"), WastagePercent: nd("10")},
		{Id: 2, Name: "Zip", Section: entity.BomSectionHardware, Unit: nstr("pc"),
			UnitPrice: nd("2"), Currency: nstr("EUR")},
	}
	c.Colorways = []entity.TechCardColorway{{
		Id: 1, Name: "Black", ProductId: bidx(55),
		Usages: []entity.TechCardColorwayUsage{
			{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, SizeConsumptions: []entity.TechCardBomSizeConsumption{
				bsSizeCons(4, "2"), bsSizeCons(5, "2.5"), bsSizeCons(6, "3"),
			}},
			{BomItemId: sql.NullInt64{Int64: 2, Valid: true}, Quantity: nd("3")},
		},
	}}
	c.Costing = &entity.TechCardCosting{CmtCost: nd("5"), Currency: nstr("EUR")}
	return c
}

// TestColorwayUnitCostOnRangeAverage walks the whole seeding path — the one that ends in
// product.cost_price — and proves that neither of the two retired inputs participates any more:
// not the declared typical run, and not the base sample size.
func TestColorwayUnitCostOnRangeAverage(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := rangeAvgCard()

	// shell (2+2.5+3)/3 × 10 × 1.1 = 27.5; zip 3 × 2 = 6; cmt 5 → 38.5.
	unit, ccy := ComputeColorwayUnitCost(card, 55, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "38.5", unit.Decimal.String())

	// Rewriting the phantom run must not move the cost by a cent: the average divides by the SIZE
	// COUNT of the declared range, never by quantities.
	shifted := rangeAvgCard()
	shifted.SizeQuantities = []entity.TechCardSizeQuantity{
		{SizeId: 4, OrderQty: 1}, {SizeId: 5, OrderQty: 1}, {SizeId: 6, OrderQty: 900},
	}
	shiftedUnit, _ := ComputeColorwayUnitCost(shifted, 55, fx)
	require.True(t, shiftedUnit.Valid)
	require.Equal(t, unit.Decimal.String(), shiftedUnit.Decimal.String(),
		"size_quantities must not influence the standard cost")
	noQty := rangeAvgCard()
	noQty.SizeQuantities = nil
	noQtyUnit, _ := ComputeColorwayUnitCost(noQty, 55, fx)
	require.True(t, noQtyUnit.Valid, "a card with no declared run still has a standard cost")
	require.Equal(t, "38.5", noQtyUnit.Decimal.String())

	// Re-pointing or clearing the base sample size must not move it either: since T6 the field is
	// the reference «размер образца», not a costing input.
	repointed := rangeAvgCard()
	repointed.BaseSampleSizeId = sql.NullInt32{Int32: 6, Valid: true}
	repointedUnit, _ := ComputeColorwayUnitCost(repointed, 55, fx)
	require.Equal(t, "38.5", repointedUnit.Decimal.String(),
		"the base sample size is reference-only and must not move the cost")
	cleared := rangeAvgCard()
	cleared.BaseSampleSizeId = sql.NullInt32{}
	clearedUnit, _ := ComputeColorwayUnitCost(cleared, 55, fx)
	require.True(t, clearedUnit.Valid,
		"a card with NO base sample size prices fine — the old «no base size, no cost» rule is retired")
	require.Equal(t, "38.5", clearedUnit.Decimal.String())
}

// TestColorwayUnitCostIncompleteRangeDoesNotSeed is the guard that matters most: when the norm
// does not cover the WHOLE declared range (or there is no range), the cost must be UNCOMPUTED, so
// nothing is written to product.cost_price. An average over the graded subset would be a cost
// nobody approved, presented as one that was.
func TestColorwayUnitCostIncompleteRangeDoesNotSeed(t *testing.T) {
	fx := CostingFx{Base: "EUR"}

	// (a) a range size the usage carries no norm for.
	gap := rangeAvgCard()
	gap.Colorways[0].Usages[0].SizeConsumptions = []entity.TechCardBomSizeConsumption{
		bsSizeCons(4, "2"), bsSizeCons(6, "3"),
	}
	unit, _ := ComputeColorwayUnitCost(gap, 55, fx)
	require.False(t, unit.Valid, "a hole in the range coverage → no seedable unit cost")

	// (b) the range grew past the grading (a size added to the card after the norms were written).
	grown := rangeAvgCard()
	grown.SizeIds = append(grown.SizeIds, 7)
	unit, _ = ComputeColorwayUnitCost(grown, 55, fx)
	require.False(t, unit.Valid, "a new range size without a norm → no seedable unit cost")

	// (c) no declared range at all.
	rangeless := rangeAvgCard()
	rangeless.SizeIds = nil
	unit, _ = ComputeColorwayUnitCost(rangeless, 55, fx)
	require.False(t, unit.Valid, "no declared size range → nothing to average over → no cost")

	// The cost breakdown snapshot travels with the same verdict: cost_price and cost_breakdown
	// are never allowed to disagree about whether the recipe is fully costed.
	_, ok := ComputeColorwayCostBreakdownBase(gap, 55, fx)
	require.False(t, ok)

	// The display path still renders — it just reports the hole instead of hiding it.
	pb := ConvertEntityTechCardToPb(gap, fx)
	require.NotNil(t, pb.TechCard.Costing)
	require.True(t, pb.TechCard.Costing.HasUnpriced, "the gap must reach the wire")
}

// TestStyleCostEstimateUsesRangeAverage mirrors the seed's basis on the transparent estimate: the
// same averaged consumption, the same line total, and an explicit caveat NAMING the missing sizes
// when coverage is incomplete (the operator must be sent to the grading — to specific sizes — not
// on a hunt for a price that was never absent).
func TestStyleCostEstimateUsesRangeAverage(t *testing.T) {
	fx := CostingFx{Base: "EUR"}

	est := ComputeStyleCostEstimate(rangeAvgCard(), 0, nil, fx)
	require.NotNil(t, est)
	require.Len(t, est.Materials, 2)
	require.Equal(t, "2.5", est.Materials[0].Consumption.Value, "the range-averaged norm, ungrossed")
	require.Equal(t, "27.50", est.Materials[0].LineTotalBase.Value)
	require.Equal(t, "33.50", est.MaterialsPerUnitBase.Value)
	require.Equal(t, "38.50", est.UnitCostBase.Value)
	require.Empty(t, est.Caveat)

	// INVARIANT (kept from the golden test): the transparent estimate and the seed math must agree
	// to the cent on a fully-snapshotted card, on the new basis too, or the costing tab and
	// product.cost_price would show two different standard costs.
	seedUnit, _ := ComputeTechCardUnitCost(rangeAvgCard(), fx)
	require.True(t, seedUnit.Valid)
	require.Equal(t, seedUnit.Decimal.StringFixed(2), est.UnitCostBase.Value)

	// A hole in the coverage: the line shows no consumption, and the caveat names the missing
	// size poimённо (the dictionary cache is empty in tests, so the label falls back to «#id»).
	gap := rangeAvgCard()
	gap.Colorways[0].Usages[0].SizeConsumptions = []entity.TechCardBomSizeConsumption{
		bsSizeCons(4, "2"), bsSizeCons(6, "3"),
	}
	est = ComputeStyleCostEstimate(gap, 0, nil, fx)
	require.Nil(t, est.Materials[0].Consumption, "an uncosted graded line shows no per-garment norm")
	require.Equal(t, "6.00", est.MaterialsPerUnitBase.Value, "only the countable trim is costed")
	require.True(t, strings.Contains(est.Caveat, "declared size range"),
		"the estimate must name the missing basis, got %q", est.Caveat)
	require.True(t, strings.Contains(est.Caveat, "#5"),
		"the missing size must be NAMED, got %q", est.Caveat)
	require.False(t, strings.Contains(est.Caveat, "have no price"),
		"a priced article with an uncovered range is NOT an unpriced line; saying so sends the "+
			"operator to fix a price that was never missing: %q", est.Caveat)

	// No declared range: a different sentence — the fix is declaring the range, not grading more.
	rangeless := rangeAvgCard()
	rangeless.SizeIds = nil
	est = ComputeStyleCostEstimate(rangeless, 0, nil, fx)
	require.Nil(t, est.Materials[0].Consumption)
	require.True(t, strings.Contains(est.Caveat, "declares no size range"),
		"an empty range needs its own caveat, got %q", est.Caveat)
}

// TestStyleCostEstimateNamesBothDefectsAtOnce is the review fix (MINOR 4): the failure modes are
// independent verdicts, not an else-ladder. A line with NO price AND a coverage hole must put BOTH
// sentences in the caveat — and name the missing sizes — or the operator fixes the price only to
// be handed the second problem on the next save.
func TestStyleCostEstimateNamesBothDefectsAtOnce(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	both := rangeAvgCard()
	both.BomItems[0].UnitPrice = decimal.NullDecimal{} // no snapshot price, no material link → no price
	both.Colorways[0].Usages[0].SizeConsumptions = []entity.TechCardBomSizeConsumption{
		bsSizeCons(4, "2"), bsSizeCons(6, "3"), // size 5 of the declared range is not covered
	}

	est := ComputeStyleCostEstimate(both, 0, nil, fx)
	require.True(t, strings.Contains(est.Caveat, "have no price"),
		"the unpriced article must be named, got %q", est.Caveat)
	require.True(t, strings.Contains(est.Caveat, "declared size range"),
		"the coverage hole must be named IN THE SAME caveat, got %q", est.Caveat)
	require.True(t, strings.Contains(est.Caveat, "#5"),
		"the missing size must be named poimённо even while the price is also missing, got %q", est.Caveat)
}

// TestCostingDigestCoversSizeRange closes the hole T6 opened by making the declared size range
// price the style: if the range could move without moving the COSTING fingerprint, adding a size
// would reprice every colourway under a sign-off that still read "approved". And symmetrically:
// the base sample size LEFT the projection — a reference field must not stale a signature.
func TestCostingDigestCoversSizeRange(t *testing.T) {
	card := rangeAvgCard()
	base := TechCardSectionDigests(&card.TechCardInsert)

	// MEMBERSHIP is the input, so it restamps…
	grown := rangeAvgCard()
	grown.SizeIds = append(grown.SizeIds, 7)
	grownDigests := TechCardSectionDigests(&grown.TechCardInsert)
	require.NotEqual(t, base[entity.SignoffCosting], grownDigests[entity.SignoffCosting],
		"adding a size to the range reprices the style and must stale the costing sign-off")
	shrunk := rangeAvgCard()
	shrunk.SizeIds = []int{4, 5}
	shrunkDigests := TechCardSectionDigests(&shrunk.TechCardInsert)
	require.NotEqual(t, base[entity.SignoffCosting], shrunkDigests[entity.SignoffCosting],
		"removing a size reprices the style and must stale the costing sign-off")

	// …while ORDER is not: Σ/n is order-blind, so a reshuffle must not restamp.
	reordered := rangeAvgCard()
	reordered.SizeIds = []int{6, 4, 5}
	reorderedDigests := TechCardSectionDigests(&reordered.TechCardInsert)
	require.Equal(t, base[entity.SignoffCosting], reorderedDigests[entity.SignoffCosting],
		"reordering the declared range moves no figure and must not stale the sign-off")

	// The base sample size is OUT of the costing signature: re-pointing or clearing the reference
	// field moves no figure and must not stale anything.
	repointed := rangeAvgCard()
	repointed.BaseSampleSizeId = sql.NullInt32{Int32: 6, Valid: true}
	repointedDigests := TechCardSectionDigests(&repointed.TechCardInsert)
	cleared := rangeAvgCard()
	cleared.BaseSampleSizeId = sql.NullInt32{}
	clearedDigests := TechCardSectionDigests(&cleared.TechCardInsert)
	for _, sec := range []entity.TechCardSignoffSection{
		entity.SignoffDesign, entity.SignoffConstruction, entity.SignoffMaterials,
		entity.SignoffColour, entity.SignoffLabels, entity.SignoffPackaging, entity.SignoffCosting,
	} {
		require.Equal(t, base[sec], repointedDigests[sec], "section %s must not move with the reference base size", sec)
		require.Equal(t, base[sec], clearedDigests[sec], "section %s must not move when the reference base size clears", sec)
	}

	// Containment: the size range prices the COSTING section; changing it must not stale the
	// sections that do not hash it.
	for _, sec := range []entity.TechCardSignoffSection{
		entity.SignoffDesign, entity.SignoffConstruction, entity.SignoffMaterials,
		entity.SignoffColour, entity.SignoffLabels, entity.SignoffPackaging,
	} {
		require.Equal(t, base[sec], grownDigests[sec], "section %s must not move with the size range", sec)
	}
}

// TestRunCellNoSizeStaysUncostedUnderRangeAverage is the regression the three-valued basis exists
// for. Before T6 a run line with no size expressed «без базиса» through a NULL base_sample_size_id
// — a state that, after the default became «average over the range», would have FALLEN INTO that
// default: the sizeless line of a run would silently take the range-average price. The trap is
// armed exactly when the style default CAN answer (full coverage, average computable) — and the
// sizeless cell must still refuse.
func TestRunCellNoSizeStaysUncostedUnderRangeAverage(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := rangeAvgCard()

	// The style default is alive and computable — the trap's precondition.
	style, _ := ComputeTechCardUnitCost(card, fx)
	require.True(t, style.Valid)
	require.Equal(t, "38.5", style.Decimal.String())

	// A concrete size prices at that size — the run-cell path did not move.
	at6, _ := ComputeTechCardUnitCostOnSize(card, fx, decimal.NullDecimal{}, 6)
	require.True(t, at6.Valid)
	require.Equal(t, "44", at6.Decimal.String(), "3×10×1.1 + 6 + 5: the cell basis is that size's norm")

	// sizeID 0: NO basis — the graded shell prices nothing, so the whole figure is uncomputed.
	// This must NOT equal the style average; it must not exist at all.
	atNone, _ := ComputeTechCardUnitCostOnSize(card, fx, decimal.NullDecimal{}, 0)
	require.False(t, atNone.Valid,
		"a sizeless computation must stay uncosted even though the range average is computable")

	// The same trap through the run: a line with no size (and no product — the card has one
	// colourway, so the style path is taken) leaves the batch unpriced.
	unit, _ := ComputeProductionRunPlannedUnitCost(card, fx, decimal.NullDecimal{},
		[]entity.ProductionRunLine{{SizeId: 0, PlannedQty: 10}})
	require.False(t, unit.Valid, "a sizeless run line must never take the range-average price")

	// And the re-basing never leaks into the caller's card: the override is set on a copy.
	require.Nil(t, card.CostingSizeOverride, "pricing a cell must not re-base the caller's card")
	require.EqualValues(t, 4, card.BaseSampleSizeId.Int32, "the reference field is untouched")
}
