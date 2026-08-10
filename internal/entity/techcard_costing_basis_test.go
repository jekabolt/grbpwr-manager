package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// These tests pin the costing basis arithmetic at its source (T6): a style's standard cost takes a
// size-graded norm as the SIMPLE AVERAGE over the card's DECLARED size range — the whole range or
// nothing — and the basis is three-valued (range average | concrete size | none), so «no size» can
// never relax into the average. See TechCardColorwayUsage.UnitTotal for the history of the two
// retired bases; every assertion below exists to keep their fallbacks from creeping back in.

func cbCons(sizeID int, v string) TechCardBomSizeConsumption {
	return TechCardBomSizeConsumption{SizeId: sizeID, Consumption: decimal.RequireFromString(v)}
}

func cbBom() *TechCardBomItem {
	return &TechCardBomItem{
		UnitPrice:      decimal.NewNullDecimal(decimal.RequireFromString("10")),
		WastagePercent: decimal.NewNullDecimal(decimal.RequireFromString("10")),
	}
}

func cbGraded() TechCardColorwayUsage {
	return TechCardColorwayUsage{SizeConsumptions: []TechCardBomSizeConsumption{
		cbCons(4, "2"), cbCons(5, "2.5"), cbCons(6, "3"),
	}}
}

// TestRangeAverageTotal is the definition itself: Σ norm(s) / |range| over EVERY size of the
// declared range, × price, grossed by the article's wastage.
func TestRangeAverageTotal(t *testing.T) {
	bom := cbBom()
	graded := cbGraded()

	// (2 + 2.5 + 3) / 3 = 2.5 → 2.5 × 10 × 1.1 = 27.5.
	got := graded.RangeAverageTotal(bom, []int{4, 5, 6})
	require.True(t, got.Valid)
	require.Equal(t, "27.5", got.Decimal.String())

	// A single-size range is the degenerate case of the same formula: the average IS that norm.
	one := graded.RangeAverageTotal(bom, []int{6})
	require.True(t, one.Valid)
	require.Equal(t, "33", one.Decimal.String())
	require.Equal(t, graded.SizeNormTotal(bom, 6).Decimal.String(), one.Decimal.String(),
		"a one-size range must agree with pricing that size directly")

	// A norm on a size OUTSIDE the declared range neither joins the average nor blocks it: the
	// range is the only carrier of the set (legacy tails after a range edit are inert).
	tailed := graded
	tailed.SizeConsumptions = append(append([]TechCardBomSizeConsumption(nil),
		tailed.SizeConsumptions...), cbCons(99, "100"))
	tailedAvg := tailed.RangeAverageTotal(bom, []int{4, 5, 6})
	require.True(t, tailedAvg.Valid)
	require.Equal(t, "27.5", tailedAvg.Decimal.String())

	// Marker-sourced norms are never grossed (the measured lay already contains the cutting
	// waste); the range average must not reopen that double-count.
	marker := cbGraded()
	marker.ConsumptionSource = sql.NullString{String: ConsumptionSourceMarker, Valid: true}
	mAvg := marker.RangeAverageTotal(bom, []int{4, 5, 6})
	require.True(t, mAvg.Valid)
	require.Equal(t, "25", mAvg.Decimal.String())
}

// TestRangeAverageWholeRangeOrNothing pins the coverage rule: a range size with no norm makes the
// whole line uncosted — the average over the graded subset is the forbidden fallback (an only-XS
// average would silently understate the style). The missing sizes are named, poimённо.
func TestRangeAverageWholeRangeOrNothing(t *testing.T) {
	bom := cbBom()
	graded := cbGraded() // norms on 4/5/6

	require.False(t, graded.RangeAverageTotal(bom, []int{4, 5, 6, 7}).Valid,
		"a range size with no norm must make the line uncosted, never averaged over the rest")
	require.Equal(t, []int{7}, graded.MissingRangeNorms([]int{4, 5, 6, 7}))

	partial := TechCardColorwayUsage{SizeConsumptions: []TechCardBomSizeConsumption{cbCons(5, "2.5")}}
	require.False(t, partial.RangeAverageTotal(bom, []int{4, 5, 6}).Valid)
	require.Equal(t, []int{4, 6}, partial.MissingRangeNorms([]int{4, 5, 6}),
		"missing sizes are named in declared-range order")

	// An EMPTY range is a different hole with the same verdict: nothing to average over.
	require.False(t, graded.RangeAverageTotal(bom, nil).Valid,
		"a card with no declared size range prices no graded norm")
	require.Nil(t, graded.MissingRangeNorms(nil),
		"an empty range reports no missing sizes — it is a missing RANGE, a different caveat")

	// Full coverage reports nothing missing.
	require.Nil(t, graded.MissingRangeNorms([]int{4, 5, 6}))

	// And the money guards stay: no price → no number; piece-bound row → no norm-money.
	require.False(t, graded.RangeAverageTotal(&TechCardBomItem{}, []int{4, 5, 6}).Valid)
	piece := cbGraded()
	piece.PieceId = sql.NullInt64{Int64: 9, Valid: true}
	require.False(t, piece.RangeAverageTotal(bom, []int{4, 5, 6}).Valid)
}

// TestCostingBasisResolver pins the THREE-valued basis and the one resolver. The zero override is
// the load-bearing state: «no size» must resolve to NO basis, never to the style default — a run
// line with no size would otherwise silently take the range-average price.
func TestCostingBasisResolver(t *testing.T) {
	tc := &TechCardInsert{SizeIds: []int{4, 5, 6}}
	tc.BaseSampleSizeId = sql.NullInt32{Int32: 5, Valid: true} // reference field; must not be read

	// Default: the style basis — the average over the declared range.
	basis := tc.CostingBasis()
	require.Equal(t, CostingBasisRangeAverage, basis.Mode)
	require.Equal(t, []int{4, 5, 6}, basis.RangeSizeIds)

	// Override > 0: one concrete size (a production-run cell).
	six := 6
	tc.CostingSizeOverride = &six
	basis = tc.CostingBasis()
	require.Equal(t, CostingBasisSize, basis.Mode)
	require.Equal(t, 6, basis.SizeID)

	// Override == 0: NO basis. The regression this three-valuedness exists for.
	zero := 0
	tc.CostingSizeOverride = &zero
	require.Equal(t, CostingBasisNone, tc.CostingBasis().Mode,
		"«no size» must be a real state, not a spelling of the default")

	// A nil card resolves to no basis too.
	var nilTc *TechCardInsert
	require.Equal(t, CostingBasisNone, nilTc.CostingBasis().Mode)
}

// TestUnitTotalOnBasis walks UnitTotal through the three modes and pins the precedence rules that
// did not move: a per-garment norm is basis-blind, and a per-size grading beats a stray scalar.
func TestUnitTotalOnBasis(t *testing.T) {
	bom := cbBom()
	graded := cbGraded()
	avg := CostingBasis{Mode: CostingBasisRangeAverage, RangeSizeIds: []int{4, 5, 6}}

	got := graded.UnitTotal(bom, avg)
	require.True(t, got.Valid)
	require.Equal(t, "27.5", got.Decimal.String(), "style basis: the range average")

	got = graded.UnitTotal(bom, CostingBasis{Mode: CostingBasisSize, SizeID: 6})
	require.True(t, got.Valid)
	require.Equal(t, "33", got.Decimal.String(), "run-cell basis: that size's own norm")

	require.False(t, graded.UnitTotal(bom, CostingBasis{Mode: CostingBasisNone}).Valid,
		"no basis prices no graded norm — never the average, never a size")

	// A per-garment norm is basis-blind: same figure under every mode.
	flat := TechCardColorwayUsage{Consumption: decimal.NewNullDecimal(decimal.RequireFromString("2"))}
	for _, b := range []CostingBasis{avg, {Mode: CostingBasisSize, SizeID: 6}, {Mode: CostingBasisNone}} {
		ft := flat.UnitTotal(bom, b)
		require.True(t, ft.Valid)
		require.Equal(t, "22", ft.Decimal.String(), "2×10×1.1 whatever the basis")
	}

	// A graded norm beats a stray scalar on the same row, exactly as before: LineTotal refuses a
	// row with SizeConsumptions, so the money comes from the grading, not the leftover number.
	both := cbGraded()
	both.Consumption = decimal.NewNullDecimal(decimal.RequireFromString("99"))
	bt := both.UnitTotal(bom, avg)
	require.True(t, bt.Valid)
	require.Equal(t, "27.5", bt.Decimal.String(), "per-size grading wins over the scalar")
}
