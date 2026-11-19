package tenantbind

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// schemaBinder is a teams-mode binder with the predicate app.useSchemaGate wires at boot
// (store.SchemaBehind over LatestMigrationID read once), team #1 in the control plane with
// schema_version NULL (as on lekalo beta 19.11), and acme (team 7) at an old version.
func schemaBinder(t *testing.T, storefront int64) (*Binder, *fakePlane, string) {
	t.Helper()
	latest := store.LatestMigrationID()
	if latest == "" {
		t.Fatal("no embedded team migrations")
	}
	b := newBinder(t)
	one := teamOne
	one.SchemaVersion = "" // NULL: nothing records team #1's version
	old := acme
	old.SchemaVersion = "0001_initial_setup.sql"
	p := &fakePlane{dir: &mapDir{teams: map[int64]tenant.Team{1: one, 7: old}}}
	b.usePlane(p, platformstore.Config{BaseDomain: "beta.lekalo.studio", StorefrontTeamID: storefront})
	b.UseSchemaCheck(store.SchemaBehind(latest))
	return b, p, latest
}

// Team #1 trap: right after boot team #1's control-plane version is NULL while its database is
// current (store.New migrated it). It must be reachable on every path: by id, by its own host, on
// the storefront, and even as the directory's own row.
//
// MUTATION: store.SchemaBehind without the team #1 rule and Binder.SchemaBehind without its
// short-circuit → red.
func TestSchemaGateTeamOneWithNullVersionReachable(t *testing.T) {
	b, p, _ := schemaBinder(t, tenant.TeamOneID)
	ctx := context.Background()
	byID, err := b.TeamByID(ctx, tenant.TeamOneID)
	if err != nil || b.SchemaBehind(byID) {
		t.Fatalf("team #1 by id: behind=%v err=%v", b.SchemaBehind(byID), err)
	}
	byHost, named, err := b.RequestedTeam(ctx, "grbpwr.beta.lekalo.studio", "")
	if err != nil || !named || b.SchemaBehind(byHost) {
		t.Fatalf("team #1 by host: named=%v behind=%v err=%v", named, b.SchemaBehind(byHost), err)
	}
	if store.SchemaBehind(store.LatestMigrationID())(p.dir.teams[1]) {
		t.Fatal("team #1's control-plane row (version NULL) judged behind")
	}
	if _, err := b.bindFor(ctx, ClassStorefront); err != nil {
		t.Fatalf("team #1 storefront: %v", err)
	}
}

// A non-#1 team at an old version is refused (UNAVAILABLE on the storefront, behind for the admin
// status gate) and becomes reachable — same binder, no restart — once the fleet migrator records
// the binary's version (platformstore.SetTeamSchema writes it and invalidates the directory entry).
func TestSchemaGateOldTeamRefusedUntilFleetRecordsVersion(t *testing.T) {
	b, p, latest := schemaBinder(t, 7)
	ctx := context.Background()
	tm, err := b.TeamByID(ctx, 7)
	if err != nil || !b.SchemaBehind(tm) {
		t.Fatalf("old team: behind=%v err=%v, want behind", b.SchemaBehind(tm), err)
	}
	if _, err := b.bindFor(ctx, ClassStorefront); status.Code(err) != codes.Unavailable {
		t.Fatalf("storefront on an old team: %v, want Unavailable", err)
	}

	migrated := p.dir.teams[7]
	migrated.SchemaVersion = latest
	p.dir.teams[7] = migrated
	p.dir.Invalidate(7)

	tm, err = b.TeamByID(ctx, 7)
	if err != nil || b.SchemaBehind(tm) {
		t.Fatalf("migrated team: behind=%v err=%v, want current", b.SchemaBehind(tm), err)
	}
	if _, err := b.bindFor(ctx, ClassStorefront); err != nil {
		t.Fatalf("storefront after the fleet recorded the version: %v", err)
	}
}
