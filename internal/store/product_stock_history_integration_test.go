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

// TestUpdateProductStockHistoryIsTrueDelta is the acceptance test for problem 024: an update must
// record stock history with the real before/after/delta (not a fabricated 0->new), and an
// identity-only save (no quantity change) must produce no stock event at all.
func TestUpdateProductStockHistoryIsTrueDelta(t *testing.T) {
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

	var sizeA int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 1`).Scan(&sizeA))
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
	mkPayload := func(qty int64) *entity.ColorwayNew {
		return &entity.ColorwayNew{
			Product: &entity.ColorwayInsert{
				ProductBodyInsert: entity.ColorwayBodyInsert{
					Brand: "ACME", Color: "black", ColorCode: "BLK", ColorHexOverride: sql.NullString{String: "#000000", Valid: true}, CountryOfOrigin: "IT",
					TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
				},
				ThumbnailMediaID: mediaID,
				Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "T24", Description: "d"}},
				Prices:           prices,
			},
			SizeMeasurements: []entity.SizeWithMeasurementInsert{
				{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(qty)}},
			},
			MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
		}
	}

	prodID, err := s.Products().AddProduct(ctx, mkPayload(10))
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID) }()

	histCount := func() int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_stock_change_history WHERE product_id = ?`, prodID).Scan(&n))
		return n
	}
	lastDelta := func() (before, after, delta string) {
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT quantity_before, quantity_after, quantity_delta FROM product_stock_change_history
			 WHERE product_id = ? ORDER BY id DESC LIMIT 1`, prodID).Scan(&before, &after, &delta))
		return
	}

	afterAdd := histCount() // initial-stock event(s) from AddProduct

	// identity-only save (same quantity): no new stock event
	require.NoError(t, s.Products().UpdateProduct(ctx, mkPayload(10), prodID))
	require.Equal(t, afterAdd, histCount(), "an unchanged save must not record a stock event")

	// decrease 10 -> 8: exactly one new event with the TRUE before/after/delta
	require.NoError(t, s.Products().UpdateProduct(ctx, mkPayload(8), prodID))
	require.Equal(t, afterAdd+1, histCount(), "a quantity change records exactly one event")
	before, after, delta := lastDelta()
	require.Equal(t, "10.00", before, "before must be the real previous quantity, not 0")
	require.Equal(t, "8.00", after)
	require.Equal(t, "-2.00", delta, "delta must be -2, not +8")
}
