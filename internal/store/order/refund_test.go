package order

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestStockRestoreModeForCancel guards the invariant that stock is only ever
// reduced on the Placed -> AwaitingPayment transition. Cancelling a Placed order
// must NOT restore stock (it was never reduced), otherwise inventory inflates.
func TestStockRestoreModeForCancel(t *testing.T) {
	cases := []struct {
		status entity.OrderStatusName
		want   stockRestoreMode
	}{
		{entity.Placed, stockRestoreNone},              // stock never reduced -> must not restore
		{entity.AwaitingPayment, stockRestoreSilent},   // reduced at invoice time
		{entity.Confirmed, stockRestoreHistory},        // reduced; restore with history
		{entity.PendingReturn, stockRestoreHistory},    // came from shipped/delivered
		{entity.RefundInProgress, stockRestoreHistory}, // came from confirmed
	}

	for _, c := range cases {
		if got := stockRestoreModeForCancel(c.status); got != c.want {
			t.Errorf("stockRestoreModeForCancel(%s) = %d, want %d", c.status, got, c.want)
		}
	}
}
