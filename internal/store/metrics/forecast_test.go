package metrics

import (
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

// uniformTrailing builds a 56-day trailing window ending the day before (monthStart+elapsed), all
// days carrying the same revenue, so the day-of-week profile is flat at `perDay`.
func uniformTrailing(monthStart time.Time, elapsed int, perDay int64) []entity.TimeSeriesPoint {
	end := monthStart.AddDate(0, 0, elapsed)
	var out []entity.TimeSeriesPoint
	for d := end.AddDate(0, 0, -56); d.Before(end); d = d.AddDate(0, 0, 1) {
		out = append(out, entity.TimeSeriesPoint{Date: d, Value: decimal.NewFromInt(perDay)})
	}
	return out
}

func TestComputeForecastDOW(t *testing.T) {
	monthStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) // April = 30 days
	in := forecastInput{
		monthStart:  monthStart,
		daysInMonth: 30,
		elapsed:     10,
		remaining:   20,
		mtdActual:   decimal.NewFromInt(100),
		trailing:    uniformTrailing(monthStart, 10, 10), // flat 10/day
	}
	got := computeForecast(in)
	// forecast = mtd(100) + 20 remaining days × 10/day = 300; flat history → zero-width band.
	assert.Equal(t, "dow", got.Method)
	assert.Equal(t, "300", got.Forecast.String())
	assert.Equal(t, "300", got.RunRate.String()) // 100/10 × 30
	assert.Equal(t, "300", got.ForecastLow.String())
	assert.Equal(t, "300", got.ForecastHigh.String())
	assert.Equal(t, 20, got.RemainingDays)
}

func TestComputeForecastSeasonalBlend(t *testing.T) {
	monthStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	in := forecastInput{
		monthStart:   monthStart,
		daysInMonth:  30,
		elapsed:      10,
		remaining:    20,
		mtdActual:    decimal.NewFromInt(100),
		trailing:     uniformTrailing(monthStart, 10, 10), // dow forecast = 300
		lyToDate:     decimal.NewFromInt(50),
		lyMonthTotal: decimal.NewFromInt(200), // k = 4 → seasonal = 400
	}
	got := computeForecast(in)
	// blend = 0.5×300 + 0.5×400 = 350.
	assert.Equal(t, "dow+seasonal", got.Method)
	assert.Equal(t, "350", got.Forecast.String())
	assert.Equal(t, "200", got.LastYearMonthTotal.String())
}

func TestComputeForecastClosedMonth(t *testing.T) {
	monthStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	in := forecastInput{
		monthStart: monthStart, daysInMonth: 30, elapsed: 30, remaining: 0,
		mtdActual: decimal.NewFromInt(500),
	}
	got := computeForecast(in)
	assert.Equal(t, "closed", got.Method)
	assert.Equal(t, "500", got.Forecast.String())
	assert.Equal(t, "500", got.ForecastLow.String())
	assert.Equal(t, "500", got.ForecastHigh.String())
}

func TestComputeForecastRunRateFallback(t *testing.T) {
	monthStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	in := forecastInput{
		monthStart: monthStart, daysInMonth: 30, elapsed: 10, remaining: 20,
		mtdActual: decimal.NewFromInt(100),
		trailing:  nil, // no recent history
	}
	got := computeForecast(in)
	assert.Equal(t, "run_rate", got.Method)
	assert.Equal(t, "300", got.Forecast.String()) // naive run-rate
	assert.NotEmpty(t, got.Caveat)
}

func TestComputeForecastEarlyMonthCaveat(t *testing.T) {
	monthStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	in := forecastInput{
		monthStart: monthStart, daysInMonth: 30, elapsed: 2, remaining: 28,
		mtdActual: decimal.NewFromInt(20),
		trailing:  uniformTrailing(monthStart, 2, 10),
	}
	got := computeForecast(in)
	assert.Contains(t, got.Caveat, "Early")
}
