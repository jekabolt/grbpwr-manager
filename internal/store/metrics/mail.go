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

	return reconcileEmailSummary(row), nil
}

// reconcileEmailSummary derives a self-consistent email summary (counts + rates) from
// the raw daily counters.
//
// The counters are recorded independently and without per-event idempotency: emails_sent
// is incremented at send time, while delivered/opened/clicked come from Resend webhooks
// (keyed by the webhook's own date). Over a fixed window the raw sums can therefore be
// internally inconsistent, producing impossible rates:
//   - delivered can exceed sent (duplicate/retried webhooks, or a delivery whose send fell
//     just before `from`) -> deliveryRate > 100%;
//   - clicks can be recorded with zero opens (open pixel blocked or open tracking off) even
//     though a click necessarily implies an open -> openRate < clickRate.
//
// Reconcile the counts so the summary is self-consistent before deriving rates:
// delivered <= sent, opened >= clicked (a click implies an open) and opened <= delivered.
// All rates are therefore in [0,100] with deliveryRate <= 100 and openRate >= clickRate.
func reconcileEmailSummary(row emailMetricsSumRow) *entity.EmailMetricsSummary {
	deliveredEff := min(row.TotalDelivered, row.TotalSent)
	openedEff := min(max(row.TotalOpened, row.TotalClicked), deliveredEff)
	clickedEff := min(row.TotalClicked, deliveredEff)
	bouncedEff := min(row.TotalBounced, row.TotalSent)

	summary := &entity.EmailMetricsSummary{
		TotalSent:      row.TotalSent,
		TotalDelivered: deliveredEff,
		TotalBounced:   bouncedEff,
		TotalOpened:    openedEff,
		TotalClicked:   clickedEff,
	}

	if row.TotalSent > 0 {
		summary.DeliveryRate = float64(deliveredEff) / float64(row.TotalSent) * 100
		summary.BounceRate = float64(bouncedEff) / float64(row.TotalSent) * 100
	}
	if deliveredEff > 0 {
		summary.OpenRate = float64(openedEff) / float64(deliveredEff) * 100
		summary.ClickRate = float64(clickedEff) / float64(deliveredEff) * 100
	}

	return summary
}
