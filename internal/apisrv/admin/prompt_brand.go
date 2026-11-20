package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// promptBrand is whose voice a translator prompt speaks in. House: team #1 (or teams mode off) —
// the prompts keep their grbpwr wording byte for byte, so its translations do not shift. Otherwise
// Name is the brand name from the team's workspace settings; empty means a neutral prompt that names
// no brand at all.
type promptBrand struct {
	House bool
	Name  string
}

// translateBrand reads the calling team's brand for the AI translators. Teams mode off, no team in
// ctx (single install) or team #1 → House. A read failure is logged and answered neutral: a foreign
// team must never get grbpwr's name or voice by fallback.
func (s *Server) translateBrand(ctx context.Context) promptBrand {
	if !s.teamsOn() {
		return promptBrand{House: true}
	}
	t, ok := tenant.From(ctx)
	if !ok || t.ID == tenant.TeamOneID {
		return promptBrand{House: true}
	}
	b, found, err := s.platform.store.TeamBrand(ctx, t.ID)
	if err != nil {
		slog.Default().WarnContext(ctx, "translate: team brand read failed; neutral prompt",
			slog.String("team", t.Slug), slog.String("err", err.Error()))
		return promptBrand{}
	}
	if !found {
		return promptBrand{}
	}
	return promptBrand{Name: b.BrandName}
}
