package costbasisreport

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func nd(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func sc(sizeID int, v string) entity.TechCardBomSizeConsumption {
	return entity.TechCardBomSizeConsumption{SizeId: sizeID, Consumption: decimal.RequireFromString(v)}
}

// reportCard: sizes 4/5/6 with a declared run of 10/30/60, one graded fabric, EUR throughout.
func reportCard() *entity.TechCard {
	c := &entity.TechCard{Id: 1}
	c.Name = "Parka"
	c.SizeIds = []int{4, 5, 6}
	c.BaseSampleSizeId = sql.NullInt32{Int32: 4, Valid: true}
	c.SizeQuantities = []entity.TechCardSizeQuantity{
		{SizeId: 4, OrderQty: 10}, {SizeId: 5, OrderQty: 30}, {SizeId: 6, OrderQty: 60},
	}
	c.BomItems = []entity.TechCardBomItem{
		{Id: 1, Name: "Shell", Section: entity.BomSectionFabric, UnitPrice: nd("10"), Currency: ns("EUR")},
	}
	c.Colorways = []entity.TechCardColorway{{
		Id: 1, Name: "Black", ProductId: sql.NullInt32{Int32: 55, Valid: true},
		Usages: []entity.TechCardColorwayUsage{
			{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, SizeConsumptions: []entity.TechCardBomSizeConsumption{
				sc(4, "2"), sc(5, "2.5"), sc(6, "3"),
			}},
		},
	}}
	c.Costing = &entity.TechCardCosting{Currency: ns("EUR")}
	return c
}

func declared(c *entity.TechCard) (map[int]int, int) {
	m, total := map[int]int{}, 0
	for _, q := range c.SizeQuantities {
		if q.OrderQty > 0 {
			m[q.SizeId] = q.OrderQty
			total += q.OrderQty
		}
	}
	return m, total
}

// TestOldWeightedNormReproducesRetiredBasis pins the "before" column. The report is only worth
// reading if its old number is the number the system used to produce — an approximation would make
// every delta it prints a fiction.
func TestOldWeightedNormReproducesRetiredBasis(t *testing.T) {
	card := reportCard()
	qty, _ := declared(card)
	u := &card.Colorways[0].Usages[0]

	// (2×10 + 2.5×30 + 3×60) / 100 = 275/100 = 2.75
	got, ok := oldWeightedNorm(u, qty)
	require.True(t, ok)
	require.Equal(t, "2.75", got.String())

	// THE DEFECT, written down as a test. Drop the norm for size 6 and the numerator loses 180
	// while the denominator keeps all 100 garments: (2×10 + 2.5×30) / 100 = 0.95 — well under the
	// 2..3 the usage is actually graded at anywhere. That systematic undercount is why some cards
	// get MORE expensive under the base-size basis without anybody having repriced anything.
	partial := *u
	partial.SizeConsumptions = []entity.TechCardBomSizeConsumption{sc(4, "2"), sc(5, "2.5")}
	got, ok = oldWeightedNorm(&partial, qty)
	require.True(t, ok)
	require.Equal(t, "0.95", got.String())

	// No declared run at all → the old basis had no denominator and produced nothing.
	_, ok = oldWeightedNorm(u, map[int]int{})
	require.False(t, ok)
}

// TestOldBasisCardPricesThroughLiveCosting proves the shadow-card trick: running the CURRENT costing
// code over the rewritten card yields the retired figure, so the report never needs a second copy of
// the costing math to compare against.
func TestOldBasisCardPricesThroughLiveCosting(t *testing.T) {
	card := reportCard()
	qty, total := declared(card)
	fx := dto.CostingFx{Base: "EUR"}

	// New basis: base size 4 → 2 × 10 = 20.
	newCost, ccy := dto.ComputeColorwayUnitCost(card, 55, fx)
	require.True(t, newCost.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "20", newCost.Decimal.String())

	// Old basis via the shadow: 2.75 × 10 = 27.5.
	old := oldBasisCard(card, qty, total)
	oldCost, oldCcy := dto.ComputeColorwayUnitCost(old, 55, fx)
	require.True(t, oldCost.Valid)
	require.Equal(t, "EUR", oldCcy)
	require.Equal(t, "27.5", oldCost.Decimal.String())

	// The shadow must not mutate the caller's card — the report prices both from the same load.
	require.Len(t, card.Colorways[0].Usages[0].SizeConsumptions, 3)
	require.False(t, card.Colorways[0].Usages[0].Consumption.Valid)

	// A card with no declared run had NO old figure (nothing to divide by) but has a new one. The
	// report must show that as an appearance, not silently as a zero.
	noRun := reportCard()
	noRun.SizeQuantities = nil
	emptyQty, emptyTotal := declared(noRun)
	oldCost, _ = dto.ComputeColorwayUnitCost(oldBasisCard(noRun, emptyQty, emptyTotal), 55, fx)
	require.False(t, oldCost.Valid)
	newCost, _ = dto.ComputeColorwayUnitCost(noRun, 55, fx)
	require.True(t, newCost.Valid)
}

// TestBuildUsageReportsBothNorms checks the row a human reads: the graded norms, the retired
// average, the base-size norm, and the uncosted verdict when the base size carries none.
func TestBuildUsageReportsBothNorms(t *testing.T) {
	card := reportCard()
	qty, _ := declared(card)
	names := map[int]string{4: "M", 5: "L", 6: "XL"}

	got := buildUsage(card, &card.Colorways[0].Usages[0], 4, names, qty)
	require.True(t, got.SizeGraded)
	require.Equal(t, "Shell", got.Material)
	require.Len(t, got.NormsBySize, 3)
	require.Equal(t, "M", got.NormsBySize[0].SizeName)
	require.True(t, got.NormsBySize[0].PartOfDeclaredRun)
	require.Equal(t, "2.75", *got.OldNorm)
	require.Equal(t, "2", *got.NewNorm)
	require.False(t, got.Uncosted)

	// Base size the usage was never graded on → no new norm, flagged uncosted.
	got = buildUsage(card, &card.Colorways[0].Usages[0], 9, names, qty)
	require.Nil(t, got.NewNorm)
	require.True(t, got.Uncosted)

	// A per-garment usage is reported as such and carries no old/new pair.
	perGarment := entity.TechCardColorwayUsage{
		BomItemId: sql.NullInt64{Int64: 1, Valid: true}, Consumption: nd("1.5"),
	}
	got = buildUsage(card, &perGarment, 4, names, qty)
	require.False(t, got.SizeGraded)
	require.Equal(t, "1.5", *got.PerGarment)
	require.Nil(t, got.OldNorm)
	require.Nil(t, got.NewNorm)
	require.False(t, got.Uncosted)
}

// TestPercentileNearestRank guards the tail. An earlier (n−1)·p/100 index reported p90 = p95 = the
// SMALLEST delta on a two-element sample — a migration report whose whole job is the tail saying
// there isn't one.
func TestPercentileNearestRank(t *testing.T) {
	two := []decimal.Decimal{decimal.RequireFromString("1"), decimal.RequireFromString("100")}
	require.Equal(t, "1", percentile(two, 50))
	require.Equal(t, "100", percentile(two, 90))
	require.Equal(t, "100", percentile(two, 95))
	require.Equal(t, "100", percentile(two, 100))

	require.Equal(t, "0", percentile(nil, 50), "an empty sample has no percentile, not a panic")

	ten := make([]decimal.Decimal, 0, 10)
	for i := 1; i <= 10; i++ {
		ten = append(ten, decimal.NewFromInt(int64(i)))
	}
	require.Equal(t, "5", percentile(ten, 50))
	require.Equal(t, "9", percentile(ten, 90))
	require.Equal(t, "10", percentile(ten, 100))
}

// TestChangedVerdictReadsRawFigures locks the classification against the rounded percentage. Both
// cases below have DeltaPct nil or "0" and are still real changes.
func TestChangedVerdictReadsRawFigures(t *testing.T) {
	fx := dto.CostingFx{Base: "EUR"}
	names := map[int]string{4: "M", 5: "L", 6: "XL"}

	// A card with no declared run had NO old figure at all; the base size gives it one.
	noRun := reportCard()
	noRun.SizeQuantities = nil
	qty, total := declared(noRun)
	got := buildColorway(noRun, oldBasisCard(noRun, qty, total), &noRun.Colorways[0], 0, fx, names, qty)
	require.Nil(t, got.OldUnitCost)
	require.NotNil(t, got.NewUnitCost)
	require.Nil(t, got.DeltaPct, "no percentage exists across an absent figure")
	require.True(t, got.BecameCosted)
	require.True(t, got.Changed, "gaining a cost is a change even with no percentage to show")

	// The reverse: a base size the recipe was never graded on takes the cost away.
	lost := reportCard()
	lost.BaseSampleSizeId = sql.NullInt32{Int32: 9, Valid: true}
	qty, total = declared(lost)
	got = buildColorway(lost, oldBasisCard(lost, qty, total), &lost.Colorways[0], 0, fx, names, qty)
	require.NotNil(t, got.OldUnitCost)
	require.Nil(t, got.NewUnitCost)
	require.True(t, got.BecameUncosted)
	require.True(t, got.Changed)
	require.False(t, got.RepricesProduct, "an uncosted colourway rewrites nothing")
}

// TestRepricesProductNeedsMoreThanProvenance: a permissive cost_price_source is not by itself a
// reprice. The figure has to exist, be in the base currency, and actually differ.
func TestRepricesProductNeedsMoreThanProvenance(t *testing.T) {
	fx := dto.CostingFx{Base: "EUR"}
	names := map[int]string{4: "M"}

	card := reportCard()
	qty, total := declared(card)

	// Stored price already equals the new figure (20) → nothing moves.
	card.Colorways[0].CostPrice = nd("20")
	got := buildColorway(card, oldBasisCard(card, qty, total), &card.Colorways[0], 0, fx, names, qty)
	require.True(t, got.SeedMayOverwrite, "NULL provenance permits a write")
	require.False(t, got.RepricesProduct, "same number is not a reprice")

	// A manual price is never overwritten, however far the card figure moved.
	manual := reportCard()
	manual.Colorways[0].CostPrice = nd("99")
	manual.Colorways[0].CostPriceSource = ns("manual")
	got = buildColorway(manual, oldBasisCard(manual, qty, total), &manual.Colorways[0], 0, fx, names, qty)
	require.False(t, got.SeedMayOverwrite)
	require.False(t, got.RepricesProduct)

	// Permitted provenance, different number → this one really moves.
	moves := reportCard()
	moves.Colorways[0].CostPrice = nd("27.50")
	moves.Colorways[0].CostPriceSource = ns("tech_card")
	got = buildColorway(moves, oldBasisCard(moves, qty, total), &moves.Colorways[0], 0, fx, names, qty)
	require.True(t, got.RepricesProduct)
}
