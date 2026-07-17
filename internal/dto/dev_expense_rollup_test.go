package dto

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestComputeTechCardDevCostSummaryRollup covers the Q8 rollup: dev spend attributed to fitting
// rounds (via fitting_id → round_number), rounds-to-approval, and the time-to-approval timeline.
func TestComputeTechCardDevCostSummaryRollup(t *testing.T) {
	nt := func(s string) sql.NullTime { return sql.NullTime{Time: mustDay(s), Valid: true} }
	fid := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

	card := &entity.TechCard{Id: 7}
	fittings := []entity.Fitting{
		{Id: 10, FittingInsert: entity.FittingInsert{RoundNumber: fid(1), Outcome: nstr("new_round"), FittingDate: mustDay("2026-01-10")}},
		{Id: 11, FittingInsert: entity.FittingInsert{RoundNumber: fid(2), Outcome: nstr("approved"), FittingDate: mustDay("2026-02-15")}},
	}
	expenses := []entity.TechCardDevExpense{
		{Kind: "sample", AmountBase: nd("100"), FittingId: fid(10), IncurredAt: nt("2026-01-05")}, // round 1
		{Kind: "materials", AmountBase: nd("50"), FittingId: fid(11), IncurredAt: nt("2026-01-20")}, // round 2
		{Kind: "labour", AmountBase: nd("30")},                                                       // no fitting → round 0
		{Kind: "other", AmountBase: nd("20"), FittingId: fid(99)},                                    // unknown fitting → round 0
	}

	sum := ComputeTechCardDevCostSummary(card, expenses, fittings, CostingFx{Base: "EUR"})
	require.Equal(t, "200", sum.TotalBase.Value)

	// by_round sorted ascending: 0 → 50 (labour+other), 1 → 100, 2 → 50.
	require.Len(t, sum.ByRound, 3)
	require.Equal(t, int32(0), sum.ByRound[0].RoundNumber)
	require.Equal(t, "50", sum.ByRound[0].AmountBase.Value)
	require.Equal(t, int32(2), sum.ByRound[0].ExpenseCount)
	require.Equal(t, int32(1), sum.ByRound[1].RoundNumber)
	require.Equal(t, "100", sum.ByRound[1].AmountBase.Value)
	require.Equal(t, int32(2), sum.ByRound[2].RoundNumber)
	require.Equal(t, "50", sum.ByRound[2].AmountBase.Value)

	// approved at round 2 on 2026-02-15; earliest expense incurred 2026-01-05 → 41 days.
	require.Equal(t, int32(2), sum.RoundsToApproval)
	require.NotNil(t, sum.ApprovedAt)
	require.NotNil(t, sum.FirstExpenseAt)
	require.Equal(t, int32(41), sum.DaysToApproval)
}

// TestComputeTechCardDevCostSummaryNoApproval: rounds-to-approval stays 0 and no approval timeline
// when nothing is approved yet, but by-round attribution still works.
func TestComputeTechCardDevCostSummaryNoApproval(t *testing.T) {
	fid := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }
	card := &entity.TechCard{Id: 7}
	fittings := []entity.Fitting{
		{Id: 10, FittingInsert: entity.FittingInsert{RoundNumber: fid(1), Outcome: nstr("new_round"), FittingDate: mustDay("2026-01-10")}},
	}
	expenses := []entity.TechCardDevExpense{
		{Kind: "sample", AmountBase: nd("100"), FittingId: fid(10)},
	}
	sum := ComputeTechCardDevCostSummary(card, expenses, fittings, CostingFx{Base: "EUR"})
	require.Equal(t, int32(0), sum.RoundsToApproval)
	require.Nil(t, sum.ApprovedAt)
	require.Equal(t, int32(0), sum.DaysToApproval)
	require.Len(t, sum.ByRound, 1)
	require.Equal(t, int32(1), sum.ByRound[0].RoundNumber)
}

func mustDay(s string) time.Time {
	tt, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return tt
}
