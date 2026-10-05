package design

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// designRunUnheldSQL — nobody holds the row: never claimed / handed back (no token), or its claim
// expired. A LIVE CLAIM IS NEVER TOUCHED: the worker holding it closes its own capped run through
// its deadline path, and a sweep that closed it under the worker's feet would make the worker's
// CompleteRun lose and discard a paid result (FinishAttempt's `delivered` write is best-effort, so
// «no delivered attempt» does not prove nothing was delivered).
const designRunUnheldSQL = `(claim_token IS NULL OR claim_expires_at IS NULL OR claim_expires_at < UTC_TIMESTAMP(6))`

// CloseOverdueRuns — the wall-clock cap of an image run (entity.DesignOverdueSweep, owner 05.10),
// for runs NOBODY HOLDS: a capped run (pending or running) that started more than Cap ago and whose
// claim is absent or expired is closed — `landing_failed` when an attempt was delivered (never
// re-queued: a re-claim would buy the pictures again), `cancelled` when a cancel was asked, else
// `failed` / timed_out. The UPDATE re-checks the status AND the token it read AND that the claim is
// still not live, so a row claimed in between is left alone. Each close releases the reserve.
func (s *Store) CloseOverdueRuns(ctx context.Context, req entity.DesignOverdueSweep) (int, error) {
	if len(req.Kinds) == 0 || req.Cap <= 0 {
		return 0, nil
	}
	var closed int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		closed = 0
		db := rep.DB()
		capSec := int64(req.Cap.Seconds())
		rows, err := storeutil.QueryListNamed[entity.DesignRun](ctx, db, `
			SELECT * FROM design_run
			WHERE status IN ('pending', 'running')
			  AND kind IN (:kinds)
			  AND started_at IS NOT NULL
			  AND started_at < UTC_TIMESTAMP(6) - INTERVAL :cap SECOND
			  AND `+designRunUnheldSQL+`
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
			msg := fmt.Sprintf("took longer than %d min — try again", int(req.Cap.Round(time.Minute).Minutes()))
			switch {
			case delivered > 0:
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
				WHERE id = :id AND status = :was AND claim_token <=> :tok AND `+designRunUnheldSQL,
				map[string]any{"id": run.Id, "was": run.Status, "tok": run.ClaimToken,
					"status": status, "code": code, "msg": msg})
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

// CapClaim shortens a live claim to `until` (never lengthens it): a capped image run's lease ends
// shortly after its wall-clock cap, so a worker that died with it is swept in minutes, not after the
// full batch lease. Only the holder's own token moves it.
//
// `within` is measured from the database's own clock (no Go time crosses the wire).
func (s *Store) CapClaim(ctx context.Context, runID int, claimToken string, within time.Duration) error {
	if within <= 0 {
		return nil
	}
	if _, err := storeutil.ExecNamedRows(ctx, s.DB, `
		UPDATE design_run
		SET claim_expires_at = DATE_ADD(UTC_TIMESTAMP(6), INTERVAL :micros MICROSECOND)
		WHERE id = :id AND status = 'running' AND claim_token = :tok
		  AND claim_expires_at IS NOT NULL
		  AND claim_expires_at > DATE_ADD(UTC_TIMESTAMP(6), INTERVAL :micros MICROSECOND)`,
		map[string]any{"id": runID, "tok": claimToken, "micros": within.Microseconds()}); err != nil {
		return fmt.Errorf("failed to cap the claim of design run %d: %w", runID, err)
	}
	return nil
}
