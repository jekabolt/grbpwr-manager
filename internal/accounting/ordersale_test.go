package accounting

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildOrderSaleEntry_Walkthrough reproduces the end-to-end example from
// docs/plan-accounting/04: a 250 EUR website order, VAT 23% inclusive (snapshot 46.75), shipping
// 10, Stripe fee 7.55, one unit at cost 84.50 -> NET 193.25.
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

	e, err := BuildOrderSaleEntry(f, testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceOrderSale, e.SourceType)
	assert.Equal(t, "order-250", e.SourceKey)
	assert.Equal(t, testOccurred, e.OccurredAt)
	assert.False(t, e.HasCaveat)

	assertAmount(t, e, Acc1030, entity.AcctSideDebit, "250.00")  // money on the processor
	assertAmount(t, e, Acc4020, entity.AcctSideCredit, "193.25") // NET revenue (DTC)
	assertAmount(t, e, Acc4110, entity.AcctSideCredit, "10.00")  // shipping income
	assertAmount(t, e, Acc2070, entity.AcctSideCredit, "46.75")  // VAT
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
			_, err := BuildOrderSaleEntry(tt.facts, testOccurred)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestBuildOrderSaleEntry_Cases(t *testing.T) {
	tests := []struct {
		name  string
		facts entity.AcctOrderFacts
		check func(t *testing.T, e entity.AcctJournalEntryInsert)
	}{
		{
			name: "cash EUR no settled uses total_price, 1010/4010, vat-null caveat",
			facts: entity.AcctOrderFacts{
				UUID: "cash-1", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nullDec(), PaymentMethodName: entity.CASH,
				VatAmount: nullDec(), FreeShipping: sql.NullBool{Bool: true, Valid: true},
				Items: []entity.AcctOrderItemFact{{Id: 1, ProductId: 5, Quantity: dec("1"), UnitCost: nd("30.00")}},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc1010, entity.AcctSideDebit, "100.00")
				assertAmount(t, e, Acc4010, entity.AcctSideCredit, "100.00")
				assertAmount(t, e, Acc5010, entity.AcctSideDebit, "30.00")
				assertAmount(t, e, Acc1130, entity.AcctSideCredit, "30.00")
				assert.False(t, hasLine(e, Acc2070, entity.AcctSideCredit), "no VAT line")
				assert.False(t, hasLine(e, Acc4110, entity.AcctSideCredit), "no shipping line")
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, "vat_amount is null")
			},
		},
		{
			name: "bank-invoice EUR books to 1040 receivable",
			facts: entity.AcctOrderFacts{
				UUID: "bi-1", TotalPrice: dec("200.00"), Currency: "EUR",
				TotalSettledBase: nullDec(), PaymentMethodName: entity.BANK_INVOICE,
				VatAmount: nd("40.00"), ShipmentCost: nd("15.00"), FreeShipping: sql.NullBool{Bool: false, Valid: true},
				Items: []entity.AcctOrderItemFact{{Id: 1, ProductId: 5, Quantity: dec("1"), UnitCost: nd("60.00")}},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc1040, entity.AcctSideDebit, "200.00")
				assertAmount(t, e, Acc4020, entity.AcctSideCredit, "145.00")
				assertAmount(t, e, Acc4110, entity.AcctSideCredit, "15.00")
				assertAmount(t, e, Acc2070, entity.AcctSideCredit, "40.00")
				assert.False(t, e.HasCaveat)
			},
		},
		{
			name: "settled differs from total_price scales every component by k=0.5",
			facts: entity.AcctOrderFacts{
				UUID: "k-1", TotalPrice: dec("200.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
				VatAmount: nd("46.00"), ShipmentCost: nd("20.00"), FreeShipping: sql.NullBool{Bool: false, Valid: true},
				Items: []entity.AcctOrderItemFact{{Id: 1, ProductId: 5, Quantity: dec("1"), UnitCost: nd("40.00")}},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc1030, entity.AcctSideDebit, "100.00") // G = settled
				assertAmount(t, e, Acc2070, entity.AcctSideCredit, "23.00") // 46 * 0.5
				assertAmount(t, e, Acc4110, entity.AcctSideCredit, "10.00") // 20 * 0.5
				assertAmount(t, e, Acc4020, entity.AcctSideCredit, "67.00") // 100 - 23 - 10
				assertAmount(t, e, Acc5010, entity.AcctSideDebit, "40.00")  // COGS uses snapshot, not k
			},
		},
		{
			name: "VAT exceeds gross is dropped with caveat",
			facts: entity.AcctOrderFacts{
				UUID: "vatbig", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
				VatAmount: nd("150.00"), FreeShipping: sql.NullBool{Bool: true, Valid: true},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc1030, entity.AcctSideDebit, "100.00")
				assertAmount(t, e, Acc4020, entity.AcctSideCredit, "100.00")
				assert.False(t, hasLine(e, Acc2070, entity.AcctSideCredit))
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, "vat exceeds gross")
			},
		},
		{
			name: "shipping exceeds post-VAT remainder is dropped with caveat",
			facts: entity.AcctOrderFacts{
				UUID: "shipbig", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), PaymentMethodName: entity.CARD,
				VatAmount: nd("10.00"), ShipmentCost: nd("95.00"), FreeShipping: sql.NullBool{Bool: false, Valid: true},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc4020, entity.AcctSideCredit, "90.00") // 100 - 10
				assertAmount(t, e, Acc2070, entity.AcctSideCredit, "10.00")
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
				PaymentFee: nd("3.00"), VatAmount: nd("0"), FreeShipping: sql.NullBool{Bool: true, Valid: true},
			},
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
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				// G = total_price (non-stripe EUR, no settlement) = 100.00;
				// fee = 100.00 * 1.90/100 + 0.30 = 2.20.
				assertAmount(t, e, Acc6050, entity.AcctSideDebit, "2.20")
				assertAmount(t, e, Acc1030, entity.AcctSideCredit, "2.20")
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, "method model")
			},
		},
		{
			name: "non-stripe with no fee model posts no fee line",
			facts: entity.AcctOrderFacts{
				UUID: "nofee", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nullDec(), PaymentMethodName: entity.CASH,
				VatAmount: nullDec(), FreeShipping: sql.NullBool{Bool: true, Valid: true},
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assert.False(t, hasLine(e, Acc6050, entity.AcctSideDebit), "no fee line")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := BuildOrderSaleEntry(tt.facts, testOccurred)
			require.NoError(t, err)
			require.NoError(t, ValidateBalanced(e))
			tt.check(t, e)
		})
	}
}
