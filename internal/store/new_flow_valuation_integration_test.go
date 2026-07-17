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
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	runID, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
	})
	runRef := sql.NullInt32{Int32: int32(runID), Valid: true}

	sampleID, err := s.Samples().AddSample(ctx, &entity.SampleInsert{
		TechCardId: tcID, Purpose: "proto", Status: "planned", FabricSource: "sample",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM sample WHERE id = ?", sampleID) })
	sampleRef := sql.NullInt32{Int32: int32(sampleID), Valid: true}

	mkMaterial := func(name string) int {
		id, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
			Name: name, Section: "fabric", Unit: sql.NullString{String: "m", Valid: true},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			cctx := context.Background()
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", id)
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

// TestInventoryValuationReceiveLeavesWip closes the NF-09 valuation gap (nf09-07 #1): material
// issued to an OPEN run sits in WIP, but once the run is received it must LEAVE WIP (the WIP query
// only counts issues on planned/in_progress runs). The aux variant then shows the "WIP→raw"
// hand-off: receiving an auxiliary run drops its input from WIP and lands its output as raw stock.
// Assertions are delta-based so shared DB state can't skew them.
func TestInventoryValuationReceiveLeavesWip(t *testing.T) {
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
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	mtr := s.Metrics()
	MS := s.MaterialStock()

	wip := func() decimal.Decimal {
		v, err := mtr.GetInventoryValuation(ctx, from, to, 50)
		require.NoError(t, err)
		return v.WipValue
	}

	// --- regular run: issued material sits in WIP, then leaves on receive ---
	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Recv Style", Stage: entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "NF-RECV-1", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState:   entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	runID, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
	})

	m1, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Recv Fabric", Section: "fabric", Unit: sql.NullString{String: "m", Valid: true}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", m1)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", m1)
	})

	wipBefore := wip()
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: m1, Quantity: dec("100"), UnitCost: nd("5"), Currency: "EUR"})
	require.NoError(t, err)
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: m1, Quantity: dec("30"), ProductionRunId: sql.NullInt32{Int32: int32(runID), Valid: true}})
	require.NoError(t, err)

	wipIssued := wip()
	require.True(t, wipIssued.Sub(wipBefore).Equal(dec("150")),
		"issuing 30 @ 5 to the open run adds 150 to WIP, got %s", wipIssued.Sub(wipBefore))

	// receive the run (status → received); the WIP query must now exclude its issues.
	_, err = testDB.ExecContext(ctx, "UPDATE production_run SET status = ? WHERE id = ?", string(entity.ProductionRunReceived), runID)
	require.NoError(t, err)

	wipReceived := wip()
	require.True(t, wipReceived.Sub(wipBefore).Equal(decimal.Zero),
		"received run leaves WIP (delta back to 0), got %s", wipReceived.Sub(wipBefore))

	// --- aux variant: WIP→raw hand-off ---
	outMatID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Recv Output", Section: "packaging", Unit: sql.NullString{String: "pc", Valid: true}})
	require.NoError(t, err)
	inMatID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Recv AuxInput", Section: "fabric", Unit: sql.NullString{String: "m", Valid: true}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		for _, id := range []int{outMatID, inMatID} {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", id)
		}
	})

	auxTC, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Recv Aux", Stage: entity.TechCardStageProto,
		StyleNumber:      sql.NullString{String: "NF-RECV-AUX-1", Valid: true},
		MeasurementUnit:  entity.TechCardUnitMm,
		ApprovalState:    entity.TechCardApprovalDraft,
		Purpose:          entity.TechCardPurposeAuxiliary,
		OutputMaterialId: sql.NullInt64{Int64: int64(outMatID), Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", auxTC) })

	auxRun, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: auxTC, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 100, ReceivedQty: sql.NullInt64{Int64: 100, Valid: true}}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", auxRun)
	})

	// feed 10 @ 2 of the input material into the open aux run → +20 WIP.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: inMatID, Quantity: dec("50"), UnitCost: nd("2"), Currency: "EUR"})
	require.NoError(t, err)
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: inMatID, Quantity: dec("10"), ProductionRunId: sql.NullInt32{Int32: int32(auxRun), Valid: true}})
	require.NoError(t, err)

	beforeReceive, err := mtr.GetInventoryValuation(ctx, from, to, 50)
	require.NoError(t, err)
	require.True(t, beforeReceive.WipValue.Sub(wipReceived).Equal(dec("20")),
		"aux input 10 @ 2 adds 20 to WIP while the aux run is open, got %s", beforeReceive.WipValue.Sub(wipReceived))

	// receive the aux run's output → run received, output raw. The store derives the receipt's unit
	// cost from the run's ACTUALS (g25-07): only the 20-base material issue → 20/100 = 0.2/unit, so
	// the hand-off is value-conserving — exactly the WIP consumed lands as raw.
	require.NoError(t, s.ProductionRuns().ReceiveAuxiliaryProductionRun(ctx, auxRun, outMatID, "tester"))

	afterReceive, err := mtr.GetInventoryValuation(ctx, from, to, 50)
	require.NoError(t, err)
	// WIP→: the aux input leaves WIP on receive.
	require.True(t, afterReceive.WipValue.Sub(beforeReceive.WipValue).Equal(dec("-20")),
		"aux input leaves WIP on receive (−20), got %s", afterReceive.WipValue.Sub(beforeReceive.WipValue))
	// →raw: the run's output lands as raw material stock (100 @ 0.2 = 20 — the consumed WIP value).
	require.True(t, afterReceive.RawMaterialsValue.Sub(beforeReceive.RawMaterialsValue).Equal(dec("20")),
		"aux output becomes raw inventory (+20), got %s", afterReceive.RawMaterialsValue.Sub(beforeReceive.RawMaterialsValue))
}
