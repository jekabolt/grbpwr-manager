package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestCreateCustomOrderRequiresPositivePrice is the store-level acceptance for problem 044: the custom
// order path enforces the same positive-price invariant (requirePositivePrice) as the standard path.
// A zero/negative line price is a *entity.ValidationError and creates no order/payment (rollback); a
// positive price passes the invariant (and only then fails downstream for a different reason). It uses
// non-existent product ids on purpose: the price check runs before product/stock resolution, so the
// invariant is exercised without seeding a product (AddProduct is unrelated-broken at this base by the
// 0146 season CHECK).
func TestCreateCustomOrderRequiresPositivePrice(t *testing.T) {
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

	// pick an allowed shipment carrier so the pre-tx carrier validation passes and we reach the items.
	var carrierID int
	for _, c := range cache.GetShipmentCarriers() {
		if c.Allowed {
			carrierID = c.Id
			break
		}
	}
	require.NotZero(t, carrierID, "need an allowed shipment carrier")

	buyer := &entity.BuyerInsert{FirstName: "A", LastName: "B", Email: "a@b.cc", Phone: "12345678"}
	addr := func() *entity.AddressInsert {
		return &entity.AddressInsert{Country: "", City: "X", AddressLineOne: "L1", PostalCode: "00-000"}
	}
	mkOrder := func(items []entity.OrderItemInsert) *entity.OrderNew {
		return &entity.OrderNew{
			Items:             items,
			ShippingAddress:   addr(),
			BillingAddress:    addr(),
			Buyer:             buyer,
			PaymentMethod:     entity.CASH,
			ShipmentCarrierId: carrierID,
			Currency:          "EUR",
		}
	}
	item := func(productID int, price string) entity.OrderItemInsert {
		p := decimal.RequireFromString(price)
		return entity.OrderItemInsert{
			ProductId: productID, SizeId: 1, Quantity: decimal.NewFromInt(1),
			ProductPrice: p, ProductSalePercentage: decimal.Zero, ProductPriceWithSale: p,
		}
	}

	count := func(table string) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n))
		return n
	}
	// use product ids that do not exist, so no created product pollutes other tests.
	const fakeA, fakeB = 90000001, 90000002

	assertNoMutation := func(name string, before map[string]int) {
		for _, tbl := range []string{"customer_order", "payment"} {
			require.Equal(t, before[tbl], count(tbl), "%s: %s rows must be unchanged (rollback)", name, tbl)
		}
	}
	snapshot := func() map[string]int {
		return map[string]int{"customer_order": count("customer_order"), "payment": count("payment")}
	}

	// --- zero price -> rejected with the positive-price ValidationError, nothing created ---
	before := snapshot()
	_, err = s.Order().CreateCustomOrder(ctx, mkOrder([]entity.OrderItemInsert{item(fakeA, "0")}))
	require.Error(t, err)
	var verr *entity.ValidationError
	require.True(t, errors.As(err, &verr), "zero price must be a ValidationError, got %T", err)
	require.Contains(t, strings.ToLower(verr.Message), "price must be positive", "zero -> price invariant message")
	assertNoMutation("zero", before)

	// --- negative price -> same invariant ---
	before = snapshot()
	_, err = s.Order().CreateCustomOrder(ctx, mkOrder([]entity.OrderItemInsert{item(fakeA, "-5")}))
	require.Error(t, err)
	require.True(t, errors.As(err, &verr))
	require.Contains(t, strings.ToLower(verr.Message), "price must be positive", "negative -> price invariant message")
	assertNoMutation("negative", before)

	// --- mixed items (one positive, one zero) -> whole order rejected on the zero line ---
	before = snapshot()
	_, err = s.Order().CreateCustomOrder(ctx, mkOrder([]entity.OrderItemInsert{item(fakeA, "10"), item(fakeB, "0")}))
	require.Error(t, err)
	require.True(t, errors.As(err, &verr))
	require.Contains(t, strings.ToLower(verr.Message), "price must be positive", "mixed -> price invariant message")
	assertNoMutation("mixed", before)

	// Duplicate variant lines are validated before merge: a bad or inconsistent second price cannot
	// disappear behind the first line's price.
	before = snapshot()
	_, err = s.Order().CreateCustomOrder(ctx, mkOrder([]entity.OrderItemInsert{item(fakeA, "10"), item(fakeA, "0")}))
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "price must be positive")
	assertNoMutation("duplicate-zero", before)

	before = snapshot()
	_, err = s.Order().CreateCustomOrder(ctx, mkOrder([]entity.OrderItemInsert{item(fakeA, "10"), item(fakeA, "11")}))
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "inconsistent custom prices")
	assertNoMutation("duplicate-inconsistent", before)

	// Positive before rounding is not enough: JPY has no minor units, so 0.01 becomes zero.
	before = snapshot()
	jpyOrder := mkOrder([]entity.OrderItemInsert{item(fakeA, "0.01")})
	jpyOrder.Currency = "JPY"
	_, err = s.Order().CreateCustomOrder(ctx, jpyOrder)
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "price must be positive")
	assertNoMutation("jpy-sub-minor", before)

	// --- positive price passes the invariant: the resulting error is NOT the price one (it is the
	// downstream out-of-stock failure for the non-existent product), proving the price gate let it through.
	before = snapshot()
	_, err = s.Order().CreateCustomOrder(ctx, mkOrder([]entity.OrderItemInsert{item(fakeA, "10")}))
	require.Error(t, err)
	require.True(t, errors.As(err, &verr))
	require.NotContains(t, strings.ToLower(verr.Message), "price must be positive",
		"a positive price must pass the invariant (error should be out-of-stock, not price)")
	assertNoMutation("positive", before)
}
