package app

import "testing"

func TestTeamFairConfig(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	c := teamFairConfig(5, get)
	if c.Off || c.Team.Light != 3 || c.GlobalHeavy != 2 || c.Team.Heavy != 1 || c.Team.AI != 2 {
		t.Fatalf("defaults for pool 5: %+v", c)
	}
	env = map[string]string{
		envTeamFair: "OFF", envTeamLightParallel: "4", envTeamHeavyParallel: "2", envHeavyParallel: "3",
		envTeamAIParallel: "1", envTeamRate: "10", envTeamBurst: "20",
	}
	c = teamFairConfig(5, get)
	if !c.Off || c.Team.Light != 4 || c.Team.Heavy != 2 || c.GlobalHeavy != 3 || c.Team.AI != 1 ||
		c.Team.Rate != 10 || c.Team.Burst != 20 {
		t.Fatalf("overrides: %+v", c)
	}
	env = map[string]string{envTeamLightParallel: "-1", envTeamRate: "x"}
	c = teamFairConfig(15, get)
	if c.Team.Light != 8 || c.Team.Rate != 50 {
		t.Fatalf("bad values fall back to defaults: %+v", c)
	}
	if backgroundParallel(5) != 2 || backgroundParallel(15) != 5 || backgroundParallel(0) != 2 {
		t.Fatal("background slots")
	}
}
