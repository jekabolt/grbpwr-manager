package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// These tests pin the costing BASIS: a style's standard cost is the norm on its BASE SAMPLE SIZE,
// and there is no substitute when that norm is missing. The basis they replace divided a graded
// usage's whole-run cost by Σ tech_card.size_quantities — an illustrative mix, not a run — so the
// figure that reached product.cost_price moved whenever somebody edited a quantity nobody was going
// to produce. Every assertion below exists to keep a "reasonable" average from creeping back in.

func bsSizeCons(sizeID int, v string) entity.TechCardBomSizeConsumption {
	return entity.TechCardBomSizeConsumption{SizeId: sizeID, Consumption: decimal.RequireFromString(v)}
}

// baseSizeCard: sizes 4 (base) / 5 / 6, a graded fabric line and a countable trim, EUR throughout.
// size_quantities deliberately declare a mix that is NOT centred on the base size — if any figure
// below moves when those numbers move, the average is back.
func baseSizeCard() *entity.TechCard {
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

// TestUsageUnitTotalTakesBaseSizeNorm is the arithmetic itself, at the entity level: the base
// size's own norm, grossed by the article's wastage, and nothing about the other sizes.
func TestUsageUnitTotalTakesBaseSizeNorm(t *testing.T) {
	bom := &entity.TechCardBomItem{UnitPrice: nd("10"), WastagePercent: nd("10")}
	graded := entity.TechCardColorwayUsage{SizeConsumptions: []entity.TechCardBomSizeConsumption{
		bsSizeCons(4, "2"), bsSizeCons(5, "2.5"), bsSizeCons(6, "3"),
	}}

	// base size 4 → 2 × 10 × 1.1 = 22. The old basis over a 10/30/60 mix would have produced
	// (2×10 + 2.5×30 + 3×60)×10×1.1 / 100 = 30.25 — a number belonging to no size at all.
	got := graded.UnitTotal(bom, 4)
	require.True(t, got.Valid)
	require.Equal(t, "22", got.Decimal.String())

	// A different base size is a different standard cost — the point of the field.
	got = graded.UnitTotal(bom, 6)
	require.True(t, got.Valid)
	require.Equal(t, "33", got.Decimal.String())

	// Marker-sourced norms are still never grossed up (the measured lay already contains the
	// cutting waste); the base-size change must not reopen that double-count.
	marker := graded
	marker.ConsumptionSource = sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true}
	got = marker.UnitTotal(bom, 4)
	require.True(t, got.Valid)
	require.Equal(t, "20", got.Decimal.String())
}

// TestUsageUnitTotalNoBasisIsUncosted covers the two ways the basis can be missing. Both must
// produce NO number — never the mean, the median, or the first graded size.
func TestUsageUnitTotalNoBasisIsUncosted(t *testing.T) {
	bom := &entity.TechCardBomItem{UnitPrice: nd("10")}
	graded := entity.TechCardColorwayUsage{SizeConsumptions: []entity.TechCardBomSizeConsumption{
		bsSizeCons(5, "2.5"), bsSizeCons(6, "3"),
	}}

	// (a) the style names no base sample size.
	require.False(t, graded.UnitTotal(bom, 0).Valid, "no base size must mean no cost, not an average")

	// (b) the style names one, but THIS usage was never graded on it — the partially-graded case
	// the old denominator silently swallowed by dividing by the whole run anyway.
	require.False(t, graded.UnitTotal(bom, 4).Valid, "a usage with no norm on the base size is uncosted")

	// The whole-run helper is unaffected: it answers a different question and still has quantities.
	require.True(t, graded.SizeRunTotal(bom, map[int]int{5: 10, 6: 10}).Valid)
}

// TestUsageUnitTotalPerGarmentUnchanged pins the untouched half of the contract: a norm stated as
// one number per garment is costed exactly as before, base size or no base size.
func TestUsageUnitTotalPerGarmentUnchanged(t *testing.T) {
	bom := &entity.TechCardBomItem{UnitPrice: nd("10"), WastagePercent: nd("10")}

	measured := entity.TechCardColorwayUsage{Consumption: nd("2")}
	countable := entity.TechCardColorwayUsage{Quantity: nd("3")}

	for _, baseSize := range []int{0, 4, 999} {
		got := measured.UnitTotal(bom, baseSize)
		require.True(t, got.Valid)
		require.Equal(t, "22", got.Decimal.String(), "measured per-garment norm: 2×10×1.1, base size irrelevant")

		got = countable.UnitTotal(bom, baseSize)
		require.True(t, got.Valid)
		require.Equal(t, "30", got.Decimal.String(), "countable trim: 3×10, no wastage, base size irrelevant")
	}
}

// TestColorwayUnitCostOnBaseSize walks the whole seeding path — the one that ends in
// product.cost_price — and proves the declared typical run no longer participates in it.
func TestColorwayUnitCostOnBaseSize(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := baseSizeCard()

	// shell at base size 4: 2 × 10 × 1.1 = 22; zip 3 × 2 = 6; cmt 5 → 33.
	unit, ccy := ComputeColorwayUnitCost(card, 55, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "33", unit.Decimal.String())

	// Rewriting the phantom run must not move the cost by a cent. Under the old basis this edit
	// alone re-priced the style and rewrote product.cost_price on the next save.
	shifted := baseSizeCard()
	shifted.SizeQuantities = []entity.TechCardSizeQuantity{
		{SizeId: 4, OrderQty: 1}, {SizeId: 5, OrderQty: 1}, {SizeId: 6, OrderQty: 900},
	}
	shiftedUnit, _ := ComputeColorwayUnitCost(shifted, 55, fx)
	require.True(t, shiftedUnit.Valid)
	require.Equal(t, unit.Decimal.String(), shiftedUnit.Decimal.String(),
		"size_quantities must no longer influence the standard cost")

	// Deleting them entirely likewise: the old basis went INVALID here (nothing to divide by) and
	// the seed silently stopped updating the cost. The base size answers on its own.
	noQty := baseSizeCard()
	noQty.SizeQuantities = nil
	noQtyUnit, _ := ComputeColorwayUnitCost(noQty, 55, fx)
	require.True(t, noQtyUnit.Valid, "a card with no declared run still has a standard cost")
	require.Equal(t, "33", noQtyUnit.Decimal.String())
}

// TestColorwayUnitCostWithoutBaseSizeDoesNotSeed is the guard that matters most: when the basis is
// missing the cost must be UNCOMPUTED, so nothing is written to product.cost_price. A number here
// would be a cost nobody approved, presented as one that was.
func TestColorwayUnitCostWithoutBaseSizeDoesNotSeed(t *testing.T) {
	fx := CostingFx{Base: "EUR"}

	noBase := baseSizeCard()
	noBase.BaseSampleSizeId = sql.NullInt32{}
	unit, _ := ComputeColorwayUnitCost(noBase, 55, fx)
	require.False(t, unit.Valid, "no base sample size → no seedable unit cost")

	// Same when the base size exists but this recipe carries no norm on it.
	ungraded := baseSizeCard()
	ungraded.Colorways[0].Usages[0].SizeConsumptions = []entity.TechCardBomSizeConsumption{
		bsSizeCons(5, "2.5"), bsSizeCons(6, "3"),
	}
	unit, _ = ComputeColorwayUnitCost(ungraded, 55, fx)
	require.False(t, unit.Valid, "no norm on the base size → no seedable unit cost")

	// The cost breakdown snapshot travels with the same verdict: cost_price and cost_breakdown are
	// never allowed to disagree about whether the recipe is fully costed.
	_, ok := ComputeColorwayCostBreakdownBase(noBase, 55, fx)
	require.False(t, ok)

	// The display path still renders — it just reports the hole instead of hiding it.
	pb := ConvertEntityTechCardToPb(noBase, fx)
	require.NotNil(t, pb.TechCard.Costing)
	require.True(t, pb.TechCard.Costing.HasUnpriced, "the gap must reach the wire")
}

// TestStyleCostEstimateUsesBaseSizeNorm mirrors the seed's basis on the transparent estimate: same
// consumption, same line total, and an explicit caveat when the basis is missing (the operator must
// be sent to the grading, not on a hunt for a price that was never absent).
func TestStyleCostEstimateUsesBaseSizeNorm(t *testing.T) {
	fx := CostingFx{Base: "EUR"}

	est := ComputeStyleCostEstimate(baseSizeCard(), 0, nil, fx)
	require.NotNil(t, est)
	require.Len(t, est.Materials, 2)
	require.Equal(t, "2", est.Materials[0].Consumption.Value, "the base size's own norm, ungrossed")
	require.Equal(t, "22.00", est.Materials[0].LineTotalBase.Value)
	require.Equal(t, "28.00", est.MaterialsPerUnitBase.Value)
	require.Equal(t, "33.00", est.UnitCostBase.Value)
	require.Empty(t, est.Caveat)

	// INVARIANT (kept from the golden test): the transparent estimate and the seed math must agree
	// to the cent on a fully-snapshotted card. It has to hold on the new basis too, or the costing
	// tab and product.cost_price would show two different standard costs.
	seedUnit, _ := ComputeTechCardUnitCost(baseSizeCard(), fx)
	require.True(t, seedUnit.Valid)
	require.Equal(t, seedUnit.Decimal.StringFixed(2), est.UnitCostBase.Value)

	noBase := baseSizeCard()
	noBase.BaseSampleSizeId = sql.NullInt32{}
	est = ComputeStyleCostEstimate(noBase, 0, nil, fx)
	require.Nil(t, est.Materials[0].Consumption, "an uncosted graded line shows no per-garment norm")
	require.Equal(t, "6.00", est.MaterialsPerUnitBase.Value, "only the countable trim is costed")
	require.True(t, strings.Contains(est.Caveat, "base sample size"),
		"the estimate must name the missing basis, got %q", est.Caveat)
	require.False(t, strings.Contains(est.Caveat, "have no price"),
		"a priced article with no base-size norm is NOT an unpriced line; saying so sends the "+
			"operator to fix a price that was never missing: %q", est.Caveat)
}

// TestCostingDigestCoversBaseSize closes the hole this phase opened by making base_sample_size_id
// price the style: if the field could move without moving the COSTING fingerprint, re-pointing the
// base size would reprice every colourway under a sign-off that still read "approved".
func TestCostingDigestCoversBaseSize(t *testing.T) {
	card := baseSizeCard()
	base := TechCardSectionDigests(&card.TechCardInsert)

	moved := baseSizeCard()
	moved.BaseSampleSizeId = sql.NullInt32{Int32: 6, Valid: true}
	movedDigests := TechCardSectionDigests(&moved.TechCardInsert)
	require.NotEqual(t, base[entity.SignoffCosting], movedDigests[entity.SignoffCosting],
		"re-pointing the base sample size reprices the style and must stale the costing sign-off")

	cleared := baseSizeCard()
	cleared.BaseSampleSizeId = sql.NullInt32{}
	clearedDigests := TechCardSectionDigests(&cleared.TechCardInsert)
	require.NotEqual(t, base[entity.SignoffCosting], clearedDigests[entity.SignoffCosting],
		"clearing the base sample size makes the cost uncomputable and must stale the sign-off")

	// An unset NullInt32 and a stored 0 are the same state to the costing; they must not produce
	// two fingerprints, or a card would read as edited purely from how the column was written.
	zeroed := baseSizeCard()
	zeroed.BaseSampleSizeId = sql.NullInt32{Int32: 0, Valid: true}
	zeroedDigests := TechCardSectionDigests(&zeroed.TechCardInsert)
	require.Equal(t, clearedDigests[entity.SignoffCosting], zeroedDigests[entity.SignoffCosting])

	// Containment: the base size belongs to COSTING and to no other section's signature.
	for _, sec := range []entity.TechCardSignoffSection{
		entity.SignoffDesign, entity.SignoffConstruction, entity.SignoffMaterials,
		entity.SignoffColour, entity.SignoffLabels, entity.SignoffPackaging,
	} {
		require.Equal(t, base[sec], movedDigests[sec], "section %s must not move with the base size", sec)
	}
}
