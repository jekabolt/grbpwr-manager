package registry

import (
	"context"
	"fmt"
)

// TEAM PIN (teams wave, 02-DESIGN §10). The AI configuration is process-wide and lives in team #1's
// database: ai_provider, ai_model, ai_route, ai_settings and design_settings.budget_timezone. Every
// call the registry makes to its store — Reload, the poller's ConfigVersion, ImportEnvKeys and its
// SetProviderKey — runs on pin(ctx), so whatever team the caller's ctx carries (a request of team 2
// that saved nothing, a per-team worker tick) the registry reads and writes team #1's rows only.
// Other teams' copies of those tables are inert, and their keys never exist.
//
// A pin error fails the call exactly like a database error: Reload returns it and the previous
// snapshot stays in force; a poll counts as failed and backs off.
//
// Without WithPin the pin is the identity: the store sees the caller's ctx unchanged (teams mode
// off, tests).

// WithPin sets the function every store call's ctx goes through. The app passes
// `func(ctx) { return tenant.Pinned(ctx, dir, 1) }`. nil keeps the identity.
func WithPin(pin func(context.Context) (context.Context, error)) Option {
	return func(r *Registry) {
		if pin != nil {
			r.pin = pin
		}
	}
}

// pinned returns the ctx a store call runs on.
func (r *Registry) pinned(ctx context.Context) (context.Context, error) {
	if r.pin == nil {
		return ctx, nil
	}
	pctx, err := r.pin(ctx)
	if err != nil {
		return ctx, fmt.Errorf("ai registry: pin the config team: %w", err)
	}
	return pctx, nil
}
