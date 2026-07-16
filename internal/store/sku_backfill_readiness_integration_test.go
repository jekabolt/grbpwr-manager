package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestSKUBackfillRepairsAndBlocksReadiness is the acceptance test for problem 022. An unlocked
// malformed variant is repaired, while the same corruption on a frozen identity is reported as a
// catalog-wide postcondition failure and is never silently skipped or reminted.
func TestSKUBackfillRepairsAndBlocksReadiness(t *testing.T) {
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

	var sizeID, sizeOrdinal int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id, sku_ord FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&sizeID, &sizeOrdinal))
	var languageID int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT MIN(id) FROM language`).Scan(&languageID))

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL:   "https://x/backfill-full.jpg",
		FullSizeWidth:      100,
		FullSizeHeight:     100,
		ThumbnailMediaURL:  "https://x/backfill-thumb.jpg",
		ThumbnailWidth:     10,
		ThumbnailHeight:    10,
		CompressedMediaURL: "https://x/backfill-compressed.jpg",
		CompressedWidth:    50,
		CompressedHeight:   50,
	})
	require.NoError(t, err)

	prices := make([]entity.ColorwayPriceInsert, 0)
	for _, code := range currency.RequiredCurrencies() {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: code, Price: decimal.NewFromInt(10000)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(10000)})
	}

	payload := &entity.ColorwayNew{
		Product: &entity.ColorwayInsert{
			ProductBodyInsert: entity.ColorwayBodyInsert{
				Brand:            "ACME",
				Color:            "black",
				ColorCode:        "BLK",
				ColorHexOverride: sql.NullString{String: "#000000", Valid: true},
				CountryOfOrigin:  "IT",
				TopCategoryId:    1,
				TargetGender:     entity.Unisex,
				Season:           entity.SeasonSS,
			},
			ThumbnailMediaID: mediaID,
			Translations: []entity.ColorwayTranslationInsert{
				{LanguageId: languageID, Name: "Backfill readiness", Description: "test"},
			},
			Prices: prices,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{{
			ProductSize: entity.VariantInsert{SizeId: sizeID, Quantity: decimal.NewFromInt(1)},
		}},
		MediaIds: []int{mediaID},
		Tags:     []entity.ColorwayTagInsert{},
		Prices:   prices,
	}

	productID, err := s.Products().AddProduct(ctx, payload)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, `DELETE FROM product WHERE id = ?`, productID) }()

	var variantID int64
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM product_size WHERE product_id = ? AND size_id = ?`, productID, sizeID).Scan(&variantID))

	var originalVariantSKU string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product_size WHERE id = ?`, variantID).Scan(&originalVariantSKU))

	// A shape-valid but non-canonical lowercase value must still be selected under MySQL's default
	// case-insensitive collation, then fully repaired to the exact canonical bytes.
	_, err = testDB.ExecContext(ctx, `UPDATE product_size SET sku = ? WHERE id = ?`, strings.ToLower(originalVariantSKU), variantID)
	require.NoError(t, err)
	require.NoError(t, s.productStore.BackfillSKUs(ctx))

	var baseSKU, variantSKU string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product WHERE id = ?`, productID).Scan(&baseSKU))
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product_size WHERE id = ?`, variantID).Scan(&variantSKU))
	require.Regexp(t, `^(SS|FW|PF|RC)[0-9]{2}-[0-9]{5}-[A-Z0-9]{3}$`, baseSKU)
	require.Equal(t, fmt.Sprintf("%s-%02d", baseSKU, sizeOrdinal), variantSKU)

	// A frozen corruption cannot be repaired. The global verifier returns both safe identifiers and
	// leaves the row untouched, which makes New fail its readiness gate in the real boot path.
	_, err = testDB.ExecContext(ctx, `UPDATE product SET sku_locked_at = NOW() WHERE id = ?`, productID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `UPDATE product_size SET sku = NULL WHERE id = ?`, variantID)
	require.NoError(t, err)

	err = s.productStore.BackfillSKUs(ctx)
	require.Error(t, err)
	require.ErrorContains(t, err, fmt.Sprintf("violation_product_ids=[%d]", productID))
	require.ErrorContains(t, err, fmt.Sprintf("violation_variant_ids=[%d]", variantID))

	var frozenVariantSKU sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product_size WHERE id = ?`, variantID).Scan(&frozenVariantSKU))
	require.False(t, frozenVariantSKU.Valid, "readiness verifier must not rewrite a frozen identity")
}
