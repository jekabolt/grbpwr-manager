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

// TestStyleSizeChartFullReplace is the acceptance test for the R5 style size chart: GetStyleSizeChart
// returns the whole chart + the shared tech_card.lock_version; UpdateStyleSizeChart replaces the entire
// chart under that lock (bumping it), a stale version is ErrTechCardConflict, and an empty payload
// clears the chart. DB-backed (tech_card_size_measurement); T-E runs it on the stand.
func TestStyleSizeChartFullReplace(t *testing.T) {
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

	var sizeA, sizeB int
	rows, err := testDB.QueryContext(ctx, `SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 2`)
	require.NoError(t, err)
	ids := []int{}
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	rows.Close()
	require.GreaterOrEqual(t, len(ids), 2)
	sizeA, sizeB = ids[0], ids[1]
	var measA, measB int
	mrows, err := testDB.QueryContext(ctx, `SELECT id FROM measurement_name ORDER BY id LIMIT 2`)
	require.NoError(t, err)
	mids := []int{}
	for mrows.Next() {
		var id int
		require.NoError(t, mrows.Scan(&id))
		mids = append(mids, id)
	}
	mrows.Close()
	require.GreaterOrEqual(t, len(mids), 2)
	measA, measB = mids[0], mids[1]

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
	prodID, err := s.Products().AddProduct(ctx, &entity.ColorwayNew{
		Product: &entity.ColorwayInsert{
			ProductBodyInsert: entity.ColorwayBodyInsert{Brand: "ACME", Color: "black", ColorCode: "BLK", CountryOfOrigin: "IT", TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS},
			ThumbnailMediaID:  mediaID,
			Translations:      []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "TCHART", Description: "d"}},
			Prices:            prices,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(1)}}},
		MediaIds:         []int{mediaID},
		Tags:             []entity.ColorwayTagInsert{},
		Prices:           prices,
	})
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID) }()

	var styleID int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT style_id FROM product WHERE id = ?`, prodID).Scan(&styleID))

	// Initial read: shared lock_version present.
	chart0, err := s.TechCards().GetStyleSizeChart(ctx, styleID)
	require.NoError(t, err)
	require.Equal(t, styleID, chart0.StyleID)
	v0 := chart0.LockVersion

	// Full-replace with two cells; the lock bumps.
	cells := []entity.StyleSizeChartCell{
		{SizeID: sizeA, MeasurementNameID: measA, Value: decimal.NewFromInt(50)},
		{SizeID: sizeB, MeasurementNameID: measB, Value: decimal.RequireFromString("51.5")},
	}
	updated, err := s.TechCards().UpdateStyleSizeChart(ctx, styleID, v0, cells)
	require.NoError(t, err)
	require.Equal(t, v0+1, updated.LockVersion)
	require.Len(t, updated.Cells, 2)

	// A stale version is a conflict (ABORTED upstream).
	_, err = s.TechCards().UpdateStyleSizeChart(ctx, styleID, v0, cells)
	require.ErrorIs(t, err, entity.ErrTechCardConflict)

	// Re-read returns the stored cells at the bumped version.
	chart1, err := s.TechCards().GetStyleSizeChart(ctx, styleID)
	require.NoError(t, err)
	require.Equal(t, v0+1, chart1.LockVersion)
	require.Len(t, chart1.Cells, 2)

	// An empty full-replace clears the chart (full-replace semantics, not upsert).
	cleared, err := s.TechCards().UpdateStyleSizeChart(ctx, styleID, v0+1, nil)
	require.NoError(t, err)
	require.Empty(t, cleared.Cells)
	require.Equal(t, v0+2, cleared.LockVersion)

	// An unknown style is sql.ErrNoRows.
	_, err = s.TechCards().GetStyleSizeChart(ctx, 999999999)
	require.ErrorIs(t, err, sql.ErrNoRows)
}
