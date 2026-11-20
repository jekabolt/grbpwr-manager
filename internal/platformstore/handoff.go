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
	// LogoSHA is the sha256 of the team's logo, "" when it has none.
	LogoSHA string `db:"logo_sha"`
}

// CreateSession inserts a user_session row. teamID 0 = none (root, console). credGen is the
// account's credential_gen as the caller read it with the password it checked: the row is
// inserted only while it is unchanged, so a sign-in that checked the old password cannot open a
// session after a password change committed (ErrCredentialsChanged).
func (s *Store) CreateSession(ctx context.Context, sid string, userID, teamID int64, aud string, credGen int64, now time.Time) error {
	return createSession(ctx, s.db, sid, userID, teamID, aud, credGen, now)
}

func createSession(ctx context.Context, e sqlx.ExecerContext, sid string, userID, teamID int64, aud string, credGen int64, now time.Time) error {
	return createSessionTx(ctx, e, sid, userID, teamID, aud, "", credGen, now)
}

// createSessionTx is createSession with the root session the new one was opened from (parentSID,
// "" = none): a root sign-out revokes every session that names it. The device the request came
// from (WithSessionClient on ctx) is recorded with it.
func createSessionTx(ctx context.Context, e sqlx.ExecerContext, sid string, userID, teamID int64, aud, parentSID string,
	credGen int64, now time.Time) error {
	var team, parent any
	if teamID > 0 {
		team = teamID
	}
	if parentSID != "" {
		parent = parentSID
	}
	ua, ip := sessionClientArgs(ctx)
	r, err := e.ExecContext(ctx, `INSERT INTO user_session (sid, user_id, team_id, aud, created_at, last_seen_at, parent_sid, user_agent, ip)
SELECT ?, id, ?, ?, ?, ?, ?, ?, ? FROM platform_user WHERE id = ? AND credential_gen = ?`,
		sid, team, aud, now, now, parent, ua, ip, userID, credGen)
	if err != nil {
		return fmt.Errorf("platformstore: create session: %w", err)
	}
	if n, err := r.RowsAffected(); err != nil {
		return fmt.Errorf("platformstore: create session: %w", err)
	} else if n == 0 {
		return ErrCredentialsChanged
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

// MemberTeams lists the user's active memberships in teams that are past sign-up and not deleted,
// a team waiting for approval (review) included — the root list shows it marked.
func (s *Store) MemberTeams(ctx context.Context, userID int64) ([]MemberTeam, error) {
	var out []MemberTeam
	if err := s.db.SelectContext(ctx, &out, `SELECT m.team_id, t.slug, t.name, m.role, t.status, COALESCE(l.sha256, '') AS logo_sha
FROM team_member m JOIN team t ON t.id = m.team_id LEFT JOIN team_logo l ON l.team_id = t.id
WHERE m.user_id = ? AND m.status = 'active' AND t.status IN ('review', 'provisioning', 'active', 'suspended')
ORDER BY t.name, t.id`, userID); err != nil {
		return nil, fmt.Errorf("platformstore: member teams: %w", err)
	}
	return out, nil
}

// Membership returns the user's active membership in a team that is past sign-up and not deleted
// (status read fresh, not from the directory cache). ok=false when there is none — also for a team
// in review, which has no workspace yet and is never entered.
func (s *Store) Membership(ctx context.Context, userID, teamID int64) (MemberTeam, bool, error) {
	var m MemberTeam
	err := s.db.GetContext(ctx, &m, `SELECT m.team_id, t.slug, t.name, m.role, t.status, COALESCE(l.sha256, '') AS logo_sha
FROM team_member m JOIN team t ON t.id = m.team_id LEFT JOIN team_logo l ON l.team_id = t.id
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

// IssueHandoff inserts a hand-off row (purpose handoff, no code) for the user into the team, only
// while the root session rootSID that asked for it is still live: the session row is locked, so a
// password change (which revokes the session and expires unused hand-offs in one transaction)
// either sees the new row and expires it, or has revoked the session first (ErrTokenInvalid). The
// team row is taken FOR SHARE first (lock order at DeleteTeam): a team that is not provisioning,
// active or suspended — deleted a moment ago included — gets no code (ErrWrongTeamStatus), and a
// delete waiting on the team row sees the new code and consumes it. When rootSID is a root session
// the code carries it as parent_sid (ExchangeHandoff copies it to the team session; the root's
// sign-out expires the code if it is still unused); an operator's console session gives none.
func (s *Store) IssueHandoff(ctx context.Context, userID int64, rootSID string, t NewToken, now time.Time) error {
	t.Purpose = "handoff"
	t.CodeHash = ""
	if t.TeamID <= 0 {
		return errors.New("platformstore: a hand-off needs a team")
	}
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		st, err := shareTeamTx(ctx, tx, t.TeamID)
		if err != nil {
			return err
		}
		switch st {
		case "provisioning", "active", "suspended":
		default:
			return ErrWrongTeamStatus
		}
		var auds []string
		if err := tx.SelectContext(ctx, &auds, `SELECT aud FROM user_session
WHERE sid = ? AND user_id = ? AND revoked_at IS NULL FOR SHARE`, rootSID, userID); err != nil {
			return fmt.Errorf("platformstore: hand-off session: %w", err)
		}
		if len(auds) == 0 {
			return ErrTokenInvalid
		}
		t.ParentSID = ""
		if auds[0] == SessionRoot {
			t.ParentSID = rootSID
		}
		return insertTokenTx(ctx, tx, userID, t, now)
	})
}

// HandoffResult is an exchanged hand-off: the account and its membership in the team.
type HandoffResult struct {
	User   User
	Member MemberTeam
}

// ExchangeHandoff consumes a hand-off for teamID and opens a team session sid; the membership row is
// locked, so a concurrent removal either wins (refused) or sees and revokes the session. The team
// row (FOR SHARE) and the account come first (lock order at DeleteTeam): a delete either commits
// before (status deleted: refused) or waits for this commit and revokes the session. A team still
// being provisioned answers ErrTeamNotReady and leaves the hand-off alive (the page retries);
// everything else wrong is ErrTokenInvalid.
func (s *Store) ExchangeHandoff(ctx context.Context, tokenHash string, teamID int64, sid string, now time.Time) (HandoffResult, error) {
	uid, err := s.handoffUser(ctx, tokenHash)
	if err != nil {
		return HandoffResult{}, err
	}
	var res HandoffResult
	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		st, err := shareTeamTx(ctx, tx, teamID)
		if err != nil {
			return err
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
			return fmt.Errorf("platformstore: lock hand-off: %w", err)
		}
		if t.UsedAt.Valid || !t.ExpiresAt.After(now) || !t.TeamID.Valid || t.TeamID.Int64 != teamID || t.UserID != u.ID {
			return ErrTokenInvalid
		}
		var m MemberTeam
		err = tx.GetContext(ctx, &m, `SELECT m.team_id, t.slug, t.name, m.role, t.status
FROM team_member m JOIN team t ON t.id = m.team_id
WHERE m.user_id = ? AND m.team_id = ? AND m.status = 'active' FOR UPDATE OF m`, t.UserID, teamID)
		if isNoRows(err) {
			return ErrTokenInvalid
		}
		if err != nil {
			return fmt.Errorf("platformstore: hand-off membership: %w", err)
		}
		m.Status = st // as locked above
		switch st {
		case "provisioning":
			return ErrTeamNotReady
		case "active", "suspended":
		default:
			return ErrTokenInvalid
		}
		if u.Status != UserActive {
			return ErrTokenInvalid
		}
		if err := consumeTx(ctx, tx, t.ID, now); err != nil {
			return err
		}
		if err := createSessionTx(ctx, tx, sid, u.ID, teamID, SessionTeam, t.ParentSID.String, u.CredGen, now); err != nil {
			return err
		}
		res = HandoffResult{User: u, Member: m}
		return nil
	})
	return res, err
}

// handoffUser reads, outside any transaction, whose hand-off code tokenHash is, so the exchange can
// lock the account before the code (lock order at DeleteTeam). ErrTokenInvalid when there is none.
func (s *Store) handoffUser(ctx context.Context, tokenHash string) (int64, error) {
	var uid int64
	if err := s.db.GetContext(ctx, &uid, `SELECT user_id FROM email_token WHERE token_hash = ? AND purpose = 'handoff'`,
		tokenHash); err != nil {
		if isNoRows(err) {
			return 0, ErrTokenInvalid
		}
		return 0, fmt.Errorf("platformstore: find hand-off: %w", err)
	}
	return uid, nil
}
