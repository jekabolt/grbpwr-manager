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

// TestTierHiddenPurchaseBlock is the container acceptance test for the headline case the tier feature
// closes: the server-authoritative purchase block is enforced on the ORDER path, not driven by what
// the storefront displayed. So a HIDDEN (hidden_for_non_qualified=TRUE) tier-gated product — invisible
// in the catalogue — still cannot be bought by a non-qualifying buyer even when its buyable variant SKU
// is submitted DIRECTLY, and a MIXED cart (one gated + one normal line) is rejected as a WHOLE, creating
// nothing. A qualifying buyer succeeds. Complements TestTierLockedTeaserPurchaseBlock (non-hidden,
// single-line carts) with the hidden-item + direct-SKU + mixed-cart angles.
func TestTierHiddenPurchaseBlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	exec := func(q string, args ...any) int64 {
		res, err := testDB.ExecContext(ctx, q, args...)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}

	token := fmt.Sprintf("%d%04d", time.Now().UnixNano(), rand.Intn(10000))

	// A fully-controlled carrier (allowed, no region restriction, EUR price) MUST exist before NewForTest
	// so the dictionary cache loads it — CreateOrder resolves shipping from the cache.
	carrierID := exec(`INSERT INTO shipment_carrier (carrier, tracking_url, allowed, description)
		VALUES (CONCAT('HIDBLK-', ?), 'http://x/%s', 1, 'hidblk')`, token)
	exec(`INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price) VALUES (?, 'EUR', 5.00)`, carrierID)

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// Load the dictionary cache exactly as store.New does so order statuses and the seeded carrier resolve.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))
	cache.UpdatePaymentMethodAllowance(entity.BANK_INVOICE, true)

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
		BlurHash: sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
	})
	require.NoError(t, err)

	sizeID := exec(`INSERT INTO size (name, sku_ord, sku_system) VALUES (CONCAT('HB-', LEFT(MD5(RAND()),6)), 42, 'apparel')`)

	var pids, styleIDs []int64
	// seed inserts an ACTIVE product with the given min_tier + hidden flag and one buyable variant,
	// returning its variant SKU.
	seed := func(label string, minTier int16, hidden bool) string {
		styleID := exec(`INSERT INTO tech_card (style_number, name, brand, collection, season_code, season_year, season, target_gender, top_category_id)
			VALUES (CONCAT('HB-', UUID_SHORT()), 'HB', 'ACME', '', 'SS', 2026, 'SS26', 'unisex', 1)`)
		styleIDs = append(styleIDs, styleID)
		sku := fmt.Sprintf("%s-%s", label, token)
		pid := exec(`INSERT INTO product (sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status, min_tier, hidden_for_non_qualified)
			VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?, 2, ?, ?)`, sku, mediaID, styleID, minTier, hidden)
		pids = append(pids, pid)
		exec(`INSERT INTO product_price (product_id, currency, price) VALUES (?, 'EUR', 100.00)`, pid)
		vsku := "V" + sku
		exec(`INSERT INTO product_size (product_id, size_id, quantity, sku) VALUES (?, ?, 10, ?)`, pid, sizeID, vsku)
		return vsku
	}

	// PH: hidden_for_non_qualified=TRUE, min_tier=1 (the invisible, gated item). P0: normal, min_tier=0.
	vHidden := seed("HBH", 1, true)
	vNormal := seed("HB0", 0, false)

	t.Cleanup(func() {
		cctx := context.Background()
		// Best-effort: orders created below FK-reference the products, so those deletes no-op; the
		// ephemeral container is dropped by TestMain regardless.
		for _, pid := range pids {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM product_size WHERE product_id = ?", pid)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM product_price WHERE product_id = ?", pid)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM product WHERE id = ?", pid)
		}
		for _, styleID := range styleIDs {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", styleID)
		}
		_, _ = testDB.ExecContext(cctx, "DELETE FROM size WHERE id = ?", sizeID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM shipment_carrier_price WHERE shipment_carrier_id = ?", carrierID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM shipment_carrier WHERE id = ?", carrierID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM media WHERE id = ?", mediaID)
	})

	placeOrder := func(buyerTier int16, variantSKUs ...string) (*entity.Order, error) {
		items := make([]entity.OrderItemInsert, 0, len(variantSKUs))
		for _, v := range variantSKUs {
			items = append(items, entity.OrderItemInsert{VariantSKU: v, Quantity: decimal.NewFromInt(1)})
		}
		on := &entity.OrderNew{
			Items:             items,
			ShippingAddress:   &entity.AddressInsert{Country: "US", City: "NYC", AddressLineOne: "1 St", PostalCode: "10001"},
			BillingAddress:    &entity.AddressInsert{Country: "US", City: "NYC", AddressLineOne: "1 St", PostalCode: "10001"},
			Buyer:             &entity.BuyerInsert{FirstName: "T", LastName: "T", Email: fmt.Sprintf("buyer-%s@example.com", token), Phone: "1234567890"},
			PaymentMethod:     entity.BANK_INVOICE,
			ShipmentCarrierId: int(carrierID),
			Currency:          "EUR",
			BuyerTier:         buyerTier,
		}
		o, _, err := s.Order().CreateOrder(ctx, on, false, time.Now().UTC().Add(time.Hour))
		return o, err
	}

	requireTierLocked := func(err error, msg string) {
		require.Error(t, err, msg)
		var ve *entity.ValidationError
		require.True(t, errors.As(err, &ve), "%s: expected *entity.ValidationError, got %T: %v", msg, err, err)
		require.Equal(t, "tier_locked", ve.Reason, "%s: wrong violation reason", msg)
		require.Equal(t, "items", ve.Field, "%s: field should tag the items", msg)
	}

	// (1) HIDDEN item, DIRECT variant SKU, non-qualifying buyer (guest): rejected. The item is invisible
	// in the catalogue, but obtaining its variant SKU and submitting it directly must still be blocked.
	o, err := placeOrder(entity.TierCodeMember, vHidden)
	requireTierLocked(err, "guest buying a hidden min_tier=1 item by direct variant SKU")
	require.Nil(t, o, "no order may be created for a blocked purchase")

	// (2) MIXED cart (hidden gated line + normal line) as a guest: rejected as a WHOLE — nothing created.
	o, err = placeOrder(entity.TierCodeMember, vHidden, vNormal)
	requireTierLocked(err, "guest mixed cart (gated + normal) must be rejected as a whole")
	require.Nil(t, o, "a mixed cart with one gated line must create no order")

	// (3) Qualifying buyer (plus=1) buys the hidden min_tier=1 item: allowed — the block is min_tier based.
	o, err = placeOrder(entity.TierCodePlus, vHidden)
	require.NoError(t, err, "plus buying the hidden min_tier=1 item must be allowed")
	require.NotNil(t, o)
	require.NotZero(t, o.Id)

	// (3b) Qualifying buyer's MIXED cart (gated + normal) succeeds — both lines are eligible for plus.
	o, err = placeOrder(entity.TierCodePlus, vHidden, vNormal)
	require.NoError(t, err, "plus buying a mixed (gated + normal) cart must be allowed")
	require.NotNil(t, o)
	require.NotZero(t, o.Id)
}
