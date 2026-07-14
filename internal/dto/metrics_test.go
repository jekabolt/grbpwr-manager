package dto

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	shopspring "github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func TestComputeChangePct(t *testing.T) {
	tests := []struct {
		name     string
		current  float64
		previous float64
		want     float64
	}{
		{"revenue +24.9%", 20419.91, 16350.97, 24.88},
		{"avg order value -23.7%", 1134.44, 1486.45, -23.62},
		{"items per order +3.4%", 1.22, 1.18, 3.39},
		{"revenue vs prior period (inexact float operands)", 110, 5665.96, -98.06},
		{"1 vs 1 should be 0%", 1.0, 1.0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeChangePct(
				shopspring.NewFromFloat(tt.current),
				shopspring.NewFromFloat(tt.previous),
			)
			if got == nil {
				t.Fatal("computeChangePct returned nil")
			}
			diff := *got - tt.want
			if diff < -0.1 || diff > 0.1 {
				t.Errorf("computeChangePct() = %v, want ~%v", *got, tt.want)
			}
		})
	}
}

func TestConvertDashboardToPb_Compare(t *testing.T) {
	d := &entity.Dashboard{
		Revenue:            shopspring.NewFromInt(1000),
		Orders:             12,
		GrossMargin:        shopspring.NewFromInt(400),
		GrossMarginPct:     40,
		ContributionMargin: shopspring.NewFromInt(300),
		OperatingResult:    shopspring.NewFromInt(100),
		ComparePeriod:      entity.TimeRange{From: time.Unix(1000, 0).UTC(), To: time.Unix(2000, 0).UTC()},
		Compare: &entity.DashboardHeadline{
			Revenue:            shopspring.NewFromInt(900),
			Orders:             10,
			GrossMargin:        shopspring.NewFromInt(350),
			GrossMarginPct:     38,
			ContributionMargin: shopspring.NewFromInt(250),
			OperatingResult:    shopspring.NewFromInt(80),
		},
	}
	c := ConvertDashboardToPb(d).Compare
	assert.NotNil(t, c, "compare block emitted")
	assert.NotNil(t, c.Period, "compare window echoed")
	assert.Equal(t, "900", c.Revenue.GetValue(), "prior revenue value")
	assert.EqualValues(t, 10, c.Orders, "prior orders")
	assert.InDelta(t, 11.11, c.RevenueChangePct, 0.01, "revenue +11.11%")
	assert.InDelta(t, 20.0, c.OrdersChangePct, 0.01, "orders +20%")
	assert.InDelta(t, 14.29, c.GrossMarginChangePct, 0.01, "gross margin € +14.29%")
	// The margin-% delta must be a percentage-POINT change (40−38 = 2.0), NOT a percent-of-a-percent
	// (which would be (40−38)/38×100 ≈ 5.26). Locking 2.0 guards that distinction.
	assert.InDelta(t, 2.0, c.GrossMarginPctChangePp, 0.001, "gross margin % delta is +2 pp")
	assert.InDelta(t, 20.0, c.ContributionMarginChangePct, 0.01, "contribution +20%")
	assert.InDelta(t, 25.0, c.OperatingResultChangePct, 0.01, "operating result +25%")
}

func TestConvertDashboardToPb_CompareEdgeCases(t *testing.T) {
	// No comparison requested → no compare block.
	assert.Nil(t, ConvertDashboardToPb(&entity.Dashboard{Revenue: shopspring.NewFromInt(1000)}).Compare,
		"no compare block when Compare is nil")

	// Zero prior → change is 0 (no base), never +Inf/NaN.
	zeroPrior := &entity.Dashboard{
		Revenue: shopspring.NewFromInt(1000),
		Compare: &entity.DashboardHeadline{Revenue: shopspring.Zero},
	}
	assert.Zero(t, ConvertDashboardToPb(zeroPrior).Compare.RevenueChangePct,
		"prior 0 revenue → change 0, not +Inf")
}

func TestMetricWithComparisonToPb_ChangePct(t *testing.T) {
	// AvgOrderValue: 1134.44 vs 1486.45 should yield ~-23.6%
	m := entity.MetricWithComparison{
		Value:        shopspring.NewFromFloat(1134.44),
		CompareValue: ptr(shopspring.NewFromFloat(1486.45)),
		ChangePct:    nil, // store didn't set it
	}
	pb := metricWithComparisonToPb(m)
	if pb == nil {
		t.Fatal("metricWithComparisonToPb returned nil")
	}
	if pb.ChangePct > -23 || pb.ChangePct < -24 {
		t.Errorf("ChangePct = %v, want ~-23.6", pb.ChangePct)
	}
}

func TestMetricWithComparisonToPb_ItemsPerOrder(t *testing.T) {
	tests := []struct {
		name     string
		current  float64
		previous float64
		wantMin  float64
		wantMax  float64
	}{
		{
			name:     "BUG-01: 1.5 vs 1.3 should be +15.38%, not +100%",
			current:  1.5,
			previous: 1.3,
			wantMin:  15.0,
			wantMax:  16.0,
		},
		{
			name:     "1.14 vs 1.0 should be +14%",
			current:  1.14,
			previous: 1.0,
			wantMin:  13.5,
			wantMax:  14.5,
		},
		{
			name:     "1.0 vs 1.0 should be 0%",
			current:  1.0,
			previous: 1.0,
			wantMin:  -0.1,
			wantMax:  0.1,
		},
		{
			name:     "2.2 vs 1.8 should be +22.22%",
			current:  2.2,
			previous: 1.8,
			wantMin:  22.0,
			wantMax:  23.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := entity.MetricWithComparison{
				Value:        shopspring.NewFromFloat(tt.current),
				CompareValue: ptr(shopspring.NewFromFloat(tt.previous)),
				ChangePct:    nil,
			}
			pb := metricWithComparisonToPb(m)
			if pb == nil {
				t.Fatal("metricWithComparisonToPb returned nil")
			}
			if pb.ChangePct < tt.wantMin || pb.ChangePct > tt.wantMax {
				t.Errorf("ItemsPerOrder %.2f vs %.2f ChangePct = %v, want between %.2f and %.2f",
					tt.current, tt.previous, pb.ChangePct, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestMetricWithComparisonToPb_AvgSessionDuration(t *testing.T) {
	tests := []struct {
		name              string
		current           float64
		previous          float64
		wantChangePct     float64
		wantDisplayedCurr string
		wantDisplayedPrev string
		description       string
	}{
		{
			name:              "BUG-02 (reported): 21.94s vs 2.60s rounds to 21.9s vs 2.6s = +742.3%",
			current:           21.94,
			previous:          2.60,
			wantChangePct:     742.31,
			wantDisplayedCurr: "21.9",
			wantDisplayedPrev: "2.6",
			description:       "Original issue: displayed 22s vs 3s with 743.9% delta (not verifiable). Now: 21.9s vs 2.6s with 742.3% (verifiable)",
		},
		{
			name:              "21.95s vs 2.60s rounds to 22.0s vs 2.6s = +746.2%",
			current:           21.95,
			previous:          2.60,
			wantChangePct:     746.15,
			wantDisplayedCurr: "22.0",
			wantDisplayedPrev: "2.6",
			description:       "Rounding to 1 decimal place makes delta verifiable from displayed values",
		},
		{
			name:              "22.0s vs 3.0s = +633.3%",
			current:           22.0,
			previous:          3.0,
			wantChangePct:     633.33,
			wantDisplayedCurr: "22.0",
			wantDisplayedPrev: "3.0",
			description:       "Exact values after rounding",
		},
		{
			name:              "160.2s vs 181.3s rounds to 160.2s vs 181.3s = -11.6%",
			current:           160.2,
			previous:          181.3,
			wantChangePct:     -11.63,
			wantDisplayedCurr: "160.2",
			wantDisplayedPrev: "181.3",
			description:       "Negative delta with 1 decimal precision",
		},
		{
			name:              "Verify rounding: 22.48s vs 2.63s → 22.5s vs 2.6s = +765.4%",
			current:           22.48,
			previous:          2.63,
			wantChangePct:     765.38,
			wantDisplayedCurr: "22.5",
			wantDisplayedPrev: "2.6",
			description:       "Rounding to 1 decimal changes the delta calculation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := entity.MetricWithComparison{
				Value:        shopspring.NewFromFloat(tt.current),
				CompareValue: ptr(shopspring.NewFromFloat(tt.previous)),
				ChangePct:    nil,
			}
			pb := metricWithComparisonToPb(m, false, false, int32(1))
			if pb == nil {
				t.Fatal("metricWithComparisonToPb returned nil")
			}

			// Verify displayed values match expected rounded values
			if pb.Value.Value != tt.wantDisplayedCurr {
				t.Errorf("Value display: got %s, want %s", pb.Value.Value, tt.wantDisplayedCurr)
			}
			if pb.CompareValue != nil && pb.CompareValue.Value != tt.wantDisplayedPrev {
				t.Errorf("CompareValue display: got %s, want %s", pb.CompareValue.Value, tt.wantDisplayedPrev)
			}

			// Verify delta is computed from rounded values and matches manual calculation
			diff := pb.ChangePct - tt.wantChangePct
			if diff < -0.5 || diff > 0.5 {
				t.Errorf("%s: AvgSessionDuration %s vs %s ChangePct = %.2f, want ~%.2f (diff: %.2f)",
					tt.description, pb.Value.Value, pb.CompareValue.Value, pb.ChangePct, tt.wantChangePct, diff)
			}

			// Most importantly: verify users can manually calculate the delta from displayed values
			// Manual calculation: (current - previous) / previous * 100
			curr, _ := shopspring.NewFromString(pb.Value.Value)
			prev, _ := shopspring.NewFromString(pb.CompareValue.Value)
			manualDelta := curr.Sub(prev).Div(prev).Mul(shopspring.NewFromInt(100)).Round(2).InexactFloat64()
			deltaDiff := pb.ChangePct - manualDelta
			if deltaDiff < -0.5 || deltaDiff > 0.5 {
				t.Errorf("Delta not verifiable from displayed values: displayed %s vs %s, but ChangePct=%.2f (manual calc from displayed: %.2f)",
					pb.Value.Value, pb.CompareValue.Value, pb.ChangePct, manualDelta)
			}
		})
	}
}

func TestMetricWithComparisonToPb_RateMetrics(t *testing.T) {
	tests := []struct {
		name              string
		current           float64
		previous          float64
		wantChangePct     float64
		wantChangeAbs     float64
		wantLowerIsBetter bool
	}{
		{
			name:              "RefundRate: 0.0% vs 1.0% = -100% relative, -1.0pp absolute",
			current:           0.0,
			previous:          1.0,
			wantChangePct:     -100.0,
			wantChangeAbs:     -1.0,
			wantLowerIsBetter: true,
		},
		{
			name:              "BounceRate: 25.5% vs 30.0% = -15% relative, -4.5pp absolute",
			current:           25.5,
			previous:          30.0,
			wantChangePct:     -15.0,
			wantChangeAbs:     -4.5,
			wantLowerIsBetter: true,
		},
		{
			name:              "ConversionRate: 5.0% vs 4.0% = +25% relative, +1.0pp absolute",
			current:           5.0,
			previous:          4.0,
			wantChangePct:     25.0,
			wantChangeAbs:     1.0,
			wantLowerIsBetter: false,
		},
		{
			name:              "EmailOpenRate: 22.5% vs 20.0% = +12.5% relative, +2.5pp absolute",
			current:           22.5,
			previous:          20.0,
			wantChangePct:     12.5,
			wantChangeAbs:     2.5,
			wantLowerIsBetter: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := entity.MetricWithComparison{
				Value:        shopspring.NewFromFloat(tt.current),
				CompareValue: ptr(shopspring.NewFromFloat(tt.previous)),
				ChangePct:    nil,
			}
			pb := metricWithComparisonToPb(m, tt.wantLowerIsBetter)
			if pb == nil {
				t.Fatal("metricWithComparisonToPb returned nil")
			}

			// Verify relative percentage change
			if pb.ChangePct < tt.wantChangePct-0.1 || pb.ChangePct > tt.wantChangePct+0.1 {
				t.Errorf("ChangePct = %.2f, want %.2f", pb.ChangePct, tt.wantChangePct)
			}

			// Verify absolute delta (percentage points)
			if pb.ChangeAbsolute < tt.wantChangeAbs-0.1 || pb.ChangeAbsolute > tt.wantChangeAbs+0.1 {
				t.Errorf("ChangeAbsolute = %.2f, want %.2f", pb.ChangeAbsolute, tt.wantChangeAbs)
			}

			// Verify lowerIsBetter flag
			if pb.LowerIsBetter != tt.wantLowerIsBetter {
				t.Errorf("LowerIsBetter = %v, want %v", pb.LowerIsBetter, tt.wantLowerIsBetter)
			}
		})
	}
}

func ptr(d shopspring.Decimal) *shopspring.Decimal {
	return &d
}

// TestMetricWithComparisonToPb_Caveat verifies that the caveat field is correctly mapped from entity to proto.
func TestMetricWithComparisonToPb_Caveat(t *testing.T) {
	tests := []struct {
		name       string
		metric     entity.MetricWithComparison
		wantCaveat string
	}{
		{
			name: "Caveat present - should be mapped",
			metric: entity.MetricWithComparison{
				Value:        shopspring.NewFromInt(3290),
				CompareValue: ptr(shopspring.NewFromInt(5665)),
				Caveat:       "Gross revenue before discounts; previous period had no active discounts.",
			},
			wantCaveat: "Gross revenue before discounts; previous period had no active discounts.",
		},
		{
			name: "No caveat - should be empty",
			metric: entity.MetricWithComparison{
				Value:        shopspring.NewFromInt(3125),
				CompareValue: ptr(shopspring.NewFromInt(5000)),
			},
			wantCaveat: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb := metricWithComparisonToPb(tt.metric)

			assert.NotNil(t, pb)
			assert.Equal(t, tt.wantCaveat, pb.Caveat)
		})
	}
}

// TestFunnelRatesScaledToPct verifies bug #5 fix: BigQuery funnel rates are produced
// as 0–1 fractions but the API serves them as 0–100 percentages, matching the
// business KPI convention (conversionRate, refundRate, bounceRate).
func TestFunnelRatesScaledToPct(t *testing.T) {
	assert.InDelta(t, 23.81, fractionToPct(0.2381), 0.001)
	assert.InDelta(t, 0.0, fractionToPct(0), 0.001)
	assert.InDelta(t, 100.0, fractionToPct(1.0), 0.001)

	t.Run("AddToCartRateRow", func(t *testing.T) {
		pb := ConvertAddToCartRateToPb([]entity.AddToCartRateRow{{CartRate: 0.2381}})
		assert.InDelta(t, 23.81, pb[0].CartRate, 0.001)
	})

	t.Run("AddToCartRateAnalysis", func(t *testing.T) {
		pb := ConvertAddToCartRateAnalysisToPb(&entity.AddToCartRateAnalysis{
			AvgCartRate: 0.5,
			Products:    []entity.AddToCartRateProductRow{{CartRate: 0.25}},
			GlobalTrend: []entity.AddToCartRateGlobalRow{{GlobalCartRate: 0.1}},
		})
		assert.InDelta(t, 50.0, pb.AvgCartRate, 0.001)
		assert.InDelta(t, 25.0, pb.Products[0].CartRate, 0.001)
		assert.InDelta(t, 10.0, pb.GlobalTrend[0].GlobalCartRate, 0.001)
	})

	t.Run("AbandonedCartRow", func(t *testing.T) {
		pb := ConvertAbandonedCartToPb([]entity.AbandonedCartRow{{AbandonmentRate: 0.73}})
		assert.InDelta(t, 73.0, pb[0].AbandonmentRate, 0.001)
	})
}
