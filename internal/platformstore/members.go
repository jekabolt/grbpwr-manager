package platformstore

import (
	"context"
	"database/sql"
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
	// ErrTeamLetterCap: the team already sent the hourly or daily maximum of invite letters (X6).
	ErrTeamLetterCap = errors.New("platformstore: team letter cap reached")
	// ErrNotOwner: the actor of a people change is not (or no longer) an active owner of the team,
	// or not an active operator.
	ErrNotOwner = errors.New("platformstore: actor is not an active owner")
)

// Actor is who makes a people change. Every change re-checks it inside its own transaction, under
// lock: a member must still be an active owner of the team (an owner demoted or removed a moment
// ago cannot finish a change), an entered operator must still be an active operator.
type Actor struct {
	UserID   int64
	Operator bool
}

// checkActorTx re-checks the actor. A member's row is locked, so a concurrent demotion of the actor
// either runs first (and this refuses) or waits for this change to commit.
func checkActorTx(ctx context.Context, tx *sqlx.Tx, teamID int64, a Actor) error {
	if a.UserID <= 0 {
		return ErrNotOwner
	}
	if a.Operator {
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM platform_user WHERE id = ? AND is_operator = 1 AND status = 'active'`,
			a.UserID); err != nil {
			return fmt.Errorf("platformstore: check operator: %w", err)
		}
		if n == 0 {
			return ErrNotOwner
		}
		return nil
	}
	var role string
	err := tx.GetContext(ctx, &role, `SELECT m.role FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND m.user_id = ? AND m.status = 'active' AND u.status = 'active' FOR UPDATE OF m`, teamID, a.UserID)
	if isNoRows(err) || (err == nil && role != "owner") {
		return ErrNotOwner
	}
	if err != nil {
		return fmt.Errorf("platformstore: check actor: %w", err)
	}
	return nil
}

// TeamInvite is one invite of a team as the people screen lists it. ID never leaves the server.
type TeamInvite struct {
	ID        int64     `db:"id"`
	Email     string    `db:"email"`
	Role      string    `db:"role"`
	SentAt    time.Time `db:"created_at"`
	ExpiresAt time.Time `db:"expires_at"`
	// Name is the person's name as the inviter typed it ("" = none).
	Name string `db:"name"`
	// Bounced: this invite's own latest letter (written at its last send) bounced permanently, drew
	// a spam complaint or was held back because the address is suppressed (X6). Letters of other
	// teams or other invites to the same address never count.
	Bounced bool `db:"bounced"`
}

// OpenInvites lists the team's invites that were neither accepted nor revoked (an expired one
// included: the screen offers to resend it), newest first.
func (s *Store) OpenInvites(ctx context.Context, teamID int64) ([]TeamInvite, error) {
	var out []TeamInvite
	if err := s.db.SelectContext(ctx, &out, `SELECT i.id, i.email, i.role, i.created_at, i.expires_at, i.name,
  EXISTS (SELECT 1 FROM platform_email e WHERE e.invite_id = i.id AND e.created_at >= i.created_at
    AND (e.delivery IN ('bounced', 'complained') OR (e.status = 'failed' AND e.last_error LIKE 'suppressed:%'))) AS bounced
FROM team_invite i
WHERE i.team_id = ? AND i.accepted_at IS NULL AND i.revoked_at IS NULL ORDER BY i.created_at DESC, i.id DESC`, teamID); err != nil {
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
	// KeepPermissions: a re-invite of an open invite keeps the permissions it already holds (the
	// request named none); Permissions are written only by a new invite then.
	KeepPermissions bool
	TokenHash       string
	ExpiresAt       time.Time
	// Actor invites (re-checked under lock); a member actor is recorded as the sender.
	Actor  Actor
	Letter Letter
	Now    time.Time
	// MaxPerHour caps letters to one address per hour (verify, invite and reset together).
	MaxPerHour int
	// TeamCap caps the team's invite letters (X6; zero fields = no cap).
	TeamCap TeamLetterCap
	// Name is the person's name as the inviter typed it, checked by the caller ("" = none: a
	// re-invite keeps the name the open invite holds).
	Name string
}

// UpsertInvite invites an email into a team and queues the letter in one transaction. An active
// member with that email → ErrAlreadyMember. An open invite for the email (an expired one included)
// is re-used: its role, permissions (kept with KeepPermissions), token (the old link dies), expiry
// and sender are replaced. ErrLetterCap when the address already got MaxPerHour letters in the last
// hour.
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
		if err := checkActorTx(ctx, tx, p.TeamID, p.Actor); err != nil {
			return err
		}
		var memberEmails []string
		if err := tx.SelectContext(ctx, &memberEmails, `SELECT u.email FROM team_member m JOIN platform_user u ON u.id = m.user_id
	WHERE m.team_id = ? AND m.status = 'active' AND u.email = ?`, p.TeamID, p.Email); err != nil {
			return fmt.Errorf("platformstore: invite membership: %w", err)
		}
		for _, email := range memberEmails {
			if email == p.Email {
				return ErrAlreadyMember
			}
		}
		if err := letterCapTx(ctx, tx, p.Email, p.Now, p.MaxPerHour); err != nil {
			return err
		}
		if err := teamLetterCapTx(ctx, tx, p.TeamID, p.Now, p.TeamCap); err != nil {
			return err
		}
		var existing struct {
			ID    int64  `db:"id"`
			Email string `db:"email"`
		}
		err := tx.GetContext(ctx, &existing, `SELECT id, email FROM team_invite
	WHERE team_id = ? AND email = ? AND accepted_at IS NULL AND revoked_at IS NULL ORDER BY id DESC LIMIT 1 FOR UPDATE`, p.TeamID, p.Email)
		if err == nil && existing.Email != p.Email {
			err = sql.ErrNoRows
		}
		id := existing.ID
		switch {
		case isNoRows(err):
			r, err := tx.ExecContext(ctx, `INSERT INTO team_invite (team_id, email, role, permissions, token_hash, invited_by, expires_at, created_at, name)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.TeamID, p.Email, p.Role, string(p.Permissions), p.TokenHash, p.Actor.UserID, p.ExpiresAt, p.Now, p.Name)
			if err != nil {
				return fmt.Errorf("platformstore: insert invite: %w", err)
			}
			if id, err = r.LastInsertId(); err != nil {
				return fmt.Errorf("platformstore: invite id: %w", err)
			}
		case err != nil:
			return fmt.Errorf("platformstore: open invite: %w", err)
		case p.KeepPermissions:
			if _, err := tx.ExecContext(ctx, `UPDATE team_invite SET role = ?, token_hash = ?, invited_by = ?,
expires_at = ?, created_at = ?, name = COALESCE(NULLIF(?, ''), name) WHERE id = ?`, p.Role, p.TokenHash, p.Actor.UserID, p.ExpiresAt, p.Now, p.Name, id); err != nil {
				return fmt.Errorf("platformstore: update invite: %w", err)
			}
		default:
			if _, err := tx.ExecContext(ctx, `UPDATE team_invite SET role = ?, permissions = ?, token_hash = ?, invited_by = ?,
expires_at = ?, created_at = ?, name = COALESCE(NULLIF(?, ''), name) WHERE id = ?`, p.Role, string(p.Permissions), p.TokenHash, p.Actor.UserID, p.ExpiresAt, p.Now, p.Name, id); err != nil {
				return fmt.Errorf("platformstore: update invite: %w", err)
			}
		}
		l := p.Letter
		l.TeamID, l.InviteID = p.TeamID, id
		if err := insertLetterTx(ctx, tx, l, p.Now); err != nil {
			return err
		}
		name := p.Name
		if name == "" {
			if err := tx.GetContext(ctx, &name, `SELECT name FROM team_invite WHERE id = ?`, id); err != nil {
				return fmt.Errorf("platformstore: invite name: %w", err)
			}
		}
		out = TeamInvite{ID: id, Email: p.Email, Role: p.Role, SentAt: p.Now, ExpiresAt: p.ExpiresAt, Name: name}
		return nil
	})
	return out, err
}

// ResendParams re-sends one open invite with a new token.
type ResendParams struct {
	TeamID, InviteID int64
	Actor            Actor
	TokenHash        string
	ExpiresAt        time.Time
	// Letter is built for the invite's email; the store fills To.
	Letter     func(inv TeamInvite) Letter
	Now        time.Time
	MaxPerHour int
	TeamCap    TeamLetterCap
}

// ResendInvite gives an open invite a new token (the old link dies) and expiry and queues a new
// letter. ErrInviteNotFound, ErrLetterCap.
func (s *Store) ResendInvite(ctx context.Context, p ResendParams) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := checkActorTx(ctx, tx, p.TeamID, p.Actor); err != nil {
			return err
		}
		var inv TeamInvite
		if err := tx.GetContext(ctx, &inv, `SELECT id, email, role, created_at, expires_at, name FROM team_invite
WHERE id = ? AND team_id = ? AND accepted_at IS NULL AND revoked_at IS NULL FOR UPDATE`, p.InviteID, p.TeamID); err != nil {
			if isNoRows(err) {
				return ErrInviteNotFound
			}
			return fmt.Errorf("platformstore: lock invite: %w", err)
		}
		if err := letterCapTx(ctx, tx, inv.Email, p.Now, p.MaxPerHour); err != nil {
			return err
		}
		if err := teamLetterCapTx(ctx, tx, p.TeamID, p.Now, p.TeamCap); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE team_invite SET token_hash = ?, expires_at = ?, created_at = ? WHERE id = ?`,
			p.TokenHash, p.ExpiresAt, p.Now, inv.ID); err != nil {
			return fmt.Errorf("platformstore: resend invite: %w", err)
		}
		l := p.Letter(inv)
		l.To, l.TeamID, l.InviteID = inv.Email, p.TeamID, inv.ID
		return insertLetterTx(ctx, tx, l, p.Now)
	})
}

// RevokeInvite revokes one open invite. ErrInviteNotFound when it is not open; ErrNotOwner.
func (s *Store) RevokeInvite(ctx context.Context, teamID, inviteID int64, actor Actor, now time.Time) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if err := checkActorTx(ctx, tx, teamID, actor); err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, `UPDATE team_invite SET revoked_at = ?
WHERE id = ? AND team_id = ? AND accepted_at IS NULL AND revoked_at IS NULL`, now, inviteID, teamID)
		if err != nil {
			return fmt.Errorf("platformstore: revoke invite: %w", err)
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return ErrInviteNotFound
		}
		return nil
	})
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

// MemberChangeParams is one removal or role change.
type MemberChangeParams struct {
	TeamID, UserID int64
	Actor          Actor
	// MirrorOwners are the owners whose admins row in the team database is confirmed super and
	// enabled. Demoting or removing an owner needs another active owner in this set: the team is
	// never left without an owner who can actually work.
	MirrorOwners []int64
	Now          time.Time
}

// RemoveMember marks an active member removed and revokes their sessions in the team, in one
// transaction. ErrNotOwner, ErrNotMember; ErrLastOwner when no other working owner is left.
func (s *Store) RemoveMember(ctx context.Context, p MemberChangeParams) (MemberChange, error) {
	return s.changeMember(ctx, p, "")
}

// SetMemberRole changes an active member's role and revokes their sessions in the team so the new
// claims apply, in one transaction. Same role = no change, no revoke (the actor is still checked).
// ErrNotOwner, ErrNotMember; ErrLastOwner when no other working owner would be left.
func (s *Store) SetMemberRole(ctx context.Context, p MemberChangeParams, role string) (MemberChange, error) {
	if role != "owner" && role != "member" {
		return MemberChange{}, fmt.Errorf("platformstore: bad role %q", role)
	}
	return s.changeMember(ctx, p, role)
}

// changeMember: role "" = remove.
func (s *Store) changeMember(ctx context.Context, p MemberChangeParams, role string) (MemberChange, error) {
	var out MemberChange
	teamID, userID := p.TeamID, p.UserID
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		out = MemberChange{}
		// Every owner row of the team is locked first: two owners demoting each other at once are
		// serialised, so the last-owner rule cannot be raced.
		var owners []int64
		if err := tx.SelectContext(ctx, &owners, `SELECT user_id FROM team_member
WHERE team_id = ? AND role = 'owner' AND status = 'active' FOR UPDATE`, teamID); err != nil {
			return fmt.Errorf("platformstore: lock owners: %w", err)
		}
		if err := checkActorTx(ctx, tx, teamID, p.Actor); err != nil {
			return err
		}
		var m struct {
			Role   string         `db:"role"`
			Handle string         `db:"handle"`
			Email  sql.NullString `db:"email"`
		}
		if err := tx.GetContext(ctx, &m, `SELECT m.role, u.handle, u.email FROM team_member m JOIN platform_user u ON u.id = m.user_id
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
		if out.WasOwner && !otherWorkingOwner(owners, p.MirrorOwners, userID) {
			return ErrLastOwner
		}
		if role == "" {
			_, err := tx.ExecContext(ctx, `UPDATE team_member SET status = 'removed' WHERE team_id = ? AND user_id = ?`, teamID, userID)
			if err != nil {
				return fmt.Errorf("platformstore: remove member: %w", err)
			}
			// A removed member must not walk back in through a still-live invite (by its letter or
			// the team join link) or an open join request: both need a fresh decision now.
			if err := revokeLiveInvitesTx(ctx, tx, teamID, m.Email.String, p.Now); err != nil {
				return err
			}
			if err := closePendingJoinRequestsTx(ctx, tx, teamID, userID, p.Now); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE team_member SET role = ? WHERE team_id = ? AND user_id = ?`, role, teamID, userID); err != nil {
			return fmt.Errorf("platformstore: set role: %w", err)
		}
		revoked, err := revokeTeamSessionsTx(ctx, tx, userID, teamID, p.Now)
		if err != nil {
			return err
		}
		out.RevokedSessions = revoked
		return nil
	})
	return out, err
}

// otherWorkingOwner: an active owner other than userID whose team-side row is confirmed.
func otherWorkingOwner(owners, mirror []int64, userID int64) bool {
	for _, o := range owners {
		if o == userID {
			continue
		}
		for _, c := range mirror {
			if c == o {
				return true
			}
		}
	}
	return false
}

// revokeTeamSessionsTx revokes every live session of the user in the team and returns the sids.
func revokeTeamSessionsTx(ctx context.Context, tx *sqlx.Tx, userID, teamID int64, now time.Time) ([]string, error) {
	var sids []string
	if err := tx.SelectContext(ctx, &sids, `SELECT sid FROM user_session
WHERE user_id = ? AND team_id = ? AND revoked_at IS NULL FOR UPDATE`, userID, teamID); err != nil {
		return nil, fmt.Errorf("platformstore: list team sessions: %w", err)
	}
	if len(sids) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE user_session SET revoked_at = ?
WHERE user_id = ? AND team_id = ? AND revoked_at IS NULL`, now.UTC(), userID, teamID); err != nil {
			return nil, fmt.Errorf("platformstore: revoke team sessions: %w", err)
		}
	}
	return sids, nil
}

// CreateTeamSession opens a team session only while the team is active or read-only and the user
// is an active member of it. The team row (FOR SHARE), the account and the membership row are
// locked in that order (lock order at DeleteTeam): a delete, removal or role change either runs
// first (ok=false, nothing written) or waits for this commit and then sees and revokes the new
// session.
func (s *Store) CreateTeamSession(ctx context.Context, sid string, userID, teamID, credGen int64, now time.Time) (bool, error) {
	if teamID <= 0 {
		return false, errors.New("platformstore: a team session needs a team")
	}
	ok := false
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		st, err := shareTeamTx(ctx, tx, teamID)
		if err != nil {
			return err
		}
		if st != "active" && st != "suspended" {
			return nil
		}
		var users []int64
		if err := tx.SelectContext(ctx, &users, `SELECT id FROM platform_user WHERE id = ? FOR SHARE`, userID); err != nil {
			return fmt.Errorf("platformstore: share user: %w", err)
		}
		var rows []int64
		if err := tx.SelectContext(ctx, &rows, `SELECT user_id FROM team_member
WHERE team_id = ? AND user_id = ? AND status = 'active' FOR UPDATE`, teamID, userID); err != nil {
			return fmt.Errorf("platformstore: lock membership: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		if err := createSession(ctx, tx, sid, userID, teamID, SessionTeam, credGen, now); err != nil {
			return err
		}
		ok = true
		return nil
	})
	return ok, err
}
