package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestAnalyticsV2Task08CountryEconomics exercises per-country profitability. Two DE orders carry a cost
// snapshot (revenue 100, cost 40 each) so DE margin is real; a US order has no cost snapshot so US shows
// cost_coverage 0. One DE order also carries a €10 carrier cost and a €5 payment fee, so DE contribution
// and profit-per-order are exactly assertable:
//
//	DE: revenue 200, COGS 80, gross 120 (60%), coverage 100%, ship 10, fees 5,
//	    contribution 120−10−5 = 105, profit/order 105/2 = 52.5, LTV avg 100 (2 customers)
//	US: revenue 100, coverage 0%, gross 0, LTV avg 100 (1 customer)
//
// Σ economics revenue = 300 ties out with the by-country breakdown. Throwaway; cleans its own rows.
func TestAnalyticsV2Task08CountryEconomics(t *testing.T) {
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

	clean := func() {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE uuid LIKE 'T08-%'")
		_, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE sku = 'T08-P'")
		_, _ = testDB.ExecContext(ctx, "DELETE FROM shipment_carrier WHERE carrier = 'T08-carrier'")
	}
	clean()
	t.Cleanup(clean)

	var confirmedID, sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)

	// One product with NO product.cost_price — the cost distinction lives on the order_item snapshot.
	pr, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, brand, color, color_hex, country_of_origin, thumbnail_id, top_category_id, target_gender, version)
		VALUES ('T08-P', 'b', 'c', '#000000', 'US', ?, 1, 'unisex', 'v1')`, mediaID)
	require.NoError(t, err)
	productID, err := pr.LastInsertId()
	require.NoError(t, err)

	cr, err := testDB.ExecContext(ctx, `INSERT INTO shipment_carrier (carrier, price, tracking_url, allowed)
		VALUES ('T08-carrier', 0, 'http://x', 1)`)
	require.NoError(t, err)
	carrierID, err := cr.LastInsertId()
	require.NoError(t, err)

	addr := func(country string) int64 {
		r, err := testDB.ExecContext(ctx,
			`INSERT INTO address (country, city, address_line_one, postal_code) VALUES (?, 'x', '1 st', '00000')`, country)
		require.NoError(t, err)
		id, err := r.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM address WHERE id = ?", id) })
		return id
	}
	deAddr, usAddr := addr("DE"), addr("US")

	placed := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	mkOrder := func(uuid, email string, addrID int64) int64 {
		r, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
			(uuid, order_status_id, currency, total_price, total_settled_base, placed)
			VALUES (?, ?, 'EUR', 100, 100, ?)`, uuid, confirmedID, placed)
		require.NoError(t, err)
		oid, err := r.LastInsertId()
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx, `INSERT INTO buyer
			(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
			VALUES (?, 'a', 'b', ?, '1234567', ?, ?)`, oid, email, addrID, addrID)
		require.NoError(t, err)
		return oid
	}
	// costPrice < 0 means "no cost snapshot" (uncosted line).
	mkItem := func(orderID int64, costPrice int) {
		var cost any
		if costPrice >= 0 {
			cost = costPrice
		}
		_, err := testDB.ExecContext(ctx, `INSERT INTO order_item
			(order_id, product_id, product_price, product_price_base, cost_price_at_sale, product_sale_percentage, quantity, size_id)
			VALUES (?, ?, 100, 100, ?, 0, 1, ?)`, orderID, productID, cost, sizeID)
		require.NoError(t, err)
	}

	de1 := mkOrder("T08-DE1", "t08-alice@example.com", deAddr)
	mkItem(de1, 40)
	de2 := mkOrder("T08-DE2", "t08-bob@example.com", deAddr)
	mkItem(de2, 40)
	us1 := mkOrder("T08-US1", "t08-carol@example.com", usAddr)
	mkItem(us1, -1) // uncosted

	// DE1 gets a €10 carrier cost and a €5 payment fee.
	_, err = testDB.ExecContext(ctx, `INSERT INTO shipment (order_id, cost, carrier_id, actual_cost)
		VALUES (?, 0, ?, 10)`, de1, carrierID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `UPDATE customer_order SET payment_fee = 5 WHERE id = ?`, de1)
	require.NoError(t, err)

	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rows, err := s.Metrics().GetCountryEconomics(ctx, from, to)
	require.NoError(t, err)

	byCountry := map[string]entity.CountryEconomicsRow{}
	var totalRevenue decimal.Decimal
	for _, r := range rows {
		byCountry[r.Country] = r
		totalRevenue = totalRevenue.Add(r.Revenue)
	}

	eq := func(got decimal.Decimal, want string, msg string) {
		w, _ := decimal.NewFromString(want)
		require.Truef(t, got.Equal(w), "%s: got %s want %s", msg, got.String(), want)
	}

	de, ok := byCountry["DE"]
	require.True(t, ok, "DE row present")
	eq(de.Revenue, "200", "DE revenue")
	require.Equal(t, 2, de.Orders, "DE orders")
	eq(de.RevenueCost, "80", "DE COGS")
	eq(de.GrossMargin, "120", "DE gross margin")
	require.Equal(t, 60.0, de.GrossMarginPct, "DE gross margin %")
	require.Equal(t, 100.0, de.CostCoveragePct, "DE cost coverage %")
	eq(de.ShippingCost, "10", "DE shipping")
	eq(de.PaymentFees, "5", "DE payment fees")
	eq(de.ContributionMargin, "105", "DE contribution = 120 − 10 − 5")
	eq(de.ProfitPerOrder, "52.5", "DE profit per order = 105/2")
	eq(de.LtvAvg, "100", "DE avg LTV")
	require.Equal(t, 2, de.LtvSample, "DE LTV sample")

	us, ok := byCountry["US"]
	require.True(t, ok, "US row present")
	eq(us.Revenue, "100", "US revenue")
	require.Equal(t, 1, us.Orders, "US orders")
	require.Equal(t, 0.0, us.CostCoveragePct, "US cost coverage 0 (uncosted)")
	eq(us.GrossMargin, "0", "US gross margin 0 (no cost)")
	eq(us.LtvAvg, "100", "US avg LTV")
	require.Equal(t, 1, us.LtvSample, "US LTV sample")

	eq(totalRevenue, "300", "Σ economics revenue reconciles (DE 200 + US 100)")
}
