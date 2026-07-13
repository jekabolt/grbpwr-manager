package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func nd(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

// TestComputeStyleProductionSummary checks the plan/fact roll-up across a style's runs: planned =
// Σ(planned_unit_cost × planned_qty) over runs that carry a plan; actual = Σ valid amount_base;
// variance = actual − planned; a run whose planned cost is unset contributes qty but no plan €.
func TestComputeStyleProductionSummary(t *testing.T) {
	runs := []entity.ProductionRun{
		{
			ProductionRunInsert: entity.ProductionRunInsert{
				PlannedUnitCost: nd("3.00"),
				Sizes: []entity.ProductionRunSize{
					{SizeId: 1, PlannedQty: 5, ReceivedQty: sql.NullInt64{Int64: 5, Valid: true}},
					{SizeId: 2, PlannedQty: 5, ReceivedQty: sql.NullInt64{Int64: 3, Valid: true}},
				},
				Costs: []entity.ProductionRunCost{
					{Kind: entity.ProductionRunCostKind("cmt"), AmountBase: nd("25.00")},
				},
			},
		},
		{
			ProductionRunInsert: entity.ProductionRunInsert{
				// no frozen plan cost → contributes qty but no planned €
				PlannedUnitCost: decimal.NullDecimal{},
				Sizes: []entity.ProductionRunSize{
					{SizeId: 1, PlannedQty: 20},
				},
				Costs: []entity.ProductionRunCost{
					{Kind: entity.ProductionRunCostKind("materials"), AmountBase: decimal.NullDecimal{}}, // unconverted → skipped
					{Kind: entity.ProductionRunCostKind("logistics"), AmountBase: nd("10.00")},
				},
			},
		},
	}

	got := ComputeStyleProductionSummary(runs)
	require.EqualValues(t, 2, got.Runs)
	require.EqualValues(t, 30, got.PlannedQtyTotal, "10 + 20")
	require.EqualValues(t, 8, got.ReceivedQtyTotal, "5 + 3")
	require.True(t, decimal.RequireFromString(got.PlannedCostBase.Value).Equal(decimal.NewFromInt(30)), "3.00 × 10, got %s", got.PlannedCostBase.Value)
	require.True(t, decimal.RequireFromString(got.ActualCostBase.Value).Equal(decimal.NewFromInt(35)), "25 + 10, got %s", got.ActualCostBase.Value)
	require.True(t, decimal.RequireFromString(got.CostVariance.Value).Equal(decimal.NewFromInt(5)), "35 − 30, got %s", got.CostVariance.Value)
	require.True(t, got.HasActuals)
}

// TestComputeStyleProductionSummaryEmpty: no runs → all-zero, has_actuals false.
func TestComputeStyleProductionSummaryEmpty(t *testing.T) {
	got := ComputeStyleProductionSummary(nil)
	require.EqualValues(t, 0, got.Runs)
	require.EqualValues(t, 0, got.PlannedQtyTotal)
	require.EqualValues(t, 0, got.ReceivedQtyTotal)
	require.True(t, decimal.RequireFromString(got.ActualCostBase.Value).IsZero())
	require.False(t, got.HasActuals)
}

// TestComputeStyleProductionSummaryNoPlan: a run with recorded actuals but no frozen plan must NOT
// emit a variance (actual − 0 would read as a fabricated 100% overrun); CostVariance stays nil.
func TestComputeStyleProductionSummaryNoPlan(t *testing.T) {
	runs := []entity.ProductionRun{
		{
			ProductionRunInsert: entity.ProductionRunInsert{
				PlannedUnitCost: decimal.NullDecimal{}, // no plan
				Sizes:           []entity.ProductionRunSize{{SizeId: 1, PlannedQty: 4}},
				Costs:           []entity.ProductionRunCost{{Kind: entity.ProductionRunCostKind("cmt"), AmountBase: nd("40.00")}},
			},
		},
	}
	got := ComputeStyleProductionSummary(runs)
	require.True(t, decimal.RequireFromString(got.ActualCostBase.Value).Equal(decimal.NewFromInt(40)))
	require.True(t, decimal.RequireFromString(got.PlannedCostBase.Value).IsZero())
	require.Nil(t, got.CostVariance, "no plan → no variance (not a fabricated overrun)")
	require.True(t, got.HasActuals)
}

// TestComputeStyleProductionSummaryCancelledExcluded: a cancelled run is abandoned production and must
// not inflate run count, planned qty/cost, or actuals.
func TestComputeStyleProductionSummaryCancelledExcluded(t *testing.T) {
	runs := []entity.ProductionRun{
		{
			ProductionRunInsert: entity.ProductionRunInsert{
				Status:          entity.ProductionRunCancelled,
				PlannedUnitCost: nd("9.00"),
				Sizes:           []entity.ProductionRunSize{{SizeId: 1, PlannedQty: 100}},
				Costs:           []entity.ProductionRunCost{{Kind: entity.ProductionRunCostKind("cmt"), AmountBase: nd("500.00")}},
			},
		},
		{
			ProductionRunInsert: entity.ProductionRunInsert{
				PlannedUnitCost: nd("2.00"),
				Sizes:           []entity.ProductionRunSize{{SizeId: 1, PlannedQty: 10, ReceivedQty: sql.NullInt64{Int64: 10, Valid: true}}},
				Costs:           []entity.ProductionRunCost{{Kind: entity.ProductionRunCostKind("cmt"), AmountBase: nd("25.00")}},
			},
		},
	}
	got := ComputeStyleProductionSummary(runs)
	require.EqualValues(t, 1, got.Runs, "cancelled run excluded")
	require.EqualValues(t, 10, got.PlannedQtyTotal)
	require.EqualValues(t, 10, got.ReceivedQtyTotal)
	require.True(t, decimal.RequireFromString(got.PlannedCostBase.Value).Equal(decimal.NewFromInt(20)), "2 × 10, cancelled 9×100 excluded")
	require.True(t, decimal.RequireFromString(got.ActualCostBase.Value).Equal(decimal.NewFromInt(25)), "cancelled 500 excluded")
	require.True(t, decimal.RequireFromString(got.CostVariance.Value).Equal(decimal.NewFromInt(5)), "25 − 20")
}
