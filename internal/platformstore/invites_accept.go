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
	// Ensure creates the member's admins row in the team's database (teamsync.EnsureAdmin under the
	// team pin). It runs between the two control-plane commits, outside any transaction: a failure
	// leaves the membership reserved ('joining') and the invite live, so the same link retries.
	Ensure func(ctx context.Context, inv Invite, handle string) error
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
}

// AcceptInvite consumes an invite in three steps, so no tenant write ever hangs on an open
// control-plane transaction:
//
//  1. reserve (one commit): the account (existing → its password must match; pending → activated
//     with the new password; none → created with the chosen handle, email counted verified) and a
//     'joining' membership, which every sign-in and hand-off refuses (they read status 'active');
//     an already active membership stays active;
//  2. Ensure writes the member's admins row in the team's database;
//  3. activate (one commit): the membership becomes active with the invite's role, the invite is
//     consumed and the hand-off inserted.
//
// A failure or crash after step 1 leaves the invite live and the membership reserved: the same link
// runs again (the account then exists, so the password typed the first time is checked).
// ErrTeamReadOnly for a suspended team, ErrWrongPassword, ErrHandleTaken, ErrTermsRequired, else
// ErrTokenInvalid.
func (s *Store) AcceptInvite(ctx context.Context, p AcceptParams) (AcceptResult, error) {
	res, inv, err := s.reserveInvite(ctx, p)
	if err != nil {
		return AcceptResult{}, err
	}
	if p.Ensure != nil {
		if err := p.Ensure(ctx, inv, res.Handle); err != nil {
			return AcceptResult{}, fmt.Errorf("platformstore: team admins row: %w", err)
		}
	}
	if err := s.activateInvite(ctx, p, inv, res.UserID); err != nil {
		return AcceptResult{}, err
	}
	res.TeamID, res.TeamSlug = inv.TeamID, inv.TeamSlug
	return res, nil
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
