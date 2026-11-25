package app

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/teamfair"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
	frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

// Per-team shares (X15, gap 14; tmp/plans/teams/lanes/X15-design.md). Every value is optional: the
// defaults are derived from MYSQL_MAX_OPEN_CONNECTIONS (teamfair.Defaults). TEAM_FAIR=off turns the
// gate into a pass-through.
const (
	envTeamFair          = "TEAM_FAIR"
	envTeamLightParallel = "TEAM_LIGHT_PARALLEL"
	envTeamHeavyParallel = "TEAM_HEAVY_PARALLEL"
	envHeavyParallel     = "HEAVY_PARALLEL"
	envTeamAIParallel    = "TEAM_AI_PARALLEL"
	envTeamRate          = "TEAM_RATE"
	envTeamBurst         = "TEAM_BURST"
	envBackgroundSlots   = "BACKGROUND_PARALLEL"

	// The shop lane (storefront calls): TEAM_FAIR_SHOP=off leaves them ungated.
	envTeamFairShop        = "TEAM_FAIR_SHOP"
	envTeamShopParallel    = "TEAM_SHOP_PARALLEL"
	envTeamShopRate        = "TEAM_SHOP_RATE"
	envTeamShopBurst       = "TEAM_SHOP_BURST"
	envShopParallel        = "SHOP_PARALLEL"
	envStorefrontShopPar   = "TEAM_STOREFRONT_SHOP_PARALLEL"
	envStorefrontShopRate  = "TEAM_STOREFRONT_SHOP_RATE"
	envStorefrontShopBurst = "TEAM_STOREFRONT_SHOP_BURST"
)

func envPositiveInt(name string) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// teamFairConfig is teamfair.Defaults(pool) with the env overrides applied; storefront is the
// storefront team (0 = none).
func teamFairConfig(pool int, storefront int64, getenv func(string) string) teamfair.Config {
	cfg := teamfair.Defaults(pool)
	cfg.Off = strings.EqualFold(strings.TrimSpace(getenv(envTeamFair)), "off")
	cfg.ShopOff = strings.EqualFold(strings.TrimSpace(getenv(envTeamFairShop)), "off")
	cfg.StorefrontTeamID = storefront
	num := func(name string) int {
		n, err := strconv.Atoi(strings.TrimSpace(getenv(name)))
		if err != nil || n < 1 {
			return 0
		}
		return n
	}
	if n := num(envTeamLightParallel); n > 0 {
		cfg.Team.Light = n
	}
	if n := num(envTeamHeavyParallel); n > 0 {
		cfg.Team.Heavy = n
	}
	if n := num(envHeavyParallel); n > 0 {
		cfg.GlobalHeavy = n
	}
	if n := num(envTeamAIParallel); n > 0 {
		cfg.Team.AI = n
	}
	if n := num(envTeamRate); n > 0 {
		cfg.Team.Rate = float64(n)
	}
	if n := num(envTeamBurst); n > 0 {
		cfg.Team.Burst = n
	}
	if n := num(envTeamShopParallel); n > 0 {
		cfg.Shop.Light = n
	}
	if n := num(envTeamShopRate); n > 0 {
		cfg.Shop.Rate = float64(n)
	}
	if n := num(envTeamShopBurst); n > 0 {
		cfg.Shop.Burst = n
	}
	if n := num(envShopParallel); n > 0 {
		cfg.GlobalShop = n
	}
	if n := num(envStorefrontShopPar); n > 0 {
		cfg.StorefrontShop.Light = n
	}
	if n := num(envStorefrontShopRate); n > 0 {
		cfg.StorefrontShop.Rate = float64(n)
	}
	if n := num(envStorefrontShopBurst); n > 0 {
		cfg.StorefrontShop.Burst = n
	}
	return cfg
}

// backgroundParallel — how many team ticks ALL team workers run at once together: max(2, pool/3),
// BACKGROUND_PARALLEL overrides. Each worker alone still runs up to teamWorkerParallel teams; this
// shared bound keeps four workers from wanting sixteen connections of a pool of five.
func backgroundParallel(pool int) int {
	if n := envPositiveInt(envBackgroundSlots); n > 0 {
		return n
	}
	n := pool / 3
	if n < 2 {
		n = 2
	}
	return n
}

// backgroundSlots is the process-wide semaphore of team worker ticks, created on first use.
type backgroundSlots struct {
	once sync.Once
	c    chan struct{}
}

func (a *App) backgroundSlots() chan struct{} {
	a.bg.once.Do(func() {
		pool := 0
		if a.c != nil {
			pool = a.c.DB.MaxOpenConnections
		}
		a.bg.c = make(chan struct{}, backgroundParallel(pool))
	})
	return a.bg.c
}

// storefrontTeamID is the storefront team (grbpwr): TENANT_STOREFRONT_TEAM_ID, else team one unless
// the installation has none (0).
func (a *App) storefrontTeamID() int64 {
	cfg := a.c.Platform
	sf := cfg.StorefrontTeamID
	if sf <= 0 && !cfg.NoShop() {
		sf = tenant.TeamOneID
	}
	return sf
}

// neverRefused reports the storefront methods no share ever refuses: checkout, payment and order
// status. A customer paying, or looking at what they paid for, is never told the shop is busy.
func neverRefused(fullMethod string) bool {
	switch fullMethod {
	case frontend.FrontendService_ValidateOrderItemsInsert_FullMethodName,
		frontend.FrontendService_SubmitOrder_FullMethodName,
		frontend.FrontendService_GetOrderInvoice_FullMethodName,
		frontend.FrontendService_CancelOrderInvoice_FullMethodName,
		frontend.FrontendService_ValidateOrderByUUID_FullMethodName,
		frontend.FrontendService_GetOrderByUUIDAndEmail_FullMethodName,
		frontend.FrontendService_CancelOrderByUser_FullMethodName,
		frontend.FrontendService_ListMyOrders_FullMethodName:
		return true
	}
	return false
}

// newTeamFairEdge is the per-team gate of cfg in front of the gRPC chain: storefront calls take the
// shop lane of their shop's team, except neverRefused, which pass ungated.
func newTeamFairEdge(cfg teamfair.Config) *teamfair.Edge {
	return teamfair.NewEdge(teamfair.New(cfg),
		func(m string) bool { return tenantbind.ClassifyMethod(m) == tenantbind.ClassStorefront },
		neverRefused)
}

// initTeamFair builds the per-team gate and puts it into the gRPC chain (after admin auth).
// Storefront calls take the shop lane of their shop's team (checkout, payment and order status
// never do); the storefront team's shop is not gated unless TEAM_STOREFRONT_SHOP_* set limits.
func (a *App) initTeamFair(ctx context.Context) {
	cfg := teamFairConfig(a.c.DB.MaxOpenConnections, a.storefrontTeamID(), os.Getenv)
	a.fair = newTeamFairEdge(cfg)
	a.hs.SetTeamFair(a.fair.Unary())
	slog.Default().InfoContext(ctx, "per-team shares",
		slog.Bool("off", cfg.Off), slog.Int("light", cfg.Team.Light), slog.Int("heavy", cfg.Team.Heavy),
		slog.Int("heavy_all", cfg.GlobalHeavy), slog.Int("ai", cfg.Team.AI),
		slog.Float64("rate", cfg.Team.Rate), slog.Int("burst", cfg.Team.Burst),
		slog.Bool("shop_off", cfg.ShopOff), slog.Int("shop", cfg.Shop.Light), slog.Int("shop_all", cfg.GlobalShop),
		slog.Float64("shop_rate", cfg.Shop.Rate), slog.Int("storefront_shop", cfg.StorefrontShop.Light),
		slog.Int("background", cap(a.backgroundSlots())))
}
