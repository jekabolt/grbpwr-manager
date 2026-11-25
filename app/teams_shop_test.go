package app

import (
	"context"
	"slices"
	"testing"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
)

// The reserved hosts come from the allowed and legacy admin origins: no dev, preview or IP host, no
// duplicate.
func TestShopReservedHosts(t *testing.T) {
	got := shopReservedHosts([]string{"beta.lekalo.studio", "shop.beta.lekalo.studio"},
		[]string{"https://beta.lekalo.studio", "https://acme.shop.beta.lekalo.studio", "https://www.grbpwr.com", "https://grbpwr.com/", "http://localhost:3000", "https://x.vercel.app", "http://127.0.0.1:8080", "https://WWW.grbpwr.com"},
		[]string{"https://beta.grbpwr.com", "nonsense"},
	)
	want := []string{"www.grbpwr.com", "grbpwr.com", "beta.grbpwr.com"}
	if !slices.Equal(got, want) {
		t.Fatalf("reserved hosts %v, want %v", got, want)
	}
}

// The storefront team's shop hosts come from TENANT_STOREFRONT_HOSTS alone (default grbpwr.com, www
// and beta), never from the allowed origins: a CORS-only page (github.io) is never one unless
// listed. Admin, dev, preview and platform hosts are dropped; "none" and no storefront team = none.
func TestStorefrontShopHosts(t *testing.T) {
	platform := []string{"beta.lekalo.studio", "shop.beta.lekalo.studio"}
	admin := []string{"admin.grbpwr.com", "backend.grbpwr.com"}
	cfg := config.Platform{}
	if got := storefrontShopHosts(1, cfg.LegacyStorefrontHosts(), platform, admin); !slices.Equal(got, []string{"grbpwr.com", "www.grbpwr.com", "beta.grbpwr.com"}) {
		t.Fatalf("default: %v", got)
	}
	cfg.StorefrontHosts = " https://Shop.Brand.com/ , grbpwr.com,admin.grbpwr.com, localhost, x.vercel.app, acme.shop.beta.lekalo.studio, grbpwr.com, 127.0.0.1 "
	if got := storefrontShopHosts(1, cfg.LegacyStorefrontHosts(), platform, admin); !slices.Equal(got, []string{"shop.brand.com", "grbpwr.com"}) {
		t.Fatalf("explicit: %v", got)
	}
	cfg.StorefrontHosts = "NONE"
	if got := storefrontShopHosts(1, cfg.LegacyStorefrontHosts(), platform, admin); len(got) != 0 {
		t.Fatalf("none: %v", got)
	}
	cfg.StorefrontHosts = ""
	if got := storefrontShopHosts(0, cfg.LegacyStorefrontHosts(), platform, admin); len(got) != 0 {
		t.Fatalf("no storefront team: %v", got)
	}
	// The allowed origins never add a host: the github.io page is not a storefront host.
	if got := storefrontShopHosts(1, cfg.LegacyStorefrontHosts(), platform, admin); slices.Contains(got, "jekabolt.github.io") {
		t.Fatalf("a CORS-only origin became a storefront host: %v", got)
	}
}

// One master for shop customer keys and shop revalidation secrets fails the boot.
func TestWireShopsRefusesOneMaster(t *testing.T) {
	c := &config.Config{}
	c.Revalidation.ShopMaster = "same-master-same-master-same-master"
	c.StorefrontAuth.ShopMaster = "same-master-same-master-same-master"
	a := &App{c: c}
	if err := a.wireShops(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("one master for both accepted")
	}
	c.Revalidation.ShopMaster = "another-master-another-master-xx"
	if err := a.wireShops(context.Background(), nil, nil, nil); err != nil {
		t.Fatalf("two masters: %v", err)
	}
}

// The storefront's publishable key comes from its env processor; the mode from the key itself, the
// slot only when the key does not say.
func TestStripeKeyMode(t *testing.T) {
	for _, c := range []struct {
		cfg       stripe.Config
		slot      string
		key, mode string
	}{
		{stripe.Config{Enabled: true, PubKey: " pk_live_x "}, "test", "pk_live_x", "live"},
		{stripe.Config{Enabled: true, PubKey: "pk_test_x"}, "live", "pk_test_x", "test"},
		{stripe.Config{Enabled: true, PubKey: "pk_other"}, "live", "pk_other", "live"},
		{stripe.Config{Enabled: false, PubKey: "pk_live_x"}, "live", "", ""},
		{stripe.Config{Enabled: true}, "test", "", ""},
	} {
		if k, m := stripeKeyMode(c.cfg, c.slot); k != c.key || m != c.mode {
			t.Errorf("%+v %s: %q %q", c.cfg, c.slot, k, m)
		}
	}
}

// The storefront team's main address is never an admin or API host: the shared storefront would
// send its customers there.
func TestShopNoPrimary(t *testing.T) {
	got := shopNoPrimary("https://backend.grbpwr.com", []string{"https://admin.grbpwr.com"},
		[]string{"https://grbpwr.com", "https://www.grbpwr.com", "https://admin.grbpwr.com", "https://api.grbpwr.com", "https://admin-beta.grbpwr.com"})
	want := []string{"backend.grbpwr.com", "admin.grbpwr.com", "api.grbpwr.com", "admin-beta.grbpwr.com"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := shopNoPrimary("", nil, nil); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}
