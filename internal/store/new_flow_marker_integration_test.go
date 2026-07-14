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

// TestProductionRunMarkers exercises gap-07 v2 E: nesting markers are children of a run — inserted
// on create, read back on Get and List, full-replaced on Update, and cascade-deleted with the run.
func TestProductionRunMarkers(t *testing.T) {
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

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	PR := s.ProductionRuns()

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Marker Fabric", Section: "fabric", Unit: ns("m")})
	require.NoError(t, err)
	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Marker Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("NF-MRK-1"), MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)

	runID, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}},
		Markers: []entity.ProductionRunMarker{
			{
				Source: entity.ProductionMarkerSourceGerber, MarkerName: ns("M-A"),
				SizeId: sql.NullInt32{Int32: 1, Valid: true}, MaterialId: sql.NullInt32{Int32: int32(matID), Valid: true},
				MarkerWidth:    decimal.NullDecimal{Decimal: decimal.NewFromInt(150), Valid: true},
				LayLength:      decimal.NullDecimal{Decimal: decimal.RequireFromString("6.40"), Valid: true},
				UnitsPerMarker: sql.NullInt32{Int32: 12, Valid: true},
				EfficiencyPct:  decimal.NullDecimal{Decimal: decimal.RequireFromString("87.50"), Valid: true},
				MarkerFileUrl:  ns("https://cdn/marker-a.mrk"),
			},
			{Source: entity.ProductionMarkerSourceManual, MarkerName: ns("M-B")}, // sparse marker
		},
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", runID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", tcID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
	})

	// --- Get returns both markers, insertion-ordered, fields intact ---
	run, err := PR.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Len(t, run.Markers, 2)
	a := run.Markers[0]
	require.Equal(t, entity.ProductionMarkerSourceGerber, a.Source)
	require.Equal(t, "M-A", a.MarkerName.String)
	require.Equal(t, int32(1), a.SizeId.Int32)
	require.Equal(t, int32(matID), a.MaterialId.Int32)
	require.True(t, a.MarkerWidth.Decimal.Equal(decimal.NewFromInt(150)))
	require.True(t, a.LayLength.Decimal.Equal(decimal.RequireFromString("6.40")))
	require.Equal(t, int32(12), a.UnitsPerMarker.Int32)
	require.True(t, a.EfficiencyPct.Decimal.Equal(decimal.RequireFromString("87.50")))
	require.Equal(t, "https://cdn/marker-a.mrk", a.MarkerFileUrl.String)
	// sparse marker: source + name only, everything else NULL.
	b := run.Markers[1]
	require.Equal(t, "M-B", b.MarkerName.String)
	require.False(t, b.SizeId.Valid)
	require.False(t, b.MarkerWidth.Valid)

	// --- List attaches markers too ---
	runs, _, err := PR.ListProductionRuns(ctx, 50, 0, entity.ProductionRunListFilter{TechCardId: tcID})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Len(t, runs[0].Markers, 2)

	// --- Update full-replaces the marker set ---
	err = PR.UpdateProductionRun(ctx, runID, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines:   []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}},
		Markers: []entity.ProductionRunMarker{{Source: entity.ProductionMarkerSourceOptitex, MarkerName: ns("M-C")}},
	})
	require.NoError(t, err)
	run, err = PR.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Len(t, run.Markers, 1, "old markers replaced")
	require.Equal(t, "M-C", run.Markers[0].MarkerName.String)
	require.Equal(t, entity.ProductionMarkerSourceOptitex, run.Markers[0].Source)

	// clearing the set removes all markers.
	err = PR.UpdateProductionRun(ctx, runID, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}},
	})
	require.NoError(t, err)
	var markerCount int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM production_run_marker WHERE run_id = ?", runID).Scan(&markerCount))
	require.Equal(t, 0, markerCount)

	// --- g25-13: a run cannot move to another tech card ---
	tc2, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Marker Style 2", Stage: entity.TechCardStageProto,
		StyleNumber: ns("NF-MRK-2"), MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tc2) })
	err = PR.UpdateProductionRun(ctx, runID, &entity.ProductionRunInsert{
		TechCardId: tc2, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}},
	})
	require.ErrorIs(t, err, entity.ErrProductionRunCardChange, "re-pointing a run to another style is refused")

	// --- markers cascade-delete with the run ---
	// re-add one, then delete the run and confirm the child row is gone.
	require.NoError(t, PR.UpdateProductionRun(ctx, runID, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines:   []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}},
		Markers: []entity.ProductionRunMarker{{Source: entity.ProductionMarkerSourceManual, MarkerName: ns("M-D")}},
	}))
	require.NoError(t, PR.DeleteProductionRun(ctx, runID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM production_run_marker WHERE run_id = ?", runID).Scan(&markerCount))
	require.Equal(t, 0, markerCount, "markers cascade-deleted with the run")
}
