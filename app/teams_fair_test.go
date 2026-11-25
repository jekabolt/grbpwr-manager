package app

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/teamfair"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTeamFairConfig(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	c := teamFairConfig(5, 1, get)
	if c.Off || c.Team.Light != 3 || c.GlobalHeavy != 2 || c.Team.Heavy != 1 || c.Team.AI != 2 {
		t.Fatalf("defaults for pool 5: %+v", c)
	}
	env = map[string]string{
		envTeamFair: "OFF", envTeamLightParallel: "4", envTeamHeavyParallel: "2", envHeavyParallel: "3",
		envTeamAIParallel: "1", envTeamRate: "10", envTeamBurst: "20",
	}
	c = teamFairConfig(5, 1, get)
	if !c.Off || c.Team.Light != 4 || c.Team.Heavy != 2 || c.GlobalHeavy != 3 || c.Team.AI != 1 ||
		c.Team.Rate != 10 || c.Team.Burst != 20 {
		t.Fatalf("overrides: %+v", c)
	}
	env = map[string]string{envTeamLightParallel: "-1", envTeamRate: "x"}
	c = teamFairConfig(15, 0, get)
	if c.Team.Light != 8 || c.Team.Rate != 50 {
		t.Fatalf("bad values fall back to defaults: %+v", c)
	}
	d := teamfair.Defaults(15)
	if c.ShopOff || c.Shop.Light != 8 || c.GlobalShop != 10 || c.StorefrontTeamID != 0 || c.StorefrontShop != d.StorefrontShop {
		t.Fatalf("shop defaults for pool 15: %+v", c)
	}
	if c.Checkout != d.Checkout || c.GlobalCheckout != 10 || c.StorefrontCheckout != d.StorefrontCheckout ||
		c.StorefrontShop.Light < 64 || c.StorefrontCheckout.Light < 32 {
		t.Fatalf("checkout defaults for pool 15: %+v", c)
	}
	env = map[string]string{
		envTeamFairShop: "off", envTeamShopParallel: "2", envTeamShopRate: "5", envTeamShopBurst: "9",
		envShopParallel: "6", envStorefrontShopPar: "7", envStorefrontShopRate: "30", envStorefrontShopBurst: "60",
	}
	c = teamFairConfig(5, 1, get)
	if !c.ShopOff || c.Shop.Light != 2 || c.Shop.Rate != 5 || c.Shop.Burst != 9 || c.GlobalShop != 6 ||
		c.StorefrontTeamID != 1 || c.StorefrontShop != (teamfair.Limits{Light: 7, Rate: 30, Burst: 60}) {
		t.Fatalf("shop overrides: %+v", c)
	}
	env = map[string]string{
		envTeamCheckoutParallel: "2", envTeamCheckoutRate: "4", envTeamCheckoutBurst: "8", envCheckoutParallel: "5",
		envStorefrontCheckoutPar: "11", envStorefrontCheckoutRate: "12", envStorefrontCheckoutBurst: "13",
	}
	c = teamFairConfig(5, 1, get)
	if c.Checkout != (teamfair.Limits{Light: 2, Rate: 4, Burst: 8}) || c.GlobalCheckout != 5 ||
		c.StorefrontCheckout != (teamfair.Limits{Light: 11, Rate: 12, Burst: 13}) {
		t.Fatalf("checkout overrides: %+v", c)
	}
	if backgroundParallel(5) != 2 || backgroundParallel(15) != 5 || backgroundParallel(0) != 2 {
		t.Fatal("background slots")
	}
}

// Checkout is its own lane. A team shop's browsing full (its one slot held by a slow page, its rate
// used up): another page is refused with shop_busy, while every checkout, payment and order-status
// method still runs. Checkout itself is bounded: a team's own checkout cap, then all team shops'
// checkouts together, refuse with shop_busy; the storefront team never takes a global checkout slot.
func TestCheckoutLane(t *testing.T) {
	env := map[string]string{
		envTeamShopParallel: "1", envTeamShopRate: "1", envTeamShopBurst: "1",
		envTeamCheckoutParallel: "1", envCheckoutParallel: "2", envStorefrontCheckoutPar: "1",
	}
	cfg := teamFairConfig(5, 1, func(k string) string { return env[k] })
	cfg.ShopWait = 20 * time.Millisecond
	cfg.CheckoutWait = 20 * time.Millisecond
	e := newTeamFairEdge(cfg)
	brand := func(id int64) context.Context {
		return tenant.With(context.Background(), tenant.Team{ID: id, Slug: "brand"})
	}
	call := func(ctx context.Context, m string, h grpc.UnaryHandler) error {
		_, err := e.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: m}, h)
		return err
	}
	ok := func(context.Context, any) (any, error) { return nil, nil }
	// hold runs m for ctx until the returned func is called.
	hold := func(ctx context.Context, m string) func() {
		t.Helper()
		release, started, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			done <- call(ctx, m, func(context.Context, any) (any, error) {
				close(started)
				<-release
				return nil, nil
			})
		}()
		select {
		case <-started:
		case err := <-done:
			t.Fatalf("%s refused: %v", m, err)
		}
		return func() {
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	}
	shopBusy := func(err error) bool {
		st, _ := status.FromError(err)
		return st.Code() == codes.ResourceExhausted && st.Message() == teamfair.ShopBusyCode
	}

	page := hold(brand(2), frontend.FrontendService_GetHero_FullMethodName)
	if err := call(brand(2), frontend.FrontendService_GetColorwaysPaged_FullMethodName, ok); !shopBusy(err) {
		t.Fatalf("a page with browsing full: %v, want shop_busy", err)
	}
	paying := []string{
		frontend.FrontendService_ValidateOrderItemsInsert_FullMethodName,
		frontend.FrontendService_SubmitOrder_FullMethodName,
		frontend.FrontendService_GetOrderInvoice_FullMethodName,
		frontend.FrontendService_CancelOrderInvoice_FullMethodName,
		frontend.FrontendService_ValidateOrderByUUID_FullMethodName,
		frontend.FrontendService_GetOrderByUUIDAndEmail_FullMethodName,
		frontend.FrontendService_CancelOrderByUser_FullMethodName,
		frontend.FrontendService_ListMyOrders_FullMethodName,
	}
	for _, m := range paying {
		ran := false
		if err := call(brand(2), m, func(context.Context, any) (any, error) { ran = true; return nil, nil }); err != nil || !ran {
			t.Fatalf("%s with browsing full: err %v, ran %v", m, err, ran)
		}
	}

	// The team's own checkout cap (1): a second checkout of the same team is refused.
	pay2 := hold(brand(2), frontend.FrontendService_SubmitOrder_FullMethodName)
	if err := call(brand(2), frontend.FrontendService_GetOrderInvoice_FullMethodName, ok); !shopBusy(err) {
		t.Fatalf("a second checkout of one team: %v, want shop_busy", err)
	}
	// All team shops' checkouts (2): a third team is refused, the storefront team still checks out
	// up to its own cap.
	pay3 := hold(brand(3), frontend.FrontendService_SubmitOrder_FullMethodName)
	if err := call(brand(4), frontend.FrontendService_SubmitOrder_FullMethodName, ok); !shopBusy(err) {
		t.Fatalf("a checkout with every global checkout slot held: %v, want shop_busy", err)
	}
	paySF := hold(brand(1), frontend.FrontendService_SubmitOrder_FullMethodName)
	if err := call(brand(1), frontend.FrontendService_SubmitOrder_FullMethodName, ok); !shopBusy(err) {
		t.Fatalf("the storefront team over its checkout cap: %v, want shop_busy", err)
	}
	paySF()
	pay3()
	pay2()
	page()
	if err := call(brand(4), frontend.FrontendService_SubmitOrder_FullMethodName, ok); err != nil {
		t.Fatalf("checkout after the slots were freed: %v", err)
	}
}
