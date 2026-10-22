package fusingops

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// MySQL returns fixed-scale decimals (10.0); the wire round trip normalises them (10). Both are one
// operation, so content matching must not split them.
func TestSameFusingOpContentIgnoresDecimalScaleAndEmptySlices(t *testing.T) {
	stored := &entity.TechCardOperation{
		SMV:               decimal.NewNullDecimal(decimal.New(100, -1)),
		PressPressureNCm2: decimal.NewNullDecimal(decimal.New(2500, -2)),
		BomLineKeys:       []string{},
	}
	echo := &entity.TechCardOperation{
		SMV:               decimal.NewNullDecimal(decimal.New(10, 0)),
		PressPressureNCm2: decimal.NewNullDecimal(decimal.New(25, 0)),
	}
	if !sameFusingOpContent(stored, echo) {
		t.Fatal("10.0 and 10 must be the same content")
	}
	echo.SMV = decimal.NewNullDecimal(decimal.New(11, 0))
	if sameFusingOpContent(stored, echo) {
		t.Fatal("different SMV must differ")
	}
}
