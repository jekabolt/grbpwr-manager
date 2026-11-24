package platformstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// DAY-30 PURGE OF A DELETED ACCOUNT (S4c, P0 §3.5 «Day 30», lanes/D3-design.md §3).
//
// An account deleted by its holder can be restored until purge_at. After that the day-30 loop erases
// what identifies the person and keeps a tombstone row with its handle (the allocator keeps skipping
// it), deleted_at and the terms fields; their authored data and the invites they sent stay. The
// email becomes free for a new account (new account, new handle).

// PurgeBatch bounds one PurgeDeletedAccounts round.
const PurgeBatch = 50

// DueAccount is an account whose purge is due (the dry run's plan).
type DueAccount struct {
	ID      int64     `db:"id"`
	Handle  string    `db:"handle"`
	PurgeAt time.Time `db:"purge_at"`
}

// DueAccountPurges lists up to limit accounts whose purge is due on the database clock: deleted,
// not purged, purge_at passed, never an operator. Unlocked: PurgeAccount re-checks under the lock.
func (s *Store) DueAccountPurges(ctx context.Context, limit int) ([]DueAccount, error) {
	var out []DueAccount
	if err := s.db.SelectContext(ctx, &out, `SELECT id, handle, purge_at FROM platform_user
WHERE status = 'deleted' AND purged_at IS NULL AND purge_at IS NOT NULL AND purge_at <= UTC_TIMESTAMP(3) AND is_operator = 0
ORDER BY purge_at, id LIMIT ?`, limit); err != nil {
		return nil, fmt.Errorf("platformstore: due account purges: %w", err)
	}
	return out, nil
}

// PurgeDeletedAccounts runs one round: up to batch due accounts, each purged in its own
// transaction with a fresh unusable password hash from unusable. It returns how many were purged and
// the errors joined (the others still ran). The caller repeats rounds while a full batch was due.
func (s *Store) PurgeDeletedAccounts(ctx context.Context, batch int, unusable func() (string, error)) (purged, due int, err error) {
	if unusable == nil {
		return 0, 0, errors.New("platformstore: purge needs an unusable password hash")
	}
	list, err := s.DueAccountPurges(ctx, batch)
	if err != nil {
		return 0, 0, err
	}
	var errs []error
	for _, a := range list {
		hash, err := unusable()
		if err != nil {
			return purged, len(list), err
		}
		ok, err := s.PurgeAccount(ctx, a.ID, hash)
		if err != nil {
			errs = append(errs, fmt.Errorf("account %s: %w", a.Handle, err))
			continue
		}
		if ok {
			purged++
		}
	}
	return purged, len(list), errors.Join(errs...)
}

// PurgeAccount erases one due account. One transaction in lock order: the account FOR UPDATE and
// the re-check (deleted, not purged, purge_at passed on the database clock, not an operator; else
// false, nothing written) → its provider identities → its memberships (frozen → removed) → its
// sessions (ip, user agent forgotten) → its tokens (deleted), then the rows outside the order:
// avatar, Google-connect tickets and flows, join requests' ip, join intent, letters to the old
// address, the ip of audit rows it acted in. The row keeps its handle, deleted_at and terms fields;
// email NULL, names empty, @username NULL, password unusable, counters reset, credential_gen + 1,
// purge_at NULL, purged_at now. Audit account_purge.
func (s *Store) PurgeAccount(ctx context.Context, userID int64, unusableHash string) (bool, error) {
	if unusableHash == "" {
		return false, errors.New("platformstore: purge needs an unusable password hash")
	}
	purged := false
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		var u struct {
			Handle     string         `db:"handle"`
			Email      sql.NullString `db:"email"`
			Status     string         `db:"status"`
			IsOperator bool           `db:"is_operator"`
			PurgedAt   sql.NullTime   `db:"purged_at"`
			Due        sql.NullBool   `db:"due"`
			Now        time.Time      `db:"now"`
		}
		if err := tx.GetContext(ctx, &u, `SELECT handle, email, status, is_operator, purged_at,
purge_at <= UTC_TIMESTAMP(3) AS due, UTC_TIMESTAMP(3) AS now FROM platform_user WHERE id = ? FOR UPDATE`, userID); err != nil {
			if isNoRows(err) {
				return nil
			}
			return fmt.Errorf("platformstore: purge: lock account: %w", err)
		}
		if u.Status != UserDeleted || u.IsOperator || u.PurgedAt.Valid || !u.Due.Valid || !u.Due.Bool {
			return nil
		}
		now := u.Now.UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET email = NULL, first_name = '', last_name = '',
public_username = NULL, password_hash = ?, email_verified_at = NULL, code_failures = 0, code_failures_since = NULL,
code_locked_until = NULL, credential_gen = credential_gen + 1, password_changed_at = NULL, purge_at = NULL, purged_at = ?
WHERE id = ?`, unusableHash, now, userID); err != nil {
			return fmt.Errorf("platformstore: purge: account: %w", err)
		}
		steps := []struct{ what, q string }{
			{"identities", `DELETE FROM platform_identity WHERE user_id = ?`},
			{"memberships", `UPDATE team_member SET status = 'removed' WHERE user_id = ? AND status = 'frozen'`},
			{"sessions", `UPDATE user_session SET ip = NULL, user_agent = NULL WHERE user_id = ?`},
			{"tokens", `DELETE FROM email_token WHERE user_id = ?`},
			{"avatar", `DELETE FROM account_avatar WHERE user_id = ?`},
			{"link tickets", `DELETE FROM oauth_link_ticket WHERE user_id = ?`},
			{"link flows", `DELETE FROM oauth_state WHERE link_user_id = ?`},
			{"join requests", `UPDATE team_join_request SET ip = NULL WHERE user_id = ?`},
			{"join intent", `DELETE FROM platform_join_intent WHERE user_id = ?`},
			{"audit ip", `UPDATE platform_audit SET ip = NULL WHERE actor_user_id = ? AND ip IS NOT NULL`},
		}
		for _, st := range steps {
			if _, err := tx.ExecContext(ctx, st.q, userID); err != nil {
				return fmt.Errorf("platformstore: purge: %s: %w", st.what, err)
			}
		}
		if u.Email.Valid && u.Email.String != "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM platform_email WHERE to_email = ?`, u.Email.String); err != nil {
				return fmt.Errorf("platformstore: purge: letters: %w", err)
			}
		}
		detail, _ := json.Marshal(map[string]any{"handle": u.Handle})
		if _, err := tx.ExecContext(ctx, `INSERT INTO platform_audit (actor_user_id, team_id, action, detail, created_at)
VALUES (NULL, NULL, 'account_purge', ?, ?)`, string(detail), now); err != nil {
			return fmt.Errorf("platformstore: purge: audit: %w", err)
		}
		purged = true
		return nil
	})
	if err != nil {
		return false, lockConflict(err)
	}
	return purged, nil
}
