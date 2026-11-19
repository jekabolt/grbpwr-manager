package platformstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// Invite is a live team_invite as its link shows it.
type Invite struct {
	ID          int64     `db:"id"`
	TeamID      int64     `db:"team_id"`
	TeamSlug    string    `db:"slug"`
	TeamName    string    `db:"name"`
	TeamStatus  string    `db:"status"`
	Email       string    `db:"email"`
	Role        string    `db:"role"`
	Permissions []byte    `db:"permissions"` // JSON [{"section":"…","access":"read|write"}]
	InvitedBy   int64     `db:"invited_by"`
	ExpiresAt   time.Time `db:"expires_at"`
	// AccountExists: the invited email already has a verified account (the join form then asks only
	// for its password, no handle).
	AccountExists bool `db:"-"`
}

const inviteSelect = `SELECT i.id, i.team_id, t.slug, t.name, t.status, i.email, i.role, i.permissions, i.invited_by, i.expires_at
FROM team_invite i JOIN team t ON t.id = i.team_id
WHERE i.token_hash = ? AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > ? AND t.status <> 'deleted'`

// InviteByToken returns the live invite with this token hash (ErrTokenInvalid when there is none).
func (s *Store) InviteByToken(ctx context.Context, tokenHash string, now time.Time) (Invite, error) {
	var inv Invite
	if err := s.db.GetContext(ctx, &inv, inviteSelect, tokenHash, now); err != nil {
		if isNoRows(err) {
			return Invite{}, ErrTokenInvalid
		}
		return Invite{}, fmt.Errorf("platformstore: read invite: %w", err)
	}
	var n int
	if err := s.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM platform_user WHERE email = ? AND status <> 'pending'`, inv.Email); err != nil {
		return Invite{}, fmt.Errorf("platformstore: invite account: %w", err)
	}
	inv.AccountExists = n > 0
	return inv, nil
}

// AcceptParams is one join-form submission. The caller validated the handle and hashed the password.
type AcceptParams struct {
	TokenHash string
	Now       time.Time
	// PasswordMatches checks the typed password against an existing active account's hash.
	PasswordMatches func(hash string) bool
	// NewPasswordHash is the typed password hashed, for a new or still-pending account.
	NewPasswordHash string
	// Handle is the chosen handle for a new account ("" = derive from HandleBase).
	Handle, HandleBase string
	Handoff            NewToken
	// Stage writes the member's admins row in the team's database DISABLED, with the invite's
	// rights. It runs between the two control-plane commits, outside any transaction: a failure or
	// crash leaves the membership reserved ('joining'), the row unusable and the invite live.
	Stage func(ctx context.Context, inv Invite, handle string) error
	// Enable finishes the staged row once the membership is active (the join's last step): it
	// re-reads the membership and enables the row only through the team-side compare-and-set.
	// false = not enabled (the member was removed or the row re-decided meanwhile).
	Enable func(ctx context.Context, inv Invite, userID int64, handle string) (bool, error)
	// TermsVersion is the terms version accepted on the join page ("" = not accepted). Required
	// only when the invite creates a new account; an existing one is never asked again.
	TermsVersion string
}

// AcceptResult is an accepted invite: who joined which team, and whether the account is new.
type AcceptResult struct {
	UserID   int64
	Handle   string
	TeamID   int64
	TeamSlug string
	Created  bool
	// EnablePending: the last step failed or found nothing to enable; a row still staged is
	// finished on the member's next platform sign-in to the team.
	EnablePending bool
}

// AcceptInvite consumes an invite in four steps, so the team's admins row is never usable while
// the control plane has not made the member active:
//
//  1. reserve (one commit): the account (existing → its password must match; pending → activated
//     with the new password; none → created with the chosen handle, email counted verified) and a
//     'joining' membership (sign-in and hand-off read only 'active'); an invite for a person who is
//     already an active member is refused (ErrAlreadyMember) and changes nothing;
//  2. Stage writes the member's admins row DISABLED with the invite's rights and the team-side
//     join_pending marker;
//  3. activate (one commit): the membership becomes active, the invite is consumed, the hand-off
//     inserted;
//  4. Enable re-reads the membership and enables the row through the team-side compare-and-set.
//
// The marker lives in the team database, so enabling and clearing it are one write, and any
// disable of the row clears it: a late finish never re-enables a row someone disabled. A crash
// after step 2 leaves the row disabled and the invite live (the same link runs again; an expired
// one is swept by SweepJoining, the row stays disabled). A crash after step 3 leaves the marker on
// an active member's row: their next platform sign-in to the team finishes it.
// ErrTeamReadOnly for a suspended team, ErrWrongPassword, ErrHandleTaken, ErrTermsRequired,
// ErrAlreadyMember, else ErrTokenInvalid.
func (s *Store) AcceptInvite(ctx context.Context, p AcceptParams) (AcceptResult, error) {
	res, inv, err := s.reserveInvite(ctx, p)
	if err != nil {
		return AcceptResult{}, err
	}
	if p.Stage != nil {
		if err := p.Stage(ctx, inv, res.Handle); err != nil {
			return AcceptResult{}, fmt.Errorf("platformstore: team admins row: %w", err)
		}
	}
	if err := s.activateInvite(ctx, p, inv, res.UserID); err != nil {
		return AcceptResult{}, err
	}
	res.TeamID, res.TeamSlug = inv.TeamID, inv.TeamSlug
	if p.Enable != nil {
		if ok, err := p.Enable(ctx, inv, res.UserID, res.Handle); err != nil || !ok {
			res.EnablePending = true
		}
	}
	return res, nil
}

// SweepJoining removes reserved memberships whose invite is no longer live (expired, revoked or
// gone): the join can never finish, and the staged admins row stays disabled. It returns how many.
func (s *Store) SweepJoining(ctx context.Context, now time.Time) (int64, error) {
	r, err := s.db.ExecContext(ctx, `DELETE m FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.status = 'joining' AND NOT EXISTS (
  SELECT 1 FROM team_invite i
  WHERE i.team_id = m.team_id AND i.email = u.email AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > ?)`, now)
	if err != nil {
		return 0, fmt.Errorf("platformstore: sweep joining: %w", err)
	}
	return r.RowsAffected()
}

// lockLiveInviteTx locks the live invite of tokenHash and checks its team takes members.
func lockLiveInviteTx(ctx context.Context, tx *sqlx.Tx, tokenHash string, now time.Time) (Invite, error) {
	var inv Invite
	if err := tx.GetContext(ctx, &inv, inviteSelect+` FOR UPDATE`, tokenHash, now); err != nil {
		if isNoRows(err) {
			return Invite{}, ErrTokenInvalid
		}
		return Invite{}, fmt.Errorf("platformstore: lock invite: %w", err)
	}
	switch inv.TeamStatus {
	case "active":
		return inv, nil
	case "suspended":
		return Invite{}, ErrTeamReadOnly
	default:
		return Invite{}, ErrTokenInvalid
	}
}

// reserveInvite is step 1 of AcceptInvite.
func (s *Store) reserveInvite(ctx context.Context, p AcceptParams) (AcceptResult, Invite, error) {
	var (
		res AcceptResult
		inv Invite
	)
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		res = AcceptResult{}
		var err error
		if inv, err = lockLiveInviteTx(ctx, tx, p.TokenHash, p.Now); err != nil {
			return err
		}
		u, err := lockUserByEmail(ctx, tx, inv.Email)
		switch {
		case errors.Is(err, ErrUserNotFound):
			if p.TermsVersion == "" {
				return ErrTermsRequired
			}
			verified := p.Now
			handle, fixed := p.Handle, true
			if handle == "" {
				handle, fixed = p.HandleBase, false
			}
			id, h, err := insertUserTx(ctx, tx, inv.Email, p.NewPasswordHash, UserActive, &verified, false, handle, fixed, p.TermsVersion, p.Now)
			if err != nil {
				return err
			}
			res.UserID, res.Handle, res.Created = id, h, true
		case err != nil:
			return err
		case u.Status == UserActive:
			if p.PasswordMatches == nil || !p.PasswordMatches(u.PasswordHash) {
				return ErrWrongPassword
			}
			// After the password: the membership state is not told to someone who only holds the link.
			var active int
			if err := tx.GetContext(ctx, &active, `SELECT COUNT(*) FROM team_member
WHERE team_id = ? AND user_id = ? AND status = 'active'`, inv.TeamID, u.ID); err != nil {
				return fmt.Errorf("platformstore: read membership: %w", err)
			}
			if active > 0 {
				return ErrAlreadyMember
			}
			res.UserID, res.Handle = u.ID, u.Handle
		case u.Status == UserPending:
			// The invite proves the inbox: the half-finished sign-up becomes a verified account.
			if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET password_hash = ?, status = 'active',
email_verified_at = COALESCE(email_verified_at, ?) WHERE id = ?`, p.NewPasswordHash, p.Now, u.ID); err != nil {
				return fmt.Errorf("platformstore: activate invited user: %w", err)
			}
			res.UserID, res.Handle = u.ID, u.Handle
		default:
			return ErrTokenInvalid
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO team_member (team_id, user_id, role, status, created_at)
VALUES (?, ?, ?, 'joining', ?) ON DUPLICATE KEY UPDATE status = IF(status = 'active', 'active', 'joining')`,
			inv.TeamID, res.UserID, inv.Role, p.Now); err != nil {
			return fmt.Errorf("platformstore: reserve membership: %w", err)
		}
		return nil
	})
	return res, inv, err
}

// activateInvite is step 3 of AcceptInvite: the invite and its team are re-checked under lock.
func (s *Store) activateInvite(ctx context.Context, p AcceptParams, reserved Invite, userID int64) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		inv, err := lockLiveInviteTx(ctx, tx, p.TokenHash, p.Now)
		if err != nil {
			return err
		}
		if inv.ID != reserved.ID {
			return ErrTokenInvalid
		}
		// Only the reservation (or a membership that was already active) turns active: a member removed
		// between the steps stays removed.
		r, err := tx.ExecContext(ctx, `UPDATE team_member SET role = ?, status = 'active'
WHERE team_id = ? AND user_id = ? AND status IN ('joining', 'active')`, inv.Role, inv.TeamID, userID)
		if err != nil {
			return fmt.Errorf("platformstore: activate membership: %w", err)
		}
		if n, _ := r.RowsAffected(); n == 0 {
			var st string
			if err := tx.GetContext(ctx, &st, `SELECT status FROM team_member WHERE team_id = ? AND user_id = ?`, inv.TeamID, userID); err != nil || st != "active" {
				return ErrTokenInvalid
			}
		}
		r, err = tx.ExecContext(ctx, `UPDATE team_invite SET accepted_at = ? WHERE id = ? AND accepted_at IS NULL`, p.Now, inv.ID)
		if err != nil {
			return fmt.Errorf("platformstore: accept invite: %w", err)
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return ErrTokenInvalid
		}
		h := p.Handoff
		h.Purpose, h.CodeHash, h.TeamID = "handoff", "", inv.TeamID
		return insertTokenTx(ctx, tx, userID, h, p.Now)
	})
}
