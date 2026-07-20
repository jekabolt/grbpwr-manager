package accounting

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vdOSS23 is the resolved regime a card/EU sale posts with (23% inclusive). The refund must reverse the
// SAME regime VAT (rule S2 mirrors S1), so most cases below pass it. When it matches the saleFacts
// snapshot (46.75 on 250), the reversal equals the pre-regime snapshot figure.
func vdOSS23() VatDecision { return VatDecision{Regime: entity.VatRegimeOSS, RatePct: dec("23")} }

func saleFacts(method entity.PaymentMethodName) entity.AcctOrderFacts {
	return entity.AcctOrderFacts{
		UUID:              "order-250",
		TotalPrice:        dec("250.00"),
		Currency:          "EUR",
		TotalSettledBase:  nd("250.00"),
		VatAmount:         nd("46.75"),
		PaymentMethodName: method,
		Items: []entity.AcctOrderItemFact{
			{Id: 1, ProductId: 100, Quantity: dec("1"), UnitCost: nd("84.50")},
		},
	}
}

func TestBuildOrderRefundEntry_FullRefund(t *testing.T) {
	f := saleFacts(entity.CARD)
	refund := entity.AcctOrderRefundPayload{
		OrderUUID:      "order-250",
		RefundAmount:   dec("250.00"),
		OrderCurrency:  "EUR",
		RefundedByItem: map[int]int64{1: 1},
	}

	e, err := BuildOrderRefundEntry(f, refund, f.Items, vdOSS23(), "order-250:1", testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceOrderRefund, e.SourceType)
	assert.Equal(t, "order-250:1", e.SourceKey)
	assert.False(t, e.HasCaveat)

	assertAmount(t, e, Acc4040, entity.AcctSideDebit, "203.25")  // contra-revenue (NETr)
	assertAmount(t, e, Acc2070, entity.AcctSideDebit, "46.75")   // VAT reversal (regime 23%)
	assertAmount(t, e, Acc1030, entity.AcctSideCredit, "250.00") // money back to processor
	assertAmount(t, e, Acc1130, entity.AcctSideDebit, "84.50")   // stock back
	assertAmount(t, e, Acc5050, entity.AcctSideCredit, "84.50")  // contra-COGS

	// The fee is deliberately NOT reversed.
	assert.False(t, hasLine(e, Acc6050, entity.AcctSideDebit))
}

func TestBuildOrderRefundEntry_Cases(t *testing.T) {
	// cashFacts: a €120 cash order whose sale-time snapshot (from the address-based 0094 backfill) is
	// 23%, but whose RESOLVED regime is uk_stock_domestic at 20% — the exact C-1 mismatch.
	cashFacts := entity.AcctOrderFacts{
		UUID: "cash-120", TotalPrice: dec("120.00"), Currency: "EUR",
		TotalSettledBase:  nd("120.00"),
		VatAmount:         nd("22.44"), // 120*23/123 snapshot
		PaymentMethodName: entity.CASH,
	}
	tests := []struct {
		name   string
		facts  entity.AcctOrderFacts
		refund entity.AcctOrderRefundPayload
		vd     VatDecision
		items  []entity.AcctOrderItemFact
		check  func(t *testing.T, e entity.AcctJournalEntryInsert)
	}{
		{
			name:  "partial money refund without item return",
			facts: saleFacts(entity.CARD),
			vd:    vdOSS23(),
			refund: entity.AcctOrderRefundPayload{
				OrderUUID: "order-250", RefundAmount: dec("125.00"), OrderCurrency: "EUR",
				RefundedByItem: map[int]int64{},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc4040, entity.AcctSideDebit, "101.62") // 125 - 23.38
				assertAmount(t, e, Acc2070, entity.AcctSideDebit, "23.38")  // 46.75 * 0.5
				assertAmount(t, e, Acc1030, entity.AcctSideCredit, "125.00")
				assert.False(t, hasLine(e, Acc1130, entity.AcctSideDebit), "no stock return")
			},
		},
		{
			name:  "bank-invoice refund credits the receivable",
			facts: saleFacts(entity.BANK_INVOICE),
			vd:    vdOSS23(),
			refund: entity.AcctOrderRefundPayload{
				OrderUUID: "order-250", RefundAmount: dec("250.00"), OrderCurrency: "EUR",
				RefundedByItem: map[int]int64{1: 1},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc1040, entity.AcctSideCredit, "250.00")
				assert.False(t, hasLine(e, Acc1030, entity.AcctSideCredit))
			},
		},
		{
			// C-1: a cash order reverses the REGIME 20%, not the 23% snapshot — full refund → 2070 nets 0.
			name:  "cash refund reverses regime 20 not snapshot 23",
			facts: cashFacts,
			vd:    VatDecision{Regime: entity.VatRegimeUKStockDomestic, RatePct: dec("20")},
			refund: entity.AcctOrderRefundPayload{
				OrderUUID: "cash-120", RefundAmount: dec("120.00"), OrderCurrency: "EUR",
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc2070, entity.AcctSideDebit, "20.00")  // 120*20/120, NOT the 22.44 snapshot
				assertAmount(t, e, Acc4040, entity.AcctSideDebit, "100.00") // 120 - 20
				assertAmount(t, e, Acc1010, entity.AcctSideCredit, "120.00")
				assert.True(t, e.HasCaveat) // 23% snapshot vs 20% regime is flagged
				assert.Contains(t, e.Caveat.String, "vat snapshot mismatch")
			},
		},
		{
			// C-1: an export/wdt sale posts NO VAT, so its refund reverses none — even though the
			// address-based snapshot is non-zero. Previously this left a phantom 2070 debit.
			name:  "export refund reverses no VAT despite non-zero snapshot",
			facts: saleFacts(entity.CARD), // snapshot 46.75, but regime resolved to export
			vd:    VatDecision{Regime: entity.VatRegimeExport},
			refund: entity.AcctOrderRefundPayload{
				OrderUUID: "order-250", RefundAmount: dec("250.00"), OrderCurrency: "EUR",
				RefundedByItem: map[int]int64{1: 1},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assert.False(t, hasLine(e, Acc2070, entity.AcctSideDebit), "no VAT reversal for export")
				assertAmount(t, e, Acc4040, entity.AcctSideDebit, "250.00") // full contra-revenue, no VAT carved
			},
		},
		{
			name:  "uncosted refunded item understates COGS return",
			facts: saleFacts(entity.CARD),
			vd:    vdOSS23(),
			refund: entity.AcctOrderRefundPayload{
				OrderUUID: "order-250", RefundAmount: dec("250.00"), OrderCurrency: "EUR",
				RefundedByItem: map[int]int64{1: 1},
			},
			items: []entity.AcctOrderItemFact{
				{Id: 1, ProductId: 100, Quantity: dec("1"), UnitCost: nullDec()},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assert.False(t, hasLine(e, Acc1130, entity.AcctSideDebit), "no costed stock return")
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, "100")
			},
		},
		{
			// A-4: the payload claims 5 refunded but only 1 was sold — COGS return clamps to 1 unit.
			name:  "refunded quantity clamped to sold",
			facts: saleFacts(entity.CARD),
			vd:    vdOSS23(),
			refund: entity.AcctOrderRefundPayload{
				OrderUUID: "order-250", RefundAmount: dec("250.00"), OrderCurrency: "EUR",
				RefundedByItem: map[int]int64{1: 5},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc1130, entity.AcctSideDebit, "84.50") // 1 unit, not 5 * 84.50
			},
		},
		{
			// A-4: a refunded item id not on the order contributes no COGS and raises a caveat.
			name:  "refund references unknown item",
			facts: saleFacts(entity.CARD),
			vd:    vdOSS23(),
			refund: entity.AcctOrderRefundPayload{
				OrderUUID: "order-250", RefundAmount: dec("250.00"), OrderCurrency: "EUR",
				RefundedByItem: map[int]int64{999: 1},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assert.False(t, hasLine(e, Acc1130, entity.AcctSideDebit), "unknown item contributes no COGS")
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, "not on the order")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := tt.items
			if items == nil {
				items = tt.facts.Items
			}
			e, err := BuildOrderRefundEntry(tt.facts, tt.refund, items, tt.vd, "sk:1", testOccurred)
			require.NoError(t, err)
			require.NoError(t, ValidateBalanced(e))
			tt.check(t, e)
		})
	}
}

func TestBuildOrderRefundEntry_Errors(t *testing.T) {
	tests := []struct {
		name    string
		facts   entity.AcctOrderFacts
		refund  entity.AcctOrderRefundPayload
		wantErr error
	}{
		{
			name:    "zero refund amount",
			facts:   saleFacts(entity.CARD),
			refund:  entity.AcctOrderRefundPayload{OrderUUID: "order-250", RefundAmount: dec("0"), OrderCurrency: "EUR"},
			wantErr: ErrDegenerateAmounts,
		},
		{
			name: "non-stripe non-eur order refund is skipped like its sale",
			facts: entity.AcctOrderFacts{
				TotalPrice: dec("100.00"), Currency: "USD",
				TotalSettledBase: nullDec(), PaymentMethodName: entity.CASH,
			},
			refund:  entity.AcctOrderRefundPayload{RefundAmount: dec("50.00"), OrderCurrency: "USD"},
			wantErr: ErrSkipNonEUR,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// vd is irrelevant: these fail in the gross/degenerate guards before any VAT math.
			_, err := BuildOrderRefundEntry(tt.facts, tt.refund, tt.facts.Items, VatDecision{}, "sk:1", testOccurred)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// TestRefundReversesSaleVATToZero pins the C-1 goal directly: for a full refund at the SAME regime the
// sale used, the 2070 credit (sale) and 2070 debit (refund) are the identical amount, so the account
// nets to zero — no residual — across every VAT-bearing regime.
func TestRefundReversesSaleVATToZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		reg  entity.VatRegime
		rate string
	}{
		{"oss 19", entity.VatRegimeOSS, "19"},
		{"pl_domestic 23", entity.VatRegimePLDomestic, "23"},
		{"uk_stock_domestic 20", entity.VatRegimeUKStockDomestic, "20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := saleFacts(entity.CARD)
			vd := VatDecision{Regime: tc.reg, RatePct: dec(tc.rate)}
			g, err := grossEUR(f)
			require.NoError(t, err)
			want := vatInclusive(g, vd.RatePct).StringFixed(2)

			sale, err := BuildOrderSaleEntry(f, vd, testOccurred)
			require.NoError(t, err)
			assertAmount(t, sale, Acc2070, entity.AcctSideCredit, want)

			refund := entity.AcctOrderRefundPayload{
				OrderUUID: f.UUID, RefundAmount: f.TotalPrice, OrderCurrency: "EUR",
				RefundedByItem: map[int]int64{1: 1},
			}
			ref, err := BuildOrderRefundEntry(f, refund, f.Items, vd, f.UUID+":1", testOccurred)
			require.NoError(t, err)
			assertAmount(t, ref, Acc2070, entity.AcctSideDebit, want) // exact reversal → 2070 nets to zero
		})
	}
}
