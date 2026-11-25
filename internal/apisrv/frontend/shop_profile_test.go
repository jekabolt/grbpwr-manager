package frontend

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

type profileStore struct {
	stripe map[int64]platformstore.TeamStripe
}

func (profileStore) TeamShop(context.Context, int64) (platformstore.TeamShop, error) {
	return platformstore.TeamShop{Enabled: true}, nil
}

func (profileStore) ShopHosts(context.Context, int64) ([]platformstore.ShopHost, error) {
	return nil, nil
}

func (profileStore) TeamBrand(context.Context, int64) (platformstore.TeamBrand, bool, error) {
	return platformstore.TeamBrand{}, false, nil
}

func (p profileStore) TeamStripe(_ context.Context, id int64) (platformstore.TeamStripe, bool, error) {
	st, ok := p.stripe[id]
	return st, ok, nil
}

// The storefront team's profile carries its pack and its env publishable key (no team payments
// needed); another team gets no pack and its own key only through team payments.
func TestGetShopProfileStorefrontPackAndKey(t *testing.T) {
	s := shopAuthServer(t, testShopMaster)
	s.shops.d.Service = &shop.Service{
		St:               profileStore{stripe: map[int64]platformstore.TeamStripe{7: {Kind: "keys", PublishableKey: "pk_test_team", Mode: "test", ChargesEnabled: true}}},
		StorefrontID:     1,
		StorefrontStripe: func(context.Context) (string, string) { return "pk_test_env", "test" },
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
	p = get(7)
	if p.GetPack() != "" || p.GetStripePublishableKey() != "" {
		t.Fatalf("team without team payments: pack %q key %q", p.GetPack(), p.GetStripePublishableKey())
	}
	s.shops.d.Payments = &fakeShopPayments{}
	if p = get(7); p.GetStripePublishableKey() != "pk_test_team" || p.GetStripeMode() != "test" {
		t.Fatalf("team with team payments: key %q mode %q", p.GetStripePublishableKey(), p.GetStripeMode())
	}
}
