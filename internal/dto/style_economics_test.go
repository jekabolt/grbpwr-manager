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
				Lines: []entity.ProductionRunLine{
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
				Lines: []entity.ProductionRunLine{
					{SizeId: 1, PlannedQty: 20},
				},
				Costs: []entity.ProductionRunCost{
					{Kind: entity.ProductionRunCostKind("materials"), AmountBase: decimal.NullDecimal{}}, // unconverted → skipped
					{Kind: entity.ProductionRunCostKind("logistics"), AmountBase: nd("10.00")},
				},
			},
		},
	}

	got := ComputeStyleProductionSummary(runs, decimal.Zero, false)
	require.EqualValues(t, 2, got.Runs)
	require.EqualValues(t, 30, got.PlannedQtyTotal, "10 + 20")
	require.EqualValues(t, 8, got.ReceivedQtyTotal, "5 + 3")
	require.True(t, decimal.RequireFromString(got.PlannedCostBase.Value).Equal(decimal.NewFromInt(30)), "3.00 × 10, got %s", got.PlannedCostBase.Value)
	require.True(t, decimal.RequireFromString(got.ActualCostBase.Value).Equal(decimal.NewFromInt(35)), "25 + 10, got %s", got.ActualCostBase.Value)
	require.True(t, decimal.RequireFromString(got.CostVariance.Value).Equal(decimal.NewFromInt(5)), "35 − 30, got %s", got.CostVariance.Value)
	require.True(t, got.HasActuals)
}

// TestComputeStyleProductionSummaryFoldsStockMaterials: warehouse material issued into the style's
// runs folds into actual_cost_base and cost_variance exactly as the run-level actuals do (nf09-02),
// and is echoed in materials_from_stock_base.
func TestComputeStyleProductionSummaryFoldsStockMaterials(t *testing.T) {
	runs := []entity.ProductionRun{
		{
			ProductionRunInsert: entity.ProductionRunInsert{
				PlannedUnitCost: nd("3.00"),
				Lines:           []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10}},
				Costs:           []entity.ProductionRunCost{{Kind: entity.ProductionRunCostKind("cmt"), AmountBase: nd("25.00")}},
			},
		},
	}
	// manual actual 25 + materials-from-stock 500 = 525; planned 3×10 = 30; variance 525 − 30 = 495.
	got := ComputeStyleProductionSummary(runs, decimal.RequireFromString("500.00"), true)
	require.True(t, decimal.RequireFromString(got.ActualCostBase.Value).Equal(decimal.NewFromInt(525)), "25 + 500, got %s", got.ActualCostBase.Value)
	require.True(t, decimal.RequireFromString(got.MaterialsFromStockBase.Value).Equal(decimal.NewFromInt(500)))
	require.True(t, decimal.RequireFromString(got.CostVariance.Value).Equal(decimal.NewFromInt(495)), "525 − 30, got %s", got.CostVariance.Value)
	require.True(t, got.HasActuals)
}

// TestComputeStyleProductionSummaryStockMaterialsOnly: no manual cost, only warehouse material →
// has_actuals is still true (the run DID cost something) and actual = the material.
func TestComputeStyleProductionSummaryStockMaterialsOnly(t *testing.T) {
	runs := []entity.ProductionRun{
		{ProductionRunInsert: entity.ProductionRunInsert{Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 4}}}},
	}
	got := ComputeStyleProductionSummary(runs, decimal.RequireFromString("120.00"), true)
	require.True(t, decimal.RequireFromString(got.ActualCostBase.Value).Equal(decimal.NewFromInt(120)))
	require.True(t, got.HasActuals, "material issued → has actuals even with no manual cost article")
}

// TestComputeStyleProductionSummaryEmpty: no runs → all-zero, has_actuals false.
func TestComputeStyleProductionSummaryEmpty(t *testing.T) {
	got := ComputeStyleProductionSummary(nil, decimal.Zero, false)
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
				Lines:           []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 4}},
				Costs:           []entity.ProductionRunCost{{Kind: entity.ProductionRunCostKind("cmt"), AmountBase: nd("40.00")}},
			},
		},
	}
	got := ComputeStyleProductionSummary(runs, decimal.Zero, false)
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
				Lines:           []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 100}},
				Costs:           []entity.ProductionRunCost{{Kind: entity.ProductionRunCostKind("cmt"), AmountBase: nd("500.00")}},
			},
		},
		{
			ProductionRunInsert: entity.ProductionRunInsert{
				PlannedUnitCost: nd("2.00"),
				Lines:           []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10, ReceivedQty: sql.NullInt64{Int64: 10, Valid: true}}},
				Costs:           []entity.ProductionRunCost{{Kind: entity.ProductionRunCostKind("cmt"), AmountBase: nd("25.00")}},
			},
		},
	}
	got := ComputeStyleProductionSummary(runs, decimal.Zero, false)
	require.EqualValues(t, 1, got.Runs, "cancelled run excluded")
	require.EqualValues(t, 10, got.PlannedQtyTotal)
	require.EqualValues(t, 10, got.ReceivedQtyTotal)
	require.True(t, decimal.RequireFromString(got.PlannedCostBase.Value).Equal(decimal.NewFromInt(20)), "2 × 10, cancelled 9×100 excluded")
	require.True(t, decimal.RequireFromString(got.ActualCostBase.Value).Equal(decimal.NewFromInt(25)), "cancelled 500 excluded")
	require.True(t, decimal.RequireFromString(got.CostVariance.Value).Equal(decimal.NewFromInt(5)), "25 − 20")
}
