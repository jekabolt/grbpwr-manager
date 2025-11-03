package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type waitlistStore struct {
	*MYSQLStore
}

func (ms *MYSQLStore) Waitlist() dependency.Waitlist {
	return &waitlistStore{
		MYSQLStore: ms,
	}
}

// AddToWaitlist adds an email to the waitlist for a specific product/size combination.
// It also ensures the email is added to the subscribers list with receive_promo_emails=true.
func (ms *MYSQLStore) AddToWaitlist(ctx context.Context, productId int, sizeId int, email string) error {
	// Ensure subscriber exists with receive_promo_emails=true
	_, err := ms.UpsertSubscription(ctx, email, true)
	if err != nil {
		return fmt.Errorf("failed to upsert subscription: %w", err)
	}

	// Insert waitlist entry (unique constraint will prevent duplicates)
	query := `INSERT INTO product_waitlist (product_id, size_id, email) VALUES (:productId, :sizeId, :email)`
	params := map[string]interface{}{
		"productId": productId,
		"sizeId":    sizeId,
		"email":     email,
	}

	err = ExecNamed(ctx, ms.DB(), query, params)
	if err != nil {
		// Check for duplicate entry error (MySQL error 1062)
		errStr := err.Error()
		if strings.Contains(errStr, "Duplicate entry") || strings.Contains(errStr, "1062") {
			// Already on waitlist, return success
			return nil
		}
		return fmt.Errorf("failed to add to waitlist: %w", err)
	}

	return nil
}

// GetWaitlistEntriesByProductSize retrieves all waitlist entries for a specific product/size combination.
func (ms *MYSQLStore) GetWaitlistEntriesByProductSize(ctx context.Context, productId int, sizeId int) ([]entity.WaitlistEntry, error) {
	query := `SELECT * FROM product_waitlist WHERE product_id = :productId AND size_id = :sizeId`
	params := map[string]interface{}{
		"productId": productId,
		"sizeId":    sizeId,
	}

	entries, err := QueryListNamed[entity.WaitlistEntry](ctx, ms.DB(), query, params)
	if err != nil {
		return nil, fmt.Errorf("failed to get waitlist entries: %w", err)
	}

	return entries, nil
}

// GetWaitlistEntriesWithBuyerNames retrieves all waitlist entries with buyer names for a specific product/size combination.
// Uses a single query with subqueries to get buyer names directly from the buyer table.
func (ms *MYSQLStore) GetWaitlistEntriesWithBuyerNames(ctx context.Context, productId int, sizeId int) ([]entity.WaitlistEntryWithBuyer, error) {
	// Use a subquery to get the most recent buyer name for each email
	query := `
		SELECT 
			pw.*,
			(SELECT first_name FROM buyer WHERE email = pw.email ORDER BY id DESC LIMIT 1) AS first_name,
			(SELECT last_name FROM buyer WHERE email = pw.email ORDER BY id DESC LIMIT 1) AS last_name
		FROM product_waitlist pw
		WHERE pw.product_id = :productId AND pw.size_id = :sizeId
	`
	params := map[string]interface{}{
		"productId": productId,
		"sizeId":    sizeId,
	}

	entries, err := QueryListNamed[entity.WaitlistEntryWithBuyer](ctx, ms.DB(), query, params)
	if err != nil {
		return nil, fmt.Errorf("failed to get waitlist entries with buyer names: %w", err)
	}

	return entries, nil
}

// RemoveFromWaitlist removes a specific waitlist entry.
func (ms *MYSQLStore) RemoveFromWaitlist(ctx context.Context, productId int, sizeId int, email string) error {
	query := `DELETE FROM product_waitlist WHERE product_id = :productId AND size_id = :sizeId AND email = :email`
	params := map[string]interface{}{
		"productId": productId,
		"sizeId":    sizeId,
		"email":     email,
	}

	err := ExecNamed(ctx, ms.DB(), query, params)
	if err != nil {
		return fmt.Errorf("failed to remove from waitlist: %w", err)
	}

	return nil
}

// RemoveFromWaitlistBatch removes all waitlist entries for a specific product/size combination.
func (ms *MYSQLStore) RemoveFromWaitlistBatch(ctx context.Context, productId int, sizeId int) error {
	query := `DELETE FROM product_waitlist WHERE product_id = :productId AND size_id = :sizeId`
	params := map[string]interface{}{
		"productId": productId,
		"sizeId":    sizeId,
	}

	err := ExecNamed(ctx, ms.DB(), query, params)
	if err != nil {
		return fmt.Errorf("failed to remove from waitlist batch: %w", err)
	}

	return nil
}

// IsOnWaitlist checks if an email is already on the waitlist for a product/size.
func (ms *MYSQLStore) IsOnWaitlist(ctx context.Context, productId int, sizeId int, email string) (bool, error) {
	query := `SELECT * FROM product_waitlist WHERE product_id = :productId AND size_id = :sizeId AND email = :email`
	params := map[string]interface{}{
		"productId": productId,
		"sizeId":    sizeId,
		"email":     email,
	}

	_, err := QueryNamedOne[entity.WaitlistEntry](ctx, ms.DB(), query, params)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check waitlist: %w", err)
	}

	return true, nil
}
