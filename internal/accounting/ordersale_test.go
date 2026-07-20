package accounting

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vatDec is a VatDecision with a regime + rate; noVat is a no-VAT regime (export).
func vatDec(regime entity.VatRegime, rate string) VatDecision {
	return VatDecision{Regime: regime, RatePct: dec(rate)}
}
func noVat() VatDecision { return VatDecision{Regime: entity.VatRegimeExport} }

// vatID wraps a buyer VAT id as a valid NullString (marks an order B2B).
func vatID(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// TestBuildOrderSaleEntry_Walkthrough reproduces the end-to-end example from
// docs/plan-accounting/04: a 250 EUR website order, OSS 23% inclusive (regime VAT 46.75, matching the
// 46.75 snapshot), shipping 10, Stripe fee 7.55, one unit at cost 84.50 -> NET 193.25.
func TestBuildOrderSaleEntry_Walkthrough(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID:              "order-250",
		TotalPrice:        dec("250.00"),
		Currency:          "EUR",
		TotalSettledBase:  nd("250.00"),
		PaymentFee:        nd("7.55"),
		VatAmount:         nd("46.75"),
		PaymentMethodName: entity.CARD,
		ShipmentCost:      nd("10.00"),
		FreeShipping:      sql.NullBool{Bool: false, Valid: true},
		Items: []entity.AcctOrderItemFact{
			{Id: 1, ProductId: 100, Quantity: dec("1"), UnitCost: nd("84.50")},
		},
	}

	e, err := BuildOrderSaleEntry(f, vatDec(entity.VatRegimeOSS, "23"), testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceOrderSale, e.SourceType)
	assert.Equal(t, "order-250", e.SourceKey)
	assert.Equal(t, testOccurred, e.OccurredAt)
	assert.False(t, e.HasCaveat)

	assertAmount(t, e, Acc1030, entity.AcctSideDebit, "250.00")  // money on the processor
	assertAmount(t, e, Acc4020, entity.AcctSideCredit, "193.25") // NET revenue (DTC)
	assertAmount(t, e, Acc4110, entity.AcctSideCredit, "10.00")  // shipping income
	assertAmount(t, e, Acc2070, entity.AcctSideCredit, "46.75")  // regime VAT
	assertAmount(t, e, Acc6050, entity.AcctSideDebit, "7.55")    // fee expense
	assertAmount(t, e, Acc1030, entity.AcctSideCredit, "7.55")   // fee against processor
	assertAmount(t, e, Acc5010, entity.AcctSideDebit, "84.50")   // COGS
	assertAmount(t, e, Acc1130, entity.AcctSideCredit, "84.50")  // finished-goods relief
}

func TestBuildOrderSaleEntry_Errors(t *testing.T) {
	tests := []struct {
		name    string
		facts   entity.AcctOrderFacts
		wantErr error
	}{
		{
			name: "stripe settlement pending",
			facts: entity.AcctOrderFacts{
				TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nullDec(), PaymentMethodName: entity.CARD,
			},
			wantErr: ErrNotReady,
		},
		{
			name: "non-stripe non-eur",
			facts: entity.AcctOrderFacts{
				TotalPrice: dec("100.00"), Currency: "USD",
				TotalSettledBase: nullDec(), PaymentMethodName: entity.CASH,
			},
			wantErr: ErrSkipNonEUR,
		},
		{
			name: "zero total price",
			facts: entity.AcctOrderFacts{
				TotalPrice: dec("0"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
			},
			wantErr: ErrDegenerateAmounts,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildOrderSaleEntry(tt.facts, vatDec(entity.VatRegimeOSS, "23"), testOccurred)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestBuildOrderSaleEntry_Cases(t *testing.T) {
	tests := []struct {
		name  string
		facts entity.AcctOrderFacts
		vat   VatDecision
		check func(t *testing.T, e entity.AcctJournalEntryInsert)
	}{
		{
			name: "cash export (no VAT regime) books 1010/4010, no VAT line",
			facts: entity.AcctOrderFacts{
				UUID: "cash-1", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nullDec(), PaymentMethodName: entity.CASH,
				VatAmount: nullDec(), FreeShipping: sql.NullBool{Bool: true, Valid: true},
				Items: []entity.AcctOrderItemFact{{Id: 1, ProductId: 5, Quantity: dec("1"), UnitCost: nd("30.00")}},
			},
			vat: noVat(),
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc1010, entity.AcctSideDebit, "100.00")
				assertAmount(t, e, Acc4010, entity.AcctSideCredit, "100.00")
				assertAmount(t, e, Acc5010, entity.AcctSideDebit, "30.00")
				assertAmount(t, e, Acc1130, entity.AcctSideCredit, "30.00")
				assert.False(t, hasLine(e, Acc2070, entity.AcctSideCredit), "no VAT line")
				assert.False(t, e.HasCaveat)
			},
		},
		{
			name: "cash uk_stock_domestic 20% extracts inclusive VAT to 2070, revenue 4010",
			facts: entity.AcctOrderFacts{
				UUID: "cash-uk", TotalPrice: dec("120.00"), Currency: "EUR",
				TotalSettledBase: nullDec(), PaymentMethodName: entity.CASH,
				VatAmount: nullDec(), FreeShipping: sql.NullBool{Bool: true, Valid: true},
			},
			vat: vatDec(entity.VatRegimeUKStockDomestic, "20"),
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc1010, entity.AcctSideDebit, "120.00")
				assertAmount(t, e, Acc2070, entity.AcctSideCredit, "20.00") // 120*20/120
				assertAmount(t, e, Acc4010, entity.AcctSideCredit, "100.00")
			},
		},
		{
			name: "export B2C posts no VAT, full net to 4020",
			facts: entity.AcctOrderFacts{
				UUID: "exp-1", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
				VatAmount: nullDec(), FreeShipping: sql.NullBool{Bool: true, Valid: true},
			},
			vat: noVat(),
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc4020, entity.AcctSideCredit, "100.00")
				assert.False(t, hasLine(e, Acc2070, entity.AcctSideCredit))
			},
		},
		{
			name: "EU B2B wdt: no VAT, revenue to 4310 wholesale, money to 1040",
			facts: entity.AcctOrderFacts{
				UUID: "wdt-1", TotalPrice: dec("200.00"), Currency: "EUR",
				TotalSettledBase: nullDec(), PaymentMethodName: entity.BANK_INVOICE,
				BuyerVatID: vatID("FR12345678901"), VatAmount: nullDec(),
				FreeShipping: sql.NullBool{Bool: true, Valid: true},
				Items:        []entity.AcctOrderItemFact{{Id: 1, ProductId: 5, Quantity: dec("1"), UnitCost: nd("60.00")}},
			},
			vat: VatDecision{Regime: entity.VatRegimeWDT},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc1040, entity.AcctSideDebit, "200.00")
				assertAmount(t, e, Acc4310, entity.AcctSideCredit, "200.00") // wholesale revenue
				assert.False(t, hasLine(e, Acc2070, entity.AcctSideCredit), "no VAT on reverse charge")
				assert.False(t, hasLine(e, Acc4020, entity.AcctSideCredit))
			},
		},
		{
			name: "PL B2B pl_domestic 23%: VAT to 2070 and revenue to 4310",
			facts: entity.AcctOrderFacts{
				UUID: "plb2b", TotalPrice: dec("123.00"), Currency: "EUR",
				TotalSettledBase: nd("123.00"), PaymentMethodName: entity.CARD,
				BuyerVatID: vatID("PL1234567890"), VatAmount: nd("23.00"),
				FreeShipping: sql.NullBool{Bool: true, Valid: true},
			},
			vat: vatDec(entity.VatRegimePLDomestic, "23"),
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc2070, entity.AcctSideCredit, "23.00")
				assertAmount(t, e, Acc4310, entity.AcctSideCredit, "100.00")
				assert.False(t, e.HasCaveat)
			},
		},
		{
			name: "settled differs from total_price scales shipping by k=0.5; VAT from regime not snapshot",
			facts: entity.AcctOrderFacts{
				UUID: "k-1", TotalPrice: dec("200.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
				// Full-order snapshot 37.40 (200*23/123); scaled by k=0.5 → 18.70, matching the regime VAT.
				VatAmount: nd("37.40"), ShipmentCost: nd("20.00"), FreeShipping: sql.NullBool{Bool: false, Valid: true},
				Items: []entity.AcctOrderItemFact{{Id: 1, ProductId: 5, Quantity: dec("1"), UnitCost: nd("40.00")}},
			},
			vat: vatDec(entity.VatRegimeOSS, "23"),
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc1030, entity.AcctSideDebit, "100.00") // G = settled
				assertAmount(t, e, Acc2070, entity.AcctSideCredit, "18.70") // 100*23/123
				assertAmount(t, e, Acc4110, entity.AcctSideCredit, "10.00") // 20 * 0.5
				assertAmount(t, e, Acc4020, entity.AcctSideCredit, "71.30") // 100 - 18.70 - 10
				assertAmount(t, e, Acc5010, entity.AcctSideDebit, "40.00")  // COGS uses snapshot, not k
				assert.False(t, e.HasCaveat, "regime VAT matches the k-scaled snapshot")
			},
		},
		{
			name: "regime VAT diverging from snapshot raises a mismatch caveat",
			facts: entity.AcctOrderFacts{
				UUID: "mm", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
				VatAmount: nd("5.00"), FreeShipping: sql.NullBool{Bool: true, Valid: true},
			},
			vat: vatDec(entity.VatRegimeOSS, "23"),
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc2070, entity.AcctSideCredit, "18.70") // 100*23/123, not the 5.00 snapshot
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, "vat snapshot mismatch")
			},
		},
		{
			name: "resolver caveats travel onto the entry",
			facts: entity.AcctOrderFacts{
				UUID: "cav", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
				VatAmount: nullDec(), FreeShipping: sql.NullBool{Bool: true, Valid: true},
			},
			vat: VatDecision{Regime: entity.VatRegimeExport, Caveats: []string{CaveatUnknownDestination}},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, CaveatUnknownDestination)
			},
		},
		{
			name: "shipping exceeds post-VAT remainder is dropped with caveat",
			facts: entity.AcctOrderFacts{
				UUID: "shipbig", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
				VatAmount: nullDec(), ShipmentCost: nd("95.00"), FreeShipping: sql.NullBool{Bool: false, Valid: true},
			},
			vat: vatDec(entity.VatRegimeOSS, "23"),
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				// VAT = 100*23/123 = 18.70; ship 95 > 100-18.70 = 81.30 → dropped.
				assertAmount(t, e, Acc2070, entity.AcctSideCredit, "18.70")
				assertAmount(t, e, Acc4020, entity.AcctSideCredit, "81.30")
				assert.False(t, hasLine(e, Acc4110, entity.AcctSideCredit))
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, "shipping exceeds remainder")
			},
		},
		{
			name: "uncosted item understates COGS and is named",
			facts: entity.AcctOrderFacts{
				UUID: "unc", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
				VatAmount: nullDec(), FreeShipping: sql.NullBool{Bool: true, Valid: true},
				Items: []entity.AcctOrderItemFact{
					{Id: 1, ProductId: 100, Quantity: dec("1"), UnitCost: nd("30.00")},
					{Id: 2, ProductId: 200, Quantity: dec("1"), UnitCost: nullDec()},
				},
			},
			vat: noVat(),
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc5010, entity.AcctSideDebit, "30.00") // only the costed line
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, "200")
			},
		},
		{
			name: "stripe captured fee books to 6050 without a method-model caveat",
			facts: entity.AcctOrderFacts{
				UUID: "stripe-fee", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD_TEST,
				PaymentFee: nd("3.00"), VatAmount: nullDec(), FreeShipping: sql.NullBool{Bool: true, Valid: true},
			},
			vat: noVat(),
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc6050, entity.AcctSideDebit, "3.00")
				assertAmount(t, e, Acc1030, entity.AcctSideCredit, "3.00")
				assert.False(t, e.HasCaveat)
			},
		},
		{
			name: "non-stripe modelled fee raises caveat",
			facts: entity.AcctOrderFacts{
				UUID: "fee", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nullDec(), PaymentMethodName: entity.BANK_INVOICE,
				FeePct: dec("1.90"), FeeFixed: dec("0.30"),
				VatAmount: nullDec(), FreeShipping: sql.NullBool{Bool: true, Valid: true},
			},
			vat: noVat(),
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				// G = total_price (non-stripe EUR, no settlement) = 100.00; fee = 100*1.90/100 + 0.30 = 2.20.
				assertAmount(t, e, Acc1040, entity.AcctSideDebit, "100.00") // gross booked to receivable
				assertAmount(t, e, Acc6050, entity.AcctSideDebit, "2.20")
				assertAmount(t, e, Acc1030, entity.AcctSideCredit, "2.20") // fee credited to the processor account (phase-1 behaviour)
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, "method model")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := BuildOrderSaleEntry(tt.facts, tt.vat, testOccurred)
			require.NoError(t, err)
			require.NoError(t, ValidateBalanced(e))
			tt.check(t, e)
		})
	}
}

// TestBuildOrderSaleEntry_ExportHasNoVATEvenWithRate proves export/wdt never post VAT even if a
// (defensive) rate leaks into the decision.
func TestBuildOrderSaleEntry_ExportHasNoVATEvenWithRate(t *testing.T) {
	f := entity.AcctOrderFacts{
		UUID: "exp", TotalPrice: dec("100.00"), Currency: "EUR",
		TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
		FreeShipping: sql.NullBool{Bool: true, Valid: true},
	}
	e, err := BuildOrderSaleEntry(f, VatDecision{Regime: entity.VatRegimeExport, RatePct: decimal.NewFromInt(23)}, testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	assert.False(t, hasLine(e, Acc2070, entity.AcctSideCredit))
	assertAmount(t, e, Acc4020, entity.AcctSideCredit, "100.00")
}
