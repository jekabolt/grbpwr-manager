package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/teamlogo"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

// wireShops turns on the shops of teams (X18, teams mode): the shop screen, the console's shop
// column, the storefront profile and per-shop customer keys, CORS for shop origins, and per-shop
// revalidation. The storefront team (grbpwr) keeps today's storefront untouched. Custom domains go
// through a fake provider until lane L5 brings the Vercel one.
func (a *App) wireShops(ctx context.Context, b *tenantbind.Binder, adminS *admin.Server, frontendS *frontend.Server) error {
	if m := strings.TrimSpace(a.c.Revalidation.ShopMaster); m != "" && m == strings.TrimSpace(a.c.StorefrontAuth.ShopMaster) {
		// One master would let a revalidation secret of a shop be derived next to its customer keys.
		return errors.New("shops: SHOP_REVALIDATE_MASTER must differ from STOREFRONT_AUTH_SHOP_MASTER")
	}
	if a.platform == nil {
		return nil
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
	// A custom host goes live only with its team's DNS proof (TXT under _lekalo.<host>); without a
	// key none does.
	if proof, err := shop.NewTXTProof(cfg.TokenPepper); err != nil {
		slog.Default().ErrorContext(ctx, "shops: no host proof key (PLATFORM_TOKEN_PEPPER); custom domains cannot go live")
	} else {
		st.SetShopHostProof(proof)
	}
	own := shopOwnHosts([]string{cfg.BaseDomain, cfg.ShopZoneHost()}, a.c.HTTP.AllowedOrigins, cfg.LegacyOrigins())
	if sf > 0 {
		taken, err := st.EnsureLegacyShopHosts(ctx, sf, own, time.Now())
		if err != nil {
			return fmt.Errorf("shops: legacy hosts: %w", err)
		}
		for _, h := range taken {
			slog.Default().ErrorContext(ctx, "shops: a legacy host was attached by another team; taken back", slog.String("host", h))
		}
	}
	domains := shop.DomainProvider(&shop.FakeDomains{})
	adminS.SetShops(admin.ShopWiring{Domains: domains, Invalidate: invalidate, Revalidator: re,
		Reserved: append(append(own, "grbpwr.com", "lekalo.studio"), shop.RegistrableDomains(own...)...)})
	go drainShopDomains(ctx, st, domains)
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
	return nil
}

// shopDomainDrainEvery is how often queued provider removals of let-go custom domains are retried.
const shopDomainDrainEvery = time.Minute

// drainShopDomains removes let-go custom domains at the provider until ctx ends (every instance; the
// rows are locked one by one, so instances never remove one domain twice at once).
func drainShopDomains(ctx context.Context, st *platformstore.Store, dp shop.DomainProvider) {
	defer saferun.Recover(ctx, "shop-domain-drain")
	t := time.NewTicker(shopDomainDrainEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n, err := st.DrainShopDomainDrops(ctx, time.Now(), dp.Remove)
		if err != nil {
			slog.Default().WarnContext(ctx, "shops: domain removal at the provider; retried later", slog.String("err", err.Error()))
		}
		if n > 0 {
			slog.Default().InfoContext(ctx, "shops: domains removed at the provider", slog.Int("count", n))
		}
	}
}

// shopOwnHosts are the storefront team's own hosts (HTTP_ALLOWED_ORIGINS and the legacy admin
// origins): legacy shop hosts of the storefront team, never a team's custom host. Dev and preview
// hosts (localhost, IPs, vercel) and hosts under the platform's domains (base, shop zone: the console
// and team platform addresses, reserved anyway) are left out.
func shopOwnHosts(platform []string, origins ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range origins {
		for _, o := range list {
			h := shop.OriginHost(o)
			if h == "" || seen[h] || shop.ValidCustomHost(h, platform...) != nil {
				continue
			}
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}
