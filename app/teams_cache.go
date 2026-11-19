package app

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// dictionaryPollTeams is the teams the dictionary-revision poller serves besides team #1 (which the
// root ctx carries): the storefront team when it is a team other than #1. Every other team's cache is
// loaded on first use and checked against its own database by its own reads and writes. Mode off, or
// TENANT_STOREFRONT_TEAM_ID=none, or the storefront is team #1: none.
func (a *App) dictionaryPollTeams(ctx context.Context) []tenant.Team {
	if a.platform == nil || a.c.Platform.NoShop() {
		return nil
	}
	sf := a.c.Platform.StorefrontTeamID
	if sf <= 0 || sf == tenant.TeamOneID {
		return nil
	}
	t, err := a.platform.Directory().ByID(ctx, sf)
	if err != nil {
		slog.Default().ErrorContext(ctx, "dictionary poller: can't resolve the storefront team; its cache refreshes only on its own reads and writes",
			slog.Int64("team_id", sf), slog.String("err", err.Error()))
		return nil
	}
	return []tenant.Team{t}
}
