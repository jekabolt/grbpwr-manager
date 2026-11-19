package platformstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// Member-management errors (AdminService maps them to gRPC codes).
var (
	// ErrLastOwner: the change would leave the team without an active owner.
	ErrLastOwner = errors.New("platformstore: last owner")
	// ErrNotMember: no active member with that user in the team.
	ErrNotMember = errors.New("platformstore: not a member")
	// ErrInviteNotFound: no open invite with that id in the team.
	ErrInviteNotFound = errors.New("platformstore: invite not found")
	// ErrLetterCap: the recipient already got the hourly maximum of letters.
	ErrLetterCap = errors.New("platformstore: letter cap reached")
)

// TeamInvite is one invite of a team as the people screen lists it. ID never leaves the server.
type TeamInvite struct {
	ID        int64     `db:"id"`
	Email     string    `db:"email"`
	Role      string    `db:"role"`
	SentAt    time.Time `db:"created_at"`
	ExpiresAt time.Time `db:"expires_at"`
}

// OpenInvites lists the team's invites that were neither accepted nor revoked (an expired one
// included: the screen offers to resend it), newest first.
func (s *Store) OpenInvites(ctx context.Context, teamID int64) ([]TeamInvite, error) {
	var out []TeamInvite
	if err := s.db.SelectContext(ctx, &out, `SELECT id, email, role, created_at, expires_at FROM team_invite
WHERE team_id = ? AND accepted_at IS NULL AND revoked_at IS NULL ORDER BY created_at DESC, id DESC`, teamID); err != nil {
		return nil, fmt.Errorf("platformstore: open invites: %w", err)
	}
	return out, nil
}

// InviteParams is one invite (or re-invite) to write. The caller hashed the token and built the
// letter with its link.
type InviteParams struct {
	TeamID      int64
	Email       string
	Role        string
	Permissions []byte // JSON [{"section":"…","access":"read|write"}]
	TokenHash   string
	ExpiresAt   time.Time
	InvitedBy   int64
	Letter      Letter
	Now         time.Time
	// MaxPerHour caps letters to one address per hour (verify, invite and reset together).
	MaxPerHour int
}

// UpsertInvite invites an email into a team and queues the letter in one transaction. An active
// member with that email → ErrAlreadyMember. An open invite for the email is re-used: its role,
// permissions, token (the old link dies), expiry and sender are replaced. ErrLetterCap when the
// address already got MaxPerHour letters in the last hour.
func (s *Store) UpsertInvite(ctx context.Context, p InviteParams) (TeamInvite, error) {
	var out TeamInvite
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		// The team row serialises invites of one team (two owners inviting the same email at once).
		var st string
		if err := tx.GetContext(ctx, &st, `SELECT status FROM team WHERE id = ? FOR UPDATE`, p.TeamID); err != nil {
			if isNoRows(err) {
				return ErrTeamNotFound
			}
			return fmt.Errorf("platformstore: lock team: %w", err)
		}
		if st != "active" {
			return ErrWrongTeamStatus
		}
		var active int
		if err := tx.GetContext(ctx, &active, `SELECT COUNT(*) FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND m.status = 'active' AND u.email = ?`, p.TeamID, p.Email); err != nil {
			return fmt.Errorf("platformstore: invite membership: %w", err)
		}
		if active > 0 {
			return ErrAlreadyMember
		}
		if err := letterCapTx(ctx, tx, p.Email, p.Now, p.MaxPerHour); err != nil {
			return err
		}
		var id int64
		err := tx.GetContext(ctx, &id, `SELECT id FROM team_invite
WHERE team_id = ? AND email = ? AND accepted_at IS NULL AND revoked_at IS NULL ORDER BY id DESC LIMIT 1 FOR UPDATE`, p.TeamID, p.Email)
		switch {
		case isNoRows(err):
			r, err := tx.ExecContext(ctx, `INSERT INTO team_invite (team_id, email, role, permissions, token_hash, invited_by, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, p.TeamID, p.Email, p.Role, string(p.Permissions), p.TokenHash, p.InvitedBy, p.ExpiresAt, p.Now)
			if err != nil {
				return fmt.Errorf("platformstore: insert invite: %w", err)
			}
			if id, err = r.LastInsertId(); err != nil {
				return fmt.Errorf("platformstore: invite id: %w", err)
			}
		case err != nil:
			return fmt.Errorf("platformstore: open invite: %w", err)
		default:
			if _, err := tx.ExecContext(ctx, `UPDATE team_invite SET role = ?, permissions = ?, token_hash = ?, invited_by = ?,
expires_at = ?, created_at = ? WHERE id = ?`, p.Role, string(p.Permissions), p.TokenHash, p.InvitedBy, p.ExpiresAt, p.Now, id); err != nil {
				return fmt.Errorf("platformstore: update invite: %w", err)
			}
		}
		if err := insertLetterTx(ctx, tx, p.Letter, p.Now); err != nil {
			return err
		}
		out = TeamInvite{ID: id, Email: p.Email, Role: p.Role, SentAt: p.Now, ExpiresAt: p.ExpiresAt}
		return nil
	})
	return out, err
}

// ResendParams re-sends one open invite with a new token.
type ResendParams struct {
	TeamID, InviteID int64
	TokenHash        string
	ExpiresAt        time.Time
	// Letter is built for the invite's email; the store fills To.
	Letter     func(inv TeamInvite) Letter
	Now        time.Time
	MaxPerHour int
}

// ResendInvite gives an open invite a new token (the old link dies) and expiry and queues a new
// letter. ErrInviteNotFound, ErrLetterCap.
func (s *Store) ResendInvite(ctx context.Context, p ResendParams) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		var inv TeamInvite
		if err := tx.GetContext(ctx, &inv, `SELECT id, email, role, created_at, expires_at FROM team_invite
WHERE id = ? AND team_id = ? AND accepted_at IS NULL AND revoked_at IS NULL FOR UPDATE`, p.InviteID, p.TeamID); err != nil {
			if isNoRows(err) {
				return ErrInviteNotFound
			}
			return fmt.Errorf("platformstore: lock invite: %w", err)
		}
		if err := letterCapTx(ctx, tx, inv.Email, p.Now, p.MaxPerHour); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team_invite SET token_hash = ?, expires_at = ?, created_at = ? WHERE id = ?`,
			p.TokenHash, p.ExpiresAt, p.Now, inv.ID); err != nil {
			return fmt.Errorf("platformstore: resend invite: %w", err)
		}
		l := p.Letter(inv)
		l.To = inv.Email
		return insertLetterTx(ctx, tx, l, p.Now)
	})
}

// RevokeInvite revokes one open invite. ErrInviteNotFound when it is not open.
func (s *Store) RevokeInvite(ctx context.Context, teamID, inviteID int64, now time.Time) error {
	r, err := s.db.ExecContext(ctx, `UPDATE team_invite SET revoked_at = ?
WHERE id = ? AND team_id = ? AND accepted_at IS NULL AND revoked_at IS NULL`, now, inviteID, teamID)
	if err != nil {
		return fmt.Errorf("platformstore: revoke invite: %w", err)
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return ErrInviteNotFound
	}
	return nil
}

func letterCapTx(ctx context.Context, tx *sqlx.Tx, email string, now time.Time, maxPerHour int) error {
	if maxPerHour <= 0 {
		return nil
	}
	n, err := lettersSinceTx(ctx, tx, email, now.Add(-time.Hour))
	if err != nil {
		return err
	}
	if n >= maxPerHour {
		return ErrLetterCap
	}
	return nil
}

// MemberChange is a membership change that ended the member's sessions in the team.
type MemberChange struct {
	Handle string
	// WasOwner: the member's role before the change.
	WasOwner bool
	// RevokedSessions are the sids revoked in the same transaction (drop them from the edge cache).
	RevokedSessions []string
}

// RemoveMember marks an active member removed and revokes their sessions in the team, in one
// transaction. ErrNotMember; ErrLastOwner when they are the only active owner.
func (s *Store) RemoveMember(ctx context.Context, teamID, userID int64, now time.Time) (MemberChange, error) {
	return s.changeMember(ctx, teamID, userID, "", now)
}

// SetMemberRole changes an active member's role and revokes their sessions in the team so the new
// claims apply, in one transaction. Same role = no change, no revoke. ErrNotMember; ErrLastOwner
// when the only active owner would be demoted.
func (s *Store) SetMemberRole(ctx context.Context, teamID, userID int64, role string, now time.Time) (MemberChange, error) {
	if role != "owner" && role != "member" {
		return MemberChange{}, fmt.Errorf("platformstore: bad role %q", role)
	}
	return s.changeMember(ctx, teamID, userID, role, now)
}

// changeMember: role "" = remove.
func (s *Store) changeMember(ctx context.Context, teamID, userID int64, role string, now time.Time) (MemberChange, error) {
	var out MemberChange
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		out = MemberChange{}
		// Every owner row of the team is locked first: two owners demoting each other at once are
		// serialised, so the last-owner rule cannot be raced.
		var owners []int64
		if err := tx.SelectContext(ctx, &owners, `SELECT user_id FROM team_member
WHERE team_id = ? AND role = 'owner' AND status = 'active' FOR UPDATE`, teamID); err != nil {
			return fmt.Errorf("platformstore: lock owners: %w", err)
		}
		var m struct {
			Role   string `db:"role"`
			Handle string `db:"handle"`
		}
		if err := tx.GetContext(ctx, &m, `SELECT m.role, u.handle FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND m.user_id = ? AND m.status = 'active' FOR UPDATE`, teamID, userID); err != nil {
			if isNoRows(err) {
				return ErrNotMember
			}
			return fmt.Errorf("platformstore: lock member: %w", err)
		}
		out.Handle, out.WasOwner = m.Handle, m.Role == "owner"
		if role == m.Role {
			return nil
		}
		if out.WasOwner && len(owners) <= 1 {
			return ErrLastOwner
		}
		if role == "" {
			_, err := tx.ExecContext(ctx, `UPDATE team_member SET status = 'removed' WHERE team_id = ? AND user_id = ?`, teamID, userID)
			if err != nil {
				return fmt.Errorf("platformstore: remove member: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE team_member SET role = ? WHERE team_id = ? AND user_id = ?`, role, teamID, userID); err != nil {
			return fmt.Errorf("platformstore: set role: %w", err)
		}
		if err := tx.SelectContext(ctx, &out.RevokedSessions, `SELECT sid FROM user_session
WHERE user_id = ? AND team_id = ? AND revoked_at IS NULL FOR UPDATE`, userID, teamID); err != nil {
			return fmt.Errorf("platformstore: list team sessions: %w", err)
		}
		if len(out.RevokedSessions) > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE user_session SET revoked_at = ?
WHERE user_id = ? AND team_id = ? AND revoked_at IS NULL`, now.UTC(), userID, teamID); err != nil {
				return fmt.Errorf("platformstore: revoke team sessions: %w", err)
			}
		}
		return nil
	})
	return out, err
}
