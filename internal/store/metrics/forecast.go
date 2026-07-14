package metrics

import (
	"context"
	"math"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// forecastInput is the pure-math input for computeForecast — everything is pre-fetched so the
// projection logic has no DB dependency and is unit-tested directly.
type forecastInput struct {
	monthStart   time.Time // first day of the forecast month (UTC)
	daysInMonth  int
	elapsed      int             // days elapsed incl. asOf's day
	remaining    int             // daysInMonth - elapsed
	mtdActual    decimal.Decimal // net revenue month-to-date
	trailing     []entity.TimeSeriesPoint // daily net revenue over the trailing 8 weeks (sparse)
	lyMonthTotal decimal.Decimal // full same-month last year (0 = none)
	lyToDate     decimal.Decimal // same month last year up to the same day count (0 = none)
}

// GetRevenueForecast projects net revenue for the calendar month containing asOf. DB-only (no GA4,
// whose cache is 90-day). asOf is the period end; the forecast always anchors to asOf's calendar
// month regardless of the requested range.
func (s *Store) GetRevenueForecast(ctx context.Context, asOf time.Time) (entity.RevenueForecast, error) {
	asOf = asOf.UTC()
	monthStart := time.Date(asOf.Year(), asOf.Month(), 1, 0, 0, 0, 0, time.UTC)
	daysInMonth := time.Date(asOf.Year(), asOf.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	asOfDate := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	elapsed := asOf.Day()
	dayExpr, _ := granularitySQL(entity.MetricsGranularityDay)

	sum := func(series []entity.TimeSeriesPoint) decimal.Decimal {
		var t decimal.Decimal
		for _, p := range series {
			t = t.Add(p.Value)
		}
		return t
	}

	mtdSeries, err := s.getRevenueByPeriod(ctx, monthStart, asOfDate.AddDate(0, 0, 1), dayExpr)
	if err != nil {
		return entity.RevenueForecast{}, err
	}
	trailing, err := s.getRevenueByPeriod(ctx, asOfDate.AddDate(0, 0, -56), asOfDate.AddDate(0, 0, 1), dayExpr)
	if err != nil {
		return entity.RevenueForecast{}, err
	}
	lyStart := monthStart.AddDate(-1, 0, 0)
	lyMonthSeries, err := s.getRevenueByPeriod(ctx, lyStart, lyStart.AddDate(0, 1, 0), dayExpr)
	if err != nil {
		return entity.RevenueForecast{}, err
	}
	lyToDateSeries, err := s.getRevenueByPeriod(ctx, lyStart, lyStart.AddDate(0, 0, elapsed), dayExpr)
	if err != nil {
		return entity.RevenueForecast{}, err
	}

	return computeForecast(forecastInput{
		monthStart:   monthStart,
		daysInMonth:  daysInMonth,
		elapsed:      elapsed,
		remaining:    daysInMonth - elapsed,
		mtdActual:    sum(mtdSeries),
		trailing:     trailing,
		lyMonthTotal: sum(lyMonthSeries),
		lyToDate:     sum(lyToDateSeries),
	}), nil
}

func computeForecast(in forecastInput) entity.RevenueForecast {
	f := entity.RevenueForecast{
		Month:              in.monthStart,
		MtdActual:          in.mtdActual.Round(2),
		ElapsedDays:        in.elapsed,
		RemainingDays:      in.remaining,
		LastYearMonthTotal: in.lyMonthTotal.Round(2),
	}

	// Month already closed → the actual IS the final figure.
	if in.remaining <= 0 {
		f.Forecast = in.mtdActual.Round(2)
		f.ForecastLow = f.Forecast
		f.ForecastHigh = f.Forecast
		f.RunRate = f.Forecast
		f.Method = "closed"
		return f
	}

	mtd := in.mtdActual.InexactFloat64()

	// Naive run-rate (always computed, for eyeballing).
	var runRate float64
	if in.elapsed > 0 {
		runRate = mtd / float64(in.elapsed) * float64(in.daysInMonth)
	}
	f.RunRate = decimal.NewFromFloat(runRate).Round(2)

	// Gap-fill the trailing window to all 56 calendar days (a no-sale day is a real 0, but the
	// daily query omits it), then build a day-of-week revenue profile + a daily stdev.
	byDate := make(map[string]float64, len(in.trailing))
	for _, p := range in.trailing {
		byDate[p.Date.UTC().Format("2006-01-02")] = p.Value.InexactFloat64()
	}
	end := in.monthStart.AddDate(0, 0, in.elapsed) // exclusive: day after asOf
	var dowSum, dowCnt [7]float64
	var daily []float64
	for d := end.AddDate(0, 0, -56); d.Before(end); d = d.AddDate(0, 0, 1) {
		v := byDate[d.Format("2006-01-02")]
		wd := int(d.Weekday())
		dowSum[wd] += v
		dowCnt[wd]++
		daily = append(daily, v)
	}
	var dowAvg [7]float64
	var trailingTotal float64
	for i := 0; i < 7; i++ {
		if dowCnt[i] > 0 {
			dowAvg[i] = dowSum[i] / dowCnt[i]
		}
		trailingTotal += dowSum[i]
	}

	// No trailing history at all → DOW profile is empty; fall back to the naive run-rate.
	if trailingTotal <= 0 {
		f.Forecast = f.RunRate
		f.ForecastLow = f.MtdActual
		f.ForecastHigh = f.RunRate
		f.Method = "run_rate"
		f.Caveat = "No recent daily history; showing a naive run-rate only."
		return f
	}

	// DOW forecast: actual so far + expected revenue for each remaining calendar day by weekday.
	forecastDow := mtd
	for i := 1; i <= in.remaining; i++ {
		day := in.monthStart.AddDate(0, 0, in.elapsed+i-1) // remaining days after asOf
		forecastDow += dowAvg[int(day.Weekday())]
	}

	method := "dow"
	forecast := forecastDow
	// Seasonal ratio-to-date, blended 50/50 when prior-year data exists. Half weight + a caveat
	// because a drop calendar distorts month-over-year seasonality.
	if in.lyToDate.GreaterThan(decimal.Zero) && in.lyMonthTotal.GreaterThan(decimal.Zero) {
		k := in.lyMonthTotal.InexactFloat64() / in.lyToDate.InexactFloat64()
		forecastSeasonal := mtd * k
		forecast = 0.5*forecastDow + 0.5*forecastSeasonal
		method = "dow+seasonal"
	}

	// ~80% band: ±1.28 · dailyStdev · √remaining. Never below what's already earned.
	stdev := stddev(daily)
	half := 1.28 * stdev * math.Sqrt(float64(in.remaining))
	low := forecast - half
	if low < mtd {
		low = mtd
	}

	f.Forecast = decimal.NewFromFloat(forecast).Round(2)
	f.ForecastLow = decimal.NewFromFloat(low).Round(2)
	f.ForecastHigh = decimal.NewFromFloat(forecast + half).Round(2)
	f.Method = method
	if in.elapsed <= 3 {
		f.Caveat = "Early in the month — the point estimate is noisy; rely on the low/high band."
	}
	return f
}

func stddev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	var ss float64
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}
