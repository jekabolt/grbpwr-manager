package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestUpdatePromoCodeInPlaceAdditive exercises GAP 2: the new UpdatePromoCode store path replaces a
// promo's mutable fields in place — including re-enabling a disabled code via allowed=true — without
// the wave-1 delete-then-recreate that dropped the row's identity. It also proves a missing code is
// reported as sql.ErrNoRows (NOT_FOUND upstream) and that the in-memory cache is refreshed.
func TestUpdatePromoCodeInPlaceAdditive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	const code = "HARDENADD10"
	now := time.Now().UTC()
	require.NoError(t, s.Promo().AddPromo(ctx, &entity.PromoCodeInsert{
		Code:       code,
		Discount:   decimal.NewFromInt(10),
		Expiration: now.Add(48 * time.Hour),
		Start:      now.Add(-1 * time.Hour),
		Allowed:    true,
	}))
	defer func() { _ = s.Promo().DeletePromoCode(ctx, code) }()

	origID := promoIDByCode(ctx, t, s, code)
	require.NotZero(t, origID, "promo should exist after AddPromo")

	// Wave-1 could only delete+recreate to re-enable a disabled code; here we disable then UpdatePromoCode.
	require.NoError(t, s.Promo().DisablePromoCode(ctx, code))

	require.NoError(t, s.Promo().UpdatePromoCode(ctx, &entity.PromoCodeInsert{
		Code:         code,
		FreeShipping: true,
		Discount:     decimal.NewFromInt(25),
		Expiration:   now.Add(72 * time.Hour),
		Start:        now.Add(-2 * time.Hour),
		Allowed:      true,
	}))

	got := promoByCode(ctx, t, s, code)
	require.NotNil(t, got)
	require.Equal(t, origID, got.Id, "row id preserved (no delete+recreate)")
	require.True(t, got.Allowed, "re-enabled in place via UpdatePromoCode")
	require.True(t, got.FreeShipping, "free_shipping updated")
	require.True(t, decimal.NewFromInt(25).Equal(got.Discount), "discount updated, got %s", got.Discount)

	// Cache reflects the DB truth (id preserved, allowed toggled on).
	cached, ok := cache.GetPromoByCode(code)
	require.True(t, ok)
	require.Equal(t, origID, cached.Id)
	require.True(t, cached.Allowed)

	// Updating a non-existent code is NOT_FOUND (sql.ErrNoRows).
	err = s.Promo().UpdatePromoCode(ctx, &entity.PromoCodeInsert{
		Code: "NOPE-DOES-NOT-EXIST", Discount: decimal.Zero, Expiration: now, Start: now,
	})
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func promoIDByCode(ctx context.Context, t *testing.T, s *MYSQLStore, code string) int {
	t.Helper()
	p := promoByCode(ctx, t, s, code)
	if p == nil {
		return 0
	}
	return p.Id
}

func promoByCode(ctx context.Context, t *testing.T, s *MYSQLStore, code string) *entity.PromoCode {
	t.Helper()
	list, err := s.Promo().ListPromos(ctx, 500, 0, entity.Descending)
	require.NoError(t, err)
	for i := range list {
		if list[i].Code == code {
			return &list[i]
		}
	}
	return nil
}

// TestListOrdersBuyerIdentityProjection exercises GAP 3: GetOrdersByStatusAndPaymentTypePaged (the
// admin orders-list store query) now projects buyer identity (email/first/last) from the already-joined
// buyer table, so the list row carries who placed the order instead of a raw UUID.
func TestListOrdersBuyerIdentityProjection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	orderID := seedOrder(ctx, t)

	// Address for the buyer FKs (billing + shipping reuse the same row).
	res, err := testDB.ExecContext(ctx, `INSERT INTO address (country, city, address_line_one, postal_code)
		VALUES ('US', 'NYC', '1 Main St', '10001')`)
	require.NoError(t, err)
	addrID, err := res.LastInsertId()
	require.NoError(t, err)

	// Buyer joined to the order (INNER JOIN buyer in the list query).
	_, err = testDB.ExecContext(ctx, `INSERT INTO buyer
		(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
		VALUES (?, 'Ada', 'Lovelace', 'ada@example.com', '1234567', ?, ?)`, orderID, addrID, addrID)
	require.NoError(t, err)

	// Payment joined to the order (INNER JOIN payment in the list query).
	_, err = testDB.ExecContext(ctx, `INSERT INTO payment
		(order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency)
		SELECT ?, id, 100.00, 100.00 FROM payment_method LIMIT 1`, orderID)
	require.NoError(t, err)

	orders, err := s.Order().GetOrdersByStatusAndPaymentTypePaged(ctx, "", "", 0, 0, orderID, 10, 0, entity.Descending)
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, "ada@example.com", orders[0].BuyerEmail)
	require.Equal(t, "Ada", orders[0].BuyerFirstName)
	require.Equal(t, "Lovelace", orders[0].BuyerLastName)
}

// TestAdminColorwayRefLockVersionAndLabDip exercises GAP 1: the tech-card colourway enrichment now
// surfaces the colourway's optimistic-lock token (its style's shared tech_card.lock_version) on the
// derived ref, alongside the lab-dip lifecycle — so the admin can READ current lab-dip state and do a
// safe optimistic-locked lab-dip write (UpdateColorway.expected_colorway_version) straight from the ref.
func TestAdminColorwayRefLockVersionAndLabDip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "HARDLV", "SS", "SS26", 2026)

	prd := newColorwayInsert("BLK", "black", "HARDLV-BLACK", mediaID, langID, prices)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", colorwayID) }()

	card, err := s.TechCards().GetTechCardById(ctx, styleID)
	require.NoError(t, err)
	require.Len(t, card.Colorways, 1)
	cw := card.Colorways[0]

	// Lab-dip lifecycle default surfaced on the colourway read (mirrors the development submessage).
	require.Equal(t, entity.TechCardLabDipStatus("pending"), cw.LabDipStatus)

	// GAP 1 core: the ref's lock_version equals the parent style's shared tech_card.lock_version —
	// exactly the value UpdateColorway compares against as expected_colorway_version.
	var dbLockVersion int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&dbLockVersion))
	require.Equal(t, dbLockVersion, cw.LockVersion, "colourway ref lock_version mirrors tech_card.lock_version")
	require.Equal(t, card.LockVersion, cw.LockVersion, "ref lock_version equals the card's top-level lock_version")
}
