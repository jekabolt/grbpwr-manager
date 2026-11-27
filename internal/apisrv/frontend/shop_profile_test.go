package frontend

import (
	"context"
	"slices"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

type profileStore struct {
	stripe map[int64]platformstore.TeamStripe
	hosts  map[int64][]platformstore.ShopHost
}

func (profileStore) TeamShop(context.Context, int64) (platformstore.TeamShop, error) {
	return platformstore.TeamShop{Enabled: true}, nil
}

func (p profileStore) ShopHosts(_ context.Context, id int64) ([]platformstore.ShopHost, error) {
	return p.hosts[id], nil
}

func (profileStore) TeamBrand(context.Context, int64) (platformstore.TeamBrand, bool, error) {
	return platformstore.TeamBrand{}, false, nil
}

func (p profileStore) TeamStripe(_ context.Context, id int64) (platformstore.TeamStripe, bool, error) {
	st, ok := p.stripe[id]
	return st, ok, nil
}

// The storefront team's profile carries its pack and its env publishable key (no team payments
// needed) and never its admin or API hosts nor a CORS-only origin; another team gets no pack and its
// own key only through team payments.
func TestGetShopProfileStorefrontPackAndKey(t *testing.T) {
	s := shopAuthServer(t, testShopMaster)
	live := func(host string, primary bool) platformstore.ShopHost {
		return platformstore.ShopHost{Host: host, TeamID: 1, Kind: platformstore.ShopHostLegacy, Status: platformstore.ShopHostLive, Primary: primary}
	}
	s.shops.d.Service = &shop.Service{
		St: profileStore{
			stripe: map[int64]platformstore.TeamStripe{7: {Kind: "keys", PublishableKey: "pk_test_team", Mode: "test", ChargesEnabled: true}},
			hosts: map[int64][]platformstore.ShopHost{1: {live("admin.grbpwr.com", false), live("grbpwr.com", true),
				live("backend.grbpwr.com", false), live("jekabolt.github.io", false)}},
		},
		StorefrontID:     1,
		StorefrontStripe: func(context.Context) (string, string) { return "pk_test_env", "test" },
		AdminHosts:       []string{"admin.grbpwr.com", "backend.grbpwr.com"},
		StorefrontHosts:  []string{"grbpwr.com", "www.grbpwr.com"},
	}
	get := func(id int64) *pb_frontend.ShopProfile {
		t.Helper()
		res, err := s.GetShopProfile(tenant.With(context.Background(), tenant.Team{ID: id}), &pb_frontend.GetShopProfileRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetProfile()
	}
	p := get(1)
	if p.GetPack() != shop.StorefrontPack || p.GetStripePublishableKey() != "pk_test_env" || p.GetStripeMode() != "test" || p.GetStripeAccountId() != "" {
		t.Fatalf("storefront: pack %q key %q mode %q", p.GetPack(), p.GetStripePublishableKey(), p.GetStripeMode())
	}
	if p.GetPrimaryHost() != "grbpwr.com" || !slices.Equal(p.GetHosts(), []string{"grbpwr.com"}) {
		t.Fatalf("storefront hosts: primary %q hosts %v", p.GetPrimaryHost(), p.GetHosts())
	}
	p = get(7)
	if p.GetPack() != "" || p.GetStripePublishableKey() != "" {
		t.Fatalf("team without team payments: pack %q key %q", p.GetPack(), p.GetStripePublishableKey())
	}
	s.shops.d.Payments = &fakeShopPayments{}
	if p = get(7); p.GetStripePublishableKey() != "pk_test_team" || p.GetStripeMode() != "test" {
		t.Fatalf("team with team payments: key %q mode %q", p.GetStripePublishableKey(), p.GetStripeMode())
	}
}
