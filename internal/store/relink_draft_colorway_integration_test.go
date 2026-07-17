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

// TestRelinkDraftColorway is the acceptance test for the R4 RelinkDraftColorway: only a DRAFT may be
// relinked, both sides are optimistically guarded on their shared lock_version, and the moved
// colourway's SKU is re-minted from the target style. DB-backed; T-E runs it on the stand.
func TestRelinkDraftColorway(t *testing.T) {
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
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&sizeA))
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
			Translations:      []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "TRELINK", Description: "d"}},
			Prices:            prices,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(1)}}},
		MediaIds:         []int{mediaID},
		Tags:             []entity.ColorwayTagInsert{},
		Prices:           prices,
	})
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID) }()

	var srcStyleID, srcLV int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT style_id FROM product WHERE id = ?`, prodID).Scan(&srcStyleID))
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, srcStyleID).Scan(&srcLV))

	// Target style: a bare tech_card with a complete season so the re-mint can build the base SKU.
	res, err := testDB.ExecContext(ctx, `INSERT INTO tech_card (style_number, name, season_code, season_year) VALUES (CONCAT('AUTO-TGT-', UUID_SHORT()), 'Target', 'SS', 2026)`)
	require.NoError(t, err)
	tgtStyleID64, err := res.LastInsertId()
	require.NoError(t, err)
	tgtStyleID := int(tgtStyleID64)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM tech_card WHERE id = ?", tgtStyleID) }()
	var tgtLV int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, tgtStyleID).Scan(&tgtLV))

	// A non-draft colourway cannot be relinked (AddProduct creates it ACTIVE via the legacy bridge).
	err = s.Products().RelinkDraftColorway(ctx, prodID, tgtStyleID, srcLV, tgtLV)
	require.ErrorIs(t, err, entity.ErrColorwayNotDraft)

	// Make it a draft (the merge/CreateColorway state) and relink.
	_, err = testDB.ExecContext(ctx, `UPDATE product SET lifecycle_status = 1 WHERE id = ?`, prodID)
	require.NoError(t, err)

	// A stale version on either side is a conflict.
	require.ErrorIs(t, s.Products().RelinkDraftColorway(ctx, prodID, tgtStyleID, srcLV+99, tgtLV), entity.ErrTechCardConflict)
	require.ErrorIs(t, s.Products().RelinkDraftColorway(ctx, prodID, tgtStyleID, srcLV, tgtLV+99), entity.ErrTechCardConflict)

	// Happy path: style_id moves and the base SKU is re-minted from the target style.
	require.NoError(t, s.Products().RelinkDraftColorway(ctx, prodID, tgtStyleID, srcLV, tgtLV))
	var newStyleID int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT style_id FROM product WHERE id = ?`, prodID).Scan(&newStyleID))
	require.Equal(t, tgtStyleID, newStyleID)

	// An unknown target style is sql.ErrNoRows.
	_, err = testDB.ExecContext(ctx, `UPDATE product SET lifecycle_status = 1 WHERE id = ?`, prodID)
	require.NoError(t, err)
	require.ErrorIs(t, s.Products().RelinkDraftColorway(ctx, prodID, 999999999, tgtLV+1, 0), sql.ErrNoRows)
}
