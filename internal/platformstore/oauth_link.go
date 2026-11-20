package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Google connect and disconnect from the profile (P0 §3.7).

// LinkTicketTTL is how long a Google-connect ticket lives: the page form-POSTs it at once.
const LinkTicketTTL = 2 * time.Minute

// Google connect refusals. The caller maps each to its fixed key; nothing reaches a client as text.
var (
	// ErrAlreadyLinked: the account has a Google identity already.
	ErrAlreadyLinked = errors.New("platformstore: google already connected")
	// ErrLinkTaken: this Google account is connected to another account.
	ErrLinkTaken = errors.New("platformstore: google account connected elsewhere")
	// ErrOnlySignIn: Google is the account's only way in (no password, no other identity).
	ErrOnlySignIn = errors.New("platformstore: google is the only way in")
)

// CreateLinkTicket stores a Google-connect ticket (hex sha256 of the secret the page holds) for an
// active account without a Google identity, bound to the team host it returns to ("" = the root
// host), live for LinkTicketTTL. ErrAlreadyLinked when the account has Google; ErrUserNotFound when
// it is missing, not active or an operator's. Tickets dead for an hour are cleared first.
func (s *Store) CreateLinkTicket(ctx context.Context, ticketHash string, userID int64, returnSlug string, now time.Time) error {
	now = now.UTC()
	switch {
	case len(ticketHash) != 64 || userID <= 0:
		return errors.New("platformstore: a link ticket needs a hash and an account")
	case len(returnSlug) > 63:
		return errors.New("platformstore: return slug too long")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM oauth_link_ticket WHERE expires_at < ? LIMIT 500`, now.Add(-time.Hour)); err != nil {
		return fmt.Errorf("platformstore: clear old link tickets: %w", err)
	}
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if u.Status != UserActive || u.IsOperator {
		return ErrUserNotFound
	}
	var google int
	if err := s.db.GetContext(ctx, &google, `SELECT COUNT(*) FROM platform_identity WHERE user_id = ? AND provider = 'google'`,
		userID); err != nil {
		return fmt.Errorf("platformstore: link ticket: identities: %w", err)
	}
	if google > 0 {
		return ErrAlreadyLinked
	}
	var slug any
	if returnSlug != "" {
		slug = returnSlug
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO oauth_link_ticket (ticket_hash, user_id, return_slug, expires_at, created_at)
VALUES (?, ?, ?, ?, ?)`, ticketHash, userID, slug, now.Add(LinkTicketTTL), now); err != nil {
		return fmt.Errorf("platformstore: insert link ticket: %w", err)
	}
	return nil
}

// ConsumeLinkTicket spends a live ticket in one statement and returns whom it connects and where it
// returns. Unknown, spent and expired are one answer: ErrTokenInvalid.
func (s *Store) ConsumeLinkTicket(ctx context.Context, ticketHash string, now time.Time) (userID int64, returnSlug string, err error) {
	now = now.UTC()
	if len(ticketHash) != 64 {
		return 0, "", ErrTokenInvalid
	}
	r, err := s.db.ExecContext(ctx, `UPDATE oauth_link_ticket SET used_at = ?
WHERE ticket_hash = ? AND used_at IS NULL AND expires_at > ?`, now, ticketHash, now)
	if err != nil {
		return 0, "", fmt.Errorf("platformstore: consume link ticket: %w", err)
	}
	if n, err := r.RowsAffected(); err != nil {
		return 0, "", fmt.Errorf("platformstore: consume link ticket: %w", err)
	} else if n == 0 {
		return 0, "", ErrTokenInvalid
	}
	var row struct {
		UserID     int64          `db:"user_id"`
		ReturnSlug sql.NullString `db:"return_slug"`
	}
	if err := s.db.GetContext(ctx, &row, `SELECT user_id, return_slug FROM oauth_link_ticket WHERE ticket_hash = ?`, ticketHash); err != nil {
		if isNoRows(err) {
			return 0, "", ErrTokenInvalid
		}
		return 0, "", fmt.Errorf("platformstore: read link ticket: %w", err)
	}
	return row.UserID, row.ReturnSlug.String, nil
}

// CompleteGoogleLink connects a verified Google identity to the account a link flow named, in one
// transaction that holds the account FOR UPDATE (P0 §8; a password change, a reset, a sign-out of
// the other devices and a disconnect all take it too):
//
//   - the account is missing, not active, an operator's, or its password changed at or after
//     startedAt (the flow's start; the change ended it) → ErrTokenInvalid;
//   - the subject is this account's already → nil (nothing written); another account's → ErrLinkTaken;
//   - the account has another Google identity → ErrAlreadyLinked;
//   - an empty email, or one Google does not verify and vouch for (Gmail / Workspace) →
//     ErrProviderUnverified;
//   - else the identity is inserted. A concurrent insert of the same subject is retried once and
//     then answered by the rules above.
func (s *Store) CompleteGoogleLink(ctx context.Context, userID int64, in ProviderSignIn, startedAt, now time.Time) error {
	if in.Provider != ProviderGoogle || in.Subject == "" || len(in.Subject) > 255 || userID <= 0 {
		return errors.New("platformstore: a google link needs an account and a subject")
	}
	err := s.completeGoogleLink(ctx, userID, in, startedAt.UTC(), now.UTC())
	if errors.Is(err, errIdentityRace) {
		err = s.completeGoogleLink(ctx, userID, in, startedAt.UTC(), now.UTC())
	}
	if errors.Is(err, errIdentityRace) {
		return fmt.Errorf("platformstore: link race did not settle: %w", err)
	}
	return err
}

func (s *Store) completeGoogleLink(ctx context.Context, userID int64, in ProviderSignIn, startedAt, now time.Time) error {
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockUserByID(ctx, tx, userID)
		if errors.Is(err, ErrUserNotFound) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if u.Status != UserActive || u.IsOperator {
			return ErrTokenInvalid
		}
		var changed sql.NullTime
		if err := tx.GetContext(ctx, &changed, `SELECT password_changed_at FROM platform_user WHERE id = ?`, userID); err != nil {
			return fmt.Errorf("platformstore: link: password changed: %w", err)
		}
		if changed.Valid && !changed.Time.Before(startedAt) {
			return ErrTokenInvalid
		}
		var known []struct {
			UserID  int64  `db:"user_id"`
			Subject string `db:"subject"`
		}
		if err := tx.SelectContext(ctx, &known, `SELECT user_id, subject FROM platform_identity WHERE provider = 'google' AND subject = ?`,
			in.Subject); err != nil {
			return fmt.Errorf("platformstore: link: find identity: %w", err)
		}
		for _, k := range known {
			if k.Subject != in.Subject {
				continue
			}
			if k.UserID == userID {
				return nil
			}
			return ErrLinkTaken
		}
		var google int
		if err := tx.GetContext(ctx, &google, `SELECT COUNT(*) FROM platform_identity WHERE user_id = ? AND provider = 'google'`,
			userID); err != nil {
			return fmt.Errorf("platformstore: link: identities: %w", err)
		}
		if google > 0 {
			return ErrAlreadyLinked
		}
		if strings.TrimSpace(in.Email) == "" || !in.EmailVerified || !in.EmailAuthoritative {
			return ErrProviderUnverified
		}
		return insertIdentityTx(ctx, tx, userID, in, now)
	})
}

// UnlinkGoogle removes the account's Google identities, sessions untouched, in one transaction
// holding the account FOR UPDATE: refused with ErrOnlySignIn when the account has no password its
// owner can type and no other identity. removed is how many went (0: none was connected).
// ErrUserNotFound when the account is missing or not active.
func (s *Store) UnlinkGoogle(ctx context.Context, userID int64) (removed int64, err error) {
	if userID <= 0 {
		return 0, ErrUserNotFound
	}
	err = s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockUserByID(ctx, tx, userID)
		if err != nil {
			return err
		}
		if u.Status != UserActive {
			return ErrUserNotFound
		}
		var counts struct {
			Google int `db:"google"`
			Other  int `db:"other"`
		}
		if err := tx.GetContext(ctx, &counts, `SELECT COALESCE(SUM(provider = 'google'), 0) AS google,
COALESCE(SUM(provider <> 'google'), 0) AS other FROM platform_identity WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("platformstore: unlink: identities: %w", err)
		}
		if counts.Google == 0 {
			removed = 0
			return nil
		}
		set, err := passwordSet(ctx, tx, u.ID, u.PasswordHash)
		if err != nil {
			return err
		}
		if !set && counts.Other == 0 {
			return ErrOnlySignIn
		}
		r, err := tx.ExecContext(ctx, `DELETE FROM platform_identity WHERE user_id = ? AND provider = 'google'`, userID)
		if err != nil {
			return fmt.Errorf("platformstore: unlink: %w", err)
		}
		removed, err = r.RowsAffected()
		if err != nil {
			return fmt.Errorf("platformstore: unlink: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// ExpireLinkFlows ends every Google connect of the account in flight (expireLinkFlowsTx) under the
// account's row lock: a device signed out alone leaves no half-started connect behind.
func (s *Store) ExpireLinkFlows(ctx context.Context, userID int64, now time.Time) error {
	if userID <= 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := lockUserByID(ctx, tx, userID); err != nil {
			if errors.Is(err, ErrUserNotFound) {
				return nil
			}
			return err
		}
		return expireLinkFlowsTx(ctx, tx, userID, now)
	})
}

// expireLinkFlowsTx ends every Google connect of the account in flight — its unused tickets and its
// unused link states — inside the caller's transaction, which holds the account FOR UPDATE: a
// password change, a reset, a claim or a sign-out of the other devices leaves no half-started
// connect a stolen session could finish.
func expireLinkFlowsTx(ctx context.Context, tx *sqlx.Tx, userID int64, now time.Time) error {
	now = now.UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_link_ticket SET used_at = ? WHERE user_id = ? AND used_at IS NULL`,
		now, userID); err != nil {
		return fmt.Errorf("platformstore: expire link tickets: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_state SET used_at = ? WHERE link_user_id = ? AND used_at IS NULL`,
		now, userID); err != nil {
		return fmt.Errorf("platformstore: expire link flows: %w", err)
	}
	return nil
}
