package runpackaccess

import (
	"context"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PublicLinksAllowed refuses minting a public link (/api/rp) outside team #1.
//
// The v1 token is (scope, id, epoch) with no team, and the resolving route is bound to team #1, so
// a link minted in team 2 for local run 7 would open team #1's run 7. A ctx with no team is teams
// mode off (or a legacy caller) and is allowed exactly as before. Until the v2 token lands.
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
type statsBatch struct {
	team    tenant.Team
	hasTeam bool
	count   map[int]int64
	last    map[int]time.Time
}

// ctx returns parent carrying the batch's team; a batch recorded with no team keeps parent as is.
func (b *statsBatch) ctx(parent context.Context) context.Context {
	if !b.hasTeam {
		return parent
	}
	return tenant.With(parent, b.team)
}

// teamStats holds pending counters keyed by team. Not safe on its own: Service.statsMu guards it.
type teamStats map[statsKey]*statsBatch

func (ts teamStats) batch(key statsKey, team tenant.Team) *statsBatch {
	b := ts[key]
	if b == nil {
		b = &statsBatch{team: team, hasTeam: key.hasTeam, count: map[int]int64{}, last: map[int]time.Time{}}
		ts[key] = b
	}
	return b
}

func (ts teamStats) note(ctx context.Context, id int, at time.Time) {
	team, ok := tenant.From(ctx)
	key := statsKey{hasTeam: ok}
	if ok {
		key.teamID = team.ID
	}
	b := ts.batch(key, team)
	b.count[id]++
	b.last[id] = at
}

// size is the number of distinct (team, id) keys pending — the bound is per process, across teams.
func (ts teamStats) size() int {
	n := 0
	for _, b := range ts {
		n += len(b.count)
	}
	return n
}

// merge ADDS a failed batch back (counts summed, last access = max) and returns how many new keys
// it dropped because the process-wide pending bound max was reached.
func (ts teamStats) merge(b *statsBatch, max int) (dropped int) {
	key := statsKey{teamID: b.team.ID, hasTeam: b.hasTeam}
	total := ts.size()
	for id, n := range b.count {
		dst := ts[key]
		if _, ok := pendingCount(dst, id); !ok {
			if total >= max {
				dropped++
				continue
			}
			total++
			dst = ts.batch(key, b.team)
		}
		dst.count[id] += n
		if t, ok := b.last[id]; ok {
			if cur, seen := dst.last[id]; !seen || t.After(cur) {
				dst.last[id] = t
			}
		}
	}
	return dropped
}

func pendingCount(b *statsBatch, id int) (int64, bool) {
	if b == nil {
		return 0, false
	}
	n, ok := b.count[id]
	return n, ok
}
