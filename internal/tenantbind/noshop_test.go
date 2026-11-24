package tenantbind

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// noStorefrontBinder is shopBinder on an installation without a storefront team
// (TENANT_STOREFRONT_TEAM_ID=none).
func noStorefrontBinder(t *testing.T) (*Binder, *shopFakePlane) {
	t.Helper()
	b, p := shopBinder(t)
	b.usePlane(p, platformstore.Config{DSN: "u:p@tcp(h:3306)/lekalo", BaseDomain: "lekalo.studio", StorefrontNone: true})
	return b, p
}

// TestNoStorefrontTeamShops: without a storefront team a request naming a team shop binds that
// team; one naming no shop (or the old storefront's own host) is shop_not_found, never team #1.
func TestNoStorefrontTeamShops(t *testing.T) {
	b, _ := noStorefrontBinder(t)
	ctx := context.Background()
	if id := b.StorefrontTeamID(); id != 0 {
		t.Fatalf("storefront team %d, want none", id)
	}
	if _, err := b.StorefrontTeam(ctx); !errors.Is(err, ErrNoStorefront) {
		t.Fatalf("StorefrontTeam: %v", err)
	}
	for _, c := range []struct {
		name, host, origin string
		want               int64
		code               codes.Code
	}{
		{"header names a shop", "acme.shop.lekalo.studio", "", 7, codes.OK},
		{"origin names a shop", "", "https://bolt.shop.lekalo.studio", 9, codes.OK},
		{"no header no origin", "", "", 0, codes.NotFound},
		{"old storefront host", "grbpwr.com", "", 0, codes.NotFound},
		{"unknown origin", "", "https://elsewhere.example", 0, codes.NotFound},
	} {
		got, legacy, err := b.ShopTeam(ctx, c.host, c.origin)
		if status.Code(err) != c.code {
			t.Fatalf("%s: err %v, want %v", c.name, err, c.code)
		}
		if c.code == codes.OK && (got.ID != c.want || legacy) {
			t.Fatalf("%s: team %d legacy %v", c.name, got.ID, legacy)
		}
	}

	// gRPC: a team shop call binds its team; a call naming no shop is refused.
	var bound tenant.Team
	h := func(ctx context.Context, _ any) (any, error) {
		bound, _ = tenant.From(ctx)
		return nil, nil
	}
	md := metadata.Pairs(ShopHostMD, "acme.com", "grpcgateway-origin", "https://acme.com")
	if _, err := b.Unary()(metadata.NewIncomingContext(ctx, md), nil, &grpc.UnaryServerInfo{FullMethod: "/frontend.FrontendService/GetHero"}, h); err != nil || bound.ID != 7 {
		t.Fatalf("acme hero: %v, team %d", err, bound.ID)
	}
	bound = tenant.Team{}
	if _, err := b.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/frontend.FrontendService/GetHero"}, h); status.Code(err) != codes.NotFound || bound.ID != 0 {
		t.Fatalf("hero naming no shop: %v, team %d", err, bound.ID)
	}

	// Webhook paths stay unbound: the shop Stripe webhooks bind their own team from the token/account.
	wctx, err := b.bindFor(ctx, ClassStorefront)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := tenant.From(wctx); ok {
		t.Fatalf("webhook bound team %d without a storefront team", got.ID)
	}
}
