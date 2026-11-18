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

// WorkLoop serves the teams AI may WORK for: listed (active or suspended), not suspended, `ai` on
// (feature.Effective). A team whose switches cannot be read is skipped this round with a WARN —
// fail closed: no work for a team whose `ai` switch is unknown. Teams mode off: the static directory
// lists team #1 (profile all), so the loop is team #1 alone.
func WorkLoop(dir tenant.Directory, overrides Overrides) Loop {
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
		return each(ctx, teams, fn)
	}
}

// SweepLoop serves EVERY listed team, suspended and `ai`-off ones included: closing dead ledger rows
// and releasing stranded reserves is cleanup of work already done, never new work.
func SweepLoop(dir tenant.Directory) Loop {
	return func(ctx context.Context, fn func(ctx context.Context) error) error {
		return tenant.Each(ctx, dir, func(tctx context.Context, _ tenant.Team) error { return fn(tctx) })
	}
}

// each runs fn per team in order, bound with tenant.With; errors are joined like tenant.Each's, and
// logged by the caller.
func each(ctx context.Context, teams []tenant.Team, fn func(ctx context.Context) error) error {
	var first error
	for _, t := range teams {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(tenant.With(ctx, t)); err != nil && first == nil {
			first = fmt.Errorf("team %s: %w", t.Slug, err)
		}
	}
	return first
}
