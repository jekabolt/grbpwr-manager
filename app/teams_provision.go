package app

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/provision"
	"github.com/jekabolt/grbpwr-manager/internal/store"
)

// startProvisioner starts the team provisioner (spare databases, new teams, fleet migrator, pending
// sweep). Called only with teams mode on (a.platform set). Without TENANT_DB_PREFIX no new team can
// get a database: the provisioner stays off and says so, boot goes on (team #1 never depends on it); so does any
// other refusal of the provisioner's config.
func (a *App) startProvisioner(ctx context.Context) (stop func(), err error) {
	noop := func() {}
	if a.platform == nil {
		return noop, nil
	}
	if a.c.Platform.DBPrefix == "" {
		slog.Default().ErrorContext(ctx, "provisioner off: TENANT_DB_PREFIX is empty; new teams stay in provisioning")
		return noop, nil
	}
	ms, ok := a.db.(*store.MYSQLStore)
	if !ok {
		slog.Default().ErrorContext(ctx, "provisioner off: the repository is not the MySQL store")
		return noop, nil
	}
	w, err := provision.New(provision.Config{
		Prefix:  a.c.Platform.DBPrefix,
		Team1DB: a.teamOne.DBName,
		Spares:  a.c.Platform.Spares,
		PerHour: a.c.Platform.ProvisionPerHour,
	}, a.platform, provision.StoreSchema{BaseDSN: a.c.DB.DSN, Prefix: a.c.Platform.DBPrefix}, ms.DB(), ms.Admin())
	if err != nil {
		// A bad prefix or team #1 name refuses the provisioner, never the boot: team #1 serves on.
		slog.Default().ErrorContext(ctx, "provisioner off", slog.String("err", err.Error()))
		return noop, nil
	}
	return w.Start(ctx), nil
}
