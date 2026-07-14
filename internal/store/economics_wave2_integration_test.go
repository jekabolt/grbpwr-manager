package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestVatRatesConfig exercises the task-03 VAT rate config store methods: the migration seeds
// EU-27, UpsertVatRates inserts a new country and updates an existing rate in place, and
// ListVatRates reflects both. Restores the mutated seed rows on cleanup.
func TestVatRatesConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	defer func() {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM vat_rate WHERE country_code = 'ZZ'")
		_, _ = testDB.ExecContext(ctx, "UPDATE vat_rate SET rate_pct = 19.00 WHERE country_code = 'DE'")
	}()

	M := s.Metrics()
	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }

	// migration seeded EU-27; DE = 19.00
	rates, err := M.ListVatRates(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rates), 27)
	byCC := map[string]decimal.Decimal{}
	for _, r := range rates {
		byCC[r.CountryCode] = r.RatePct
	}
	require.True(t, byCC["DE"].Equal(d("19")), "seeded DE rate: got %s", byCC["DE"])

	// upsert: new country + update existing in place
	require.NoError(t, M.UpsertVatRates(ctx, []entity.VatRate{
		{CountryCode: "ZZ", RatePct: d("15.00")},
		{CountryCode: "DE", RatePct: d("18.00")},
	}))

	rates, err = M.ListVatRates(ctx)
	require.NoError(t, err)
	byCC = map[string]decimal.Decimal{}
	for _, r := range rates {
		byCC[r.CountryCode] = r.RatePct
	}
	require.True(t, byCC["ZZ"].Equal(d("15")), "inserted ZZ rate: got %s", byCC["ZZ"])
	require.True(t, byCC["DE"].Equal(d("18")), "updated DE rate in place: got %s", byCC["DE"])
}
