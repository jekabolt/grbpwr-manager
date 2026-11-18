package provision

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/provision/seed"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// workshopDefaults is the singleton row of workshop_settings: every column NULL = "not configured
// yet", the state the migration seeds. Kept here too so a scrub can never leave a team without it.
const workshopDefaults = `INSERT IGNORE INTO workshop_settings (id) VALUES (1)`

// ownerUpsert makes the owner's admins row a super, enabled, with a password hash nothing can match
// (the owner signs in through the platform, never with a team password). The same row
// teamsync.EnsureAdmin(super) leaves; written here on the lock's session so it is fenced too.
const ownerUpsert = `INSERT INTO admins (username, password_hash, is_super) VALUES (?, ?, TRUE)
	ON DUPLICATE KEY UPDATE is_super = TRUE, disabled = FALSE`

// ownerPerms: a super has no per-section rows (teamsync.EnsureAdmin clears them the same way).
const ownerPerms = `DELETE FROM admin_permission WHERE admin_id IN (SELECT id FROM admins WHERE username = ?)`

// unusableOwnerHash marks a password hash nothing can match: pwhash.Validate needs five '$' fields
// (the same shape as teamsync's).
func unusableOwnerHash() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("provision: random: %w", err)
	}
	return "!unusable$" + hex.EncodeToString(b), nil
}

// Execer runs one statement (the database lock's session, or the routing pool in tests).
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Seed prepares a freshly migrated team database for its owner. Every statement runs through db —
// the worker passes its database lock, so scrub, defaults and the owner's admin row all run on the
// lock's own session and fail once it is gone. Idempotent: rerunning after a crash at any point
// converges on the same rows.
//
//  1. scrub: delete grbpwr's brand rows the migration chain seeded (seed/scrub.sql);
//  2. workshop_settings defaults;
//  3. the owner's admins row (super, enabled, no usable password) and no per-section rows.
//
// The owner comes last, so a scrub statement added later (say, one that empties seeded people) can
// never remove the owner on a rerun. Team #1 is refused before any statement. Seed runs only on a
// team the directory shows provisioning, and guard (required: the worker's lease check, see
// platformstore.CheckProvisionLease plus the database lock still held) must pass before every
// statement — a live team is never scrubbed, whoever calls.
func Seed(ctx context.Context, db Execer, ownerHandle string, guard func(context.Context) error) error {
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
	ownerHandle = strings.TrimSpace(ownerHandle)
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
	hash, err := unusableOwnerHash()
	if err != nil {
		return err
	}
	if err := check("the owner"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, ownerUpsert, ownerHandle, hash); err != nil {
		return fmt.Errorf("provision: owner: %w", err)
	}
	if err := check("the owner's permissions"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, ownerPerms, ownerHandle); err != nil {
		return fmt.Errorf("provision: owner permissions: %w", err)
	}
	return nil
}
