package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestRunMaterialColorwayAttribution exercises gap-07 v2 C at the store level: an issue to a run can
// name the colour-model (product) it was cut for, and that product_id round-trips onto the run's
// movement ledger (loadRunMovements), so the dto per-colourway breakdown has data to group. Returns
// carry the attribution too.
func TestRunMaterialColorwayAttribution(t *testing.T) {
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

	// seed a media (product thumbnail FK), two products (colour-models), a tech card.
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)

	mkProduct := func(sku string) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO product
			(sku, brand, color, color_hex, country_of_origin, thumbnail_id, top_category_id, target_gender, version)
			VALUES (?, 'b', 'c', '#000000', 'US', ?, 1, 'unisex', 'v1')`, sku, mediaID)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return int(id)
	}
	prodA := mkProduct(fmt.Sprintf("NF-CW-A-%d", mediaID))
	prodB := mkProduct(fmt.Sprintf("NF-CW-B-%d", mediaID))

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Colorway Style", Stage: entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "NF-CW-1", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState:   entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)

	runID, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: int32(prodA), Valid: true}, SizeId: 1, PlannedQty: 10, ReceivedQty: sql.NullInt64{Int64: 6, Valid: true}},
			{ProductId: sql.NullInt32{Int32: int32(prodB), Valid: true}, SizeId: 1, PlannedQty: 10, ReceivedQty: sql.NullInt64{Int64: 4, Valid: true}},
		},
	})
	require.NoError(t, err)

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF CW Fabric", Section: "fabric", Unit: sql.NullString{String: "m", Valid: true}})
	require.NoError(t, err)

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", runID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", tcID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product WHERE id IN (?, ?)", prodA, prodB)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM media WHERE id = ?", mediaID)
	})

	MS := s.MaterialStock()
	runRef := sql.NullInt32{Int32: int32(runID), Valid: true}
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(100), UnitCost: nd("5"), Currency: "EUR"})
	require.NoError(t, err)

	// issue 20 to product A, 10 to product B, 1 unattributed (no product).
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(20), ProductionRunId: runRef, ProductId: sql.NullInt32{Int32: int32(prodA), Valid: true}})
	require.NoError(t, err)
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(10), ProductionRunId: runRef, ProductId: sql.NullInt32{Int32: int32(prodB), Valid: true}})
	require.NoError(t, err)
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(1), ProductionRunId: runRef})
	require.NoError(t, err)

	// GetProductionRun loads the movements with product_id populated.
	run, err := s.ProductionRuns().GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Len(t, run.MaterialMovements, 3)
	byProduct := map[int32]decimal.Decimal{}
	var unattributed decimal.Decimal
	for _, m := range run.MaterialMovements {
		if m.ProductId.Valid {
			byProduct[m.ProductId.Int32] = byProduct[m.ProductId.Int32].Add(m.Quantity)
		} else {
			unattributed = unattributed.Add(m.Quantity)
		}
	}
	require.True(t, byProduct[int32(prodA)].Equal(decimal.NewFromInt(20)), "product A issue attributed")
	require.True(t, byProduct[int32(prodB)].Equal(decimal.NewFromInt(10)), "product B issue attributed")
	require.True(t, unattributed.Equal(decimal.NewFromInt(1)), "the no-product issue stays unattributed")

	// g25-03: attributing an issue to a product that is neither on the run's lines nor linked to its
	// tech card is rejected — it would seed a by_colorway row another style owns.
	prodC := mkProduct(fmt.Sprintf("NF-CW-C-%d", mediaID))
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", prodC) })
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: matID, Quantity: decimal.NewFromInt(1), ProductionRunId: runRef,
		ProductId: sql.NullInt32{Int32: int32(prodC), Valid: true},
	})
	require.ErrorIs(t, err, entity.ErrMaterialIssueTargetInvalid, "foreign product attribution rejected")

	// g25-04: a colourway-attributed return is capped at THAT colourway's outstanding (10 for B),
	// even though the run as a whole has 31 out — over-returning B would net its bucket negative.
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: matID, Quantity: decimal.NewFromInt(25), ProductionRunId: runRef, IsReturn: true,
		ProductId: sql.NullInt32{Int32: int32(prodB), Valid: true},
	})
	require.ErrorIs(t, err, entity.ErrExcessiveMaterialReturn, "per-colourway over-return rejected")
	retMv, err := MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: matID, Quantity: decimal.NewFromInt(4), ProductionRunId: runRef, IsReturn: true,
		ProductId: sql.NullInt32{Int32: int32(prodB), Valid: true},
	})
	require.NoError(t, err, "within the colourway's outstanding")
	require.Equal(t, int32(prodB), retMv.ProductId.Int32, "the return nets against its colourway")

	// a sample issue must NOT carry a product_id even if one is passed (productAttribution guards it).
	smID, err := s.Samples().AddSample(ctx, &entity.SampleInsert{TechCardId: tcID, Purpose: "proto", Status: entity.SampleStatusInSewing, FabricSource: "sample"})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM sample WHERE id = ?", smID) })
	smMv, err := MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: matID, Quantity: decimal.NewFromInt(1),
		SampleId:  sql.NullInt32{Int32: int32(smID), Valid: true},
		ProductId: sql.NullInt32{Int32: int32(prodA), Valid: true}, // should be ignored
	})
	require.NoError(t, err)
	require.False(t, smMv.ProductId.Valid, "a sample issue never carries a product_id")
}
