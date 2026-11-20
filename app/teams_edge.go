package app

import (
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/schemacompat"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

// useSchemaGate wires the schema gate of the edge (teams mode only). X3: a window, not equality —
// a team is served while its control-plane schema_version is at least the binary's newest non-lazy
// migration and its schema_min_code (newest breaking migration applied) is one this binary embeds;
// otherwise it answers UNAVAILABLE «team is being updated» on admin calls (auth statusGate) and on
// the storefront binding until the fleet migrator records a servable version (it invalidates the
// directory entry; other instances see it within the directory's 15 s cache). So a deploy whose
// migrations are lazy closes nobody, and code one release back serves teams that are ahead by
// additive migrations. The declarations are read once here. Team #1 is always served
// (store.SchemaGate). Mode off: no predicate, every team current.
func (a *App) useSchemaGate(b *tenantbind.Binder) {
	if a.platform == nil {
		return
	}
	set, err := store.LoadSchemaSet()
	if err != nil || set.Latest == "" {
		// Unreachable past the migration lint; then every team but #1 is closed (fail closed).
		slog.Default().Error("teams: embedded team migrations unreadable; every team but #1 answers UNAVAILABLE",
			slog.Any("err", err))
		set = schemacompat.Set{}
	}
	b.UseSchemaCheck(store.SchemaGate(set))
}
