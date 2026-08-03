package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// planGuardRun is a received run whose actuals are EUR 900 for 90 units (unit cost 10) and whose
// planned unit cost is `plan` in `currency`. With a PLN plan of 142.50 the old arithmetic reported a
// "saving" of roughly 12k that was nothing but the zloty/euro rate.
func planGuardRun(plan string, currency string) *entity.ProductionRun {
	r := &entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{
		PlannedUnitCost: nd2(plan),
		Lines: []entity.ProductionRunLine{
			{ProductId: ni32(11), SizeId: 1, PlannedQty: 100, ReceivedQty: ni(90)},
		},
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostCMT, Amount: d("900"), Currency: "EUR", AmountBase: nd2("900")},
		},
	}}
	if currency != "" {
		r.PlannedCurrency = sql.NullString{String: currency, Valid: true}
	}
	return r
}

// TestRunVarianceRequiresBaseCurrencyPlan: actual cost is always base, so a plan snapshot in the
// costing currency may not be subtracted from it.
func TestRunVarianceRequiresBaseCurrencyPlan(t *testing.T) {
	base := ConvertEntityProductionRunToPb(planGuardRun("12", "eur")).Actuals // case-insensitive
	require.NotNil(t, base.PlannedTotalBase)
	require.Equal(t, "1080", base.PlannedTotalBase.Value) // 12 × 90
	require.Equal(t, "-180", base.TotalVariance.Value)    // 900 − 1080
	require.Equal(t, "-2", base.UnitCostVariance.Value)   // 10 − 12

	foreign := ConvertEntityProductionRunToPb(planGuardRun("142.50", "PLN")).Actuals
	require.Nil(t, foreign.PlannedTotalBase, "a PLN plan is not a base-currency budget")
	require.Nil(t, foreign.TotalVariance)
	require.Nil(t, foreign.UnitCostVariance)
	require.Equal(t, "10", foreign.ActualUnitCost.Value, "the actual itself is unaffected")

	unknown := ConvertEntityProductionRunToPb(planGuardRun("12", "")).Actuals
	require.Nil(t, unknown.TotalVariance, "an unrecorded plan currency is unverifiable, not assumed base")
}

// TestStyleSummaryVarianceRequiresBaseCurrencyPlan: the style roll-up shares the run-level rule, so
// the two screens cannot disagree about whether a style is over or under budget.
func TestStyleSummaryVarianceRequiresBaseCurrencyPlan(t *testing.T) {
	runs := []entity.ProductionRun{*planGuardRun("12", "EUR")}
	sum := ComputeStyleProductionSummary(runs, decimal.Zero, false)
	require.Equal(t, "1200", sum.PlannedCostBase.Value) // 12 × 100 planned
	require.NotNil(t, sum.CostVariance)
	require.Equal(t, "-300", sum.CostVariance.Value) // 900 − 1200

	foreign := ComputeStyleProductionSummary([]entity.ProductionRun{*planGuardRun("142.50", "PLN")}, decimal.Zero, false)
	require.Equal(t, "0", foreign.PlannedCostBase.Value)
	require.Nil(t, foreign.CostVariance, "no comparable plan ⇒ no variance, not actual−0")
	require.Equal(t, "900", foreign.ActualCostBase.Value)
}
