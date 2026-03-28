package metrics

import (
	"math"
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
	d := make([]decimal.Decimal, len(clvs))
	for i, v := range clvs {
		d[i] = decimal.NewFromFloat(v)
	}
	return calculateCLVStatsFromDecimals(d)
}

func TestRFMLabel(t *testing.T) {
	tests := []struct {
		name     string
		r, f, m  int
		expected string
	}{
		{"Champions - high all", 5, 5, 5, "Champions"},
		{"Champions - threshold", 4, 4, 4, "Champions"},
		{"Loyal - good all", 3, 3, 3, "Loyal"},
		{"Loyal - high recency moderate freq high monetary", 5, 3, 5, "Loyal"},
		{"Potential Loyalist - recent moderate freq", 4, 2, 3, "Potential Loyalist"},
		{"At Risk - good spender low recency", 2, 3, 3, "At Risk"},
		{"At Risk - good spender lowest recency", 1, 3, 3, "At Risk"},
		{"Can't Lose Them - high spender no recency", 1, 4, 4, "Can't Lose Them"},
		{"Lost - single purchase long ago", 1, 1, 1, "Lost"},
		{"Lost - single purchase medium recency", 2, 1, 2, "Lost"},
		{"New Customers - recent first purchase", 5, 1, 3, "New Customers"},
		{"New Customers - good recency first purchase", 3, 1, 2, "New Customers"},
		{"Promising - recent low freq/monetary", 3, 2, 2, "Promising"},
		{"Potential Loyalist - recent moderate freq low monetary", 4, 2, 1, "Potential Loyalist"},
		{"Need Attention - mid slipping", 3, 2, 3, "Need Attention"},
		{"About to Sleep - below avg", 2, 2, 2, "About to Sleep"},
		{"Hibernating - low recency freq some value", 2, 2, 1, "Hibernating"},
		{"Hibernating - lowest recency some freq", 1, 2, 2, "Hibernating"},
		{"Hibernating - edge case low recency moderate freq low monetary", 2, 3, 1, "Hibernating"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rfmLabel(tt.r, tt.f, tt.m)
			assert.Equal(t, tt.expected, result, "RFM(%d,%d,%d)", tt.r, tt.f, tt.m)
		})
	}
}

func TestFormatCategoryDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"Empty string", "", ""},
		{"Single word", "jackets", "Jackets"},
		{"Two words with underscore", "hoodies_sweatshirts", "Hoodies Sweatshirts"},
		{"Three words", "t_shirts_tops", "T Shirts Tops"},
		{"Already capitalized", "Hoodies_Sweatshirts", "Hoodies Sweatshirts"},
		{"Mixed case", "Hoodies_sweatshirts", "Hoodies Sweatshirts"},
		{"Single char", "a", "A"},
		{"Multiple underscores", "a_b_c_d", "A B C D"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatCategoryDisplayName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
