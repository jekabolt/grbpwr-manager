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

// TestMintRejectsInvalidOrdinal is the acceptance test for problem 017: a mint over a size with no
// SKU ordinal (sku_ord = 0) must FAIL and roll the whole create back, never commit a product with a
// NULL variant SKU.
func TestMintRejectsInvalidOrdinal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// zero-ordinal size, seeded before cache init so GetSizeById returns it with SkuOrd == 0
	zeroRes, err := testDB.ExecContext(ctx, `INSERT INTO size (name, sku_ord) VALUES ('T17-ZERO', 0)`)
	require.NoError(t, err)
	zid64, _ := zeroRes.LastInsertId()
	zeroSizeID := int(zid64)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM size WHERE id = ?", zeroSizeID) }()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

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
			{ProductSize: entity.VariantInsert{SizeId: zeroSizeID, Quantity: decimal.NewFromInt(5)}},
		},
		MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
	})
	require.Error(t, err, "a size with no SKU ordinal must fail the mint, not commit a NULL variant SKU")

	var after int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM product").Scan(&after))
	require.Equal(t, before, after, "the failed create must roll back — no partial product persisted")
}
