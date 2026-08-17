package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// IF THIS FAILS: the СОГЛАСИЕ never reaches the store, so an incomplete layout is refused exactly as
// before and the whole phase is inert — minutes of the only нестинг executor we have keep being
// thrown away with nothing to show for the run. The flag is carried VERBATIM and is never
// cross-checked against placed/total here: the column is derived from those counters by the store,
// and this field only answers «принять неполную или отказать». What IS checked against the geometry
// is placed_count itself — see TestPlacedCountIsCountedOffTheLayoutRatherThanBelieved.
func TestADraftConsentCrossesTheWireIntoTheSavePayload(t *testing.T) {
	pb := validMarkerInsertPb()
	require.False(t, pb.IsDraft, "sanity: a stale bundle that never heard of drafts sends false")
	out, err := ConvertPbTechCardMarkerInsertToEntity(pb)
	require.NoError(t, err)
	require.False(t, out.IsDraft, "and therefore keeps the old refusal — it cannot mint a draft by omission")

	pb.IsDraft = true
	pb.PlacedCount = 31
	pb.TotalCount = 45
	out, err = ConvertPbTechCardMarkerInsertToEntity(pb)
	require.NoError(t, err)
	require.True(t, out.IsDraft)
	require.Equal(t, 31, out.PlacedCount)
	require.Equal(t, 45, out.TotalCount)

	// Consent on a COMPLETE layout is accepted and means nothing — the store stores an ordinary
	// marker. Refusing it here would fail the happy path of a re-run that finally laid everything
	// while the client's toggle was still on.
	pb.PlacedCount, pb.TotalCount = 45, 45
	out, err = ConvertPbTechCardMarkerInsertToEntity(pb)
	require.NoError(t, err)
	require.True(t, out.IsDraft, "dto carries it; deciding what it MEANS is the store's job")
}

// IF THIS FAILS: placed_count is whatever the client says it is — and it is the number that decides
// draft versus measured (0299). A payload claiming «уложено 45» over 31 real placements saves an
// ORDINARY раскладка: every draft guard sees a complete marker, the mean of a spread that dropped
// fourteen pieces goes out as consumption_per_unit_cm, and it can be designated the norm. The blob is
// on this very request and the server is about to store it, so counting beats believing.
func TestPlacedCountIsCountedOffTheLayoutRatherThanBelieved(t *testing.T) {
	with := func(placed, total int32, placements int) *pb_common.TechCardMarkerInsert {
		pb := validMarkerInsertPb()
		pb.PlacedCount, pb.TotalCount = placed, total
		pb.Layout = &pb_common.TechCardMarkerLayout{
			SchemaVersion: 4,
			Composition:   []*pb_common.TechCardMarkerCompositionEntry{{SizeId: 3, Quantity: 4}},
			Placements:    markerPlacements(placements),
		}
		return pb
	}

	_, err := ConvertPbTechCardMarkerInsertToEntity(with(12, 12, 12))
	require.NoError(t, err, "the honest payload the live client sends: placedCount = placements.length")

	_, err = ConvertPbTechCardMarkerInsertToEntity(with(45, 45, 31))
	require.Error(t, err, "an incomplete layout dressed as a complete one")
	require.Contains(t, err.Error(), "placed_count is 45")
	require.Contains(t, err.Error(), "31 placements")

	// The other direction is refused too: overstating the shortfall would let a client mark a measured
	// раскладка a draft and quietly withdraw its норма from every screen.
	_, err = ConvertPbTechCardMarkerInsertToEntity(with(5, 45, 31))
	require.Error(t, err)

	// AND HERE IS THE HALF THAT IS STILL OPEN — pinned deliberately, not overlooked. total_count is the
	// client's claim: piece multiplicity lives on the card, not in the blob, so a payload that
	// understates the denominator still passes an incomplete раскладка off as complete. 31 placements
	// against a truthful denominator of 45 is a draft; declaring 31 makes it a measured norm, and this
	// converter accepts it. When a later phase closes the denominator (a blob that keeps its UNPLACED
	// pieces makes Σ quantity × состав derivable), this assertion is the one that must be inverted.
	_, err = ConvertPbTechCardMarkerInsertToEntity(with(31, 31, 31))
	require.NoError(t, err, "known debt inherited from the pre-0299 refusal — see ConvertPbTechCardMarkerInsertToEntity")
}

// IF THIS FAILS: a черновик is indistinguishable on the wire from a раскладка that simply has not
// been costed yet — every other field looks the same, and an absent number reads as «ещё не
// посчитали» rather than «считать нечего». The screen would offer a re-run of nothing, or worse,
// leave the operator waiting for a figure that will never arrive.
func TestDraftSummaryCarriesTheFlagAndWithholdsEveryNumber(t *testing.T) {
	area := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	m := entity.TechCardMarkerSummary{
		Id:           7,
		Name:         "смешанная 40-42",
		UsedLengthCm: decimal.RequireFromString("1200"),
		// A GENUINELY COMPLETE row: every piece laid, is_draft false because the counters agree. The
		// control has to be the real thing — a fixture that says «полная» while carrying 31 of 45 would
		// be asserting the very confusion this phase exists to remove.
		PlacedCount: 45,
		TotalCount:  45,
		TotalUnits:  sql.NullInt64{Int64: 3, Valid: true},
		Composition: []entity.MarkerCompositionEntry{
			{SizeId: 3, Quantity: 1, AreaPerGarmentCm2: area("10000")},
			{SizeId: 4, Quantity: 2, AreaPerGarmentCm2: area("11000")},
		},
	}

	// It DOES publish per-size numbers and their area basis. Without this half the assertions below
	// would pass on a mapper that simply never emits anything.
	complete := TechCardMarkerSummaryToPb(m)
	require.False(t, complete.IsDraft)
	require.Len(t, complete.Composition, 2)
	require.NotNil(t, complete.Composition[0].ConsumptionPerUnitCm)
	require.NotNil(t, complete.Composition[0].AreaPerGarmentCm2)

	// The same раскладка after a re-run that ran out of budget: fourteen pieces short.
	m.PlacedCount, m.IsDraft = 31, true
	draft := TechCardMarkerSummaryToPb(m)
	require.True(t, draft.IsDraft, "the client cannot label it a draft without being told")
	require.Nil(t, draft.ConsumptionPerUnitCm, "the scalar norm a client copies into a recipe")
	require.Len(t, draft.Composition, 2, "the состав stays: it is what the раскладка was asked to lay")
	for _, c := range draft.Composition {
		require.Nil(t, c.ConsumptionPerUnitCm, "size %d: the per-size norm a MIXED раскладка is applied by", c.SizeId)
		require.Nil(t, c.AreaPerGarmentCm2, "size %d: the basis a client continues the formula from", c.SizeId)
		require.NotZero(t, c.Quantity)
	}
	require.NotEmpty(t, draft.ScalarApplyRefusal, "an absent number must say WHY, or it reads as «not computed yet»")
	require.Contains(t, draft.ScalarApplyRefusal, "31")
	require.Contains(t, draft.ScalarApplyRefusal, "45")
	require.Contains(t, draft.ScalarApplyRefusal, "search budget")
	// The measurements themselves still travel — the operator has to SEE what the run produced in
	// order to decide whether re-running is worth it.
	require.Equal(t, int32(31), draft.PlacedCount)
	require.Equal(t, int32(45), draft.TotalCount)
	require.Equal(t, "1200", draft.UsedLengthCm.Value)
}
