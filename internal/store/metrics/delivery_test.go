package metrics

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestComputeDeliveryMetrics(t *testing.T) {
	base := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC) // a Monday
	day := func(n int) sql.NullTime {
		return sql.NullTime{Time: base.AddDate(0, 0, n), Valid: true}
	}
	rows := []deliveryRow{
		// A: shipped +2, delivered +5, ETA +6 → on-time.
		{Placed: base, ShippedAt: day(2), DeliveredAt: day(5), ETA: day(6)},
		// B: shipped +1, delivered +7, ETA +4 → late.
		{Placed: base, ShippedAt: day(1), DeliveredAt: day(7), ETA: day(4)},
		// C: shipped +3, never delivered → counts toward shipped + delivered-coverage denominator only.
		{Placed: base, ShippedAt: day(3), DeliveredAt: sql.NullTime{}, ETA: sql.NullTime{}},
	}

	got := computeDeliveryMetrics(rows)

	assert.Equal(t, 2.0, got.AvgDaysPlacedToShipped)        // (2+1+3)/3
	assert.Equal(t, 4.5, got.AvgDaysShippedToDelivered)     // (3+6)/2
	assert.Equal(t, 6.0, got.AvgDaysPlacedToDelivered)      // (5+7)/2
	assert.Equal(t, 6.0, got.MedianDaysPlacedToDelivered)   // median(5,7)
	assert.Equal(t, 3, got.ShippedSample)
	assert.Equal(t, 2, got.DeliveredSample)
	assert.Equal(t, 2, got.OnTimeSample)                    // A,B have delivered+ETA
	assert.Equal(t, 50.0, got.OnTimeRatePct)                // A on-time, B late
	assert.Equal(t, 100.0, got.EtaCoveragePct)              // both delivered orders had an ETA
	assert.InDelta(t, 66.67, got.DeliveredCoveragePct, 0.01) // 2 of 3 shipped delivered
	assert.NotEmpty(t, got.Caveat, "low delivered coverage should set a caveat")

	// Weekly series: all placed in the same week → one bucket, avg 6 days over 2 delivered orders.
	if assert.Len(t, got.AvgDeliveryDaysByWeek, 1) {
		assert.Equal(t, "6", got.AvgDeliveryDaysByWeek[0].Value.String())
		assert.Equal(t, 2, got.AvgDeliveryDaysByWeek[0].Count)
		assert.Equal(t, base, got.AvgDeliveryDaysByWeek[0].Date) // week starts Monday
	}
}

func TestComputeDeliveryMetricsEmpty(t *testing.T) {
	got := computeDeliveryMetrics(nil)
	assert.Equal(t, 0.0, got.AvgDaysPlacedToDelivered)
	assert.Equal(t, 0, got.DeliveredSample)
	assert.Empty(t, got.Caveat)
	assert.Empty(t, got.AvgDeliveryDaysByWeek)
}

// Non-positive durations (legacy 0024 history stamped at placed time) are dropped.
func TestComputeDeliveryMetricsDropsNonPositive(t *testing.T) {
	base := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	rows := []deliveryRow{
		{Placed: base, ShippedAt: sql.NullTime{Time: base, Valid: true}, DeliveredAt: sql.NullTime{Time: base, Valid: true}},
	}
	got := computeDeliveryMetrics(rows)
	assert.Equal(t, 0, got.ShippedSample, "zero-day shipped duration dropped")
	assert.Equal(t, 0, got.DeliveredSample, "zero-day delivered duration dropped")
	// but the order still counts for coverage (it was shipped and delivered)
	assert.Equal(t, 100.0, got.DeliveredCoveragePct)
}
