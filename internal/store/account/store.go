// Package account implements dependency.StorefrontAccount.
package account

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/jekabolt/grbpwr-manager/internal/storefront"
	"github.com/jekabolt/grbpwr-manager/internal/storefront/tokenhash"
)

// TxFunc matches the store transaction callback used by MYSQLStore.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.StorefrontAccount.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a storefront account store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// InsertLoginChallenge stores hashed OTP and magic link credentials.
// Any still-pending challenges for the same email are marked consumed first so only this
// OTP and magic link remain valid (resend supersedes older emails).
func (s *Store) InsertLoginChallenge(ctx context.Context, email, otpHash, magicHash string, expiresAt time.Time) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		// Lock existing rows for this email so concurrent login requests serialize.
		type idRow struct {
			ID int64 `db:"id"`
		}
		lockQ := `
			SELECT id
			FROM email_login_challenge
			WHERE email = :email
			FOR UPDATE`
		if _, err := storeutil.QueryListNamed[idRow](ctx, db, lockQ, map[string]any{"email": email}); err != nil {
			return fmt.Errorf("lock login challenges: %w", err)
		}
		now := time.Now().UTC()
		inv := `
			UPDATE email_login_challenge
			SET consumed_at = :now
			WHERE email = :email AND consumed_at IS NULL`
		if err := storeutil.ExecNamed(ctx, db, inv, map[string]any{"now": now, "email": email}); err != nil {
			return fmt.Errorf("invalidate prior login challenges: %w", err)
		}
		ins := `
			INSERT INTO email_login_challenge (email, otp_code_hash, magic_token_hash, expires_at)
			VALUES (:email, :otpHash, :magicHash, :expiresAt)`
		if err := storeutil.ExecNamed(ctx, db, ins, map[string]any{
			"email":     email,
			"otpHash":   otpHash,
			"magicHash": magicHash,
			"expiresAt": expiresAt,
		}); err != nil {
			return fmt.Errorf("insert login challenge: %w", err)
		}
		return nil
	})
}

// ConsumeLoginChallengeOTP validates the code and marks the challenge consumed.
func (s *Store) ConsumeLoginChallengeOTP(ctx context.Context, email, otpPlain, otpPepper string) (string, error) {
	want := tokenhash.Hash(otpPepper, "otp:"+email+":"+otpPlain)
	var emailOut string
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		now := time.Now().UTC()
		q := `
			SELECT id, otp_code_hash, consumed_at, expires_at
			FROM email_login_challenge
			WHERE email = :email AND consumed_at IS NULL AND expires_at > :now
			ORDER BY id DESC
			LIMIT 1
			FOR UPDATE`
		type otpChallengeRow struct {
			ID          int64        `db:"id"`
			OTPCodeHash string       `db:"otp_code_hash"`
			ConsumedAt  sql.NullTime `db:"consumed_at"`
			ExpiresAt   time.Time    `db:"expires_at"`
		}
		r, err := storeutil.QueryNamedOne[otpChallengeRow](ctx, db, q, map[string]any{"email": email, "now": now})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return fmt.Errorf("login challenge lookup: %w", err)
		}
		if !tokenhash.Equal(want, r.OTPCodeHash) {
			return sql.ErrNoRows
		}
		upd := `UPDATE email_login_challenge SET consumed_at = :now WHERE id = :id AND consumed_at IS NULL`
		if err := execNamedExpectOneRow(ctx, db, upd, map[string]any{"now": now, "id": r.ID}); err != nil {
			return fmt.Errorf("consume challenge: %w", err)
		}
		emailOut = email
		return nil
	})
	if err != nil {
		return "", err
	}
	return emailOut, nil
}

// ConsumeLoginChallengeMagic validates the magic token and marks the challenge consumed.
func (s *Store) ConsumeLoginChallengeMagic(ctx context.Context, magicPlain, magicPepper string) (string, error) {
	want := tokenhash.Hash(magicPepper, "magic:"+magicPlain)
	var emailOut string
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		now := time.Now().UTC()
		q := `
			SELECT id, email, consumed_at, expires_at
			FROM email_login_challenge
			WHERE magic_token_hash = :h AND consumed_at IS NULL AND expires_at > :now
			FOR UPDATE`
		type magicChallengeRow struct {
			ID    int64  `db:"id"`
			Email string `db:"email"`
		}
		r, err := storeutil.QueryNamedOne[magicChallengeRow](ctx, db, q, map[string]any{"h": want, "now": now})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return fmt.Errorf("magic challenge lookup: %w", err)
		}
		upd := `UPDATE email_login_challenge SET consumed_at = :now WHERE id = :id AND consumed_at IS NULL`
		if err := execNamedExpectOneRow(ctx, db, upd, map[string]any{"now": now, "id": r.ID}); err != nil {
			return fmt.Errorf("consume magic challenge: %w", err)
		}
		emailOut = r.Email
		return nil
	})
	if err != nil {
		return "", err
	}
	return emailOut, nil
}

func execNamedExpectOneRow(ctx context.Context, db dependency.DB, query string, params map[string]any) error {
	q, args, err := storeutil.MakeQuery(query, params)
	if err != nil {
		return fmt.Errorf("make query: %w", err)
	}
	res, err := db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetAccountByEmail loads a storefront account by email.
func (s *Store) GetAccountByEmail(ctx context.Context, email string) (*entity.StorefrontAccount, error) {
	q := `SELECT id, email, first_name, last_name, birth_date, shopping_preference, created_at, updated_at FROM storefront_account WHERE email = :email`
	a, err := storeutil.QueryNamedOne[entity.StorefrontAccount](ctx, s.DB, q, map[string]any{"email": email})
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// GetOrCreateAccountByEmail returns an account row, creating a shell row if needed.
func (s *Store) GetOrCreateAccountByEmail(ctx context.Context, email string) (*entity.StorefrontAccount, error) {
	a, err := s.GetAccountByEmail(ctx, email)
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get account: %w", err)
	}
	ins := `INSERT INTO storefront_account (email, first_name, last_name) VALUES (:email, '', '')`
	if err := storeutil.ExecNamed(ctx, s.DB, ins, map[string]any{"email": email}); err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 { // ER_DUP_ENTRY: concurrent first login
			a, err := s.GetAccountByEmail(ctx, email)
			if err != nil {
				return nil, fmt.Errorf("get account after duplicate insert: %w", err)
			}
			return a, nil
		}
		return nil, fmt.Errorf("insert account: %w", err)
	}
	return s.GetAccountByEmail(ctx, email)
}

// UpdateAccountProfile updates profile fields for the given email.
func (s *Store) UpdateAccountProfile(ctx context.Context, email string, firstName, lastName string, birthDate sql.NullTime, shoppingPreference sql.NullString) error {
	if shoppingPreference.Valid {
		if !entity.IsValidStorefrontShoppingPreference(shoppingPreference.String) {
			return fmt.Errorf("invalid storefront shopping preference: %q", shoppingPreference.String)
		}
	}
	q := `
		UPDATE storefront_account
		SET first_name = :fn, last_name = :ln, birth_date = :bd, shopping_preference = :shoppingPreference, updated_at = CURRENT_TIMESTAMP
		WHERE email = :email`
	return storeutil.ExecNamed(ctx, s.DB, q, map[string]any{
		"fn":                 firstName,
		"ln":                 lastName,
		"bd":                 birthDate,
		"shoppingPreference": shoppingPreference,
		"email":              email,
	})
}

// InsertRefreshToken stores a hashed refresh token.
func (s *Store) InsertRefreshToken(ctx context.Context, accountID int, tokenHash, familyID string, expiresAt time.Time) (int64, error) {
	q := `
		INSERT INTO storefront_refresh_token (account_id, token_hash, family_id, expires_at)
		VALUES (:accountId, :tokenHash, :familyId, :expiresAt)`
	id, err := storeutil.ExecNamedLastId(ctx, s.DB, q, map[string]any{
		"accountId": accountID,
		"tokenHash": tokenHash,
		"familyId":  familyID,
		"expiresAt": expiresAt,
	})
	if err != nil {
		return 0, err
	}
	return int64(id), nil
}

type refreshTokenRow struct {
	ID        int64          `db:"id"`
	AccountID int            `db:"account_id"`
	FamilyID  string         `db:"family_id"`
	RevokedAt sql.NullTime   `db:"revoked_at"`
	ExpiresAt time.Time      `db:"expires_at"`
	Email     string         `db:"email"`
}

// RotateRefreshToken validates the refresh token, revokes it, issues a new one (same family).
// All logic runs inside a transaction to avoid TOCTOU races.
func (s *Store) RotateRefreshToken(ctx context.Context, rawRefresh, refreshPepper string, refreshTTL time.Duration, now time.Time) (newRaw string, accountEmail string, err error) {
	h := tokenhash.Hash(refreshPepper, rawRefresh)
	var outRaw, outEmail string
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		lockQ := `
			SELECT rt.id, rt.account_id, rt.family_id, rt.revoked_at, rt.expires_at, a.email AS email
			FROM storefront_refresh_token rt
			JOIN storefront_account a ON a.id = rt.account_id
			WHERE rt.token_hash = :h
			FOR UPDATE`
		locked, err := storeutil.QueryNamedOne[refreshTokenRow](ctx, db, lockQ, map[string]any{"h": h})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return fmt.Errorf("lock refresh row: %w", err)
		}
		if locked.RevokedAt.Valid {
			if err := s.revokeFamily(ctx, db, locked.FamilyID, now); err != nil {
				slog.Default().ErrorContext(ctx, "failed to revoke token family",
					slog.String("err", err.Error()),
					slog.String("family_id", locked.FamilyID),
				)
			}
			return storefront.ErrRefreshTokenRevoked
		}
		if !locked.ExpiresAt.After(now) {
			return storefront.ErrRefreshTokenExpired
		}
		genRaw, err := randomOpaqueToken()
		if err != nil {
			return err
		}
		newHash := tokenhash.Hash(refreshPepper, genRaw)
		newExp := now.Add(refreshTTL)
		upd := `UPDATE storefront_refresh_token SET revoked_at = :now, replaced_by_id = :newId WHERE id = :id AND revoked_at IS NULL`
		ins := `
			INSERT INTO storefront_refresh_token (account_id, token_hash, family_id, expires_at)
			VALUES (:accountId, :tokenHash, :familyId, :expiresAt)`
		newID, err := storeutil.ExecNamedLastId(ctx, db, ins, map[string]any{
			"accountId": locked.AccountID,
			"tokenHash": newHash,
			"familyId":  locked.FamilyID,
			"expiresAt": newExp,
		})
		if err != nil {
			return fmt.Errorf("insert rotated refresh: %w", err)
		}
		query, args, err := storeutil.MakeQuery(upd, map[string]any{
			"now":   now,
			"newId": newID,
			"id":    locked.ID,
		})
		if err != nil {
			return fmt.Errorf("revoke old refresh: %w", err)
		}
		res, err := db.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("revoke old refresh: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("revoke old refresh rows: %w", err)
		}
		if n != 1 {
			return storefront.ErrRefreshTokenRevoked
		}
		outRaw = genRaw
		outEmail = locked.Email
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return outRaw, outEmail, nil
}

func (s *Store) revokeFamily(ctx context.Context, db dependency.DB, familyID string, t time.Time) error {
	q := `UPDATE storefront_refresh_token SET revoked_at = :t WHERE family_id = :fid AND revoked_at IS NULL`
	return storeutil.ExecNamed(ctx, db, q, map[string]any{"t": t, "fid": familyID})
}

type refreshFamilyLookupRow struct {
	FamilyID string `db:"family_id"`
}

// RevokeAllRefreshTokensForAccount revokes all refresh tokens for an account (logout all devices).
func (s *Store) RevokeAllRefreshTokensForAccount(ctx context.Context, accountID int) error {
	now := time.Now().UTC()
	q := `UPDATE storefront_refresh_token SET revoked_at = :t WHERE account_id = :aid AND revoked_at IS NULL`
	return storeutil.ExecNamed(ctx, s.DB, q, map[string]any{"t": now, "aid": accountID})
}

// RevokeRefreshTokenFamilyByRawTokenForAccount revokes every refresh token in the family for a row matching rawRefresh and accountID.
func (s *Store) RevokeRefreshTokenFamilyByRawTokenForAccount(ctx context.Context, rawRefresh, refreshPepper string, accountID int) error {
	h := tokenhash.Hash(refreshPepper, rawRefresh)
	q := `
		SELECT rt.family_id AS family_id
		FROM storefront_refresh_token rt
		WHERE rt.token_hash = :h AND rt.account_id = :aid
		LIMIT 1`
	row, err := storeutil.QueryNamedOne[refreshFamilyLookupRow](ctx, s.DB, q, map[string]any{"h": h, "aid": accountID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return fmt.Errorf("refresh family lookup: %w", err)
	}
	now := time.Now().UTC()
	if err := s.revokeFamily(ctx, s.DB, row.FamilyID, now); err != nil {
		return fmt.Errorf("revoke refresh family: %w", err)
	}
	return nil
}

// InsertJtiDenylist adds a revoked access token jti to the denylist.
func (s *Store) InsertJtiDenylist(ctx context.Context, jti string, accountID int, expiresAt time.Time) error {
	q := `
		INSERT INTO storefront_access_jti_denylist (jti, account_id, expires_at)
		VALUES (:jti, :accountId, :expiresAt)`
	return storeutil.ExecNamed(ctx, s.DB, q, map[string]any{
		"jti":       jti,
		"accountId": accountID,
		"expiresAt": expiresAt,
	})
}

// IsJtiDenylisted returns true if the jti is in the denylist and not yet expired.
func (s *Store) IsJtiDenylisted(ctx context.Context, jti string) (bool, error) {
	now := time.Now().UTC()
	q := `SELECT 1 FROM storefront_access_jti_denylist WHERE jti = :jti AND expires_at > :now LIMIT 1`
	n, err := storeutil.QueryCountNamed(ctx, s.DB, q, map[string]any{"jti": jti, "now": now})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// CleanupExpiredJtiDenylist deletes expired rows from storefront_access_jti_denylist.
// Returns the number of rows deleted.
func (s *Store) CleanupExpiredJtiDenylist(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	q := `DELETE FROM storefront_access_jti_denylist WHERE expires_at < :now`
	query, args, err := storeutil.MakeQuery(q, map[string]any{"now": now})
	if err != nil {
		return 0, fmt.Errorf("make query: %w", err)
	}
	res, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("exec: %w", err)
	}
	return res.RowsAffected()
}

// CleanupExpiredLoginChallenges deletes expired rows from email_login_challenge.
// Returns the number of rows deleted.
func (s *Store) CleanupExpiredLoginChallenges(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	q := `DELETE FROM email_login_challenge WHERE expires_at < :now`
	query, args, err := storeutil.MakeQuery(q, map[string]any{"now": now})
	if err != nil {
		return 0, fmt.Errorf("make query: %w", err)
	}
	res, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("exec: %w", err)
	}
	return res.RowsAffected()
}

// CleanupExpiredRefreshTokens deletes expired rows from storefront_refresh_token.
// Returns the number of rows deleted.
func (s *Store) CleanupExpiredRefreshTokens(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	q := `DELETE FROM storefront_refresh_token WHERE expires_at < :now`
	query, args, err := storeutil.MakeQuery(q, map[string]any{"now": now})
	if err != nil {
		return 0, fmt.Errorf("make query: %w", err)
	}
	res, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("exec: %w", err)
	}
	return res.RowsAffected()
}

// ListSavedAddresses returns saved addresses for an account.
func (s *Store) ListSavedAddresses(ctx context.Context, accountID int) ([]entity.StorefrontSavedAddress, error) {
	q := `
		SELECT id, account_id, label, country, state, city, address_line_one, address_line_two, company, postal_code, is_default, created_at, updated_at
		FROM storefront_saved_address
		WHERE account_id = :aid
		ORDER BY is_default DESC, id ASC`
	return storeutil.QueryListNamed[entity.StorefrontSavedAddress](ctx, s.DB, q, map[string]any{"aid": accountID})
}

// AddSavedAddress inserts a saved address.
func (s *Store) AddSavedAddress(ctx context.Context, accountID int, ins *entity.StorefrontSavedAddressInsert) (int, error) {
	var id int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if ins.IsDefault {
			if err := s.clearDefaultFlagDB(ctx, db, accountID); err != nil {
				return err
			}
		}
		q := `
			INSERT INTO storefront_saved_address
				(account_id, label, country, state, city, address_line_one, address_line_two, company, postal_code, is_default)
			VALUES
				(:accountId, :label, :country, :state, :city, :a1, :a2, :company, :postal, :isDefault)`
		idVal, err := storeutil.ExecNamedLastId(ctx, db, q, map[string]any{
			"accountId": accountID,
			"label":     ins.Label,
			"country":   ins.Country,
			"state":     ins.State,
			"city":      ins.City,
			"a1":        ins.AddressLineOne,
			"a2":        ins.AddressLineTwo,
			"company":   ins.Company,
			"postal":    ins.PostalCode,
			"isDefault": ins.IsDefault,
		})
		if err != nil {
			return err
		}
		id = idVal
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) clearDefaultFlag(ctx context.Context, accountID int) error {
	return s.clearDefaultFlagDB(ctx, s.DB, accountID)
}

func (s *Store) clearDefaultFlagDB(ctx context.Context, db dependency.DB, accountID int) error {
	q := `UPDATE storefront_saved_address SET is_default = FALSE WHERE account_id = :aid`
	return storeutil.ExecNamed(ctx, db, q, map[string]any{"aid": accountID})
}

// UpdateSavedAddress updates a saved address owned by the account.
func (s *Store) UpdateSavedAddress(ctx context.Context, accountID int, id int, ins *entity.StorefrontSavedAddressInsert) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if ins.IsDefault {
			if err := s.clearDefaultFlagDB(ctx, db, accountID); err != nil {
				return err
			}
		}
		q := `
			UPDATE storefront_saved_address
			SET label = :label, country = :country, state = :state, city = :city,
			    address_line_one = :a1, address_line_two = :a2, company = :company, postal_code = :postal, is_default = :isDefault,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = :id AND account_id = :accountId`
		query, args, err := storeutil.MakeQuery(q, map[string]any{
			"id":        id,
			"accountId": accountID,
			"label":     ins.Label,
			"country":   ins.Country,
			"state":     ins.State,
			"city":      ins.City,
			"a1":        ins.AddressLineOne,
			"a2":        ins.AddressLineTwo,
			"company":   ins.Company,
			"postal":    ins.PostalCode,
			"isDefault": ins.IsDefault,
		})
		if err != nil {
			return fmt.Errorf("update saved address: %w", err)
		}
		res, err := db.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("update saved address: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// DeleteSavedAddress removes a saved address.
// Returns sql.ErrNoRows if the address was not found or does not belong to the account.
func (s *Store) DeleteSavedAddress(ctx context.Context, accountID int, id int) error {
	q := `DELETE FROM storefront_saved_address WHERE id = :id AND account_id = :accountId`
	query, args, err := storeutil.MakeQuery(q, map[string]any{"id": id, "accountId": accountID})
	if err != nil {
		return fmt.Errorf("make query: %w", err)
	}
	res, err := s.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetDefaultSavedAddress marks one address as default.
func (s *Store) SetDefaultSavedAddress(ctx context.Context, accountID int, id int) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()
		if err := s.clearDefaultFlagDB(ctx, db, accountID); err != nil {
			return err
		}
		q := `UPDATE storefront_saved_address SET is_default = TRUE, updated_at = CURRENT_TIMESTAMP WHERE id = :id AND account_id = :accountId`
		return storeutil.ExecNamed(ctx, db, q, map[string]any{"id": id, "accountId": accountID})
	})
}

func randomOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// NewRefreshFamilyID returns a new UUID string for a refresh token family.
func NewRefreshFamilyID() string {
	return uuid.New().String()
}
