package entity

import (
	"database/sql"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// draftSummary is a раскладка that laid `placed` of `total` pieces on a spread of `usedLength`, with
// a two-size состав whose areas ARE recorded — i.e. the shape that would happily produce per-size
// norms if nothing stopped it.
func draftSummary(placed, total int, usedLength string) TechCardMarkerSummary {
	area := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	return TechCardMarkerSummary{
		Id:           7,
		Name:         "смешанная 40-42",
		UsedLengthCm: decimal.RequireFromString(usedLength),
		PlacedCount:  placed,
		TotalCount:   total,
		IsDraft:      placed < total,
		TotalUnits:   sql.NullInt64{Int64: 3, Valid: true},
		Composition: []MarkerCompositionEntry{
			{SizeId: 3, Quantity: 1, AreaPerGarmentCm2: area("10000")},
			{SizeId: 4, Quantity: 2, AreaPerGarmentCm2: area("11000")},
		},
	}
}

// IF THIS FAILS: a раскладка whose нестинг ran out of budget prices the garment. Every screen that
// asks «сколько ткани на изделие» — the costing band, the recipe apply dialog, the readiness gate —
// reads a length measured on a spread the missing pieces were never laid into, so the norm is short
// by exactly the cloth they would have taken, and the client copies that number into
// tech_card_colorway_usage.consumption where a release snapshot freezes it forever.
func TestADraftNamesNoConsumptionAtAll(t *testing.T) {
	full := draftSummary(45, 45, "1200")
	require.False(t, full.IsDraft)
	require.NotEmpty(t, full.ScalarNormRefusal(), "sanity: a complete MIXED marker still refuses the SCALAR")
	perSizeFull := full.PerSizeConsumption()
	require.Len(t, perSizeFull, 2)
	require.True(t, perSizeFull[0].ConsumptionCm.Valid, "a complete marker does state a per-size расход")
	require.True(t, perSizeFull[0].AreaPerGarmentCm2.Valid)

	draft := draftSummary(31, 45, "1200")
	require.True(t, draft.IsDraft)
	perSize := draft.PerSizeConsumption()
	require.Len(t, perSize, 2, "the состав survives: it is what the раскладка was ASKED to lay")
	for i, r := range perSize {
		require.Equal(t, perSizeFull[i].SizeId, r.SizeId)
		require.Equal(t, perSizeFull[i].Quantity, r.Quantity)
		require.False(t, r.ConsumptionCm.Valid, "size %d: a draft states no расход", r.SizeId)
		// The area goes with the number rather than staying behind. It is published as the BASIS a
		// client CONTINUES the formula from onto sizes the состав does not cut, so a draft's areas
		// would understate the whole размерный ряд, not just the sizes it laid.
		require.False(t, r.AreaPerGarmentCm2.Valid, "size %d: a draft states no area basis", r.SizeId)
	}
	// Withholding must not have reached back into the row: the caller keeps reading this summary.
	require.True(t, draft.Composition[0].AreaPerGarmentCm2.Valid, "the stored состав must not be blanked in place")
}

// IF THIS FAILS: a HOMOGENEOUS draft goes out with a norm. The mixed-состав refusal cannot catch it —
// one состав line reads as honest — so the mean of a spread that dropped pieces would be emitted as
// «сколько ткани на изделие» with nothing beside it saying otherwise. The draft refusal has to be
// asked FIRST and independently of how many sizes the раскладка cuts.
func TestAHomogeneousDraftIsRefusedTooAndTheRefusalNamesTheArithmetic(t *testing.T) {
	homogeneous := TechCardMarkerSummary{
		Name:         "основная 42",
		UsedLengthCm: decimal.RequireFromString("512.4"),
		PlacedCount:  31,
		TotalCount:   45,
		IsDraft:      true,
		SizeId:       sql.NullInt64{Int64: 3, Valid: true},
		Sets:         sql.NullInt64{Int64: 4, Valid: true},
	}
	require.Empty(t, MarkerScalarNormRefusal(homogeneous.Name, MarkerPerSizeConsumption(
		homogeneous.CompositionOrLegacy(), homogeneous.UsedLengthCm)),
		"sanity: the mixed-состав rule has nothing to say about one size — which is why it cannot be the guard")

	refusal := homogeneous.ScalarNormRefusal()
	require.NotEmpty(t, refusal)
	require.Contains(t, refusal, "31", "the refusal must name how many pieces were laid")
	require.Contains(t, refusal, "45", "and out of how many")
	require.Contains(t, refusal, "search budget", "and the action: raise the search budget and re-run")
}

// IF THIS FAILS: a черновик becomes the effective НОРМА of a cloth. chk_tcm_draft_not_norm (0299)
// makes the pair unstorable, so this can only be reached by a path that bypassed the schema — and
// SelectNorm's tiebreak is «newest updated_at wins», which hands a fresh draft the norm over the
// measured раскладка it was re-run from. Every reader (card read, single-marker read, readiness
// gate) resolves the norm through NormPeersOf, so dropping it here is what makes the invariant hold
// in Go regardless of the CHECK.
func TestNormPeersOfDropsADraftEvenWhenItSomehowCarriesTheFlag(t *testing.T) {
	bom := sql.NullInt64{Int64: 11, Valid: true}
	measured := TechCardMarkerSummary{
		Id: 1, Name: "измеренная", IsNorm: true, BomItemId: bom,
		UpdatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
	draft := TechCardMarkerSummary{
		Id: 2, Name: "черновик", IsNorm: true, IsDraft: true, BomItemId: bom,
		PlacedCount: 31, TotalCount: 45,
		UpdatedAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
	}

	peers := NormPeersOf([]TechCardMarkerSummary{measured, draft})
	require.Len(t, peers, 1)
	require.Equal(t, 1, peers[0].Id, "the measured раскладка is the norm; the newer draft is not a contender")

	scope := MarkerNormScope(measured)
	winner, contenders, ok := SelectNorm(peers, scope)
	require.True(t, ok)
	require.Equal(t, 1, winner.Id)
	require.Len(t, contenders, 1)
	// And the card must not be told it has two norms: the draft was never one, so «две нормы на одной
	// ткани» would be an alarm about a state that does not exist.
	require.Empty(t, NormConflictReport(peers, scope))
}
