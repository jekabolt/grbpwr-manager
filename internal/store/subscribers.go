package store

import (
	"context"
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
	// SQL query that inserts a new subscriber or updates an existing one.
	query := `
        INSERT INTO subscriber (email, receive_promo_emails)
        VALUES (:email, :receivePromoEmails)
        ON DUPLICATE KEY UPDATE receive_promo_emails = VALUES(:receivePromoEmails)
    `
	err := ExecNamed(ctx, ms.DB(), query, map[string]any{
		"email":              email,
		"receivePromoEmails": receivePromo,
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe: %w", err)
	}
	return nil
}
