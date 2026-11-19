package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

// Console errors (PlatformService maps them to FAILED_PRECONDITION / NOT_FOUND).
var (
	// ErrTeamNotFound: no team with that slug.
	ErrTeamNotFound = errors.New("platformstore: team not found")
	// ErrWrongTeamStatus: the team is not in the status the operation starts from.
	ErrWrongTeamStatus = errors.New("platformstore: wrong team status")
)

// SlugRedirect is how long a retired slug keeps redirecting to its team after a rename.
const SlugRedirect = 90 * 24 * time.Hour

// DeleteRetention is how long a deleted team's data is kept (and RestoreTeam can bring it back)
// before its wipe is due (team.purge_at).
const DeleteRetention = 30 * 24 * time.Hour

// ConsoleTeam is one team as the operator's console lists it. ID never leaves the server.
type ConsoleTeam struct {
	ID        int64     `db:"id"`
	Slug      string    `db:"slug"`
	Name      string    `db:"name"`
	Profile   string    `db:"profile"`
	Status    string    `db:"status"`
	People    int       `db:"people"`
	CreatedAt time.Time `db:"created_at"`
	// SchemaOK: the team's fingerprint equals the reference schema of its version (an unknown
	// reference or an unrecorded fingerprint counts as ok).
	SchemaOK bool `db:"-"`
	// Overrides are the team's explicit feature switches (team_feature).
	Overrides map[string]bool `db:"-"`

	SchemaVersion     sql.NullString `db:"schema_version"`
	SchemaFingerprint sql.NullString `db:"schema_fingerprint"`
	// PurgeAt is set only while the team is deleted: the moment its data is wiped.
	PurgeAt sql.NullTime `db:"purge_at"`
}

const consoleTeamSelect = `SELECT t.id, t.slug, t.name, t.profile, t.status, t.created_at, t.schema_version, t.schema_fingerprint, t.purge_at,
 (SELECT COUNT(*) FROM team_member m WHERE m.team_id = t.id AND m.status = 'active') AS people
FROM team t`

// ConsoleTeams lists every team that is not deleted, then the deleted teams whose data is not wiped
// yet (purge_at set), filtered by a slug / name substring (empty = all): team #1 first, then newest
// first, the deleted ones last (newest first among them). A fixed three queries whatever the number
// of teams: teams with their people count, every feature override, the reference fingerprints.
func (s *Store) ConsoleTeams(ctx context.Context, query string) ([]ConsoleTeam, error) {
	q := consoleTeamSelect + ` WHERE (t.status <> 'deleted' OR t.purge_at IS NOT NULL)`
	var args []any
	if query = strings.ToLower(strings.TrimSpace(query)); query != "" {
		like := "%" + escapeLike(query) + "%"
		q += ` AND (t.slug LIKE ? ESCAPE '!' OR LOWER(t.name) LIKE ? ESCAPE '!')`
		args = append(args, like, like)
	}
	q += ` ORDER BY t.status = 'deleted', t.id = 1 DESC, t.created_at DESC, t.id DESC`
	var teams []ConsoleTeam
	if err := s.db.SelectContext(ctx, &teams, q, args...); err != nil {
		return nil, fmt.Errorf("platformstore: console teams: %w", err)
	}
	if len(teams) == 0 {
		return teams, nil
	}
	if err := s.fillConsole(ctx, teams); err != nil {
		return nil, err
	}
	return teams, nil
}

// ConsoleTeam returns one team by its live slug for the console's actions; it never returns a
// deleted team: ErrWrongTeamStatus for one (every action but DeleteTeam / RestoreTeam / GetTeam
// answers wrong_status on it), ErrTeamNotFound when there is no team with the slug.
func (s *Store) ConsoleTeam(ctx context.Context, slug string) (ConsoleTeam, error) {
	t, err := s.ConsoleTeamWithDeleted(ctx, slug)
	if err == nil && t.Status == string(tenant.StatusDeleted) {
		return ConsoleTeam{}, ErrWrongTeamStatus
	}
	return t, err
}

// ConsoleTeamWithDeleted is ConsoleTeam that also returns a deleted team (DeleteTeam, RestoreTeam,
// GetTeam). ErrTeamNotFound when there is no team with the slug.
func (s *Store) ConsoleTeamWithDeleted(ctx context.Context, slug string) (ConsoleTeam, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return ConsoleTeam{}, ErrTeamNotFound
	}
	return s.consoleTeamWhere(ctx, `t.slug = ?`, slug)
}

// ConsoleTeamByID is ConsoleTeamWithDeleted by the team's id, for answering an action that already
// committed by id (DeleteTeam, RestoreTeam): a rename in between does not lose the team.
func (s *Store) ConsoleTeamByID(ctx context.Context, teamID int64) (ConsoleTeam, error) {
	return s.consoleTeamWhere(ctx, `t.id = ?`, teamID)
}

func (s *Store) consoleTeamWhere(ctx context.Context, where string, arg any) (ConsoleTeam, error) {
	var t ConsoleTeam
	if err := s.db.GetContext(ctx, &t, consoleTeamSelect+` WHERE `+where, arg); err != nil {
		if isNoRows(err) {
			return ConsoleTeam{}, ErrTeamNotFound
		}
		return ConsoleTeam{}, fmt.Errorf("platformstore: console team: %w", err)
	}
	teams := []ConsoleTeam{t}
	if err := s.fillConsole(ctx, teams); err != nil {
		return ConsoleTeam{}, err
	}
	return teams[0], nil
}

func consoleIDs(teams []ConsoleTeam) []int64 {
	ids := make([]int64, 0, len(teams))
	for _, t := range teams {
		ids = append(ids, t.ID)
	}
	return ids
}

// AISpendFor sums this span's AI spend (from day on, inclusive) of the given teams only, keyed by
// team id; a team without rows is absent. No ids, no query.
func (s *Store) AISpendFor(ctx context.Context, day time.Time, teamIDs []int64) (map[int64]AISpend, error) {
	out := make(map[int64]AISpend, len(teamIDs))
	if len(teamIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		TeamID   int64           `db:"team_id"`
		CostUSD  decimal.Decimal `db:"cost_usd"`
		Unpriced int64           `db:"unpriced_calls"`
		Calls    int64           `db:"calls"`
	}
	q, args, err := sqlx.In(`SELECT team_id, COALESCE(SUM(cost_usd), 0) AS cost_usd,
       CAST(COALESCE(SUM(unpriced_calls), 0) AS SIGNED) AS unpriced_calls, CAST(COALESCE(SUM(calls), 0) AS SIGNED) AS calls
FROM team_ai_spend_daily WHERE team_id IN (?) AND day >= ? GROUP BY team_id`, teamIDs, day.Format(dayLayout))
	if err != nil {
		return nil, fmt.Errorf("platformstore: ai spend for teams: %w", err)
	}
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(q), args...); err != nil {
		return nil, fmt.Errorf("platformstore: ai spend for teams since %s: %w", day.Format(dayLayout), err)
	}
	for _, r := range rows {
		out[r.TeamID] = AISpend{CostUSD: r.CostUSD, Unpriced: r.Unpriced, Calls: r.Calls}
	}
	return out, nil
}

// fillConsole sets Overrides and SchemaOK on teams in two queries; the override read covers only
// these teams.
func (s *Store) fillConsole(ctx context.Context, teams []ConsoleTeam) error {
	var rows []struct {
		TeamID  int64  `db:"team_id"`
		Feature string `db:"feature"`
		Enabled bool   `db:"enabled"`
	}
	q, args, err := sqlx.In(`SELECT team_id, feature, enabled FROM team_feature WHERE team_id IN (?)`, consoleIDs(teams))
	if err != nil {
		return fmt.Errorf("platformstore: console features: %w", err)
	}
	if err := s.db.SelectContext(ctx, &rows, s.db.Rebind(q), args...); err != nil {
		return fmt.Errorf("platformstore: console features: %w", err)
	}
	over := make(map[int64]map[string]bool, len(teams))
	for _, r := range rows {
		if over[r.TeamID] == nil {
			over[r.TeamID] = map[string]bool{}
		}
		over[r.TeamID][r.Feature] = r.Enabled
	}

	var refs []struct {
		Version string `db:"schema_version"`
		FP      string `db:"schema_fingerprint"`
	}
	// The same pick as ReferenceSpare, for every version at once: ready before claimed, oldest first.
	if err := s.db.SelectContext(ctx, &refs, `SELECT schema_version, schema_fingerprint FROM spare_db
WHERE status IN ('ready','claimed') AND schema_version IS NOT NULL AND schema_fingerprint IS NOT NULL
ORDER BY status = 'ready' DESC, created_at`); err != nil {
		return fmt.Errorf("platformstore: reference fingerprints: %w", err)
	}
	ref := make(map[string]string, len(refs))
	for _, r := range refs {
		if _, seen := ref[r.Version]; !seen {
			ref[r.Version] = r.FP
		}
	}
	for i := range teams {
		t := &teams[i]
		t.Overrides = over[t.ID]
		if t.Overrides == nil {
			t.Overrides = map[string]bool{}
		}
		t.SchemaOK = true
		if t.SchemaVersion.Valid && t.SchemaFingerprint.Valid {
			if fp, ok := ref[t.SchemaVersion.String]; ok {
				t.SchemaOK = fp == t.SchemaFingerprint.String
			}
		}
	}
	return nil
}

// escapeLike escapes LIKE wildcards with '!'.
func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}

// ConsoleMember is one active member as the console shows it.
type ConsoleMember struct {
	UserID     int64          `db:"user_id"`
	Handle     string         `db:"handle"`
	Email      sql.NullString `db:"email"`
	Role       string         `db:"role"`
	LastSeenAt sql.NullTime   `db:"last_seen_at"`
}

// TeamMembers lists the team's active members, owners first, with the last time any of their
// sessions in this team was seen.
func (s *Store) TeamMembers(ctx context.Context, teamID int64) ([]ConsoleMember, error) {
	var out []ConsoleMember
	if err := s.db.SelectContext(ctx, &out, `SELECT m.user_id, u.handle, u.email, m.role,
 (SELECT MAX(us.last_seen_at) FROM user_session us WHERE us.user_id = m.user_id AND us.team_id = m.team_id) AS last_seen_at
FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND m.status = 'active'
ORDER BY m.role = 'owner' DESC, u.handle`, teamID); err != nil {
		return nil, fmt.Errorf("platformstore: team members: %w", err)
	}
	return out, nil
}

// SetTeamReadOnly moves a team active → suspended (readOnly) or back. Any other status, team #1
// included by the caller's rule, is ErrWrongTeamStatus. The directory entry is dropped.
func (s *Store) SetTeamReadOnly(ctx context.Context, teamID int64, readOnly bool) error {
	from, to := "suspended", "active"
	if readOnly {
		from, to = "active", "suspended"
	}
	var st string
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &st, `SELECT status FROM team WHERE id = ? FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: lock team: %w", err)
		}
		if st == to {
			return nil // already there: idempotent
		}
		if st != from {
			return ErrWrongTeamStatus
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team SET status = ? WHERE id = ?`, to, teamID); err != nil {
			return fmt.Errorf("platformstore: set team status: %w", err)
		}
		return nil
	})
	s.dir.Invalidate(teamID)
	return err
}

// RenameTeam changes a team's slug and / or name. A changed slug must be free in team and
// team_slug_history (a retired slug is never reused, whatever its redirect window); the old one
// goes to the history, redirecting until now + SlugRedirect. newSlug "" or equal keeps the slug.
// The caller validated both values. ErrSlugTaken, ErrTeamNotFound, ErrWrongTeamStatus (review,
// pending, deleted).
func (s *Store) RenameTeam(ctx context.Context, teamID int64, newSlug, newName string, now time.Time) error {
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var cur struct {
			Slug   string `db:"slug"`
			Status string `db:"status"`
		}
		if err := tx.GetContext(ctx, &cur, `SELECT slug, status FROM team WHERE id = ? FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: lock team: %w", err)
		}
		switch cur.Status {
		case "provisioning", "active", "suspended":
		default:
			return ErrWrongTeamStatus
		}
		if newSlug != "" && newSlug != cur.Slug {
			var n int
			if err := tx.GetContext(ctx, &n, `SELECT
 (SELECT COUNT(*) FROM team WHERE slug = ?) + (SELECT COUNT(*) FROM team_slug_history WHERE slug = ?)`, newSlug, newSlug); err != nil {
				return fmt.Errorf("platformstore: slug free: %w", err)
			}
			if n > 0 {
				return ErrSlugTaken
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO team_slug_history (slug, team_id, retired_at, redirect_until)
VALUES (?, ?, ?, ?)`, cur.Slug, teamID, now, now.Add(SlugRedirect)); err != nil {
				if dupKey(err, "PRIMARY") {
					return ErrSlugTaken
				}
				return fmt.Errorf("platformstore: retire slug: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE team SET slug = ? WHERE id = ?`, newSlug, teamID); err != nil {
				if dupKey(err, "uq_team_slug") {
					return ErrSlugTaken
				}
				return fmt.Errorf("platformstore: set slug: %w", err)
			}
		}
		if newName != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE team SET name = ? WHERE id = ?`, newName, teamID); err != nil {
				return fmt.Errorf("platformstore: set name: %w", err)
			}
		}
		return nil
	})
	s.dir.Invalidate(teamID)
	return err
}

// ApproveTeam moves a team review → provisioning and queues the team_approved letter to its owner
// (link = the team's own address) in the same transaction. ErrWrongTeamStatus from any other status.
func (s *Store) ApproveTeam(ctx context.Context, teamID int64, link string, now time.Time) error {
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE team SET status = 'provisioning' WHERE id = ? AND status = 'review'`, teamID)
		if err != nil {
			return fmt.Errorf("platformstore: approve team: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrWrongTeamStatus
		}
		return TeamApprovedLetterTx(ctx, tx, teamID, link, now)
	})
	s.dir.Invalidate(teamID)
	return err
}

// DeclineTeam deletes a team in review with its membership rows (nothing else exists for it); the
// slug is free again and the person stays registered without a team. ErrWrongTeamStatus otherwise.
func (s *Store) DeclineTeam(ctx context.Context, teamID int64) error {
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var st string
		if err := tx.GetContext(ctx, &st, `SELECT status FROM team WHERE id = ? FOR UPDATE`, teamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: lock team: %w", err)
		}
		if st != "review" {
			return ErrWrongTeamStatus
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM team_member WHERE team_id = ?`, teamID); err != nil {
			return fmt.Errorf("platformstore: decline members: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM email_token WHERE team_id = ?`, teamID); err != nil {
			return fmt.Errorf("platformstore: decline tokens: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM team WHERE id = ?`, teamID); err != nil {
			return fmt.Errorf("platformstore: decline team: %w", err)
		}
		return nil
	})
	s.dir.Invalidate(teamID)
	return err
}

// OperatorHandoff is an exchanged operator-entry code: the operator and the team.
type OperatorHandoff struct {
	User User
	Team tenant.Team
}

// ExchangeOperatorHandoff consumes an operator-entry hand-off for teamID and opens a team session
// sid with no membership: the code's user must still be an active operator and the team active or
// suspended. The team row (FOR SHARE) and the account are locked before the code (lock order at
// DeleteTeam): a delete either commits first (refused) or waits and revokes the session.
// Everything wrong is ErrTokenInvalid (a provisioning team: ErrTeamNotReady, the code stays alive).
func (s *Store) ExchangeOperatorHandoff(ctx context.Context, tokenHash string, teamID int64, sid string, now time.Time) (OperatorHandoff, error) {
	uid, err := s.handoffUser(ctx, tokenHash)
	if err != nil {
		return OperatorHandoff{}, err
	}
	var res OperatorHandoff
	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		var tr teamRow
		if err := tx.GetContext(ctx, &tr, `SELECT `+teamColumns+` FROM team WHERE id = ? FOR SHARE`, teamID); err != nil {
			if isNoRows(err) {
				return ErrTokenInvalid
			}
			return fmt.Errorf("platformstore: operator hand-off team: %w", err)
		}
		u, err := shareUserByID(ctx, tx, uid)
		if errors.Is(err, ErrUserNotFound) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		var t tokenRow
		if err := tx.GetContext(ctx, &t, `SELECT `+tokenColumns+` FROM email_token
WHERE token_hash = ? AND purpose = 'handoff' FOR UPDATE`, tokenHash); err != nil {
			if isNoRows(err) {
				return ErrTokenInvalid
			}
			return fmt.Errorf("platformstore: lock operator hand-off: %w", err)
		}
		if t.UsedAt.Valid || !t.ExpiresAt.After(now) || !t.TeamID.Valid || t.TeamID.Int64 != teamID || t.UserID != u.ID {
			return ErrTokenInvalid
		}
		switch tr.Status {
		case "provisioning":
			return ErrTeamNotReady
		case "active", "suspended":
		default:
			return ErrTokenInvalid
		}
		if u.Status != UserActive || !u.IsOperator {
			return ErrTokenInvalid
		}
		if err := consumeTx(ctx, tx, t.ID, now); err != nil {
			return err
		}
		if err := createSession(ctx, tx, sid, u.ID, teamID, SessionTeam, u.CredGen, now); err != nil {
			return err
		}
		res = OperatorHandoff{User: u, Team: tr.team()}
		return nil
	})
	return res, err
}
