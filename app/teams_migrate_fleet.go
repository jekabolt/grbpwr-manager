package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/config"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/provision"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// FleetRun is what MigrateFleet did.
type FleetRun struct {
	TeamsMode bool // false: only team #1 was migrated
	provision.FleetReport
}

// MigrateFleet is the pre-deploy migration job (`products-manager migrate-fleet`, X3): it migrates
// team #1 exactly as a boot would (store.New, MYSQL_AUTOMIGRATE), then — teams mode on — every team
// through the same fleet code the running app uses (canary, bounded parallel, per-database lease and
// lock), and returns. It opens no HTTP listener, starts no worker and writes nothing else. A failed
// team is in the report, never an error; err means the run itself could not happen.
func MigrateFleet(ctx context.Context, c *config.Config) (FleetRun, error) {
	var run FleetRun
	a := New(c)
	teamOne, err := a.teamOneFromConfig()
	if err != nil {
		return run, err
	}
	ctx = tenant.With(ctx, teamOne)
	db, err := store.New(ctx, c.DB)
	if err != nil {
		return run, fmt.Errorf("team #1: %w", err)
	}
	defer db.Close()
	if !c.Platform.Enabled() {
		return run, nil
	}
	run.TeamsMode = true
	if err := provision.CheckPrefix(c.Platform.DBPrefix, teamOne.DBName); err != nil {
		return run, err
	}
	pf, err := platformstore.Open(ctx, c.Platform.DSN)
	if err != nil {
		return run, fmt.Errorf("control plane: %w", err)
	}
	defer pf.Close()
	if db.DB() == nil {
		return run, errors.New("team #1: no database handle")
	}
	w, err := provision.New(provision.Config{
		Prefix:  c.Platform.DBPrefix,
		Team1DB: teamOne.DBName,
		Spares:  c.Platform.Spares,
		PerHour: c.Platform.ProvisionPerHour,
	}, pf, provision.StoreSchema{BaseDSN: c.DB.DSN, Prefix: c.Platform.DBPrefix}, db.DB())
	if err != nil {
		return run, err
	}
	run.FleetReport, err = w.MigrateFleet(ctx)
	return run, err
}
