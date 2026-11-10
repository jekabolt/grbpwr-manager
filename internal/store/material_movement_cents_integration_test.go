package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	acctrules "github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func newMaterialMovementCentStore(t *testing.T, ctx context.Context) *MYSQLStore {
	t.Helper()
	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(s.Close)

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))
	return s
}

func TestMaterialReturnAllocatesFinalBookedCentResidual(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s := newMaterialMovementCentStore(t, ctx)
	dec := decimal.RequireFromString
	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: dec(v), Valid: true}
	}

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Cent Return Style", Stage: entity.TechCardStageProto,
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	runID, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 3}},
	})
	require.NoError(t, err)
	materialID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Cent Return Material", Section: "fabric",
		Unit: sql.NullString{String: "m", Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = testDB.ExecContext(cleanupCtx, "DELETE FROM material_stock_movement WHERE material_id = ?", materialID)
		_, _ = testDB.ExecContext(cleanupCtx, "DELETE FROM production_run WHERE id = ?", runID)
		_, _ = testDB.ExecContext(cleanupCtx, "DELETE FROM tech_card WHERE id = ?", tcID)
		_, _ = testDB.ExecContext(cleanupCtx, "DELETE FROM material WHERE id = ?", materialID)
	})

	stock := s.MaterialStock()
	_, err = stock.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: materialID, Quantity: dec("3"), UnitCost: nd("0.005"), Currency: "EUR",
	})
	require.NoError(t, err)
	runRef := sql.NullInt32{Int32: int32(runID), Valid: true}
	issued, err := stock.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: materialID, Quantity: dec("3"), ProductionRunId: runRef,
	})
	require.NoError(t, err)
	require.Equal(t, "0.02", entity.MovementValueCents(issued.Quantity, issued.UnitCostBase.Decimal).StringFixed(2))

	first, err := stock.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: materialID, Quantity: dec("1"), ProductionRunId: runRef, IsReturn: true,
	})
	require.NoError(t, err)
	require.True(t, first.UnitCostBase.Valid)
	require.Equal(t, "0.01", entity.MovementValueCents(first.Quantity, first.UnitCostBase.Decimal).StringFixed(2))

	final, err := stock.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: materialID, Quantity: dec("2"), ProductionRunId: runRef, IsReturn: true,
	})
	require.NoError(t, err)
	require.True(t, final.UnitCostBase.Valid)
	require.Equal(t, "0.01", entity.MovementValueCents(final.Quantity, final.UnitCostBase.Decimal).StringFixed(2))

	returned := entity.MovementValueCents(first.Quantity, first.UnitCostBase.Decimal).
		Add(entity.MovementValueCents(final.Quantity, final.UnitCostBase.Decimal))
	require.Equal(t, "0.02", returned.StringFixed(2), "partial plus final return must relieve every issued cent")
}

func TestMaterialMovementCentConsumersAgree(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	s := newMaterialMovementCentStore(t, ctx)
	dec := decimal.RequireFromString
	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: dec(v), Valid: true}
	}
	from := time.Now().UTC().AddDate(0, 0, -1)
	to := time.Now().UTC().AddDate(0, 0, 2)

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Cent Consumers Style", Stage: entity.TechCardStageProto,
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	runID, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 2}},
	})
	require.NoError(t, err)
	runMaterialID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Cent Consumers Run Material", Section: "fabric",
		Unit: sql.NullString{String: "m", Valid: true},
	})
	require.NoError(t, err)

	var journalEntryIDs []int
	var sampleID int
	var sampleMaterialID int
	var writeoffMaterialID int
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, id := range journalEntryIDs {
			_, _ = testDB.ExecContext(cleanupCtx, "DELETE FROM acct_journal_entry WHERE id = ?", id)
		}
		for _, id := range []int{runMaterialID, sampleMaterialID, writeoffMaterialID} {
			if id != 0 {
				_, _ = testDB.ExecContext(cleanupCtx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			}
		}
		if sampleID != 0 {
			_, _ = testDB.ExecContext(cleanupCtx, "DELETE FROM sample WHERE id = ?", sampleID)
		}
		_, _ = testDB.ExecContext(cleanupCtx, "DELETE FROM production_run WHERE id = ?", runID)
		_, _ = testDB.ExecContext(cleanupCtx, "DELETE FROM tech_card WHERE id = ?", tcID)
		for _, id := range []int{runMaterialID, sampleMaterialID, writeoffMaterialID} {
			if id != 0 {
				_, _ = testDB.ExecContext(cleanupCtx, "DELETE FROM material WHERE id = ?", id)
			}
		}
	})

	stock := s.MaterialStock()
	_, err = stock.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: runMaterialID, Quantity: dec("2"), UnitCost: nd("0.005"), Currency: "EUR",
	})
	require.NoError(t, err)
	reconBefore, err := s.Accounting().GetReconciliation(ctx, from, to)
	require.NoError(t, err)
	valuationBefore, err := s.Metrics().GetInventoryValuation(ctx, from, to, 50)
	require.NoError(t, err)

	runRef := sql.NullInt32{Int32: int32(runID), Valid: true}
	var issues []entity.MaterialMovement
	for range 2 {
		movement, issueErr := stock.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
			MaterialId: runMaterialID, Quantity: dec("1"), ProductionRunId: runRef,
		})
		require.NoError(t, issueErr)
		issues = append(issues, movement)
	}

	posterTotal := decimal.Zero
	for _, movement := range issues {
		entry, buildErr := acctrules.BuildMaterialMovementEntry(entity.AcctMovementFacts{
			MaterialMovement: movement,
			MaterialName:     "Cent Consumers Run Material",
		}, from)
		require.NoError(t, buildErr)
		for _, line := range entry.Lines {
			if line.AccountCode == acctrules.Acc1110 {
				require.Equal(t, entity.AcctSideCredit, line.Side)
				posterTotal = posterTotal.Add(line.Amount)
			}
		}
		var entryID int
		require.NoError(t, s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			var createErr error
			entryID, _, createErr = rep.Accounting().CreateJournalEntry(ctx, entry)
			return createErr
		}))
		journalEntryIDs = append(journalEntryIDs, entryID)
	}
	require.Equal(t, "0.02", posterTotal.StringFixed(2))

	run, err := s.ProductionRuns().GetProductionRun(ctx, runID)
	require.NoError(t, err)
	actual, ok := run.ActualTotalCostBase()
	require.True(t, ok)
	require.True(t, actual.Equal(posterTotal), "production actual %s, poster %s", actual, posterTotal)

	valuationAfter, err := s.Metrics().GetInventoryValuation(ctx, from, to, 50)
	require.NoError(t, err)
	wipDelta := valuationAfter.WipValue.Sub(valuationBefore.WipValue)
	require.True(t, wipDelta.Equal(posterTotal), "WIP delta %s, poster %s", wipDelta, posterTotal)

	styleStock, err := s.Metrics().GetStyleMaterialsFromStock(ctx, tcID)
	require.NoError(t, err)
	require.True(t, styleStock.Base.Equal(posterTotal), "style stock %s, poster %s", styleStock.Base, posterTotal)

	reconAfter, err := s.Accounting().GetReconciliation(ctx, from, to)
	require.NoError(t, err)
	operationalDelta := reconAfter.Materials.Operational.Sub(reconBefore.Materials.Operational)
	ledgerDelta := reconAfter.Materials.Ledger.Sub(reconBefore.Materials.Ledger)
	require.True(t, operationalDelta.Equal(posterTotal.Neg()), "reconcile operational delta %s", operationalDelta)
	require.True(t, ledgerDelta.Equal(posterTotal.Neg()), "reconcile ledger delta %s", ledgerDelta)

	// Sample cost, style sample summary, and deletion preview use the same per-movement cents.
	sampleID, err = s.Samples().AddSample(ctx, &entity.SampleInsert{
		TechCardId: tcID, Purpose: "proto", Status: "planned", FabricSource: "sample",
	})
	require.NoError(t, err)
	sampleMaterialID, err = s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Cent Consumers Sample Material", Section: "fabric",
		Unit: sql.NullString{String: "m", Valid: true},
	})
	require.NoError(t, err)
	_, err = stock.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: sampleMaterialID, Quantity: dec("2"), UnitCost: nd("0.005"), Currency: "EUR",
	})
	require.NoError(t, err)
	sampleRef := sql.NullInt32{Int32: int32(sampleID), Valid: true}
	for range 2 {
		_, err = stock.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
			MaterialId: sampleMaterialID, Quantity: dec("1"), SampleId: sampleRef,
		})
		require.NoError(t, err)
	}
	sample, err := s.Samples().GetSampleById(ctx, sampleID)
	require.NoError(t, err)
	require.True(t, sample.Cost.MaterialsBase.Equal(dec("0.02")), "sample cost %s", sample.Cost.MaterialsBase)
	sampleSummary, err := s.Metrics().GetStyleSampleSummary(ctx, tcID)
	require.NoError(t, err)
	require.True(t, sampleSummary.MaterialsCostBase.Equal(dec("0.02")), "style sample cost %s", sampleSummary.MaterialsCostBase)
	_, err = stock.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: sampleMaterialID, Quantity: dec("2"), SampleId: sampleRef, IsReturn: true,
	})
	require.NoError(t, err)
	verdict, _, err := s.Samples().EvaluateSampleDeletion(ctx, sampleID)
	require.NoError(t, err)
	for _, orphan := range verdict.Orphans {
		require.NotEqual(t, entity.SampleOrphanStyleCost, orphan.Reason, "fully returned booked cents leave no sample-cost residue")
	}

	// Two half-cent write-offs are two booked cents, not one raw-product cent.
	writeoffMaterialID, err = s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Cent Consumers Writeoff Material", Section: "fabric",
		Unit: sql.NullString{String: "m", Valid: true},
	})
	require.NoError(t, err)
	_, err = stock.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: writeoffMaterialID, Quantity: dec("2"), UnitCost: nd("0.005"), Currency: "EUR",
	})
	require.NoError(t, err)
	writeoffBefore, err := s.Metrics().GetInventoryValuation(ctx, from, to, 50)
	require.NoError(t, err)
	for range 2 {
		_, err = stock.AdjustMaterialStock(ctx, entity.MaterialAdjustInsert{
			MaterialId: writeoffMaterialID, Mode: entity.MaterialAdjustModeWriteoff,
			Quantity: dec("1"), Reason: entity.MaterialAdjustReasonDamage,
		})
		require.NoError(t, err)
	}
	writeoffAfter, err := s.Metrics().GetInventoryValuation(ctx, from, to, 50)
	require.NoError(t, err)
	writeoffDelta := writeoffAfter.WriteOffsMaterialsValue.Sub(writeoffBefore.WriteOffsMaterialsValue)
	require.True(t, writeoffDelta.Equal(dec("0.02")), "write-off delta %s", writeoffDelta)
}
