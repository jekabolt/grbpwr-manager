package design

import (
	"context"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// THE GENERATIVE HALF OF THE BAND — SIGNATURES ONLY, BODIES NEXT WAVE.
//
// WHY THEY ARE HERE AT ALL. dependency.Design and its registration in internal/store/store.go are
// the SEAM between this wave and the next. If the interface ships incomplete, the next executor
// has to reopen exactly those two files to add the missing signatures — and those are the two
// files every other agent in this tree also has a reason to touch. A defect born on a seam is the
// most expensive kind here, so the whole surface is frozen now and the next wave adds FILES to
// this package instead of editing the seam.
//
// The bodies are cut from this wave for a measured reason, not for scheduling: internal/openrouter
// has no picture parsing in its response path at all, there is no 3D provider, and the 4 MiB
// response ceiling is smaller than a single base64 PNG. So the manual path ships alone, and every
// method below refuses honestly rather than pretending to work.
//
// Each of these must keep the properties written into the plan when it grows a body:
//   - StartRun: ONE transaction — reserve the day's budget, check `spent + reserved <= cap`, take
//     the input snapshot, insert the run and its attempt 0. A duplicate client_request_id is
//     caught by the UNIQUE key and returns the EXISTING row with OK.
//   - ClaimRuns: SELECT … FOR UPDATE SKIP LOCKED, then an UPDATE that REPEATS THE WHOLE PREDICATE,
//     lease included — without the repeat a second claim steals a live token and the first worker
//     can never finish its run.
//   - ReviveExpiredRuns: without it «an expired claim is the same road» is a road with no legs,
//     because ClaimRuns only ever takes `pending`.
//   - StartAttempt / FinishAttempt: the paid attempt is its own ROW, and the day's counters move
//     with it — a retry pays twice and the budget bar must SEE that.
//   - CompleteRun: idempotent picture insertion on uq_design_picture_run_ordinal; a partial answer
//     means fewer pictures and still `done`.
//   - MintSheetVersion: writes the DOCUMENT with the same code as UpdateTechCard and mints the
//     version in ONE transaction. The transaction callback already hands over the whole
//     repository, so rep.TechCards() and rep.Design() are in the same transaction — that property
//     is what makes the atomic mint expressible, and it must not be narrowed.

// StartRun opens a paid job: budget reservation, input snapshot and the run row in one
// SERIALIZABLE transaction.
func (s *Store) StartRun(ctx context.Context, req entity.DesignRunStart) (*entity.DesignRunStarted, error) {
	return nil, entity.ErrDesignNotImplemented
}

// ClaimRuns leases up to n pending runs to a worker.
func (s *Store) ClaimRuns(ctx context.Context, n int, lease time.Duration, claimToken string) ([]entity.DesignRun, error) {
	return nil, entity.ErrDesignNotImplemented
}

// ReviveExpiredRuns returns runs whose lease expired to `pending`.
func (s *Store) ReviveExpiredRuns(ctx context.Context) (int, error) {
	return 0, entity.ErrDesignNotImplemented
}

// StartAttempt opens one paid provider call.
func (s *Store) StartAttempt(ctx context.Context, req entity.DesignAttemptStart) (*entity.DesignRunAttempt, error) {
	return nil, entity.ErrDesignNotImplemented
}

// FinishAttempt closes one paid provider call and moves the day's counters.
func (s *Store) FinishAttempt(ctx context.Context, req entity.DesignAttemptFinish) error {
	return entity.ErrDesignNotImplemented
}

// CompleteRun files the outputs and closes the run.
func (s *Store) CompleteRun(ctx context.Context, req entity.DesignRunComplete) (*entity.DesignRun, error) {
	return nil, entity.ErrDesignNotImplemented
}

// FailRun records a failure: exponential retry or a terminal `failed`.
func (s *Store) FailRun(ctx context.Context, req entity.DesignRunFail) (*entity.DesignRun, error) {
	return nil, entity.ErrDesignNotImplemented
}

// CancelRun stops a run: `pending` becomes `cancelled` and the day's reservation is released;
// `running` gets cancel_requested_at and the worker honours it either side of the dispatch.
func (s *Store) CancelRun(ctx context.Context, runID int, actor string) (*entity.DesignRun, error) {
	return nil, entity.ErrDesignNotImplemented
}

// MintSheetVersion writes the document and mints the frozen version in ONE transaction.
func (s *Store) MintSheetVersion(ctx context.Context, req entity.DesignSheetMint) (*entity.DesignSheetVersionFull, error) {
	return nil, entity.ErrDesignNotImplemented
}
