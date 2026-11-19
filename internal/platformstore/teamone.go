package platformstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Team #1 feature profiles (TENANT_TEAM1_PROFILE).
const (
	// Team1ProfileAll: every feature on (team.profile 'all') — today's grbpwr, the default.
	Team1ProfileAll = "all"
	// Team1ProfileProduction: an installation without a shop. The standard default-on set plus the
	// team-#1 administration keys and products (the catalogue the tech cards hang on).
	Team1ProfileProduction = "production"
)

// team1ProductionAdds are the keys the production profile switches on on top of feature.DefaultOn.
func team1ProductionAdds() []feature.Key {
	return []feature.Key{feature.AI, feature.AIProviders, feature.LocalAccounts, feature.Products}
}

// ParseTeam1Profile normalizes TENANT_TEAM1_PROFILE: empty = Team1ProfileAll; an unknown value is
// an error naming the variable.
func ParseTeam1Profile(raw string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case "", Team1ProfileAll:
		return Team1ProfileAll, nil
	case Team1ProfileProduction:
		return Team1ProfileProduction, nil
	default:
		return "", fmt.Errorf("TENANT_TEAM1_PROFILE %q: want %s or %s", raw, Team1ProfileAll, Team1ProfileProduction)
	}
}

// Team1ProfilePlan is what a team #1 profile writes: the team.profile value and the features the
// system switches on with team_feature rows (updated_by NULL). managed lists every key a profile
// may write, so a switch back removes the system rows of the other profile. team.profile is an
// ENUM('standard','all'): production rides on 'standard' plus rows, no migration.
func Team1ProfilePlan(profile string) (dbProfile string, systemOn, managed []feature.Key, err error) {
	p, err := ParseTeam1Profile(profile)
	if err != nil {
		return "", nil, nil, err
	}
	defaults := map[feature.Key]bool{}
	for _, k := range feature.DefaultOn() {
		defaults[k] = true
	}
	for _, k := range team1ProductionAdds() {
		if !defaults[k] {
			managed = append(managed, k)
		}
	}
	if p == Team1ProfileAll {
		return "all", nil, managed, nil
	}
	return "standard", managed, managed, nil
}

// EnsureTeamOne is EnsureTeamOneProfile with Team1ProfileAll (today's behaviour).
func (s *Store) EnsureTeamOne(ctx context.Context, dbName string, admins []TeamOneAdmin) error {
	return s.EnsureTeamOneProfile(ctx, dbName, admins, Team1ProfileAll)
}

// TeamOneAdmin is one row of team #1's admins table as it stands at boot.
type TeamOneAdmin struct {
	Username, PasswordHash string
	Super, Disabled        bool
}

// EnsureTeamOne adopts today's grbpwr database as team #1 (02-DESIGN §13): the team row (slug
// grbpwr, the given profile, active, no file key) and one platform user per admin (handle = username,
// no email, the password hash copied once), owners = supers, members = the rest.
//
// Idempotent and conservative on reruns:
//   - an existing team #1 on another database is an error (team #1 is never re-pointed);
//   - an existing user's password hash is never overwritten (a later change wins);
//   - a disabled admin disables its user; an enabled admin never re-enables a disabled user;
//   - an existing membership is left as it is;
//   - an existing user who is NOT already in team #1 is left alone (no membership, no disable)
//     and logged: the handle belongs to someone who signed up on the platform, and a later admin
//     with the same username must not hand them grbpwr. Boot goes on — a stranger's sign-up must
//     never stop the store;
//   - the profile (Team1ProfilePlan) is applied on every boot, not only when the row is created;
//     a team_feature row an operator wrote (updated_by set) is never overwritten or removed.
func (s *Store) EnsureTeamOneProfile(ctx context.Context, dbName string, admins []TeamOneAdmin, profile string) error {
	if strings.TrimSpace(dbName) == "" {
		return fmt.Errorf("platformstore: team #1 needs its database name")
	}
	dbProfile, systemOn, managed, err := Team1ProfilePlan(profile)
	if err != nil {
		return fmt.Errorf("platformstore: ensure team #1: %w", err)
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
VALUES (?, ?, ?, ?, NULL, ?, 'active', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))`,
			tenant.TeamOneID, tenant.TeamOneSlug, tenant.TeamOneName, dbName, dbProfile); err != nil {
			return fmt.Errorf("platformstore: ensure team #1: insert team: %w", err)
		}
	default:
		return fmt.Errorf("platformstore: ensure team #1: read team: %w", err)
	}
	if err := applyTeamOneProfile(ctx, tx, dbProfile, systemOn, managed); err != nil {
		return err
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
			var member int
			if err := tx.GetContext(ctx, &member, `SELECT COUNT(*) FROM team_member WHERE team_id = ? AND user_id = ?`,
				tenant.TeamOneID, u.ID); err != nil {
				return fmt.Errorf("platformstore: ensure team #1: membership of %s: %w", handle, err)
			}
			if member == 0 {
				slog.Default().ErrorContext(ctx, "platformstore: team #1 admin's handle belongs to a platform user outside team #1; not adopted",
					slog.String("handle", handle))
				continue
			}
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

// applyTeamOneProfile writes team #1's profile inside EnsureTeamOneProfile's transaction. System
// rows (updated_by NULL) follow the profile; operator rows (updated_by set) are left as they are.
func applyTeamOneProfile(ctx context.Context, tx execer, dbProfile string, systemOn, managed []feature.Key) error {
	if _, err := tx.ExecContext(ctx, `UPDATE team SET profile = ? WHERE id = ?`, dbProfile, tenant.TeamOneID); err != nil {
		return fmt.Errorf("platformstore: ensure team #1: profile: %w", err)
	}
	on := map[feature.Key]bool{}
	for _, k := range systemOn {
		on[k] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO team_feature (team_id, feature, enabled, updated_by, updated_at)
VALUES (?, ?, 1, NULL, UTC_TIMESTAMP(3)) AS nv
ON DUPLICATE KEY UPDATE
  enabled = IF(team_feature.updated_by IS NULL, nv.enabled, team_feature.enabled),
  updated_at = IF(team_feature.updated_by IS NULL, nv.updated_at, team_feature.updated_at)`,
			tenant.TeamOneID, string(k)); err != nil {
			return fmt.Errorf("platformstore: ensure team #1: feature %s: %w", k, err)
		}
	}
	for _, k := range managed {
		if on[k] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM team_feature WHERE team_id = ? AND feature = ? AND updated_by IS NULL`,
			tenant.TeamOneID, string(k)); err != nil {
			return fmt.Errorf("platformstore: ensure team #1: clear feature %s: %w", k, err)
		}
	}
	return nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
