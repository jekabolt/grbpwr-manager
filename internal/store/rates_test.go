package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func TestRatesUpdateAndGetLatest(t *testing.T) {
	// Setup: Assuming you have a function to initialize your test database connection
	db := newTestDB(t)
	rs := db.Rates()
	ctx := context.Background()

	// Define the rates to be updated
	updateRates := []entity.CurrencyRate{
		{
			CurrencyCode: "USD",
			Rate:         decimal.NewFromFloat(1.1),
		},
		{
			CurrencyCode: "EUR",
			Rate:         decimal.NewFromFloat(0.9),
		},
	}

	// Step 1: Bulk update rates
	err := rs.BulkUpdateRates(ctx, updateRates)
	assert.NoError(t, err, "BulkUpdateRates should not return an error")

	// Step 2: Retrieve the latest rates and validate
	latestRates, err := rs.GetLatestRates(ctx)
	assert.NoError(t, err, "GetLatestRates should not return an error")

	// Build a map for easier lookup
	latestRatesMap := make(map[string]entity.CurrencyRate)
	for _, rate := range latestRates {
		latestRatesMap[rate.CurrencyCode] = rate
	}

	// Assert the updated rates
	for _, rate := range updateRates {
		latestRate, exists := latestRatesMap[rate.CurrencyCode]
		assert.True(t, exists, "Updated currency should exist in latest rates")
		assert.True(t, latestRate.Rate.Equal(rate.Rate), "Rate should match the updated value")
		assert.WithinDuration(t, time.Now(), latestRate.UpdatedAt, time.Minute, "UpdatedAt should be recent")
	}
}
