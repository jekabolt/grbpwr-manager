package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDaysToSellThrough(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return base.AddDate(0, 0, n) }

	tests := []struct {
		name         string
		points       []dropSalePoint
		initialUnits int64
		targetPct    float64
		wantValid    bool
		wantDays     int64
	}{
		{
			name:         "no points -> invalid",
			points:       nil,
			initialUnits: 100,
			targetPct:    50,
			wantValid:    false,
		},
		{
			name:         "zero initial -> invalid",
			points:       []dropSalePoint{{Date: day(0), Units: 10}},
			initialUnits: 0,
			targetPct:    50,
			wantValid:    false,
		},
		{
			name:         "reached on the first sale day -> 0 days",
			points:       []dropSalePoint{{Date: day(0), Units: 60}},
			initialUnits: 100,
			targetPct:    50,
			wantValid:    true,
			wantDays:     0,
		},
		{
			name: "crosses 50% on day 10",
			points: []dropSalePoint{
				{Date: day(0), Units: 20},
				{Date: day(5), Units: 20},
				{Date: day(10), Units: 20}, // cumulative 60 >= 50 here
			},
			initialUnits: 100,
			targetPct:    50,
			wantValid:    true,
			wantDays:     10,
		},
		{
			name: "exact threshold counts as reached",
			points: []dropSalePoint{
				{Date: day(0), Units: 25},
				{Date: day(3), Units: 25}, // cumulative exactly 50
			},
			initialUnits: 100,
			targetPct:    50,
			wantValid:    true,
			wantDays:     3,
		},
		{
			name: "never reaches 50% -> invalid",
			points: []dropSalePoint{
				{Date: day(0), Units: 20},
				{Date: day(9), Units: 20}, // cumulative 40 < 50
			},
			initialUnits: 100,
			targetPct:    50,
			wantValid:    false,
		},
		{
			name: "days measured from first sale, not from epoch",
			points: []dropSalePoint{
				{Date: day(30), Units: 40},
				{Date: day(37), Units: 40}, // cumulative 80 >= 50 -> 7 days after first sale
			},
			initialUnits: 100,
			targetPct:    50,
			wantValid:    true,
			wantDays:     7,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := daysToSellThrough(tt.points, tt.initialUnits, tt.targetPct)
			assert.Equal(t, tt.wantValid, got.Valid)
			if tt.wantValid {
				assert.Equal(t, tt.wantDays, got.Int64)
			}
		})
	}
}
