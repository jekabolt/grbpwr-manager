package metrics

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

// nd is a small helper to build a valid NullDecimal from a float.
func nd(v float64) decimal.NullDecimal {
	return decimal.NullDecimal{Valid: true, Decimal: decimal.NewFromFloat(v)}
}

func TestComputeRowMargin(t *testing.T) {
	tests := []struct {
		name        string
		revenue     float64
		unitCost    decimal.NullDecimal
		revenueCost float64
		wantHasCost bool
		wantUnit    float64
		wantRevCost float64
		wantMargin  float64
		wantPct     float64
	}{
		{
			// The whole point of has_cost: no cost set ⇒ everything N/A (zero), NOT a 100% margin.
			name:        "no cost is N/A not 100pct",
			revenue:     100,
			unitCost:    decimal.NullDecimal{}, // invalid = no cost_price
			revenueCost: 0,
			wantHasCost: false,
		},
		{
			name:        "normal margin",
			revenue:     100,
			unitCost:    nd(3),
			revenueCost: 30,
			wantHasCost: true,
			wantUnit:    3,
			wantRevCost: 30,
			wantMargin:  70,
			wantPct:     70,
		},
		{
			// Costed product with no sales in the window: cost known, but no revenue/margin.
			name:        "cost set but zero revenue",
			revenue:     0,
			unitCost:    nd(5),
			revenueCost: 0,
			wantHasCost: true,
			wantUnit:    5,
			wantRevCost: 0,
			wantMargin:  0,
			wantPct:     0, // guarded: revenue not > 0
		},
		{
			// Sold below cost (deep discount) ⇒ honest negative margin, not clamped.
			name:        "negative margin when sold below cost",
			revenue:     50,
			unitCost:    nd(6),
			revenueCost: 60,
			wantHasCost: true,
			wantUnit:    6,
			wantRevCost: 60,
			wantMargin:  -10,
			wantPct:     -20,
		},
		{
			name:        "rounds to two places",
			revenue:     99.999,
			unitCost:    nd(1.111),
			revenueCost: 33.335,
			wantHasCost: true,
			wantUnit:    1.111, // unit cost is passed through unrounded
			wantRevCost: 33.34, // Round(2)
			wantMargin:  66.66, // 99.999 - 33.34 = 66.659 -> 66.66
			wantPct:     66.66, // 66.66 / 99.999 * 100 = 66.66
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rm := computeRowMargin(decimal.NewFromFloat(tt.revenue), tt.unitCost, decimal.NewFromFloat(tt.revenueCost))
			assert.Equal(t, tt.wantHasCost, rm.HasCost)
			if !tt.wantHasCost {
				// N/A: all money fields stay zero so the API can render "N/A".
				assert.True(t, rm.UnitCost.IsZero(), "unit cost")
				assert.True(t, rm.RevenueCost.IsZero(), "revenue cost")
				assert.True(t, rm.GrossMargin.IsZero(), "gross margin")
				assert.Zero(t, rm.GrossMarginPct, "gross margin pct")
				return
			}
			assert.True(t, decimal.NewFromFloat(tt.wantUnit).Equal(rm.UnitCost), "unit cost: got %s", rm.UnitCost)
			assert.True(t, decimal.NewFromFloat(tt.wantRevCost).Equal(rm.RevenueCost), "revenue cost: got %s", rm.RevenueCost)
			assert.True(t, decimal.NewFromFloat(tt.wantMargin).Equal(rm.GrossMargin), "gross margin: got %s", rm.GrossMargin)
			assert.InDelta(t, tt.wantPct, rm.GrossMarginPct, 0.01)
		})
	}
}
