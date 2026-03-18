// Package admin implements admin user management operations.
package admin

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// TxFunc executes f within a serializable transaction with deadlock retry.
type TxFunc func(ctx context.Context, f func(context.Context, dependency.Repository) error) error

// Store implements dependency.Admin.
type Store struct {
	storeutil.Base
	txFunc TxFunc
}

// New creates a new admin store.
func New(base storeutil.Base, txFunc TxFunc) *Store {
	return &Store{Base: base, txFunc: txFunc}
}

// AddAdmin creates a new admin user.
func (s *Store) AddAdmin(ctx context.Context, un, pwHash string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		_, err := rep.DB().ExecContext(ctx, `
		INSERT INTO admins
		(username, password_hash)
		VALUES
		(?, ?)`, un, pwHash)
		if err != nil {
			return fmt.Errorf("can't add admin user %v", err.Error())
		}
		return nil
	})
}

// DeleteAdmin deletes an admin user.
func (s *Store) DeleteAdmin(ctx context.Context, username string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := rep.DB().ExecContext(ctx, `
		DELETE FROM admins WHERE username = ?`, username)
		if err != nil {
			return fmt.Errorf("failed to delete admin user")
		}
		ra, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get affected rows")
		}
		if ra == 0 {
			return fmt.Errorf("admin not found")
		}
		return nil
	})
}

// ChangePassword changes the password of an admin user.
func (s *Store) ChangePassword(ctx context.Context, un, newHash string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := rep.DB().ExecContext(ctx, `
			UPDATE admins
			SET password_hash = ?
			WHERE username = ?`, newHash, un)
		if err != nil {
			return fmt.Errorf("failed change admin user password")
		}
		ra, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get affected rows")
		}
		if ra == 0 {
			return fmt.Errorf("admin not found")
		}
		return nil
	})
}

// PasswordHashByUsername returns password hash of an admin user.
func (s *Store) PasswordHashByUsername(ctx context.Context, un string) (string, error) {
	query := `
	SELECT
		*
	FROM admins WHERE username = :username`
	adm, err := storeutil.QueryNamedOne[entity.Admin](ctx, s.DB, query, map[string]any{"username": un})
	if err != nil {
		return "", fmt.Errorf("failed to get password hash %w", err)
	}
	return adm.PasswordHash, nil
}

// GetAdminByUsername returns an admin user by username.
func (s *Store) GetAdminByUsername(ctx context.Context, un string) (*entity.Admin, error) {
	query := `
	SELECT
		id,
		username,
		password_hash
	FROM admins WHERE username = :username`
	admin, err := storeutil.QueryNamedOne[entity.Admin](ctx, s.DB, query, map[string]any{"username": un})
	if err != nil {
		return nil, fmt.Errorf("failed to get admin")
	}
	return &admin, nil
}
