package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

type deliveryRow struct {
	Placed      time.Time    `db:"placed"`
	ShippedAt   sql.NullTime `db:"shipped_at"`
	DeliveredAt sql.NullTime `db:"delivered_at"`
	ETA         sql.NullTime `db:"eta"`
}

// GetDeliveryMetrics reports fulfilment speed for net-revenue orders placed in [from, to). Durations
// come from the order_status_history timestamps (MIN changed_at per status), on-time from the
// shipment ETA. Placed-cohort so the average is not dragged by tails from earlier periods.
func (s *Store) GetDeliveryMetrics(ctx context.Context, from, to time.Time) (entity.DeliverySection, error) {
	query := `
		SELECT co.placed,
			MIN(CASE WHEN os.name = 'shipped' THEN h.changed_at END) AS shipped_at,
			MIN(CASE WHEN os.name = 'delivered' THEN h.changed_at END) AS delivered_at,
			MAX(sh.estimated_arrival_date) AS eta
		FROM customer_order co
		LEFT JOIN order_status_history h ON h.order_id = co.id
		LEFT JOIN order_status os ON os.id = h.order_status_id
		LEFT JOIN shipment sh ON sh.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY co.id, co.placed
	`
	rows, err := storeutil.QueryListNamed[deliveryRow](ctx, s.DB, query, map[string]any{
		"from": from, "to": to, "statusIds": cache.OrderStatusIDsForNetRevenue(),
	})
	if err != nil {
		return entity.DeliverySection{}, err
	}
	return computeDeliveryMetrics(rows), nil
}

// computeDeliveryMetrics derives the fulfilment-speed section from per-order timestamp tuples. Pure
// (no DB) so it is unit-tested directly. Non-positive durations are dropped: the 0024 history
// backfill stamped legacy transitions at placed time, which would otherwise report 0-day deliveries.
func computeDeliveryMetrics(rows []deliveryRow) entity.DeliverySection {
	var placedToShipped, shippedToDelivered, placedToDelivered []float64
	var shippedCount, deliveredAmongShipped, deliveredCount, etaAmongDelivered, onTimeTotal, onTimeHit int
	weekSum := map[time.Time]float64{}
	weekCnt := map[time.Time]int{}

	for _, r := range rows {
		if r.ShippedAt.Valid {
			shippedCount++
			if d := daysBetween(r.Placed, r.ShippedAt.Time); d > 0 {
				placedToShipped = append(placedToShipped, d)
			}
			if r.DeliveredAt.Valid {
				deliveredAmongShipped++
				if d := daysBetween(r.ShippedAt.Time, r.DeliveredAt.Time); d > 0 {
					shippedToDelivered = append(shippedToDelivered, d)
				}
			}
		}
		if r.DeliveredAt.Valid {
			deliveredCount++
			if d := daysBetween(r.Placed, r.DeliveredAt.Time); d > 0 {
				placedToDelivered = append(placedToDelivered, d)
				wk := startOfWeekUTC(r.Placed)
				weekSum[wk] += d
				weekCnt[wk]++
			}
			if r.ETA.Valid {
				etaAmongDelivered++
				onTimeTotal++
				// On-time = delivered on or before the ETA calendar day (ETA is a date the operator
				// entered; allowing the whole ETA day absorbs UTC-vs-local drift).
				if !dateOnlyUTC(r.DeliveredAt.Time).After(dateOnlyUTC(r.ETA.Time)) {
					onTimeHit++
				}
			}
		}
	}

	sec := entity.DeliverySection{
		AvgDaysPlacedToShipped:      round2(mean(placedToShipped)),
		AvgDaysShippedToDelivered:   round2(mean(shippedToDelivered)),
		AvgDaysPlacedToDelivered:    round2(mean(placedToDelivered)),
		MedianDaysPlacedToDelivered: round2(median(placedToDelivered)),
		ShippedSample:               len(placedToShipped),
		DeliveredSample:             len(placedToDelivered),
		OnTimeSample:                onTimeTotal,
	}
	if onTimeTotal > 0 {
		sec.OnTimeRatePct = round2(float64(onTimeHit) / float64(onTimeTotal) * 100)
	}
	if deliveredCount > 0 {
		sec.EtaCoveragePct = round2(float64(etaAmongDelivered) / float64(deliveredCount) * 100)
	}
	if shippedCount > 0 {
		sec.DeliveredCoveragePct = round2(float64(deliveredAmongShipped) / float64(shippedCount) * 100)
	}

	weeks := make([]time.Time, 0, len(weekSum))
	for w := range weekSum {
		weeks = append(weeks, w)
	}
	sort.Slice(weeks, func(i, j int) bool { return weeks[i].Before(weeks[j]) })
	for _, w := range weeks {
		sec.AvgDeliveryDaysByWeek = append(sec.AvgDeliveryDaysByWeek, entity.TimeSeriesPoint{
			Date:  w,
			Value: decimal.NewFromFloat(round2(weekSum[w] / float64(weekCnt[w]))),
			Count: weekCnt[w],
		})
	}

	if shippedCount > 0 && sec.DeliveredCoveragePct < 80 {
		sec.Caveat = fmt.Sprintf("Only %.0f%% of shipped orders are marked delivered; placed→delivered figures cover that subset. Mark orders delivered for a complete picture.", sec.DeliveredCoveragePct)
	}
	return sec
}

func daysBetween(a, b time.Time) float64 { return b.Sub(a).Hours() / 24 }

func dateOnlyUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func startOfWeekUTC(t time.Time) time.Time {
	d := dateOnlyUTC(t)
	wd := int(d.Weekday())
	if wd == 0 { // Sunday → treat as day 7 so weeks start Monday
		wd = 7
	}
	return d.AddDate(0, 0, -(wd - 1))
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := make([]float64, len(xs))
	copy(s, xs)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
