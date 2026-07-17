package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestSizeSKUContractIsStrict is the DB/runtime acceptance test for problem 017: individual size
// rows cannot carry a missing/out-of-range/duplicate/free-text contract, and a colourway cannot mix
// otherwise-valid size systems. Every failure must happen before a partial product is committed.
func TestSizeSKUContractIsStrict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var existingSystem string
	var existingOrd int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT sku_system, sku_ord FROM size ORDER BY id LIMIT 1`).Scan(&existingSystem, &existingOrd))

	invalidSizeWrites := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "missing contract", query: `INSERT INTO size (name) VALUES (?)`, args: []any{"T17-MISSING"}},
		{name: "zero ordinal", query: `INSERT INTO size (name, sku_ord, sku_system) VALUES (?, 0, 'apparel')`, args: []any{"T17-ZERO"}},
		{name: "ordinal over width", query: `INSERT INTO size (name, sku_ord, sku_system) VALUES (?, 100, 'apparel')`, args: []any{"T17-100"}},
		{name: "free-text system", query: `INSERT INTO size (name, sku_ord, sku_system) VALUES (?, 1, 'free-text')`, args: []any{"T17-FREE"}},
		{name: "wrong-case system", query: `INSERT INTO size (name, sku_ord, sku_system) VALUES (?, 1, 'APPAREL')`, args: []any{"T17-UPPER"}},
		{name: "duplicate system ordinal", query: `INSERT INTO size (name, sku_ord, sku_system) VALUES (?, ?, ?)`, args: []any{"T17-DUP", existingOrd, existingSystem}},
	}
	for _, tc := range invalidSizeWrites {
		t.Run(tc.name, func(t *testing.T) {
			_, err := testDB.ExecContext(ctx, tc.query, tc.args...)
			require.Error(t, err)
		})
	}

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))
	var apparelSizeID, shoeSizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&apparelSizeID))
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'shoe' ORDER BY sku_ord LIMIT 1`).Scan(&shoeSizeID))

	var langID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)
	prices := make([]entity.ColorwayPriceInsert, 0)
	for _, c := range currency.RequiredCurrencies() {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(10000)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(10000)})
	}

	var before int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM product").Scan(&before))

	_, err = s.Products().AddProduct(ctx, &entity.ColorwayNew{
		Product: &entity.ColorwayInsert{
			ProductBodyInsert: entity.ColorwayBodyInsert{
				Brand: "ACME", Color: "black", ColorCode: "BLK", ColorHexOverride: sql.NullString{String: "#000000", Valid: true}, CountryOfOrigin: "IT",
				TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
			},
			ThumbnailMediaID: mediaID,
			Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "T17", Description: "d"}},
			Prices:           prices,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{
			{ProductSize: entity.VariantInsert{SizeId: apparelSizeID, Quantity: decimal.NewFromInt(5)}},
			{ProductSize: entity.VariantInsert{SizeId: shoeSizeID, Quantity: decimal.NewFromInt(5)}},
		},
		MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
	})
	require.ErrorContains(t, err, "mixes size SKU systems")

	var after int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM product").Scan(&after))
	require.Equal(t, before, after, "the failed mixed-system create must roll back — no partial product persisted")
}
