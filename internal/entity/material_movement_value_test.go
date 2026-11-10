package entity

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestMovementValueCentsRoundsEachMovementBeforeSumming(t *testing.T) {
	qty := decimal.NewFromInt(1)

	t.Run("half cent rounds up per movement", func(t *testing.T) {
		unitCost := decimal.RequireFromString("0.005")
		total := MovementValueCents(qty, unitCost).Add(MovementValueCents(qty, unitCost))
		if !total.Equal(decimal.RequireFromString("0.02")) {
			t.Fatalf("two movements at 0.005 = %s, want 0.02", total)
		}
	})

	t.Run("below half cent rounds down per movement", func(t *testing.T) {
		unitCost := decimal.RequireFromString("0.0049")
		total := MovementValueCents(qty, unitCost).Add(MovementValueCents(qty, unitCost))
		if !total.Equal(decimal.Zero) {
			t.Fatalf("two movements at 0.0049 = %s, want 0.00", total)
		}
	})
}
