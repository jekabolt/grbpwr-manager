// Package teamsync mirrors a team member onto the team database's admins table.
//
// Every team database keeps one admins row per member (02-DESIGN §3): username = the platform
// handle, is_super = owner, password = an unusable random value (members sign in through the
// platform, never with a team-local password), admin_permission = the member's section RBAC.
//
// The ctx must carry the team: the write goes to that team's database through the routing pool.
// A ctx without a team is refused here, before any statement, whatever the teams mode.
package teamsync

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// unusablePrefix marks a password hash nothing can match: pwhash.Validate needs five '$' fields.
const unusablePrefix = "!unusable$"

// EnsureAdmin creates or updates the member's admins row in the ctx team's database: present,
// enabled, with this super flag and these permissions (ignored for a super).
func EnsureAdmin(ctx context.Context, admins dependency.Admin, handle string, super bool, perms []entity.AdminPermission) error {
	if _, ok := tenant.From(ctx); !ok {
		return tenant.ErrNoTenant
	}
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return errors.New("teamsync: empty handle")
	}
	cur, err := admins.GetAdminByUsername(ctx, handle)
	switch {
	case err == nil:
		if err := admins.SetAccountPermissions(ctx, handle, super, perms); err != nil {
			return fmt.Errorf("teamsync: set permissions of %s: %w", handle, err)
		}
		if cur.Disabled {
			if err := admins.SetAccountDisabled(ctx, handle, false); err != nil {
				return fmt.Errorf("teamsync: enable %s: %w", handle, err)
			}
		}
		return nil
	case errors.Is(err, sql.ErrNoRows):
		hash, err := unusableHash()
		if err != nil {
			return err
		}
		if err := admins.AddAccount(ctx, handle, hash, super, perms); err != nil {
			return fmt.Errorf("teamsync: add %s: %w", handle, err)
		}
		return nil
	default:
		return fmt.Errorf("teamsync: read %s: %w", handle, err)
	}
}

// DisableAdmin disables the member's admins row in the ctx team's database. A member with no row
// is already locked out: no error.
func DisableAdmin(ctx context.Context, admins dependency.Admin, handle string) error {
	if _, ok := tenant.From(ctx); !ok {
		return tenant.ErrNoTenant
	}
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return errors.New("teamsync: empty handle")
	}
	cur, err := admins.GetAdminByUsername(ctx, handle)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("teamsync: read %s: %w", handle, err)
	}
	if cur.Disabled {
		return nil
	}
	if err := admins.SetAccountDisabled(ctx, handle, true); err != nil {
		return fmt.Errorf("teamsync: disable %s: %w", handle, err)
	}
	return nil
}

func unusableHash() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("teamsync: random: %w", err)
	}
	return unusablePrefix + hex.EncodeToString(b), nil
}
