package runpackaccess

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
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

// TestEveryTeamMintsAndOpensOnlyItsOwn — team 2 mints a v2 'r' token that opens under team 2 and
// is the uniform 404 under team 3 and team #1; team #1's v1 token stays v1 and is 404 under team 2.
func TestEveryTeamMintsAndOpensOnlyItsOwn(t *testing.T) {
	svc, _, _ := newTestService(t)
	tok2 := svc.MintRunPackToken(teamCtx(2), 41)
	tok1 := svc.MintRunPackToken(context.Background(), 41)
	if tok2 == "" || strings.Count(tok2, ".") != 3 || strings.Count(tok1, ".") != 2 {
		t.Fatalf("tokens: team2 %q team1 %q", tok2, tok1)
	}
	if got := svc.MintRunPackToken(teamCtx(tenant.TeamOneID), 41); got != tok1 {
		t.Fatalf("team #1 ctx minted %q, want the v1 token %q", got, tok1)
	}
	for _, c := range []struct {
		ctx  context.Context
		tok  string
		code int
	}{
		{teamCtx(2), tok2, http.StatusOK},
		{context.Background(), tok1, http.StatusOK},
		{teamCtx(3), tok2, http.StatusNotFound},
		{teamCtx(tenant.TeamOneID), tok2, http.StatusNotFound},
		{teamCtx(2), tok1, http.StatusNotFound},
	} {
		if w := serveRunPackCtx(svc, c.ctx, "/api/rp/"+c.tok); w.Code != c.code {
			t.Errorf("%q: %d, want %d", c.tok, w.Code, c.code)
		}
	}
}

// TestFlushKeepsTeam — counters recorded under team 2 are written with team 2 in ctx (also after a
// failed flush requeues them); counters recorded with no team flush with no team.
func TestFlushKeepsTeam(t *testing.T) {
	svc, runs, _ := newTestService(t)
	token := map[bool]string{true: svc.MintRunPackToken(teamCtx(2), 41), false: svc.MintRunPackToken(context.Background(), 41)}

	for _, ctx := range []context.Context{teamCtx(2), teamCtx(2), context.Background()} {
		_, inTeam := tenant.From(ctx)
		if w := serveRunPackCtx(svc, ctx, "/api/rp/"+token[inTeam]); w.Code != http.StatusOK {
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
