package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// measuredWastageCard builds a card whose single measured usage (Consumption, so wastage applies)
// consumes 3 units of a fabric priced at 2, plus a cmt of 10, all in EUR. The fabric BOM line
// carries a 5% cutting-wastage ESTIMATE. Unit cost = 3×2×(1+wastage) + 10.
func measuredWastageCard() *entity.TechCard {
	return &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		SizeQuantities: []entity.TechCardSizeQuantity{{SizeId: 4, OrderQty: 100}},
		BomItems: []entity.TechCardBomItem{{
			Section: entity.BomSectionFabric, Name: "shell",
			UnitPrice:      nd2("2"),
			Currency:       sql.NullString{String: "EUR", Valid: true},
			WastagePercent: nd2("5"), // BOM ESTIMATE (the fallback)
		}},
		Colorways: []entity.TechCardColorway{{Name: "Black", Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: sql.NullInt32{Int32: 0, Valid: true}, Consumption: nd2("3")}, // measured → grossed by wastage
		}}},
		Costing: &entity.TechCardCosting{CmtCost: nd2("10"), Currency: sql.NullString{String: "EUR", Valid: true}},
	}}
}

// TestComputeTechCardUnitCostWithWastage_RunActualOverridesBomEstimate is the authoritative proof of
// the run-cost fallback used by snapshotPlannedCost: the run's ACTUAL wastage overrides the BOM
// line's estimate when set, and falls back to the BOM estimate when unset — identically to the
// plain ComputeTechCardUnitCost.
func TestComputeTechCardUnitCostWithWastage_RunActualOverridesBomEstimate(t *testing.T) {
	fx := CostingFx{Base: "EUR"}
	card := measuredWastageCard()

	// Baseline: the BOM estimate (5%) → materials 3×2×1.05 = 6.30, + cmt 10 = 16.30.
	unitBOM, ccy := ComputeTechCardUnitCost(card, fx)
	require.True(t, unitBOM.Valid)
	require.Equal(t, "EUR", ccy)
	require.Equal(t, "16.3", unitBOM.Decimal.String())

	// Unset override → identical to the BOM-estimate baseline (the fallback).
	unitUnset, _ := ComputeTechCardUnitCostWithWastage(card, fx, decimal.NullDecimal{})
	require.True(t, unitUnset.Decimal.Equal(unitBOM.Decimal), "unset run wastage falls back to the BOM estimate")

	// Run ACTUAL 8% overrides the BOM 5% → materials 3×2×1.08 = 6.48, + cmt 10 = 16.48.
	unit8, _ := ComputeTechCardUnitCostWithWastage(card, fx, nd2("8"))
	require.True(t, unit8.Valid)
	require.Equal(t, "16.48", unit8.Decimal.String())
	// The whole delta is the wastage swap on the 6.00 material base: 6 × (0.08 − 0.05) = 0.18.
	require.True(t, unit8.Decimal.Sub(unitBOM.Decimal).Equal(d("0.18")),
		"run wastage 8%% raises unit cost by exactly the wastage delta on the material base")

	// A run wastage of 0 is a real value (not "unset") → no gross-up: materials 6.00 + cmt 10 = 16.00.
	unit0, _ := ComputeTechCardUnitCostWithWastage(card, fx, nd2("0"))
	require.Equal(t, "16", unit0.Decimal.String(), "explicit 0%% run wastage removes the BOM gross-up")

	// The override must not mutate the caller's card (shallow-copy contract).
	require.Equal(t, "5", card.BomItems[0].WastagePercent.Decimal.String(), "override must not mutate the card")
}

// TestComputeProductionRunMaterialPlan_RunActualWastageOverride proves the material plan honours the
// same fallback: the run's ACTUAL wastage overrides the BOM estimate for the required-material
// gross-up, else falls back to the BOM line's estimate.
func TestComputeProductionRunMaterialPlan_RunActualWastageOverride(t *testing.T) {
	mid := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	bomIdx := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Name: "Main fabric", MaterialId: mid(100), Unit: sql.NullString{String: "m", Valid: true}, WastagePercent: nd2("5")},
	}
	card.Colorways = []entity.TechCardColorway{
		{Id: 1, Name: "Black", ProductId: sql.NullInt32{Int32: 55, Valid: true}, Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: bomIdx(0), Consumption: nd2("2")}, // 2 m per garment
		}},
	}
	lines := []entity.ProductionRunLine{{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10}}

	// Fallback: no run wastage → BOM estimate 5% → 2×10 × 1.05 = 21.
	runFallback := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Lines: lines}}
	resp := ComputeProductionRunMaterialPlan(runFallback, card, nil, nil)
	require.Len(t, resp.Rows, 1)
	require.Equal(t, "21", resp.Rows[0].Required.Value, "no run wastage → BOM estimate 5%%")

	// Override: run ACTUAL 8% → 2×10 × 1.08 = 21.6.
	runOverride := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7, Lines: lines, ActualWastagePercent: nd2("8"),
	}}
	resp = ComputeProductionRunMaterialPlan(runOverride, card, nil, nil)
	require.Len(t, resp.Rows, 1)
	require.Equal(t, "21.6", resp.Rows[0].Required.Value, "run ACTUAL wastage 8%% overrides the BOM estimate")
}
