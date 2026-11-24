package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ShopStripeHandlers are the team shops' Stripe routes (X18-L3, shoppay.Service).
type ShopStripeHandlers struct {
	// Callback is Stripe Connect's OAuth redirect: GET /api/auth/stripe/callback.
	Callback http.Handler
	// Connect is the platform's Connect webhook: POST /api/webhooks/stripe/connect.
	Connect http.Handler
	// Team is a team's own-keys webhook: POST /api/webhooks/stripe/team/{token}.
	Team http.Handler
}

// SetShopStripeHandlers registers the team shops' Stripe routes (teams mode). The webhooks are shop
// routes (not mounted on a no-shop installation); the callback is a plain GET next to the
// AuthService gateway, like the team logo, and is not team-bound.
func (s *Server) SetShopStripeHandlers(h ShopStripeHandlers) {
	s.shopStripe = &h
}

func (s *Server) mountShopStripeCallback(r chi.Router) {
	if s.shopStripe != nil && s.shopStripe.Callback != nil {
		r.Method(http.MethodGet, "/auth/stripe/callback", s.shopStripe.Callback)
	}
}

func (s *Server) mountShopStripeWebhooks(r chi.Router) {
	if s.shopStripe == nil {
		return
	}
	if s.shopStripe.Connect != nil {
		r.Method(http.MethodPost, "/api/webhooks/stripe/connect", s.shopStripe.Connect)
	}
	if s.shopStripe.Team != nil {
		r.Method(http.MethodPost, "/api/webhooks/stripe/team/{token}", s.shopStripe.Team)
	}
}
