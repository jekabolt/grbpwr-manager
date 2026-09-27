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
