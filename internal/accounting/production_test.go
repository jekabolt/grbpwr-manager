package accounting

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildProductionReceiveEntry_Walkthrough reproduces the receive step of the 04 example:
// manual costs 230 (CMT 200 + overhead 30), ledger WIP 192.50 (fabric 180 + hardware 12.50),
// FG 422.50.
func TestBuildProductionReceiveEntry_Walkthrough(t *testing.T) {
	r := entity.AcctRunFacts{
		RunID:        7,
		TechCardName: "hoodie",
		ReceivedAt:   testOccurred,
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostCMT, AmountBase: nd("200.00")},
			{Kind: entity.ProductionRunCostOther, AmountBase: nd("30.00")},
		},
		Issues: []entity.AcctRunIssueFact{
			{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: nd("180.00"), CreatedAt: testOccurred},
			{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: nd("12.50"), CreatedAt: testOccurred},
		},
	}
	e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceProductionReceive, e.SourceType)
	assert.Equal(t, "7", e.SourceKey)
	assert.Equal(t, testOccurred, e.OccurredAt)
	assert.False(t, e.HasCaveat)

	assertAmount(t, e, Acc1120, entity.AcctSideDebit, "230.00")  // manual into WIP
	assertAmount(t, e, Acc2010, entity.AcctSideCredit, "230.00") // against AP
	assertAmount(t, e, Acc1130, entity.AcctSideDebit, "422.50")  // WIP -> finished goods
	assertAmount(t, e, Acc1120, entity.AcctSideCredit, "422.50")
}

func TestBuildProductionReceiveEntry_Cases(t *testing.T) {
	t.Run("no manual costs posts only the FG transfer", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 1, ReceivedAt: testOccurred,
			Issues: []entity.AcctRunIssueFact{
				{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: nd("100.00"), CreatedAt: testOccurred},
			},
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assert.False(t, hasLine(e, Acc2010, entity.AcctSideCredit))
		assertAmount(t, e, Acc1130, entity.AcctSideDebit, "100.00")
		assertAmount(t, e, Acc1120, entity.AcctSideCredit, "100.00")
	})

	t.Run("nothing costed skips", func(t *testing.T) {
		r := entity.AcctRunFacts{RunID: 2, ReceivedAt: testOccurred}
		_, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		assert.ErrorIs(t, err, ErrSkipEmpty)
	})

	t.Run("caveats surfaced", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 3, ReceivedAt: testOccurred,
			Costs: []entity.ProductionRunCost{
				{Kind: entity.ProductionRunCostCMT, AmountBase: nd("50.00")},
				{Kind: entity.ProductionRunCostOther, AmountBase: nullDec()},
				{Kind: entity.ProductionRunCostOther, AmountBase: nullDec()},
			},
			Issues: []entity.AcctRunIssueFact{
				{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: nd("100.00"), CreatedAt: testOccurred},
				{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: nd("999.00"), CreatedAt: testStartDate.AddDate(0, 0, -1)},
				{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: nullDec(), CreatedAt: testOccurred},
			},
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assert.True(t, e.HasCaveat)
		assert.Contains(t, e.Caveat.String, "uncosted material issues")
		assert.Contains(t, e.Caveat.String, "manual cost line")
		assert.Contains(t, e.Caveat.String, "pre-cutover WIP excluded")
	})

	t.Run("negative FG keeps manual posting and flags", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 4, ReceivedAt: testOccurred,
			Costs: []entity.ProductionRunCost{{Kind: entity.ProductionRunCostCMT, AmountBase: nd("50.00")}},
			Issues: []entity.AcctRunIssueFact{
				{MovementType: entity.MaterialMovementReturnProduction, Quantity: dec("1"), UnitCostBase: nd("200.00"), CreatedAt: testOccurred},
			},
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assertAmount(t, e, Acc1120, entity.AcctSideDebit, "50.00")
		assertAmount(t, e, Acc2010, entity.AcctSideCredit, "50.00")
		assert.False(t, hasLine(e, Acc1130, entity.AcctSideDebit), "no FG transfer")
		assert.True(t, e.HasCaveat)
		assert.Contains(t, e.Caveat.String, "non-positive finished-goods")
	})

	t.Run("pre-cutover issue excluded from ledger WIP even when costed", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 5, ReceivedAt: testOccurred,
			Costs: []entity.ProductionRunCost{{Kind: entity.ProductionRunCostCMT, AmountBase: nd("50.00")}},
			Issues: []entity.AcctRunIssueFact{
				// pre-cutover: costed, but excluded from ledger WIP entirely (would be 300 if counted).
				{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: nd("300.00"), CreatedAt: testStartDate.AddDate(0, 0, -1)},
				// post-cutover: the only issue that counts.
				{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: nd("20.00"), CreatedAt: testOccurred},
			},
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assertAmount(t, e, Acc1120, entity.AcctSideDebit, "50.00") // manual
		assertAmount(t, e, Acc2010, entity.AcctSideCredit, "50.00")
		assertAmount(t, e, Acc1130, entity.AcctSideDebit, "70.00") // FG = 50 manual + 20 ledger WIP, not 350
		assertAmount(t, e, Acc1120, entity.AcctSideCredit, "70.00")
		assert.True(t, e.HasCaveat)
		assert.Contains(t, e.Caveat.String, "pre-cutover WIP excluded")
		assert.NotContains(t, e.Caveat.String, "uncosted material issues")
		assert.NotContains(t, e.Caveat.String, "manual cost line")
	})
}

// Phase 4 (receipt v1): the receipt is the accounting unit — its id keys the entry — and an
// all-scrap receipt (zero good units) must NOT transfer WIP to finished goods.
func TestBuildProductionReceiveEntry_ReceiptKeysAndAllScrap(t *testing.T) {
	t.Run("receipt id keys the entry, versioned on re-post", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 42, GoodQtyTotal: 10, ReceivedAt: testOccurred,
			Costs: []entity.ProductionRunCost{{Kind: entity.ProductionRunCostCMT, AmountBase: nd("100.00")}},
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		assert.Equal(t, "receipt:42", e.SourceKey)
		e2, err := BuildProductionReceiveEntry(r, testStartDate, 3)
		require.NoError(t, err)
		assert.Equal(t, "receipt:42:v3", e2.SourceKey)
	})

	t.Run("legacy facts without a receipt keep the run-id family", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 7, ReceivedAt: testOccurred,
			Costs: []entity.ProductionRunCost{{Kind: entity.ProductionRunCostCMT, AmountBase: nd("100.00")}},
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 2)
		require.NoError(t, err)
		assert.Equal(t, "7:v2", e.SourceKey)
	})

	t.Run("all-scrap capitalises manual cost but leaves WIP in place", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 42, GoodQtyTotal: 0, DefectQtyTotal: 9, ReceivedAt: testOccurred,
			Costs: []entity.ProductionRunCost{{Kind: entity.ProductionRunCostCMT, AmountBase: nd("100.00")}},
			Issues: []entity.AcctRunIssueFact{
				{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("2"), UnitCostBase: nd("50.00"), CreatedAt: testOccurred},
			},
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		// The CMT invoice is owed either way: Dr WIP / Cr AP stays.
		assertAmount(t, e, Acc1120, entity.AcctSideDebit, "100.00")
		assertAmount(t, e, Acc2010, entity.AcctSideCredit, "100.00")
		// But nothing moved to finished goods — there are no finished goods.
		assert.False(t, hasLine(e, Acc1130, entity.AcctSideDebit), "no FG transfer on all-scrap")
		assert.True(t, e.HasCaveat, "the stranded WIP is flagged for the write-off phase")
	})

	t.Run("all-scrap with nothing costed still skips", func(t *testing.T) {
		r := entity.AcctRunFacts{RunID: 7, ReceiptID: 42, GoodQtyTotal: 0, DefectQtyTotal: 9, ReceivedAt: testOccurred}
		_, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.ErrorIs(t, err, ErrSkipEmpty)
	})

	t.Run("zero-line legacy backfill is NOT scrap: FG transfer posts as before receipts", func(t *testing.T) {
		// A 0231 backfill of a run whose grid was edited back to NULLs: a receipt with no counted
		// lines at all. Base (pre-receipt) posting transferred WIP→FG for that run; treating the
		// missing counts as "all scrap" would silently strand the cost in WIP.
		r := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 42, GoodQtyTotal: 0, DefectQtyTotal: 0, ReceivedAt: testOccurred,
			Issues: []entity.AcctRunIssueFact{
				{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("2"), UnitCostBase: nd("50.00"), CreatedAt: testOccurred},
			},
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assert.True(t, hasLine(e, Acc1130, entity.AcctSideDebit), "WIP→FG posts for a zero-line legacy receipt")
	})
}

// TestBuildProductionReceiveEntry_PartialReceipts pins the Phase 5 arithmetic: manual capitalises
// once (delta vs siblings), each partial relieves the current WIP balance pro-rata against the
// units still expected, and the FINAL receipt trues the run's good-unit share up exactly — Σ FG
// over the receipts lands on TOTAL_WIP × good ÷ received to the cent, rounding residue never
// strands on 1120, and the defect share deliberately stays there.
func TestBuildProductionReceiveEntry_PartialReceipts(t *testing.T) {
	wip100 := []entity.AcctRunIssueFact{
		{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: nd("100.00"), CreatedAt: testOccurred},
	}

	t.Run("three equal partial-partial-final receipts split 100.00 exactly", func(t *testing.T) {
		r1 := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 51, ReceivedAt: testOccurred, Issues: wip100,
			GoodQtyTotal: 1, AllGoodQty: 1, AllReceivedQty: 1, PlannedQtyTotal: 3,
		}
		e1, err := BuildProductionReceiveEntry(r1, testStartDate, 1)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e1))
		assertAmount(t, e1, Acc1130, entity.AcctSideDebit, "33.33") // 100 × 1/3

		r2 := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 52, ReceivedAt: testOccurred, Issues: wip100,
			GoodQtyTotal: 1, AllGoodQty: 2, AllReceivedQty: 2, PlannedQtyTotal: 3,
			OtherPostedFGBase: dec("33.33"),
		}
		e2, err := BuildProductionReceiveEntry(r2, testStartDate, 1)
		require.NoError(t, err)
		assertAmount(t, e2, Acc1130, entity.AcctSideDebit, "33.34") // (100−33.33) × 1/2

		r3 := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 53, ReceivedAt: testOccurred, Issues: wip100, IsFinal: true,
			GoodQtyTotal: 1, AllGoodQty: 3, AllReceivedQty: 3, PlannedQtyTotal: 3,
			OtherPostedFGBase: dec("66.67"),
		}
		e3, err := BuildProductionReceiveEntry(r3, testStartDate, 1)
		require.NoError(t, err)
		assertAmount(t, e3, Acc1130, entity.AcctSideDebit, "33.33") // true-up: 100.00 − 66.67
	})

	t.Run("mixed FINAL leaves the defect share in WIP", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 60, ReceivedAt: testOccurred, Issues: wip100, IsFinal: true,
			GoodQtyTotal: 8, DefectQtyTotal: 2, AllGoodQty: 8, AllReceivedQty: 10, PlannedQtyTotal: 10,
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		assertAmount(t, e, Acc1130, entity.AcctSideDebit, "80.00")
		assert.True(t, e.HasCaveat == false, "good-share transfer is the rule, not a caveat")
	})

	t.Run("all-scrap partial capitalises the manual delta only", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 61, ReceivedAt: testOccurred, Issues: wip100,
			GoodQtyTotal: 0, DefectQtyTotal: 5, AllGoodQty: 0, AllReceivedQty: 5, PlannedQtyTotal: 10,
			Costs: []entity.ProductionRunCost{{Kind: entity.ProductionRunCostCMT, AmountBase: nd("50.00")}},
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		assertAmount(t, e, Acc1120, entity.AcctSideDebit, "50.00")
		assertAmount(t, e, Acc2010, entity.AcctSideCredit, "50.00")
		assert.False(t, hasLine(e, Acc1130, entity.AcctSideDebit), "no finished goods exist")
	})

	t.Run("manual capitalises once: the sibling-posted total is never re-booked", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 62, ReceivedAt: testOccurred, Issues: wip100, IsFinal: true,
			GoodQtyTotal: 2, AllGoodQty: 3, AllReceivedQty: 3, PlannedQtyTotal: 3,
			Costs:                 []entity.ProductionRunCost{{Kind: entity.ProductionRunCostCMT, AmountBase: nd("100.00")}},
			OtherPostedManualBase: dec("100.00"),
			OtherPostedFGBase:     dec("66.66"),
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		assert.False(t, hasLine(e, Acc1120, entity.AcctSideDebit), "manual already capitalised by a sibling")
		// TOTAL_WIP = 100 issues + 100 manual = 200; target = 200 × 3/3 = 200; true-up = 200 − 66.66.
		assertAmount(t, e, Acc1130, entity.AcctSideDebit, "133.34")
	})

	t.Run("a late partial after the final trued up posts nothing", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 63, ReceivedAt: testOccurred, Issues: wip100,
			GoodQtyTotal: 1, AllGoodQty: 3, AllReceivedQty: 3, PlannedQtyTotal: 3,
			OtherPostedFGBase: dec("100.00"),
		}
		_, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.ErrorIs(t, err, ErrSkipEmpty)
	})

	t.Run("over-plan delivery keeps the ratio at one, never above", func(t *testing.T) {
		r := entity.AcctRunFacts{
			RunID: 7, ReceiptID: 64, ReceivedAt: testOccurred, Issues: wip100,
			GoodQtyTotal: 5, AllGoodQty: 5, AllReceivedQty: 5, PlannedQtyTotal: 3,
		}
		e, err := BuildProductionReceiveEntry(r, testStartDate, 1)
		require.NoError(t, err)
		// remaining = max(3−0, 5) = 5 → fg = 100 × 5/5, capped by the good-share target (100).
		assertAmount(t, e, Acc1130, entity.AcctSideDebit, "100.00")
	})
}
