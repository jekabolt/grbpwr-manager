package app

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

// The schema gate is wired only in teams mode: mode off every team stays «current» (no predicate),
// teams mode an old team is behind and team #1 with a NULL version is not.
func TestSchemaGateWiredOnlyInTeamsMode(t *testing.T) {
	old := tenant.Team{ID: 7, Slug: "acme", Status: tenant.StatusActive, SchemaVersion: "0001_initial_setup.sql"}
	one := tenant.TeamOne("grbpwr_beta")

	off, err := tenantbind.New(one)
	if err != nil {
		t.Fatal(err)
	}
	(&App{c: &config.Config{}, teamOne: one}).useSchemaGate(off)
	if off.SchemaBehind(old) {
		t.Fatal("mode off: a schema predicate was installed")
	}

	on, err := tenantbind.New(one)
	if err != nil {
		t.Fatal(err)
	}
	(&App{c: &config.Config{}, teamOne: one, platform: &platformstore.Store{}}).useSchemaGate(on)
	if !on.SchemaBehind(old) {
		t.Fatal("teams mode: an old team is not behind")
	}
	if on.SchemaBehind(one) {
		t.Fatal("teams mode: team #1 (version NULL) is behind")
	}
}
