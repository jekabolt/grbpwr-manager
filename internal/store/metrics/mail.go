package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type emailMetricsSumRow struct {
	TotalSent      int `db:"total_sent"`
	TotalDelivered int `db:"total_delivered"`
	TotalBounced   int `db:"total_bounced"`
	TotalOpened    int `db:"total_opened"`
	TotalClicked   int `db:"total_clicked"`
}

// GetEmailMetricsSummary aggregates email delivery counters for a date range and computes rates.
func (s *Store) GetEmailMetricsSummary(ctx context.Context, from, to time.Time) (*entity.EmailMetricsSummary, error) {
	query := `
SELECT
	COALESCE(SUM(emails_sent), 0)      AS total_sent,
	COALESCE(SUM(emails_delivered), 0) AS total_delivered,
	COALESCE(SUM(emails_bounced), 0)   AS total_bounced,
	COALESCE(SUM(emails_opened), 0)    AS total_opened,
	COALESCE(SUM(emails_clicked), 0)   AS total_clicked
FROM
	email_daily_metrics
WHERE
	date >= :from
	AND date < :to
`
	row, err := storeutil.QueryNamedOne[emailMetricsSumRow](ctx, s.DB, query, map[string]any{
		"from": from,
		"to":   to,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get email metrics summary: %w", err)
	}

	summary := &entity.EmailMetricsSummary{
		TotalSent:      row.TotalSent,
		TotalDelivered: row.TotalDelivered,
		TotalBounced:   row.TotalBounced,
		TotalOpened:    row.TotalOpened,
		TotalClicked:   row.TotalClicked,
	}

	if row.TotalSent > 0 {
		summary.DeliveryRate = float64(row.TotalDelivered) / float64(row.TotalSent) * 100
		summary.BounceRate = float64(row.TotalBounced) / float64(row.TotalSent) * 100
	}
	if row.TotalDelivered > 0 {
		summary.OpenRate = float64(row.TotalOpened) / float64(row.TotalDelivered) * 100
		summary.ClickRate = float64(row.TotalClicked) / float64(row.TotalDelivered) * 100
	}

	return summary, nil
}
