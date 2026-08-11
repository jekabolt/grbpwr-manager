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

// reportCard: declared range 4/5/6, base sample size 4, one graded fabric (2 / 2.5 / 3 m at 10
// EUR). Old basis (base size): 2 × 10 = 20. New basis (range average): 2.5 × 10 = 25.
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

// TestOldBaseSizeNormReproducesOutgoingBasis pins the "before" column. The report is only worth
// reading if its old number is the number the system used to produce — an approximation would make
// every delta it prints a fiction. The outgoing basis: the base sample size's norm, strictly.
func TestOldBaseSizeNormReproducesOutgoingBasis(t *testing.T) {
	card := reportCard()
	u := &card.Colorways[0].Usages[0]

	got, ok := oldBaseSizeNorm(u, oldBaseSizeID(card))
	require.True(t, ok)
	require.Equal(t, "2", got.String())

	// No base size at all → the outgoing basis produced nothing. On beta that is EVERY card.
	_, ok = oldBaseSizeNorm(u, 0)
	require.False(t, ok)

	// A base size this usage was never graded on → nothing either, exactly as the outgoing
	// UnitTotal answered.
	_, ok = oldBaseSizeNorm(u, 9)
	require.False(t, ok)
}

// TestOldBasisCardPricesThroughLiveCosting proves the shadow-card trick: running the CURRENT
// costing code (now the range average) over the rewritten card yields the outgoing figure, so the
// report never needs a second copy of the costing math to compare against.
func TestOldBasisCardPricesThroughLiveCosting(t *testing.T) {
	card := reportCard()
	fx := dto.CostingFx{Base: "EUR"}

	// New basis: the range average (2+2.5+3)/3 = 2.5 → 25.
	newCost, ccy := dto.ComputeColorwayUnitCost(card, 55, fx)
	require.True(t, newCost.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "25", newCost.Decimal.String())

	// Old basis via the shadow: base size 4 → 2 × 10 = 20.
	old := oldBasisCard(card)
	oldCost, oldCcy := dto.ComputeColorwayUnitCost(old, 55, fx)
	require.True(t, oldCost.Valid)
	require.Equal(t, "EUR", oldCcy)
	require.Equal(t, "20", oldCost.Decimal.String())

	// The shadow must not mutate the caller's card — the report prices both from the same load.
	require.Len(t, card.Colorways[0].Usages[0].SizeConsumptions, 3)
	require.False(t, card.Colorways[0].Usages[0].Consumption.Valid)

	// A card with no base size had NO old figure but HAS a new one (the beta-wide case). The
	// report must show that as an appearance, not silently as a zero.
	noBase := reportCard()
	noBase.BaseSampleSizeId = sql.NullInt32{}
	oldCost, _ = dto.ComputeColorwayUnitCost(oldBasisCard(noBase), 55, fx)
	require.False(t, oldCost.Valid)
	newCost, _ = dto.ComputeColorwayUnitCost(noBase, 55, fx)
	require.True(t, newCost.Valid)

	// And the reverse: coverage that satisfied the base size but not the whole range loses its
	// figure — the count the summary must carry as BecameUncosted.
	gap := reportCard()
	gap.SizeIds = append(gap.SizeIds, 7)
	oldCost, _ = dto.ComputeColorwayUnitCost(oldBasisCard(gap), 55, fx)
	require.True(t, oldCost.Valid, "the outgoing basis never looked past the base size")
	newCost, _ = dto.ComputeColorwayUnitCost(gap, 55, fx)
	require.False(t, newCost.Valid, "the range average requires the whole range")
}

// TestBuildUsageReportsBothNorms checks the row a human reads: the graded norms, the base-size
// norm (old), the range average (new), and the uncosted verdict when the range is not covered.
func TestBuildUsageReportsBothNorms(t *testing.T) {
	card := reportCard()
	names := map[int]string{4: "M", 5: "L", 6: "XL"}

	got := buildUsage(card, &card.Colorways[0].Usages[0], oldBaseSizeID(card), names)
	require.True(t, got.SizeGraded)
	require.Equal(t, "Shell", got.Material)
	require.Len(t, got.NormsBySize, 3)
	require.Equal(t, "M", got.NormsBySize[0].SizeName)
	require.True(t, got.NormsBySize[0].InDeclaredRange)
	require.Equal(t, "2", *got.OldNorm)
	require.Equal(t, "2.5", *got.NewNorm)
	require.False(t, got.Uncosted)

	// No base size → no old norm; the new side stands on its own (the beta-wide appearance).
	got = buildUsage(card, &card.Colorways[0].Usages[0], 0, names)
	require.Nil(t, got.OldNorm)
	require.Equal(t, "2.5", *got.NewNorm)
	require.False(t, got.Uncosted)

	// A range size with no norm → no new norm, flagged uncosted (whole-range-or-nothing); the
	// norm on the stray size is reported but marked outside the range.
	gap := reportCard()
	gap.SizeIds = []int{4, 5, 6, 7}
	got = buildUsage(gap, &gap.Colorways[0].Usages[0], oldBaseSizeID(gap), names)
	require.Equal(t, "2", *got.OldNorm)
	require.Nil(t, got.NewNorm)
	require.True(t, got.Uncosted)

	// A per-garment usage is reported as such and carries no old/new pair.
	perGarment := entity.TechCardColorwayUsage{
		BomItemId: sql.NullInt64{Int64: 1, Valid: true}, Consumption: nd("1.5"),
	}
	got = buildUsage(card, &perGarment, oldBaseSizeID(card), names)
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
// cases below have DeltaPct nil and are still real changes.
func TestChangedVerdictReadsRawFigures(t *testing.T) {
	fx := dto.CostingFx{Base: "EUR"}
	names := map[int]string{4: "M", 5: "L", 6: "XL"}

	// A card with no base size had NO old figure at all; the range average gives it one.
	noBase := reportCard()
	noBase.BaseSampleSizeId = sql.NullInt32{}
	got := buildColorway(noBase, oldBasisCard(noBase), &noBase.Colorways[0], 0, fx, names, true)
	require.Nil(t, got.OldUnitCost)
	require.NotNil(t, got.NewUnitCost)
	require.Nil(t, got.DeltaPct, "no percentage exists across an absent figure")
	require.True(t, got.BecameCosted)
	require.True(t, got.Changed, "gaining a cost is a change even with no percentage to show")

	// The reverse: a range grown past the grading takes the cost away.
	lost := reportCard()
	lost.SizeIds = append(lost.SizeIds, 7)
	got = buildColorway(lost, oldBasisCard(lost), &lost.Colorways[0], 0, fx, names, true)
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

	// Stored price already equals the new figure (25) → nothing moves.
	card := reportCard()
	card.Colorways[0].CostPrice = nd("25")
	got := buildColorway(card, oldBasisCard(card), &card.Colorways[0], 0, fx, names, true)
	require.True(t, got.SeedMayOverwrite, "NULL provenance permits a write")
	require.False(t, got.RepricesProduct, "same number is not a reprice")

	// A manual price is never overwritten, however far the card figure moved.
	manual := reportCard()
	manual.Colorways[0].CostPrice = nd("99")
	manual.Colorways[0].CostPriceSource = ns("manual")
	got = buildColorway(manual, oldBasisCard(manual), &manual.Colorways[0], 0, fx, names, true)
	require.False(t, got.SeedMayOverwrite)
	require.False(t, got.RepricesProduct)

	// Permitted provenance, different number → this one really moves (20 stored, 25 computed).
	moves := reportCard()
	moves.Colorways[0].CostPrice = nd("20")
	moves.Colorways[0].CostPriceSource = ns("tech_card")
	got = buildColorway(moves, oldBasisCard(moves), &moves.Colorways[0], 0, fx, names, true)
	require.True(t, got.RepricesProduct)
}

// TestRepricesProductNeedsOwnership is the review fix (MAJOR 3): the live seed's UPDATE requires
// product.primary_tech_card_id = this card ON TOP of provenance, so a colourway product owned by
// another card is a guaranteed no-op and must not be counted — this report is the only place a
// human sees the price of the change before prod, and it must not overstate it.
func TestRepricesProductNeedsOwnership(t *testing.T) {
	fx := dto.CostingFx{Base: "EUR"}
	names := map[int]string{4: "M"}

	// Same figures as the "moves" case above — permissive provenance, 20 stored vs 25 computed —
	// with the ONE difference that this card is not the product's primary.
	moves := reportCard()
	moves.Colorways[0].CostPrice = nd("20")
	moves.Colorways[0].CostPriceSource = ns("tech_card")
	got := buildColorway(moves, oldBasisCard(moves), &moves.Colorways[0], 0, fx, names, false)
	require.True(t, got.SeedMayOverwrite, "provenance alone still permits the write")
	require.False(t, got.OwnedByThisCard)
	require.False(t, got.RepricesProduct, "a non-primary card reprices nothing, however far the figure moved")
}

// TestUsageDetailSkipsPieceAssignments is the review fix (MINOR 5): a piece-bound row carries no
// norm and is skipped by BOTH costing sides (T8), so the detail must not list it with an old→new
// pair — the one row that cannot reprice would read as if it just did.
func TestUsageDetailSkipsPieceAssignments(t *testing.T) {
	fx := dto.CostingFx{Base: "EUR"}
	names := map[int]string{4: "M", 5: "L", 6: "XL"}

	card := reportCard()
	card.Colorways[0].Usages = append(card.Colorways[0].Usages, entity.TechCardColorwayUsage{
		BomItemId: sql.NullInt64{Int64: 1, Valid: true},
		PieceId:   sql.NullInt64{Int64: 9, Valid: true}, // назначение материала детали, T8
		// Легаси-числа на строке-назначении: деньги их не читают, и отчёт не имеет права их печатать.
		SizeConsumptions: []entity.TechCardBomSizeConsumption{sc(4, "1"), sc(5, "1"), sc(6, "1")},
	})

	got := buildColorway(card, oldBasisCard(card), &card.Colorways[0], 0, fx, names, true)
	require.Len(t, got.Usages, 1, "the piece-bound row must not appear in the detail")
	require.Equal(t, "Shell", got.Usages[0].Material)

	// And the money agrees with the detail: the legacy numbers on the piece row moved nothing.
	clean := reportCard()
	cleanCw := buildColorway(clean, oldBasisCard(clean), &clean.Colorways[0], 0, fx, names, true)
	require.Equal(t, *cleanCw.NewUnitCost, *got.NewUnitCost)
	require.Equal(t, *cleanCw.OldUnitCost, *got.OldUnitCost)
}
