package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/shop"
)

// The team shops' origins are admitted on the storefront API only (X18); the admin and auth APIs keep
// the base rules.
func TestShopCORSOnlyOnFrontend(t *testing.T) {
	origins := shop.NewOrigins("shop.lekalo.studio", func(context.Context) ([]string, error) {
		return []string{"acme.com"}, nil
	})
	base, err := buildCORS([]string{"https://grbpwr.com"}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	front, err := buildCORSWith([]string{"https://grbpwr.com"}, false, "", origins.Allowed)
	if err != nil {
		t.Fatal(err)
	}
	h := corsByPath(base, front)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	allowed := func(path, origin string) bool {
		r := httptest.NewRequest(http.MethodOptions, path, nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "content-type,x-shop-host")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Header().Get("Access-Control-Allow-Origin") == origin
	}
	cases := []struct {
		path, origin string
		want         bool
	}{
		{"/api/frontend/hero", "https://grbpwr.com", true},
		{"/api/frontend/hero", "https://acme.shop.lekalo.studio", true},
		{"/api/frontend/hero", "https://acme.com", true},
		{"/api/frontend/hero", "http://acme.com", false},
		{"/api/frontend/hero", "https://a.b.shop.lekalo.studio", false},
		{"/api/frontend/hero", "https://evil.com", false},
		{"/api/admin/products", "https://acme.shop.lekalo.studio", false},
		{"/api/auth/login", "https://acme.com", false},
		{"/api/admin/products", "https://grbpwr.com", true},
	}
	for _, c := range cases {
		if got := allowed(c.path, c.origin); got != c.want {
			t.Errorf("%s from %s: allowed %v, want %v", c.path, c.origin, got, c.want)
		}
	}
}

// The shared storefront names its shop in X-Shop-Host (plain or as gRPC metadata): a shop origin's
// preflight on the storefront API admits both (X18 v3).
func TestShopHostHeaderAllowedOnFrontend(t *testing.T) {
	origins := shop.NewOrigins("shop.lekalo.studio", func(context.Context) ([]string, error) { return []string{"acme.com"}, nil })
	base, err := buildCORS([]string{"https://grbpwr.com"}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	front, err := buildCORSWith([]string{"https://grbpwr.com"}, false, "", origins.Allowed)
	if err != nil {
		t.Fatal(err)
	}
	h := corsByPath(base, front)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, origin := range []string{"https://grbpwr.com", "https://acme.com"} {
		for _, hdr := range []string{"x-shop-host", "grpc-metadata-x-shop-host"} {
			r := httptest.NewRequest(http.MethodOptions, "/api/frontend/shop/profile", nil)
			r.Header.Set("Origin", origin)
			r.Header.Set("Access-Control-Request-Method", "GET")
			r.Header.Set("Access-Control-Request-Headers", hdr)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Header().Get("Access-Control-Allow-Origin") != origin ||
				!strings.Contains(strings.ToLower(w.Header().Get("Access-Control-Allow-Headers")), hdr) {
				t.Errorf("%s from %s: allow-origin %q allow-headers %q", hdr, origin,
					w.Header().Get("Access-Control-Allow-Origin"), w.Header().Get("Access-Control-Allow-Headers"))
			}
		}
	}
}
