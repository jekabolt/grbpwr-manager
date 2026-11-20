package provision

import (
	"context"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// SetSeedForTest replaces the seeder (unit tests without a database).
// The worker still checks its lease guard before and after it, as it does around Seed.
func (w *Worker) SetSeedForTest(f func(ctx context.Context, ownerHandle string) error) {
	w.seed = func(ctx context.Context, _ Execer, owner string, _ func(context.Context) error) error {
		return f(ctx, owner)
	}
}

// SetSeedExecForTest replaces the seeder with one that sees the database lock and the guard.
func (w *Worker) SetSeedExecForTest(f func(ctx context.Context, exec Execer, ownerHandle string, guard func(context.Context) error) error) {
	w.seed = f
}

// SeedAndActivateForTest runs the seed + activate step as a worker holding token would.
func (w *Worker) SeedAndActivateForTest(ctx context.Context, t platformstore.ProvisioningTeam, token, dbName string, claimed bool) error {
	return w.seedAndActivate(ctx, t, token, dbName, claimed)
}

// SetNowForTest replaces the clock.
func (w *Worker) SetNowForTest(now func() time.Time) { w.now = now }

// SetLoggerForTest replaces the logger (X3: count the fleet's error events).
func (w *Worker) SetLoggerForTest(l *slog.Logger) { w.log = l }
