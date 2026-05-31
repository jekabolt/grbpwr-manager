package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDailyConversionRate(t *testing.T) {
	tests := []struct {
		name     string
		orders   int
		sessions int
		want     float64
	}{
		{"normal", 3, 100, 3.0},
		{"zero sessions", 5, 0, 0},
		{"exactly 100%", 4, 4, 100.0},
		// Bug #3: orders-by-day and sessions-by-day come from different sources and can
		// misalign, yielding >100% (e.g. 250%, 100%, 66.7% in the report). Clamp to 100.
		{"more orders than sessions clamps to 100", 5, 2, 100.0},
		{"single order single session", 1, 1, 100.0},
		{"negative orders clamps to 0", -3, 10, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := dailyConversionRate(tt.orders, tt.sessions).Float64()
			assert.InDelta(t, tt.want, got, 0.01)
			assert.GreaterOrEqual(t, got, 0.0)
			assert.LessOrEqual(t, got, 100.0)
		})
	}
}
