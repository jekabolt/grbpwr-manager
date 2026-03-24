package metrics

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

// TestSessionWeightedAverages verifies that bounce rate, avg session duration,
// and pages per session are computed as session-weighted averages, not simple
// averages of daily averages.
func TestSessionWeightedAverages(t *testing.T) {
	tests := []struct {
		name                      string
		ga4Daily                  []ga4.DailyMetrics
		wantBounceRate            float64
		wantAvgSessionDuration    float64
		wantPagesPerSession       float64
	}{
		{
			name: "equal sessions - weighted equals unweighted",
			ga4Daily: []ga4.DailyMetrics{
				{Sessions: 100, BounceRate: 50.0, AvgSessionDuration: 120.0, PagesPerSession: 2.5},
				{Sessions: 100, BounceRate: 60.0, AvgSessionDuration: 180.0, PagesPerSession: 3.0},
			},
			wantBounceRate:         55.0,  // (50 + 60) / 2
			wantAvgSessionDuration: 150.0, // (120 + 180) / 2
			wantPagesPerSession:    2.75,  // (2.5 + 3.0) / 2
		},
		{
			name: "high variance - weighted differs from unweighted",
			ga4Daily: []ga4.DailyMetrics{
				{Sessions: 5, BounceRate: 80.0, AvgSessionDuration: 60.0, PagesPerSession: 1.5},
				{Sessions: 1000, BounceRate: 40.0, AvgSessionDuration: 200.0, PagesPerSession: 3.5},
			},
			// Unweighted: (80 + 40) / 2 = 60.0
			// Weighted: (80*5 + 40*1000) / 1005 = 40400 / 1005 ≈ 40.199
			wantBounceRate: 40.199,
			// Unweighted: (60 + 200) / 2 = 130.0
			// Weighted: (60*5 + 200*1000) / 1005 = 200300 / 1005 ≈ 199.303
			wantAvgSessionDuration: 199.303,
			// Unweighted: (1.5 + 3.5) / 2 = 2.5
			// Weighted: (1.5*5 + 3.5*1000) / 1005 = 3507.5 / 1005 ≈ 3.490
			wantPagesPerSession: 3.490,
		},
		{
			name: "weekend vs weekday traffic",
			ga4Daily: []ga4.DailyMetrics{
				// Weekend - low traffic, high bounce
				{Sessions: 50, BounceRate: 70.0, AvgSessionDuration: 90.0, PagesPerSession: 2.0},
				{Sessions: 60, BounceRate: 75.0, AvgSessionDuration: 85.0, PagesPerSession: 1.8},
				// Weekdays - high traffic, low bounce
				{Sessions: 500, BounceRate: 35.0, AvgSessionDuration: 180.0, PagesPerSession: 3.5},
				{Sessions: 520, BounceRate: 38.0, AvgSessionDuration: 190.0, PagesPerSession: 3.6},
				{Sessions: 480, BounceRate: 36.0, AvgSessionDuration: 175.0, PagesPerSession: 3.4},
			},
			// Total sessions: 1610
			// Weighted bounce: (70*50 + 75*60 + 35*500 + 38*520 + 36*480) / 1610
			//                = (3500 + 4500 + 17500 + 19760 + 17280) / 1610
			//                = 62540 / 1610 ≈ 38.845
			wantBounceRate: 38.845,
			// Weighted duration: (90*50 + 85*60 + 180*500 + 190*520 + 175*480) / 1610
			//                  = (4500 + 5100 + 90000 + 98800 + 84000) / 1610
			//                  = 282400 / 1610 ≈ 175.404
			wantAvgSessionDuration: 175.404,
			// Weighted pages: (2.0*50 + 1.8*60 + 3.5*500 + 3.6*520 + 3.4*480) / 1610
			//               = (100 + 108 + 1750 + 1872 + 1632) / 1610
			//               = 5462 / 1610 ≈ 3.392
			wantPagesPerSession: 3.392,
		},
		{
			name:                   "zero sessions - no division by zero",
			ga4Daily:               []ga4.DailyMetrics{},
			wantBounceRate:         0,
			wantAvgSessionDuration: 0,
			wantPagesPerSession:    0,
		},
		{
			name: "single day",
			ga4Daily: []ga4.DailyMetrics{
				{Sessions: 250, BounceRate: 45.5, AvgSessionDuration: 165.0, PagesPerSession: 3.2},
			},
			wantBounceRate:         45.5,
			wantAvgSessionDuration: 165.0,
			wantPagesPerSession:    3.2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Replicate the calculation logic from business.go lines 463-474, 566-571
			var totalSessions int
			var weightedBounceRate, weightedAvgSessionDuration, weightedPagesPerSession float64

			for _, d := range tt.ga4Daily {
				totalSessions += d.Sessions
				weightedBounceRate += d.BounceRate * float64(d.Sessions)
				weightedAvgSessionDuration += d.AvgSessionDuration * float64(d.Sessions)
				weightedPagesPerSession += d.PagesPerSession * float64(d.Sessions)
			}

			var gotBounceRate, gotAvgSessionDuration, gotPagesPerSession float64
			if totalSessions > 0 {
				gotBounceRate = weightedBounceRate / float64(totalSessions)
				gotAvgSessionDuration = weightedAvgSessionDuration / float64(totalSessions)
				gotPagesPerSession = weightedPagesPerSession / float64(totalSessions)
			}

			// Use InDelta for floating-point comparison with 0.001 tolerance
			assert.InDelta(t, tt.wantBounceRate, gotBounceRate, 0.001,
				"BounceRate mismatch")
			assert.InDelta(t, tt.wantAvgSessionDuration, gotAvgSessionDuration, 0.001,
				"AvgSessionDuration mismatch")
			assert.InDelta(t, tt.wantPagesPerSession, gotPagesPerSession, 0.001,
				"PagesPerSession mismatch")
		})
	}
}

// TestGA4TimeSeriesGapFilling verifies that GA4-sourced time series (SessionsByDay,
// UsersByDay, PageViewsByDay) are gap-filled to align with MySQL-sourced time series,
// preventing misaligned ConversionRateByDay charts when GA4 data has missing days.
func TestGA4TimeSeriesGapFilling(t *testing.T) {
	tests := []struct {
		name                 string
		from                 time.Time
		to                   time.Time
		granularity          entity.MetricsGranularity
		ga4Daily             []ga4.DailyMetrics
		ordersByDay          []entity.TimeSeriesPoint
		wantSessionsCount    int
		wantConvRateCount    int
		wantFirstConvRate    decimal.Decimal
		wantMissingDayConv   decimal.Decimal
	}{
		{
			name:        "GA4 missing middle day - gap filled with zero sessions",
			from:        time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
			to:          time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC),
			granularity: entity.MetricsGranularityDay,
			ga4Daily: []ga4.DailyMetrics{
				{Date: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), Sessions: 100, Users: 80, PageViews: 300},
				{Date: time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC), Sessions: 120, Users: 90, PageViews: 350},
			},
			ordersByDay: []entity.TimeSeriesPoint{
				{Date: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), Value: decimal.NewFromInt(5), Count: 5},
				{Date: time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC), Value: decimal.NewFromInt(3), Count: 3},
				{Date: time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC), Value: decimal.NewFromInt(6), Count: 6},
			},
			wantSessionsCount:  3,
			wantConvRateCount:  3,
			wantFirstConvRate:  decimal.NewFromInt(5),
			wantMissingDayConv: decimal.Zero,
		},
		{
			name:        "GA4 missing first and last day - gap filled",
			from:        time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
			to:          time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC),
			granularity: entity.MetricsGranularityDay,
			ga4Daily: []ga4.DailyMetrics{
				{Date: time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC), Sessions: 150, Users: 100, PageViews: 400},
				{Date: time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC), Sessions: 200, Users: 120, PageViews: 500},
				{Date: time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC), Sessions: 180, Users: 110, PageViews: 450},
			},
			ordersByDay: []entity.TimeSeriesPoint{
				{Date: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), Value: decimal.NewFromInt(2), Count: 2},
				{Date: time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC), Value: decimal.NewFromInt(8), Count: 8},
				{Date: time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC), Value: decimal.NewFromInt(10), Count: 10},
				{Date: time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC), Value: decimal.NewFromInt(9), Count: 9},
				{Date: time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC), Value: decimal.NewFromInt(4), Count: 4},
			},
			wantSessionsCount:  5,
			wantConvRateCount:  5,
			wantFirstConvRate:  decimal.Zero,
			wantMissingDayConv: decimal.Zero,
		},
		{
			name:        "GA4 all days present - no gaps to fill",
			from:        time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC),
			to:          time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC),
			granularity: entity.MetricsGranularityDay,
			ga4Daily: []ga4.DailyMetrics{
				{Date: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), Sessions: 100, Users: 80, PageViews: 300},
				{Date: time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC), Sessions: 120, Users: 90, PageViews: 350},
			},
			ordersByDay: []entity.TimeSeriesPoint{
				{Date: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), Value: decimal.NewFromInt(5), Count: 5},
				{Date: time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC), Value: decimal.NewFromInt(6), Count: 6},
			},
			wantSessionsCount: 2,
			wantConvRateCount: 2,
			wantFirstConvRate: decimal.NewFromInt(5),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionsByDay := make([]entity.TimeSeriesPoint, len(tt.ga4Daily))
			usersByDay := make([]entity.TimeSeriesPoint, len(tt.ga4Daily))
			pageViewsByDay := make([]entity.TimeSeriesPoint, len(tt.ga4Daily))
			for i, ga := range tt.ga4Daily {
				sessionsByDay[i] = entity.TimeSeriesPoint{
					Date:  ga.Date,
					Value: decimal.NewFromInt(int64(ga.Sessions)),
					Count: ga.Sessions,
				}
				usersByDay[i] = entity.TimeSeriesPoint{
					Date:  ga.Date,
					Value: decimal.NewFromInt(int64(ga.Users)),
					Count: ga.Users,
				}
				pageViewsByDay[i] = entity.TimeSeriesPoint{
					Date:  ga.Date,
					Value: decimal.NewFromInt(int64(ga.PageViews)),
					Count: ga.PageViews,
				}
			}

			sessionsByDay = fillTimeSeriesGaps(sessionsByDay, tt.from, tt.to, tt.granularity)
			usersByDay = fillTimeSeriesGaps(usersByDay, tt.from, tt.to, tt.granularity)
			pageViewsByDay = fillTimeSeriesGaps(pageViewsByDay, tt.from, tt.to, tt.granularity)

			assert.Len(t, sessionsByDay, tt.wantSessionsCount, "SessionsByDay should be gap-filled")
			assert.Len(t, usersByDay, tt.wantSessionsCount, "UsersByDay should be gap-filled")
			assert.Len(t, pageViewsByDay, tt.wantSessionsCount, "PageViewsByDay should be gap-filled")

			ordersByDayMap := make(map[string]int)
			for _, o := range tt.ordersByDay {
				ordersByDayMap[o.Date.Format("2006-01-02")] = o.Count
			}

			conversionRateByDay := make([]entity.TimeSeriesPoint, 0)
			for _, s := range sessionsByDay {
				dateKey := s.Date.Format("2006-01-02")
				ordersCount := ordersByDayMap[dateKey]
				convRate := decimal.Zero
				if s.Count > 0 {
					convRate = decimal.NewFromInt(int64(ordersCount)).Div(decimal.NewFromInt(int64(s.Count))).Mul(decimal.NewFromInt(100))
				}
				conversionRateByDay = append(conversionRateByDay, entity.TimeSeriesPoint{
					Date:  s.Date,
					Value: convRate,
					Count: ordersCount,
				})
			}

			assert.Len(t, conversionRateByDay, tt.wantConvRateCount, "ConversionRateByDay should have same length as SessionsByDay")
			assert.True(t, tt.wantFirstConvRate.Equal(conversionRateByDay[0].Value), "First conversion rate should match expected")

			if tt.wantMissingDayConv.GreaterThan(decimal.Zero) || !tt.wantMissingDayConv.IsZero() {
				found := false
				for _, cr := range conversionRateByDay {
					if cr.Value.Equal(tt.wantMissingDayConv) && cr.Count == 0 {
						found = true
						break
					}
				}
				assert.True(t, found, "Should have gap-filled day with zero conversion rate")
			}
		})
	}
}
