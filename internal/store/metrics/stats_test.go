package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestProportionMarginOfError(t *testing.T) {
	tests := []struct {
		name    string
		ratePct float64
		n       int
		want    float64
	}{
		{"no sample -> 0", 12, 0, 0},
		{"negative n -> 0", 12, -5, 0},
		{"p=0 -> 0", 0, 100, 0},
		{"p=100 -> 0", 100, 100, 0},
		{"over 100 clamps to p=1 -> 0", 150, 100, 0},
		{"50pct over 100 -> 9.8", 50, 100, 9.8}, // 1.96*sqrt(.25/100)*100
		{"50pct over 400 halves -> 4.9", 50, 400, 4.9},
		{"larger n shrinks the interval", 20, 10000, 0.784}, // 1.96*sqrt(.16/10000)*100
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := proportionMarginOfError(tt.ratePct, tt.n)
			assert.InDelta(t, tt.want, got, 0.01)
			assert.GreaterOrEqual(t, got, 0.0)
		})
	}
}

func TestAssociationMetrics(t *testing.T) {
	tests := []struct {
		name                             string
		coCount, ordersA, ordersB, total int
		wantSupport, wantConf, wantLift  float64
	}{
		// 100 orders; A in 20, B in 10, both in 5.
		// support=5/100=.05, confidence=5/20=.25, lift=5*100/(20*10)=2.5 (co-bought 2.5× chance).
		{"positive association", 5, 20, 10, 100, 0.05, 0.25, 2.5},
		// independence: P(A∧B)=P(A)P(B) ⇒ lift=1. A=50,B=40,both=20,total=100 → 20*100/(50*40)=1.
		{"independent -> lift 1", 20, 50, 40, 100, 0.20, 0.40, 1.0},
		// negative association: lift<1. A=50,B=50,both=10,total=100 → 10*100/(50*50)=0.4.
		{"negative association", 10, 50, 50, 100, 0.10, 0.20, 0.4},
		{"no orders -> all 0", 0, 0, 0, 0, 0, 0, 0},
		// degenerate marginal (product never sold alone) must not divide by zero.
		{"zero marginal -> lift 0", 3, 0, 5, 100, 0.03, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			support, conf, lift := associationMetrics(tt.coCount, tt.ordersA, tt.ordersB, tt.total)
			assert.InDelta(t, tt.wantSupport, support, 1e-9, "support")
			assert.InDelta(t, tt.wantConf, conf, 1e-9, "confidence")
			assert.InDelta(t, tt.wantLift, lift, 1e-9, "lift")
		})
	}
}
