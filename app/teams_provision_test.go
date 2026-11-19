package app

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Mode off (no control plane): no provisioner, no error, and no teams variable is read.
func TestProvisionerModeOffNeedsNothing(t *testing.T) {
	a := &App{c: &config.Config{}}
	stop, err := a.startProvisioner(context.Background())
	if err != nil || stop == nil {
		t.Fatalf("mode off: stop=%v err=%v", stop != nil, err)
	}
	stop()
}

// Teams mode on with a missing or refused TENANT_DB_PREFIX is a boot error, never a healthy boot
// with no keeper, claimer, fleet migrator or sweep.
func TestProvisionerTeamsModeRefusesBadPrefix(t *testing.T) {
	for _, tc := range []struct{ prefix, team1 string }{
		{"", "grbpwr_beta"},
		{"Bad-Prefix", "grbpwr_beta"},
		{"lkb_t_", "lkb_t_abcd1234"}, // the new-team pattern would match team #1's database
	} {
		prefix := tc.prefix
		c := &config.Config{}
		c.Platform.DBPrefix = prefix
		a := &App{c: c, platform: &platformstore.Store{}, teamOne: tenant.TeamOne(tc.team1)}
		if _, err := a.startProvisioner(context.Background()); err == nil {
			t.Errorf("prefix %q: provisioner started or stayed off silently", prefix)
		}
	}
}
