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
	if c.ShopOff || c.Shop.Light != 8 || c.GlobalShop != 10 || c.StorefrontTeamID != 0 || c.StorefrontShop != (teamfair.Limits{}) {
		t.Fatalf("shop defaults for pool 15: %+v", c)
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
	if backgroundParallel(5) != 2 || backgroundParallel(15) != 5 || backgroundParallel(0) != 2 {
		t.Fatal("background slots")
	}
}

// A team shop's lane full (its one slot held by a slow page, its rate used up): another page is
// refused with shop_busy, while checkout, payment and order status still run.
func TestCheckoutNeverRefused(t *testing.T) {
	env := map[string]string{envTeamShopParallel: "1", envTeamShopRate: "1", envTeamShopBurst: "1"}
	cfg := teamFairConfig(5, 1, func(k string) string { return env[k] })
	cfg.ShopWait = 20 * time.Millisecond
	e := newTeamFairEdge(cfg)
	ctx := tenant.With(context.Background(), tenant.Team{ID: 2, Slug: "brand"})
	call := func(m string, h grpc.UnaryHandler) error {
		_, err := e.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: m}, h)
		return err
	}

	hold, started, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- call(frontend.FrontendService_GetHero_FullMethodName, func(context.Context, any) (any, error) {
			close(started)
			<-hold
			return nil, nil
		})
	}()
	<-started
	err := call(frontend.FrontendService_GetColorwaysPaged_FullMethodName, func(context.Context, any) (any, error) { return nil, nil })
	if st, _ := status.FromError(err); st.Code() != codes.ResourceExhausted || st.Message() != teamfair.ShopBusyCode {
		t.Fatalf("a page with the lane full: %v, want shop_busy", err)
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
		if err := call(m, func(context.Context, any) (any, error) { ran = true; return nil, nil }); err != nil || !ran {
			t.Fatalf("%s with the lane full: err %v, ran %v", m, err, ran)
		}
	}
	close(hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
