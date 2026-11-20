package patternaccess

import (
	"context"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

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
