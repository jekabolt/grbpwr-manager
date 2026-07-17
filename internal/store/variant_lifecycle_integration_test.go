package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestVariantCRUD is the acceptance test for the R2 admin Variant CRUD (D2-core): CreateVariant adds a
// zero-stock ACTIVE variant with a minted SKU, rejects a duplicate size, and rejects an unknown or
// archived colourway; SetVariantStatus archives (and un-archives) under the optimistic guard and never
// touches the size/SKU. DB-backed (product_size.status, migration 0155); T-E runs it on the stand.
func TestVariantCRUD(t *testing.T) {
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

	sizeRows, err := testDB.QueryContext(ctx, `SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 2`)
	require.NoError(t, err)
	var sizeIDs []int
	for sizeRows.Next() {
		var id int
		require.NoError(t, sizeRows.Scan(&id))
		sizeIDs = append(sizeIDs, id)
	}
	require.NoError(t, sizeRows.Err())
	sizeRows.Close()
	require.GreaterOrEqual(t, len(sizeIDs), 2)
	sizeA, sizeB := sizeIDs[0], sizeIDs[1]

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

	payload := &entity.ColorwayNew{
		Product: &entity.ColorwayInsert{
			ProductBodyInsert: entity.ColorwayBodyInsert{
				Brand: "ACME", Color: "black", ColorCode: "BLK", ColorHexOverride: sql.NullString{String: "#000000", Valid: true}, CountryOfOrigin: "IT",
				TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
			},
			ThumbnailMediaID: mediaID,
			Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "TVAR", Description: "d"}},
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

	// 1) CreateVariant adds a zero-stock ACTIVE variant for a new size, with a minted SKU.
	v, err := s.Products().CreateVariant(ctx, prodID, sizeB)
	require.NoError(t, err)
	require.Equal(t, prodID, v.ProductId)
	require.Equal(t, sizeB, v.SizeId)
	require.True(t, v.Quantity.Equal(decimal.Zero), "new variant starts at zero stock")
	require.Equal(t, uint8(entity.VariantStatusActive), v.Status)
	require.True(t, v.SKU.Valid && v.SKU.String != "", "the variant SKU is minted from the built base")

	// 2) A duplicate size is rejected (UNIQUE(product_id, size_id)).
	_, err = s.Products().CreateVariant(ctx, prodID, sizeB)
	require.ErrorIs(t, err, entity.ErrVariantExists)

	// 3) An unknown colourway is sql.ErrNoRows (NOT_FOUND upstream).
	_, err = s.Products().CreateVariant(ctx, 999999999, sizeB)
	require.ErrorIs(t, err, sql.ErrNoRows)

	// 4) SetVariantStatus archives the variant under the optimistic guard; the SKU/size are untouched.
	archived, err := s.Products().SetVariantStatus(ctx, v.Id, entity.VariantStatusArchived)
	require.NoError(t, err)
	require.Equal(t, uint8(entity.VariantStatusArchived), archived.Status)
	require.Equal(t, v.SKU.String, archived.SKU.String)
	require.Equal(t, v.SizeId, archived.SizeId)

	got, err := s.Products().GetVariantByID(ctx, v.Id)
	require.NoError(t, err)
	require.Equal(t, uint8(entity.VariantStatusArchived), got.Status)

	// 5) Un-archive is allowed (ACTIVE via the same patch path).
	active, err := s.Products().SetVariantStatus(ctx, v.Id, entity.VariantStatusActive)
	require.NoError(t, err)
	require.Equal(t, uint8(entity.VariantStatusActive), active.Status)

	// 6) SetVariantStatus on an unknown variant is sql.ErrNoRows.
	_, err = s.Products().SetVariantStatus(ctx, 999999999, entity.VariantStatusArchived)
	require.True(t, errors.Is(err, sql.ErrNoRows))
}
