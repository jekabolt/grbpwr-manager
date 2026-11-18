package platformstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// Session audiences (user_session.aud).
const (
	SessionTeam    = "team"
	SessionConsole = "console"
	SessionRoot    = "root"
)

// MemberTeam is one team a user is an active member of, as the root host lists it after sign-in.
type MemberTeam struct {
	TeamID int64  `db:"team_id"`
	Slug   string `db:"slug"`
	Name   string `db:"name"`
	Role   string `db:"role"`
	Status string `db:"status"`
}

// CreateSession inserts a user_session row. teamID 0 = none (root, console).
func (s *Store) CreateSession(ctx context.Context, sid string, userID, teamID int64, aud string, now time.Time) error {
	return createSession(ctx, s.db, sid, userID, teamID, aud, now)
}

func createSession(ctx context.Context, e sqlx.ExecerContext, sid string, userID, teamID int64, aud string, now time.Time) error {
	var team any
	if teamID > 0 {
		team = teamID
	}
	if _, err := e.ExecContext(ctx, `INSERT INTO user_session (sid, user_id, team_id, aud, created_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?)`, sid, userID, team, aud, now, now); err != nil {
		return fmt.Errorf("platformstore: create session: %w", err)
	}
	return nil
}

// RevokeSession revokes one session (sign-out). An unknown or revoked sid is not an error.
func (s *Store) RevokeSession(ctx context.Context, sid string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE user_session SET revoked_at = ? WHERE sid = ? AND revoked_at IS NULL`, now, sid); err != nil {
		return fmt.Errorf("platformstore: revoke session: %w", err)
	}
	return nil
}

// MemberTeams lists the user's active memberships in teams that are past sign-up and not deleted.
func (s *Store) MemberTeams(ctx context.Context, userID int64) ([]MemberTeam, error) {
	var out []MemberTeam
	if err := s.db.SelectContext(ctx, &out, `SELECT m.team_id, t.slug, t.name, m.role, t.status
FROM team_member m JOIN team t ON t.id = m.team_id
WHERE m.user_id = ? AND m.status = 'active' AND t.status IN ('provisioning', 'active', 'suspended')
ORDER BY t.name, t.id`, userID); err != nil {
		return nil, fmt.Errorf("platformstore: member teams: %w", err)
	}
	return out, nil
}

// Membership returns the user's active membership in a team that is past sign-up and not deleted
// (status read fresh, not from the directory cache). ok=false when there is none.
func (s *Store) Membership(ctx context.Context, userID, teamID int64) (MemberTeam, bool, error) {
	var m MemberTeam
	err := s.db.GetContext(ctx, &m, `SELECT m.team_id, t.slug, t.name, m.role, t.status
FROM team_member m JOIN team t ON t.id = m.team_id
WHERE m.user_id = ? AND m.team_id = ? AND m.status = 'active' AND t.status IN ('provisioning', 'active', 'suspended')`,
		userID, teamID)
	if isNoRows(err) {
		return MemberTeam{}, false, nil
	}
	if err != nil {
		return MemberTeam{}, false, fmt.Errorf("platformstore: membership: %w", err)
	}
	return m, true, nil
}

// IssueHandoff inserts a hand-off row (purpose handoff, no code) for the user into the team.
func (s *Store) IssueHandoff(ctx context.Context, userID int64, t NewToken, now time.Time) error {
	t.Purpose = "handoff"
	t.CodeHash = ""
	if t.TeamID <= 0 {
		return errors.New("platformstore: a hand-off needs a team")
	}
	return s.inTx(ctx, func(tx *sqlx.Tx) error { return insertTokenTx(ctx, tx, userID, t, now) })
}

// HandoffResult is an exchanged hand-off: the account and its membership in the team.
type HandoffResult struct {
	User   User
	Member MemberTeam
}

// ExchangeHandoff consumes a hand-off for teamID and opens a team session sid. A team still being
// provisioned answers ErrTeamNotReady and leaves the hand-off alive (the page retries); everything
// else wrong is ErrTokenInvalid.
func (s *Store) ExchangeHandoff(ctx context.Context, tokenHash string, teamID int64, sid string, now time.Time) (HandoffResult, error) {
	var res HandoffResult
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var t tokenRow
		if err := tx.GetContext(ctx, &t, `SELECT `+tokenColumns+` FROM email_token
WHERE token_hash = ? AND purpose = 'handoff' FOR UPDATE`, tokenHash); err != nil {
			if isNoRows(err) {
				return ErrTokenInvalid
			}
			return fmt.Errorf("platformstore: lock hand-off: %w", err)
		}
		if t.UsedAt.Valid || !t.ExpiresAt.After(now) || !t.TeamID.Valid || t.TeamID.Int64 != teamID {
			return ErrTokenInvalid
		}
		var m MemberTeam
		err := tx.GetContext(ctx, &m, `SELECT m.team_id, t.slug, t.name, m.role, t.status
FROM team_member m JOIN team t ON t.id = m.team_id
WHERE m.user_id = ? AND m.team_id = ? AND m.status = 'active'`, t.UserID, teamID)
		if isNoRows(err) {
			return ErrTokenInvalid
		}
		if err != nil {
			return fmt.Errorf("platformstore: hand-off membership: %w", err)
		}
		switch m.Status {
		case "provisioning":
			return ErrTeamNotReady
		case "active", "suspended":
		default:
			return ErrTokenInvalid
		}
		u, err := s.userWhere(ctx, tx, `id = ?`, t.UserID)
		if err != nil {
			if errors.Is(err, ErrUserNotFound) {
				return ErrTokenInvalid
			}
			return err
		}
		if u.Status != UserActive {
			return ErrTokenInvalid
		}
		if err := consumeTx(ctx, tx, t.ID, now); err != nil {
			return err
		}
		if err := createSession(ctx, tx, sid, u.ID, teamID, SessionTeam, now); err != nil {
			return err
		}
		res = HandoffResult{User: u, Member: m}
		return nil
	})
	return res, err
}
