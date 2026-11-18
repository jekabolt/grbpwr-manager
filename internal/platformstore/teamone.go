package platformstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// TeamOneAdmin is one row of team #1's admins table as it stands at boot.
type TeamOneAdmin struct {
	Username, PasswordHash string
	Super, Disabled        bool
}

// EnsureTeamOne adopts today's grbpwr database as team #1 (02-DESIGN §13): the team row (slug
// grbpwr, profile all, active, no file key) and one platform user per admin (handle = username,
// no email, the password hash copied once), owners = supers, members = the rest.
//
// Idempotent and conservative on reruns:
//   - an existing team #1 on another database is an error (team #1 is never re-pointed);
//   - an existing user's password hash is never overwritten (a later change wins);
//   - a disabled admin disables its user; an enabled admin never re-enables a disabled user;
//   - an existing membership is left as it is.
func (s *Store) EnsureTeamOne(ctx context.Context, dbName string, admins []TeamOneAdmin) error {
	if strings.TrimSpace(dbName) == "" {
		return fmt.Errorf("platformstore: team #1 needs its database name")
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platformstore: ensure team #1: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var cur struct {
		Slug   string         `db:"slug"`
		DBName sql.NullString `db:"db_name"`
	}
	err = tx.GetContext(ctx, &cur, `SELECT slug, db_name FROM team WHERE id = ? FOR UPDATE`, tenant.TeamOneID)
	switch {
	case err == nil:
		if cur.DBName.String != dbName {
			return fmt.Errorf("platformstore: team #1 is on database %q, boot asked for %q", cur.DBName.String, dbName)
		}
	case isNoRows(err):
		if _, err := tx.ExecContext(ctx, `INSERT INTO team
  (id, slug, name, db_name, file_key, profile, status, created_at, activated_at)
VALUES (?, ?, ?, ?, NULL, 'all', 'active', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))`,
			tenant.TeamOneID, tenant.TeamOneSlug, tenant.TeamOneName, dbName); err != nil {
			return fmt.Errorf("platformstore: ensure team #1: insert team: %w", err)
		}
	default:
		return fmt.Errorf("platformstore: ensure team #1: read team: %w", err)
	}

	for _, a := range admins {
		handle := strings.TrimSpace(a.Username)
		if handle == "" || len(handle) > 64 {
			return fmt.Errorf("platformstore: ensure team #1: admin handle %q does not fit", a.Username)
		}
		var u struct {
			ID     int64  `db:"id"`
			Status string `db:"status"`
		}
		err := tx.GetContext(ctx, &u, `SELECT id, status FROM platform_user WHERE handle = ? FOR UPDATE`, handle)
		switch {
		case err == nil:
			if a.Disabled && u.Status != "disabled" {
				if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET status = 'disabled' WHERE id = ?`, u.ID); err != nil {
					return fmt.Errorf("platformstore: ensure team #1: disable %s: %w", handle, err)
				}
			}
		case isNoRows(err):
			if a.PasswordHash == "" {
				return fmt.Errorf("platformstore: ensure team #1: admin %s has no password hash", handle)
			}
			status := "active"
			if a.Disabled {
				status = "disabled"
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO platform_user (email, handle, password_hash, status, created_at)
VALUES (NULL, ?, ?, ?, UTC_TIMESTAMP(3))`, handle, a.PasswordHash, status)
			if err != nil {
				return fmt.Errorf("platformstore: ensure team #1: insert user %s: %w", handle, err)
			}
			if u.ID, err = res.LastInsertId(); err != nil {
				return fmt.Errorf("platformstore: ensure team #1: user id %s: %w", handle, err)
			}
		default:
			return fmt.Errorf("platformstore: ensure team #1: read user %s: %w", handle, err)
		}

		role := "member"
		if a.Super {
			role = "owner"
		}
		if _, err := tx.ExecContext(ctx, `INSERT IGNORE INTO team_member (team_id, user_id, role, status, created_at)
VALUES (?, ?, ?, 'active', UTC_TIMESTAMP(3))`, tenant.TeamOneID, u.ID, role); err != nil {
			return fmt.Errorf("platformstore: ensure team #1: member %s: %w", handle, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platformstore: ensure team #1: commit: %w", err)
	}
	s.dir.Invalidate(tenant.TeamOneID)
	return nil
}
