package aiprov

import (
	"context"
	"strings"
)

// Actor is WHO a provider call is made for — the "по аккаунтам" half of the spend report. Username
// is the admin login as the auth interceptor sees it (it is also what design_run.author holds);
// AdminID is the admin row id when it is known, nil when the lookup failed or there is no admin
// behind the call. The ledger stores both: the name reads well, the id survives a rename.
type Actor struct {
	Username string
	AdminID  *int
}

// Actor names that are not an admin login.
const (
	// ActorSystem — a call nobody pressed a button for: a background worker, a sweeper, a boot probe.
	ActorSystem = "system"
	// ActorUnknown — a call whose context carries no actor. It is a WIRING defect, not a person: a
	// ledger row with this name says the path that made it forgot to call WithActor, and the report
	// shows it as its own line instead of silently folding the money into somebody's total.
	ActorUnknown = "unknown"
)

type actorContextKey struct{}

// WithActor returns ctx carrying a. The AdminID is copied, so a caller that reuses its int after
// this call cannot rewrite the actor of calls already in flight.
func WithActor(ctx context.Context, a Actor) context.Context {
	if a.AdminID != nil {
		id := *a.AdminID
		a.AdminID = &id
	}
	return context.WithValue(ctx, actorContextKey{}, a)
}

// ActorFrom returns the actor WithActor put into ctx. A context without one — or with a blank
// Username — yields {Username: ActorUnknown} (a blank name keeps its AdminID, if any): the ledger's
// actor column must never be an empty string that the report cannot tell apart from "not set".
// The returned AdminID is a fresh copy.
func ActorFrom(ctx context.Context) Actor {
	if ctx == nil {
		return Actor{Username: ActorUnknown}
	}
	a, ok := ctx.Value(actorContextKey{}).(Actor)
	if !ok {
		return Actor{Username: ActorUnknown}
	}
	if a.AdminID != nil {
		id := *a.AdminID
		a.AdminID = &id
	}
	if strings.TrimSpace(a.Username) == "" {
		a.Username = ActorUnknown
	}
	return a
}

// ───────────────────────── run ─────────────────────────

// run is the design run attempt a provider call is made for — the ledger row's (run_id, attempt_no).
type run struct{ runID, attemptNo int }

type runContextKey struct{}

// WithRun returns ctx carrying the design run attempt the calls made under it belong to, so the
// ledger row of each of them names it: (run_id, attempt_no) is how the spend report and the sweeper
// find a run's calls, and UNIQUE (run_id, attempt_no, call_no) is what keeps a retried write from
// booking one call twice. The router numbers its physical calls under ONE Chat 1..N (a fallback is
// call 2), so one attempt must be one Router.Chat — two Chats under the same attempt would both
// start at 1 and the store would refuse the second row.
//
// Only a positive pair is a run: RunFrom answers (nil, nil) for anything else, and the row is then
// booked without a run instead of being refused by the store (run_id must be positive, attempt_no
// 1..255) and LOST — a keyless row still carries the money.
func WithRun(ctx context.Context, runID, attemptNo int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, runContextKey{}, run{runID: runID, attemptNo: attemptNo})
}

// RunFrom returns the run attempt WithRun put into ctx as fresh pointers (a caller writing through
// them changes nobody else's row), or (nil, nil) when ctx carries none, or carries a pair that is
// not a run (either number < 1): a chat that belongs to no run books NULLs, which never collide.
func RunFrom(ctx context.Context) (runID, attemptNo *int) {
	if ctx == nil {
		return nil, nil
	}
	r, ok := ctx.Value(runContextKey{}).(run)
	if !ok || r.runID < 1 || r.attemptNo < 1 {
		return nil, nil
	}
	id, no := r.runID, r.attemptNo
	return &id, &no
}
