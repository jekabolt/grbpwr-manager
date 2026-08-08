package entity

import (
	"testing"

	"github.com/shopspring/decimal"
)

// ValidateStitchesPerCm exists because chk_construction_stitches answers with MySQL error 3819 —
// no field, no sentence — and «0» is two keystrokes away in a plain numeric input.
func TestValidateStitchesPerCm(t *testing.T) {
	nd := func(s string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
	}

	t.Run("unset is legal — that is «inherit», not «zero»", func(t *testing.T) {
		if err := ValidateStitchesPerCm("f", decimal.NullDecimal{}); err != nil {
			t.Fatalf("an unset density must be accepted: %v", err)
		}
	})
	// ZERO IS NOT LEGAL HERE, and the contrast with the seam allowance is the point: 0 mm of
	// allowance is a real instruction («the выкройки carry the cut line»), 0 stitches per cm is a
	// seam with nothing holding it together.
	for _, bad := range []string{"0", "0.5", "20.1", "50"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			if err := ValidateStitchesPerCm("f", nd(bad)); err == nil {
				t.Fatalf("%s must be refused", bad)
			}
		})
	}
	for _, ok := range []string{"1", "4", "4.5", "20"} {
		t.Run("accepts "+ok, func(t *testing.T) {
			if err := ValidateStitchesPerCm("f", nd(ok)); err != nil {
				t.Fatalf("%s must be accepted: %v", ok, err)
			}
		})
	}
	t.Run("rejects a scale the column would truncate", func(t *testing.T) {
		if err := ValidateStitchesPerCm("f", nd("4.555")); err == nil {
			t.Fatal("a third decimal place must be refused rather than silently lost")
		}
	})
}
