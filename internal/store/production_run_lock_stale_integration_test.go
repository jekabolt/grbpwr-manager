package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestProductionRunOptimisticLock covers #9: lock_version is 0 on a fresh run, bumps on every
// UpdateProductionRun, a positive-but-stale expected_lock_version is refused with
// ErrProductionRunConflict, the matching version succeeds, and expected=0 keeps the legacy
// last-write-wins behaviour regardless of the stored version.
func TestProductionRunOptimisticLock(t *testing.T) {
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

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Lock Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("NF-LOCK-1"), MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	runID, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", runID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	upd := func(expected int) error {
		return PR.UpdateProductionRun(ctx, runID, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}},
		}, expected)
	}

	// fresh run starts at lock_version 0.
	run, err := PR.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, 0, run.LockVersion, "fresh run lock_version is 0")

	// expected=0 opts out of the lock and always applies; it still bumps the version.
	require.NoError(t, upd(0))
	run, err = PR.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, 1, run.LockVersion, "update bumps lock_version even when opted out")

	// a stale positive expected version is refused.
	require.ErrorIs(t, upd(0+99), entity.ErrProductionRunConflict, "stale expected_lock_version is refused")
	run, err = PR.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, 1, run.LockVersion, "a refused update does not bump the version")

	// the matching version succeeds and bumps again.
	require.NoError(t, upd(1))
	run, err = PR.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, 2, run.LockVersion)

	// re-using the now-stale version 1 is refused.
	require.ErrorIs(t, upd(1), entity.ErrProductionRunConflict)
}

// TestProductionRunStaleFilter covers #10: ListProductionRuns with StaleDays>0 returns only runs
// still open (planned/in_progress) whose created_at is older than the cutoff — the same rule as the
// stale_open_production_run dashboard alert. A fresh open run and an old but closed run are excluded.
func TestProductionRunStaleFilter(t *testing.T) {
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

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Stale Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("NF-STALE-1"), MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)

	mkRun := func(status entity.ProductionRunStatus) int {
		id, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: status,
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}},
		})
		require.NoError(t, err)
		return id
	}
	oldOpen := mkRun(entity.ProductionRunInProgress)  // old + open  -> stale
	freshOpen := mkRun(entity.ProductionRunPlanned)   // new + open  -> not stale
	oldClosed := mkRun(entity.ProductionRunInProgress) // old but will be closed -> not stale

	// backdate the two "old" runs 30 days; close one of them.
	old := time.Now().UTC().AddDate(0, 0, -30)
	for _, id := range []int{oldOpen, oldClosed} {
		_, err = testDB.ExecContext(ctx, "UPDATE production_run SET created_at = ? WHERE id = ?", old, id)
		require.NoError(t, err)
	}
	_, err = testDB.ExecContext(ctx, "UPDATE production_run SET status = ? WHERE id = ?", string(entity.ProductionRunClosed), oldClosed)
	require.NoError(t, err)

	t.Cleanup(func() {
		cctx := context.Background()
		for _, id := range []int{oldOpen, freshOpen, oldClosed} {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", id)
		}
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	// stale_days=7: only the old, still-open run qualifies.
	runs, total, err := PR.ListProductionRuns(ctx, 50, 0, entity.ProductionRunListFilter{TechCardId: tcID, StaleDays: 7})
	require.NoError(t, err)
	require.Equal(t, 1, total, "only one stale run")
	require.Len(t, runs, 1)
	require.Equal(t, oldOpen, runs[0].Id)

	// no stale filter: all three runs of this card are returned.
	all, totalAll, err := PR.ListProductionRuns(ctx, 50, 0, entity.ProductionRunListFilter{TechCardId: tcID})
	require.NoError(t, err)
	require.Equal(t, 3, totalAll)
	require.Len(t, all, 3)
}
