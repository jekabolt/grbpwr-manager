package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestFrozenUpdatePreservesVariantSKUs is the acceptance test for problem 001: an ordinary save of a
// FROZEN colourway (no size change) must not blank any variant SKU, and adding a new size to a frozen
// colourway must mint a valid variant SKU from the frozen base — never a NULL row, never a rebuilt base.
func TestFrozenUpdatePreservesVariantSKUs(t *testing.T) {
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

	// three distinct real sizes with a non-zero SKU ordinal (so variants can be minted)
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
	require.GreaterOrEqual(t, len(sizeIDs), 3, "need 3 seeded sizes with ordinals")
	sizeA, sizeB, sizeC := sizeIDs[0], sizeIDs[1], sizeIDs[2]

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)

	var langID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))

	prices := make([]entity.ColorwayPriceInsert, 0)
	for _, c := range currency.RequiredCurrencies() {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(10000)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(10000)})
	}

	mkPayload := func(sizeIDs []int) *entity.ColorwayNew {
		sms := make([]entity.SizeWithMeasurementInsert, 0, len(sizeIDs))
		for _, sid := range sizeIDs {
			sms = append(sms, entity.SizeWithMeasurementInsert{
				ProductSize: entity.VariantInsert{SizeId: sid, Quantity: decimal.NewFromInt(5)},
			})
		}
		return &entity.ColorwayNew{
			Product: &entity.ColorwayInsert{
				ProductBodyInsert: entity.ColorwayBodyInsert{
					Brand:           "ACME",
					Color:           "black",
					ColorHex:        "#000000",
					CountryOfOrigin: "IT",
					TopCategoryId:   1,
					TargetGender:    entity.Unisex,
					Season:          entity.SeasonSS,
				},
				ThumbnailMediaID: mediaID,
				Translations: []entity.ColorwayTranslationInsert{
					{LanguageId: langID, Name: "Test Coat", Description: "Test"},
				},
				Prices: prices,
			},
			SizeMeasurements: sms,
			MediaIds:         []int{mediaID},
			Tags:             []entity.ColorwayTagInsert{},
			Prices:           prices,
		}
	}

	prodID, err := s.Products().AddProduct(ctx, mkPayload([]int{sizeA, sizeB}))
	require.NoError(t, err)
	defer func() {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID)
	}()

	type variant struct {
		Id  int    `db:"id"`
		SKU string `db:"sku"`
	}
	read := func() map[int]variant {
		out := map[int]variant{}
		r, err := testDB.QueryContext(ctx,
			`SELECT size_id, id, COALESCE(sku,'') FROM product_size WHERE product_id = ? ORDER BY size_id`, prodID)
		require.NoError(t, err)
		defer r.Close()
		for r.Next() {
			var sid, id int
			var sku string
			require.NoError(t, r.Scan(&sid, &id, &sku))
			out[sid] = variant{Id: id, SKU: sku}
		}
		require.NoError(t, r.Err())
		return out
	}

	afterAdd := read()
	require.Len(t, afterAdd, 2)
	require.NotEmpty(t, afterAdd[sizeA].SKU, "size A minted a variant SKU")
	require.NotEmpty(t, afterAdd[sizeB].SKU, "size B minted a variant SKU")

	// Freeze the colourway (as first sale / first label would).
	_, err = testDB.ExecContext(ctx, "UPDATE product SET sku_locked_at = NOW() WHERE id = ?", prodID)
	require.NoError(t, err)

	// 1) Ordinary save, no size change: ids AND SKUs must be byte-for-byte stable.
	require.NoError(t, s.Products().UpdateProduct(ctx, mkPayload([]int{sizeA, sizeB}), prodID))
	afterSave := read()
	require.Len(t, afterSave, 2)
	require.Equal(t, afterAdd[sizeA], afterSave[sizeA], "frozen save must not change size A row id/sku")
	require.Equal(t, afterAdd[sizeB], afterSave[sizeB], "frozen save must not change size B row id/sku")

	// 2) Add a new size to the frozen colourway: originals unchanged, new size gets a valid variant SKU.
	require.NoError(t, s.Products().UpdateProduct(ctx, mkPayload([]int{sizeA, sizeB, sizeC}), prodID))
	afterAddSize := read()
	require.Len(t, afterAddSize, 3)
	require.Equal(t, afterAdd[sizeA], afterAddSize[sizeA], "adding a size must not touch size A")
	require.Equal(t, afterAdd[sizeB], afterAddSize[sizeB], "adding a size must not touch size B")
	require.NotEmpty(t, afterAddSize[sizeC].SKU, "new size must get a variant SKU, not NULL")

	// The new variant SKU must share the frozen base (prefix before the final "-NN" segment).
	base := afterAdd[sizeA].SKU[:len(afterAdd[sizeA].SKU)-3] // strip "-NN"
	require.Contains(t, afterAddSize[sizeC].SKU, base, "new variant derives from the frozen base")

	// No NULL/empty variant SKUs anywhere on the frozen product.
	for sid, v := range afterAddSize {
		require.NotEmpty(t, v.SKU, "variant for size %d must not be NULL/empty", sid)
	}
}
