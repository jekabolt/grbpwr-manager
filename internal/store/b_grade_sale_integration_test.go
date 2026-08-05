package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestBGradeManualPriceSale is the container acceptance test for 0251 (owner decision 2026-08-04:
// seconds sell at a MANUAL per-variant price). It walks the whole chain on one product carrying an
// A variant (catalogue price 100, sale 50%, cost 30) and a B variant of the same size ('-B' SKU,
// seconds stock):
//
//	(1) pricing guardrails — SetVariantPrice refuses an A target, a base-currency-less set, and a
//	    non-positive price; ListVariantSeconds shows the B row with its saved prices;
//	(2) fail-closed — the '-B' SKU resolves but cannot be BOUGHT until a manual price exists;
//	(3) the sale — a mixed A+B cart prices the B line at the manual figure (NO sale percentage),
//	    snapshots grade='B', cost_price_at_sale=0 (zero-cost invariant) and product_price_base
//	    from product_size_price, while the A line keeps the catalogue price, the sale percentage
//	    and the product cost snapshot;
//	(4) stock — payment decrements the B row (not A); a restock refund puts the unit back on B;
//	(5) the storefront waitlist resolver still refuses '-B' SKUs (grade pin).
func TestBGradeManualPriceSale(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	exec := func(q string, args ...any) int64 {
		res, err := testDB.ExecContext(ctx, q, args...)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}
	count := func(q string, args ...any) int {
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx, q, args...).Scan(&n))
		return n
	}

	token := fmt.Sprintf("%d%04d", time.Now().UnixNano(), rand.Intn(10000))

	carrierID := exec(`INSERT INTO shipment_carrier (carrier, tracking_url, allowed, description)
		VALUES (CONCAT('BGR-', ?), 'http://x/%s', 1, 'bgrade')`, token)
	exec(`INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price) VALUES (?, 'EUR', 5.00)`, carrierID)

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

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
		BlurHash: sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
	})
	require.NoError(t, err)

	sizeID := exec(`INSERT INTO size (name, sku_ord, sku_system) VALUES (CONCAT('BG-', LEFT(MD5(RAND()),6)), 43, 'apparel')`)

	styleID := exec(`INSERT INTO tech_card (style_number, name, brand, collection, season_code, season_year, season, target_gender, top_category_id)
		VALUES (CONCAT('BG-', UUID_SHORT()), 'BG', 'ACME', '', 'SS', 2026, 'SS26', 'unisex', 1)`)
	baseSKU := "BGRD-" + token
	pid := exec(`INSERT INTO product (sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status, sale_percentage, cost_price)
		VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?, 2, 50.00, 30.00)`, baseSKU, mediaID, styleID)
	exec(`INSERT INTO product_price (product_id, currency, price) VALUES (?, 'EUR', 100.00)`, pid)
	aSKU := "V" + baseSKU
	bSKU := aSKU + "-B"
	aVarID := exec(`INSERT INTO product_size (product_id, size_id, quantity, sku) VALUES (?, ?, 10, ?)`, pid, sizeID, aSKU)
	bVarID := exec(`INSERT INTO product_size (product_id, size_id, quantity, sku, grade) VALUES (?, ?, 3, ?, 'B')`, pid, sizeID, bSKU)

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_size_price WHERE product_size_id IN (?, ?)", aVarID, bVarID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_size WHERE product_id = ?", pid)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_price WHERE product_id = ?", pid)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product WHERE id = ?", pid)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", styleID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM size WHERE id = ?", sizeID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM shipment_carrier_price WHERE shipment_carrier_id = ?", carrierID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM shipment_carrier WHERE id = ?", carrierID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM media WHERE id = ?", mediaID)
	})

	price := func(cur string, v float64) entity.ColorwayPriceInsert {
		return entity.ColorwayPriceInsert{Currency: cur, Price: decimal.NewFromFloat(v)}
	}

	// --- (1) pricing guardrails ---
	err = s.Products().SetVariantPrice(ctx, int(aVarID), []entity.ColorwayPriceInsert{price("EUR", 40)})
	require.ErrorIs(t, err, entity.ErrVariantPriceNotSeconds, "pricing an A variant must be refused")

	err = s.Products().SetVariantPrice(ctx, int(bVarID), []entity.ColorwayPriceInsert{price("USD", 45)})
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "base-currency-less set must be a ValidationError, got %T: %v", err, err)

	err = s.Products().SetVariantPrice(ctx, int(bVarID), []entity.ColorwayPriceInsert{price("EUR", 0)})
	require.Error(t, err, "zero price must be rejected")

	// --- (2) fail-closed: the '-B' SKU resolves but is not buyable while unpriced ---
	placeOrder := func(skus ...string) (*entity.Order, error) {
		items := make([]entity.OrderItemInsert, 0, len(skus))
		for _, v := range skus {
			items = append(items, entity.OrderItemInsert{VariantSKU: v, Quantity: decimal.NewFromInt(1)})
		}
		on := &entity.OrderNew{
			Items:             items,
			ShippingAddress:   &entity.AddressInsert{Country: "US", City: "NYC", AddressLineOne: "1 St", PostalCode: "10001"},
			BillingAddress:    &entity.AddressInsert{Country: "US", City: "NYC", AddressLineOne: "1 St", PostalCode: "10001"},
			Buyer:             &entity.BuyerInsert{FirstName: "T", LastName: "T", Email: fmt.Sprintf("bgrade-%s@example.com", token), Phone: "1234567890"},
			PaymentMethod:     entity.CARD,
			ShipmentCarrierId: int(carrierID),
			Currency:          "EUR",
		}
		o, _, err := s.Order().CreateOrder(ctx, on, false, time.Now().UTC().Add(time.Hour))
		return o, err
	}

	o, err := placeOrder(bSKU)
	require.Error(t, err, "an unpriced B variant must not be sellable")
	require.True(t, errors.As(err, &ve), "unpriced B must fail as a ValidationError, got %T: %v", err, err)
	require.Contains(t, ve.Message, "manual price", "the refusal must be the fail-closed price gate, not an availability drop")
	require.True(t, o == nil || o.Id == 0, "no order may be created for an unpriced B line")

	// Price it: EUR (base) 40 + USD 45. The catalogue A price stays 100 with 50% sale.
	require.NoError(t, s.Products().SetVariantPrice(ctx, int(bVarID), []entity.ColorwayPriceInsert{price("EUR", 40), price("USD", 45)}))

	seconds, err := s.Products().ListVariantSeconds(ctx, int(pid))
	require.NoError(t, err)
	require.Len(t, seconds, 1)
	require.Equal(t, int(bVarID), seconds[0].Id)
	require.Equal(t, bSKU, seconds[0].SKU.String)
	require.Len(t, seconds[0].Prices, 2)

	// --- (3) the mixed A+B sale ---
	o, err = placeOrder(aSKU, bSKU)
	require.NoError(t, err, "a priced B variant must be buyable by direct '-B' SKU")
	require.NotNil(t, o)

	type line struct {
		Grade     string
		Price     string
		SalePct   string
		Cost      sql.NullString
		BasePrice sql.NullString
		VariantID int64
	}
	readLine := func(grade string) line {
		var l line
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT grade, product_price, COALESCE(product_sale_percentage, 0), cost_price_at_sale, product_price_base, variant_id
			 FROM order_item WHERE order_id = ? AND grade = ?`, o.Id, grade).
			Scan(&l.Grade, &l.Price, &l.SalePct, &l.Cost, &l.BasePrice, &l.VariantID))
		return l
	}
	requireMoney := func(got string, want float64, msg string) {
		g, err := decimal.NewFromString(got)
		require.NoError(t, err, msg)
		require.True(t, g.Equal(decimal.NewFromFloat(want)), "%s: got %s want %v", msg, got, want)
	}

	a := readLine("A")
	requireMoney(a.Price, 100, "A line keeps the catalogue price")
	requireMoney(a.SalePct, 50, "A line keeps the product sale percentage")
	require.True(t, a.Cost.Valid, "A line snapshots the product cost")
	requireMoney(a.Cost.String, 30, "A line cost snapshot")
	require.Equal(t, aVarID, a.VariantID)

	b := readLine("B")
	requireMoney(b.Price, 40, "B line sells at the manual price")
	requireMoney(b.SalePct, 0, "B line ignores the product sale percentage (the manual price IS the price)")
	require.True(t, b.Cost.Valid, "B line must carry an EXPLICIT zero cost, not NULL")
	requireMoney(b.Cost.String, 0, "B zero-cost invariant")
	require.True(t, b.BasePrice.Valid, "B line snapshots its own base price")
	requireMoney(b.BasePrice.String, 40, "B base price comes from product_size_price, never the A catalogue")
	require.Equal(t, bVarID, b.VariantID)

	// --- (4) stock: the invoice decrements B from its own row; a restock refund returns it there ---
	pme, ok := cache.GetPaymentMethodByName(entity.CARD)
	require.True(t, ok, "card payment method must be cached")
	_, err = s.Order().InsertFiatInvoice(ctx, o.UUID, "bgrade-secret-"+token, pme.Method, time.Now().UTC().Add(time.Hour))
	require.NoError(t, err, "invoicing (which reduces stock) must accept a B line")
	_, err = s.Order().OrderPaymentDone(ctx, o.UUID, &entity.Payment{
		PaymentInsert: entity.PaymentInsert{
			PaymentMethodID:                  pme.Method.Id,
			TransactionAmount:                decimal.NewFromInt(95),
			TransactionAmountPaymentCurrency: decimal.NewFromInt(95),
		},
	})
	require.NoError(t, err, "payment (incl. the SKU freeze) must accept a B line")

	require.Equal(t, 9, count(`SELECT quantity FROM product_size WHERE id = ?`, aVarID), "A stock decremented from its own row")
	require.Equal(t, 2, count(`SELECT quantity FROM product_size WHERE id = ?`, bVarID), "B stock decremented from the B row, not A")

	require.NoError(t, s.Order().RefundOrder(ctx, o.UUID, nil, "test", "OTHER", false, entity.RefundDispositionRestock))
	require.Equal(t, 10, count(`SELECT quantity FROM product_size WHERE id = ?`, aVarID), "A unit restocked to A")
	require.Equal(t, 3, count(`SELECT quantity FROM product_size WHERE id = ?`, bVarID), "B unit restocked to B, not A")

	// The journal separates the two streams by grade.
	require.Equal(t, 2, count(`SELECT COUNT(*) FROM product_stock_change_history WHERE order_id = ? AND grade = 'B'`, o.Id),
		"B line leaves grade='B' journal rows (paid + returned)")

	// --- (5) the storefront waitlist resolver still pins grade A ---
	_, err = s.Products().GetVariantBySKU(ctx, bSKU)
	require.ErrorIs(t, err, sql.ErrNoRows, "NotifyMe must not resolve a '-B' SKU")

	// Clearing the prices closes the shop window again (fail-closed).
	require.NoError(t, s.Products().SetVariantPrice(ctx, int(bVarID), nil))
	o2, err := placeOrder(bSKU)
	require.Error(t, err, "clearing prices must make the B variant unsellable again")
	require.True(t, o2 == nil || o2.Id == 0, "no order may be created after prices are cleared")
}
