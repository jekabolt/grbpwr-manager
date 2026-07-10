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
