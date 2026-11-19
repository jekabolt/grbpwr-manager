package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// SetPlatform builds the provider registry from OAUTH_*: a half-set provider is the boot's error
// (named, no value) and no routes; nothing set gives routes that answer only 404.
func TestSetPlatformOAuth(t *testing.T) {
	half := &Server{}
	half.SetPlatform(&platformstore.Store{}, platformstore.Config{
		BaseDomain: "beta.lekalo.studio",
		OAuth:      platformstore.OAuthConfig{GoogleClientID: "gid-value-7"},
	})
	defer half.closePlatformService()
	err := half.OAuthConfigErr()
	if err == nil || !strings.Contains(err.Error(), "OAUTH_GOOGLE_CLIENT_SECRET") || strings.Contains(err.Error(), "gid-value-7") {
		t.Fatalf("half-set Google: %v", err)
	}
	if half.OAuthHandler() != nil {
		t.Fatal("a refused configuration still has provider routes")
	}

	none := &Server{}
	none.SetPlatform(&platformstore.Store{}, platformstore.Config{BaseDomain: "beta.lekalo.studio"})
	defer none.closePlatformService()
	if err := none.OAuthConfigErr(); err != nil {
		t.Fatalf("nothing set refused: %v", err)
	}
	h := none.OAuthHandler()
	if h == nil {
		t.Fatal("teams mode without providers has no provider handler")
	}
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/google/start", strings.NewReader("target=signin")),
		httptest.NewRequest(http.MethodGet, "/google/callback?state=s&code=c", nil),
		httptest.NewRequest(http.MethodPost, "/apple/callback", strings.NewReader("state=s&code=c")),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", req.Method, req.URL.Path, rec.Code)
		}
	}

	off := &Server{}
	if off.OAuthHandler() != nil || off.OAuthConfigErr() != nil {
		t.Fatal("teams mode off has provider routes or an error")
	}
}

type rootEdge struct{ Edge }

func (rootEdge) RootOriginAllowed(o string) bool { return o == "https://beta.lekalo.studio" }
func (rootEdge) CanonicalOrigin(o string) string { return "canon:" + o }
func (rootEdge) OriginAllowed(o string, t tenant.Team, _ bool) bool {
	return o == "https://"+t.Slug+".beta.lekalo.studio"
}

type teamEdge struct{ Edge }

func (teamEdge) OriginAllowed(string, tenant.Team, bool) bool { return true }

// The origin rules are the edge's, read when a start arrives; an edge without the root rule
// refuses every root start, and an empty origin is refused even where the edge would pass it.
func TestEdgeOrigins(t *testing.T) {
	s := &Server{}
	o := edgeOrigins{s: s}
	acme := tenant.Team{ID: 7, Slug: "acme"}
	if o.RootOriginAllowed("https://beta.lekalo.studio") || o.OriginAllowed("https://acme.beta.lekalo.studio", acme, false) {
		t.Fatal("no edge allowed an origin")
	}
	s.SetEdge(rootEdge{})
	if !o.RootOriginAllowed("https://beta.lekalo.studio") || o.RootOriginAllowed("https://acme.beta.lekalo.studio") {
		t.Fatal("root rule not the edge's")
	}
	if !o.OriginAllowed("https://acme.beta.lekalo.studio", acme, false) || o.OriginAllowed("https://beta.lekalo.studio", acme, false) {
		t.Fatal("team rule not the edge's")
	}
	if got := o.CanonicalOrigin("HTTPS://Beta.Lekalo.Studio"); got != "canon:HTTPS://Beta.Lekalo.Studio" {
		t.Fatalf("canonical origin %q is not the edge's", got)
	}
	s.SetEdge(teamEdge{})
	if o.RootOriginAllowed("https://beta.lekalo.studio") {
		t.Fatal("an edge without the root rule allowed a root start")
	}
	if got := o.CanonicalOrigin("https://Evil.example"); got != "" {
		t.Fatalf("an edge without the canonical form keyed on %q", got)
	}
	if o.OriginAllowed("", acme, false) || o.OriginAllowed("  ", acme, false) {
		t.Fatal("an empty origin passed")
	}
}
