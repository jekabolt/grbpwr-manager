package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestSingleUseVoucherClaimLifecycle exercises the voucher claim as a database concurrency
// primitive, not as a cache convention. It intentionally uses the public order-store methods so
// create/cancel/expiry/payment/refund all run through their real transaction boundaries.
func TestSingleUseVoucherClaimLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	token := fmt.Sprintf("%d", time.Now().UnixNano())
	exec := func(query string, args ...any) int64 {
		res, err := testDB.ExecContext(ctx, query, args...)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}

	// Seed a dedicated carrier before loading the process cache so the test never depends on
	// environment-specific carrier/currency configuration.
	carrierID := exec(`INSERT INTO shipment_carrier (carrier, tracking_url, allowed, description)
		VALUES (?, 'https://example.test/%s', 1, 'voucher claim test')`, "VOUCHER-"+token)
	exec(`INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price)
		VALUES (?, 'EUR', 5.00)`, carrierID)

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
	cache.UpdatePaymentMethodAllowance(entity.CARD, true)
	paymentMethod, ok := cache.GetPaymentMethodByName(entity.CARD)
	require.True(t, ok)
	require.True(t, paymentMethod.Method.Allowed)

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://example.test/voucher-full.jpg", FullSizeWidth: 10, FullSizeHeight: 10,
		ThumbnailMediaURL: "https://example.test/voucher-thumb.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://example.test/voucher-compressed.jpg", CompressedWidth: 10, CompressedHeight: 10,
		BlurHash: sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
	})
	require.NoError(t, err)

	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 1`).Scan(&sizeID))
	styleID := exec(`INSERT INTO tech_card
		(style_number, name, brand, collection, season_code, season_year, season, target_gender, top_category_id)
		VALUES (?, 'Voucher test', 'ACME', '', 'SS', 2026, 'SS26', 'unisex', 1)`, "VC-"+token)
	productID := exec(`INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status)
		VALUES (?, 'black', 'BLK', '#000000', 'PL', ?, ?, 2)`, "VC-P-"+token, mediaID, styleID)
	exec(`INSERT INTO product_price (product_id, currency, price) VALUES (?, 'EUR', 100.00)`, productID)
	variantSKU := "VC-V-" + token
	variantID := exec(`INSERT INTO product_size (product_id, size_id, quantity, sku)
		VALUES (?, ?, 50, ?)`, productID, sizeID, variantSKU)

	type madeOrder struct {
		id   int
		uuid string
	}
	var ordersMu sync.Mutex
	var orders []madeOrder
	var promoCodes []string

	trackOrder := func(order *entity.Order) {
		ordersMu.Lock()
		orders = append(orders, madeOrder{id: order.Id, uuid: order.UUID})
		ordersMu.Unlock()
	}
	newOrder := func(code, suffix string) *entity.OrderNew {
		address := &entity.AddressInsert{Country: "", City: "Warsaw", AddressLineOne: "1 Test St", PostalCode: "00-001"}
		return &entity.OrderNew{
			Items:             []entity.OrderItemInsert{{VariantSKU: variantSKU, Quantity: decimal.NewFromInt(1)}},
			ShippingAddress:   address,
			BillingAddress:    address,
			Buyer:             &entity.BuyerInsert{FirstName: "Voucher", LastName: "Test", Email: fmt.Sprintf("voucher-%s-%s@example.com", token, suffix), Phone: "48123456789"},
			PaymentMethod:     entity.CARD,
			ShipmentCarrierId: int(carrierID),
			PromoCode:         code,
			Currency:          "EUR",
		}
	}
	createOrder := func(code, suffix string) (*entity.Order, error) {
		order, _, err := s.Order().CreateOrder(ctx, newOrder(code, suffix), false, time.Now().UTC().Add(time.Hour))
		if err == nil {
			trackOrder(order)
		}
		return order, err
	}
	addPromo := func(name string, voucher bool) string {
		code := fmt.Sprintf("VC-%s-%s", name, token)
		require.NoError(t, s.Promo().AddPromo(ctx, &entity.PromoCodeInsert{
			Code:       code,
			Discount:   decimal.NewFromInt(10),
			Start:      time.Now().UTC().Add(-time.Hour),
			Expiration: time.Now().UTC().Add(24 * time.Hour),
			Voucher:    voucher,
			Allowed:    true,
		}))
		promoCodes = append(promoCodes, code)
		return code
	}
	assertAllowed := func(code string, want bool) {
		t.Helper()
		var got bool
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT allowed FROM promo_code WHERE code = ?`, code).Scan(&got))
		require.Equal(t, want, got, "database allowed flag for %s", code)
		cached, ok := cache.GetPromoByCode(code)
		require.True(t, ok)
		require.Equal(t, want, cached.Allowed, "cache allowed flag for %s", code)
	}
	assertPromoRejected := func(err error) {
		t.Helper()
		require.Error(t, err)
		var validationErr *entity.ValidationError
		require.True(t, errors.As(err, &validationErr), "expected ValidationError, got %T: %v", err, err)
		require.Equal(t, "promo code is not allowed", validationErr.Message)
	}

	// Use a fresh background context because test defers cancel ctx before Cleanup callbacks run.
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		ordersMu.Lock()
		made := append([]madeOrder(nil), orders...)
		ordersMu.Unlock()
		for _, order := range made {
			var billingID, shippingID sql.NullInt64
			_ = testDB.QueryRowContext(cleanupCtx,
				`SELECT billing_address_id, shipping_address_id FROM buyer WHERE order_id = ?`, order.id).
				Scan(&billingID, &shippingID)
			_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM acct_event WHERE source_key = ? OR source_key LIKE ?`, order.uuid, order.uuid+":%")
			_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM product_stock_change_history WHERE order_id = ?`, order.id)
			_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM customer_order WHERE id = ?`, order.id)
			if billingID.Valid {
				_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM address WHERE id = ?`, billingID.Int64)
			}
			if shippingID.Valid && (!billingID.Valid || shippingID.Int64 != billingID.Int64) {
				_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM address WHERE id = ?`, shippingID.Int64)
			}
		}
		for _, code := range promoCodes {
			_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM promo_code WHERE code = ?`, code)
			cache.DeletePromo(code)
		}
		_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM product_size WHERE id = ?`, variantID)
		_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM product_price WHERE product_id = ?`, productID)
		_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM product WHERE id = ?`, productID)
		_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM tech_card WHERE id = ?`, styleID)
		_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM media WHERE id = ?`, mediaID)
		_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM shipment_carrier_price WHERE shipment_carrier_id = ?`, carrierID)
		_, _ = testDB.ExecContext(cleanupCtx, `DELETE FROM shipment_carrier WHERE id = ?`, carrierID)
	})

	t.Run("claim_second_refused_and_cancel_releases", func(t *testing.T) {
		code := addPromo("CLAIM", true)
		first, err := createOrder(code, "claim-first")
		require.NoError(t, err)
		require.True(t, first.PromoId.Valid)
		assertAllowed(code, false)

		_, err = createOrder(code, "claim-second")
		assertPromoRejected(err)

		var attached int
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM customer_order co
			JOIN promo_code pc ON pc.id = co.promo_id
			WHERE pc.code = ?`, code).Scan(&attached))
		require.Equal(t, 1, attached, "only the winning order may hold the voucher")

		require.NoError(t, s.Order().CancelOrder(ctx, first.UUID))
		assertAllowed(code, true)
		replacement, err := createOrder(code, "claim-after-cancel")
		require.NoError(t, err)
		assertAllowed(code, false)
		require.NoError(t, s.Order().CancelOrder(ctx, replacement.UUID))
		assertAllowed(code, true)
	})

	t.Run("payment_expiry_releases", func(t *testing.T) {
		code := addPromo("EXPIRE", true)
		order, err := createOrder(code, "expire-first")
		require.NoError(t, err)
		_, err = s.Order().InsertFiatInvoice(ctx, order.UUID, "pi-expire-"+token, paymentMethod.Method, time.Now().UTC().Add(-time.Minute))
		require.NoError(t, err)
		_, err = s.Order().ExpireOrderPayment(ctx, order.UUID)
		require.NoError(t, err)
		assertAllowed(code, true)

		replacement, err := createOrder(code, "expire-after")
		require.NoError(t, err)
		require.NoError(t, s.Order().CancelOrder(ctx, replacement.UUID))
	})

	t.Run("payment_and_refund_keep_consumed", func(t *testing.T) {
		code := addPromo("PAID", true)
		order, err := createOrder(code, "paid")
		require.NoError(t, err)
		full, err := s.Order().InsertFiatInvoice(ctx, order.UUID, "pi-paid-"+token, paymentMethod.Method, time.Now().UTC().Add(time.Hour))
		require.NoError(t, err)
		updated, err := s.Order().OrderPaymentDone(ctx, order.UUID, &full.Payment)
		require.NoError(t, err)
		require.True(t, updated)
		assertAllowed(code, false)

		// Refunds reverse money/stock, not the one-time entitlement: a paid voucher stays spent.
		require.NoError(t, s.Order().RefundOrder(ctx, order.UUID, nil, "voucher test", "", true, entity.RefundDispositionRestock))
		assertAllowed(code, false)
	})

	t.Run("reusable_promo_is_unchanged", func(t *testing.T) {
		code := addPromo("REUSE", false)
		first, err := createOrder(code, "reuse-first")
		require.NoError(t, err)
		second, err := createOrder(code, "reuse-second")
		require.NoError(t, err)
		assertAllowed(code, true)
		require.NoError(t, s.Order().CancelOrder(ctx, first.UUID))
		require.NoError(t, s.Order().CancelOrder(ctx, second.UUID))
		assertAllowed(code, true)
	})

	t.Run("parallel_creates_have_one_winner", func(t *testing.T) {
		code := addPromo("RACE", true)
		start := make(chan struct{})
		results := make(chan struct {
			order *entity.Order
			err   error
		}, 2)
		for i := 0; i < 2; i++ {
			i := i
			go func() {
				<-start
				order, err := createOrder(code, fmt.Sprintf("race-%d", i))
				results <- struct {
					order *entity.Order
					err   error
				}{order: order, err: err}
			}()
		}
		close(start)

		var winner *entity.Order
		var successes, failures int
		for i := 0; i < 2; i++ {
			result := <-results
			if result.err == nil {
				successes++
				winner = result.order
				continue
			}
			failures++
			assertPromoRejected(result.err)
		}
		require.Equal(t, 1, successes)
		require.Equal(t, 1, failures)
		require.NotNil(t, winner)
		assertAllowed(code, false)
		require.NoError(t, s.Order().CancelOrder(ctx, winner.UUID))
		assertAllowed(code, true)
	})
}
