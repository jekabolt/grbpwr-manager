package frontend

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/storefront"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Without a storefront team (TENANT_STOREFRONT_TEAM_ID=none) the storefront API is built with no
// legacy STOREFRONT_AUTH_* keys: team shops sign customers in with STOREFRONT_AUTH_SHOP_MASTER, and a
// call that names no shop is refused with a clear code instead of the server dropping team shops.
func TestTeamShopsOnlyNeedsNoLegacyAuth(t *testing.T) {
	s := NewTeamShops(nil, nil, nil, nil, nil, nil, &storefront.Config{})
	if s.storefront != nil {
		t.Fatal("team-shops-only server must have no legacy customer keys")
	}
	s.SetShops(ShopDeps{StorefrontID: 0, Master: testShopMaster})

	_, err := s.sfAuth(context.Background())
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("no shop: %v, want FailedPrecondition", err)
	}
	rt, err := s.sfAuth(tenant.With(context.Background(), tenant.Team{ID: 7}))
	if err != nil {
		t.Fatalf("team shop: %v", err)
	}
	if rt.teamID != 7 || rt.accessJwtAuth == nil || rt.loginPepper == "" || rt.refreshPepper == "" {
		t.Fatalf("team shop runtime not derived: %+v", rt)
	}
	if rt.magicLinkBaseURL != defaultShopMagicLinkPath || rt.accessTTL == 0 || rt.loginChallengeTTL == 0 {
		t.Fatalf("defaults: link %q access %v challenge %v", rt.magicLinkBaseURL, rt.accessTTL, rt.loginChallengeTTL)
	}
	if !verifies(rt, issue(t, rt)) {
		t.Fatal("team shop refuses its own token")
	}

	// Without the shop master team-shop accounts are unavailable (the app lists it in /statusz).
	bare := NewTeamShops(nil, nil, nil, nil, nil, nil, nil)
	bare.SetShops(ShopDeps{})
	if _, err := bare.sfAuth(tenant.With(context.Background(), tenant.Team{ID: 7})); status.Code(err) != codes.Unavailable {
		t.Fatalf("no master: %v, want Unavailable", err)
	}
}

// The configured magic-link base keeps its path for a team shop's letter.
func TestTeamShopsOnlyKeepsConfiguredMagicPath(t *testing.T) {
	rt := newShopBaseRuntime(&storefront.Config{MagicLinkBaseURL: "https://grbpwr.com/login/", AccessJWTIssuer: "iss"})
	if rt.magicLinkBaseURL != "https://grbpwr.com/login" {
		t.Fatalf("link %q", rt.magicLinkBaseURL)
	}
	d, err := deriveShopAuth(rt, testShopMaster, 3)
	if err != nil {
		t.Fatal(err)
	}
	if d.accessExpectations.Issuer != "iss" || d.accessExpectations.Audience != "shop:3" {
		t.Fatalf("expectations %+v", d.accessExpectations)
	}
}
