package platformstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// TeamSession reads the live tuple behind a team token in one query: the session sid belongs to
// user uid in team tid with aud team and is not revoked, the platform user is active, and the
// membership (tid, uid) is active. ok=false when any part is missing; role is the membership role.
func (s *Store) TeamSession(ctx context.Context, sid string, uid, tid int64) (string, bool, error) {
	if sid == "" || uid <= 0 || tid <= 0 {
		return "", false, nil
	}
	var roles []string
	err := s.db.SelectContext(ctx, &roles, `
SELECT m.role
FROM user_session s
JOIN platform_user u ON u.id = s.user_id
JOIN team_member m ON m.team_id = s.team_id AND m.user_id = s.user_id
WHERE s.sid = ? AND s.user_id = ? AND s.team_id = ? AND s.aud = 'team' AND s.revoked_at IS NULL
  AND u.status = 'active' AND m.status = 'active'`, sid, uid, tid)
	if err != nil {
		return "", false, fmt.Errorf("platformstore: team session: %w", err)
	}
	if len(roles) == 0 {
		return "", false, nil
	}
	return roles[0], true, nil
}

// RevokeTeamSessions revokes every live team session of user uid in team tid and returns their
// sids, so the caller can drop them from the edge cache (tenantbind.Binder.InvalidateSession).
// Member removal and demotion call it right after the membership change (not built in this lane).
func (s *Store) RevokeTeamSessions(ctx context.Context, uid, tid int64, now time.Time) ([]string, error) {
	var sids []string
	if err := s.db.SelectContext(ctx, &sids,
		`SELECT sid FROM user_session WHERE user_id = ? AND team_id = ? AND revoked_at IS NULL`, uid, tid); err != nil {
		return nil, fmt.Errorf("platformstore: list team sessions: %w", err)
	}
	if len(sids) == 0 {
		return nil, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE user_session SET revoked_at = ? WHERE user_id = ? AND team_id = ? AND revoked_at IS NULL`,
		now.UTC(), uid, tid); err != nil {
		return nil, fmt.Errorf("platformstore: revoke team sessions: %w", err)
	}
	return sids, nil
}

// ErrCredentialsChanged: the account's credential generation moved (a password change committed)
// between reading the account and writing what the old password was supposed to buy.
var ErrCredentialsChanged = errors.New("platformstore: credentials changed")

// CredentialChange is a committed password change: the account's new credential generation (legacy
// team #1 tokens carry it) and the sessions it revoked, which the caller drops from its cache.
type CredentialChange struct {
	Gen  int64
	SIDs []string
}

// teamOneUser is the platform user behind a team #1 admin.
type teamOneUser struct {
	ID     int64  `db:"id"`
	Hash   string `db:"password_hash"`
	Gen    int64  `db:"credential_gen"`
	Status string `db:"status"`
}

// teamOneUserSQL finds the platform user of a team #1 admin by handle THROUGH team #1 membership
// (any membership status): a platform user who only shares the handle is never touched.
const teamOneUserSQL = `SELECT u.id, u.password_hash, u.credential_gen, u.status
FROM platform_user u JOIN team_member m ON m.user_id = u.id AND m.team_id = 1
WHERE u.handle = ?`

// changeCredentialsTx is the one credential write: the hash (when given; password_changed_at is now
// for a usable one, NULL for an unusable one), credential_gen + 1, every live session revoked, every
// unused hand-off, provider sign-in code and verify letter expired (a verify letter signs in: one
// held from before the change must not open a session on the new generation; a pending team still
// waiting gets a fresh letter by ResendVerification), and every Google connect in flight ended.
// Inside the caller's transaction.
func changeCredentialsTx(ctx context.Context, tx *sqlx.Tx, userID int64, hash string, now time.Time) (CredentialChange, error) {
	now = now.UTC()
	if hash != "" {
		var changed any = now
		if IsUnusableHash(hash) {
			changed = nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET password_hash = ?, credential_gen = credential_gen + 1,
password_changed_at = ? WHERE id = ?`, hash, changed, userID); err != nil {
			return CredentialChange{}, fmt.Errorf("platformstore: set password: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET credential_gen = credential_gen + 1 WHERE id = ?`, userID); err != nil {
		return CredentialChange{}, fmt.Errorf("platformstore: bump credentials: %w", err)
	}
	if err := expireLinkFlowsTx(ctx, tx, userID, now); err != nil {
		return CredentialChange{}, err
	}
	var c CredentialChange
	if err := tx.GetContext(ctx, &c.Gen, `SELECT credential_gen FROM platform_user WHERE id = ?`, userID); err != nil {
		return CredentialChange{}, fmt.Errorf("platformstore: read credential generation: %w", err)
	}
	if err := tx.SelectContext(ctx, &c.SIDs,
		`SELECT sid FROM user_session WHERE user_id = ? AND revoked_at IS NULL FOR UPDATE`, userID); err != nil {
		return CredentialChange{}, fmt.Errorf("platformstore: live sessions: %w", err)
	}
	if len(c.SIDs) > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE user_session SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID); err != nil {
			return CredentialChange{}, fmt.Errorf("platformstore: revoke sessions: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE email_token SET used_at = ? WHERE user_id = ? AND purpose IN ('handoff', 'signin', 'verify') AND used_at IS NULL`, now, userID); err != nil {
		return CredentialChange{}, fmt.Errorf("platformstore: expire hand-offs, sign-in codes and verify letters: %w", err)
	}
	return c, nil
}

// keptChainChange is a committed changeCredentialsKeepChainTx: the generation, the ended sessions
// and how many live devices they were.
type keptChainChange struct {
	Gen     int64
	SIDs    []string
	Devices int
}

// changeCredentialsKeepChainTx is changeCredentialsTx for a person changing their own password (P0
// §3.4): the new hash with password_changed_at now and credential_gen + 1 (legacy team #1 tokens of
// the old generation die, a sign-in that checked the old password opens no session), every unused
// hand-off, sign-in code, verify and reset letter expired, every Google connect in flight ended.
// Sessions: signOutOthers ends every one outside keepChain (revokeOtherDevicesTx); false ends none.
// The caller's own sid token lives on (gen is checked for legacy tokens only). Inside the caller's
// transaction, which holds the account FOR UPDATE.
func changeCredentialsKeepChainTx(ctx context.Context, tx *sqlx.Tx, userID int64, hash, keepChain string, signOutOthers bool,
	live SessionLiveness, now time.Time) (keptChainChange, error) {
	now = now.UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET password_hash = ?, credential_gen = credential_gen + 1,
password_changed_at = ? WHERE id = ?`, hash, now, userID); err != nil {
		return keptChainChange{}, fmt.Errorf("platformstore: change password: %w", err)
	}
	var c keptChainChange
	if err := tx.GetContext(ctx, &c.Gen, `SELECT credential_gen FROM platform_user WHERE id = ?`, userID); err != nil {
		return keptChainChange{}, fmt.Errorf("platformstore: read credential generation: %w", err)
	}
	if signOutOthers {
		d, err := revokeOtherDevicesTx(ctx, tx, userID, keepChain, live, now)
		if err != nil {
			return keptChainChange{}, err
		}
		c.SIDs, c.Devices = d.SIDs, d.Devices
	}
	if _, err := tx.ExecContext(ctx, `UPDATE email_token SET used_at = ?
WHERE user_id = ? AND purpose IN ('handoff', 'signin', 'verify', 'reset') AND used_at IS NULL`, now, userID); err != nil {
		return keptChainChange{}, fmt.Errorf("platformstore: expire codes and letters: %w", err)
	}
	if err := expireLinkFlowsTx(ctx, tx, userID, now); err != nil {
		return keptChainChange{}, err
	}
	return c, nil
}

// SetPasswordAndRevokeSessions is the authoritative password write of a team #1 admin in teams
// mode: the platform user linked to team #1 under this handle is locked, verify checks the current
// password against ITS hash (the tenant admins hash is only a mirror and may lag), then
// changeCredentialsTx runs, all in ONE transaction. ErrUserNotFound when no team #1 member has the
// handle (a platform user who only shares it is never touched); ErrPasswordMismatch (wrapping
// verify's error) when verify refuses, and nothing changes. verify nil = an admin resetting
// another account (the caller checked its right to).
func (s *Store) SetPasswordAndRevokeSessions(ctx context.Context, handle, hash string, verify func(current string) error, now time.Time) (CredentialChange, error) {
	if strings.TrimSpace(hash) == "" {
		return CredentialChange{}, fmt.Errorf("platformstore: empty password hash")
	}
	var c CredentialChange
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var u teamOneUser
		if err := tx.GetContext(ctx, &u, teamOneUserSQL+` FOR UPDATE`, handle); err != nil {
			if isNoRows(err) {
				return ErrUserNotFound
			}
			return fmt.Errorf("platformstore: find team #1 user: %w", err)
		}
		if verify != nil {
			if err := verify(u.Hash); err != nil {
				return fmt.Errorf("%w: %w", ErrPasswordMismatch, err)
			}
		}
		var err error
		c, err = changeCredentialsTx(ctx, tx, u.ID, hash, now)
		return err
	})
	if err != nil {
		return CredentialChange{}, err
	}
	return c, nil
}

// TeamOneCred is the control-plane side of a team #1 admin's credentials.
type TeamOneCred struct {
	Hash     string
	Gen      int64
	Disabled bool
}

// TeamOneCredentials reads the control-plane hash, credential generation and status of the team #1
// admin with this handle: the old /api/auth/login checks the password against it (the tenant
// admins row is only a mirror). linked=false: no team #1 member has the handle.
func (s *Store) TeamOneCredentials(ctx context.Context, handle string) (TeamOneCred, bool, error) {
	var u teamOneUser
	if err := s.db.GetContext(ctx, &u, teamOneUserSQL, handle); err != nil {
		if isNoRows(err) {
			return TeamOneCred{}, false, nil
		}
		return TeamOneCred{}, false, fmt.Errorf("platformstore: team #1 credentials: %w", err)
	}
	// A deleted account signs in nowhere, the legacy login included, until it is restored.
	return TeamOneCred{Hash: u.Hash, Gen: u.Gen, Disabled: u.Status == UserDisabled || u.Status == UserDeleted}, true, nil
}

// LegacyCredGen is the credential generation a legacy team #1 token must carry to be valid: the
// linked platform user's credential_gen, 0 for a team #1 admin with no linked platform user.
func (s *Store) LegacyCredGen(ctx context.Context, handle string) (int64, error) {
	var gens []int64
	if err := s.db.SelectContext(ctx, &gens, `SELECT u.credential_gen
FROM platform_user u JOIN team_member m ON m.user_id = u.id AND m.team_id = 1
WHERE u.handle = ?`, handle); err != nil {
		return 0, fmt.Errorf("platformstore: credential generation: %w", err)
	}
	if len(gens) == 0 {
		return 0, nil
	}
	return gens[0], nil
}

// LinkTeamOneAdmin gives a team #1 admin created in the admin panel its control-plane account, in
// one transaction: a free handle → a new platform user (no email) with a team #1 membership (owner
// for super, else member); a handle already linked to team #1 (an account re-created after a
// delete) → its password is set like a reset (changeCredentialsTx); a handle that belongs to a
// platform user outside team #1 → ErrHandleTaken and nothing changes. A disabled linked user stays
// disabled (an operator decided that).
func (s *Store) LinkTeamOneAdmin(ctx context.Context, handle, hash string, super bool, now time.Time) (CredentialChange, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" || len(handle) > 64 {
		return CredentialChange{}, fmt.Errorf("platformstore: admin handle %q does not fit", handle)
	}
	if strings.TrimSpace(hash) == "" {
		return CredentialChange{}, fmt.Errorf("platformstore: empty password hash")
	}
	var c CredentialChange
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var u struct {
			ID int64 `db:"id"`
		}
		err := tx.GetContext(ctx, &u, `SELECT id FROM platform_user WHERE handle = ? FOR UPDATE`, handle)
		switch {
		case err == nil:
			var member int
			if err := tx.GetContext(ctx, &member, `SELECT COUNT(*) FROM team_member WHERE team_id = 1 AND user_id = ?`, u.ID); err != nil {
				return fmt.Errorf("platformstore: team #1 membership: %w", err)
			}
			if member == 0 {
				return ErrHandleTaken
			}
			c, err = changeCredentialsTx(ctx, tx, u.ID, hash, now)
			return err
		case isNoRows(err):
			res, err := tx.ExecContext(ctx, `INSERT INTO platform_user (email, handle, password_hash, status, created_at)
VALUES (NULL, ?, ?, 'active', ?)`, handle, hash, now.UTC())
			if err != nil {
				return fmt.Errorf("platformstore: insert team #1 user: %w", err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("platformstore: team #1 user id: %w", err)
			}
			// One namespace (P0 §3.1): a handle someone shows as their @username is taken too.
			held, err := publicUsernameHeldTx(ctx, tx, handle)
			if err != nil {
				return err
			}
			if held {
				return ErrHandleTaken
			}
			role := "member"
			if super {
				role = "owner"
			}
			// A new team #1 member owes its first entry (P0 §4).
			if _, err := tx.ExecContext(ctx, `INSERT INTO team_member (team_id, user_id, role, status, entry_pending, created_at)
VALUES (1, ?, ?, 'active', 1, ?)`, id, role, now.UTC()); err != nil {
				return fmt.Errorf("platformstore: team #1 member: %w", err)
			}
			return nil
		default:
			return fmt.Errorf("platformstore: find user: %w", err)
		}
	})
	if err != nil {
		return CredentialChange{}, err
	}
	return c, nil
}
