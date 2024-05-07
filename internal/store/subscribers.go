package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type subscribersStore struct {
	*MYSQLStore
}

// Subscribers returns an object implementing Subscribers interface
func (ms *MYSQLStore) Subscribers() dependency.Subscribers {
	return &subscribersStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) GetActiveSubscribers(ctx context.Context) ([]entity.Subscriber, error) {

	query := `SELECT * FROM subscriber WHERE receive_promo_emails = 1`
	subscribers, err := QueryListNamed[entity.Subscriber](ctx, ms.DB(), query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get active subscribers: %v", err)
	}

	return subscribers, nil
}

func (ms *MYSQLStore) UpsertSubscription(ctx context.Context, email string, receivePromo bool) error {
	// Corrected SQL query that inserts a new subscriber or updates an existing one if the email already exists.
	query := `
		INSERT INTO subscriber (email, receive_promo_emails)
		VALUES (:email, :receivePromoEmails)
		ON DUPLICATE KEY UPDATE receive_promo_emails = VALUES(receive_promo_emails)
    `

	// Prepare the parameters for the query
	params := map[string]interface{}{
		"email":              email,
		"receivePromoEmails": receivePromo,
	}

	// Execute the query with named parameters
	err := ExecNamed(ctx, ms.DB(), query, params)
	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) IsSubscribed(ctx context.Context, email string) (bool, error) {
	query := `SELECT * FROM subscriber WHERE email = :email`
	params := map[string]interface{}{
		"email": email,
	}

	subscriber, err := QueryNamedOne[entity.Subscriber](ctx, ms.DB(), query, params)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check subscription: %w", err)
	}
	return subscriber.ReceivePromoEmails, nil
}
