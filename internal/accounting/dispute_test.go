package accounting

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testDisputeDate = time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)

func TestBuildDisputeEntry_AmountAndFee(t *testing.T) {
	e, err := BuildDisputeEntry(dec("50.00"), dec("15.00"), true, "order-1", "dp_1", "EUR", testDisputeDate)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	assert.Equal(t, entity.AcctSourceOrderDispute, e.SourceType)
	assert.Equal(t, "dispute:dp_1", e.SourceKey)
	// Dr 4040 disputed amount + Dr 6050 fee / Cr 1030 (amount + fee). COGS untouched.
	assertAmount(t, e, Acc4040, entity.AcctSideDebit, "50.00")
	assertAmount(t, e, Acc6050, entity.AcctSideDebit, "15.00")
	assertAmount(t, e, Acc1030, entity.AcctSideCredit, "65.00")
	assert.False(t, hasLine(e, Acc5010, entity.AcctSideDebit), "no COGS on a dispute")
	assert.False(t, e.HasCaveat)
}

func TestBuildDisputeEntry_FeeUnknown(t *testing.T) {
	e, err := BuildDisputeEntry(dec("50.00"), dec("0"), false, "order-1", "dp_2", "EUR", testDisputeDate)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	// No fee line; the whole disputed amount is pulled from 1030; a caveat records the missing fee.
	assert.False(t, hasLine(e, Acc6050, entity.AcctSideDebit))
	assertAmount(t, e, Acc4040, entity.AcctSideDebit, "50.00")
	assertAmount(t, e, Acc1030, entity.AcctSideCredit, "50.00")
	assert.True(t, e.HasCaveat)
	assert.Contains(t, e.Caveat.String, "fee unavailable")
}

func TestBuildDisputeEntry_NonEURCaveat(t *testing.T) {
	e, err := BuildDisputeEntry(dec("50.00"), dec("15.00"), true, "order-1", "dp_3", "USD", testDisputeDate)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	assert.True(t, e.HasCaveat)
	assert.Contains(t, e.Caveat.String, "USD")
}

func TestBuildDisputeEntry_ZeroAmount(t *testing.T) {
	_, err := BuildDisputeEntry(dec("0"), dec("15.00"), true, "order-1", "dp_4", "EUR", testDisputeDate)
	assert.ErrorIs(t, err, ErrDegenerateAmounts)
}
