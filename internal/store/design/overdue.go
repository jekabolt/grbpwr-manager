package design

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// CloseOverdueRuns — the wall-clock cap of an image run (entity.DesignOverdueSweep, owner 05.10):
//
//   - a capped run (pending or running) that started more than Cap ago and has NO delivered attempt
//     is closed: `cancelled` when a cancel was asked, else `failed` / timed_out;
//   - one with a DELIVERED attempt (the pictures are bought and were being filed) gets LandingGrace
//     more, then `failed` / landing_failed — never re-queued: a re-claim would buy them again.
//
// Each close is one guarded UPDATE (the status it read) and releases the run's reserve, like the
// other sweeps. A live worker that later reports on a closed row meets ErrDesignRunTerminal.
func (s *Store) CloseOverdueRuns(ctx context.Context, req entity.DesignOverdueSweep) (int, error) {
	if len(req.Kinds) == 0 || req.Cap <= 0 {
		return 0, nil
	}
	var closed int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		closed = 0
		db := rep.DB()
		capSec := int64(req.Cap.Seconds())
		landSec := capSec + int64(req.LandingGrace.Seconds())
		rows, err := storeutil.QueryListNamed[entity.DesignRun](ctx, db, `
			SELECT * FROM design_run
			WHERE status IN ('pending', 'running')
			  AND kind IN (:kinds)
			  AND started_at IS NOT NULL
			  AND started_at < UTC_TIMESTAMP(6) - INTERVAL :cap SECOND
			ORDER BY id`, map[string]any{"kinds": req.Kinds, "cap": capSec})
		if err != nil {
			return fmt.Errorf("failed to read overdue design runs: %w", err)
		}
		for _, run := range rows {
			delivered, err := storeutil.QueryCountNamed(ctx, db, `
				SELECT COUNT(*) FROM design_run_attempt WHERE run_id = :run AND state = 'delivered'`,
				map[string]any{"run": run.Id})
			if err != nil {
				return fmt.Errorf("failed to read the attempts of design run %d: %w", run.Id, err)
			}
			status, code := "failed", entity.DesignErrorCodeTimedOut
			msg := fmt.Sprintf("took longer than %d min — try again", capSec/60)
			switch {
			case delivered > 0:
				// still inside the landing grace? the worker may be filing it right now
				young, err := storeutil.QueryCountNamed(ctx, db, `
					SELECT COUNT(*) FROM design_run
					WHERE id = :id AND started_at >= UTC_TIMESTAMP(6) - INTERVAL :land SECOND`,
					map[string]any{"id": run.Id, "land": landSec})
				if err != nil {
					return fmt.Errorf("failed to read design run %d: %w", run.Id, err)
				}
				if young > 0 {
					continue
				}
				code = entity.DesignErrorCodeLandingFailed
				msg = "the pictures were generated but could not be saved — try again"
			case run.CancelRequestedAt.Valid:
				status, code, msg = "cancelled", "cancelled", "cancelled"
			}
			n, err := storeutil.ExecNamedRows(ctx, db, `
				UPDATE design_run
				SET status = :status,
				    error_code = :code,
				    last_error = :msg,
				    completed_at = COALESCE(completed_at, UTC_TIMESTAMP(6)),
				    claim_token = NULL,
				    claim_expires_at = NULL
				WHERE id = :id AND status = :was`,
				map[string]any{"id": run.Id, "was": run.Status, "status": status, "code": code, "msg": msg})
			if err != nil {
				return fmt.Errorf("failed to close overdue design run %d: %w", run.Id, err)
			}
			if n == 1 {
				if err := releaseRunReserve(ctx, db, run); err != nil {
					return err
				}
				closed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return closed, nil
}
