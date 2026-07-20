package accounting

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assertAllPositive re-asserts, line by line, the ValidateBalanced invariant that every amount is
// strictly positive (the side alone carries the sign) — belt and braces for the sweep test below.
func assertAllPositive(t *testing.T, e entity.AcctJournalEntryInsert) {
	t.Helper()
	for _, l := range e.Lines {
		assert.Truef(t, l.Amount.IsPositive(), "line %s %s amount %s must be positive", l.Side, l.AccountCode, l.Amount.String())
	}
}

// --- BuildOrderPrepaymentEntry (S1n) --------------------------------------------------------------

// TestBuildOrderPrepaymentEntry_Balanced_VatSplit reproduces the 04-walkthrough numbers at the
// prepayment stage: 250 EUR, OSS 23% inclusive VAT recognised now (46.75), the remainder (203.25)
// parked as a customer-prepayment liability — no revenue, no COGS at this stage.
func TestBuildOrderPrepaymentEntry_Balanced_VatSplit(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID:              "prepay-250",
		TotalPrice:        dec("250.00"),
		Currency:          "EUR",
		TotalSettledBase:  nd("250.00"),
		PaymentMethodName: entity.CARD,
	}
	e, err := BuildOrderPrepaymentEntry(f, vatDec(entity.VatRegimeOSS, "23"), testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceOrderPrepayment, e.SourceType)
	assert.Equal(t, "prepay-250", e.SourceKey)
	assert.Equal(t, testOccurred, e.OccurredAt)
	assert.False(t, e.HasCaveat)

	assertAmount(t, e, Acc1030, entity.AcctSideDebit, "250.00")
	assertAmount(t, e, Acc2070, entity.AcctSideCredit, "46.75") // vatInclusive(250, 23)
	assertAmount(t, e, Acc2090, entity.AcctSideCredit, "203.25")

	// No revenue and no COGS at prepayment — those land at delivery.
	assert.False(t, hasLine(e, Acc5010, entity.AcctSideDebit))
	assert.False(t, hasLine(e, Acc1130, entity.AcctSideCredit))
	assert.False(t, hasLine(e, Acc4020, entity.AcctSideCredit))
	assert.False(t, hasLine(e, Acc4310, entity.AcctSideCredit))
}

// TestBuildOrderPrepaymentEntry_ZeroVatRegime checks the no-VAT regimes (export / wdt / none) post
// no 2070 line — the whole gross is parked on 2090.
func TestBuildOrderPrepaymentEntry_ZeroVatRegime(t *testing.T) {
	for _, regime := range []entity.VatRegime{entity.VatRegimeExport, entity.VatRegimeWDT, entity.VatRegimeNone} {
		t.Run(string(regime), func(t *testing.T) {
			f := entity.AcctOrderFacts{
				UUID:              "prepay-" + string(regime),
				TotalPrice:        dec("100.00"),
				Currency:          "EUR",
				TotalSettledBase:  nd("100.00"),
				PaymentMethodName: entity.CARD,
			}
			e, err := BuildOrderPrepaymentEntry(f, VatDecision{Regime: regime}, testOccurred)
			require.NoError(t, err)
			require.NoError(t, ValidateBalanced(e))

			assertAmount(t, e, Acc2090, entity.AcctSideCredit, "100.00")
			assert.False(t, hasLine(e, Acc2070, entity.AcctSideCredit), "no VAT line")
		})
	}
}

// TestBuildOrderPrepaymentEntry_FeeLeg checks the captured Stripe fee books its own balanced
// Dr 6050 / Cr money pair, independent of the VAT split.
func TestBuildOrderPrepaymentEntry_FeeLeg(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID:              "prepay-fee",
		TotalPrice:        dec("100.00"),
		Currency:          "EUR",
		TotalSettledBase:  nd("100.00"),
		PaymentMethodName: entity.CARD_TEST,
		PaymentFee:        nd("3.00"),
	}
	e, err := BuildOrderPrepaymentEntry(f, noVat(), testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assertAmount(t, e, Acc6050, entity.AcctSideDebit, "3.00")
	assertAmount(t, e, Acc1030, entity.AcctSideCredit, "3.00")
	assertAmount(t, e, Acc2090, entity.AcctSideCredit, "100.00") // fee leg does not touch the prepayment
	assert.False(t, e.HasCaveat, "captured stripe fee needs no method-model caveat")
}

// --- BuildOrderTransitEntry (shipped) -------------------------------------------------------------

// TestBuildOrderTransitEntry_Balanced checks 1140 debit == 1130 credit == Σ(UnitCost × Qty).
func TestBuildOrderTransitEntry_Balanced(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID: "transit-1",
		Items: []entity.AcctOrderItemFact{
			{Id: 1, ProductId: 100, Quantity: dec("1"), UnitCost: nd("84.50")},
			{Id: 2, ProductId: 200, Quantity: dec("2"), UnitCost: nd("10.00")},
		},
	}
	e, err := BuildOrderTransitEntry(f, testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceOrderTransit, e.SourceType)
	assert.Equal(t, "transit-1", e.SourceKey)
	assertAmount(t, e, Acc1140, entity.AcctSideDebit, "104.50") // 84.50 + 2*10.00
	assertAmount(t, e, Acc1130, entity.AcctSideCredit, "104.50")
	assert.False(t, e.HasCaveat)
}

// TestBuildOrderTransitEntry_AllUncosted_ErrSkipEmpty checks nothing costed -> ErrSkipEmpty (the
// worker records the shipped event processed without posting).
func TestBuildOrderTransitEntry_AllUncosted_ErrSkipEmpty(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID: "transit-empty",
		Items: []entity.AcctOrderItemFact{
			{Id: 1, ProductId: 100, Quantity: dec("1"), UnitCost: nullDec()},
		},
	}
	_, err := BuildOrderTransitEntry(f, testOccurred)
	assert.ErrorIs(t, err, ErrSkipEmpty)
}

// TestBuildOrderTransitEntry_PartialUncostedCaveat checks a mix of costed/uncosted lines posts the
// costed sum and raises a caveat naming the uncosted product.
func TestBuildOrderTransitEntry_PartialUncostedCaveat(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID: "transit-partial",
		Items: []entity.AcctOrderItemFact{
			{Id: 1, ProductId: 100, Quantity: dec("1"), UnitCost: nd("84.50")},
			{Id: 2, ProductId: 200, Quantity: dec("1"), UnitCost: nullDec()},
		},
	}
	e, err := BuildOrderTransitEntry(f, testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assertAmount(t, e, Acc1140, entity.AcctSideDebit, "84.50")
	assertAmount(t, e, Acc1130, entity.AcctSideCredit, "84.50")
	assert.True(t, e.HasCaveat)
	assert.Contains(t, e.Caveat.String, "200")
}

// --- BuildOrderDeliveredSaleEntry (S1d) -----------------------------------------------------------

// deliveredFacts is the delivered-stage sibling of the 04-walkthrough 250 EUR order: same gross,
// same shipment, k = 1 (settled base == total price).
func deliveredFacts(uuid string, method entity.PaymentMethodName) entity.AcctOrderFacts {
	return entity.AcctOrderFacts{
		UUID:              uuid,
		TotalPrice:        dec("250.00"),
		Currency:          "EUR",
		TotalSettledBase:  nd("250.00"),
		PaymentMethodName: method,
		ShipmentCost:      nd("10.00"),
		FreeShipping:      sql.NullBool{Bool: false, Valid: true},
	}
}

// TestBuildOrderDeliveredSaleEntry_DrainsExact checks the drain of the exact posted balances
// (prepaymentNet 203.25, transitCost 84.50) into shipping + NET revenue + COGS, with no 2070 line
// (VAT was already recognised at prepayment).
func TestBuildOrderDeliveredSaleEntry_DrainsExact(t *testing.T) {
	f := deliveredFacts("delivered-1", entity.CARD)
	e, err := BuildOrderDeliveredSaleEntry(f, dec("203.25"), dec("84.50"), testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceOrderDeliveredSale, e.SourceType)
	assert.Equal(t, "delivered-1", e.SourceKey)
	assert.False(t, e.HasCaveat)

	assertAmount(t, e, Acc2090, entity.AcctSideDebit, "203.25")
	assertAmount(t, e, Acc4110, entity.AcctSideCredit, "10.00")
	assertAmount(t, e, Acc4020, entity.AcctSideCredit, "193.25")
	assertAmount(t, e, Acc5010, entity.AcctSideDebit, "84.50")
	assertAmount(t, e, Acc1140, entity.AcctSideCredit, "84.50")

	assert.False(t, hasLine(e, Acc2070, entity.AcctSideCredit), "VAT already recognised at prepayment")
	assert.False(t, hasLine(e, Acc2070, entity.AcctSideDebit), "VAT already recognised at prepayment")
}

// TestBuildOrderDeliveredSaleEntry_B2B_Revenue4310 checks a B2B order (buyer VAT id present) routes
// the drained revenue to 4310 Wholesale instead of the B2C 4020.
func TestBuildOrderDeliveredSaleEntry_B2B_Revenue4310(t *testing.T) {
	f := deliveredFacts("delivered-b2b", entity.CARD)
	f.BuyerVatID = vatID("FR12345678901")

	e, err := BuildOrderDeliveredSaleEntry(f, dec("203.25"), dec("84.50"), testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assertAmount(t, e, Acc4310, entity.AcctSideCredit, "193.25")
	assert.False(t, hasLine(e, Acc4020, entity.AcctSideCredit), "B2B revenue does not also land on 4020")
}

// TestBuildOrderDeliveredSaleEntry_ZeroPrepayment_ErrSkipEmpty checks a fully pre-refunded (or
// otherwise non-positive) prepayment remainder skips the entry rather than posting a negative/zero
// drain.
func TestBuildOrderDeliveredSaleEntry_ZeroPrepayment_ErrSkipEmpty(t *testing.T) {
	f := deliveredFacts("delivered-zero", entity.CARD)
	for _, prepaymentNet := range []decimal.Decimal{decimal.Zero, dec("-5.00")} {
		_, err := BuildOrderDeliveredSaleEntry(f, prepaymentNet, dec("10.00"), testOccurred)
		assert.ErrorIs(t, err, ErrSkipEmpty)
	}
}

// TestBuildOrderDeliveredSaleEntry_NoTransitCost_RevenueOnlyCaveat checks an order that was never
// costed (no outstanding 1140 balance) posts no 5010/1140 pair, raises a caveat, and the revenue
// line alone still balances the 2090 drain.
func TestBuildOrderDeliveredSaleEntry_NoTransitCost_RevenueOnlyCaveat(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID:              "delivered-noship",
		TotalPrice:        dec("100.00"),
		Currency:          "EUR",
		TotalSettledBase:  nd("100.00"),
		PaymentMethodName: entity.CARD,
	}
	e, err := BuildOrderDeliveredSaleEntry(f, dec("100.00"), decimal.Zero, testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.False(t, hasLine(e, Acc5010, entity.AcctSideDebit))
	assert.False(t, hasLine(e, Acc1140, entity.AcctSideCredit))
	assertAmount(t, e, Acc2090, entity.AcctSideDebit, "100.00")
	assertAmount(t, e, Acc4020, entity.AcctSideCredit, "100.00")
	assert.True(t, e.HasCaveat)
	assert.Contains(t, e.Caveat.String, "COGS not recognised")
}

// --- BuildOrderPreDeliveredRefundEntry --------------------------------------------------------------

// TestBuildOrderPreDeliveredRefundEntry_Unshipped checks a refund before the order ever shipped:
// only the 2090 prepayment (+ its 2070 VAT share) unwinds and the money returns — no stock line, and
// (unlike a post-delivery S2 refund) never a 4040/5050 pair.
func TestBuildOrderPreDeliveredRefundEntry_Unshipped(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID:              "order-250",
		TotalPrice:        dec("250.00"),
		Currency:          "EUR",
		TotalSettledBase:  nd("250.00"),
		PaymentMethodName: entity.CARD,
	}
	refund := entity.AcctOrderRefundPayload{
		OrderUUID:      "order-250",
		RefundAmount:   dec("125.00"),
		OrderCurrency:  "EUR",
		RefundedByItem: map[int]int64{},
	}
	e, err := BuildOrderPreDeliveredRefundEntry(f, refund, nil, vatDec(entity.VatRegimeOSS, "23"), false, "order-250:1", testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceOrderRefund, e.SourceType)
	assert.Equal(t, "order-250:1", e.SourceKey)

	assertAmount(t, e, Acc2090, entity.AcctSideDebit, "101.62") // 125 - 23.38
	assertAmount(t, e, Acc2070, entity.AcctSideDebit, "23.38")  // 46.75 * 0.5
	assertAmount(t, e, Acc1030, entity.AcctSideCredit, "125.00")

	assert.False(t, hasLine(e, Acc1130, entity.AcctSideDebit), "never shipped, nothing to return")
	assert.False(t, hasLine(e, Acc1140, entity.AcctSideCredit), "never shipped, nothing to return")
	assert.False(t, hasLine(e, Acc4040, entity.AcctSideDebit), "revenue was never recognised")
	assert.False(t, hasLine(e, Acc5050, entity.AcctSideCredit), "revenue was never recognised")
}

// TestBuildOrderPreDeliveredRefundEntry_Shipped_ReturnsTransit checks a refund after shipping (but
// before delivery) additionally returns the transit stock to finished goods at its costed value.
func TestBuildOrderPreDeliveredRefundEntry_Shipped_ReturnsTransit(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID:              "order-250",
		TotalPrice:        dec("250.00"),
		Currency:          "EUR",
		TotalSettledBase:  nd("250.00"),
		PaymentMethodName: entity.CARD,
		Items: []entity.AcctOrderItemFact{
			{Id: 1, ProductId: 100, Quantity: dec("1"), UnitCost: nd("84.50")},
		},
	}
	refund := entity.AcctOrderRefundPayload{
		OrderUUID:      "order-250",
		RefundAmount:   dec("250.00"),
		OrderCurrency:  "EUR",
		RefundedByItem: map[int]int64{1: 1},
	}
	e, err := BuildOrderPreDeliveredRefundEntry(f, refund, f.Items, vatDec(entity.VatRegimeOSS, "23"), true, "order-250:2", testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assertAmount(t, e, Acc2090, entity.AcctSideDebit, "203.25")
	assertAmount(t, e, Acc2070, entity.AcctSideDebit, "46.75")
	assertAmount(t, e, Acc1030, entity.AcctSideCredit, "250.00")
	assertAmount(t, e, Acc1130, entity.AcctSideDebit, "84.50") // stock returned from transit
	assertAmount(t, e, Acc1140, entity.AcctSideCredit, "84.50")
}

// TestBuildOrderPreDeliveredRefundEntry_FullRefund_ReversesPrepayment pins the exact-reversal
// property: a full pre-delivery refund at the SAME regime the prepayment used unwinds precisely the
// 2090 + 2070 amounts the prepayment posted — no residual.
func TestBuildOrderPreDeliveredRefundEntry_FullRefund_ReversesPrepayment(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID:              "order-full-250",
		TotalPrice:        dec("250.00"),
		Currency:          "EUR",
		TotalSettledBase:  nd("250.00"),
		PaymentMethodName: entity.CARD,
	}
	vd := vatDec(entity.VatRegimeOSS, "23")

	prepay, err := BuildOrderPrepaymentEntry(f, vd, testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(prepay))
	prepayVat := lineAmount(t, prepay, Acc2070, entity.AcctSideCredit)
	prepayNet := lineAmount(t, prepay, Acc2090, entity.AcctSideCredit)

	refund := entity.AcctOrderRefundPayload{
		OrderUUID:     f.UUID,
		RefundAmount:  f.TotalPrice,
		OrderCurrency: "EUR",
	}
	ref, err := BuildOrderPreDeliveredRefundEntry(f, refund, nil, vd, false, f.UUID+":1", testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(ref))
	refVat := lineAmount(t, ref, Acc2070, entity.AcctSideDebit)
	refNet := lineAmount(t, ref, Acc2090, entity.AcctSideDebit)

	assert.True(t, prepayVat.Equal(refVat), "want exact VAT reversal, prepay %s vs refund %s", prepayVat, refVat)
	assert.True(t, prepayNet.Equal(refNet), "want exact prepayment reversal, prepay %s vs refund %s", prepayNet, refNet)
}

// --- cross-builder sweep ---------------------------------------------------------------------------

// TestDeliveredBuildersAlwaysBalance sweeps the four wave-2 builders over varied gross/rate/ship/cost
// combinations (including a ship-exceeds-remainder guard trip and an all-uncosted transit skip),
// asserting every entry that IS built balances and posts strictly positive amounts — the two package
// invariants (balance.go) must hold regardless of the input mix.
func TestDeliveredBuildersAlwaysBalance(t *testing.T) {
	type row struct {
		name      string
		gross     string
		regime    entity.VatRegime
		rate      string // "" -> no rate set (RegimeHasVAT decides whether it matters)
		ship      string // "" -> no shipment on the order
		unitCost  string // "" -> uncosted line (all-uncosted transit -> ErrSkipEmpty)
		refundAmt string
	}
	rows := []row{
		{name: "oss23 ship+cost", gross: "250.00", regime: entity.VatRegimeOSS, rate: "23", ship: "10.00", unitCost: "84.50", refundAmt: "50.00"},
		{name: "export no-vat no-ship uncosted", gross: "100.00", regime: entity.VatRegimeExport, refundAmt: "100.00"},
		{name: "oss19 ship exceeds prepayment remainder", gross: "500.00", regime: entity.VatRegimeOSS, rate: "19", ship: "450.00", unitCost: "200.00", refundAmt: "200.00"},
		{name: "pl_domestic8 odd cents", gross: "59.99", regime: entity.VatRegimePLDomestic, rate: "8", ship: "5.00", unitCost: "13.37", refundAmt: "59.99"},
	}

	for i, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			f := entity.AcctOrderFacts{
				UUID:              fmtSweepUUID(i),
				TotalPrice:        dec(tc.gross),
				Currency:          "EUR",
				TotalSettledBase:  nd(tc.gross),
				PaymentMethodName: entity.CARD,
			}
			if tc.ship != "" {
				f.ShipmentCost = nd(tc.ship)
				f.FreeShipping = sql.NullBool{Bool: false, Valid: true}
			}
			uc := nullDec()
			if tc.unitCost != "" {
				uc = nd(tc.unitCost)
			}
			f.Items = []entity.AcctOrderItemFact{{Id: 1, ProductId: 100, Quantity: dec("1"), UnitCost: uc}}

			vd := VatDecision{Regime: tc.regime}
			if tc.rate != "" {
				vd.RatePct = dec(tc.rate)
			}

			// order_prepayment.
			prepay, err := BuildOrderPrepaymentEntry(f, vd, testOccurred)
			require.NoError(t, err)
			require.NoError(t, ValidateBalanced(prepay))
			assertAllPositive(t, prepay)
			prepaymentNet := lineAmount(t, prepay, Acc2090, entity.AcctSideCredit)

			// order_transit (may legitimately skip when nothing is costed).
			transitCost := decimal.Zero
			transitPosted := false
			transit, err := BuildOrderTransitEntry(f, testOccurred)
			if err != nil {
				require.ErrorIs(t, err, ErrSkipEmpty)
			} else {
				require.NoError(t, ValidateBalanced(transit))
				assertAllPositive(t, transit)
				transitCost = lineAmount(t, transit, Acc1140, entity.AcctSideDebit)
				transitPosted = true
			}

			// order_delivered_sale.
			delivered, err := BuildOrderDeliveredSaleEntry(f, prepaymentNet, transitCost, testOccurred)
			require.NoError(t, err)
			require.NoError(t, ValidateBalanced(delivered))
			assertAllPositive(t, delivered)

			// order_refund (pre-delivery unwind).
			refund := entity.AcctOrderRefundPayload{
				OrderUUID:      f.UUID,
				RefundAmount:   dec(tc.refundAmt),
				OrderCurrency:  "EUR",
				RefundedByItem: map[int]int64{1: 1},
			}
			ref, err := BuildOrderPreDeliveredRefundEntry(f, refund, f.Items, vd, transitPosted, f.UUID+":1", testOccurred)
			require.NoError(t, err)
			require.NoError(t, ValidateBalanced(ref))
			assertAllPositive(t, ref)
		})
	}
}

func fmtSweepUUID(i int) string {
	return "sweep-order-" + string(rune('a'+i))
}
