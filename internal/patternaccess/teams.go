package patternaccess

import (
	"context"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PublicLinksAllowed refuses minting a public link (/api/p, /api/pv) outside team #1.
//
// The v1 token is (scope, id, epoch) with no team, and the resolving routes are bound to team #1,
// so a link minted in team 2 for local id 7 would open team #1's object 7. A ctx with no team is
// teams mode off (or a legacy caller) and is allowed exactly as before. Until the v2 token lands.
func PublicLinksAllowed(ctx context.Context) error {
	if t, ok := tenant.From(ctx); ok && t.ID != tenant.TeamOneID {
		return status.Error(codes.FailedPrecondition, "public_links_team_one_only")
	}
	return nil
}

// statsKey identifies one pending batch: the team the accesses were recorded under, or none.
type statsKey struct {
	teamID  int64
	hasTeam bool
}

// statsBatch is one team's debounced counters. The flush writes it with the team in ctx, because
// in teams mode the store refuses a ctx with no team.
type statsBatch[K comparable] struct {
	team    tenant.Team
	hasTeam bool
	count   map[K]int64
	last    map[K]time.Time
}

// ctx returns parent carrying the batch's team; a batch recorded with no team keeps parent as is.
func (b *statsBatch[K]) ctx(parent context.Context) context.Context {
	if !b.hasTeam {
		return parent
	}
	return tenant.With(parent, b.team)
}

// teamStats holds pending counters keyed by team. Not safe on its own: Service.statsMu guards it.
type teamStats[K comparable] map[statsKey]*statsBatch[K]

func (ts teamStats[K]) note(ctx context.Context, id K, at time.Time) {
	team, ok := tenant.From(ctx)
	key := statsKey{hasTeam: ok}
	if ok {
		key.teamID = team.ID
	}
	b := ts[key]
	if b == nil {
		b = &statsBatch[K]{team: team, hasTeam: ok, count: map[K]int64{}, last: map[K]time.Time{}}
		ts[key] = b
	}
	b.count[id]++
	b.last[id] = at
}
