package accounting

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	e, err := BuildOrderRefundEntry(f, refund, f.Items, "order-250:1", testOccurred)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceOrderRefund, e.SourceType)
	assert.Equal(t, "order-250:1", e.SourceKey)
	assert.False(t, e.HasCaveat)

	assertAmount(t, e, Acc4040, entity.AcctSideDebit, "203.25")  // contra-revenue (NETr)
	assertAmount(t, e, Acc2070, entity.AcctSideDebit, "46.75")   // VAT reversal
	assertAmount(t, e, Acc1030, entity.AcctSideCredit, "250.00") // money back to processor
	assertAmount(t, e, Acc1130, entity.AcctSideDebit, "84.50")   // stock back
	assertAmount(t, e, Acc5050, entity.AcctSideCredit, "84.50")  // contra-COGS

	// The fee is deliberately NOT reversed.
	assert.False(t, hasLine(e, Acc6050, entity.AcctSideDebit))
}

func TestBuildOrderRefundEntry_Cases(t *testing.T) {
	tests := []struct {
		name   string
		facts  entity.AcctOrderFacts
		refund entity.AcctOrderRefundPayload
		items  []entity.AcctOrderItemFact
		check  func(t *testing.T, e entity.AcctJournalEntryInsert)
	}{
		{
			name:  "partial money refund without item return",
			facts: saleFacts(entity.CARD),
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
			name: "VAT portion exceeding the refund is dropped",
			facts: entity.AcctOrderFacts{
				UUID: "bad-vat", TotalPrice: dec("100.00"), Currency: "EUR",
				TotalSettledBase: nd("100.00"), VatAmount: nd("200.00"), PaymentMethodName: entity.CARD,
			},
			refund: entity.AcctOrderRefundPayload{
				OrderUUID: "bad-vat", RefundAmount: dec("100.00"), OrderCurrency: "EUR",
			},
			check: func(t *testing.T, e entity.AcctJournalEntryInsert) {
				assertAmount(t, e, Acc4040, entity.AcctSideDebit, "100.00")
				assert.False(t, hasLine(e, Acc2070, entity.AcctSideDebit))
				assert.True(t, e.HasCaveat)
				assert.Contains(t, e.Caveat.String, "vat exceeds refund")
			},
		},
		{
			name:  "uncosted refunded item understates COGS return",
			facts: saleFacts(entity.CARD),
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := tt.items
			if items == nil {
				items = tt.facts.Items
			}
			e, err := BuildOrderRefundEntry(tt.facts, tt.refund, items, "sk:1", testOccurred)
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
			_, err := BuildOrderRefundEntry(tt.facts, tt.refund, tt.facts.Items, "sk:1", testOccurred)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}
