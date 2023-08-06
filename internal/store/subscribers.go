package store

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
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

func (ms *MYSQLStore) GetActiveSubscribers(ctx context.Context) ([]string, error) {
	query := `SELECT email FROM buyer WHERE receive_promo_emails = TRUE`
	rows, err := ms.DB().QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []string
	for rows.Next() {
		var email string
		err := rows.Scan(&email)
		if err != nil {
			return nil, err
		}
		subs = append(subs, email)
	}
	return subs, nil
}

func (ms *MYSQLStore) getTestAddressId(ctx context.Context) (int64, error) {
	row := ms.DB().QueryRowContext(ctx, `select id from address where street = 'test' and postal_code = 'test'`)
	var id int64
	err := row.Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (ms *MYSQLStore) Subscribe(ctx context.Context, email string) error {

	return ms.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
		aid, err := ms.getTestAddressId(ctx)
		if err != nil {
			return err
		}
		_, err = addBuyer(ctx, store, &dto.Buyer{
			FirstName: "",
			LastName:  "",
			Phone:     "",
			Email:     email,
			BillingAddress: &dto.Address{
				ID: aid,
			},
			ShippingAddress: &dto.Address{
				ID: aid,
			},
			ReceivePromoEmails: true,
		})
		return err
	})

}

func (ms *MYSQLStore) Unsubscribe(ctx context.Context, email string) error {
	_, err := ms.DB().ExecContext(ctx, `
	UPDATE buyer
	SET receive_promo_emails = FALSE
	WHERE email = ?`, email)

	if err != nil {
		return err
	}
	return nil
}
