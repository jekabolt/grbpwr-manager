package app

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/shopdomains"
)

// shopDomainProvider is the Vercel provider when VERCEL_TOKEN and VERCEL_PROJECT_ID are set, else
// the fake one (dev, tests, beta without a token): nothing outside the process changes.
func (a *App) shopDomainProvider(ctx context.Context) shop.DomainProvider {
	c := a.c.ShopDomains
	if !c.Enabled() {
		slog.Default().InfoContext(ctx, "shops: VERCEL_TOKEN / VERCEL_PROJECT_ID unset; custom domains use the fake provider")
		return &shop.FakeDomains{}
	}
	slog.Default().InfoContext(ctx, "shops: custom domains on Vercel", slog.String("project", c.ProjectID),
		slog.Bool("team", c.TeamID != ""))
	return shop.NewVercel(c, "", nil)
}

// startShopDomains starts the worker that re-checks pending custom domains (X18-L5), only with the
// real provider: the fake one never turns a domain live. Each team's tick takes a shared background
// slot (X15).
func (a *App) startShopDomains(ctx context.Context, dp shop.DomainProvider, invalidate func(int64), re dependency.RevalidationService) {
	if _, real := dp.(*shop.Vercel); !real || a.platform == nil {
		return
	}
	changed := func(teamCtx context.Context, teamID int64) {
		invalidate(teamID)
		if re == nil {
			return
		}
		if err := re.RevalidateAll(teamCtx, &dto.RevalidationData{Profile: true}); err != nil {
			slog.Default().WarnContext(teamCtx, "shopdomains: revalidate", slog.String("err", err.Error()))
		}
	}
	w := shopdomains.New(a.platform, dp, a.workerTeamLoop("shopdomains", activeTeams), changed)
	if err := w.Start(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "couldn't start shop domains worker", slog.String("err", err.Error()))
		return
	}
	a.sdw = w
}
