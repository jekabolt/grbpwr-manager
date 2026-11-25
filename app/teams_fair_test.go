package app

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/teamfair"
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
