package tenantbind

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/patterntoken"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// serveToken runs one request through the edge and reports the status and the team the handler saw
// (0 = the handler did not run).
func serveTokenReq(t *testing.T, b *Binder, path, host string) (int, int64) {
	t.Helper()
	var seen int64
	h := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tm, ok := tenant.From(r.Context()); ok {
			seen = tm.ID
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if host != "" {
		req.Host = host
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, seen
}

func TestTokenBindsItsOwnTeam(t *testing.T) {
	b, p := teamsBinder(t)
	m, err := patterntoken.NewMinter("test-pepper")
	if err != nil {
		t.Fatal(err)
	}
	paused := tenant.Team{ID: 8, Slug: "paused", DBName: "lkb_t_p", Status: tenant.StatusSuspended}
	gone := tenant.Team{ID: 9, Slug: "gone", DBName: "lkb_t_g", Status: tenant.StatusDeleted}
	review := tenant.Team{ID: 10, Slug: "review", DBName: "lkb_t_r", Status: tenant.StatusReview}
	p.dir.teams[8], p.dir.teams[9], p.dir.teams[10] = paused, gone, review

	// Before the verifier is installed every link is team #1 (pre-v2 behaviour).
	if code, seen := serveTokenReq(t, b, "/api/f/"+m.MintFor(7, patterntoken.ScopeFile, 1, 1), ""); code != 200 || seen != tenant.TeamOneID {
		t.Fatalf("no verifier: %d team %d", code, seen)
	}
	b.UseTokenTeams(m.TokenTeam)

	v1 := m.Mint(patterntoken.ScopePrint, 5, 0)
	acmeTok := m.MintFor(7, patterntoken.ScopePrint, 5, 0)
	cases := []struct {
		name, path, host string
		code             int
		team             int64
	}{
		{"v1 stays team #1 forever", "/api/p/" + v1, "", 200, tenant.TeamOneID},
		{"v1 on a team host is still team #1", "/api/p/" + v1, "acme.beta.lekalo.studio", 200, tenant.TeamOneID},
		{"v2 binds its team", "/api/p/" + acmeTok, "", 200, 7},
		{"v2 replayed on another team's host binds the token's team", "/api/pv/" + m.MintFor(7, patterntoken.ScopeCard, 3, 1), "paused.beta.lekalo.studio", 200, 7},
		{"run pack", "/api/rp/" + m.MintFor(7, patterntoken.ScopeRunPack, 3, 1), "", 200, 7},
		{"paused team still opens", "/api/f/" + m.MintFor(8, patterntoken.ScopeFile, 3, 1), "", 200, 8},
		{"deleted team refused", "/api/f/" + m.MintFor(9, patterntoken.ScopeFile, 3, 1), "", 404, 0},
		{"team in review refused", "/api/f/" + m.MintFor(10, patterntoken.ScopeFile, 3, 1), "", 404, 0},
		{"unknown team refused like deleted", "/api/f/" + m.MintFor(99, patterntoken.ScopeFile, 3, 1), "", 404, 0},
		// A forged team breaks the signature: bound to team #1, whose handler 404s a v2 token.
		{"forged team", "/api/p/i8." + acmeTok[3:], "", 200, tenant.TeamOneID},
		{"garbage", "/api/p/zzz", "", 200, tenant.TeamOneID},
		{"extra segment", "/api/p/" + acmeTok + "/x", "", 200, tenant.TeamOneID},
	}
	for _, c := range cases {
		code, seen := serveTokenReq(t, b, c.path, c.host)
		if code != c.code || seen != c.team {
			t.Errorf("%s: %d team %d, want %d team %d", c.name, code, seen, c.code, c.team)
		}
	}

	// Restored team: links work again (the directory answers active).
	gone.Status = tenant.StatusActive
	p.dir.teams[9] = gone
	if code, seen := serveTokenReq(t, b, "/api/f/"+m.MintFor(9, patterntoken.ScopeFile, 3, 1), ""); code != 200 || seen != 9 {
		t.Fatalf("restored team: %d team %d", code, seen)
	}

	// Database behind the binary: 503, the team's own calls answer the same.
	b.UseSchemaCheck(func(tm tenant.Team) bool { return tm.ID == 7 })
	if code, _ := serveTokenReq(t, b, "/api/p/"+acmeTok, ""); code != http.StatusServiceUnavailable {
		t.Fatalf("schema behind: %d", code)
	}
	b.UseSchemaCheck(nil)
}

func TestTokenTeamsModeOffStaysTeamOne(t *testing.T) {
	b := newBinder(t)
	m, _ := patterntoken.NewMinter("test-pepper")
	b.UseTokenTeams(m.TokenTeam)
	if code, seen := serveTokenReq(t, b, "/api/p/"+m.MintFor(7, patterntoken.ScopePrint, 5, 0), ""); code != 200 || seen != tenant.TeamOneID {
		t.Fatalf("teams mode off: %d team %d", code, seen)
	}
}

type downDir struct{ mapDir }

func (*downDir) ByID(context.Context, int64) (tenant.Team, error) {
	return tenant.Team{}, errors.New("down")
}

type downPlane struct {
	*fakePlane
	d *downDir
}

func (p downPlane) Directory() tenant.Directory { return p.d }

func TestTokenControlPlaneDown(t *testing.T) {
	b, p := teamsBinder(t)
	m, _ := patterntoken.NewMinter("test-pepper")
	b.usePlane(downPlane{p, &downDir{}}, platformstore.Config{BaseDomain: "beta.lekalo.studio"})
	b.UseTokenTeams(m.TokenTeam)
	if code, _ := serveTokenReq(t, b, "/api/p/"+m.MintFor(7, patterntoken.ScopePrint, 5, 0), ""); code != http.StatusServiceUnavailable {
		t.Fatalf("control plane down: %d", code)
	}
	// v1 needs no lookup: team #1 is the boot team.
	if code, seen := serveTokenReq(t, b, "/api/p/"+m.Mint(patterntoken.ScopePrint, 5, 0), ""); code != 200 || seen != tenant.TeamOneID {
		t.Fatalf("v1 with control plane down: %d team %d", code, seen)
	}
}

func TestTokenOfPath(t *testing.T) {
	for in, want := range map[string]string{
		"/api/p/abc": "abc", "/api/pv/a.b.c": "a.b.c", "/api/p/": "", "/api/p": "", "/api/p/a/b": "", "/x/p/a": "",
	} {
		if got := tokenOfPath(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
