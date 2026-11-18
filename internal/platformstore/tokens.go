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
}

const tokenColumns = `id, user_id, team_id, purpose, code_hash, code_attempts, expires_at, used_at`

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
}

// verifyTx activates the user, moves the letter's team to provisioning (B4 claims it from there),
// adds the owner membership and inserts the hand-off.
func verifyTx(ctx context.Context, tx *sqlx.Tx, t tokenRow, u User, handoff NewToken, now time.Time) (VerifyResult, error) {
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
	if _, err := tx.ExecContext(ctx, `UPDATE team SET status = 'provisioning', pending_expires_at = NULL WHERE id = ?`, team.ID); err != nil {
		return VerifyResult{}, fmt.Errorf("platformstore: team to provisioning: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_member (team_id, user_id, role, status, created_at)
VALUES (?, ?, 'owner', 'active', ?) ON DUPLICATE KEY UPDATE role = 'owner', status = 'active'`, team.ID, u.ID, now); err != nil {
		return VerifyResult{}, fmt.Errorf("platformstore: owner membership: %w", err)
	}
	handoff.TeamID = team.ID
	if err := insertTokenTx(ctx, tx, u.ID, handoff, now); err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{UserID: u.ID, Handle: u.Handle, TeamID: team.ID, TeamSlug: team.Slug}, nil
}

// VerifyByLink confirms a sign-up by its link token hash and inserts the hand-off row.
func (s *Store) VerifyByLink(ctx context.Context, tokenHash string, now time.Time, handoff NewToken) (VerifyResult, error) {
	var res VerifyResult
	err := s.withLink(ctx, "verify", tokenHash, now, func(tx *sqlx.Tx, t tokenRow, u User) error {
		if err := clearCodeFailuresTx(ctx, tx, u.ID); err != nil {
			return err
		}
		r, err := verifyTx(ctx, tx, t, u, handoff, now)
		res = r
		return err
	})
	if err == nil {
		s.dir.Invalidate(res.TeamID)
	}
	return res, err
}

// VerifyByCode confirms a sign-up by (email, code) and inserts the hand-off row.
func (s *Store) VerifyByCode(ctx context.Context, a CodeAttempt, handoff NewToken) (VerifyResult, error) {
	var res VerifyResult
	a.Purpose = "verify"
	err := s.withCode(ctx, a, func(tx *sqlx.Tx, t tokenRow, u User) error {
		r, err := verifyTx(ctx, tx, t, u, handoff, a.Now)
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
}

func resetTx(ctx context.Context, tx *sqlx.Tx, u User, newHash string, now time.Time) (ResetResult, error) {
	if u.Status == UserDisabled {
		return ResetResult{}, ErrTokenInvalid
	}
	// A reset proves the inbox: a pending account (an operator seeded at boot is active) stays as is.
	if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET password_hash = ?,
email_verified_at = COALESCE(email_verified_at, ?) WHERE id = ?`, newHash, now, u.ID); err != nil {
		return ResetResult{}, fmt.Errorf("platformstore: set password: %w", err)
	}
	r, err := tx.ExecContext(ctx, `UPDATE user_session SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, u.ID)
	if err != nil {
		return ResetResult{}, fmt.Errorf("platformstore: revoke sessions: %w", err)
	}
	revoked, _ := r.RowsAffected()
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM team_member WHERE team_id = 1 AND user_id = ? AND status = 'active'`, u.ID); err != nil {
		return ResetResult{}, fmt.Errorf("platformstore: team one membership: %w", err)
	}
	return ResetResult{UserID: u.ID, Handle: u.Handle, TeamOneMem: n > 0, Revoked: revoked}, nil
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
