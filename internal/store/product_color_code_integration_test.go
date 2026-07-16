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

// TestColorCodeSurvivesRoundTrip is the acceptance test for problem 008: a product's dictionary
// color_code must be returned on GET and survive a create -> GET -> unchanged-save round trip, so the
// FK is not silently cleared and the SKU does not drift to the free-text fallback.
func TestColorCodeSurvivesRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	var colorCode, colorName string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT code, name FROM color ORDER BY code LIMIT 1`).Scan(&colorCode, &colorName))
	var sizeA int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 1`).Scan(&sizeA))
	var langID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
		BlurHash: sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
	})
	require.NoError(t, err)
	prices := make([]entity.ColorwayPriceInsert, 0)
	for _, c := range currency.RequiredCurrencies() {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(10000)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(10000)})
	}
	mkPayload := func() *entity.ColorwayNew {
		return &entity.ColorwayNew{
			Product: &entity.ColorwayInsert{
				ProductBodyInsert: entity.ColorwayBodyInsert{
					Brand: "ACME", Color: colorName, ColorCode: colorCode,
					ColorHexOverride: sql.NullString{String: "#000000", Valid: true}, CountryOfOrigin: "IT",
					TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
				},
				ThumbnailMediaID: mediaID,
				Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "T08rt", Description: "d"}},
				Prices:           prices,
			},
			SizeMeasurements: []entity.SizeWithMeasurementInsert{
				{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(5)}},
			},
			MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
		}
	}

	prodID, err := s.Products().AddProduct(ctx, mkPayload())
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID) }()

	// GET must now return the dictionary color_code (was empty before the fix)
	got, err := s.Products().GetProductByIdShowHidden(ctx, prodID)
	require.NoError(t, err)
	require.Equal(t, colorCode, got.Product.ProductDisplay.ProductBody.ProductBodyInsert.ColorCode,
		"GET must return the dictionary color_code")

	var skuBefore, codeBefore string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku, COALESCE(color_code,'') FROM product WHERE id = ?`, prodID).Scan(&skuBefore, &codeBefore))
	require.Equal(t, colorCode, codeBefore)

	// Round trip: feed the GET result back unchanged (same payload) -> code and SKU must be stable.
	require.NoError(t, s.Products().UpdateProduct(ctx, mkPayload(), prodID))

	var skuAfter, codeAfter string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku, COALESCE(color_code,'') FROM product WHERE id = ?`, prodID).Scan(&skuAfter, &codeAfter))
	require.Equal(t, colorCode, codeAfter, "unchanged save must not clear the color_code FK")
	require.Equal(t, skuBefore, skuAfter, "unchanged save must not drift the SKU")
}
