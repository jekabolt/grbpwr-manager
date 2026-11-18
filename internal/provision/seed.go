package provision

import (
	"context"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/provision/seed"
	"github.com/jekabolt/grbpwr-manager/internal/teamsync"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// workshopDefaults is the singleton row of workshop_settings: every column NULL = "not configured
// yet", the state the migration seeds. Kept here too so a scrub can never leave a team without it.
const workshopDefaults = `INSERT IGNORE INTO workshop_settings (id) VALUES (1)`

// Seed prepares a freshly migrated team database for its owner, through the routing pool under the
// team in ctx. Idempotent: rerunning after a crash at any point converges on the same rows.
//
//  1. scrub: delete grbpwr's brand rows the migration chain seeded (seed/scrub.sql);
//  2. workshop_settings defaults;
//  3. the owner's admins row (super), via teamsync.EnsureAdmin.
//
// The owner comes last, so a scrub statement added later (say, one that empties seeded people) can
// never remove the owner on a rerun. Team #1 is refused before any statement. Seed runs only on a
// team the directory shows provisioning, and guard (required: the worker's lease check, see
// platformstore.CheckProvisionLease) must pass before every statement — a live team is never
// scrubbed, whoever calls.
func Seed(ctx context.Context, db dependency.DB, admins dependency.Admin, ownerHandle string, guard func(context.Context) error) error {
	t, ok := tenant.From(ctx)
	if !ok {
		return tenant.ErrNoTenant
	}
	if t.ID == tenant.TeamOneID {
		return errors.New("provision: seed refused on team #1")
	}
	if t.Status != tenant.StatusProvisioning {
		return fmt.Errorf("provision: seed refused: team %d is %q, not provisioning", t.ID, t.Status)
	}
	if guard == nil {
		return errors.New("provision: seed needs the provisioning lease guard")
	}
	if ownerHandle == "" {
		return errors.New("provision: seed needs the owner's handle")
	}
	check := func(step string) error {
		if err := guard(ctx); err != nil {
			return fmt.Errorf("provision: seed stopped before %s: %w", step, err)
		}
		return nil
	}
	for i, q := range seed.Statements() {
		if err := check(fmt.Sprintf("scrub statement %d", i+1)); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("provision: scrub statement %d: %w", i+1, err)
		}
	}
	if err := check("workshop defaults"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, workshopDefaults); err != nil {
		return fmt.Errorf("provision: workshop defaults: %w", err)
	}
	if err := check("the owner"); err != nil {
		return err
	}
	if err := teamsync.EnsureAdmin(ctx, admins, ownerHandle, true, nil); err != nil {
		return fmt.Errorf("provision: owner: %w", err)
	}
	return nil
}
