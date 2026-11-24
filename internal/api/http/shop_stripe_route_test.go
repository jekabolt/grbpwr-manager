package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

func shopStripeRouter(t *testing.T, noShop bool) http.Handler {
	t.Helper()
	s := New(&Config{Address: "127.0.0.1", Port: "1"})
	s.SetStripeWebhookHandler(nopWebhooks{})
	s.SetHealthRegistry(&health.Registry{})
	s.SetNoShop(noShop)
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
