package platformstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	// Name is the person's name as the inviter typed it ("" = none); it seeds a new account's
	// first and last name on accept.
	Name string `db:"invite_name"`
	// AccountExists: the invited email already has a verified account (the join form then asks only
	// for its password, no handle).
	AccountExists bool `db:"-"`
}

const inviteSelect = `SELECT i.id, i.team_id, t.slug, t.name, t.status, i.email, i.role, i.permissions, i.invited_by, i.expires_at,
i.name AS invite_name
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
	var emails []string
	if err := s.db.SelectContext(ctx, &emails, `SELECT email FROM platform_user WHERE email = ? AND status <> 'pending'`, inv.Email); err != nil {
		return Invite{}, fmt.Errorf("platformstore: invite account: %w", err)
	}
	for _, email := range emails {
		if email == inv.Email {
			inv.AccountExists = true
			break
		}
	}
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
	// FirstName and LastName name a new account, or a pending one that has no name yet ("" = none);
	// an existing active account keeps its own. The caller split them from the join form's name, or
	// from the invite's when the form had none.
	FirstName, LastName string

	// Provider, when set, accepts the invite with a verified provider sign-in instead of a password
	// (teams O §2, lane O3): the provider must vouch for the invited email (verified, authoritative,
	// ProviderEmail equal to it) and its identity, when already known, must be that account's,
	// otherwise ErrInviteMismatch. An existing active account joins with its password untouched and
	// the identity linked; a pending one is claimed as a provider sign-in claims it; no account → a
	// new one as a provider sign-up creates it (unusable password), terms still required. An
	// operator's account → ErrProviderOperator, a disabled one → ErrProviderDisabled. Handoff is
	// then inserted as a sign-in code for the invite's team (its exchange answers enter_team_slug).
	// PasswordMatches and NewPasswordHash are not used.
	Provider *ProviderSignIn
	// ProviderEmail is Provider.Email normalised by the caller ("" when it is not a usable address).
	ProviderEmail string
	// UnusableHash makes the password hash of an account the provider path creates or claims.
	UnusableHash func() (string, error)
}

// ErrInviteMismatch: a provider sign-in accepting an invite does not vouch for the invited email,
// or its identity already belongs to another account. Key invite_mismatch.
var ErrInviteMismatch = errors.New("platformstore: provider identity does not match the invite")

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
	// SIDs are sessions a provider claim of a pending account revoked (the caller drops them from
	// the edge cache).
	SIDs []string
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
// ErrAlreadyMember, else ErrTokenInvalid; on the provider path (p.Provider) also
// ErrInviteMismatch, ErrProviderOperator, ErrProviderDisabled.
func (s *Store) AcceptInvite(ctx context.Context, p AcceptParams) (AcceptResult, error) {
	if p.Provider != nil {
		in := *p.Provider
		in.Subject = strings.TrimSpace(in.Subject)
		if (in.Provider != ProviderGoogle && in.Provider != ProviderApple) || in.Subject == "" || len(in.Subject) > 255 ||
			p.UnusableHash == nil {
			return AcceptResult{}, errors.New("platformstore: provider invite needs a provider, a subject and a hash maker")
		}
		p.Provider = &in
	}
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

// lockLiveInviteTx locks the team row FOR SHARE, then the live invite of tokenHash (lock order at
// DeleteTeam), and checks that the invite belongs to teamID and its team takes members. A delete
// either commits first (the invite is revoked, the team deleted: refused) or waits for this commit.
func lockLiveInviteTx(ctx context.Context, tx *sqlx.Tx, teamID int64, tokenHash string, now time.Time) (Invite, error) {
	st, err := shareTeamTx(ctx, tx, teamID)
	if err != nil {
		return Invite{}, err
	}
	var inv Invite
	if err := tx.GetContext(ctx, &inv, inviteSelect+` FOR UPDATE OF i`, tokenHash, now); err != nil {
		if isNoRows(err) {
			return Invite{}, ErrTokenInvalid
		}
		return Invite{}, fmt.Errorf("platformstore: lock invite: %w", err)
	}
	if inv.TeamID != teamID {
		return Invite{}, ErrTokenInvalid
	}
	inv.TeamStatus = st // as locked above
	switch st {
	case "active":
		return inv, nil
	case "suspended":
		return Invite{}, ErrTeamReadOnly
	default:
		return Invite{}, ErrTokenInvalid
	}
}

// inviteTeam reads, outside any transaction, which team the invite of tokenHash belongs to, so
// the accept can lock the team row before the invite. ErrTokenInvalid when there is none.
func (s *Store) inviteTeam(ctx context.Context, tokenHash string) (int64, error) {
	var teamID int64
	if err := s.db.GetContext(ctx, &teamID, `SELECT team_id FROM team_invite WHERE token_hash = ?`, tokenHash); err != nil {
		if isNoRows(err) {
			return 0, ErrTokenInvalid
		}
		return 0, fmt.Errorf("platformstore: find invite: %w", err)
	}
	return teamID, nil
}

// reserveInvite is step 1 of AcceptInvite.
func (s *Store) reserveInvite(ctx context.Context, p AcceptParams) (AcceptResult, Invite, error) {
	teamID, err := s.inviteTeam(ctx, p.TokenHash)
	if err != nil {
		return AcceptResult{}, Invite{}, err
	}
	var (
		res   AcceptResult
		inv   Invite
		swept []int64
	)
	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		res, swept = AcceptResult{}, nil
		var err error
		if inv, err = lockLiveInviteTx(ctx, tx, teamID, p.TokenHash, p.Now); err != nil {
			return err
		}
		if p.Provider != nil {
			if res, swept, err = reserveProviderAccountTx(ctx, tx, inv, p); err != nil {
				return err
			}
			return reserveMembershipTx(ctx, tx, inv, res.UserID, p.Now)
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
			id, h, err := insertUserTx(ctx, tx, inv.Email, p.NewPasswordHash, UserActive, &verified, false, handle, fixed, p.TermsVersion,
				p.FirstName, p.LastName, p.Now)
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
			if err := setNamesIfEmptyTx(ctx, tx, u.ID, p.FirstName, p.LastName); err != nil {
				return err
			}
			res.UserID, res.Handle = u.ID, u.Handle
		case u.Status == UserDeleted:
			// After the password, as for an active account: only its holder learns it is deleted.
			if p.PasswordMatches == nil || !p.PasswordMatches(u.PasswordHash) {
				return ErrWrongPassword
			}
			return deletedAccountErr(u, p.Now)
		default:
			return ErrTokenInvalid
		}
		return reserveMembershipTx(ctx, tx, inv, res.UserID, p.Now)
	})
	for _, id := range swept {
		s.dir.Invalidate(id)
	}
	return res, inv, err
}

// reserveMembershipTx is step 1's 'joining' membership (an active one stays active).
func reserveMembershipTx(ctx context.Context, tx *sqlx.Tx, inv Invite, userID int64, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_member (team_id, user_id, role, status, created_at)
VALUES (?, ?, ?, 'joining', ?) ON DUPLICATE KEY UPDATE status = IF(status = 'active', 'active', 'joining')`,
		inv.TeamID, userID, inv.Role, now); err != nil {
		return fmt.Errorf("platformstore: reserve membership: %w", err)
	}
	return nil
}

// reserveProviderAccountTx is step 1's account on the provider path (AcceptParams.Provider), inside
// the transaction that holds the team row and the invite. The provider's word on the email is
// checked before any lookup; the identity is read without a lock (its unique key settles a
// concurrent link), the invited account FOR UPDATE (lock order: team, account, its identities).
// Returns the team ids a pending claim deleted (the caller drops them from the directory cache).
func reserveProviderAccountTx(ctx context.Context, tx *sqlx.Tx, inv Invite, p AcceptParams) (AcceptResult, []int64, error) {
	in := *p.Provider
	if p.ProviderEmail == "" || p.ProviderEmail != inv.Email || !in.EmailVerified || !in.EmailAuthoritative {
		return AcceptResult{}, nil, ErrInviteMismatch
	}
	var known []int64
	if err := tx.SelectContext(ctx, &known, `SELECT user_id FROM platform_identity WHERE provider = ? AND subject = ?`,
		in.Provider, in.Subject); err != nil {
		return AcceptResult{}, nil, fmt.Errorf("platformstore: find identity: %w", err)
	}
	var (
		res   AcceptResult
		swept []int64
	)
	u, err := lockUserByEmail(ctx, tx, inv.Email)
	switch {
	case errors.Is(err, ErrUserNotFound):
		if len(known) > 0 {
			return AcceptResult{}, nil, ErrInviteMismatch // the identity is another account's
		}
		if p.TermsVersion == "" {
			return AcceptResult{}, nil, ErrTermsRequired
		}
		hash, err := p.UnusableHash()
		if err != nil {
			return AcceptResult{}, nil, err
		}
		verified := p.Now
		id, h, err := insertUserTx(ctx, tx, inv.Email, hash, UserActive, &verified, false, p.HandleBase, false, p.TermsVersion,
			p.FirstName, p.LastName, p.Now)
		if errors.Is(err, ErrTokenInvalid) {
			return AcceptResult{}, nil, errIdentityRace // the same email was created a moment ago
		}
		if err != nil {
			return AcceptResult{}, nil, err
		}
		res = AcceptResult{UserID: id, Handle: h, Created: true}
	case err != nil:
		return AcceptResult{}, nil, err
	case len(known) > 0 && known[0] != u.ID:
		return AcceptResult{}, nil, ErrInviteMismatch
	case u.IsOperator:
		return AcceptResult{}, nil, ErrProviderOperator
	case u.Status == UserDisabled:
		return AcceptResult{}, nil, ErrProviderDisabled
	case u.Status == UserDeleted:
		return AcceptResult{}, nil, deletedAccountErr(u, p.Now)
	case u.Status == UserActive:
		var active int
		if err := tx.GetContext(ctx, &active, `SELECT COUNT(*) FROM team_member
WHERE team_id = ? AND user_id = ? AND status = 'active'`, inv.TeamID, u.ID); err != nil {
			return AcceptResult{}, nil, fmt.Errorf("platformstore: read membership: %w", err)
		}
		if active > 0 {
			return AcceptResult{}, nil, ErrAlreadyMember
		}
		res = AcceptResult{UserID: u.ID, Handle: u.Handle}
	case u.Status == UserPending:
		// The pre-hijack claim, as a provider sign-in makes it: whoever typed the password of the
		// unconfirmed sign-up loses it, its pending teams go.
		sids, teams, err := claimPendingTx(ctx, tx, u.ID, ProviderParams{Now: p.Now, UnusableHash: p.UnusableHash})
		if err != nil {
			return AcceptResult{}, nil, err
		}
		if err := setNamesIfEmptyTx(ctx, tx, u.ID, p.FirstName, p.LastName); err != nil {
			return AcceptResult{}, nil, err
		}
		res, swept = AcceptResult{UserID: u.ID, Handle: u.Handle, SIDs: sids}, teams
	default:
		return AcceptResult{}, nil, ErrTokenInvalid
	}
	if len(known) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE platform_identity SET last_login_at = ? WHERE provider = ? AND subject = ? AND user_id = ?`,
			p.Now, in.Provider, in.Subject, res.UserID); err != nil {
			return AcceptResult{}, nil, fmt.Errorf("platformstore: stamp identity: %w", err)
		}
	} else if err := insertIdentityTx(ctx, tx, res.UserID, in, p.Now); err != nil {
		return AcceptResult{}, nil, err
	}
	return res, swept, nil
}

// activateInvite is step 3 of AcceptInvite: the team and the invite are re-checked under lock.
func (s *Store) activateInvite(ctx context.Context, p AcceptParams, reserved Invite, userID int64) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		inv, err := lockLiveInviteTx(ctx, tx, reserved.TeamID, p.TokenHash, p.Now)
		if err != nil {
			return err
		}
		if inv.ID != reserved.ID {
			return ErrTokenInvalid
		}
		// The account again, FOR SHARE (after the invite: AcceptInvite's exception to the lock order):
		// a delete that committed after step 1 left the reservation 'joining', and it must not turn
		// active for a deleted account; a delete that comes later waits for this commit and freezes
		// the membership it finds active.
		u, err := shareUserByID(ctx, tx, userID)
		if errors.Is(err, ErrUserNotFound) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if u.Status == UserDeleted {
			return deletedAccountErr(u, p.Now)
		}
		if u.Status != UserActive {
			return ErrTokenInvalid
		}
		// A membership coming back (joining → active) starts clean: every older session of the person
		// in this team is revoked, so nothing from before a removal comes back to life with it.
		var prev string
		if err := tx.GetContext(ctx, &prev, `SELECT status FROM team_member WHERE team_id = ? AND user_id = ? FOR UPDATE`,
			inv.TeamID, userID); err != nil && !isNoRows(err) {
			return fmt.Errorf("platformstore: lock membership: %w", err)
		}
		if prev == "joining" {
			if _, err := revokeTeamSessionsTx(ctx, tx, userID, inv.TeamID, p.Now); err != nil {
				return err
			}
		}
		// Only the reservation (or a membership that was already active) turns active: a member removed
		// between the steps stays removed. A reservation turning active is a membership that starts, so
		// it owes the first entry (P0 §4); an active one keeps its flag. entry_pending is assigned before
		// status, so it reads the old status.
		r, err := tx.ExecContext(ctx, `UPDATE team_member SET role = ?, entry_pending = IF(status = 'joining', 1, entry_pending),
status = 'active'
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
		if p.Provider != nil {
			// The provider path lands on the root page: a sign-in code that names the team.
			h.Purpose, h.ParentSID = "signin", ""
		}
		return insertTokenTx(ctx, tx, userID, h, p.Now)
	})
}
