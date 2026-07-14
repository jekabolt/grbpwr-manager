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

// TestMaterialLotTracking exercises gap-07 v2 D: receipts open/top-up structured lots, an issue
// draws from a named lot (guarded against the lot's remaining and against a foreign material), a
// return puts quantity back, and — crucially — lots are traceability only: the moving-average
// valuation is never touched by any lot operation.
func TestMaterialLotTracking(t *testing.T) {
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
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	MS := s.MaterialStock()

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Lot Fabric", Section: "fabric", Unit: ns("m")})
	require.NoError(t, err)
	mat2, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Lot Fabric2", Section: "fabric", Unit: ns("m")})
	require.NoError(t, err)

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Lot Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("NF-LOT-1"), MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	runID, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 50}},
	})
	require.NoError(t, err)
	runRef := sql.NullInt32{Int32: int32(runID), Valid: true}

	t.Cleanup(func() {
		cctx := context.Background()
		for _, id := range []int{matID, mat2} {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_lot WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", id)
		}
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", runID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	// --- receipts open / top up lots (all @ 5 so the moving average stays 5) ---
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(60), UnitCost: nd("5"), Currency: "EUR", Lot: ns("DYE-A")})
	require.NoError(t, err)
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(40), UnitCost: nd("5"), Currency: "EUR", Lot: ns("DYE-A")})
	require.NoError(t, err)
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(30), UnitCost: nd("5"), Currency: "EUR", Lot: ns("DYE-B")})
	require.NoError(t, err)

	lots, err := MS.ListMaterialLots(ctx, matID, false)
	require.NoError(t, err)
	require.Len(t, lots, 2, "two distinct lots")
	byCode := map[string]entity.MaterialLot{}
	for _, l := range lots {
		byCode[l.LotCode] = l
	}
	dyeA := byCode["DYE-A"]
	require.True(t, dyeA.ReceivedQty.Equal(decimal.NewFromInt(100)), "DYE-A accumulated 60+40, got %s", dyeA.ReceivedQty)
	require.True(t, dyeA.RemainingQty.Equal(decimal.NewFromInt(100)))
	require.True(t, byCode["DYE-B"].RemainingQty.Equal(decimal.NewFromInt(30)))

	assertAvg5 := func(where string) {
		st, err := MS.GetMaterialStock(ctx, matID)
		require.NoError(t, err)
		require.True(t, st.AvgUnitCostBase.Valid && st.AvgUnitCostBase.Decimal.Equal(decimal.NewFromInt(5)),
			"moving average stays 5 (lots don't touch valuation) %s, got %v", where, st.AvgUnitCostBase)
	}
	assertAvg5("after receipts")

	// --- issue 25 from DYE-A to the run: lot remaining 75, movement carries the lot ---
	mv, err := MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(25), ProductionRunId: runRef, LotId: sql.NullInt32{Int32: int32(dyeA.Id), Valid: true}})
	require.NoError(t, err)
	require.True(t, mv.LotId.Valid && mv.LotId.Int32 == int32(dyeA.Id), "issue movement references the lot")
	lots, _ = MS.ListMaterialLots(ctx, matID, false)
	for _, l := range lots {
		if l.Id == dyeA.Id {
			require.True(t, l.RemainingQty.Equal(decimal.NewFromInt(75)), "DYE-A remaining 100-25=75, got %s", l.RemainingQty)
		}
	}
	assertAvg5("after lot issue")

	// --- drawing more than a lot has remaining is guarded (independent of total stock) ---
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(50), ProductionRunId: runRef, LotId: sql.NullInt32{Int32: int32(byCode["DYE-B"].Id), Valid: true}})
	require.ErrorIs(t, err, entity.ErrInsufficientMaterialLot, "DYE-B has only 30 remaining")

	// --- return 5 to DYE-A puts it back on the lot ---
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(5), ProductionRunId: runRef, IsReturn: true, LotId: sql.NullInt32{Int32: int32(dyeA.Id), Valid: true}})
	require.NoError(t, err)
	lots, _ = MS.ListMaterialLots(ctx, matID, false)
	for _, l := range lots {
		if l.Id == dyeA.Id {
			require.True(t, l.RemainingQty.Equal(decimal.NewFromInt(80)), "DYE-A remaining 75+5=80, got %s", l.RemainingQty)
		}
	}

	// --- a lot from a different material is rejected ---
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: mat2, Quantity: decimal.NewFromInt(10), UnitCost: nd("5"), Currency: "EUR", Lot: ns("DYE-C")})
	require.NoError(t, err)
	lots2, err := MS.ListMaterialLots(ctx, mat2, false)
	require.NoError(t, err)
	require.Len(t, lots2, 1)
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(1), ProductionRunId: runRef, LotId: sql.NullInt32{Int32: int32(lots2[0].Id), Valid: true}})
	require.ErrorIs(t, err, entity.ErrMaterialLotMismatch, "a lot of another material must be rejected")

	// --- g25-05: a return can't push a lot's remaining above its received_qty ---
	// issue 25 from DYE-B so the run has plenty outstanding (target cap passes), then over-return to
	// DYE-A: remaining 80 + 21 would exceed the 100 ever received into it — only 20 of DYE-A is out.
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(25), ProductionRunId: runRef, LotId: sql.NullInt32{Int32: int32(byCode["DYE-B"].Id), Valid: true}})
	require.NoError(t, err)
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(21), ProductionRunId: runRef, IsReturn: true, LotId: sql.NullInt32{Int32: int32(dyeA.Id), Valid: true}})
	require.ErrorIs(t, err, entity.ErrExcessiveMaterialReturn, "lot remaining can never exceed its received_qty")

	// --- g25-06: a receipt under an archived lot's code reactivates the lot ---
	_, err = testDB.ExecContext(ctx, "UPDATE material_lot SET archived = TRUE WHERE id = ?", byCode["DYE-B"].Id)
	require.NoError(t, err)
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(10), UnitCost: nd("5"), Currency: "EUR", Lot: ns("DYE-B")})
	require.NoError(t, err)
	lots, err = MS.ListMaterialLots(ctx, matID, false)
	require.NoError(t, err)
	var dyeBAlive bool
	for _, l := range lots {
		if l.Id == byCode["DYE-B"].Id {
			dyeBAlive = true
			require.False(t, l.Archived, "top-up un-archives the lot")
			require.True(t, l.RemainingQty.Equal(decimal.NewFromInt(15)), "DYE-B remaining 30-25+10=15, got %s", l.RemainingQty)
		}
	}
	require.True(t, dyeBAlive, "the reactivated lot is back in the active list")
	assertAvg5("after all lot operations")
}
