package accounting

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func movementFacts(mt entity.MaterialMovementType) entity.AcctMovementFacts {
	return entity.AcctMovementFacts{
		MaterialMovement: entity.MaterialMovement{
			Id:           42,
			MaterialId:   7,
			MovementType: mt,
			Quantity:     dec("10"),
			UnitCostBase: nd("18.00"), // V = 180.00
			OnHandBefore: dec("5"),
			OnHandAfter:  dec("8"), // positive delta for the adjustment row
			CreatedAt:    testOccurred,
		},
		MaterialName: "cotton twill",
	}
}

func TestBuildMaterialMovementEntry_Types(t *testing.T) {
	tests := []struct {
		name       string
		mt         entity.MaterialMovementType
		before     string
		after      string
		wantDr     string
		wantCr     string
		wantSource entity.AcctSourceType
	}{
		{"M1 receipt", entity.MaterialMovementReceipt, "0", "10", Acc1110, Acc2010, entity.AcctSourceMaterialReceipt},
		{"M2 receipt_production", entity.MaterialMovementReceiptProduction, "0", "10", Acc1110, Acc1120, entity.AcctSourceMaterialReceipt},
		{"M3 issue_production", entity.MaterialMovementIssueProduction, "10", "0", Acc1120, Acc1110, entity.AcctSourceMaterialIssue},
		{"M4 issue_sample", entity.MaterialMovementIssueSample, "10", "0", Acc6210, Acc1110, entity.AcctSourceMaterialIssue},
		{"M5 return_production", entity.MaterialMovementReturnProduction, "0", "10", Acc1110, Acc1120, entity.AcctSourceMaterialReturn},
		{"M6 return_sample", entity.MaterialMovementReturnSample, "0", "10", Acc1110, Acc6210, entity.AcctSourceMaterialReturn},
		{"M7 writeoff", entity.MaterialMovementWriteoff, "10", "0", Acc5040, Acc1110, entity.AcctSourceMaterialWriteoff},
		{"M8 adjustment gain", entity.MaterialMovementAdjustment, "5", "8", Acc1110, Acc5090, entity.AcctSourceMaterialAdjustment},
		{"M8 adjustment loss", entity.MaterialMovementAdjustment, "8", "5", Acc5090, Acc1110, entity.AcctSourceMaterialAdjustment},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := movementFacts(tt.mt)
			m.OnHandBefore = dec(tt.before)
			m.OnHandAfter = dec(tt.after)

			e, err := BuildMaterialMovementEntry(m, testStartDate)
			require.NoError(t, err)
			require.NoError(t, ValidateBalanced(e))

			assert.Equal(t, tt.wantSource, e.SourceType)
			assert.Equal(t, "42", e.SourceKey)
			assertAmount(t, e, tt.wantDr, entity.AcctSideDebit, "180.00")
			assertAmount(t, e, tt.wantCr, entity.AcctSideCredit, "180.00")
		})
	}
}

func TestBuildMaterialMovementEntry_Skips(t *testing.T) {
	t.Run("uncosted movement", func(t *testing.T) {
		m := movementFacts(entity.MaterialMovementReceipt)
		m.UnitCostBase = nullDec()
		_, err := BuildMaterialMovementEntry(m, testStartDate)
		assert.ErrorIs(t, err, ErrSkipUncosted)
	})
	t.Run("zero value", func(t *testing.T) {
		m := movementFacts(entity.MaterialMovementReceipt)
		m.Quantity = dec("0")
		_, err := BuildMaterialMovementEntry(m, testStartDate)
		assert.ErrorIs(t, err, ErrSkipUncosted)
	})
	t.Run("adjustment with zero delta", func(t *testing.T) {
		m := movementFacts(entity.MaterialMovementAdjustment)
		m.OnHandBefore = dec("5")
		m.OnHandAfter = dec("5")
		_, err := BuildMaterialMovementEntry(m, testStartDate)
		assert.ErrorIs(t, err, ErrSkipUncosted)
	})
	t.Run("unknown movement type", func(t *testing.T) {
		m := movementFacts(entity.MaterialMovementType("teleport"))
		_, err := BuildMaterialMovementEntry(m, testStartDate)
		assert.ErrorIs(t, err, ErrUnknownMovementType)
	})
}

func TestBuildMaterialMovementEntry_OccurredAtClamp(t *testing.T) {
	after := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		occurredAt sql.NullTime
		createdAt  time.Time
		want       time.Time
	}{
		{"occurred after start is kept", sql.NullTime{Time: after, Valid: true}, testOccurred, after},
		{"occurred before start clamps up", sql.NullTime{Time: before, Valid: true}, testOccurred, testStartDate},
		{"null occurred falls back to created", sql.NullTime{}, after, after},
		{"null occurred with early created clamps up", sql.NullTime{}, before, testStartDate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := movementFacts(entity.MaterialMovementReceipt)
			m.OccurredAt = tt.occurredAt
			m.CreatedAt = tt.createdAt
			e, err := BuildMaterialMovementEntry(m, testStartDate)
			require.NoError(t, err)
			assert.Equal(t, tt.want, e.OccurredAt)
		})
	}
}

func TestBuildMaterialMovementEntry_Description(t *testing.T) {
	m := movementFacts(entity.MaterialMovementWriteoff)
	m.Reason = sql.NullString{String: "damage", Valid: true}
	m.Comment = sql.NullString{String: "water leak", Valid: true}
	e, err := BuildMaterialMovementEntry(m, testStartDate)
	require.NoError(t, err)
	assert.Equal(t, "cotton twill — damage — water leak", e.Description)
}

// inputVAT sets the input-VAT fields on a receipt movement (V = 180.00 from movementFacts).
func inputVAT(m entity.AcctMovementFacts, amount string, regime entity.InputVatRegime) entity.AcctMovementFacts {
	m.InputVatAmount = nd(amount)
	m.InputVatRegime = sql.NullString{String: string(regime), Valid: true}
	return m
}

func TestBuildMaterialMovementEntry_InputVAT(t *testing.T) {
	t.Run("domestic_pl: NET to 1110, VAT to 2080, GROSS to 2010", func(t *testing.T) {
		m := inputVAT(movementFacts(entity.MaterialMovementReceipt), "41.40", entity.InputVatRegimeDomesticPL)
		e, err := BuildMaterialMovementEntry(m, testStartDate)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assert.Equal(t, entity.AcctSourceMaterialReceipt, e.SourceType)
		assertAmount(t, e, Acc1110, entity.AcctSideDebit, "180.00")
		assertAmount(t, e, Acc2080, entity.AcctSideDebit, "41.40")
		assertAmount(t, e, Acc2010, entity.AcctSideCredit, "221.40")
		assert.False(t, e.HasCaveat)
	})
	t.Run("domestic_uk behaves like domestic_pl", func(t *testing.T) {
		m := inputVAT(movementFacts(entity.MaterialMovementReceipt), "36.00", entity.InputVatRegimeDomesticUK)
		e, err := BuildMaterialMovementEntry(m, testStartDate)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assertAmount(t, e, Acc2080, entity.AcctSideDebit, "36.00")
		assertAmount(t, e, Acc2010, entity.AcctSideCredit, "216.00")
	})
	t.Run("wnt: net-zero self-charge (Dr 2080 / Cr 2070), material as plain M1", func(t *testing.T) {
		m := inputVAT(movementFacts(entity.MaterialMovementReceipt), "41.40", entity.InputVatRegimeWNT)
		e, err := BuildMaterialMovementEntry(m, testStartDate)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assertAmount(t, e, Acc1110, entity.AcctSideDebit, "180.00")
		assertAmount(t, e, Acc2010, entity.AcctSideCredit, "180.00")
		assertAmount(t, e, Acc2080, entity.AcctSideDebit, "41.40")
		assertAmount(t, e, Acc2070, entity.AcctSideCredit, "41.40")
	})
	t.Run("import behaves like wnt", func(t *testing.T) {
		m := inputVAT(movementFacts(entity.MaterialMovementReceipt), "10.00", entity.InputVatRegimeImport)
		e, err := BuildMaterialMovementEntry(m, testStartDate)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assertAmount(t, e, Acc2070, entity.AcctSideCredit, "10.00")
		assertAmount(t, e, Acc2080, entity.AcctSideDebit, "10.00")
	})
	t.Run("input VAT amount with an unknown regime posts plain receipt + caveat", func(t *testing.T) {
		m := movementFacts(entity.MaterialMovementReceipt)
		m.InputVatAmount = nd("41.40")
		m.InputVatRegime = sql.NullString{} // NULL
		e, err := BuildMaterialMovementEntry(m, testStartDate)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assertAmount(t, e, Acc1110, entity.AcctSideDebit, "180.00")
		assertAmount(t, e, Acc2010, entity.AcctSideCredit, "180.00")
		assert.False(t, hasLine(e, Acc2080, entity.AcctSideDebit))
		assert.True(t, e.HasCaveat)
		assert.Contains(t, e.Caveat.String, "input vat")
	})
	t.Run("input VAT on a non-receipt movement is ignored (plain rule)", func(t *testing.T) {
		m := inputVAT(movementFacts(entity.MaterialMovementIssueProduction), "41.40", entity.InputVatRegimeWNT)
		m.OnHandBefore, m.OnHandAfter = dec("10"), dec("0")
		e, err := BuildMaterialMovementEntry(m, testStartDate)
		require.NoError(t, err)
		require.NoError(t, ValidateBalanced(e))
		assertAmount(t, e, Acc1120, entity.AcctSideDebit, "180.00")
		assertAmount(t, e, Acc1110, entity.AcctSideCredit, "180.00")
		assert.False(t, hasLine(e, Acc2080, entity.AcctSideDebit))
	})
}
