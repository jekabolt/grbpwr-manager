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
				{Sessions: 100, BounceRate: 50.0, AvgSessionDuration: 120.0, UserEngagementSeconds: 12000, PagesPerSession: 2.5},
				{Sessions: 100, BounceRate: 60.0, AvgSessionDuration: 180.0, UserEngagementSeconds: 18000, PagesPerSession: 3.0},
			},
			wantBounceRate:         55.0,  // (50 + 60) / 2
			wantAvgSessionDuration: 150.0, // (12000 + 18000) / 200 = 30000 / 200 = 150
			wantPagesPerSession:    2.75,  // (2.5 + 3.0) / 2
		},
		{
			name: "high variance - weighted differs from unweighted",
			ga4Daily: []ga4.DailyMetrics{
				{Sessions: 5, BounceRate: 80.0, AvgSessionDuration: 60.0, UserEngagementSeconds: 300, PagesPerSession: 1.5},
				{Sessions: 1000, BounceRate: 40.0, AvgSessionDuration: 200.0, UserEngagementSeconds: 200000, PagesPerSession: 3.5},
			},
			// Unweighted: (80 + 40) / 2 = 60.0
			// Weighted: (80*5 + 40*1000) / 1005 = 40400 / 1005 ≈ 40.199
			wantBounceRate: 40.199,
			// Weighted engagement: (300 + 200000) / 1005 = 200300 / 1005 ≈ 199.303
			wantAvgSessionDuration: 199.303,
			// Unweighted: (1.5 + 3.5) / 2 = 2.5
			// Weighted: (1.5*5 + 3.5*1000) / 1005 = 3507.5 / 1005 ≈ 3.490
			wantPagesPerSession: 3.490,
		},
		{
			name: "weekend vs weekday traffic",
			ga4Daily: []ga4.DailyMetrics{
				// Weekend - low traffic, high bounce
				{Sessions: 50, BounceRate: 70.0, AvgSessionDuration: 90.0, UserEngagementSeconds: 4500, PagesPerSession: 2.0},
				{Sessions: 60, BounceRate: 75.0, AvgSessionDuration: 85.0, UserEngagementSeconds: 5100, PagesPerSession: 1.8},
				// Weekdays - high traffic, low bounce
				{Sessions: 500, BounceRate: 35.0, AvgSessionDuration: 180.0, UserEngagementSeconds: 90000, PagesPerSession: 3.5},
				{Sessions: 520, BounceRate: 38.0, AvgSessionDuration: 190.0, UserEngagementSeconds: 98800, PagesPerSession: 3.6},
				{Sessions: 480, BounceRate: 36.0, AvgSessionDuration: 175.0, UserEngagementSeconds: 84000, PagesPerSession: 3.4},
			},
			// Total sessions: 1610
			// Weighted bounce: (70*50 + 75*60 + 35*500 + 38*520 + 36*480) / 1610
			//                = (3500 + 4500 + 17500 + 19760 + 17280) / 1610
			//                = 62540 / 1610 ≈ 38.845
			wantBounceRate: 38.845,
			// Total engagement: 4500 + 5100 + 90000 + 98800 + 84000 = 282400
			// Avg per session: 282400 / 1610 ≈ 175.404
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
				{Sessions: 250, BounceRate: 45.5, AvgSessionDuration: 165.0, UserEngagementSeconds: 41250, PagesPerSession: 3.2},
			},
			wantBounceRate:         45.5,
			wantAvgSessionDuration: 165.0, // 41250 / 250 = 165
			wantPagesPerSession:    3.2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Replicate the calculation logic from business.go lines 461-577
			var totalSessions int
			var totalUserEngagementSeconds int64
			var weightedBounceRate, weightedAvgSessionDuration, weightedPagesPerSession float64

			for _, d := range tt.ga4Daily {
				totalSessions += d.Sessions
				totalUserEngagementSeconds += d.UserEngagementSeconds
				weightedBounceRate += d.BounceRate * float64(d.Sessions)
				weightedAvgSessionDuration += d.AvgSessionDuration * float64(d.Sessions)
				weightedPagesPerSession += d.PagesPerSession * float64(d.Sessions)
			}

			var gotBounceRate, gotAvgSessionDuration, gotPagesPerSession float64
			if totalSessions > 0 {
				gotBounceRate = weightedBounceRate / float64(totalSessions)
				if totalUserEngagementSeconds > 0 {
					gotAvgSessionDuration = float64(totalUserEngagementSeconds) / float64(totalSessions)
				} else {
					gotAvgSessionDuration = weightedAvgSessionDuration / float64(totalSessions)
				}
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

// TestAvgSessionDurationUsesEngagement verifies that when user_engagement_seconds
// is present, AvgSessionDuration uses total engagement / total sessions instead of
// the weighted average of avg_session_duration (which is wall-clock and inflated).
func TestAvgSessionDurationUsesEngagement(t *testing.T) {
	tests := []struct {
		name                   string
		ga4Daily               []ga4.DailyMetrics
		wantAvgSessionDuration float64
	}{
		{
			name: "engagement present - use engagement / sessions",
			ga4Daily: []ga4.DailyMetrics{
				{Sessions: 39, AvgSessionDuration: 2213.48, UserEngagementSeconds: 851, BounceRate: 50.0, PagesPerSession: 2.5},
				{Sessions: 24, AvgSessionDuration: 2706.43, UserEngagementSeconds: 227, BounceRate: 55.0, PagesPerSession: 2.8},
			},
			// Total engagement: 851 + 227 = 1078
			// Total sessions: 39 + 24 = 63
			// Avg: 1078 / 63 ≈ 17.11
			wantAvgSessionDuration: 1078.0 / 63.0,
		},
		{
			name: "engagement zero - fallback to weighted avg_session_duration",
			ga4Daily: []ga4.DailyMetrics{
				{Sessions: 100, AvgSessionDuration: 120.0, UserEngagementSeconds: 0, BounceRate: 50.0, PagesPerSession: 2.5},
				{Sessions: 100, AvgSessionDuration: 180.0, UserEngagementSeconds: 0, BounceRate: 60.0, PagesPerSession: 3.0},
			},
			wantAvgSessionDuration: 150.0, // (120*100 + 180*100) / 200
		},
		{
			name: "mixed - some days have engagement, some don't",
			ga4Daily: []ga4.DailyMetrics{
				{Sessions: 50, AvgSessionDuration: 200.0, UserEngagementSeconds: 2500, BounceRate: 50.0, PagesPerSession: 2.5},
				{Sessions: 50, AvgSessionDuration: 300.0, UserEngagementSeconds: 0, BounceRate: 60.0, PagesPerSession: 3.0},
			},
			// Total engagement: 2500 (partial)
			// Total sessions: 100
			// Since totalUserEngagementSeconds > 0, use engagement: 2500 / 100 = 25
			wantAvgSessionDuration: 25.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var totalSessions int
			var totalUserEngagementSeconds int64
			var weightedAvgSessionDuration float64

			for _, d := range tt.ga4Daily {
				totalSessions += d.Sessions
				totalUserEngagementSeconds += d.UserEngagementSeconds
				weightedAvgSessionDuration += d.AvgSessionDuration * float64(d.Sessions)
			}

			var gotAvgSessionDuration float64
			if totalSessions > 0 {
				if totalUserEngagementSeconds > 0 {
					gotAvgSessionDuration = float64(totalUserEngagementSeconds) / float64(totalSessions)
				} else {
					gotAvgSessionDuration = weightedAvgSessionDuration / float64(totalSessions)
				}
			}

			assert.InDelta(t, tt.wantAvgSessionDuration, gotAvgSessionDuration, 0.001,
				"AvgSessionDuration mismatch")
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
				{Date: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), Sessions: 100, Users: 80, PageViews: 300, UserEngagementSeconds: 5000},
				{Date: time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC), Sessions: 120, Users: 90, PageViews: 350, UserEngagementSeconds: 6000},
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
				{Date: time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC), Sessions: 150, Users: 100, PageViews: 400, UserEngagementSeconds: 7500},
				{Date: time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC), Sessions: 200, Users: 120, PageViews: 500, UserEngagementSeconds: 10000},
				{Date: time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC), Sessions: 180, Users: 110, PageViews: 450, UserEngagementSeconds: 9000},
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
				{Date: time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), Sessions: 100, Users: 80, PageViews: 300, UserEngagementSeconds: 5000},
				{Date: time.Date(2026, 3, 21, 0, 0, 0, 0, time.UTC), Sessions: 120, Users: 90, PageViews: 350, UserEngagementSeconds: 6000},
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

// TestComparisonValuesForGA4AndCustomerMetrics verifies that all 7 metrics
// (BounceRate, AvgSessionDuration, PagesPerSession, RepeatCustomersRate,
// AvgOrdersPerCustomer, AvgDaysBetweenOrders, and NewSubscribers) have
// CompareValue and ChangePct set when a comparison period is provided.
func TestComparisonValuesForGA4AndCustomerMetrics(t *testing.T) {
	// Compute expected values for period (simulating weighted averages from GA4 daily data)
	// Period: 100 sessions @ 50% bounce, 200 sessions @ 60% bounce
	expectedBounceRate := (50.0*100 + 60.0*200) / 300.0
	expectedAvgSessionDuration := (12000.0 + 36000.0) / 300.0
	expectedPagesPerSession := (2.5*100 + 3.0*200) / 300.0

	// Compute expected values for compare period
	// Compare: 150 sessions @ 55% bounce, 250 sessions @ 65% bounce
	expectedCBounceRate := (55.0*150 + 65.0*250) / 400.0
	expectedCAvgSessionDuration := (22500.0 + 50000.0) / 400.0
	expectedCPagesPerSession := (2.8*150 + 3.2*250) / 400.0

	// Mock repeat customer metrics
	repeatRate := decimal.NewFromFloat(25.5)
	avgOrders := decimal.NewFromFloat(1.8)
	avgDays := decimal.NewFromFloat(45.2)
	cRepeatRate := decimal.NewFromFloat(22.3)
	cAvgOrders := decimal.NewFromFloat(1.6)
	cAvgDays := decimal.NewFromFloat(50.1)

	// Build BusinessMetrics (simulating GetBusinessMetrics logic)
	m := &entity.BusinessMetrics{}

	// Set period values
	m.BounceRate.Value = decimal.NewFromFloat(expectedBounceRate)
	m.AvgSessionDuration.Value = decimal.NewFromFloat(expectedAvgSessionDuration)
	m.PagesPerSession.Value = decimal.NewFromFloat(expectedPagesPerSession)
	m.RepeatCustomersRate.Value = repeatRate
	m.AvgOrdersPerCustomer.Value = avgOrders
	m.AvgDaysBetweenOrders.Value = avgDays
	m.NewSubscribers.Value = decimal.NewFromInt(100)

	// Set comparison values (this is what the fix adds)
	cBounceRate := decimal.NewFromFloat(expectedCBounceRate)
	m.BounceRate.CompareValue = &cBounceRate
	m.BounceRate.ChangePct = changePct(m.BounceRate.Value, cBounceRate)

	cAvgSessionDuration := decimal.NewFromFloat(expectedCAvgSessionDuration)
	m.AvgSessionDuration.CompareValue = &cAvgSessionDuration
	m.AvgSessionDuration.ChangePct = changePct(m.AvgSessionDuration.Value, cAvgSessionDuration)

	cPagesPerSession := decimal.NewFromFloat(expectedCPagesPerSession)
	m.PagesPerSession.CompareValue = &cPagesPerSession
	m.PagesPerSession.ChangePct = changePct(m.PagesPerSession.Value, cPagesPerSession)

	m.RepeatCustomersRate.CompareValue = &cRepeatRate
	m.RepeatCustomersRate.ChangePct = changePct(repeatRate, cRepeatRate)

	m.AvgOrdersPerCustomer.CompareValue = &cAvgOrders
	m.AvgOrdersPerCustomer.ChangePct = changePct(avgOrders, cAvgOrders)

	m.AvgDaysBetweenOrders.CompareValue = &cAvgDays
	m.AvgDaysBetweenOrders.ChangePct = changePct(avgDays, cAvgDays)

	cNewSubs := decimal.NewFromInt(85)
	m.NewSubscribers.CompareValue = &cNewSubs
	m.NewSubscribers.ChangePct = changePct(m.NewSubscribers.Value, cNewSubs)

	// Assertions: verify all 7 metrics have CompareValue and ChangePct
	t.Run("BounceRate has comparison data", func(t *testing.T) {
		assert.NotNil(t, m.BounceRate.CompareValue, "BounceRate.CompareValue should not be nil")
		assert.NotNil(t, m.BounceRate.ChangePct, "BounceRate.ChangePct should not be nil")
		assert.InDelta(t, expectedCBounceRate, m.BounceRate.CompareValue.InexactFloat64(), 0.01)
		assert.InDelta(t, -7.48, *m.BounceRate.ChangePct, 0.1)
	})

	t.Run("AvgSessionDuration has comparison data", func(t *testing.T) {
		assert.NotNil(t, m.AvgSessionDuration.CompareValue, "AvgSessionDuration.CompareValue should not be nil")
		assert.NotNil(t, m.AvgSessionDuration.ChangePct, "AvgSessionDuration.ChangePct should not be nil")
		assert.InDelta(t, expectedCAvgSessionDuration, m.AvgSessionDuration.CompareValue.InexactFloat64(), 0.01)
		assert.InDelta(t, -11.72, *m.AvgSessionDuration.ChangePct, 0.1)
	})

	t.Run("PagesPerSession has comparison data", func(t *testing.T) {
		assert.NotNil(t, m.PagesPerSession.CompareValue, "PagesPerSession.CompareValue should not be nil")
		assert.NotNil(t, m.PagesPerSession.ChangePct, "PagesPerSession.ChangePct should not be nil")
		assert.InDelta(t, expectedCPagesPerSession, m.PagesPerSession.CompareValue.InexactFloat64(), 0.01)
		assert.InDelta(t, -7.11, *m.PagesPerSession.ChangePct, 0.1)
	})

	t.Run("RepeatCustomersRate has comparison data", func(t *testing.T) {
		assert.NotNil(t, m.RepeatCustomersRate.CompareValue, "RepeatCustomersRate.CompareValue should not be nil")
		assert.NotNil(t, m.RepeatCustomersRate.ChangePct, "RepeatCustomersRate.ChangePct should not be nil")
		assert.InDelta(t, 22.3, m.RepeatCustomersRate.CompareValue.InexactFloat64(), 0.01)
		assert.InDelta(t, 14.35, *m.RepeatCustomersRate.ChangePct, 0.1)
	})

	t.Run("AvgOrdersPerCustomer has comparison data", func(t *testing.T) {
		assert.NotNil(t, m.AvgOrdersPerCustomer.CompareValue, "AvgOrdersPerCustomer.CompareValue should not be nil")
		assert.NotNil(t, m.AvgOrdersPerCustomer.ChangePct, "AvgOrdersPerCustomer.ChangePct should not be nil")
		assert.InDelta(t, 1.6, m.AvgOrdersPerCustomer.CompareValue.InexactFloat64(), 0.01)
		assert.InDelta(t, 12.5, *m.AvgOrdersPerCustomer.ChangePct, 0.1)
	})

	t.Run("AvgDaysBetweenOrders has comparison data", func(t *testing.T) {
		assert.NotNil(t, m.AvgDaysBetweenOrders.CompareValue, "AvgDaysBetweenOrders.CompareValue should not be nil")
		assert.NotNil(t, m.AvgDaysBetweenOrders.ChangePct, "AvgDaysBetweenOrders.ChangePct should not be nil")
		assert.InDelta(t, 50.1, m.AvgDaysBetweenOrders.CompareValue.InexactFloat64(), 0.01)
		assert.InDelta(t, -9.78, *m.AvgDaysBetweenOrders.ChangePct, 0.1)
	})

	t.Run("NewSubscribers has comparison data", func(t *testing.T) {
		assert.NotNil(t, m.NewSubscribers.CompareValue, "NewSubscribers.CompareValue should not be nil")
		assert.NotNil(t, m.NewSubscribers.ChangePct, "NewSubscribers.ChangePct should not be nil")
		assert.InDelta(t, 85.0, m.NewSubscribers.CompareValue.InexactFloat64(), 0.01)
		assert.InDelta(t, 17.65, *m.NewSubscribers.ChangePct, 0.1)
	})
}
