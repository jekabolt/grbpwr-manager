package runpackaccess

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func teamCtx(id int64) context.Context {
	return tenant.With(context.Background(), tenant.Team{ID: id, Slug: "t"})
}

func serveRunPackCtx(svc *Service, ctx context.Context, target string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(http.MethodGet, "/api/rp/{token}", svc.Handler())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx))
	return w
}

// TestMintRefusedOutsideTeamOne — a v1 'r' token has no team and resolves in team #1, so minting
// it in team 2 would hand out team #1's run with the same local id.
func TestMintRefusedOutsideTeamOne(t *testing.T) {
	if err := PublicLinksAllowed(teamCtx(2)); status.Code(err) != codes.FailedPrecondition ||
		status.Convert(err).Message() != "public_links_team_one_only" {
		t.Fatalf("team 2: want FailedPrecondition public_links_team_one_only, got %v", err)
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		mint bool
	}{
		{"no team", context.Background(), true},
		{"team one", teamCtx(tenant.TeamOneID), true},
		{"team two", teamCtx(2), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, runs, _ := newTestService(t)
			got := svc.MintRunPackToken(tc.ctx, 41)
			if (got != "") != tc.mint {
				t.Fatalf("minted %q, want mint=%v", got, tc.mint)
			}
			if !tc.mint && runs.ensured != 0 {
				t.Fatalf("refused mint must not create the access row (ensured=%d)", runs.ensured)
			}
		})
	}
}

// TestFlushKeepsTeam — counters recorded under team 2 are written with team 2 in ctx (also after a
// failed flush requeues them); counters recorded with no team flush with no team.
func TestFlushKeepsTeam(t *testing.T) {
	svc, runs, _ := newTestService(t)
	token := svc.MintRunPackToken(context.Background(), 41)

	for _, ctx := range []context.Context{teamCtx(2), teamCtx(2), context.Background()} {
		if w := serveRunPackCtx(svc, ctx, "/api/rp/"+token); w.Code != http.StatusOK {
			t.Fatalf("serve: %d", w.Code)
		}
	}
	runs.recordErr = errors.New("db down")
	svc.flushStats()
	runs.recordErr = nil
	svc.flushStats()

	var team2, plain int
	for _, f := range runs.flushes {
		if f.failed {
			continue
		}
		switch {
		case f.hasTeam && f.teamID == 2 && f.counts[41] == 2:
			team2++
		case !f.hasTeam && f.counts[41] == 1:
			plain++
		default:
			t.Errorf("unexpected flush %+v", f)
		}
	}
	if team2 != 1 || plain != 1 {
		t.Fatalf("flushes %+v: want one team-2 write of 2 and one plain write of 1", runs.flushes)
	}
}

// TestPendingBoundIsAcrossTeams — the pending bound is per process, summed over teams.
func TestPendingBoundIsAcrossTeams(t *testing.T) {
	svc, runs, _ := newTestService(t)
	runs.recordErr = errors.New("db down")
	for i := 1; i <= maxPendingRuns-10; i++ {
		svc.noteAccess(teamCtx(2), i)
	}
	for i := 1; i <= 50; i++ {
		svc.noteAccess(context.Background(), i)
	}
	svc.flushStats()
	svc.statsMu.Lock()
	pending := svc.stats.size()
	svc.statsMu.Unlock()
	if pending != maxPendingRuns {
		t.Fatalf("pending = %d, want process-wide bound %d", pending, maxPendingRuns)
	}
}
