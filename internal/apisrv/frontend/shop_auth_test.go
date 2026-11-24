package frontend

import (
	"context"
	"testing"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testShopMaster = "test-shop-master-at-least-32-bytes-long!"

func shopAuthServer(t *testing.T, master string) *Server {
	t.Helper()
	base, err := newStorefrontAuthRuntime(storefrontConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{storefront: base}
	s.SetShops(ShopDeps{StorefrontID: 1, Master: master})
	return s
}

func issue(t *testing.T, rt *storefrontAuthRuntime) string {
	t.Helper()
	tok, err := jwt.NewTokenWithSubjectOpts(rt.accessJwtAuth, rt.accessTTL, testEmail, rt.accessIssueOpts)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func verifies(rt *storefrontAuthRuntime, tok string) bool {
	_, _, _, err := jwt.VerifyTokenFull(rt.accessJwtAuth, tok, rt.accessExpectations)
	return err == nil
}

func TestShopCustomerTokensStayInTheirShop(t *testing.T) {
	s := shopAuthServer(t, testShopMaster)
	at := func(id int64) *storefrontAuthRuntime {
		ctx := context.Background()
		if id != 0 {
			ctx = tenant.With(ctx, tenant.Team{ID: id})
		}
		rt, err := s.sfAuth(ctx)
		if err != nil {
			t.Fatalf("team %d: %v", id, err)
		}
		return rt
	}
	grbpwr, noTenant, acme, bolt := at(1), at(0), at(7), at(9)
	if grbpwr != s.storefront || noTenant != s.storefront {
		t.Fatal("the storefront team must keep the env keys")
	}
	if at(7) != acme {
		t.Fatal("per-team runtime is not cached")
	}
	gTok, aTok := issue(t, grbpwr), issue(t, acme)
	if !verifies(grbpwr, gTok) || !verifies(acme, aTok) {
		t.Fatal("a shop refuses its own token")
	}
	if verifies(bolt, aTok) || verifies(grbpwr, aTok) || verifies(acme, gTok) {
		t.Fatal("a token crossed shops")
	}
	if acme.loginPepper == bolt.loginPepper || acme.refreshPepper == grbpwr.refreshPepper {
		t.Fatal("peppers are shared")
	}
	tk, err := jwtauth.VerifyToken(acme.accessJwtAuth, aTok)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := tk.AsMap(context.Background())
	if tid, _ := m["tid"].(float64); tid != 7 {
		t.Fatalf("tid = %v, want 7", m["tid"])
	}
	if aud := tk.Audience(); len(aud) != 1 || aud[0] != "shop:7" {
		t.Fatalf("aud = %v", aud)
	}
	tg, _ := jwtauth.VerifyToken(grbpwr.accessJwtAuth, gTok)
	if mg, _ := tg.AsMap(context.Background()); mg["tid"] != nil {
		t.Fatal("the storefront team's token gained a tid")
	}
}

func TestShopAccountsWithoutMaster(t *testing.T) {
	for _, m := range []string{"", "short"} {
		s := shopAuthServer(t, m)
		_, err := s.sfAuth(tenant.With(context.Background(), tenant.Team{ID: 7}))
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("master %q: %v", m, err)
		}
		if rt, err := s.sfAuth(tenant.With(context.Background(), tenant.Team{ID: 1})); err != nil || rt != s.storefront {
			t.Fatalf("master %q: storefront team broke: %v", m, err)
		}
	}
}

func TestShopCheckoutNeverUsesTheStorefrontStripe(t *testing.T) {
	s := shopAuthServer(t, testShopMaster)
	_, err := s.getPaymentHandler(tenant.With(context.Background(), tenant.Team{ID: 7}), "card")
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != MsgShopPaymentsNotReady {
		t.Fatalf("team shop checkout: %v", err)
	}
	// The storefront team reaches today's selector (no processor in this test: unavailable).
	_, err = s.getPaymentHandler(tenant.With(context.Background(), tenant.Team{ID: 1}), "card")
	if status.Convert(err).Message() == MsgShopPaymentsNotReady {
		t.Fatal("the storefront team was refused")
	}
}
