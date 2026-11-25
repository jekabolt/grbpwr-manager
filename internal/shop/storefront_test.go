package shop

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// memStore is a Store over maps.
type memStore struct {
	shops   map[int64]platformstore.TeamShop
	hosts   map[int64][]platformstore.ShopHost
	stripe  map[int64]platformstore.TeamStripe
	stripes int // TeamStripe calls
	err     error
}

func (m *memStore) TeamShop(_ context.Context, id int64) (platformstore.TeamShop, error) {
	return m.shops[id], m.err
}

func (m *memStore) ShopHosts(_ context.Context, id int64) ([]platformstore.ShopHost, error) {
	return m.hosts[id], m.err
}

func (m *memStore) TeamBrand(context.Context, int64) (platformstore.TeamBrand, bool, error) {
	return platformstore.TeamBrand{}, false, nil
}

func (m *memStore) TeamStripe(_ context.Context, id int64) (platformstore.TeamStripe, bool, error) {
	m.stripes++
	st, ok := m.stripe[id]
	return st, ok, nil
}

func legacy(host string, primary bool) platformstore.ShopHost {
	return platformstore.ShopHost{Host: host, TeamID: 1, Kind: platformstore.ShopHostLegacy, Status: platformstore.ShopHostLive, Primary: primary}
}

// The storefront team draws with its pack and its env key; it never reads team_stripe. Another
// team has no pack and its own Stripe setup; a www redirect is never one of its hosts.
func TestProfileStorefrontPackAndKey(t *testing.T) {
	ctx := context.Background()
	acct := "acct"
	st := &memStore{
		hosts: map[int64][]platformstore.ShopHost{
			1: {legacy("grbpwr.com", true), legacy("www.grbpwr.com", false)},
			7: {
				{Host: "brand.com", Kind: platformstore.ShopHostCustom, Status: platformstore.ShopHostLive, Primary: true},
				{Host: "www.brand.com", Kind: platformstore.ShopHostCustom, Status: platformstore.ShopHostLive, RedirectTo: "brand.com"},
			},
		},
		stripe: map[int64]platformstore.TeamStripe{
			1: {Kind: "keys", PublishableKey: "pk_team_row", Mode: "live", ChargesEnabled: true},
			7: {Kind: "connect", AccountID: &acct, Mode: "test", ChargesEnabled: true},
		},
	}
	svc := &Service{St: st, StorefrontID: 1, StorefrontStripe: func(context.Context) (string, string) { return "pk_env", "test" }}

	p, err := svc.Profile(ctx, tenant.Team{ID: 1, Name: "grbpwr"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Pack != StorefrontPack || p.StripePublishableKey != "pk_env" || p.StripeMode != "test" || p.StripeAccountID != "" {
		t.Fatalf("storefront profile: %+v", p)
	}
	if p.PrimaryHost != "grbpwr.com" || len(p.Hosts) != 2 || p.Hosts[0] != "grbpwr.com" {
		t.Fatalf("storefront hosts: %+v", p.Hosts)
	}
	if st.stripes != 0 {
		t.Fatal("the storefront team read team_stripe")
	}

	p, err = svc.Profile(ctx, tenant.Team{ID: 7, Name: "brand"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Pack != "" || p.StripeAccountID != "acct" || p.StripeMode != "test" || p.StripePublishableKey != "" {
		t.Fatalf("team profile: %+v", p)
	}
	if len(p.Hosts) != 1 || p.Hosts[0] != "brand.com" {
		t.Fatalf("a redirect served the shop: %v", p.Hosts)
	}

	// No env key: checkout off, no mode.
	svc.StorefrontStripe = func(context.Context) (string, string) { return "", "live" }
	if p, _ = svc.Profile(ctx, tenant.Team{ID: 1}, true); p.StripePublishableKey != "" || p.StripeMode != "" || p.Pack != StorefrontPack {
		t.Fatalf("no key: %+v", p)
	}
	svc.StorefrontStripe = nil
	if p, _ = svc.Profile(ctx, tenant.Team{ID: 1}, true); p.StripePublishableKey != "" || p.StripeMode != "" {
		t.Fatalf("nil key func: %+v", p)
	}
	// Without a storefront team, team #1 is a team like any other.
	svc.StorefrontID = 0
	if p, _ = svc.Profile(ctx, tenant.Team{ID: 1}, true); p.Pack != "" || p.StripePublishableKey != "pk_team_row" {
		t.Fatalf("no storefront: %+v", p)
	}
}

// The storefront team's target is its main address whatever team_shop says; another team needs its
// shop on and not closed.
func TestRevalidateTargetStorefront(t *testing.T) {
	ctx := context.Background()
	st := &memStore{
		shops: map[int64]platformstore.TeamShop{7: {Enabled: true}, 8: {}},
		hosts: map[int64][]platformstore.ShopHost{
			1: {legacy("grbpwr.com", true)},
			7: {{Host: "brand.com", Status: platformstore.ShopHostLive, Primary: true}},
			8: {{Host: "off.com", Status: platformstore.ShopHostLive, Primary: true}},
		},
	}
	svc := &Service{St: st, StorefrontID: 1}
	for _, c := range []struct {
		team int64
		host string
		ok   bool
	}{{1, "grbpwr.com", true}, {7, "brand.com", true}, {8, "", false}} {
		h, ok, err := svc.RevalidateTarget(ctx, c.team)
		if err != nil || h != c.host || ok != c.ok {
			t.Errorf("team %d: %q %v %v", c.team, h, ok, err)
		}
	}
	st.hosts[1] = []platformstore.ShopHost{legacy("grbpwr.com", false)}
	if h, ok, err := svc.RevalidateTarget(ctx, 1); err != nil || ok || h != "" {
		t.Fatalf("storefront without a main address: %q %v %v", h, ok, err)
	}
	st.err = errors.New("down")
	if _, ok, err := svc.RevalidateTarget(ctx, 1); err == nil || ok {
		t.Fatal("a failed lookup must fail")
	}
}

// The storefront team goes to the shared storefront at its main address once it has one, and to the
// old storefront (Base) before that, without the shop path, or when the lookup fails.
func TestRevalidatorStorefrontOnShared(t *testing.T) {
	ctx := tenant.With(context.Background(), tenant.Team{ID: 1})
	base, sh := &allRe{}, &hostRe{}
	host, lookupErr := "", error(nil)
	r := &Revalidator{Base: base, Shop: sh, StorefrontID: 1, Target: func(context.Context, int64) (string, bool, error) {
		return host, host != "", lookupErr
	}}
	_ = r.RevalidateAll(ctx, &dto.RevalidationData{})
	if base.n != 1 || sh.host != "" {
		t.Fatalf("no main address: base %d shop %q", base.n, sh.host)
	}
	host = "grbpwr.com"
	_ = r.RevalidateAll(ctx, &dto.RevalidationData{})
	if base.n != 1 || sh.host != "grbpwr.com" || sh.key != "t1" {
		t.Fatalf("main address: base %d shop %q %q", base.n, sh.host, sh.key)
	}
	sh.host, lookupErr = "", errors.New("down")
	_ = r.RevalidateAll(ctx, &dto.RevalidationData{})
	if base.n != 2 || sh.host != "" {
		t.Fatalf("failed lookup: base %d shop %q", base.n, sh.host)
	}
	lookupErr, r.Shop = nil, nil
	_ = r.RevalidateAll(ctx, &dto.RevalidationData{})
	if base.n != 3 {
		t.Fatalf("no shop path: base %d", base.n)
	}
	r.Base = nil
	if err := r.RevalidateAll(ctx, &dto.RevalidationData{}); err != nil {
		t.Fatal(err)
	}
}
