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

// TestBatchIssueMaterialStock proves a multi-line issue commits as one warehouse act.
func TestBatchIssueMaterialStock(t *testing.T) {
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

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name:            "NF Batch Issue Style",
		Stage:           entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "NF-BATCH-ISSUE", Valid: true},
		TargetGender:    sql.NullString{String: "unisex", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState:   entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	sampleID, err := s.Samples().AddSample(ctx, &entity.SampleInsert{
		TechCardId: tcID, Purpose: "proto", Status: "planned", FabricSource: "sample",
	})
	require.NoError(t, err)

	materialIDs := make([]int, 0, 3)
	for _, name := range []string{"NF Batch Fabric", "NF Batch Thread", "NF Batch Trim"} {
		id, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
			Name: name, Section: "fabric", Unit: sql.NullString{String: "m", Valid: true},
		})
		require.NoError(t, err)
		materialIDs = append(materialIDs, id)
	}
	t.Cleanup(func() {
		cctx := context.Background()
		for _, id := range materialIDs {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", id)
		}
		_, _ = testDB.ExecContext(cctx, "DELETE FROM sample WHERE id = ?", sampleID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	ms := s.MaterialStock()
	starting := []decimal.Decimal{decimal.NewFromInt(10), decimal.NewFromInt(20), decimal.NewFromInt(30)}
	for i, materialID := range materialIDs {
		_, err := ms.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
			MaterialId: materialID, Quantity: starting[i],
		})
		require.NoError(t, err)
	}

	sampleRef := sql.NullInt32{Int32: int32(sampleID), Valid: true}
	happyLines := []entity.MaterialBatchIssueLine{
		{MaterialId: materialIDs[0], Quantity: decimal.NewFromInt(2)},
		{MaterialId: materialIDs[1], Quantity: decimal.NewFromInt(3)},
		{MaterialId: materialIDs[2], Quantity: decimal.NewFromInt(4)},
	}
	movements, err := ms.BatchIssueMaterialStock(ctx, entity.MaterialBatchIssueInsert{
		SampleId: sampleRef, AdminUsername: "batch-test", Lines: happyLines,
	})
	require.NoError(t, err)
	require.Len(t, movements, len(happyLines))
	for i, movement := range movements {
		require.Equal(t, materialIDs[i], movement.MaterialId)
		require.Equal(t, entity.MaterialMovementIssueSample, movement.MovementType)
		require.True(t, movement.Quantity.Equal(happyLines[i].Quantity))
		require.Equal(t, "batch-test", movement.AdminUsername)
	}

	expectedBalances := []decimal.Decimal{decimal.NewFromInt(8), decimal.NewFromInt(17), decimal.NewFromInt(26)}
	assertBalances := func() {
		for i, materialID := range materialIDs {
			stock, err := ms.GetMaterialStock(ctx, materialID)
			require.NoError(t, err)
			require.True(t, stock.OnHand.Equal(expectedBalances[i]),
				"material %d: expected %s, got %s", materialID, expectedBalances[i], stock.OnHand)
		}
	}
	movementCount := func() int {
		var count int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM material_stock_movement WHERE sample_id = ?", sampleID).Scan(&count))
		return count
	}
	assertBalances()
	require.Equal(t, len(happyLines), movementCount())

	_, err = ms.BatchIssueMaterialStock(ctx, entity.MaterialBatchIssueInsert{
		SampleId: sampleRef,
		Lines: []entity.MaterialBatchIssueLine{
			{MaterialId: materialIDs[0], Quantity: decimal.NewFromInt(1)},
			{MaterialId: materialIDs[1], Quantity: decimal.NewFromInt(1)},
			{MaterialId: materialIDs[2], Quantity: decimal.NewFromInt(100)},
		},
	})
	require.ErrorIs(t, err, entity.ErrInsufficientMaterialStock)
	require.ErrorContains(t, err, "line 3")
	assertBalances()
	require.Equal(t, len(happyLines), movementCount(), "a refused batch writes no movement rows")

	_, err = ms.BatchIssueMaterialStock(ctx, entity.MaterialBatchIssueInsert{
		SampleId: sampleRef,
		Lines: []entity.MaterialBatchIssueLine{
			{MaterialId: materialIDs[0], Quantity: decimal.NewFromInt(1)},
			{MaterialId: materialIDs[0], Quantity: decimal.NewFromInt(1)},
		},
	})
	require.ErrorContains(t, err, "duplicate material_id")
	assertBalances()
	require.Equal(t, len(happyLines), movementCount(), "duplicate validation happens before any write")
}
