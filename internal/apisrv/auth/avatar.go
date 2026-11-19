package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/avatar"
)

// AvatarHandler is the public avatar route (GET / HEAD /api/auth/avatar/{sha256}, P0 §3.2) over the
// control plane; it shares the logo route's per-IP limit. With teams mode off (no SetPlatform) every
// request is the 404.
func (s *Server) AvatarHandler() http.Handler {
	st := s.platform.store
	h := avatar.Handler(func(ctx context.Context, sha string) (int64, time.Time, bool, error) {
		if st == nil {
			return 0, time.Time{}, false, nil
		}
		m, ok, err := st.AvatarMetaBySHA(ctx, sha)
		return m.Size, m.UpdatedAt, ok, err
	}, func(ctx context.Context, sha string) ([]byte, bool, error) {
		if st == nil {
			return nil, false, nil
		}
		return st.AvatarPNGBySHA(ctx, sha)
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
