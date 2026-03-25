package metrics

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// mysqlUTCExpr evaluates to the offset '+00:00' without a ':00' substring in the query text.
// sqlx.Named treats ':00' inside a literal like '+00:00' as a bind parameter named "00".
const mysqlUTCExpr = "CONCAT('+', '00', CHAR(58), '00')"

// granularitySQL returns date expression for ORDER BY/SELECT and GROUP BY.
// dateExpr for order tables (co.placed), subDateExpr for subscriber (created_at).
// Uses CONVERT_TZ to UTC so date bucketing matches Go's bucketStart (UTC).
func granularitySQL(g entity.MetricsGranularity) (dateExpr, subDateExpr string) {
	// CONVERT_TZ to UTC (same as offset '+00:00') so DATE() uses UTC regardless of MySQL session TZ
	placedUTC := "CONVERT_TZ(co.placed, @@session.time_zone, " + mysqlUTCExpr + ")"
	createdUTC := "CONVERT_TZ(created_at, @@session.time_zone, " + mysqlUTCExpr + ")"
	switch g {
	case entity.MetricsGranularityWeek:
		return fmt.Sprintf("DATE(DATE_SUB(%s, INTERVAL WEEKDAY(%s) DAY))", placedUTC, placedUTC),
			fmt.Sprintf("DATE(DATE_SUB(%s, INTERVAL WEEKDAY(%s) DAY))", createdUTC, createdUTC)
	case entity.MetricsGranularityMonth:
		return fmt.Sprintf("DATE(DATE_FORMAT(%s, '%%Y-%%m-01'))", placedUTC),
			fmt.Sprintf("DATE(DATE_FORMAT(%s, '%%Y-%%m-01'))", createdUTC)
	default:
		return fmt.Sprintf("DATE(%s)", placedUTC), fmt.Sprintf("DATE(%s)", createdUTC)
	}
}

// granularityDateExpr returns date expression for a given column (e.g. osh.changed_at).
// Uses CONVERT_TZ to UTC for alignment with Go bucketStart.
func granularityDateExpr(g entity.MetricsGranularity, col string) string {
	colUTC := fmt.Sprintf("CONVERT_TZ(%s, @@session.time_zone, %s)", col, mysqlUTCExpr)
	switch g {
	case entity.MetricsGranularityWeek:
		return fmt.Sprintf("DATE(DATE_SUB(%s, INTERVAL WEEKDAY(%s) DAY))", colUTC, colUTC)
	case entity.MetricsGranularityMonth:
		return fmt.Sprintf("DATE(DATE_FORMAT(%s, '%%Y-%%m-01'))", colUTC)
	default:
		return fmt.Sprintf("DATE(%s)", colUTC)
	}
}

// fillTimeSeriesGaps ensures continuous date range for charts; fills missing buckets with zeros.
func fillTimeSeriesGaps(points []entity.TimeSeriesPoint, from, to time.Time, granularity entity.MetricsGranularity) []entity.TimeSeriesPoint {
	pointMap := make(map[string]entity.TimeSeriesPoint)
	for _, p := range points {
		key := p.Date.Format("2006-01-02")
		pointMap[key] = p
	}
	var result []entity.TimeSeriesPoint
	cur := bucketStart(from, granularity)
	end := bucketStart(to, granularity)
	for !cur.After(end) {
		key := cur.Format("2006-01-02")
		if p, ok := pointMap[key]; ok {
			result = append(result, p)
		} else {
			result = append(result, entity.TimeSeriesPoint{Date: cur, Value: decimal.Zero, Count: 0})
		}
		cur = bucketNext(cur, granularity)
	}
	return result
}

// bucketStart returns the start of the bucket containing t. Uses UTC to align with MySQL
// CONVERT_TZ(..., UTC offset) in granularitySQL; avoids timezone mismatch between Go and MySQL.
func bucketStart(t time.Time, g entity.MetricsGranularity) time.Time {
	t = t.UTC()
	loc := time.UTC
	switch g {
	case entity.MetricsGranularityWeek:
		// Monday 00:00 (align with MySQL WEEKDAY: 0=Mon, 6=Sun; Go: 0=Sun, 1=Mon)
		weekday := int(t.Weekday())
		daysBack := (weekday + 6) % 7
		return time.Date(t.Year(), t.Month(), t.Day()-daysBack, 0, 0, 0, 0, loc)
	case entity.MetricsGranularityMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	default:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
}

func bucketNext(t time.Time, g entity.MetricsGranularity) time.Time {
	switch g {
	case entity.MetricsGranularityWeek:
		return t.AddDate(0, 0, 7)
	case entity.MetricsGranularityMonth:
		return t.AddDate(0, 1, 0)
	default:
		return t.AddDate(0, 0, 1)
	}
}

func changePct(current, previous decimal.Decimal) *float64 {
	if previous.IsZero() {
		return nil
	}
	diff := current.Sub(previous).Div(previous).Mul(decimal.NewFromInt(100))
	f := diff.Round(2).InexactFloat64()
	return &f
}

func changePctInt(current, previous int) *float64 {
	if previous == 0 {
		return nil
	}
	f := (float64(current-previous) / float64(previous)) * 100
	return &f
}

func ptr(d decimal.Decimal) *decimal.Decimal {
	return &d
}

var ga4APISyncTypes = map[string]bool{
	"daily_metrics":        true,
	"product_page_metrics": true,
	"country_metrics":      true,
	"ecommerce":            true,
	"revenue_by_source":    true,
	"product_conversion":   true,
}

// buildDataFreshness computes per-source staleness from ga4_sync_status rows.
// ga4Threshold: how long since the last GA4 API success before data is considered stale.
// bqThreshold: how long since the last BQ success before data is considered stale.
func buildDataFreshness(statuses []entity.SyncSourceStatus, ga4Threshold, bqThreshold time.Duration) *entity.DataFreshness {
	if len(statuses) == 0 {
		return nil
	}

	now := time.Now().UTC()
	f := &entity.DataFreshness{
		Sources: make([]entity.SyncSourceStatus, 0, len(statuses)),
	}

	for _, s := range statuses {
		src := s
		isGA4 := ga4APISyncTypes[s.SyncType]
		threshold := bqThreshold
		if isGA4 {
			threshold = ga4Threshold
		}

		if s.Success {
			if isGA4 {
				if f.GA4APILastSuccess == nil || s.LastSyncAt.After(*f.GA4APILastSuccess) {
					t := s.LastSyncAt
					f.GA4APILastSuccess = &t
				}
			} else {
				if f.BQLastSuccess == nil || s.LastSyncAt.After(*f.BQLastSuccess) {
					t := s.LastSyncAt
					f.BQLastSuccess = &t
				}
			}
		}

		if !s.Success || now.Sub(s.LastSyncAt) > threshold {
			t := s.LastSyncAt
			src.StaleSince = &t
		}

		f.Sources = append(f.Sources, src)
	}

	if f.GA4APILastSuccess != nil && now.Sub(*f.GA4APILastSuccess) > ga4Threshold {
		f.GA4Stale = true
	}
	if f.GA4APILastSuccess == nil && len(statuses) > 0 {
		f.GA4Stale = true
	}
	if f.BQLastSuccess != nil && now.Sub(*f.BQLastSuccess) > bqThreshold {
		f.BQStale = true
	}
	if f.BQLastSuccess == nil && len(statuses) > 0 {
		f.BQStale = true
	}

	return f
}
