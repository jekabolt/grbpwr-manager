package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

type tokenRow struct {
	ID           int64          `db:"id"`
	UserID       int64          `db:"user_id"`
	TeamID       sql.NullInt64  `db:"team_id"`
	Purpose      string         `db:"purpose"`
	CodeHash     sql.NullString `db:"code_hash"`
	CodeAttempts int            `db:"code_attempts"`
	ExpiresAt    time.Time      `db:"expires_at"`
	UsedAt       sql.NullTime   `db:"used_at"`
	// ParentSID is the root session a hand-off code was issued from (NULL = none).
	ParentSID sql.NullString `db:"parent_sid"`
}

const tokenColumns = `id, user_id, team_id, purpose, code_hash, code_attempts, expires_at, used_at, parent_sid`

// CodeAttempt is one typed (email, code) pair against a letter of Purpose.
type CodeAttempt struct {
	Purpose, Email string
	Now            time.Time
	MaxRowAttempts int
	// Match compares the typed code with a stored hash bound to userID. It is also called with
	// userID 0 and an empty hash when there is nothing to compare, so every path costs one HMAC.
	Match func(userID int64, storedHash string) bool
	// Fail returns the user's counters after one more wrong code.
	Fail func(c CodeCounters) CodeCounters
	// Locked reports whether counters lock the code path at Now.
	Locked func(c CodeCounters) bool
}

// consumeTx marks a token used; exactly one row must change, else ErrTokenInvalid.
func consumeTx(ctx context.Context, tx *sqlx.Tx, id int64, now time.Time) error {
	r, err := tx.ExecContext(ctx, `UPDATE email_token SET used_at = ? WHERE id = ? AND used_at IS NULL AND expires_at > ?`,
		now, id, now)
	if err != nil {
		return fmt.Errorf("platformstore: consume token: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return fmt.Errorf("platformstore: consume token: %w", err)
	}
	if n != 1 {
		return ErrTokenInvalid
	}
	return nil
}

func clearCodeFailuresTx(ctx context.Context, tx *sqlx.Tx, userID int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET code_failures = 0, code_failures_since = NULL,
code_locked_until = NULL WHERE id = ?`, userID); err != nil {
		return fmt.Errorf("platformstore: clear code failures: %w", err)
	}
	return nil
}

// withLink finds a live token of purpose by its link hash, locks its user then the token (the same
// order as the code path: user first), consumes it and runs fn inside the same transaction.
func (s *Store) withLink(ctx context.Context, purpose, tokenHash string, now time.Time,
	fn func(tx *sqlx.Tx, t tokenRow, u User) error) error {
	var uid int64
	if err := s.db.GetContext(ctx, &uid, `SELECT user_id FROM email_token WHERE token_hash = ? AND purpose = ?`,
		tokenHash, purpose); err != nil {
		if isNoRows(err) {
			return ErrTokenInvalid
		}
		return fmt.Errorf("platformstore: find token: %w", err)
	}
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockUserByID(ctx, tx, uid)
		if errors.Is(err, ErrUserNotFound) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		var t tokenRow
		if err := tx.GetContext(ctx, &t, `SELECT `+tokenColumns+` FROM email_token
WHERE token_hash = ? AND purpose = ? FOR UPDATE`, tokenHash, purpose); err != nil {
			if isNoRows(err) {
				return ErrTokenInvalid
			}
			return fmt.Errorf("platformstore: lock token: %w", err)
		}
		if t.UsedAt.Valid || !t.ExpiresAt.After(now) || t.UserID != u.ID {
			return ErrTokenInvalid
		}
		if err := consumeTx(ctx, tx, t.ID, now); err != nil {
			return err
		}
		return fn(tx, t, u)
	})
}

// withCode checks a typed code against the user's newest live letter of the purpose. A wrong code
// counts against the letter (dead at MaxRowAttempts) and the user (Fail/Locked) and commits those
// counters before answering ErrTokenInvalid; a right one consumes the letter, clears the user's
// counters and runs fn in the same transaction.
func (s *Store) withCode(ctx context.Context, a CodeAttempt, fn func(tx *sqlx.Tx, t tokenRow, u User) error) error {
	wrong := false
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockUserByEmail(ctx, tx, a.Email)
		if errors.Is(err, ErrUserNotFound) {
			a.Match(0, "")
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if a.Locked(u.Counters()) {
			a.Match(0, "")
			return ErrTokenInvalid
		}
		var t tokenRow
		err = tx.GetContext(ctx, &t, `SELECT `+tokenColumns+` FROM email_token
WHERE user_id = ? AND purpose = ? AND used_at IS NULL AND expires_at > ? AND code_hash IS NOT NULL
ORDER BY id DESC LIMIT 1 FOR UPDATE`, u.ID, a.Purpose, a.Now)
		found := err == nil
		if err != nil && !isNoRows(err) {
			return fmt.Errorf("platformstore: lock code token: %w", err)
		}
		if found && t.CodeAttempts < a.MaxRowAttempts && a.Match(u.ID, t.CodeHash.String) {
			if err := consumeTx(ctx, tx, t.ID, a.Now); err != nil {
				return err
			}
			if err := clearCodeFailuresTx(ctx, tx, u.ID); err != nil {
				return err
			}
			return fn(tx, t, u)
		}
		if !found || t.CodeAttempts >= a.MaxRowAttempts {
			a.Match(0, "")
		}
		// Wrong: count it on the letter and on the user, and keep those counts.
		if found {
			if _, err := tx.ExecContext(ctx, `UPDATE email_token SET code_attempts = code_attempts + 1 WHERE id = ?`, t.ID); err != nil {
				return fmt.Errorf("platformstore: count code attempt: %w", err)
			}
		}
		c := a.Fail(u.Counters())
		if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET code_failures = ?, code_failures_since = ?,
code_locked_until = ? WHERE id = ?`, c.Failures, c.Since, c.LockedUntil, u.ID); err != nil {
			return fmt.Errorf("platformstore: count code failure: %w", err)
		}
		wrong = true
		return nil
	})
	if err != nil {
		return err
	}
	if wrong {
		return ErrTokenInvalid
	}
	return nil
}

// VerifyResult is a confirmed sign-up: the workspace to hand the person to.
type VerifyResult struct {
	UserID   int64
	Handle   string
	TeamID   int64
	TeamSlug string
	// AwaitingApproval: the team went to review; no hand-off was inserted.
	AwaitingApproval bool
	// RootSession: this confirmation opened the root session the caller named (a first-time
	// confirmation of a non-operator account); the caller mints its token. Never set on the repeat
	// of a used link (verifiedAwaiting).
	RootSession bool
}

// Review is the sign-up approval switch (PLATFORM_SIGNUP_APPROVAL). On: a confirmed team waits in
// review and every active operator gets a team_request letter whose link is ConsoleLink.
type Review struct {
	On          bool
	ConsoleLink string
}

// verifyTx activates the user and adds the owner membership. Without review it moves the letter's
// team to provisioning (B4 claims it from there) and inserts the hand-off; with review the team
// goes to review instead, the operators get a team_request letter each, and no hand-off is made.
//
// Either way, when rootSID is set and the account is not an operator, it opens the root session
// rootSID (the row the password sign-in opens, on the credential generation read under the
// account's lock): the person is signed in by the code. An operator signs in with the password
// only, as on the provider path. Reached only through a letter consumed in this transaction, so a
// used, expired or wrong code or link never gets here, nor a letter from before a password change
// (changeCredentialsTx expires it).
func verifyTx(ctx context.Context, tx *sqlx.Tx, t tokenRow, u User, handoff NewToken, rootSID string, review Review,
	now time.Time) (VerifyResult, error) {
	if u.Status == UserDisabled || !t.TeamID.Valid {
		return VerifyResult{}, ErrTokenInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET status = 'active',
email_verified_at = COALESCE(email_verified_at, ?) WHERE id = ?`, now, u.ID); err != nil {
		return VerifyResult{}, fmt.Errorf("platformstore: activate user: %w", err)
	}
	var team TeamBrief
	if err := tx.GetContext(ctx, &team, `SELECT id, slug, name, status, COALESCE(created_by, 0) AS created_by
FROM team WHERE id = ? FOR UPDATE`, t.TeamID.Int64); err != nil {
		if isNoRows(err) {
			return VerifyResult{}, ErrTokenInvalid
		}
		return VerifyResult{}, fmt.Errorf("platformstore: lock team: %w", err)
	}
	if team.Status != "pending" || team.CreatedBy != u.ID {
		return VerifyResult{}, ErrTokenInvalid
	}
	next := "provisioning"
	if review.On {
		next = "review"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE team SET status = ?, pending_expires_at = NULL WHERE id = ?`, next, team.ID); err != nil {
		return VerifyResult{}, fmt.Errorf("platformstore: team to %s: %w", next, err)
	}
	// The confirmed team's owner owes its first entry (P0 §4); an owner membership already active
	// keeps its flag (entry_pending is assigned before status, so it reads the old status).
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_member (team_id, user_id, role, status, entry_pending, created_at)
VALUES (?, ?, 'owner', 'active', 1, ?)
ON DUPLICATE KEY UPDATE role = 'owner', entry_pending = IF(status = 'active', entry_pending, 1), status = 'active'`,
		team.ID, u.ID, now); err != nil {
		return VerifyResult{}, fmt.Errorf("platformstore: owner membership: %w", err)
	}
	root := false
	if rootSID != "" && !u.IsOperator {
		if err := createSessionTx(ctx, tx, rootSID, u.ID, 0, SessionRoot, "", u.CredGen, now); err != nil {
			return VerifyResult{}, err
		}
		root = true
	}
	if review.On {
		var operators []string
		if err := tx.SelectContext(ctx, &operators, `SELECT email FROM platform_user
WHERE is_operator = 1 AND status = 'active' AND email IS NOT NULL ORDER BY id`); err != nil {
			return VerifyResult{}, fmt.Errorf("platformstore: operators: %w", err)
		}
		for _, to := range operators {
			if err := insertTeamLetterTx(ctx, tx, to, "team_request", teamLetterPayload{
				Link: review.ConsoleLink, TeamName: team.Name, Slug: team.Slug, Email: u.Email.String,
			}, now); err != nil {
				return VerifyResult{}, err
			}
		}
		return VerifyResult{UserID: u.ID, Handle: u.Handle, TeamID: team.ID, TeamSlug: team.Slug, AwaitingApproval: true,
			RootSession: root}, nil
	}
	handoff.TeamID = team.ID
	if err := insertTokenTx(ctx, tx, u.ID, handoff, now); err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{UserID: u.ID, Handle: u.Handle, TeamID: team.ID, TeamSlug: team.Slug, RootSession: root}, nil
}

// VerifyByLink confirms a sign-up by its link token hash and inserts the hand-off row (or, under
// review, queues the operators' letters), and opens the root session rootSID (see verifyTx). With
// review on, a second click on a used, unexpired link whose team still waits in review answers the
// same result again and writes nothing: no session.
func (s *Store) VerifyByLink(ctx context.Context, tokenHash string, now time.Time, handoff NewToken, rootSID string,
	review Review) (VerifyResult, error) {
	var res VerifyResult
	err := s.withLink(ctx, "verify", tokenHash, now, func(tx *sqlx.Tx, t tokenRow, u User) error {
		if err := clearCodeFailuresTx(ctx, tx, u.ID); err != nil {
			return err
		}
		r, err := verifyTx(ctx, tx, t, u, handoff, rootSID, review, now)
		res = r
		return err
	})
	if errors.Is(err, ErrTokenInvalid) && review.On {
		if again, ok, aerr := s.verifiedAwaiting(ctx, tokenHash, now); aerr != nil {
			return VerifyResult{}, aerr
		} else if ok {
			return again, nil
		}
	}
	if err == nil {
		s.dir.Invalidate(res.TeamID)
	}
	return res, err
}

// verifiedAwaiting is the repeat of an already used verify link: its team, while it still waits in
// review for the account that confirmed it and the link has not expired.
func (s *Store) verifiedAwaiting(ctx context.Context, tokenHash string, now time.Time) (VerifyResult, bool, error) {
	var row struct {
		UserID int64  `db:"user_id"`
		Handle string `db:"handle"`
		TeamID int64  `db:"team_id"`
		Slug   string `db:"slug"`
	}
	err := s.db.GetContext(ctx, &row, `SELECT e.user_id, u.handle, t.id AS team_id, t.slug
FROM email_token e JOIN team t ON t.id = e.team_id JOIN platform_user u ON u.id = e.user_id
WHERE e.token_hash = ? AND e.purpose = 'verify' AND e.used_at IS NOT NULL AND e.expires_at > ?
  AND t.status = 'review' AND t.created_by = e.user_id AND u.status = 'active'`, tokenHash, now)
	if isNoRows(err) {
		return VerifyResult{}, false, nil
	}
	if err != nil {
		return VerifyResult{}, false, fmt.Errorf("platformstore: repeated verify: %w", err)
	}
	return VerifyResult{UserID: row.UserID, Handle: row.Handle, TeamID: row.TeamID, TeamSlug: row.Slug, AwaitingApproval: true}, true, nil
}

// VerifyByCode confirms a sign-up by (email, code) and inserts the hand-off row (or, under review,
// queues the operators' letters), and opens the root session rootSID (see verifyTx).
func (s *Store) VerifyByCode(ctx context.Context, a CodeAttempt, handoff NewToken, rootSID string, review Review) (VerifyResult, error) {
	var res VerifyResult
	a.Purpose = "verify"
	err := s.withCode(ctx, a, func(tx *sqlx.Tx, t tokenRow, u User) error {
		r, err := verifyTx(ctx, tx, t, u, handoff, rootSID, review, a.Now)
		res = r
		return err
	})
	if err == nil {
		s.dir.Invalidate(res.TeamID)
	}
	return res, err
}

// ResetResult is a changed password: the account, and whether it is a member of team #1 (whose
// admins row the caller then updates so the old Login keeps working).
type ResetResult struct {
	UserID     int64
	Handle     string
	TeamOneMem bool
	Revoked    int64
	// SIDs are the sessions the reset revoked: the caller drops them from the edge cache
	// (tenantbind.Binder.InvalidateSession), which otherwise keeps them live for its TTL.
	SIDs []string
	// Gen is the account's new credential generation.
	Gen int64
}

func resetTx(ctx context.Context, tx *sqlx.Tx, u User, newHash string, now time.Time) (ResetResult, error) {
	// No hash: the caller only counts a miss its preflight saw; a code that matches now is not consumed.
	if u.Status == UserDisabled || newHash == "" {
		return ResetResult{}, ErrTokenInvalid
	}
	// A reset proves the inbox: a pending account (an operator seeded at boot is active) stays as is.
	if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET email_verified_at = COALESCE(email_verified_at, ?) WHERE id = ?`,
		now, u.ID); err != nil {
		return ResetResult{}, fmt.Errorf("platformstore: verify email: %w", err)
	}
	// New hash, credential_gen + 1 (legacy team #1 tokens of the old generation die), sessions
	// revoked, unused hand-off codes and verify letters expired — one transaction.
	c, err := changeCredentialsTx(ctx, tx, u.ID, newHash, now)
	if err != nil {
		return ResetResult{}, err
	}
	revoked := int64(len(c.SIDs))
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM team_member WHERE team_id = 1 AND user_id = ? AND status = 'active'`, u.ID); err != nil {
		return ResetResult{}, fmt.Errorf("platformstore: team one membership: %w", err)
	}
	return ResetResult{UserID: u.ID, Handle: u.Handle, TeamOneMem: n > 0, Revoked: revoked, SIDs: c.SIDs, Gen: c.Gen}, nil
}

// ResetLinkLive is a read-only check that tokenHash is a live reset link of an account that is not
// disabled. It runs before the password is hashed; ResetByLink re-checks under locks.
func (s *Store) ResetLinkLive(ctx context.Context, tokenHash string, now time.Time) (bool, error) {
	var n int
	if err := s.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM email_token t JOIN platform_user u ON u.id = t.user_id
WHERE t.token_hash = ? AND t.purpose = 'reset' AND t.used_at IS NULL AND t.expires_at > ? AND u.status <> ?`,
		tokenHash, now, UserDisabled); err != nil {
		return false, fmt.Errorf("platformstore: reset link: %w", err)
	}
	return n > 0, nil
}

// ResetCodeMatches is a read-only check that (email, code) matches the newest live reset letter of
// an unlocked account. It counts nothing: on false the caller runs ResetByCode with no hash, which
// counts the miss; on true it hashes and ResetByCode re-checks under locks.
func (s *Store) ResetCodeMatches(ctx context.Context, a CodeAttempt) (bool, error) {
	u, err := s.UserByEmail(ctx, a.Email)
	if errors.Is(err, ErrUserNotFound) {
		a.Match(0, "")
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if u.Status == UserDisabled || a.Locked(u.Counters()) {
		a.Match(0, "")
		return false, nil
	}
	var t tokenRow
	err = s.db.GetContext(ctx, &t, `SELECT `+tokenColumns+` FROM email_token
WHERE user_id = ? AND purpose = 'reset' AND used_at IS NULL AND expires_at > ? AND code_hash IS NOT NULL
ORDER BY id DESC LIMIT 1`, u.ID, a.Now)
	if isNoRows(err) {
		a.Match(0, "")
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("platformstore: reset code: %w", err)
	}
	if t.CodeAttempts >= a.MaxRowAttempts {
		a.Match(0, "")
		return false, nil
	}
	return a.Match(u.ID, t.CodeHash.String), nil
}

// ResetByLink sets a new password by a reset link and revokes every session of the account.
func (s *Store) ResetByLink(ctx context.Context, tokenHash, newHash string, now time.Time) (ResetResult, error) {
	var res ResetResult
	err := s.withLink(ctx, "reset", tokenHash, now, func(tx *sqlx.Tx, _ tokenRow, u User) error {
		if err := clearCodeFailuresTx(ctx, tx, u.ID); err != nil {
			return err
		}
		r, err := resetTx(ctx, tx, u, newHash, now)
		res = r
		return err
	})
	return res, err
}

// ResetByCode sets a new password by (email, code) and revokes every session of the account.
func (s *Store) ResetByCode(ctx context.Context, a CodeAttempt, newHash string) (ResetResult, error) {
	var res ResetResult
	a.Purpose = "reset"
	err := s.withCode(ctx, a, func(tx *sqlx.Tx, _ tokenRow, u User) error {
		r, err := resetTx(ctx, tx, u, newHash, a.Now)
		res = r
		return err
	})
	return res, err
}
