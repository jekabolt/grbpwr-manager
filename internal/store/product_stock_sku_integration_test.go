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

// TestStockPathMintsVariantSKU is the acceptance test for problem 002: a stock update for a size the
// colourway did not have must materialise the variant WITH a SKU (never NULL), for both unlocked and
// frozen products; a stock update for an existing variant must not touch its SKU; and an unmintable
// size (no SKU ordinal) must fail rather than leave a SKU-less variant.
func TestStockPathMintsVariantSKU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// A size with sku_ord = 0 must exist in the cache to exercise the unmintable path — seed one before
	// cache init so GetSizeById returns it with SkuOrd == 0.
	zeroRes, err := testDB.ExecContext(ctx, `INSERT INTO size (name, sku_ord) VALUES ('T02-ZERO', 0)`)
	require.NoError(t, err)
	zeroSizeID64, err := zeroRes.LastInsertId()
	require.NoError(t, err)
	zeroSizeID := int(zeroSizeID64)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM size WHERE id = ?", zeroSizeID) }()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	sizeRows, err := testDB.QueryContext(ctx, `SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 3`)
	require.NoError(t, err)
	var sizeIDs []int
	for sizeRows.Next() {
		var id int
		require.NoError(t, sizeRows.Scan(&id))
		sizeIDs = append(sizeIDs, id)
	}
	require.NoError(t, sizeRows.Err())
	sizeRows.Close()
	require.GreaterOrEqual(t, len(sizeIDs), 3)
	sizeA, sizeB, sizeC := sizeIDs[0], sizeIDs[1], sizeIDs[2]

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

	// product starts with a single size (sizeA)
	payload := &entity.ColorwayNew{
		Product: &entity.ColorwayInsert{
			ProductBodyInsert: entity.ColorwayBodyInsert{
				Brand: "ACME", Color: "black", ColorCode: "BLK", ColorHexOverride: sql.NullString{String: "#000000", Valid: true}, CountryOfOrigin: "IT",
				TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
			},
			ThumbnailMediaID: mediaID,
			Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "T02", Description: "d"}},
			Prices:           prices,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{
			{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(5)}},
		},
		MediaIds: []int{mediaID},
		Tags:     []entity.ColorwayTagInsert{},
		Prices:   prices,
	}
	prodID, err := s.Products().AddProduct(ctx, payload)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID) }()

	skuOf := func(sizeID int) (string, bool) {
		var sku string
		var ok bool
		row := testDB.QueryRowContext(ctx, `SELECT COALESCE(sku,'') FROM product_size WHERE product_id = ? AND size_id = ?`, prodID, sizeID)
		if err := row.Scan(&sku); err != nil {
			return "", false
		}
		ok = true
		return sku, ok
	}

	skuA0, ok := skuOf(sizeA)
	require.True(t, ok)
	require.NotEmpty(t, skuA0, "AddProduct minted variant A")

	// 1) existing variant stock update — quantity changes, SKU identity does NOT
	require.NoError(t, s.Products().UpdateProductSizeStock(ctx, prodID, sizeA, 3))
	skuA1, _ := skuOf(sizeA)
	require.Equal(t, skuA0, skuA1, "stock update must not change an existing variant's SKU")

	// 2) stock for an absent size on an unlocked product — new variant gets a SKU
	require.NoError(t, s.Products().UpdateProductSizeStock(ctx, prodID, sizeB, 4))
	skuB, ok := skuOf(sizeB)
	require.True(t, ok, "variant B row created")
	require.NotEmpty(t, skuB, "new variant via stock path must get a SKU, not NULL")

	// 3) freeze, then stock for another absent size — new variant still gets a SKU from the frozen base
	_, err = testDB.ExecContext(ctx, "UPDATE product SET sku_locked_at = NOW() WHERE id = ?", prodID)
	require.NoError(t, err)
	require.NoError(t, s.Products().UpdateProductSizeStock(ctx, prodID, sizeC, 2))
	skuC, ok := skuOf(sizeC)
	require.True(t, ok)
	require.NotEmpty(t, skuC, "new variant on a frozen product must get a SKU")

	// 4) unmintable size (sku_ord = 0) via the transactional admin path — errors AND leaves no row
	err = s.Products().UpdateProductSizeStockWithHistory(ctx, prodID, zeroSizeID, 1, "correction", "")
	require.Error(t, err, "a size with no SKU ordinal must not silently create a SKU-less variant")
	_, exists := skuOf(zeroSizeID)
	require.False(t, exists, "the failed stock update must roll back — no SKU-less variant row left behind")
}
