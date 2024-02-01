package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

type ratesStore struct {
	*MYSQLStore
}

// Rates returns an object implementing Rates interface
func (ms *MYSQLStore) Rates() dependency.Rates {
	return &ratesStore{
		MYSQLStore: ms,
	}
}

// GetLatestRates retrieves the most recent rates for all currencies.
func (ms *MYSQLStore) GetLatestRates(ctx context.Context) ([]entity.CurrencyRate, error) {
	var rates []entity.CurrencyRate
	query := `SELECT id, currency_code, rate, updated_at FROM currency_rate`
	err := ms.DB().SelectContext(ctx, &rates, query)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []entity.CurrencyRate{}, nil
		}
		return nil, fmt.Errorf("failed to get latest currency rates: %w", err)
	}
	return rates, nil
}

func (ms *MYSQLStore) BulkUpdateRates(ctx context.Context, rates []entity.CurrencyRate) error {
	// Use the Tx method to manage the transaction
	err := ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		for _, rate := range rates {
			query := `UPDATE currency_rate SET rate = ?, updated_at = NOW() WHERE currency_code = ?`
			res, err := rep.DB().ExecContext(ctx, query, rate.Rate, rate.CurrencyCode)
			if err != nil {
				return fmt.Errorf("failed to update rate for currency_code %s: %w", rate.CurrencyCode, err)
			}
			ra, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("failed to get rows affected: %w", err)
			}
			if ra == 0 {
				query := `INSERT INTO currency_rate (currency_code, rate) VALUES (?, ?)`
				_, err := rep.DB().ExecContext(ctx, query, rate.CurrencyCode,
					rate.Rate)
				if err != nil {
					return fmt.Errorf("failed to insert rate for currency_code %s: %w", rate.CurrencyCode, err)
				}
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("can't bulk update rates: %w", err)
	}

	return nil
}
