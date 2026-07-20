package accounting

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discountFacts is a minimal EUR Stripe order (G = 100, no VAT / shipping / fee / COGS) so the revenue
// split math is easy to read: with a 20% promo, fullNet = 100 / 0.80 = 125, discount = 25.
func discountFacts(pct string) entity.AcctOrderFacts {
	f := entity.AcctOrderFacts{
		UUID:              "order-disc",
		TotalPrice:        dec("100.00"),
		Currency:          "EUR",
		TotalSettledBase:  nd("100.00"),
		PaymentMethodName: entity.CARD,
		FreeShipping:      sql.NullBool{Bool: false, Valid: true},
	}
	if pct != "" {
		f.PromoDiscountPct = nd(pct)
	}
	return f
}

func TestBuildOrderSaleEntry_DiscountSplit(t *testing.T) {
	e, err := BuildOrderSaleEntry(discountFacts("20.00"), noVat(), testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assertAmount(t, e, Acc1030, entity.AcctSideDebit, "100.00")  // money in
	assertAmount(t, e, Acc4020, entity.AcctSideCredit, "125.00") // full-price NET
	assertAmount(t, e, Acc4030, entity.AcctSideDebit, "25.00")   // reconstructed discount contra
	assert.False(t, e.HasCaveat)

	// P&L revenue effect is unchanged: 4020 credit − 4030 debit == the plain net (100).
	net := lineAmount(t, e, Acc4020, entity.AcctSideCredit).Sub(lineAmount(t, e, Acc4030, entity.AcctSideDebit))
	assert.Equal(t, "100.00", net.StringFixed(2))
}

func TestBuildOrderSaleEntry_NoPromoNoSplit(t *testing.T) {
	e, err := BuildOrderSaleEntry(discountFacts(""), noVat(), testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	assertAmount(t, e, Acc4020, entity.AcctSideCredit, "100.00")
	assert.False(t, hasLine(e, Acc4030, entity.AcctSideDebit))
}

func TestBuildOrderSaleEntry_ZeroPctNoSplit(t *testing.T) {
	e, err := BuildOrderSaleEntry(discountFacts("0.00"), noVat(), testOccurred)
	require.NoError(t, err)
	assert.False(t, hasLine(e, Acc4030, entity.AcctSideDebit))
	assert.False(t, e.HasCaveat)
}

func TestBuildOrderSaleEntry_DiscountGuardFallsBack(t *testing.T) {
	// A >= 100% discount is not reconstructable — fall back to the single credit + caveat, never break
	// the entry balance for analytics (07 §7.4.11).
	e, err := BuildOrderSaleEntry(discountFacts("100.00"), noVat(), testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	assertAmount(t, e, Acc4020, entity.AcctSideCredit, "100.00")
	assert.False(t, hasLine(e, Acc4030, entity.AcctSideDebit))
	assert.True(t, e.HasCaveat)
	assert.Contains(t, e.Caveat.String, "not reconstructable")
}

func TestBuildOrderDeliveredSaleEntry_DiscountSplit(t *testing.T) {
	// Delivered chain uses the SAME split: drain 100 off 2090, credit full-price 125, debit 25 to 4030.
	e, err := BuildOrderDeliveredSaleEntry(discountFacts("20.00"), dec("100.00"), decimal.Zero, testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	assertAmount(t, e, Acc2090, entity.AcctSideDebit, "100.00")
	assertAmount(t, e, Acc4020, entity.AcctSideCredit, "125.00")
	assertAmount(t, e, Acc4030, entity.AcctSideDebit, "25.00")
}
