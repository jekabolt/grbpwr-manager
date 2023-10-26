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

func (ms *MYSQLStore) GetActiveSubscribers(ctx context.Context) ([]entity.Buyer, error) {

	query := `SELECT * FROM subscriber WHERE receive_promo_emails = 1`
	subscribers, err := QueryListNamed[entity.Subscriber](ctx, ms.DB(), query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get active subscribers: %v", err)
	}
	subs := map[string]entity.Buyer{}
	for _, s := range subscribers {
		subs[s.Email] = entity.Buyer{
			Email:     s.Email,
			FirstName: s.Name,
		}
	}

	query = `SELECT email FROM buyer WHERE receive_promo_emails = 1`
	buyers, err := QueryListNamed[entity.Buyer](ctx, ms.DB(), query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get active byers: %v", err)
	}

	for _, b := range buyers {
		subs[b.Email] = b
	}

	var subsSlice []entity.Buyer
	for _, v := range subs {
		subsSlice = append(subsSlice, v)
	}

	return subsSlice, nil
}

func (ms *MYSQLStore) Subscribe(ctx context.Context, email, name string) error {
	err := ExecNamed(ctx, ms.DB(), `INSERT INTO buyer (email, name, receive_promo_emails, country) VALUES
		(:email, :name, :receivePromoEmails, :country)`, map[string]any{
		"email":              email,
		"name":               name,
		"receivePromoEmails": true,
	})

	if err != nil {
		return fmt.Errorf("failed to add subscriber: %w", err)
	}
	return nil
}

func (ms *MYSQLStore) Unsubscribe(ctx context.Context, email string) error {
	err := ExecNamed(ctx, ms.DB(), `UPDATE buyer SET receive_promo_emails = false WHERE email = :email`, map[string]any{
		"email": email,
	})
	if err != nil {
		return fmt.Errorf("failed to unsubscribe: %w", err)
	}
	return nil
}
