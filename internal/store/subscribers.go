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

func (ms *MYSQLStore) UpsertSubscription(ctx context.Context, email string, receivePromo bool) (bool, error) {
	subscriber, err := ms.getSubscriberByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			query := `INSERT INTO subscriber (email, receive_promo_emails) VALUES (:email, :receivePromoEmails)`
			params := map[string]interface{}{
				"email":              email,
				"receivePromoEmails": receivePromo,
			}

			if err := ExecNamed(ctx, ms.DB(), query, params); err != nil {
				return false, fmt.Errorf("failed to insert subscriber: %w", err)
			}
			return false, nil
		}
		return false, fmt.Errorf("failed to get subscriber: %w", err)
	}

	wasSubscribed := subscriber.ReceivePromoEmails
	if wasSubscribed == receivePromo {
		return wasSubscribed, nil
	}

	query := `UPDATE subscriber SET receive_promo_emails = :receivePromoEmails WHERE email = :email`
	params := map[string]interface{}{
		"email":              email,
		"receivePromoEmails": receivePromo,
	}

	if err := ExecNamed(ctx, ms.DB(), query, params); err != nil {
		return wasSubscribed, fmt.Errorf("failed to update subscriber: %w", err)
	}

	return wasSubscribed, nil
}

func (ms *MYSQLStore) IsSubscribed(ctx context.Context, email string) (bool, error) {
	subscriber, err := ms.getSubscriberByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check subscription: %w", err)
	}
	return subscriber.ReceivePromoEmails, nil
}

func (ms *MYSQLStore) getSubscriberByEmail(ctx context.Context, email string) (*entity.Subscriber, error) {
	query := `SELECT * FROM subscriber WHERE email = :email`
	params := map[string]interface{}{
		"email": email,
	}

	subscriber, err := QueryNamedOne[entity.Subscriber](ctx, ms.DB(), query, params)
	if err != nil {
		return nil, err
	}

	return &subscriber, nil
}
