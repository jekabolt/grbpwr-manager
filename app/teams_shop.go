package app

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/teamlogo"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

// wireShops turns on the shops of teams (X18, teams mode): the shop screen, the console's shop
// column, the storefront profile and per-shop customer keys, CORS for shop origins, and per-shop
// revalidation. The storefront team (grbpwr) keeps today's storefront untouched. Custom domains go
// through a fake provider until lane L5 brings the Vercel one.
func (a *App) wireShops(ctx context.Context, b *tenantbind.Binder, adminS *admin.Server, frontendS *frontend.Server) {
	if a.platform == nil {
		return
	}
	cfg := a.c.Platform
	st := a.platform
	sf := cfg.StorefrontTeamID
	if sf <= 0 && !cfg.NoShop() {
		sf = tenant.TeamOneID
	}
	svc := &shop.Service{
		St:           st,
		StorefrontID: sf,
		LogoURL: func(ctx context.Context, teamID int64, slug string) string {
			sha, err := st.TeamLogoSHA(ctx, teamID)
			if err != nil {
				slog.Default().WarnContext(ctx, "shop: team logo", slog.String("err", err.Error()))
				return ""
			}
			return teamlogo.URL(cfg.APIBase(), slug, sha)
		},
	}
	origins := shop.NewOrigins(cfg.ShopZoneHost(), st.OpenShopHosts)
	invalidate := func(teamID int64) {
		b.InvalidateShop(teamID)
		origins.Invalidate()
	}
	a.hs.SetShopOrigins(origins.Allowed)
	a.hs.SetShopInvalidator(invalidate)

	re := &shop.Revalidator{Base: a.re, Target: svc.RevalidateTarget, StorefrontID: sf}
	if hr, ok := a.re.(shop.HostRevalidator); ok {
		re.Shop = hr
	}
	adminS.SetShops(admin.ShopWiring{Domains: &shop.FakeDomains{}, Invalidate: invalidate, Revalidator: re})
	if frontendS != nil {
		frontendS.SetShops(frontend.ShopDeps{
			StorefrontID: sf,
			Master:       a.c.StorefrontAuth.ShopMaster,
			Service:      svc,
			Open:         b.ShopOpen,
			Revalidator:  re,
		})
	}
	if a.c.StorefrontAuth.ShopMaster == "" {
		slog.Default().WarnContext(ctx, "shops: STOREFRONT_AUTH_SHOP_MASTER is empty; team shop customers cannot sign in")
	}
	if cfg.ShopZoneHost() == "" {
		slog.Default().WarnContext(ctx, "shops: no shop zone (PLATFORM_SHOP_ZONE / base domain); shops cannot be turned on")
	}
}
