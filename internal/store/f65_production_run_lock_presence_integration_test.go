package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestProductionRunFreshLockVersionIsGuarded is the Ф6.5 acceptance test: the optimistic lock must
// protect the FIRST edit of a run, not only the second.
//
// A freshly created run is born at lock_version = 0, and 0120:9-10 declared 0 to mean "skip the
// check". So every run's first save — the one where two tabs opened from the same list are most
// likely to collide, because neither has saved yet — went through unguarded, and the second writer
// silently clobbered the first. The token has to carry PRESENCE (did the caller send a version?),
// never MAGNITUDE (is that version positive?).
//
// SAFE ONLY against a local container DSN — see the guard and mysql_test.go / project memory.
func TestProductionRunFreshLockVersionIsGuarded(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	PR := s.ProductionRuns()

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "F65 Lock Presence Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("F65-LOCK-1"), MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	newRun := func(t *testing.T) int {
		t.Helper()
		id, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", id)
		})
		run, err := PR.GetProductionRun(ctx, id)
		require.NoError(t, err)
		require.Equal(t, 0, run.LockVersion, "a freshly created run is born at lock_version 0")
		return id
	}

	// save is the run's own edit path, as the admin client drives it: read the run, echo back the
	// lock_version that came with it, save.
	save := func(runID, qty int, expected entity.LockGuard) error {
		return PR.UpdateProductionRun(ctx, runID, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: qty}},
		}, expected, dto.CostingFx{})
	}

	t.Run("two tabs editing a FRESH run: the second is refused", func(t *testing.T) {
		runID := newRun(t)

		// Both tabs read the run at lock_version 0 and both echo that 0 back. This is not a
		// contrived value: it is literally what GetProductionRun hands a client for any run that
		// has never been saved, and what the admin client sends back (JSON.stringify of the
		// request object puts the read version — including a literal 0 — on the wire).
		require.NoError(t, save(runID, 30, entity.LockVersion(0)), "the first writer of a fresh run wins")

		// The second tab still holds version 0, but the run is at 1. Its save is counted against a
		// grid that no longer exists and MUST be refused.
		require.ErrorIs(t, save(runID, 40, entity.LockVersion(0)), entity.ErrProductionRunConflict,
			"a second save counted against the fresh run's version 0 must be refused, not silently applied")

		// And the loser's quantities must not be on the row.
		run, err := PR.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, 1, run.LockVersion, "a refused save does not bump the version")
		require.EqualValues(t, 30, run.Lines[0].PlannedQty, "the first writer's grid survives")
	})

	t.Run("receive counted against a FRESH run's version 0 is refused", func(t *testing.T) {
		runID := newRun(t)

		// The warehouse opens the receive modal on the fresh run and reads lock_version 0.
		counted := entity.LockVersion(0)

		// Meanwhile the planner edits the grid: the run moves to lock_version 1 and the quantities
		// the warehouse is counting against are gone.
		require.NoError(t, save(runID, 999, counted))

		// The receipt is posted against the version the operator counted against. The guard sits
		// ahead of line resolution in PostProductionRunReceipt, so a conflict must surface as a
		// conflict — reaching the line lookup at all proves the guard never fired.
		key, err := entity.MintProductionRunLineKey()
		require.NoError(t, err)
		lines := []entity.ProductionRunReceiptLineInput{{LineKey: "F65GHOSTLINEKEY00000000000"[:26], GoodQty: 1}}
		_, err = PR.PostProductionRunReceipt(ctx, entity.PostProductionRunReceiptParams{
			RunID:               runID,
			Lines:               lines,
			IdempotencyKey:      key,
			RequestHash:         dto.HashProductionRunReceiptPayload(runID, lines, "", false, true),
			ExpectedLockVersion: counted,
			Username:            "tester",
			Final:               true,
		})
		require.ErrorIs(t, err, entity.ErrProductionRunConflict,
			"a receipt counted against the fresh run's version 0 must be refused after a concurrent edit")
	})

	// The other half of the contract: presence must not become a silent break for callers that send
	// nothing. A pre-Ф6.5 client (and the deprecated ReceiveProductionRun shim) omits the field, and
	// an omitted field has to keep behaving exactly as it did yesterday — last write wins, at ANY
	// stored version. Without this, the fix would trade a race for an outage.
	t.Run("an ABSENT token still opts out, at any stored version", func(t *testing.T) {
		runID := newRun(t)

		require.NoError(t, save(runID, 10, entity.NoLockVersion()), "absent token applies on a fresh run")
		require.NoError(t, save(runID, 20, entity.NoLockVersion()), "and keeps applying once the version has moved")

		run, err := PR.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, 2, run.LockVersion, "the legacy path still bumps the version it does not check")
		require.EqualValues(t, 20, run.Lines[0].PlannedQty)

		// A PRESENT token at the same stored version is the new-client path and must succeed.
		require.NoError(t, save(runID, 25, entity.LockVersion(run.LockVersion)))
	})
}
