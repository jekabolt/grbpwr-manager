package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// AccountRestore is a committed RestoreAccount: the account as it is now, the teams whose
// membership came back from frozen (the caller re-enables the person's admins row there, which
// only finishes a row its delete froze) and the teams that closed with the account and reopened.
type AccountRestore struct {
	User     User
	Unfrozen []int64
	Reopened []int64
}

// deletedAccountErr is the answer to a deleted account met on a sign-in path after its holder proved
// it (password, provider): ErrAccountDeleted while it can still be restored, else ErrTokenInvalid.
func deletedAccountErr(u User, now time.Time) error {
	if _, ok := u.RestoreUntil(now); ok && !u.IsOperator {
		return ErrAccountDeleted
	}
	return ErrTokenInvalid
}

// closedTeam is a team row that closed together with an account, as RestoreAccount locks it.
type closedTeam struct {
	ID          int64          `db:"id"`
	Status      string         `db:"status"`
	DeletedFrom sql.NullString `db:"deleted_from"`
	PurgeAt     sql.NullTime   `db:"purge_at"`
}

// lockClosedTeamsTx locks, FOR UPDATE in id order, every team that closed with the account: the
// first locks of RestoreAccount (lock order: team rows before the account). Console RestoreTeam
// refuses these teams while the account is deleted, so nothing else reopens them meanwhile.
func lockClosedTeamsTx(ctx context.Context, tx *sqlx.Tx, userID int64) ([]closedTeam, error) {
	var teams []closedTeam
	if err := tx.SelectContext(ctx, &teams, `SELECT id, status, deleted_from, purge_at FROM team
WHERE closed_with_user_id = ? ORDER BY id FOR UPDATE`, userID); err != nil {
		return nil, fmt.Errorf("platformstore: restore account: lock closed teams: %w", err)
	}
	return teams, nil
}

// restoreTx is the account half of RestoreAccount, inside the transaction that holds the closed
// teams (lockClosedTeamsTx), the account and its consumed restore letter. A deleted account whose
// window still runs goes back to active; its frozen memberships turn active with their roles; the
// closed teams whose wipe is still ahead reopen to the status they had; the root session rootSID
// opens. Anything else: ErrTokenInvalid (nothing written; the letter stays unconsumed).
func restoreTx(ctx context.Context, tx *sqlx.Tx, u User, closed []closedTeam, rootSID string, now time.Time) (AccountRestore, error) {
	if _, ok := u.RestoreUntil(now); !ok || u.IsOperator {
		return AccountRestore{}, ErrTokenInvalid
	}
	res := AccountRestore{}
	for _, t := range closed {
		if t.Status != "deleted" || !t.PurgeAt.Valid || !t.PurgeAt.Time.After(now) {
			continue
		}
		if err := reopenTeamTx(ctx, tx, t.ID, restoredStatus(t.DeletedFrom)); err != nil {
			return AccountRestore{}, err
		}
		res.Reopened = append(res.Reopened, t.ID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE platform_user SET status = 'active', deleted_at = NULL, purge_at = NULL
WHERE id = ? AND status = 'deleted'`, u.ID); err != nil {
		return AccountRestore{}, fmt.Errorf("platformstore: restore account: %w", err)
	}
	if err := tx.SelectContext(ctx, &res.Unfrozen, `SELECT team_id FROM team_member
WHERE user_id = ? AND status = 'frozen' ORDER BY team_id FOR UPDATE`, u.ID); err != nil {
		return AccountRestore{}, fmt.Errorf("platformstore: restore account: frozen memberships: %w", err)
	}
	if len(res.Unfrozen) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE team_member SET status = 'active'
WHERE user_id = ? AND status = 'frozen'`, u.ID); err != nil {
			return AccountRestore{}, fmt.Errorf("platformstore: restore account: memberships: %w", err)
		}
	}
	if err := createSessionTx(ctx, tx, rootSID, u.ID, 0, SessionRoot, "", u.CredGen, now); err != nil {
		return AccountRestore{}, err
	}
	// Every other restore letter of the account dies with this one.
	if _, err := tx.ExecContext(ctx, `UPDATE email_token SET used_at = ?
WHERE user_id = ? AND purpose = 'restore' AND used_at IS NULL`, now, u.ID); err != nil {
		return AccountRestore{}, fmt.Errorf("platformstore: restore account: retire letters: %w", err)
	}
	u.Status, u.PurgeAt, u.DeletedAt = UserActive, sql.NullTime{}, sql.NullTime{}
	res.User = u
	return res, nil
}

// RestoreByLink restores a deleted account by its restore letter's link (P0 §3.5) and opens the
// root session rootSID. One transaction in lock order: the teams that closed with the account →
// the account → its token → memberships → the session. ErrTokenInvalid for a dead, used or unknown
// link, an account that is not deleted, or a window already over.
func (s *Store) RestoreByLink(ctx context.Context, tokenHash string, now time.Time, rootSID string) (AccountRestore, error) {
	now = now.UTC()
	var (
		res    AccountRestore
		closed []closedTeam
	)
	err := s.withLinkAfter(ctx, "restore", tokenHash, now, func(tx *sqlx.Tx, userID int64) error {
		var err error
		closed, err = lockClosedTeamsTx(ctx, tx, userID)
		return err
	}, func(tx *sqlx.Tx, _ tokenRow, u User) error {
		var err error
		res, err = restoreTx(ctx, tx, u, closed, rootSID, now)
		return err
	})
	return s.restored(res, err)
}

// RestoreByCode restores a deleted account by the code of its restore letter (a.Email is the
// account's stored email, userID the account the caller resolved from the typed login) and opens
// the root session rootSID. A wrong code counts against the letter and the account as every code
// does (withCode). ErrTokenInvalid as RestoreByLink.
func (s *Store) RestoreByCode(ctx context.Context, a CodeAttempt, userID int64, rootSID string) (AccountRestore, error) {
	a.Purpose = "restore"
	a.Now = a.Now.UTC()
	var (
		res    AccountRestore
		closed []closedTeam
	)
	err := s.withCodeAfter(ctx, a, func(tx *sqlx.Tx) error {
		var err error
		closed, err = lockClosedTeamsTx(ctx, tx, userID)
		return err
	}, func(tx *sqlx.Tx, _ tokenRow, u User) error {
		if u.ID != userID {
			return ErrTokenInvalid
		}
		var err error
		res, err = restoreTx(ctx, tx, u, closed, rootSID, a.Now)
		return err
	})
	return s.restored(res, err)
}

// restored drops the reopened teams from the directory cache after the commit.
func (s *Store) restored(res AccountRestore, err error) (AccountRestore, error) {
	if err != nil {
		return AccountRestore{}, lockConflict(err)
	}
	for _, id := range res.Reopened {
		s.dir.Invalidate(id)
	}
	return res, nil
}

// RequestAccountRestore queues a restore letter (link + code) for a deleted account whose window
// still runs, to its stored email. It reports whether one was queued (false: any other account, no
// email, or the hourly letter cap).
func (s *Store) RequestAccountRestore(ctx context.Context, userID int64, now time.Time, maxPerHour int, mk MakeLetter) (bool, error) {
	now = now.UTC()
	sent := false
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockUserByID(ctx, tx, userID)
		if errors.Is(err, ErrUserNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, ok := u.RestoreUntil(now); !ok || u.IsOperator || !u.Email.Valid || u.Email.String == "" {
			return nil
		}
		n, err := lettersSinceTx(ctx, tx, u.Email.String, now.Add(-time.Hour))
		if err != nil {
			return err
		}
		if n >= maxPerHour {
			return nil
		}
		if err := restoreLetterTx(ctx, tx, u, mk, false, now); err != nil {
			return err
		}
		sent = true
		return nil
	})
	return sent, err
}

// restoreLetterTx inserts a restore token and its letter for the locked deleted account u, to its
// stored email, with the end of its window; withDeleted adds when the account was deleted (the
// letter a sign-up with the account's email gets).
func restoreLetterTx(ctx context.Context, tx *sqlx.Tx, u User, mk MakeLetter, withDeleted bool, now time.Time) error {
	if mk == nil || !u.Email.Valid || u.Email.String == "" || !u.PurgeAt.Valid {
		return nil
	}
	tok, l, err := mk(u.ID, nil)
	if err != nil {
		return err
	}
	l.To, l.Template, l.Until = u.Email.String, "restore", u.PurgeAt.Time
	if withDeleted && u.DeletedAt.Valid {
		l.Deleted = u.DeletedAt.Time
	}
	tok.Purpose, tok.TeamID = "restore", 0
	if err := insertTokenTx(ctx, tx, u.ID, tok, now); err != nil {
		return err
	}
	return insertLetterTx(ctx, tx, l, now)
}
