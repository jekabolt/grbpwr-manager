package platformstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// Password change and the «has a password» rule (P0 §3.4).

// unusableSaltMark opens the salt of an unusable password hash: an account whose password nobody
// knows (made or claimed through a provider, a seeded operator). Its hash stays a well-formed
// PBKDF2 string of the same cost, so a sign-in against it takes the usual time and fails.
const unusableSaltMark = "lekalo/unusable/"

// legacyProviderWindow: an identity created this close to its account made the account (a provider
// sign-up from before the unusable mark).
const legacyProviderWindow = "5 SECOND"

// maxPasswordHash is the width of password_hash (platform_user and every team's admins).
const maxPasswordHash = 255

// MarkUnusable turns a fresh PBKDF2 hash of a random secret into the unusable form: the mark goes in
// front of the salt, so the stored key matches no password and IsUnusableHash recognises it. A hash
// the mark would push past the column width comes back as it is (still unmatchable, read by the
// legacy rule of passwordSet).
func MarkUnusable(hash string) (string, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 5 || parts[0] != "PBKDF2" {
		return "", errors.New("platformstore: not a PBKDF2 hash")
	}
	salt, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return "", fmt.Errorf("platformstore: hash salt: %w", err)
	}
	parts[3] = base64.StdEncoding.EncodeToString(append([]byte(unusableSaltMark), salt...))
	if out := strings.Join(parts, "$"); len(out) <= maxPasswordHash {
		return out, nil
	}
	return hash, nil
}

// IsUnusableHash reports whether stored was made by MarkUnusable.
func IsUnusableHash(stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 5 || parts[0] != "PBKDF2" {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[3])
	return err == nil && bytes.HasPrefix(salt, []byte(unusableSaltMark))
}

// wellFormedHash reports whether stored has the shape pwhash writes: PBKDF2$sha512$<n>$<salt>$<key>.
func wellFormedHash(stored string) bool {
	return strings.HasPrefix(stored, "PBKDF2$") && strings.Count(stored, "$") == 4
}

// passwordSet reports whether the account has a password its owner can type: a well-formed hash
// that is not unusable, and not a provider sign-up from before the unusable mark (an identity made
// with the account and no password written since). q is the caller's transaction or the pool.
func passwordSet(ctx context.Context, q sqlx.QueryerContext, userID int64, hash string) (bool, error) {
	if !wellFormedHash(hash) || IsUnusableHash(hash) {
		return false, nil
	}
	var legacy int
	if err := sqlx.GetContext(ctx, q, &legacy, `SELECT COUNT(*) FROM platform_user u
JOIN platform_identity i ON i.user_id = u.id
WHERE u.id = ? AND u.password_changed_at IS NULL AND i.created_at <= u.created_at + INTERVAL `+legacyProviderWindow,
		userID); err != nil {
		return false, fmt.Errorf("platformstore: password set: %w", err)
	}
	return legacy == 0, nil
}

// PasswordChange is ChangeMyPassword's write.
type PasswordChange struct {
	UserID int64
	// Hash is the new password's hash.
	Hash string
	// KeepChain is the caller's device chain: its sessions survive.
	KeepChain string
	// SignOutOthers also ends every session outside KeepChain.
	SignOutOthers bool
	// Live decides which of the ended sessions count as signed-out devices.
	Live SessionLiveness
	// Verify checks the request against the stored hash under the account's row lock; set reports
	// whether the account has a password its owner can type. A refusal is ErrPasswordMismatch
	// wrapping it, and nothing changes.
	Verify func(stored string, set bool) error
	Now    time.Time
}

// PasswordChanged is a committed ChangePassword.
type PasswordChanged struct {
	// Gen is the account's new credential generation; SIDs the sessions ended (for the edge cache)
	// and Devices how many live devices they were.
	Gen     int64
	SIDs    []string
	Devices int
	// Handle and TeamOne: a team #1 member's admins row mirrors the new hash.
	Handle  string
	TeamOne bool
}

// ChangePassword changes or sets an active account's password in one transaction: the account FOR
// UPDATE, Verify against its hash, then changeCredentialsKeepChainTx. ErrUserNotFound when the
// account is missing or not active.
func (s *Store) ChangePassword(ctx context.Context, p PasswordChange) (PasswordChanged, error) {
	switch {
	case p.UserID <= 0 || p.Verify == nil:
		return PasswordChanged{}, errors.New("platformstore: a password change needs an account and a check")
	case strings.TrimSpace(p.Hash) == "" || IsUnusableHash(p.Hash):
		return PasswordChanged{}, errors.New("platformstore: a password change needs a usable hash")
	case strings.TrimSpace(p.KeepChain) == "":
		return PasswordChanged{}, errors.New("platformstore: a password change needs the caller's device")
	}
	var out PasswordChanged
	err := s.inTx(ctx, func(tx *sqlx.Tx) error {
		u, err := lockUserByID(ctx, tx, p.UserID)
		if err != nil {
			return err
		}
		if u.Status != UserActive {
			return ErrUserNotFound
		}
		set, err := passwordSet(ctx, tx, u.ID, u.PasswordHash)
		if err != nil {
			return err
		}
		if err := p.Verify(u.PasswordHash, set); err != nil {
			return fmt.Errorf("%w: %w", ErrPasswordMismatch, err)
		}
		c, err := changeCredentialsKeepChainTx(ctx, tx, u.ID, p.Hash, p.KeepChain, p.SignOutOthers, p.Live, p.Now)
		if err != nil {
			return err
		}
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM team_member WHERE team_id = 1 AND user_id = ? AND status = 'active'`,
			u.ID); err != nil {
			return fmt.Errorf("platformstore: team one membership: %w", err)
		}
		out = PasswordChanged{Gen: c.Gen, SIDs: c.SIDs, Devices: c.Devices, Handle: u.Handle, TeamOne: n > 0}
		return nil
	})
	if err != nil {
		return PasswordChanged{}, err
	}
	return out, nil
}
