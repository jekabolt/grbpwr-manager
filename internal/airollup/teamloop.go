package airollup

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// The team loops the AI workers run on (designgen.TeamLoop has this exact shape). They live here,
// beside the rollup, because "which teams does AI work for" is one AI rule, and the app wires both.

// Loop walks teams: fn runs once per served team with ctx bound to it. A non-nil error with no team
// served means the list itself failed.
type Loop = func(ctx context.Context, fn func(ctx context.Context) error) error

// Overrides reads one team's explicit feature switches (platformstore.Store.FeatureOverrides). nil
// = no overrides anywhere (teams mode off): every team follows its profile.
type Overrides func(ctx context.Context, teamID int64) (map[string]bool, error)

// workCacheTTL — how long WorkLoop trusts its list of served teams. The design worker ticks every
// few seconds; the control plane must not be asked for the team list and every team's switches on
// each tick. 15 s is the feature cache of 02-DESIGN §9: a switch flipped in the console stops new
// AI work within it.
const workCacheTTL = 15 * time.Second

// DefaultParallel — how many teams one loop serves at once. Small on purpose: every team in flight
// holds database connections, and beta lekalo allows ten in all. AI_TEAM_PARALLEL overrides it
// (app/teams_ai.go), clamped to [1, MaxParallel].
const (
	DefaultParallel = 2
	MaxParallel     = 8
)

// ClampParallel keeps a configured parallelism inside [1, MaxParallel]; 0 or less = DefaultParallel.
func ClampParallel(n int) int {
	switch {
	case n <= 0:
		return DefaultParallel
	case n > MaxParallel:
		return MaxParallel
	}
	return n
}

// WorkLoop serves the teams AI may WORK for — claim and spend: listed, not suspended, `ai` on
// (feature.Effective). A team whose switches cannot be read is skipped this round with a WARN —
// fail closed: no work for a team whose `ai` switch is unknown. Teams mode off: the static directory
// lists team #1 (profile all), so the loop is team #1 alone. Up to parallel teams run at once.
// Cleanup of work a team already started never goes through here: that is CleanupLoop.
func WorkLoop(dir tenant.Directory, overrides Overrides, parallel int) Loop {
	var (
		mu      sync.Mutex
		cached  []tenant.Team
		cachedT time.Time
	)
	served := func(ctx context.Context) ([]tenant.Team, error) {
		mu.Lock()
		defer mu.Unlock()
		if !cachedT.IsZero() && time.Since(cachedT) < workCacheTTL {
			return cached, nil
		}
		teams, err := dir.ListActive(ctx)
		if err != nil {
			return nil, fmt.Errorf("airollup: list teams: %w", err)
		}
		out := make([]tenant.Team, 0, len(teams))
		for _, t := range teams {
			if t.Status != tenant.StatusActive {
				continue // suspended = read-only: no new paid work (02-DESIGN §7)
			}
			var ov map[string]bool
			if overrides != nil {
				if ov, err = overrides(ctx, t.ID); err != nil {
					slog.Default().WarnContext(ctx, "ai work: a team's feature switches could not be read; skipped this round",
						slog.Int64("tid", t.ID), slog.String("slug", t.Slug), slog.String("err", err.Error()))
					continue
				}
			}
			if !feature.Effective(t.ID, t.Profile, ov)[feature.AI] {
				continue
			}
			out = append(out, t)
		}
		cached, cachedT = out, time.Now()
		return out, nil
	}
	return func(ctx context.Context, fn func(ctx context.Context) error) error {
		teams, err := served(ctx)
		if err != nil {
			return err
		}
		return each(ctx, teams, parallel, fn)
	}
}

// CleanupLoop serves EVERY listed team — active and suspended, `ai` on or off: reviving dead
// leases, closing dead ledger rows and releasing stranded reserves is cleanup of work already
// started, never new work, so neither a pause nor the `ai` switch may stop it. Up to parallel teams
// run at once.
func CleanupLoop(dir tenant.Directory, parallel int) Loop {
	return func(ctx context.Context, fn func(ctx context.Context) error) error {
		teams, err := dir.ListActive(ctx)
		if err != nil {
			return fmt.Errorf("airollup: list teams: %w", err)
		}
		return each(ctx, teams, parallel, fn)
	}
}

// each runs fn per team, bound with tenant.With, at most parallel at once (ClampParallel), and
// returns when every started fn has returned. One slow team holds one slot, never the others: the
// next team starts as soon as any slot is free. The first error is returned (logged by the caller).
func each(ctx context.Context, teams []tenant.Team, parallel int, fn func(ctx context.Context) error) error {
	sem := make(chan struct{}, ClampParallel(parallel))
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
loop:
	for _, t := range teams {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		wg.Add(1)
		go func(t tenant.Team) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(tenant.With(ctx, t)); err != nil {
				mu.Lock()
				if first == nil {
					first = fmt.Errorf("team %s: %w", t.Slug, err)
				}
				mu.Unlock()
			}
		}(t)
	}
	wg.Wait()
	if first == nil {
		return ctx.Err()
	}
	return first
}
