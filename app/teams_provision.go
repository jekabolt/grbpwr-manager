package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/provision"
	"github.com/jekabolt/grbpwr-manager/internal/store"
)

// startProvisioner starts the team provisioner (spare databases, new teams, fleet migrator, pending
// sweep). Mode off (a.platform nil) it does nothing and reads no teams variable. Teams mode on, any
// refusal — TENANT_DB_PREFIX missing or rejected, no MySQL store — is a boot error: a healthy boot
// without the keeper, claimer, fleet migrator and sweep would leave new teams in provisioning and
// team schemas behind forever.
func (a *App) startProvisioner(ctx context.Context) (stop func(), err error) {
	noop := func() {}
	if a.platform == nil {
		return noop, nil
	}
	if err := provision.CheckPrefix(a.c.Platform.DBPrefix, a.teamOne.DBName); err != nil {
		return nil, err
	}
	ms, ok := a.db.(*store.MYSQLStore)
	if !ok {
		return nil, errors.New("provisioner: the repository is not the MySQL store")
	}
	w, err := provision.New(provision.Config{
		Prefix:  a.c.Platform.DBPrefix,
		Team1DB: a.teamOne.DBName,
		Spares:  a.c.Platform.Spares,
		PerHour: a.c.Platform.ProvisionPerHour,
	}, a.platform, provision.StoreSchema{BaseDSN: a.c.DB.DSN, Prefix: a.c.Platform.DBPrefix}, ms.DB())
	if err != nil {
		return nil, fmt.Errorf("provisioner: %w", err)
	}
	return w.Start(ctx), nil
}
