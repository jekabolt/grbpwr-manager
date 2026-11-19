package fileaccess

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// TestLinkURLContextRefusedOutsideTeamOne — a v1 'f' token has no team and resolves in team #1, so
// a link minted in team 2 would open team #1's file with the same local id.
func TestLinkURLContextRefusedOutsideTeamOne(t *testing.T) {
	if err := PublicLinksAllowed(teamCtx(2)); status.Code(err) != codes.FailedPrecondition ||
		status.Convert(err).Message() != "public_links_team_one_only" {
		t.Fatalf("team 2: want FailedPrecondition public_links_team_one_only, got %v", err)
	}
	svc := newTestService(t, &fakeFiles{}, &fakePresign{})
	want := svc.LinkURL(7, 1)
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"no team", context.Background(), want},
		{"team one", teamCtx(tenant.TeamOneID), want},
		{"team two", teamCtx(2), ""},
	} {
		if got := svc.LinkURLContext(tc.ctx, 7, 1); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
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
	tok := svc.minter.Mint(patterntoken.ScopeFile, 7, 3)

	for _, ctx := range []context.Context{teamCtx(2), teamCtx(2), context.Background()} {
		if w := serveFileCtx(svc, ctx, "/api/f/"+tok); w.Code != http.StatusFound {
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
