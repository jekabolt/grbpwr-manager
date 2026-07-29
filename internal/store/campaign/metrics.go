package campaign

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type campaignMetricRow struct {
	Total         int64 `db:"total"`
	Pending       int64 `db:"pending"`
	Sent          int64 `db:"sent"`
	Failed        int64 `db:"failed"`
	Skipped       int64 `db:"skipped"`
	Delivered     int64 `db:"delivered"`
	UniqueOpened  int64 `db:"unique_opened"`
	TotalOpens    int64 `db:"total_opens"`
	UniqueClicked int64 `db:"unique_clicked"`
	TotalClicks   int64 `db:"total_clicks"`
	Bounced       int64 `db:"bounced"`
	Complained    int64 `db:"complained"`
	Unsubscribed  int64 `db:"unsubscribed"`
}

type campaignVariantMetricRow struct {
	VariantID     int    `db:"variant_id"`
	Label         string `db:"label"`
	Total         int64  `db:"total"`
	Pending       int64  `db:"pending"`
	Sent          int64  `db:"sent"`
	Failed        int64  `db:"failed"`
	Skipped       int64  `db:"skipped"`
	Delivered     int64  `db:"delivered"`
	UniqueOpened  int64  `db:"unique_opened"`
	TotalOpens    int64  `db:"total_opens"`
	UniqueClicked int64  `db:"unique_clicked"`
	TotalClicks   int64  `db:"total_clicks"`
	Bounced       int64  `db:"bounced"`
	Complained    int64  `db:"complained"`
	Unsubscribed  int64  `db:"unsubscribed"`
}

const campaignMetricSelect = `
	COUNT(ecr.id) AS total,
	COALESCE(SUM(ecr.status = 'pending'), 0) AS pending,
	COALESCE(SUM(ecr.status = 'sent'), 0) AS sent,
	COALESCE(SUM(ecr.status = 'failed'), 0) AS failed,
	COALESCE(SUM(ecr.status = 'skipped'), 0) AS skipped,
	COALESCE(SUM(ecr.delivered_at IS NOT NULL), 0) AS delivered,
	COALESCE(SUM(ecr.first_opened_at IS NOT NULL), 0) AS unique_opened,
	COALESCE(SUM(ecr.open_count), 0) AS total_opens,
	COALESCE(SUM(ecr.first_clicked_at IS NOT NULL), 0) AS unique_clicked,
	COALESCE(SUM(ecr.click_count), 0) AS total_clicks,
	COALESCE(SUM(ecr.bounced_at IS NOT NULL), 0) AS bounced,
	COALESCE(SUM(ecr.complained_at IS NOT NULL), 0) AS complained,
	COALESCE(SUM(ecr.unsubscribed_at IS NOT NULL), 0) AS unsubscribed`

const campaignTotalMetricsSQL = `
	SELECT ` + campaignMetricSelect + `
	FROM email_campaign ec
	LEFT JOIN email_campaign_recipient ecr ON ecr.campaign_id = ec.id
	WHERE ec.id = :campaign_id
	GROUP BY ec.id`

const campaignVariantMetricsSQL = `
	SELECT ecv.id AS variant_id, ecv.label, ` + campaignMetricSelect + `
	FROM email_campaign_variant ecv
	LEFT JOIN email_campaign_recipient ecr
	  ON ecr.campaign_id = ecv.campaign_id
	 AND ecr.variant_id = ecv.id
	 AND ecr.cohort = 'ab'
	WHERE ecv.campaign_id = :campaign_id
	GROUP BY ecv.id, ecv.label
	ORDER BY ecv.id`

func campaignMetricCounts(row campaignMetricRow) entity.CampaignMetricCounts {
	return entity.CampaignMetricCounts{
		Total:         row.Total,
		Pending:       row.Pending,
		Sent:          row.Sent,
		Failed:        row.Failed,
		Skipped:       row.Skipped,
		Delivered:     row.Delivered,
		UniqueOpened:  row.UniqueOpened,
		TotalOpens:    row.TotalOpens,
		UniqueClicked: row.UniqueClicked,
		TotalClicks:   row.TotalClicks,
		Bounced:       row.Bounced,
		Complained:    row.Complained,
		Unsubscribed:  row.Unsubscribed,
	}
}

// GetCampaignMetrics computes read-only aggregates from the recipient ledger.
// It deliberately creates no second source of truth; boutique campaign volume
// makes compute-on-read both simpler and safer.
func (s *Store) GetCampaignMetrics(
	ctx context.Context,
	campaignID int,
) (entity.CampaignMetrics, error) {
	total, err := storeutil.QueryNamedOne[campaignMetricRow](ctx, s.DB, campaignTotalMetricsSQL,
		map[string]any{"campaign_id": campaignID})
	if err != nil {
		return entity.CampaignMetrics{}, fmt.Errorf("get campaign metric totals: %w", err)
	}

	rows, err := storeutil.QueryListNamed[campaignVariantMetricRow](ctx, s.DB, campaignVariantMetricsSQL,
		map[string]any{"campaign_id": campaignID})
	if err != nil {
		return entity.CampaignMetrics{}, fmt.Errorf("get campaign metrics by variant: %w", err)
	}

	counts := campaignMetricCounts(total)
	out := entity.CampaignMetrics{
		CampaignID: campaignID,
		Counts:     counts,
		Rates:      counts.Rates(),
		Variants:   make([]entity.CampaignVariantMetrics, len(rows)),
	}
	for i := range rows {
		variantCounts := campaignMetricCounts(campaignMetricRow{
			Total:         rows[i].Total,
			Pending:       rows[i].Pending,
			Sent:          rows[i].Sent,
			Failed:        rows[i].Failed,
			Skipped:       rows[i].Skipped,
			Delivered:     rows[i].Delivered,
			UniqueOpened:  rows[i].UniqueOpened,
			TotalOpens:    rows[i].TotalOpens,
			UniqueClicked: rows[i].UniqueClicked,
			TotalClicks:   rows[i].TotalClicks,
			Bounced:       rows[i].Bounced,
			Complained:    rows[i].Complained,
			Unsubscribed:  rows[i].Unsubscribed,
		})
		out.Variants[i] = entity.CampaignVariantMetrics{
			VariantID: rows[i].VariantID,
			Label:     rows[i].Label,
			Counts:    variantCounts,
			Rates:     variantCounts.Rates(),
		}
	}
	return out, nil
}
