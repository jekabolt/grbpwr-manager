package dto

import (
	"database/sql"
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// markerSrc marks a usage as marker-sourced with an optional display decomposition.
func markerSrc(u entity.TechCardColorwayUsage, selvedge, cut string) entity.TechCardColorwayUsage {
	u.ConsumptionSource = sql.NullString{String: entity.ConsumptionSourceMarker, Valid: true}
	if selvedge != "" {
		u.WasteSelvedgePct = nd2(selvedge)
	}
	if cut != "" {
		u.WasteCutPct = nd2(cut)
	}
	return u
}

// TestComputeProductionRunMaterialPlan_MarkerRowSkipsRunOverride is the named double-count trap
// (PIECES-WASTAGE-DESIGN §2.3): a run's ACTUAL wastage override must NOT re-gross a marker-sourced
// row — the marker length already paid for the cutting waste.
func TestComputeProductionRunMaterialPlan_MarkerRowSkipsRunOverride(t *testing.T) {
	mid := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	bomIdx := func(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{
		{Name: "Main fabric", Section: entity.BomSectionFabric, MaterialId: mid(100),
			Unit: sql.NullString{String: "m", Valid: true}, WastagePercent: nd2("5")},
	}
	card.Colorways = []entity.TechCardColorway{
		{Id: 1, Name: "Black", ProductId: sql.NullInt32{Int32: 55, Valid: true},
			Usages: []entity.TechCardColorwayUsage{
				markerSrc(entity.TechCardColorwayUsage{BomItemIndex: bomIdx(0), Consumption: nd2("2")}, "3.20", "11.50"),
			}},
	}
	lines := []entity.ProductionRunLine{{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10}}

	// Marker row, no override: required = 2×10 = 20 flat — the BOM estimate 5% must not apply.
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Lines: lines}}
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, nil)
	require.Len(t, resp.Rows, 1)
	require.Equal(t, "20", resp.Rows[0].Required.Value, "marker row must not gross by the BOM estimate")

	// Marker row WITH a run ACTUAL override: still 20 — the override is a wastage substitute,
	// and a marker row has no wastage slot to substitute into.
	runOverride := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7, Lines: lines, ActualWastagePercent: nd2("8"),
	}}
	resp = ComputeProductionRunMaterialPlan(runOverride, card, nil, nil, nil)
	require.Len(t, resp.Rows, 1)
	require.Equal(t, "20", resp.Rows[0].Required.Value, "run ACTUAL override must not re-gross a marker row")
}

// TestComputeStyleCostEstimate_MarkerLine proves the estimate emits the marker decomposition
// (source + selvedge/cut + affirmative total) and never grosses the line by wastage_percent.
func TestComputeStyleCostEstimate_MarkerLine(t *testing.T) {
	c := baseEstimateCard()
	c.Colorways[0].Usages[0] = markerSrc(c.Colorways[0].Usages[0], "3.20", "11.50")

	est := ComputeStyleCostEstimate(c, 0, nil, CostingFx{Base: "EUR"})
	require.NotNil(t, est)
	require.Len(t, est.Materials, 2)
	fabric := est.Materials[0]
	require.Equal(t, "marker", fabric.WastageSource)
	require.Equal(t, "3.2", fabric.WastageSelvedgePct.Value)
	require.Equal(t, "11.5", fabric.WastageCutPct.Value)
	require.Equal(t, "14.7", fabric.WastagePct.Value, "wastage_pct = selvedge + cut on marker lines")
	// fabric: 2 × 10 FLAT (no 5% gross) = 20.00 ; zip unchanged 3 × 2 = 6.00
	require.Equal(t, "20.00", fabric.LineTotalBase.Value, "marker line is never grossed")
	require.Equal(t, "6.00", est.Materials[1].LineTotalBase.Value)
	require.Equal(t, "26.00", est.MaterialsPerUnitBase.Value)

	// A marker line with NO decomposition still reads affirmatively as zero, not absent.
	c2 := baseEstimateCard()
	c2.Colorways[0].Usages[0] = markerSrc(c2.Colorways[0].Usages[0], "", "")
	est2 := ComputeStyleCostEstimate(c2, 0, nil, CostingFx{Base: "EUR"})
	require.Equal(t, "marker", est2.Materials[0].WastageSource)
	require.NotNil(t, est2.Materials[0].WastagePct, "marker line without decomposition emits 0 affirmatively")
	require.Equal(t, "0", est2.Materials[0].WastagePct.Value)
	require.Equal(t, "20.00", est2.Materials[0].LineTotalBase.Value)

	// Manual baseline stays bit-identical to the golden: 2×10×1.05 = 21.00.
	est3 := ComputeStyleCostEstimate(baseEstimateCard(), 0, nil, CostingFx{Base: "EUR"})
	require.Equal(t, "21.00", est3.Materials[0].LineTotalBase.Value)
	require.Equal(t, pb_admin.StyleCostPriceSource_STYLE_COST_PRICE_SOURCE_BOM_SNAPSHOT, est3.Materials[0].PriceSource)
}
