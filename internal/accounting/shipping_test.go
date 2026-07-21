package accounting

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testShipDate = time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)

func shipFacts(actual, ret string) entity.AcctShipmentCostFacts {
	f := entity.AcctShipmentCostFacts{
		ShipmentID:   42,
		OrderUUID:    "order-ship",
		ShippingDate: sql.NullTime{Time: testShipDate, Valid: true},
		UpdatedAt:    testShipDate.Add(48 * time.Hour),
	}
	if actual != "" {
		f.ActualCost = nd(actual)
	}
	if ret != "" {
		f.ReturnShippingCost = nd(ret)
	}
	return f
}

func TestBuildShippingActualEntry_Basic(t *testing.T) {
	e, err := BuildShippingActualEntry(shipFacts("12.34", ""), 1)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))

	assert.Equal(t, entity.AcctSourceShippingActual, e.SourceType)
	assert.Equal(t, "ship:42", e.SourceKey)
	assert.Equal(t, testShipDate, e.OccurredAt)
	assert.False(t, e.HasCaveat)
	assertAmount(t, e, Acc6030, entity.AcctSideDebit, "12.34")
	assertAmount(t, e, Acc2030, entity.AcctSideCredit, "12.34")
}

func TestBuildShippingActualEntry_ActualPlusReturn(t *testing.T) {
	e, err := BuildShippingActualEntry(shipFacts("10.00", "3.50"), 1)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	// Both costs debit 6030 (two lines); 2030 is the balancing total.
	assertAmount(t, e, Acc2030, entity.AcctSideCredit, "13.50")
	n := 0
	sum := decimal.Zero
	for _, l := range e.Lines {
		if l.AccountCode == Acc6030 && l.Side == entity.AcctSideDebit {
			n++
			sum = sum.Add(l.Amount)
		}
	}
	assert.Equal(t, 2, n)
	assert.Equal(t, "13.50", sum.StringFixed(2))
}

func TestBuildShippingActualEntry_Version(t *testing.T) {
	e, err := BuildShippingActualEntry(shipFacts("5.00", ""), 3)
	require.NoError(t, err)
	assert.Equal(t, "ship:42:v3", e.SourceKey)
}

func TestBuildShippingActualEntry_NoCostSkips(t *testing.T) {
	_, err := BuildShippingActualEntry(shipFacts("", ""), 1)
	assert.ErrorIs(t, err, ErrSkipEmpty)

	_, err = BuildShippingActualEntry(shipFacts("0", "0"), 1)
	assert.ErrorIs(t, err, ErrSkipEmpty)
}

func TestBuildShippingActualEntry_MissingShippingDateFallback(t *testing.T) {
	f := shipFacts("9.99", "")
	f.ShippingDate = sql.NullTime{} // no shipping date
	e, err := BuildShippingActualEntry(f, 1)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	assert.Equal(t, f.UpdatedAt, e.OccurredAt)
	assert.True(t, e.HasCaveat)
	assert.Contains(t, e.Caveat.String, "shipping_date missing")
}

func TestBuildShippingActualEntry_NegativeDropped(t *testing.T) {
	f := shipFacts("10.00", "")
	f.ReturnShippingCost = nd("-4.00")
	e, err := BuildShippingActualEntry(f, 1)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	assertAmount(t, e, Acc2030, entity.AcctSideCredit, "10.00") // negative return excluded
	assert.True(t, e.HasCaveat)
	assert.Contains(t, e.Caveat.String, "negative return_shipping_cost")
}
