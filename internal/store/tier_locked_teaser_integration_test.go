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
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// bySKU indexes a colourway list by base SKU for set assertions.
func bySKU(cws []entity.Colorway) map[string]entity.Colorway {
	m := make(map[string]entity.Colorway, len(cws))
	for i := range cws {
		m[cws[i].SKU] = cws[i]
	}
	return m
}

// TestTierLockedTeaserCatalog is the container acceptance test for the storefront tier-locked teaser
// read behaviour (VERIFY b/c/d-display): tier-gated products are returned INLINE as locked teasers to
// everyone, hidden_for_non_qualified rows are excluded from non-qualifying viewers, the `exclusive`
// flag narrows to gated items only, and the DTO `locked`/`required_tier` projection is per-viewer.
func TestTierLockedTeaserCatalog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	exec := func(q string, args ...any) int64 {
		res, err := testDB.ExecContext(ctx, q, args...)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}

	token := fmt.Sprintf("%d%04d", time.Now().UnixNano(), rand.Intn(10000))
	tag := "TIERTEST-" + token

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
		BlurHash: sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
	})
	require.NoError(t, err)

	type seed struct {
		sku    string
		minT   int16
		hidden bool
	}
	// P0 normal, P1 gated teaser, P99 hacker teaser, PH gated + hidden_for_non_qualified.
	P0 := seed{"SKU0-" + token, 0, false}
	P1 := seed{"SKU1-" + token, 1, false}
	P99 := seed{"SKU99-" + token, 99, false}
	PH := seed{"SKUH-" + token, 1, true}
	seeds := []seed{P0, P1, P99, PH}

	var pids, styleIDs []int64
	for _, sd := range seeds {
		// A distinct style per product: (style_id, color_code) is UNIQUE, so all four can share color BLK.
		styleID := exec(`INSERT INTO tech_card (style_number, name, brand, collection, season_code, season_year, season, target_gender, top_category_id)
			VALUES (CONCAT('TIER-', UUID_SHORT()), 'TIER', 'ACME', '', 'SS', 2026, 'SS26', 'unisex', 1)`)
		styleIDs = append(styleIDs, styleID)
		pid := exec(`INSERT INTO product (sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status, min_tier, hidden_for_non_qualified)
			VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?, 2, ?, ?)`,
			sd.sku, mediaID, styleID, sd.minT, sd.hidden)
		exec(`INSERT INTO product_tag (product_id, tag) VALUES (?, ?)`, pid, tag)
		pids = append(pids, pid)
	}

	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_tag WHERE tag = ?", tag)
		for _, pid := range pids {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM product WHERE id = ?", pid)
		}
		for _, styleID := range styleIDs {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", styleID)
		}
		_, _ = testDB.ExecContext(cctx, "DELETE FROM media WHERE id = ?", mediaID)
	})

	get := func(viewerTier int16, exclusive bool) map[string]entity.Colorway {
		fc := &entity.FilterConditions{ByTag: tag, ViewerTier: viewerTier, Exclusive: exclusive}
		cws, _, err := s.Products().GetProductsPaged(ctx, 100, 0, nil, "", fc, nil, false)
		require.NoError(t, err)
		return bySKU(cws)
	}

	// (b) main catalogue for a guest (viewerTier=0): gated items returned INLINE, hidden EXCLUDED.
	guest := get(0, false)
	require.Contains(t, guest, P0.sku, "tier-0 product shown to guest")
	require.Contains(t, guest, P1.sku, "min_tier=1 returned INLINE to guest (locked teaser)")
	require.Contains(t, guest, P99.sku, "min_tier=99 returned INLINE to guest (locked teaser)")
	require.NotContains(t, guest, PH.sku, "hidden_for_non_qualified MUST NOT leak to a guest")

	// qualifying viewer (plus=1) additionally sees the hidden gated item.
	plus := get(1, false)
	require.Contains(t, plus, PH.sku, "hidden_for_non_qualified visible to a qualifying (plus) viewer")
	require.Len(t, plus, 4)

	// hacker (99) qualifies for everything, including the hidden min_tier=1 row.
	hacker := get(99, false)
	require.Len(t, hacker, 4, "hacker sees all four")

	// (c) exclusive catalogue: only tier-gated items (min_tier>0); hidden still excluded for guest.
	guestEx := get(0, true)
	require.NotContains(t, guestEx, P0.sku, "exclusive excludes tier-0")
	require.Contains(t, guestEx, P1.sku)
	require.Contains(t, guestEx, P99.sku)
	require.NotContains(t, guestEx, PH.sku, "exclusive still hides hidden_for_non_qualified from guest")
	require.Len(t, guestEx, 2)

	plusEx := get(1, true)
	require.Contains(t, plusEx, PH.sku, "exclusive surfaces the hidden gated item to a qualifying viewer")
	require.Len(t, plusEx, 3)

	// (d-display) per-viewer locked / required_tier via the DTO projection.
	lockedRT := func(m map[string]entity.Colorway, sku string, vt int16) (bool, int32) {
		cw := m[sku]
		pb := dto.StorefrontColorwayFromColorway(&cw, vt)
		return pb.Locked, pb.RequiredTier
	}
	assertLR := func(name string, gotL bool, gotRT int32, wantL bool, wantRT int32) {
		if gotL != wantL || gotRT != wantRT {
			t.Fatalf("%s: locked=%v required_tier=%d, want locked=%v required_tier=%d", name, gotL, gotRT, wantL, wantRT)
		}
	}
	l, rt := lockedRT(guest, P0.sku, 0)
	assertLR("P0/guest", l, rt, false, 0)
	l, rt = lockedRT(guest, P1.sku, 0)
	assertLR("P1/guest", l, rt, true, 1)
	l, rt = lockedRT(guest, P99.sku, 0)
	assertLR("P99/guest", l, rt, true, 99)
	l, rt = lockedRT(plus, P1.sku, 1)
	assertLR("P1/plus", l, rt, false, 1)
	l, rt = lockedRT(plus, P99.sku, 1)
	assertLR("P99/plus", l, rt, true, 99)
	l, rt = lockedRT(plus, PH.sku, 1)
	assertLR("PH/plus", l, rt, false, 1)
	l, rt = lockedRT(hacker, P99.sku, 99)
	assertLR("P99/hacker", l, rt, false, 99)
}

// TestTierLockedTeaserPurchaseBlock is the container acceptance test for the server-authoritative
// purchase block (VERIFY a/d-order): CreateOrder rejects any line whose product min_tier the buyer
// does not satisfy (entity.TierCanPurchase, hacker=99 rule) with a field-tagged "tier_locked"
// violation, while allowing qualifying and tier-0 lines. buyerTier is server-set (OrderNew.BuyerTier);
// the block is independent of what the storefront displayed.
func TestTierLockedTeaserPurchaseBlock(t *testing.T) {
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

	// A fully-controlled carrier (allowed, no region restriction, EUR price) MUST exist before
	// NewForTest so the dictionary cache loads it — CreateOrder resolves shipping from the cache.
	carrierID := exec(`INSERT INTO shipment_carrier (carrier, tracking_url, allowed, description)
		VALUES (CONCAT('TIERTEST-', ?), 'http://x/%s', 1, 'tiertest')`, token)
	exec(`INSERT INTO shipment_carrier_price (shipment_carrier_id, currency, price) VALUES (?, 'EUR', 5.00)`, carrierID)

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// NewForTest does not populate the global dictionary cache the way store.New does. Load it exactly
	// as New does so order statuses (order_status FK) and shipment carriers — including the carrier +
	// EUR price seeded above — resolve from the DB into the cache CreateOrder consults.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	// Payment-method allowance is config/cache-driven; force our method allowed AFTER InitConsts so the
	// order path can reach and exercise the tier block.
	cache.UpdatePaymentMethodAllowance(entity.BANK_INVOICE, true)

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
		BlurHash: sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
	})
	require.NoError(t, err)

	// One shared size row; each product links it via its own product_size (unique (product_id, size_id)).
	sizeID := exec(`INSERT INTO size (name, sku_ord, sku_system) VALUES (CONCAT('PB-', LEFT(MD5(RAND()),6)), 42, 'apparel')`)

	// variantSKU[minTier] -> the buyable variant sku for a product requiring that tier.
	variantSKU := map[int16]string{}
	var pids, styleIDs []int64
	for _, minT := range []int16{0, 1, 99} {
		styleID := exec(`INSERT INTO tech_card (style_number, name, brand, collection, season_code, season_year, season, target_gender, top_category_id)
			VALUES (CONCAT('PB-', UUID_SHORT()), 'PB', 'ACME', '', 'SS', 2026, 'SS26', 'unisex', 1)`)
		styleIDs = append(styleIDs, styleID)
		sku := fmt.Sprintf("PB%d-%s", minT, token)
		pid := exec(`INSERT INTO product (sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status, min_tier, hidden_for_non_qualified)
			VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?, 2, ?, 0)`, sku, mediaID, styleID, minT)
		pids = append(pids, pid)
		exec(`INSERT INTO product_price (product_id, currency, price) VALUES (?, 'EUR', 100.00)`, pid)
		vsku := "V" + sku
		exec(`INSERT INTO product_size (product_id, size_id, quantity, sku) VALUES (?, ?, 10, ?)`, pid, sizeID, vsku)
		variantSKU[minT] = vsku
	}

	t.Cleanup(func() {
		cctx := context.Background()
		// Best-effort: orders created below FK-reference the products; those deletes no-op and the
		// ephemeral container is fully dropped by TestMain's cleanup regardless.
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

	placeOrder := func(minTier int16, buyerTier int16) (*entity.Order, error) {
		on := &entity.OrderNew{
			Items:             []entity.OrderItemInsert{{VariantSKU: variantSKU[minTier], Quantity: decimal.NewFromInt(1)}},
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

	// (a) REJECT: a guest (tier 0) may not buy a min_tier=1 product.
	_, err = placeOrder(1, 0)
	requireTierLocked(err, "guest buying min_tier=1")

	// ALLOW a qualifying buyer: plus (tier 1) buys the same min_tier=1 product — order is created.
	o, err := placeOrder(1, 1)
	require.NoError(t, err, "plus buying min_tier=1 must be allowed")
	require.NotNil(t, o)
	require.NotZero(t, o.Id)

	// ALLOW normal (tier-0) items: guest buys a min_tier=0 product.
	o0, err := placeOrder(0, 0)
	require.NoError(t, err, "guest buying tier-0 must be allowed")
	require.NotNil(t, o0)

	// Hacker rule: plus_plus (tier 2) may NOT buy a min_tier=99 (invite-only) product.
	_, err = placeOrder(99, 2)
	requireTierLocked(err, "plus_plus buying min_tier=99")

	// Hacker rule: a hacker (tier 99) MAY buy a min_tier=99 product.
	oh, err := placeOrder(99, 99)
	require.NoError(t, err, "hacker buying min_tier=99 must be allowed")
	require.NotNil(t, oh)
}
