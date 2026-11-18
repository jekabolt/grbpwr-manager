package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
)

func serveStatusz(srv *Server, token, origin string) (int, string, bool) {
	reached := false
	h := srv.WithStatuszAuthz(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true }))
	req := httptest.NewRequest(http.MethodGet, "/statusz", nil)
	if token != "" {
		req.Header.Set(AuthMetadataKey, "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), reached
}

// Review item 7, mode OFF: exactly today's WithAuth answers (200 for an admin token, 401 with the
// same body otherwise).
func TestStatuszModeOffUnchanged(t *testing.T) {
	srv := newEdgeServer(t, nil)
	for _, c := range []struct {
		name, tok string
		want      int
	}{
		{"admin token", legacyToken(t, srv), http.StatusOK},
		{"pre-RBAC token", preRBACToken(t, srv), http.StatusOK},
		{"no token", "", http.StatusUnauthorized},
		{"garbage", "x.y.z", http.StatusUnauthorized},
	} {
		code, body, reached := serveStatusz(srv, c.tok, "")
		if code != c.want || reached != (c.want == http.StatusOK) {
			t.Errorf("%s: %d reached=%v, want %d", c.name, code, reached, c.want)
		}
		if c.want == http.StatusUnauthorized && body != "{\"error\":\"unauthorized\"}\n" {
			t.Errorf("%s: body %q changed", c.name, body)
		}
	}
}

// Review item 7, teams mode: /statusz is process-wide state, so it runs the admin chain (origin,
// live session, account) read-only and only team #1 sees it.
func TestStatuszTeamsModeAdminChainTeamOneOnly(t *testing.T) {
	e := newEdge()
	srv := newEdgeServer(t, e)
	if code, _, _ := serveStatusz(srv, legacyToken(t, srv), legacyOrigin); code != http.StatusOK {
		t.Fatalf("team #1 admin: %d", code)
	}
	acme := teamToken(t, srv, authjwt.TeamClaims{TeamID: 7, Audience: authjwt.AudTeam, Super: true})
	if code, _, reached := serveStatusz(srv, acme, acmeOrigin); code != http.StatusForbidden || reached {
		t.Fatalf("another team's owner: %d reached=%v, want 403", code, reached)
	}
	if code, _, _ := serveStatusz(srv, legacyToken(t, srv), "https://evil.example"); code != http.StatusUnauthorized {
		t.Fatalf("foreign origin: %d, want 401", code)
	}
	one := teamToken(t, srv, authjwt.TeamClaims{TeamID: 1, Audience: authjwt.AudTeam, Super: true})
	if code, _, _ := serveStatusz(srv, one, "https://grbpwr.lekalo.studio"); code != http.StatusOK {
		t.Fatalf("team #1 member token: %d", code)
	}
	e.removed[5] = true
	if code, _, _ := serveStatusz(srv, one, "https://grbpwr.lekalo.studio"); code != http.StatusUnauthorized {
		t.Fatalf("removed team #1 member: %d, want 401", code)
	}
}
