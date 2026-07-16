package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestPaymentResnapshotsLiveSKU is the acceptance test for problem 003: at payment the order line's
// product_sku must be re-snapshotted from the CURRENT live variant SKU (which may have changed in the
// checkout->payment window), then the product is frozen — in one transaction. A retried payment is a
// no-op, and a missing live variant SKU rejects the payment.
func TestPaymentResnapshotsLiveSKU(t *testing.T) {
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
	var langID, pmID, awaitingID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM payment_method").Scan(&pmID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM order_status WHERE name = ?", string(entity.AwaitingPayment)).Scan(&awaitingID))

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
					Brand: "ACME", Color: "black", ColorHex: "#000000", CountryOfOrigin: "IT",
					TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
				},
				ThumbnailMediaID: mediaID,
				Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "T03", Description: "d"}},
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

	// seedOrder creates an AwaitingPayment order with one line, snapshotting checkoutSKU on the line.
	seedOrder := func(uuid string, prodID int, checkoutSKU string) int64 {
		res, err := testDB.ExecContext(ctx,
			`INSERT INTO customer_order (uuid, order_status_id, currency, total_price) VALUES (?, ?, 'EUR', 100)`,
			uuid, awaitingID)
		require.NoError(t, err)
		oid, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx,
			`INSERT INTO order_item (order_id, product_id, product_price, product_price_base, quantity, size_id, product_sku)
			 VALUES (?, ?, 100, 100, 1, ?, ?)`, oid, prodID, sizeA, checkoutSKU)
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx,
			`INSERT INTO payment (order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done)
			 VALUES (?, ?, 100, 100, 0)`, oid, pmID)
		require.NoError(t, err)
		return oid
	}
	pay := func(uuid string) (bool, error) {
		return s.Order().OrderPaymentDone(ctx, uuid, &entity.Payment{
			PaymentInsert: entity.PaymentInsert{
				PaymentMethodID:                  pmID,
				TransactionAmount:                decimal.NewFromInt(100),
				TransactionAmountPaymentCurrency: decimal.NewFromInt(100),
			},
		})
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

	// --- main scenario: checkout snapshot A, live remint to B, payment must capture B ---
	prodID := mkProduct()
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID) }()
	// the checkout snapshot is whatever AddProduct minted for the variant
	var checkoutSKU string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product_size WHERE product_id = ? AND size_id = ?`, prodID, sizeA).Scan(&checkoutSKU))
	// live remint while still unlocked: the variant SKU changes to B before payment
	liveSKU := checkoutSKU + "X"
	_, err = testDB.ExecContext(ctx, `UPDATE product_size SET sku = ? WHERE product_id = ? AND size_id = ?`, liveSKU, prodID, sizeA)
	require.NoError(t, err)

	oid := seedOrder("T03-RESNAP-0001", prodID, checkoutSKU)
	defer func() {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM payment WHERE order_id = ?", oid)
		_, _ = testDB.ExecContext(ctx, "DELETE FROM order_item WHERE order_id = ?", oid)
		_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", oid)
	}()
	require.Equal(t, checkoutSKU, lineSKU(oid), "precondition: line holds the stale checkout snapshot")

	updated, err := pay("T03-RESNAP-0001")
	require.NoError(t, err)
	require.True(t, updated)
	require.Equal(t, liveSKU, lineSKU(oid), "payment must re-snapshot the line to the live variant SKU")
	require.True(t, locked(prodID), "payment must freeze the product")

	// --- retry is a no-op (order now Confirmed, not AwaitingPayment) ---
	updated2, err := pay("T03-RESNAP-0001")
	require.NoError(t, err)
	require.False(t, updated2, "retried payment is a no-op")
	require.Equal(t, liveSKU, lineSKU(oid), "retry leaves the snapshot unchanged")

	// --- missing live variant SKU rejects the payment ---
	prodID2 := mkProduct()
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID2) }()
	_, err = testDB.ExecContext(ctx, `UPDATE product_size SET sku = NULL WHERE product_id = ? AND size_id = ?`, prodID2, sizeA)
	require.NoError(t, err)
	oid2 := seedOrder("T03-RESNAP-0002", prodID2, "")
	defer func() {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM payment WHERE order_id = ?", oid2)
		_, _ = testDB.ExecContext(ctx, "DELETE FROM order_item WHERE order_id = ?", oid2)
		_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", oid2)
	}()
	_, err = pay("T03-RESNAP-0002")
	require.Error(t, err, "payment must be rejected when a line has no live variant SKU")
	require.False(t, locked(prodID2), "a rejected payment must not freeze the product")
	_ = fmt.Sprint(oid2)
}
