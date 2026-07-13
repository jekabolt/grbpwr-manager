package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ComputeProductionRunMaterialPlan resolves per-colourway norms × planned qty × (1+wastage) to a
// per-material requirement, nets it against on-hand + issued, and surfaces caveats.
func TestComputeProductionRunMaterialPlan(t *testing.T) {
	mid := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	bomIdx := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Name: "Main fabric", MaterialId: mid(100), Unit: sql.NullString{String: "m", Valid: true}, WastagePercent: nd2("5")}, // 5% wastage
		{Name: "Free-text trim", Unit: sql.NullString{String: "pc", Valid: true}},                                             // no material_id → caveat
	}
	card.Colorways = []entity.TechCardColorway{
		{Id: 1, Name: "Black", ProductId: sql.NullInt32{Int32: 55, Valid: true}, Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: bomIdx(0), Consumption: nd2("2")}, // 2 m per garment
			{BomItemIndex: bomIdx(1), Quantity: nd2("3")},    // trim, no material_id
		}},
		{Id: 2, Name: "Navy", ProductId: sql.NullInt32{Int32: 66, Valid: true}, Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: bomIdx(0), Consumption: nd2("2")},
		}},
	}

	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10},
			{ProductId: sql.NullInt32{Int32: 66, Valid: true}, SizeId: 1, PlannedQty: 20},
			{ProductId: sql.NullInt32{Int32: 99, Valid: true}, SizeId: 1, PlannedQty: 5}, // no matching colourway → caveat
		},
	}}

	onHand := map[int]decimal.Decimal{100: d("5")}
	issued := map[int]decimal.Decimal{100: d("10")}

	resp := ComputeProductionRunMaterialPlan(run, card, onHand, issued)
	require.Len(t, resp.Rows, 1, "only material 100 is countable")
	row := resp.Rows[0]
	require.Equal(t, int32(100), row.MaterialId)
	require.Equal(t, "Main fabric", row.MaterialName)
	require.Equal(t, "m", row.Unit)
	// (2×10 + 2×20) × 1.05 = 60 × 1.05 = 63
	require.Equal(t, "63", row.Required.Value)
	require.Equal(t, "5", row.OnHand.Value)
	require.Equal(t, "10", row.Issued.Value)
	require.Equal(t, "48", row.Shortage.Value, "63 − 10 − 5")
	require.False(t, row.HasSizeNorms, "per-garment norm, not size-graded")

	// caveats: free-text BOM line + the product with no colourway
	require.GreaterOrEqual(t, len(resp.Caveats), 2)
}

// materials-from-stock is folded into the run actuals; a manual materials cost alongside stock
// issues raises the mixed-sources caveat, and an uncosted issue raises has_uncosted_issues.
func TestProductionRunActualsMaterialsFromStock(t *testing.T) {
	run := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{
		Lines: []entity.ProductionRunLine{{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10, ReceivedQty: ni(10)}},
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostCMT, Amount: d("400"), Currency: "EUR", AmountBase: nd2("400")},
		},
	}}
	run.MaterialMovements = []entity.MaterialMovement{
		{MaterialId: 100, MovementType: entity.MaterialMovementIssueProduction, Quantity: d("21"), UnitCostBase: nd2("10")}, // 210
		{MaterialId: 100, MovementType: entity.MaterialMovementReturnProduction, Quantity: d("1"), UnitCostBase: nd2("10")}, // −10
	}
	a := ConvertEntityProductionRunToPb(run).Actuals
	require.Equal(t, "200", a.MaterialsFromStockBase.Value, "210 issued − 10 returned")
	require.Equal(t, "600", a.ActualTotalBase.Value, "400 CMT + 200 materials-from-stock")
	require.False(t, a.MixedMaterialsSources, "no manual kind=materials, so no double-count risk")
	require.False(t, a.HasUncostedIssues)
	require.Equal(t, "60", a.ActualUnitCost.Value, "600 / 10 received")

	// add a manual materials cost + an uncosted issue → both caveats fire.
	run.Costs = append(run.Costs, entity.ProductionRunCost{Kind: entity.ProductionRunCostMaterials, Amount: d("50"), Currency: "EUR", AmountBase: nd2("50")})
	run.MaterialMovements = append(run.MaterialMovements, entity.MaterialMovement{MaterialId: 200, MovementType: entity.MaterialMovementIssueProduction, Quantity: d("5")}) // no unit_cost_base
	a = ConvertEntityProductionRunToPb(run).Actuals
	require.True(t, a.MixedMaterialsSources, "manual materials + stock issues")
	require.True(t, a.HasUncostedIssues, "an issue had no average cost")

	// cost_price figure is not trustworthy while an issue is uncosted.
	require.False(t, ProductionRunActualUnitCostBase(run).Valid, "uncosted issue → no cost_price seed")
}
