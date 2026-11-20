package platformstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// Avatar is an account's photo (P0 §3.2): PNG bytes already checked and fitted by the caller, and the
// hex sha256 of exactly those bytes (its public URL and ETag).
type Avatar struct {
	PNG    []byte
	SHA256 string
}

// AvatarMeta is enough for the public route to answer HEAD and evaluate an ETag without reading the
// MEDIUMBLOB.
type AvatarMeta struct {
	Size      int64     `db:"size"`
	UpdatedAt time.Time `db:"updated_at"`
}

// SetAvatar sets (a non-nil avatar) or removes (nil) an active account's avatar in one transaction
// that holds the account row first (lock order: account before its own rows). ErrUserNotFound when
// the account is missing or not active.
func (s *Store) SetAvatar(ctx context.Context, userID int64, a *Avatar) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockUserByID(ctx, tx, userID)
		if err != nil {
			return err
		}
		if u.Status != UserActive {
			return ErrUserNotFound
		}
		if a == nil || len(a.PNG) == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM account_avatar WHERE user_id = ?`, userID); err != nil {
				return fmt.Errorf("platformstore: delete avatar: %w", err)
			}
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_avatar (user_id, png, sha256, updated_at)
VALUES (?, ?, ?, UTC_TIMESTAMP(3)) AS nv
ON DUPLICATE KEY UPDATE png = nv.png, sha256 = nv.sha256, updated_at = nv.updated_at`, userID, a.PNG, a.SHA256); err != nil {
			return fmt.Errorf("platformstore: put avatar: %w", err)
		}
		return nil
	})
}

// AvatarMetaBySHA reads the size and time of the avatar with this sha256. Two accounts may hold the
// same picture; the bytes are the same, so any row answers. ok=false when no account has it (removed
// or replaced: its old URL is a 404).
func (s *Store) AvatarMetaBySHA(ctx context.Context, sha string) (AvatarMeta, bool, error) {
	var rows []AvatarMeta
	if err := s.db.SelectContext(ctx, &rows, `SELECT OCTET_LENGTH(png) AS size, updated_at
FROM account_avatar WHERE sha256 = ? ORDER BY updated_at LIMIT 1`, sha); err != nil {
		return AvatarMeta{}, false, fmt.Errorf("platformstore: avatar metadata: %w", err)
	}
	if len(rows) == 0 {
		return AvatarMeta{}, false, nil
	}
	return rows[0], true, nil
}

// AvatarPNGBySHA reads the avatar bytes with this sha256 (ok=false when none).
func (s *Store) AvatarPNGBySHA(ctx context.Context, sha string) ([]byte, bool, error) {
	var rows [][]byte
	if err := s.db.SelectContext(ctx, &rows, `SELECT png FROM account_avatar WHERE sha256 = ? LIMIT 1`, sha); err != nil {
		return nil, false, fmt.Errorf("platformstore: avatar content: %w", err)
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	return rows[0], true, nil
}
