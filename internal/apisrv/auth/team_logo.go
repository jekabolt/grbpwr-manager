package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/teamlogo"
)

// TeamLogoHandler is the public logo route (GET / HEAD /api/auth/team-logo/{slug}, teams O §11,
// §15) over the control plane. With teams mode off (no SetPlatform) every request is the 404.
func (s *Server) TeamLogoHandler() http.Handler {
	st := s.platform.store
	h := teamlogo.Handler(func(ctx context.Context, slug string) (string, int64, time.Time, bool, error) {
		if st == nil {
			return "", 0, time.Time{}, false, nil
		}
		m, ok, err := st.TeamLogoMetaBySlug(ctx, slug)
		return m.SHA256, m.Size, m.UpdatedAt, ok, err
	}, func(ctx context.Context, slug, sha string) ([]byte, bool, error) {
		if st == nil {
			return nil, false, nil
		}
		return st.TeamLogoPNGBySlug(ctx, slug, sha)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.rateLimiter != nil && !s.rateLimiter.logoIP.Allow(middleware.GetClientIP(r.Context())) {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		h.ServeHTTP(w, r)
	})
}
