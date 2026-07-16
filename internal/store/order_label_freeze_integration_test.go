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

// TestSetShipmentLabelFreezesSKU is the acceptance test for problem 035: the first persisted
// shipping label is a SKU-freeze lifecycle point. Persisting the label must, in one transaction,
// re-snapshot each line's product_sku from the live variant and stamp sku_locked_at — so a
// confirmed-but-still-unlocked order can never have its identity drift once a label exists. A line
// with no live variant SKU is a hard failure that rolls back the label persist (no partial freeze),
// and a repeated persist is idempotent.
func TestSetShipmentLabelFreezesSKU(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	var langID, carrierID, confirmedID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM shipment_carrier").Scan(&carrierID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))

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
	mkProduct := func() int {
		p := &entity.ColorwayNew{
			Product: &entity.ColorwayInsert{
				ProductBodyInsert: entity.ColorwayBodyInsert{
					Brand: "ACME", Color: "black", ColorCode: "BLK", ColorHexOverride: sql.NullString{String: "#000000", Valid: true}, CountryOfOrigin: "IT",
					TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
				},
				ThumbnailMediaID: mediaID,
				Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "T35", Description: "d"}},
				Prices:           prices,
			},
			SizeMeasurements: []entity.SizeWithMeasurementInsert{
				{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(5)}},
			},
			MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
		}
		id, err := s.Products().AddProduct(ctx, p)
		require.NoError(t, err)
		return id
	}

	// seedConfirmedOrder builds a Confirmed order with one line + a shipment, but leaves the product
	// UNLOCKED (the anomalous case problem 035 protects: label reached on a not-yet-frozen order).
	seedConfirmedOrder := func(uuid string, prodID int, checkoutSKU string) int64 {
		res, err := testDB.ExecContext(ctx,
			`INSERT INTO customer_order (uuid, order_status_id, currency, total_price) VALUES (?, ?, 'EUR', 100)`,
			uuid, confirmedID)
		require.NoError(t, err)
		oid, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx,
			`INSERT INTO order_item (order_id, product_id, product_price, product_price_base, quantity, size_id, product_sku)
			 VALUES (?, ?, 100, 100, 1, ?, ?)`, oid, prodID, sizeA, checkoutSKU)
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx,
			`INSERT INTO shipment (carrier_id, order_id, cost, free_shipping) VALUES (?, ?, 0, 0)`,
			carrierID, oid)
		require.NoError(t, err)
		return oid
	}
	cleanupOrder := func(oid int64) {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM shipment WHERE order_id = ?", oid)
		_, _ = testDB.ExecContext(ctx, "DELETE FROM order_item WHERE order_id = ?", oid)
		_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", oid)
	}
	lineSKU := func(oid int64) string {
		var sku string
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COALESCE(product_sku,'') FROM order_item WHERE order_id = ?`, oid).Scan(&sku))
		return sku
	}
	locked := func(prodID int) bool {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM product WHERE id = ? AND sku_locked_at IS NOT NULL`, prodID).Scan(&n))
		return n > 0
	}
	labelURL := func(oid int64) string {
		var u string
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COALESCE(label_url,'') FROM shipment WHERE order_id = ?`, oid).Scan(&u))
		return u
	}

	label := func(url string) entity.ShipmentLabel {
		return entity.ShipmentLabel{
			LabelURL:          url,
			CarrierShipmentID: "sc-" + url,
			ServiceType:       "standard",
			ParcelWeightGrams: 500,
			ParcelDimensions:  "30x20x10 cm",
		}
	}

	// --- 1) success: live remint B while unlocked, then the label persist re-snapshots B + freezes ---
	prodID := mkProduct()
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID) }()
	var checkoutSKU string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product_size WHERE product_id = ? AND size_id = ?`, prodID, sizeA).Scan(&checkoutSKU))
	liveSKU := checkoutSKU + "X"
	_, err = testDB.ExecContext(ctx, `UPDATE product_size SET sku = ? WHERE product_id = ? AND size_id = ?`, liveSKU, prodID, sizeA)
	require.NoError(t, err)

	oid := seedConfirmedOrder("T35-LABEL-0001", prodID, checkoutSKU)
	defer cleanupOrder(oid)
	require.Equal(t, checkoutSKU, lineSKU(oid), "precondition: line holds the stale checkout snapshot")
	require.False(t, locked(prodID), "precondition: product not yet frozen")

	require.NoError(t, s.Order().SetShipmentLabel(ctx, "T35-LABEL-0001", label("first")))
	require.Equal(t, liveSKU, lineSKU(oid), "label persist must re-snapshot the line to the live variant SKU")
	require.True(t, locked(prodID), "label persist must freeze the product")
	require.Equal(t, "first", labelURL(oid), "label must be persisted")

	// --- 2) repeat is idempotent: the frozen live SKU is immutable, snapshot unchanged ---
	require.NoError(t, s.Order().SetShipmentLabel(ctx, "T35-LABEL-0001", label("second")))
	require.Equal(t, liveSKU, lineSKU(oid), "repeat leaves the frozen snapshot unchanged")
	require.Equal(t, "second", labelURL(oid), "repeat still updates label columns")

	// --- 3) missing live SKU: hard failure, label NOT saved, product NOT frozen (rollback) ---
	prodID2 := mkProduct()
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID2) }()
	_, err = testDB.ExecContext(ctx, `UPDATE product_size SET sku = NULL WHERE product_id = ? AND size_id = ?`, prodID2, sizeA)
	require.NoError(t, err)
	oid2 := seedConfirmedOrder("T35-LABEL-0002", prodID2, "")
	defer cleanupOrder(oid2)

	err = s.Order().SetShipmentLabel(ctx, "T35-LABEL-0002", label("should-not-persist"))
	require.Error(t, err, "label persist must be rejected when a line has no live variant SKU")
	require.False(t, locked(prodID2), "a rejected label must not freeze the product")
	require.Equal(t, "", labelURL(oid2), "a rejected label must not be persisted (rollback)")
}
