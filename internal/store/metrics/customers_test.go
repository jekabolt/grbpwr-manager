package metrics

import (
	"math"
	"sort"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func TestCLVStatsCalculation(t *testing.T) {
	tests := []struct {
		name           string
		clvs           []float64
		expectedMean   string
		expectedMedian string
		expectedP90    string
		expectedSample int
		isCollapsed    bool
	}{
		{
			name:           "Single customer - collapsed distribution",
			clvs:           []float64{55.00},
			expectedMean:   "55",
			expectedMedian: "55",
			expectedP90:    "55",
			expectedSample: 1,
			isCollapsed:    true,
		},
		{
			name:           "Two customers same value - collapsed distribution",
			clvs:           []float64{55.00, 55.00},
			expectedMean:   "55",
			expectedMedian: "55",
			expectedP90:    "55",
			expectedSample: 2,
			isCollapsed:    true,
		},
		{
			name:           "Two customers different values",
			clvs:           []float64{55.00, 105.00},
			expectedMean:   "80",
			expectedMedian: "80",
			expectedP90:    "105",
			expectedSample: 2,
			isCollapsed:    false,
		},
		{
			name:           "Three customers",
			clvs:           []float64{55.00, 105.00, 1350.00},
			expectedMean:   "503.33",
			expectedMedian: "105",
			expectedP90:    "1350",
			expectedSample: 3,
			isCollapsed:    false,
		},
		{
			name: "Ten customers - realistic distribution",
			clvs: []float64{55, 105, 1350, 1350, 1450, 1470.99, 4886.98, 6860.99, 9963.95, 15041.93},
			expectedMean:   "4253.48",
			expectedMedian: "1460.5",
			expectedP90:    "9963.95",
			expectedSample: 10,
			isCollapsed:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := calculateCLVStatsFromFloats(tt.clvs)

			assert.Equal(t, tt.expectedMean, stats.Mean.String(), "Mean mismatch")
			assert.Equal(t, tt.expectedMedian, stats.Median.String(), "Median mismatch")
			assert.Equal(t, tt.expectedP90, stats.P90.String(), "P90 mismatch")
			assert.Equal(t, tt.expectedSample, stats.SampleSize, "SampleSize mismatch")

			if tt.isCollapsed {
				assert.True(t, stats.Mean.Equal(stats.Median) && stats.Median.Equal(stats.P90),
					"Expected collapsed distribution (mean = median = P90)")
			}
		})
	}
}

func TestP90IndexCalculation(t *testing.T) {
	tests := []struct {
		sampleSize int
		expectedIdx int
	}{
		{1, 0},
		{2, 1},
		{3, 2},
		{10, 8},
		{100, 89},
	}

	for _, tt := range tests {
		t.Run(string(rune(tt.sampleSize)), func(t *testing.T) {
			p90Idx := int(math.Ceil(float64(tt.sampleSize)*0.9)) - 1
			if p90Idx < 0 {
				p90Idx = 0
			}
			assert.Equal(t, tt.expectedIdx, p90Idx)
		})
	}
}

func calculateCLVStatsFromFloats(clvs []float64) entity.CLVStats {
	if len(clvs) == 0 {
		return entity.CLVStats{}
	}

	sort.Float64s(clvs)

	mean := 0.0
	for _, v := range clvs {
		mean += v
	}
	mean /= float64(len(clvs))

	median := 0.0
	if len(clvs)%2 == 1 {
		median = clvs[len(clvs)/2]
	} else {
		median = (clvs[len(clvs)/2-1] + clvs[len(clvs)/2]) / 2
	}

	p90Idx := int(math.Ceil(float64(len(clvs))*0.9)) - 1
	if p90Idx < 0 {
		p90Idx = 0
	}
	p90 := clvs[p90Idx]

	return entity.CLVStats{
		Mean:       decimal.NewFromFloat(mean).Round(2),
		Median:     decimal.NewFromFloat(median).Round(2),
		P90:        decimal.NewFromFloat(p90).Round(2),
		SampleSize: len(clvs),
	}
}
