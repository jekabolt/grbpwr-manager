package app

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

// useSchemaGate wires the schema gate of the edge (teams mode only): a team whose control-plane
// schema_version is not the binary's latest migration answers UNAVAILABLE «team is being updated»
// on admin calls (auth statusGate) and on the storefront binding until the fleet migrator records
// the new version (it invalidates the directory entry; other instances see it within the
// directory's 15 s cache). The latest migration is read once here. Team #1 is current by
// construction (store.SchemaBehind): its version stays NULL in the control plane while store.New
// has migrated its database before the server listens. Mode off: no predicate, every team current.
func (a *App) useSchemaGate(b *tenantbind.Binder) {
	if a.platform == nil {
		return
	}
	latest := store.LatestMigrationID()
	if latest == "" {
		// Unreachable with the embedded set; then every team but #1 is behind (fail closed).
		slog.Default().Error("teams: no embedded team migrations; every team but #1 answers UNAVAILABLE")
	}
	b.UseSchemaCheck(store.SchemaBehind(latest))
}
