package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// AddToWaitlist adds an email to the waitlist for a specific product/size combination.
// It also ensures the email is added to the subscribers list.
func (s *Store) AddToWaitlist(ctx context.Context, productId int, sizeId int, email string) error {
	rep := s.repFunc()
	_, err := rep.Subscribers().UpsertSubscription(ctx, email, true)
	if err != nil {
		return fmt.Errorf("failed to upsert subscription: %w", err)
	}

	query := `INSERT INTO product_waitlist (product_id, size_id, email) VALUES (:productId, :sizeId, :email)`
	params := map[string]any{
		"productId": productId,
		"sizeId":    sizeId,
		"email":     email,
	}

	err = storeutil.ExecNamed(ctx, s.DB, query, params)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "Duplicate entry") || strings.Contains(errStr, "1062") {
			return nil
		}
		return fmt.Errorf("failed to add to waitlist: %w", err)
	}

	return nil
}

// GetWaitlistEntriesByProductSize retrieves all waitlist entries for a specific product/size combination.
func (s *Store) GetWaitlistEntriesByProductSize(ctx context.Context, productId int, sizeId int) ([]entity.WaitlistEntry, error) {
	query := `SELECT * FROM product_waitlist WHERE product_id = :productId AND size_id = :sizeId`
	params := map[string]any{
		"productId": productId,
		"sizeId":    sizeId,
	}

	entries, err := storeutil.QueryListNamed[entity.WaitlistEntry](ctx, s.DB, query, params)
	if err != nil {
		return nil, fmt.Errorf("failed to get waitlist entries: %w", err)
	}

	return entries, nil
}

// GetWaitlistEntriesWithBuyerNames retrieves waitlist entries with buyer names.
func (s *Store) GetWaitlistEntriesWithBuyerNames(ctx context.Context, productId int, sizeId int) ([]entity.WaitlistEntryWithBuyer, error) {
	query := `
		SELECT 
			pw.*,
			(SELECT first_name FROM buyer WHERE email = pw.email ORDER BY id DESC LIMIT 1) AS first_name,
			(SELECT last_name FROM buyer WHERE email = pw.email ORDER BY id DESC LIMIT 1) AS last_name
		FROM product_waitlist pw
		WHERE pw.product_id = :productId AND pw.size_id = :sizeId
	`
	params := map[string]any{
		"productId": productId,
		"sizeId":    sizeId,
	}

	entries, err := storeutil.QueryListNamed[entity.WaitlistEntryWithBuyer](ctx, s.DB, query, params)
	if err != nil {
		return nil, fmt.Errorf("failed to get waitlist entries with buyer names: %w", err)
	}

	return entries, nil
}

// RemoveFromWaitlist removes a specific waitlist entry.
func (s *Store) RemoveFromWaitlist(ctx context.Context, productId int, sizeId int, email string) error {
	query := `DELETE FROM product_waitlist WHERE product_id = :productId AND size_id = :sizeId AND email = :email`
	params := map[string]any{
		"productId": productId,
		"sizeId":    sizeId,
		"email":     email,
	}

	err := storeutil.ExecNamed(ctx, s.DB, query, params)
	if err != nil {
		return fmt.Errorf("failed to remove from waitlist: %w", err)
	}

	return nil
}

// RemoveFromWaitlistBatch removes all waitlist entries for a specific product/size combination.
func (s *Store) RemoveFromWaitlistBatch(ctx context.Context, productId int, sizeId int) error {
	query := `DELETE FROM product_waitlist WHERE product_id = :productId AND size_id = :sizeId`
	params := map[string]any{
		"productId": productId,
		"sizeId":    sizeId,
	}

	err := storeutil.ExecNamed(ctx, s.DB, query, params)
	if err != nil {
		return fmt.Errorf("failed to remove from waitlist batch: %w", err)
	}

	return nil
}

// IsOnWaitlist checks if an email is already on the waitlist for a product/size.
func (s *Store) IsOnWaitlist(ctx context.Context, productId int, sizeId int, email string) (bool, error) {
	query := `SELECT * FROM product_waitlist WHERE product_id = :productId AND size_id = :sizeId AND email = :email`
	params := map[string]any{
		"productId": productId,
		"sizeId":    sizeId,
		"email":     email,
	}

	_, err := storeutil.QueryNamedOne[entity.WaitlistEntry](ctx, s.DB, query, params)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check waitlist: %w", err)
	}

	return true, nil
}
