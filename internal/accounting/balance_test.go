package accounting

import (
	"database/sql"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- shared test helpers -------------------------------------------------------------------------

var (
	testOccurred  = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	testStartDate = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

// dec parses a decimal literal (panics on bad input — test-only).
func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// nd is a valid NullDecimal from a literal; nullDec is the NULL one.
func nd(s string) decimal.NullDecimal { return decimal.NullDecimal{Decimal: dec(s), Valid: true} }
func nullDec() decimal.NullDecimal    { return decimal.NullDecimal{} }

// lineAmount returns the amount of the single (code, side) line, failing if it is absent or appears
// more than once.
func lineAmount(t *testing.T, e entity.AcctJournalEntryInsert, code string, side entity.AcctSide) decimal.Decimal {
	t.Helper()
	var found []decimal.Decimal
	for _, l := range e.Lines {
		if l.AccountCode == code && l.Side == side {
			found = append(found, l.Amount)
		}
	}
	require.Lenf(t, found, 1, "want exactly one %s %s line in %+v", side, code, e.Lines)
	return found[0]
}

// hasLine reports whether any (code, side) line exists.
func hasLine(e entity.AcctJournalEntryInsert, code string, side entity.AcctSide) bool {
	for _, l := range e.Lines {
		if l.AccountCode == code && l.Side == side {
			return true
		}
	}
	return false
}

// assertAmount checks a (code, side) line equals want (compared at 2dp).
func assertAmount(t *testing.T, e entity.AcctJournalEntryInsert, code string, side entity.AcctSide, want string) {
	t.Helper()
	assert.Equalf(t, want, lineAmount(t, e, code, side).StringFixed(2), "%s %s amount", side, code)
}

func randCents(r *rand.Rand, maxCents int) decimal.Decimal {
	return decimal.New(int64(r.Intn(maxCents)+1), -2)
}

// ndDec wraps a decimal as a valid NullDecimal.
func ndDec(d decimal.Decimal) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: d, Valid: true}
}

// --- ValidateBalanced unit tests -----------------------------------------------------------------

func TestValidateBalanced(t *testing.T) {
	tests := []struct {
		name    string
		lines   []entity.AcctJournalLineInsert
		wantErr bool
	}{
		{
			name: "balanced",
			lines: []entity.AcctJournalLineInsert{
				{AccountCode: Acc1030, Side: entity.AcctSideDebit, Amount: dec("100.00")},
				{AccountCode: Acc4020, Side: entity.AcctSideCredit, Amount: dec("100.00")},
			},
		},
		{
			name: "balanced multi-line",
			lines: []entity.AcctJournalLineInsert{
				{AccountCode: Acc1030, Side: entity.AcctSideDebit, Amount: dec("100.00")},
				{AccountCode: Acc4020, Side: entity.AcctSideCredit, Amount: dec("77.00")},
				{AccountCode: Acc2070, Side: entity.AcctSideCredit, Amount: dec("23.00")},
			},
		},
		{
			name:    "no lines",
			lines:   nil,
			wantErr: true,
		},
		{
			name: "unbalanced",
			lines: []entity.AcctJournalLineInsert{
				{AccountCode: Acc1030, Side: entity.AcctSideDebit, Amount: dec("100.00")},
				{AccountCode: Acc4020, Side: entity.AcctSideCredit, Amount: dec("99.99")},
			},
			wantErr: true,
		},
		{
			name: "non-positive amount",
			lines: []entity.AcctJournalLineInsert{
				{AccountCode: Acc1030, Side: entity.AcctSideDebit, Amount: dec("0")},
				{AccountCode: Acc4020, Side: entity.AcctSideCredit, Amount: dec("0")},
			},
			wantErr: true,
		},
		{
			name: "invalid side",
			lines: []entity.AcctJournalLineInsert{
				{AccountCode: Acc1030, Side: entity.AcctSide("sideways"), Amount: dec("1.00")},
				{AccountCode: Acc4020, Side: entity.AcctSideCredit, Amount: dec("1.00")},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBalanced(entity.AcctJournalEntryInsert{Lines: tt.lines})
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- property tests: every non-skipped entry balances, at any proportion ------------------------

func TestPropertyOrderSaleBalances(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	methods := []entity.PaymentMethodName{entity.CARD, entity.CARD_TEST, entity.CASH, entity.BANK_INVOICE}
	currencies := []string{"EUR", "USD"}
	for i := 0; i < 3000; i++ {
		f := randomOrderFacts(r, methods, currencies)
		entry, err := BuildOrderSaleEntry(f, randomVatDecision(r), testOccurred)
		if err != nil {
			require.Truef(t, isSaleSkip(err), "iter %d unexpected sale error: %v", i, err)
			continue
		}
		require.NoErrorf(t, ValidateBalanced(entry), "iter %d unbalanced sale: %+v", i, entry.Lines)
	}
}

// randomVatDecision picks a random regime and a random rate (0.00..27.99%) for the property test, so
// balancing is exercised across every regime and every inclusive extraction.
func randomVatDecision(r *rand.Rand) VatDecision {
	regimes := []entity.VatRegime{
		entity.VatRegimeOSS, entity.VatRegimePLDomestic, entity.VatRegimeExport,
		entity.VatRegimeWDT, entity.VatRegimeUKStockDomestic, entity.VatRegimeNone,
	}
	return VatDecision{
		Regime:  regimes[r.Intn(len(regimes))],
		RatePct: decimal.New(int64(r.Intn(2800)), -2),
	}
}

func TestPropertyRefundBalances(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	methods := []entity.PaymentMethodName{entity.CARD, entity.CARD_TEST, entity.CASH, entity.BANK_INVOICE}
	currencies := []string{"EUR", "USD"}
	for i := 0; i < 3000; i++ {
		f := randomOrderFacts(r, methods, currencies)
		refundedByItem := map[int]int64{}
		for _, it := range f.Items {
			if r.Intn(2) == 0 {
				refundedByItem[it.Id] = int64(r.Intn(2) + 1)
			}
		}
		refund := entity.AcctOrderRefundPayload{
			OrderUUID:      f.UUID,
			RefundAmount:   randCents(r, 30000),
			OrderCurrency:  f.Currency,
			RefundedByItem: refundedByItem,
		}
		entry, err := BuildOrderRefundEntry(f, refund, f.Items, f.UUID+":1", testOccurred)
		if err != nil {
			require.Truef(t, isSaleSkip(err), "iter %d unexpected refund error: %v", i, err)
			continue
		}
		require.NoErrorf(t, ValidateBalanced(entry), "iter %d unbalanced refund: %+v", i, entry.Lines)
	}
}

func TestPropertyMaterialBalances(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	types := []entity.MaterialMovementType{
		entity.MaterialMovementReceipt, entity.MaterialMovementReceiptProduction,
		entity.MaterialMovementIssueProduction, entity.MaterialMovementIssueSample,
		entity.MaterialMovementReturnProduction, entity.MaterialMovementReturnSample,
		entity.MaterialMovementWriteoff, entity.MaterialMovementAdjustment,
	}
	for i := 0; i < 3000; i++ {
		before := randCents(r, 10000)
		after := randCents(r, 10000)
		var cost decimal.NullDecimal
		if r.Intn(4) != 0 {
			cost = ndDec(randCents(r, 5000))
		}
		mt := types[r.Intn(len(types))]
		mm := entity.MaterialMovement{
			Id:           i + 1,
			MaterialId:   i%50 + 1,
			MovementType: mt,
			Quantity:     randCents(r, 5000),
			UnitCostBase: cost,
			OnHandBefore: before,
			OnHandAfter:  after,
			CreatedAt:    testOccurred,
		}
		// Occasionally a receipt records input VAT — exercise the extended M1 rule's balance too.
		if mt == entity.MaterialMovementReceipt && r.Intn(2) == 0 {
			regimes := []entity.InputVatRegime{
				entity.InputVatRegimeWNT, entity.InputVatRegimeImport,
				entity.InputVatRegimeDomesticPL, entity.InputVatRegimeDomesticUK,
			}
			mm.InputVatAmount = ndDec(randCents(r, 5000))
			mm.InputVatRegime = sql.NullString{String: string(regimes[r.Intn(len(regimes))]), Valid: true}
		}
		m := entity.AcctMovementFacts{MaterialMovement: mm, MaterialName: "material"}
		entry, err := BuildMaterialMovementEntry(m, testStartDate)
		if err != nil {
			require.Truef(t, errors.Is(err, ErrSkipUncosted), "iter %d unexpected material error: %v", i, err)
			continue
		}
		require.NoErrorf(t, ValidateBalanced(entry), "iter %d unbalanced movement: %+v", i, entry.Lines)
	}
}

func TestPropertyProductionBalances(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	for i := 0; i < 3000; i++ {
		var costs []entity.ProductionRunCost
		for j := 0; j < r.Intn(3); j++ {
			c := entity.ProductionRunCost{Kind: entity.ProductionRunCostOther}
			if r.Intn(4) != 0 {
				c.AmountBase = ndDec(randCents(r, 20000))
			}
			costs = append(costs, c)
		}

		// Random mix of issues/returns, costed/uncosted, pre- and post-cutover — the derived ledger
		// WIP can come out negative when returns (costed) outrun issues; that's intentional, to
		// exercise the negative-FG guard.
		var issues []entity.AcctRunIssueFact
		for j := 0; j < r.Intn(4); j++ {
			mt := entity.MaterialMovementIssueProduction
			if r.Intn(2) == 0 {
				mt = entity.MaterialMovementReturnProduction
			}
			createdAt := testOccurred
			if r.Intn(5) == 0 {
				createdAt = testStartDate.AddDate(0, 0, -1) // pre-cutover
			}
			iss := entity.AcctRunIssueFact{
				MovementType: mt,
				Quantity:     randCents(r, 5000),
				CreatedAt:    createdAt,
			}
			if r.Intn(4) != 0 {
				iss.UnitCostBase = ndDec(randCents(r, 5000))
			}
			issues = append(issues, iss)
		}

		run := entity.AcctRunFacts{
			RunID:        i + 1,
			TechCardName: "tc",
			ReceivedAt:   testOccurred,
			Costs:        costs,
			Issues:       issues,
		}
		entry, err := BuildProductionReceiveEntry(run, testStartDate)
		if err != nil {
			require.Truef(t, errors.Is(err, ErrSkipEmpty), "iter %d unexpected production error: %v", i, err)
			continue
		}
		require.NoErrorf(t, ValidateBalanced(entry), "iter %d unbalanced receive: %+v", i, entry.Lines)
	}
}

func TestPropertyOpexBalances(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	cats := []string{"salaries", "rent", "software", "marketing_other", "production_content",
		"taxes", "bank_fees", "professional_services", "logistics_office", "other", "mystery_new"}
	month := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3000; i++ {
		var sums []entity.AcctOpexCategorySum
		for j := 0; j < r.Intn(6); j++ {
			amount := decimal.Zero
			if r.Intn(4) != 0 {
				amount = randCents(r, 100000)
			}
			sums = append(sums, entity.AcctOpexCategorySum{
				Category:   cats[r.Intn(len(cats))],
				AmountBase: amount,
			})
		}
		entry, err := BuildOpexMonthEntry(month, sums, r.Intn(3)+1)
		if err != nil {
			require.Truef(t, errors.Is(err, ErrSkipEmpty), "iter %d unexpected opex error: %v", i, err)
			continue
		}
		require.NoErrorf(t, ValidateBalanced(entry), "iter %d unbalanced opex: %+v", i, entry.Lines)
	}
}

// randomOrderFacts builds a random but internally-consistent order fact for the property tests.
func randomOrderFacts(r *rand.Rand, methods []entity.PaymentMethodName, currencies []string) entity.AcctOrderFacts {
	totalCents := r.Intn(100000) + 1
	totalPrice := decimal.New(int64(totalCents), -2)
	var settled decimal.NullDecimal
	if r.Intn(2) == 0 {
		settled = ndDec(randCents(r, 100000))
	}
	var vat decimal.NullDecimal
	if r.Intn(3) != 0 {
		// VAT no larger than total_price (an inclusive snapshot); occasionally equal, to hit the guard.
		vat = ndDec(decimal.New(int64(r.Intn(totalCents+1)), -2))
	}
	var ship decimal.NullDecimal
	if r.Intn(2) == 0 {
		ship = ndDec(randCents(r, 20000))
	}
	var fee decimal.NullDecimal
	if r.Intn(2) == 0 {
		fee = ndDec(randCents(r, 2000))
	}
	var items []entity.AcctOrderItemFact
	for j := 0; j < r.Intn(4); j++ {
		var uc decimal.NullDecimal
		if r.Intn(4) != 0 {
			uc = ndDec(randCents(r, 20000))
		}
		items = append(items, entity.AcctOrderItemFact{
			Id:        j + 1,
			ProductId: 100 + j,
			Quantity:  decimal.NewFromInt(int64(r.Intn(3) + 1)),
			UnitCost:  uc,
		})
	}
	// FeePct/FeeFixed feed the non-Stripe estimated-fee path (orderFee); occasionally left at zero,
	// like a method with no fee model configured.
	feePct := decimal.Zero
	feeFixed := decimal.Zero
	if r.Intn(2) == 0 {
		feePct = decimal.New(int64(r.Intn(300)), -2)  // 0.00 .. 2.99 %
		feeFixed = decimal.New(int64(r.Intn(50)), -2) // 0.00 .. 0.49
	}
	// Occasionally a B2B order (buyer VAT id present) so the 4310 wholesale-revenue routing is exercised.
	var buyerVatID sql.NullString
	if r.Intn(3) == 0 {
		buyerVatID = sql.NullString{String: "VAT123", Valid: true}
	}
	return entity.AcctOrderFacts{
		UUID:              "order-uuid",
		TotalPrice:        totalPrice,
		Currency:          currencies[r.Intn(len(currencies))],
		TotalSettledBase:  settled,
		PaymentFee:        fee,
		FeePct:            feePct,
		FeeFixed:          feeFixed,
		VatAmount:         vat,
		PaymentMethodName: methods[r.Intn(len(methods))],
		ShipmentCost:      ship,
		FreeShipping:      sql.NullBool{Bool: r.Intn(2) == 0, Valid: true},
		BuyerVatID:        buyerVatID,
		Items:             items,
	}
}

func isSaleSkip(err error) bool {
	return errors.Is(err, ErrNotReady) ||
		errors.Is(err, ErrSkipNonEUR) ||
		errors.Is(err, ErrDegenerateAmounts)
}
