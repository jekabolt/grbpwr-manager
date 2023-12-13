package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	gerrors "github.com/jekabolt/grbpwr-manager/internal/errors"
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

func (ms *MYSQLStore) GetActiveSubscribers(ctx context.Context) ([]entity.BuyerInsert, error) {

	query := `SELECT * FROM subscriber WHERE receive_promo_emails = 1`
	subscribers, err := QueryListNamed[entity.Subscriber](ctx, ms.DB(), query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get active subscribers: %v", err)
	}
	subs := map[string]entity.BuyerInsert{}
	for _, s := range subscribers {
		subs[s.Email] = entity.BuyerInsert{
			Email:     s.Email,
			FirstName: s.Name,
		}
	}

	query = `SELECT email FROM buyer WHERE receive_promo_emails = 1`
	buyers, err := QueryListNamed[entity.BuyerInsert](ctx, ms.DB(), query, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("failed to get active byers: %v", err)
	}

	for _, b := range buyers {
		subs[b.Email] = b
	}

	var subsSlice []entity.BuyerInsert
	for _, v := range subs {
		subsSlice = append(subsSlice, v)
	}

	return subsSlice, nil
}

func (ms *MYSQLStore) Subscribe(ctx context.Context, email, name string) error {
	// Check if the email already exists
	var subscriber struct {
		ID                 int  `db:"id"`
		ReceivePromoEmails bool `db:"receive_promo_emails"`
	}
	err := ms.DB().GetContext(ctx, &subscriber, "SELECT id, receive_promo_emails FROM subscriber WHERE email = ?", email)
	if err == nil {
		if subscriber.ReceivePromoEmails {
			return gerrors.ErrAlreadySubscribed
		} else {
			// Update receive_promo_emails to true
			_, err := ms.DB().ExecContext(ctx, "UPDATE subscriber SET receive_promo_emails = ? WHERE id = ?", true, subscriber.ID)
			if err != nil {
				return fmt.Errorf("failed to update subscriber: %w", err)
			}
			return nil
		}
	} else if err != sql.ErrNoRows {
		return fmt.Errorf("failed to check subscriber: %w", err)
	}

	// Email does not exist, add it to the database
	err = ExecNamed(ctx, ms.DB(), `INSERT INTO subscriber (name, email, receive_promo_emails) VALUES (:name, :email, :receivePromoEmails)`, map[string]any{
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
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to unsubscribe buyer: %w", err)
		}
	}

	err = ExecNamed(ctx, ms.DB(), `UPDATE subscriber SET receive_promo_emails = false WHERE email = :email`, map[string]any{
		"email": email,
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("failed to unsubscribe subscriber: %w", err)
		}
	}

	return nil
}
