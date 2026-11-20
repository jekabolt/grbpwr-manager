package fileaccess

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

func teamCtx(id int64) context.Context {
	return tenant.With(context.Background(), tenant.Team{ID: id, Slug: "t"})
}

func serveFileCtx(svc *Service, ctx context.Context, target string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Method(http.MethodGet, "/api/f/{token}", svc.Handler())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx))
	return w
}

// TestLinkURLContextEveryTeam — team #1 (and no team) keeps the v1 url; team 2 gets a v2 url that
// opens under team 2 only.
func TestLinkURLContextEveryTeam(t *testing.T) {
	files := &fakeFiles{rows: map[int]*entity.LibraryFileLinkTarget{7: linkTarget(7, 1)}}
	svc := newTestService(t, files, &fakePresign{})
	v1 := svc.LinkURL(7, 1)
	if got := svc.LinkURLContext(context.Background(), 7, 1); got != v1 {
		t.Fatalf("no team: %q, want %q", got, v1)
	}
	if got := svc.LinkURLContext(teamCtx(tenant.TeamOneID), 7, 1); got != v1 {
		t.Fatalf("team one: %q, want %q", got, v1)
	}
	v2 := svc.LinkURLContext(teamCtx(2), 7, 1)
	if v2 == "" || v2 == v1 || strings.Count(v2[strings.LastIndex(v2, "/")+1:], ".") != 3 {
		t.Fatalf("team two: %q", v2)
	}
	path := func(u string) string { return u[strings.Index(u, "/api/f/"):] }
	for _, c := range []struct {
		ctx  context.Context
		u    string
		code int
	}{
		{teamCtx(2), v2, http.StatusFound},
		{context.Background(), v1, http.StatusFound},
		{teamCtx(3), v2, http.StatusNotFound},
		{teamCtx(tenant.TeamOneID), v2, http.StatusNotFound},
		{teamCtx(2), v1, http.StatusNotFound},
	} {
		if w := serveFileCtx(svc, c.ctx, path(c.u)); w.Code != c.code {
			t.Errorf("%s: %d, want %d", c.u, w.Code, c.code)
		}
	}
	var nilSvc *Service
	if nilSvc.LinkURLContext(context.Background(), 7, 1) != "" {
		t.Fatal("a nil service must mint nothing")
	}
}

// TestFlushKeepsTeam — counters recorded under team 2 are written with team 2 in ctx (also after a
// failed flush requeues them); counters recorded with no team flush with no team.
func TestFlushKeepsTeam(t *testing.T) {
	files := &fakeFiles{rows: map[int]*entity.LibraryFileLinkTarget{7: linkTarget(7, 3)}}
	svc := newTestService(t, files, &fakePresign{})
	tok := map[bool]string{true: svc.minter.MintFor(2, patterntoken.ScopeFile, 7, 3), false: svc.minter.Mint(patterntoken.ScopeFile, 7, 3)}

	for _, ctx := range []context.Context{teamCtx(2), teamCtx(2), context.Background()} {
		_, inTeam := tenant.From(ctx)
		if w := serveFileCtx(svc, ctx, "/api/f/"+tok[inTeam]); w.Code != http.StatusFound {
			t.Fatalf("serve: %d", w.Code)
		}
	}
	files.recordErr = errors.New("db down")
	svc.flushStats()
	files.recordErr = nil
	svc.flushStats()

	var team2, plain int
	for _, f := range files.flushes {
		if f.failed {
			continue
		}
		switch {
		case f.hasTeam && f.teamID == 2 && f.counts[7] == 2:
			team2++
		case !f.hasTeam && f.counts[7] == 1:
			plain++
		default:
			t.Errorf("unexpected flush %+v", f)
		}
	}
	if team2 != 1 || plain != 1 {
		t.Fatalf("flushes %+v: want one team-2 write of 2 and one plain write of 1", files.flushes)
	}
}
