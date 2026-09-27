package entity

import (
	"errors"
	"fmt"
	"strings"
)

// DesignErrorCodePaidCollectWaiting — «the job is BOUGHT (an accepted request id is on record) and
// its route cannot collect it right now» (FAL_KEY absent, the route switched off). G-03 r2, Codex 4:
// the worker files this as a RETRYABLE failure, and the store reads the code as a WAIT — the run
// goes back to the queue at the capped back-off without spending a round of the ten-round ceiling,
// because no second purchase can come of it (a pass that finds an accepted id never submits) and
// closing it would throw away work that is paid for and free to collect once the key is back.
//
// The store honours the code ONLY together with an accepted, id-bearing attempt on the run (the
// second lock), so a stray code on an unpaid run is an ordinary retry under the ordinary ceilings.
// A person who wants the wait over cancels the run (CancelRun closes a pending run terminally).
const DesignErrorCodePaidCollectWaiting = "paid_collect_waiting"

// DesignPaidCollectWaits — does this failure put the run into the WAIT that spends no round of the
// store's ceilings (see DesignErrorCodePaidCollectWaiting)? Only a retryable, un-cancelled failure
// named paid_collect_waiting on a run that really holds an accepted, id-bearing attempt. hasAccepted
// is asked only when the rest already says yes (it is a query).
func DesignPaidCollectWaits(req DesignRunFail, cancelled bool, hasAccepted func() (bool, error)) (bool, error) {
	if !req.Retryable || cancelled || req.ErrorCode != DesignErrorCodePaidCollectWaiting || hasAccepted == nil {
		return false, nil
	}
	return hasAccepted()
}

// ErrDesignAttemptConflict — a second FinishAttempt on an attempt row that is already closed, with
// an outcome that contradicts the recorded one AND carries a paid request id that the row does not
// hold. It is returned rather than swallowed (G-03 r2, Codex 3): «idempotent success» here would
// tell the caller its paid id is persisted when it is not.
var ErrDesignAttemptConflict = errors.New("design: attempt already closed with a different outcome")

// DesignLateAcceptedFinish decides what a FinishAttempt that finds its row ALREADY CLOSED does.
//
// THE RACE IT EXISTS FOR (G-03 r2, Codex 3). Worker A opens an attempt and submits to fal; its lease
// expires while the call is still live (a process pause, a transport that ignores its context);
// worker B claims the run, sees the attempt open with no accepted id, and closes it `unknown`
// (dispatch.go, the pickup guard). Then fal answers A with a PAID request id, and A calls
// FinishAttempt(accepted, id). The row is closed, and the old rule — «closed means idempotent
// success» — dropped the id on the floor while telling A it was written.
//
// THE ANSWER: an accepted finish carrying an id is MORE INFORMATIVE than an `unknown` close (unknown
// literally means «nobody knows whether it was bought»), so it UPGRADES that row — state `accepted`,
// the id written — and moves no money (an accepted submit carries no price; the charge is booked by
// the collect). Any other contradiction that would lose an id — a different id already on the row,
// or a row closed in a state that is not `unknown` — returns ErrDesignAttemptConflict so the caller
// logs the id instead of believing it persisted.
//
// Returns (upgrade, err):
//   - (false, nil): nothing to do — the second call brings no id, or the row already holds this id
//     as `accepted` (a genuine repeat after a lost answer);
//   - (true, nil): write state=accepted and the id onto the closed row;
//   - (false, ErrDesignAttemptConflict): refuse loudly.
func DesignLateAcceptedFinish(existing DesignRunAttempt, req DesignAttemptFinish) (bool, error) {
	id := strings.TrimSpace(req.ProviderRequestId)
	if req.State != DesignAttemptAccepted || id == "" {
		// Not a paid id arriving late: the old idempotency stands (a repeat after a lost answer, a
		// late failure of a row somebody already closed — neither carries anything to lose).
		return false, nil
	}
	have := ""
	if existing.ProviderRequestId.Valid {
		have = strings.TrimSpace(existing.ProviderRequestId.String)
	}
	if have != "" && have != id {
		return false, fmt.Errorf("%w: attempt %d of run %d holds request %q and a late finish brought %q",
			ErrDesignAttemptConflict, existing.AttemptNo, existing.RunId, have, id)
	}
	switch existing.State {
	case DesignAttemptAccepted:
		if have == id {
			return false, nil
		}
		// accepted with no id on the row: the id fills the gap.
		return true, nil
	case DesignAttemptUnknown:
		return true, nil
	}
	return false, fmt.Errorf("%w: attempt %d of run %d is closed as %q and a late finish says fal accepted "+
		"request %q", ErrDesignAttemptConflict, existing.AttemptNo, existing.RunId, existing.State, id)
}
