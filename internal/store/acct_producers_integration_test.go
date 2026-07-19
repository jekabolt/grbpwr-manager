package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestAcctEventProducers is the PR4 (docs/plan-accounting/03-event-capture.md, 09 §9.5) acceptance
// for the three outbox producers wired into the hot order paths. It proves, end to end against a real
// DB, that each confirming/refunding path enqueues exactly the right acct_event row inside the same
// transaction:
//
//	(a) OrderPaymentDone   -> one order_paid event; a retried (no-op) payment adds no duplicate, and a
//	                          direct re-enqueue is a no-op (ON DUPLICATE KEY).
//	(b) CreateCustomOrder  -> one order_paid event (cash order never passes through OrderPaymentDone).
//	(c) RefundOrder        -> one order_refund event per refund, source_key uuid:1 then uuid:2, each
//	                          payload carrying THIS refund's exact amount and per-item quantities.
//	(d) enqueue is part of the caller's tx: a failure after EnqueueEvent rolls the event back.
//
// It seeds its own products/orders and cleans its own acct_event rows (source_key prefix
// 'ACCT-PROD-' + the one custom order's generated uuid), so it does not disturb the other suites.
func TestAcctEventProducers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	// Remove every event this suite creates: the seeded orders use the 'ACCT-PROD-' prefix; the one
	// custom order gets a generated uuid, cleaned by its own subtest defer.
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_event WHERE source_key LIKE 'ACCT-PROD-%'")
	})

	// --- reference ids --------------------------------------------------------------------------
	var sizeA int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 1`).Scan(&sizeA))
	var langID, pmID, awaitingID, deliveredID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM payment_method").Scan(&pmID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM order_status WHERE name = ?", string(entity.AwaitingPayment)).Scan(&awaitingID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM order_status WHERE name = ?", string(entity.Delivered)).Scan(&deliveredID))
	var carrierID int
	for _, c := range cache.GetShipmentCarriers() {
		if c.Allowed {
			carrierID = c.Id
			break
		}
	}
	require.NotZero(t, carrierID, "need an allowed shipment carrier")

	// --- shared fixtures ------------------------------------------------------------------------
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
		// BlurHash intentionally left NULL: order-item fetch COALESCEs m.blur_hash to '' (fetch.go),
		// so a null-blurhash media item (legacy/edge data) fetches without a scan error. This is the
		// regression guard for that fix — without the COALESCE this test fails on the NULL scan.
	})
	require.NoError(t, err)
	prices := make([]entity.ColorwayPriceInsert, 0)
	for _, c := range currency.RequiredCurrencies() {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(10000)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(10000)})
	}
	// mkProduct creates a fresh product with one live sized variant (sizeA) and stock, mirroring the
	// order_resnapshot suite — enough for a payable/refundable order line.
	mkProduct := func() int {
		p := &entity.ColorwayNew{
			Product: &entity.ColorwayInsert{
				ProductBodyInsert: entity.ColorwayBodyInsert{
					Brand: "ACME", Color: "black", ColorCode: "BLK", ColorHexOverride: sql.NullString{String: "#000000", Valid: true}, CountryOfOrigin: "IT",
					TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
				},
				ThumbnailMediaID: mediaID,
				Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "TACCT", Description: "d"}},
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
	skuOf := func(prodID int) string {
		var sku string
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product_size WHERE product_id = ? AND size_id = ?`, prodID, sizeA).Scan(&sku))
		return sku
	}
	cleanupOrder := func(oid int64) {
		for _, q := range []string{
			"DELETE FROM refunded_order_item WHERE order_id = ?",
			"DELETE FROM order_status_history WHERE order_id = ?",
			"DELETE FROM shipment WHERE order_id = ?",
			"DELETE FROM buyer WHERE order_id = ?",
			"DELETE FROM payment WHERE order_id = ?",
			"DELETE FROM order_item WHERE order_id = ?",
			"DELETE FROM customer_order WHERE id = ?",
		} {
			_, _ = testDB.ExecContext(context.Background(), q, oid)
		}
	}
	countEvents := func(eventType, sourceKey string) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM acct_event WHERE event_type = ? AND source_key = ?`, eventType, sourceKey).Scan(&n))
		return n
	}
	readRefundPayload := func(sourceKey string) entity.AcctOrderRefundPayload {
		var raw []byte
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT payload FROM acct_event WHERE event_type = 'order_refund' AND source_key = ?`, sourceKey).Scan(&raw))
		var p entity.AcctOrderRefundPayload
		require.NoError(t, json.Unmarshal(raw, &p))
		return p
	}

	// (a) order_paid via OrderPaymentDone: one event, and neither a retried payment nor a direct
	// duplicate enqueue adds a second row.
	t.Run("order_paid_via_payment_done", func(t *testing.T) {
		prodID := mkProduct()
		defer func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", prodID) }()
		checkoutSKU := skuOf(prodID)

		const uuid = "ACCT-PROD-PAID-0001"
		res, err := testDB.ExecContext(ctx,
			`INSERT INTO customer_order (uuid, order_status_id, currency, total_price) VALUES (?, ?, 'EUR', 100)`,
			uuid, awaitingID)
		require.NoError(t, err)
		oid, err := res.LastInsertId()
		require.NoError(t, err)
		defer cleanupOrder(oid)
		_, err = testDB.ExecContext(ctx,
			`INSERT INTO order_item (order_id, product_id, variant_id, product_price, product_price_base, product_sale_percentage, quantity, size_id, variant_sku_snapshot)
			 VALUES (?, ?, (SELECT id FROM product_size WHERE product_id = ? AND size_id = ?), 100, 100, 0, 1, ?, ?)`,
			oid, prodID, prodID, sizeA, sizeA, checkoutSKU)
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx,
			`INSERT INTO payment (order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done)
			 VALUES (?, ?, 100, 100, 0)`, oid, pmID)
		require.NoError(t, err)

		pay := func() (bool, error) {
			return s.Order().OrderPaymentDone(ctx, uuid, &entity.Payment{
				PaymentInsert: entity.PaymentInsert{
					PaymentMethodID:                  pmID,
					TransactionAmount:                decimal.NewFromInt(100),
					TransactionAmountPaymentCurrency: decimal.NewFromInt(100),
				},
			})
		}

		updated, err := pay()
		require.NoError(t, err)
		require.True(t, updated)
		require.Equal(t, 1, countEvents("order_paid", uuid), "first payment enqueues exactly one order_paid event")

		// The order is now Confirmed; OrderPaymentDone is a no-op and must not enqueue again.
		updated2, err := pay()
		require.NoError(t, err)
		require.False(t, updated2)
		require.Equal(t, 1, countEvents("order_paid", uuid), "a retried (no-op) payment adds no duplicate event")

		// A direct re-enqueue of the same (event_type, source_key) is an ON DUPLICATE KEY no-op.
		require.NoError(t, s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			return rep.Accounting().EnqueueEvent(ctx, entity.AcctEventInsert{
				EventType:  entity.AcctEventOrderPaid,
				SourceKey:  uuid,
				Payload:    entity.AcctOrderPaidPayload{OrderUUID: uuid},
				OccurredAt: s.Now(),
			})
		}))
		require.Equal(t, 1, countEvents("order_paid", uuid), "duplicate enqueue is a no-op")
	})

	// (b) order_paid via CreateCustomOrder (cash): the order is born Confirmed and never touches
	// OrderPaymentDone, so its event must come from the CreateCustomOrder producer.
	t.Run("order_paid_via_custom_order", func(t *testing.T) {
		prodID := mkProduct()
		defer func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", prodID) }()

		addr := func() *entity.AddressInsert {
			return &entity.AddressInsert{Country: "", City: "X", AddressLineOne: "L1", PostalCode: "00-000"}
		}
		on := &entity.OrderNew{
			Items: []entity.OrderItemInsert{{
				ProductId: prodID, SizeId: sizeA, Quantity: decimal.NewFromInt(1),
				ProductPrice: decimal.NewFromInt(100), ProductSalePercentage: decimal.Zero, ProductPriceWithSale: decimal.NewFromInt(100),
			}},
			ShippingAddress:   addr(),
			BillingAddress:    addr(),
			Buyer:             &entity.BuyerInsert{FirstName: "A", LastName: "B", Email: "acct-custom@b.cc", Phone: "12345678"},
			PaymentMethod:     entity.CASH,
			ShipmentCarrierId: carrierID,
			Currency:          "EUR",
		}
		ord, err := s.Order().CreateCustomOrder(ctx, on)
		require.NoError(t, err)
		require.NotEmpty(t, ord.UUID)
		defer cleanupOrder(int64(ord.Id))
		defer func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_event WHERE source_key = ?", ord.UUID) }()

		require.Equal(t, 1, countEvents("order_paid", ord.UUID), "a custom cash order enqueues one order_paid event")
	})

	// (c) order_refund via RefundOrder: two partial refunds on a delivered order produce events
	// uuid:1 then uuid:2, each carrying THIS refund's exact amount + per-item quantities (proving the
	// amount is the specific refund, not the running customer_order.refunded_amount aggregate).
	t.Run("order_refund_sequence", func(t *testing.T) {
		prodA := mkProduct()
		defer func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", prodA) }()
		prodB := mkProduct()
		defer func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", prodB) }()

		const uuid = "ACCT-PROD-REFUND-0001"
		res, err := testDB.ExecContext(ctx,
			`INSERT INTO customer_order (uuid, order_status_id, currency, total_price) VALUES (?, ?, 'EUR', 150)`,
			uuid, deliveredID)
		require.NoError(t, err)
		oid, err := res.LastInsertId()
		require.NoError(t, err)
		defer cleanupOrder(oid)

		insItem := func(prodID, price int) int {
			_, err := testDB.ExecContext(ctx,
				`INSERT INTO order_item (order_id, product_id, variant_id, product_price, product_price_base, product_sale_percentage, quantity, size_id, variant_sku_snapshot)
				 VALUES (?, ?, (SELECT id FROM product_size WHERE product_id = ? AND size_id = ?), ?, ?, 0, 1, ?, ?)`,
				oid, prodID, prodID, sizeA, price, price, sizeA, skuOf(prodID))
			require.NoError(t, err)
			var iid int
			require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM order_item WHERE order_id = ? AND product_id = ?`, oid, prodID).Scan(&iid))
			return iid
		}
		itemA := insItem(prodA, 100)
		itemB := insItem(prodB, 50)

		// First refund: item B (50) -> delivered becomes partially_refunded, event uuid:1.
		require.NoError(t, s.Order().RefundOrder(ctx, uuid, []int32{int32(itemB)}, "reason-1", "code-1", false))
		require.Equal(t, 1, countEvents("order_refund", uuid+":1"), "first refund enqueues event uuid:1")
		p1 := readRefundPayload(uuid + ":1")
		require.Equal(t, uuid, p1.OrderUUID)
		require.Equal(t, "EUR", p1.OrderCurrency)
		require.True(t, p1.RefundAmount.Equal(decimal.NewFromInt(50)), "refund uuid:1 amount = 50, got %s", p1.RefundAmount)
		require.Equal(t, int64(1), p1.RefundedByItem[itemB], "refund uuid:1 restores 1 unit of item B")
		require.NotContains(t, p1.RefundedByItem, itemA, "refund uuid:1 must not mention item A")

		// Second refund: item A (100) -> covers the remaining order, becomes refunded, event uuid:2.
		require.NoError(t, s.Order().RefundOrder(ctx, uuid, []int32{int32(itemA)}, "reason-2", "code-2", false))
		require.Equal(t, 1, countEvents("order_refund", uuid+":2"), "second refund enqueues event uuid:2")
		p2 := readRefundPayload(uuid + ":2")
		require.True(t, p2.RefundAmount.Equal(decimal.NewFromInt(100)), "refund uuid:2 amount = 100, got %s", p2.RefundAmount)
		require.Equal(t, int64(1), p2.RefundedByItem[itemA], "refund uuid:2 restores 1 unit of item A")

		// Exactly two refund events exist for this order.
		var total int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM acct_event WHERE event_type = 'order_refund' AND source_key LIKE ?`, uuid+":%").Scan(&total))
		require.Equal(t, 2, total, "exactly two order_refund events for the order")
	})

	// (d) The enqueue is part of the caller's transaction. An artificial failure AFTER a successful
	// EnqueueEvent must roll the row back — proving the producers can safely reject a payment/refund
	// on a downstream error without leaking a phantom accounting event.
	t.Run("event_rolls_back_with_tx", func(t *testing.T) {
		const uuid = "ACCT-PROD-ROLLBACK-0001"
		sentinel := errors.New("forced failure after enqueue")
		err := s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			if e := rep.Accounting().EnqueueEvent(ctx, entity.AcctEventInsert{
				EventType:  entity.AcctEventOrderPaid,
				SourceKey:  uuid,
				Payload:    entity.AcctOrderPaidPayload{OrderUUID: uuid},
				OccurredAt: s.Now(),
			}); e != nil {
				return e
			}
			return sentinel // fail the tx after the insert
		})
		require.ErrorIs(t, err, sentinel)
		require.Equal(t, 0, countEvents("order_paid", uuid), "the enqueue rolled back with the tx: no event persisted")
	})
}
