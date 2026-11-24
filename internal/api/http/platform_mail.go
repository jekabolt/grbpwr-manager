package httpapi

import "net/http"

// SetSignupChallengeHandler mounts GET /api/auth/signup-challenge (teams mode, X6): the sign-up
// form's invisible proof-of-work challenge. Public, behind CORS like the other /api/auth routes.
func (s *Server) SetSignupChallengeHandler(h http.Handler) {
	s.signupChallengeHandler = h
}

// SetPlatformMailHandler mounts POST /api/webhooks/platform-mail (teams mode, X6): Resend's delivery
// events for the platform letters. Mounted in shop and no-shop mode alike; the handler verifies the
// Svix signature itself and is only built when its secret is set.
func (s *Server) SetPlatformMailHandler(h http.Handler) {
	s.platformMailHandler = h
}

// SetShopMailHandler mounts POST /api/webhooks/shop-mail (X18-L7): Resend's delivery events of the
// team shops' letters when they go over their own Resend account (PLATFORM_SHOP_RESEND_API_KEY).
// Independent of the storefront's webhooks (mounted under none too); Svix-signed like platform-mail.
func (s *Server) SetShopMailHandler(h http.Handler) {
	s.shopMailHandler = h
}
