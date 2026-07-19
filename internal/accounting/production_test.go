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
	e, err := BuildProductionReceiveEntry(r, testStartDate)
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
		e, err := BuildProductionReceiveEntry(r, testStartDate)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assert.False(t, hasLine(e, Acc2010, entity.AcctSideCredit))
		assertAmount(t, e, Acc1130, entity.AcctSideDebit, "100.00")
		assertAmount(t, e, Acc1120, entity.AcctSideCredit, "100.00")
	})

	t.Run("nothing costed skips", func(t *testing.T) {
		r := entity.AcctRunFacts{RunID: 2, ReceivedAt: testOccurred}
		_, err := BuildProductionReceiveEntry(r, testStartDate)
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
		e, err := BuildProductionReceiveEntry(r, testStartDate)
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
		e, err := BuildProductionReceiveEntry(r, testStartDate)
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
		e, err := BuildProductionReceiveEntry(r, testStartDate)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assertAmount(t, e, Acc1120, entity.AcctSideDebit, "50.00")  // manual
		assertAmount(t, e, Acc2010, entity.AcctSideCredit, "50.00")
		assertAmount(t, e, Acc1130, entity.AcctSideDebit, "70.00")  // FG = 50 manual + 20 ledger WIP, not 350
		assertAmount(t, e, Acc1120, entity.AcctSideCredit, "70.00")
		assert.True(t, e.HasCaveat)
		assert.Contains(t, e.Caveat.String, "pre-cutover WIP excluded")
		assert.NotContains(t, e.Caveat.String, "uncosted material issues")
		assert.NotContains(t, e.Caveat.String, "manual cost line")
	})
}
