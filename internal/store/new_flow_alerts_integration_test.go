package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/metrics"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestDashboardAlertQueries exercises the two NF-09 dashboard-alert queries at the SQL level
// (nf09-07 #2). The metrics package has no live-DB test harness of its own — the store package owns
// the single DB-owning test binary — so these run here against the concrete *metrics.Store via the
// exported GetLowStockMaterials / GetStaleOpenRunCount (the unit tests in dashboard_test.go only
// inject pre-computed counts into buildDashboardAlerts, so the queries themselves were untested).
func TestDashboardAlertQueries(t *testing.T) {
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

	mtr, ok := s.Metrics().(*metrics.Store)
	require.True(t, ok, "metrics store is the concrete *metrics.Store")

	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	MS := s.MaterialStock()

	// --- getLowStockMaterials: LEFT JOIN, NULL exclusion, boundary `<` ---
	// Distinct names so membership in the (shared-DB) result set is unambiguous.
	mkMat := func(name string, minStock decimal.NullDecimal) int {
		id, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
			Name: name, Section: "fabric", Unit: sql.NullString{String: "m", Valid: true}, MinStock: minStock,
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			cctx := context.Background()
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", id)
		})
		return id
	}

	// M_low: has a min_stock but NO movements → no material_stock row at all. The LEFT JOIN + COALESCE
	// must still surface it (the nf09-01 fix); an INNER JOIN would silently drop it.
	nameLow := "NF Alert Low (no stock row)"
	mkMat(nameLow, nd("5"))

	// M_null: no min_stock → can never be low-stock, regardless of on-hand.
	nameNull := "NF Alert NullMin"
	mkMat(nameNull, decimal.NullDecimal{})

	// M_eq: on-hand exactly equals min_stock → NOT below (boundary is strict `<`).
	nameEq := "NF Alert Boundary"
	eqID := mkMat(nameEq, nd("4"))
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: eqID, Quantity: decimal.NewFromInt(4), UnitCost: nd("1"), Currency: "EUR"})
	require.NoError(t, err)

	// M_below: on-hand below min_stock → included.
	nameBelow := "NF Alert Below"
	belowID := mkMat(nameBelow, nd("10"))
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: belowID, Quantity: decimal.NewFromInt(3), UnitCost: nd("1"), Currency: "EUR"})
	require.NoError(t, err)

	// limit 0 = no name cap, so our materials can't be pushed out of the returned slice by shared data.
	names, count, err := mtr.GetLowStockMaterials(ctx, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, count, 2, "at least our two below-min materials are counted")
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	require.True(t, got[nameLow], "material with min_stock and no stock row is low-stock (LEFT JOIN)")
	require.True(t, got[nameBelow], "material below its min_stock is low-stock")
	require.False(t, got[nameNull], "material with no min_stock is never low-stock")
	require.False(t, got[nameEq], "on-hand == min_stock is NOT below (strict `<`)")
	require.Len(t, names, count, "with no cap the name list matches the total count")

	// --- getStaleOpenRunCount: open-status filter + created_at cutoff ---
	staleTC, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Stale Run Style", Stage: entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "NF-STALE-1", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState:   entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	// Fresh context: this test's ctx is cancelled by its own `defer cancel()` before t.Cleanup
	// callbacks run (defers run before Cleanups), which would make this DELETE a no-op and leak
	// the tech card (style_number is globally UNIQUE) into later tests.
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", staleTC) })

	mkRun := func() int {
		id, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: staleTC, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 5}},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", id) })
		return id
	}
	old := time.Now().UTC().AddDate(0, 0, -100) // well past the 60-day threshold

	const staleDays = 60
	before, err := mtr.GetStaleOpenRunCount(ctx, staleDays)
	require.NoError(t, err)

	// R_stale_open: open + old → counted.
	staleOpen := mkRun()
	_, err = testDB.ExecContext(ctx, "UPDATE production_run SET created_at = ? WHERE id = ?", old, staleOpen)
	require.NoError(t, err)
	// R_recent_open: open but created just now → NOT counted (inside the window).
	mkRun()
	// R_stale_received: old but already received → NOT counted (status filter).
	staleReceived := mkRun()
	_, err = testDB.ExecContext(ctx, "UPDATE production_run SET status = ?, created_at = ? WHERE id = ?",
		string(entity.ProductionRunReceived), old, staleReceived)
	require.NoError(t, err)

	after, err := mtr.GetStaleOpenRunCount(ctx, staleDays)
	require.NoError(t, err)
	require.Equal(t, before+1, after, "only the old OPEN run is stale (recent-open and received-old are excluded)")

	// staleDays <= 0 disables the check entirely.
	off, err := mtr.GetStaleOpenRunCount(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, 0, off, "staleDays <= 0 disables the stale-run count")
}
