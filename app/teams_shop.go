package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/teamlogo"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

// wireShops turns on the shops of teams (X18, teams mode): the shop screen, the console's shop
// column, the storefront profile and per-shop customer keys, CORS for shop origins, and per-shop
// revalidation. The storefront team (grbpwr) keeps today's storefront untouched. Custom domains go
// to the Vercel project when VERCEL_TOKEN and VERCEL_PROJECT_ID are set, else to a fake provider.
func (a *App) wireShops(ctx context.Context, b *tenantbind.Binder, adminS *admin.Server, frontendS *frontend.Server) error {
	if m := strings.TrimSpace(a.c.Revalidation.ShopMaster); m != "" && m == strings.TrimSpace(a.c.StorefrontAuth.ShopMaster) {
		// One master would let a revalidation secret of a shop be derived next to its customer keys.
		return errors.New("shops: SHOP_REVALIDATE_MASTER must differ from STOREFRONT_AUTH_SHOP_MASTER")
	}
	cfg := a.c.Platform
	sf := a.storefrontTeamID()
	platform := []string{cfg.BaseDomain, cfg.ShopZoneHost()}
	adminHosts := shopNoPrimary(cfg.APIBase(), cfg.LegacyOrigins(), a.c.HTTP.AllowedOrigins)
	// The storefront team's shop hosts come from TENANT_STOREFRONT_HOSTS only, never from the allowed
	// origins: a CORS-only page is never its main address nor gets its revalidation secret. A bad
	// entry fails the boot.
	legacy, err := storefrontShopHosts(sf, cfg.LegacyStorefrontHosts(), platform, adminHosts)
	if err != nil {
		return err
	}
	if a.platform == nil {
		return nil
	}
	st := a.platform
	// Only the storefront team's configured hosts count as legacy rows; any other legacy row (a host
	// no longer listed, "none", a former storefront team) is ignored everywhere. Nothing is deleted.
	st.SetLegacyShopHosts(sf, legacy)
	if b != nil {
		b.SetStorefrontHosts(legacy)
	}
	svc := &shop.Service{
		St:              st,
		StorefrontID:    sf,
		AdminHosts:      adminHosts,
		StorefrontHosts: legacy,
		LogoURL: func(ctx context.Context, teamID int64, slug string) string {
			sha, err := st.TeamLogoSHA(ctx, teamID)
			if err != nil {
				slog.Default().WarnContext(ctx, "shop: team logo", slog.String("err", err.Error()))
				return ""
			}
			return teamlogo.URL(cfg.APIBase(), slug, sha)
		},
		StorefrontStripe: storefrontStripe(a.c.StripePayment, a.c.StripePaymentTest),
	}
	origins := shop.NewOrigins(cfg.ShopZoneHost(), st.OpenShopHosts)
	invalidate := func(teamID int64) {
		b.InvalidateShop(teamID)
		origins.Invalidate()
	}
	a.hs.SetShopOrigins(origins.Allowed)
	a.hs.SetShopInvalidator(invalidate)

	re := &shop.Revalidator{Base: a.re, Target: svc.RevalidateTarget, StorefrontID: sf}
	// Without SHOP_REVALIDATE_MASTER no shop is revalidated at its address, so the storefront team
	// revalidates only the old route (Base) even with a main address.
	if hr, ok := a.re.(shop.HostRevalidator); ok && strings.TrimSpace(a.c.Revalidation.ShopMaster) != "" {
		re.Shop = hr
	}
	// A custom host goes live only with its team's DNS proof (TXT under _lekalo.<host>); without a
	// key none does.
	if proof, err := shop.NewTXTProof(cfg.TokenPepper); err != nil {
		slog.Default().ErrorContext(ctx, "shops: no host proof key (PLATFORM_TOKEN_PEPPER); custom domains cannot go live")
	} else {
		st.SetShopHostProof(proof)
	}
	if sf > 0 {
		// Seed only: the boot inserts absent hosts and never updates or deletes a shop host.
		held, err := st.EnsureLegacyShopHosts(ctx, sf, legacy, time.Now())
		if err != nil {
			return fmt.Errorf("shops: legacy hosts: %w", err)
		}
		for _, h := range held {
			slog.Default().ErrorContext(ctx, "shops: a storefront host belongs to another team; left alone", slog.String("host", h))
		}
	}
	// Reserved for every team: the storefront's hosts and every allowed or legacy admin origin.
	own := append(shopReservedHosts(platform, a.c.HTTP.AllowedOrigins, cfg.LegacyOrigins()), legacy...)
	domains := a.shopDomainProvider(ctx)
	// Team payments run with or without a storefront team: only the storefront team's own (env) Stripe
	// is gone on a no-shop installation.
	pay := a.newShopPay(ctx, b, adminS, frontendS != nil) // X18-L3, teams_shop_pay.go
	adminS.SetShops(admin.ShopWiring{Domains: domains, Invalidate: invalidate, Revalidator: re,
		Reserved:        append(append(own, "grbpwr.com", "lekalo.studio"), shop.RegistrableDomains(own...)...),
		Payments:        shopPayments(pay),
		NoPrimary:       adminHosts,
		StorefrontHosts: legacy})
	go drainShopDomains(ctx, st, domains)
	a.startShopDomains(ctx, domains, invalidate, re)
	if frontendS != nil {
		frontendS.SetShops(frontend.ShopDeps{
			StorefrontID: sf,
			Master:       a.c.StorefrontAuth.ShopMaster,
			Service:      svc,
			Open:         b.ShopOpen,
			Revalidator:  re,
			Payments:     frontendShopPayments(pay),
		})
	}
	switch m := a.c.StorefrontAuth.ShopMaster; {
	case m == "":
		a.noteProblem(ctx, "shops: STOREFRONT_AUTH_SHOP_MASTER is empty; team shop customers cannot sign in")
	case len(m) < jwt.MinSymmetricSecretBytes:
		a.noteProblem(ctx, "shops: STOREFRONT_AUTH_SHOP_MASTER is shorter than 32 bytes; team shop customers cannot sign in")
	}
	if cfg.ShopZoneHost() == "" {
		slog.Default().WarnContext(ctx, "shops: no shop zone (PLATFORM_SHOP_ZONE / base domain); shops cannot be turned on")
	}
	return nil
}

// storefrontStripe answers the storefront team's publishable key from its env Stripe config: the
// processor its checkout runs on now (the team's payment-is-prod setting picks CARD = live config,
// else CARD_TEST = test config). The mode follows the key itself, so a test key in the live slot (beta)
// answers "test".
func storefrontStripe(live, test stripe.Config) func(context.Context) (string, string) {
	return func(ctx context.Context) (string, string) {
		if cache.GetPaymentIsProd(ctx) {
			return stripeKeyMode(live, "live")
		}
		return stripeKeyMode(test, "test")
	}
}

// stripeKeyMode is c's publishable key and its mode ("" when the processor is off or has no key).
func stripeKeyMode(c stripe.Config, slot string) (string, string) {
	pk := strings.TrimSpace(c.PubKey)
	if !c.Enabled || pk == "" {
		return "", ""
	}
	switch {
	case strings.HasPrefix(pk, "pk_live_"):
		return pk, "live"
	case strings.HasPrefix(pk, "pk_test_"):
		return pk, "test"
	}
	return pk, slot
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

// shopNoPrimary are the admin and API hosts among the storefront team's hosts: the legacy admin
// origins, the API host, and any allowed origin whose first label names an admin or API host
// (admin.grbpwr.com, backend.grbpwr.com). Never its main address (the shared storefront would send
// grbpwr's customers there), never listed on its shop screen or in its storefront profile.
func shopNoPrimary(api string, admin, allowed []string) []string {
	out := []string{}
	add := func(h string) {
		if h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	add(shop.OriginHost(api))
	for _, o := range admin {
		add(shop.OriginHost(o))
	}
	for _, o := range allowed {
		h := shop.OriginHost(o)
		first, _, _ := strings.Cut(h, ".")
		for _, p := range []string{"admin", "backend", "api"} {
			if strings.HasPrefix(first, p) {
				add(h)
			}
		}
	}
	return out
}

// storefrontShopHosts are the storefront team's shop hosts (TENANT_STOREFRONT_HOSTS entries, bare
// hosts or origins): its legacy shop hosts, the only ones that name it, may be its main address and
// receive its revalidation. nil entries ("none") = none. Every entry must be a valid custom host:
// an empty or malformed entry, an admin or API host, a dev or preview host or a host under the
// platform's domains is an error (the boot fails), never dropped. A repeated host counts once. None
// without a storefront team (the entries are still checked).
func storefrontShopHosts(sf int64, entries, platform, admin []string) ([]string, error) {
	if entries == nil {
		return nil, nil
	}
	var out []string
	for _, v := range entries {
		h := shop.OriginHost(v)
		if h == "" && !strings.Contains(v, "://") {
			h = shop.NormHost(v)
		}
		switch {
		case h == "" || !shop.ValidHost(h):
			return nil, fmt.Errorf("shops: TENANT_STOREFRONT_HOSTS: %q is not a host", v)
		case slices.Contains(admin, h):
			return nil, fmt.Errorf("shops: TENANT_STOREFRONT_HOSTS: %q is an admin or API host", v)
		case shop.ValidCustomHost(h, platform...) != nil:
			return nil, fmt.Errorf("shops: TENANT_STOREFRONT_HOSTS: %q is a dev, preview or platform host", v)
		case !slices.Contains(out, h):
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return nil, errors.New(`shops: TENANT_STOREFRONT_HOSTS names no host (use "none" for none)`)
	}
	if sf <= 0 {
		return nil, nil
	}
	return out, nil
}

// shopReservedHosts are the hosts of the allowed and legacy admin origins (HTTP_ALLOWED_ORIGINS,
// PLATFORM_LEGACY_ADMIN_ORIGINS): never a team's custom host. Not shop hosts of the storefront team
// (storefrontShopHosts). Dev and preview hosts (localhost, IPs, vercel) and hosts under the
// platform's domains (base, shop zone: the console and team platform addresses, reserved anyway)
// are left out.
func shopReservedHosts(platform []string, origins ...[]string) []string {
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
