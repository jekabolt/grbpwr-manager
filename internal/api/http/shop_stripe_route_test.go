package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"

	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

func shopStripeRouter(t *testing.T, noShop bool, teamShops ...bool) http.Handler {
	t.Helper()
	s := New(&Config{Address: "127.0.0.1", Port: "1"})
	s.SetStripeWebhookHandler(nopWebhooks{})
	s.SetHealthRegistry(&health.Registry{})
	s.SetNoShop(noShop)
	s.SetTeamShops(len(teamShops) > 0 && teamShops[0])
	answer := func(code int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
	}
	s.SetShopStripeHandlers(ShopStripeHandlers{Callback: answer(http.StatusSeeOther), Connect: answer(http.StatusAccepted), Team: answer(http.StatusTeapot)})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := s.setupHTTPAPI(ctx, newTestAuth(t))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestShopStripeRoutes: the Connect callback sits next to the AuthService gateway (not team-bound);
// the two team webhooks are shop routes beside today's Stripe webhook, gone on a no-shop installation.
func TestShopStripeRoutes(t *testing.T) {
	h := shopStripeRouter(t, false)
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/api/auth/stripe/callback?state=x&code=y", http.StatusSeeOther},
		{http.MethodPost, "/api/webhooks/stripe/connect", http.StatusAccepted},
		{http.MethodPost, "/api/webhooks/stripe/team/abc", http.StatusTeapot},
		{http.MethodPost, "/api/webhooks/stripe", http.StatusOK},
	} {
		if rec := serve(h, c.method, c.path, ""); rec.Code != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
	walked := map[string]bool{}
	_ = chi.Walk(h.(chi.Routes), func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		walked[route] = true
		return nil
	})
	for route, class := range map[string]tenantbind.Class{
		"/api/auth/stripe/callback":         tenantbind.ClassXTeam,
		"/api/webhooks/stripe/connect":      tenantbind.ClassStorefront,
		"/api/webhooks/stripe/team/{token}": tenantbind.ClassStorefront,
	} {
		if !walked[route] {
			t.Errorf("route %s not mounted", route)
		}
		if got := tenantbind.ClassifyPath(route); got != class {
			t.Errorf("%s: class %q, want %q", route, got, class)
		}
	}

	h = shopStripeRouter(t, true)
	for _, p := range []string{"/api/webhooks/stripe/connect", "/api/webhooks/stripe/team/abc"} {
		if rec := serve(h, http.MethodPost, p, ""); rec.Code != http.StatusServiceUnavailable || rec.Body.String() != noShopBody {
			t.Errorf("no shop %s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

// TestNoStorefrontTeamShops — no storefront team, team shops on: the storefront gateway is the real
// one and the team Stripe webhooks reach their handlers; the storefront team's own webhooks (env
// Stripe, AfterShip, Resend) keep the 503.
func TestNoStorefrontTeamShops(t *testing.T) {
	h := shopStripeRouter(t, true, true)
	for _, c := range []struct {
		path string
		want int
	}{
		{"/api/webhooks/stripe/connect", http.StatusAccepted},
		{"/api/webhooks/stripe/team/abc", http.StatusTeapot},
	} {
		if rec := serve(h, http.MethodPost, c.path, ""); rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.path, rec.Code, c.want)
		}
	}
	for _, p := range []string{"/api/webhooks/stripe", "/api/webhooks/aftership", "/api/webhooks/resend"} {
		if rec := serve(h, http.MethodPost, p, ""); rec.Code != http.StatusServiceUnavailable || rec.Body.String() != noShopBody {
			t.Errorf("%s: %d %q, want the no-shop 503", p, rec.Code, rec.Body.String())
		}
	}
	if rec := serve(h, http.MethodGet, "/api/frontend/hero", ""); rec.Body.String() == noShopBody {
		t.Fatal("team shops on: the frontend gateway answered the no-shop body")
	}
	s := New(&Config{})
	s.SetNoShop(true)
	s.SetTeamShops(true)
	called := false
	_ = s.noShopStreamInterceptor()(nil, nil, &grpc.StreamServerInfo{FullMethod: frontendMethodPrefix() + "GetHero"},
		func(any, grpc.ServerStream) error { called = true; return nil })
	if !called {
		t.Fatal("team shops on: a FrontendService call was stopped")
	}
	if _, ok := s.unknownServiceOption().(grpc.EmptyServerOption); !ok {
		t.Fatal("team shops on: the unknown-service handler is installed")
	}
}
