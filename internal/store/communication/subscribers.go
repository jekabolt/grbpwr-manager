package communication

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// GetActiveSubscribers returns all subscribers that opted in for promo emails.
func (s *Store) GetActiveSubscribers(ctx context.Context) ([]entity.Subscriber, error) {
	query := `SELECT * FROM subscriber WHERE receive_promo_emails = 1`
	subscribers, err := storeutil.QueryListNamed[entity.Subscriber](ctx, s.DB, query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get active subscribers: %v", err)
	}

	return subscribers, nil
}

// UpsertSubscription creates or updates a subscriber's promo email preference.
func (s *Store) UpsertSubscription(ctx context.Context, email string, receivePromo bool) (bool, error) {
	subscriber, err := s.getSubscriberByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			query := `INSERT INTO subscriber (email, receive_promo_emails, created_at) VALUES (:email, :receivePromoEmails, CURRENT_TIMESTAMP)`
			params := map[string]any{
				"email":              email,
				"receivePromoEmails": receivePromo,
			}

			if err := storeutil.ExecNamed(ctx, s.DB, query, params); err != nil {
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
	params := map[string]any{
		"email":              email,
		"receivePromoEmails": receivePromo,
	}

	if err := storeutil.ExecNamed(ctx, s.DB, query, params); err != nil {
		return wasSubscribed, fmt.Errorf("failed to update subscriber: %w", err)
	}

	return wasSubscribed, nil
}

// IsSubscribed checks if an email is subscribed to promo emails.
func (s *Store) IsSubscribed(ctx context.Context, email string) (bool, error) {
	subscriber, err := s.getSubscriberByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check subscription: %w", err)
	}
	return subscriber.ReceivePromoEmails, nil
}

func (s *Store) getSubscriberByEmail(ctx context.Context, email string) (*entity.Subscriber, error) {
	query := `SELECT * FROM subscriber WHERE email = :email`
	params := map[string]any{
		"email": email,
	}

	subscriber, err := storeutil.QueryNamedOne[entity.Subscriber](ctx, s.DB, query, params)
	if err != nil {
		return nil, err
	}

	return &subscriber, nil
}

// GetNewSubscribersCount returns the number of subscribers added in the given period.
func (s *Store) GetNewSubscribersCount(ctx context.Context, from, to time.Time) (int, error) {
	query := `SELECT COUNT(*) FROM subscriber WHERE created_at >= :from AND created_at < :to`
	params := map[string]any{
		"from": from,
		"to":   to,
	}

	count, err := storeutil.QueryCountNamed(ctx, s.DB, query, params)
	if err != nil {
		return 0, fmt.Errorf("failed to get new subscribers count: %w", err)
	}
	return count, nil
}
