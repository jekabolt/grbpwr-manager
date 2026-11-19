package auth

import (
	"context"
	"net/http"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth/teamlogo"
)

// TeamLogoHandler is the public logo route (GET / HEAD /api/auth/team-logo/{slug}, teams O §11,
// §15) over the control plane. With teams mode off (no SetPlatform) every request is the 404.
func (s *Server) TeamLogoHandler() http.Handler {
	st := s.platform.store
	return teamlogo.Handler(func(ctx context.Context, slug string) ([]byte, string, time.Time, bool, error) {
		if st == nil {
			return nil, "", time.Time{}, false, nil
		}
		l, updated, ok, err := st.TeamLogoBySlug(ctx, slug)
		return l.PNG, l.SHA256, updated, ok, err
	})
}
