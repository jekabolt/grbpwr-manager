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
	// team pin). It runs before the control-plane commit: a failure rolls the acceptance back.
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

// AcceptInvite consumes an invite: the account (existing → its password must match; pending →
// activated with the new password; none → created with the chosen handle, email counted verified),
// the membership with the invite's role, the admins row in the team, and a hand-off — one transaction.
// ErrTeamReadOnly for a suspended team, ErrWrongPassword, ErrHandleTaken, else ErrTokenInvalid.
func (s *Store) AcceptInvite(ctx context.Context, p AcceptParams) (AcceptResult, error) {
	var res AcceptResult
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		res = AcceptResult{}
		var inv Invite
		if err := tx.GetContext(ctx, &inv, inviteSelect+` FOR UPDATE`, p.TokenHash, p.Now); err != nil {
			if isNoRows(err) {
				return ErrTokenInvalid
			}
			return fmt.Errorf("platformstore: lock invite: %w", err)
		}
		switch inv.TeamStatus {
		case "active":
		case "suspended":
			return ErrTeamReadOnly
		default:
			return ErrTokenInvalid
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
VALUES (?, ?, ?, 'active', ?) ON DUPLICATE KEY UPDATE role = VALUES(role), status = 'active'`,
			inv.TeamID, res.UserID, inv.Role, p.Now); err != nil {
			return fmt.Errorf("platformstore: invite membership: %w", err)
		}
		r, err := tx.ExecContext(ctx, `UPDATE team_invite SET accepted_at = ? WHERE id = ? AND accepted_at IS NULL`, p.Now, inv.ID)
		if err != nil {
			return fmt.Errorf("platformstore: accept invite: %w", err)
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return ErrTokenInvalid
		}
		h := p.Handoff
		h.Purpose, h.CodeHash, h.TeamID = "handoff", "", inv.TeamID
		if err := insertTokenTx(ctx, tx, res.UserID, h, p.Now); err != nil {
			return err
		}
		if p.Ensure != nil {
			if err := p.Ensure(ctx, inv, res.Handle); err != nil {
				return fmt.Errorf("platformstore: team admins row: %w", err)
			}
		}
		res.TeamID, res.TeamSlug = inv.TeamID, inv.TeamSlug
		return nil
	})
	return res, err
}
