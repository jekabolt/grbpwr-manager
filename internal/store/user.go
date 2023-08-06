package store

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

type adminStore struct {
	*MYSQLStore
}

// UsersStore returns an object implementing dependency.Admin interface
func (ms *MYSQLStore) Admin() dependency.Admin {
	return &adminStore{
		MYSQLStore: ms,
	}
}

// AddUser creates a new user
func (as *adminStore) AddAdmin(ctx context.Context, un, pwHash string) error {
	return as.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		_, err := as.db.ExecContext(ctx, `
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

// DeleteUser deletes a user
func (as *adminStore) DeleteAdmin(ctx context.Context, username string) error {
	return as.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := as.db.ExecContext(ctx, `
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

// ChangePassword changes the password of a user
func (as *adminStore) ChangePassword(ctx context.Context, un, newHash string) error {
	return as.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := as.db.ExecContext(ctx, `
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

// GetUserPasswordHash returns password hash of a user
func (as *adminStore) PasswordHashByUsername(ctx context.Context, un string) (string, error) {
	row := as.db.QueryRowContext(ctx, `
	 	SELECT
	 	password_hash
	 	FROM admins WHERE username = ?`, un)
	if row.Err() != nil {
		return "", fmt.Errorf("not found %v", row.Err().Error())
	}
	var pw string
	err := row.Scan(&pw)
	if err != nil {
		return "", fmt.Errorf("failed to scan password")
	}
	return pw, nil
}

// GetUserByUsername returns user by username
func (as *adminStore) GetAdminByUsername(ctx context.Context, un string) (*dto.Admin, error) {
	row := as.db.QueryRowContext(ctx, `
		SELECT
		id,
		username,
		password_hash
		FROM admins WHERE username = ?`, un)
	if row.Err() != nil {
		return nil, fmt.Errorf("not found %v", row.Err().Error())
	}

	var admin dto.Admin
	err := row.Scan(&admin.ID, &admin.Username, &admin.PasswordHash)
	if err != nil {
		return nil, fmt.Errorf("failed to scan admin")
	}
	return &admin, nil
}
