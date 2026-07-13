package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestInventoryValuationMaterialsAndStyle exercises NF-09 analytics integration: the raw-material +
// WIP + write-off extension of GetInventoryValuation, and the per-style sample / materials-from-stock
// aggregates. Assertions are DELTA-based (baseline vs after) so shared DB state from other suites
// can't skew them. Cleans up all its fixtures.
func TestInventoryValuationMaterialsAndStyle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	dec := decimal.RequireFromString
	// Wide window so material write-offs (created_at = now) and any sales fall inside.
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)

	mtr := s.Metrics()
	baseline, err := mtr.GetInventoryValuation(ctx, from, to, 50)
	require.NoError(t, err)

	// --- fixtures: a tech card with an open run + a sample, and three materials ---
	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Valuation Style", Stage: entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "NF-VAL-1", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState:   entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM tech_card WHERE id = ?", tcID) })

	runID, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM production_run WHERE id = ?", runID) })
	runRef := sql.NullInt32{Int32: int32(runID), Valid: true}

	sampleID, err := s.Samples().AddSample(ctx, &entity.SampleInsert{
		TechCardId: tcID, Purpose: "proto", Status: "planned", FabricSource: "sample",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM sample WHERE id = ?", sampleID) })
	sampleRef := sql.NullInt32{Int32: int32(sampleID), Valid: true}

	mkMaterial := func(name string) int {
		id, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
			Name: name, Section: "fabric", Unit: sql.NullString{String: "m", Valid: true},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(ctx, "DELETE FROM material WHERE id = ?", id)
		})
		return id
	}
	m1 := mkMaterial("NF Val Fabric Run")    // costed, issued to the run
	m2 := mkMaterial("NF Val Fabric Sample") // costed, issued to the sample
	m3 := mkMaterial("NF Val Fabric Uncosted")

	MS := s.MaterialStock()
	// M1: receive 100 @ 5 (avg 5); issue 30 to run, return 5 → on_hand 75, WIP (30-5)*5 = 125.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: m1, Quantity: dec("100"), UnitCost: nd("5"), Currency: "EUR"})
	require.NoError(t, err)
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: m1, Quantity: dec("30"), ProductionRunId: runRef})
	require.NoError(t, err)
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: m1, Quantity: dec("5"), ProductionRunId: runRef, IsReturn: true})
	require.NoError(t, err)

	// M2: receive 20 @ 3 (avg 3); issue 8 to sample, return 2 → on_hand 14, sample materials (8-2)*3 = 18.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: m2, Quantity: dec("20"), UnitCost: nd("3"), Currency: "EUR"})
	require.NoError(t, err)
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: m2, Quantity: dec("8"), SampleId: sampleRef})
	require.NoError(t, err)
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: m2, Quantity: dec("2"), SampleId: sampleRef, IsReturn: true})
	require.NoError(t, err)

	// M3: receive 5 with NO cost → on_hand 5, avg NULL (uncosted).
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: m3, Quantity: dec("5")})
	require.NoError(t, err)

	// --- valuation (before any write-off) ---
	v1, err := mtr.GetInventoryValuation(ctx, from, to, 50)
	require.NoError(t, err)
	// raw value delta: M1 75×5=375 + M2 14×3=42 + M3 0 = 417.
	require.True(t, v1.RawMaterialsValue.Sub(baseline.RawMaterialsValue).Equal(dec("417")),
		"raw materials value delta 417, got %s", v1.RawMaterialsValue.Sub(baseline.RawMaterialsValue))
	require.Equal(t, 3, v1.RawMaterialsCount-baseline.RawMaterialsCount, "3 new materials with stock")
	require.Equal(t, 1, v1.RawUncostedCount-baseline.RawUncostedCount, "M3 uncosted")
	require.True(t, v1.WipValue.Sub(baseline.WipValue).Equal(dec("125")),
		"WIP delta 125 (only the open run), got %s", v1.WipValue.Sub(baseline.WipValue))
	// top materials includes M1 (375) and M2 (42), not the uncosted M3.
	top := map[int]string{}
	for _, r := range v1.TopMaterialsByValue {
		top[r.MaterialId] = r.Value.String()
	}
	require.Equal(t, "375", top[m1])
	require.Equal(t, "42", top[m2])
	require.NotContains(t, top, m3, "uncosted material is not valued")

	// --- style aggregates ---
	sampleSummary, err := mtr.GetStyleSampleSummary(ctx, tcID)
	require.NoError(t, err)
	require.Equal(t, 1, sampleSummary.Count)
	require.True(t, sampleSummary.MaterialsCostBase.Equal(dec("18")),
		"sample materials 18, got %s", sampleSummary.MaterialsCostBase)
	require.False(t, sampleSummary.HasUncosted)

	matFromStock, err := mtr.GetStyleMaterialsFromStock(ctx, tcID)
	require.NoError(t, err)
	require.True(t, matFromStock.Base.Equal(dec("125")),
		"style materials-from-stock 125, got %s", matFromStock.Base)

	// --- material write-off: 5 of M1 at avg 5 = 25 ---
	_, err = MS.AdjustMaterialStock(ctx, entity.MaterialAdjustInsert{
		MaterialId: m1, Mode: entity.MaterialAdjustModeWriteoff, Quantity: dec("5"), Reason: "damage",
	})
	require.NoError(t, err)
	v2, err := mtr.GetInventoryValuation(ctx, from, to, 50)
	require.NoError(t, err)
	require.True(t, v2.WriteOffsMaterialsValue.Sub(baseline.WriteOffsMaterialsValue).Equal(dec("25")),
		"material write-off delta 25, got %s", v2.WriteOffsMaterialsValue.Sub(baseline.WriteOffsMaterialsValue))
}
