package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauthflow"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

// The binder handed to SetEdge carries every origin rule the provider routes read through it (the
// auth server reaches RootOriginAllowed and CanonicalOrigin by assertion; losing one would refuse
// every root start or collapse the start limiter to one bucket per IP).
var _ oauthflow.Origins = (*tenantbind.Binder)(nil)

// The provider routes are a mount of their own next to the AuthService gateway: every method and
// path under /api/auth/oauth is the provider handler's answer (its 405 included), never the
// gateway's, and Apple's cross-site form_post reaches it through the CORS middleware.
func TestOAuthMountBeatsAuthMount(t *testing.T) {
	s := New(&Config{Address: "127.0.0.1", Port: "8081"})
	var got []string
	s.SetOAuthHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := s.setupHTTPAPI(ctx, &auth.Server{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ method, path, origin string }{
		{http.MethodPost, "/api/auth/oauth/google/start", "https://beta.lekalo.studio"},
		{http.MethodGet, "/api/auth/oauth/google/callback?state=s&code=c", ""},
		{http.MethodPost, "/api/auth/oauth/apple/callback", "https://appleid.apple.com"},
		{http.MethodGet, "/api/auth/oauth/apple/callback", ""},
		{http.MethodPost, "/api/auth/oauth/x/y/z", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader("state=s&code=c"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTeapot {
			t.Errorf("%s %s = %d, want the provider handler", c.method, c.path, rec.Code)
		}
	}
	if len(got) != len(cases) {
		t.Errorf("provider handler saw %d requests, want %d: %v", len(got), len(cases), got)
	}
}

// Teams mode off (no handler set): the routes do not exist.
func TestOAuthRoutesAbsentWithoutHandler(t *testing.T) {
	s := New(&Config{Address: "127.0.0.1", Port: "8081"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := s.setupHTTPAPI(ctx, &auth.Server{})
	if err != nil {
		t.Fatal(err)
	}
	router, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("setupHTTPAPI returned %T, not a chi router", h)
	}
	walked := 0
	err = chi.Walk(router, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		walked++
		if strings.HasPrefix(route, "/api/auth/oauth") {
			t.Errorf("route %s exists without a provider handler", route)
		}
		return nil
	})
	if err != nil || walked == 0 {
		t.Fatalf("walk: %v (%d routes)", err, walked)
	}
}
