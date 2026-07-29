package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestConvertEntityCampaignMetricsToPB(t *testing.T) {
	counts := entity.CampaignMetricCounts{
		Total:         12,
		Sent:          10,
		Delivered:     8,
		UniqueOpened:  4,
		TotalOpens:    6,
		UniqueClicked: 2,
		TotalClicks:   3,
		Bounced:       1,
		Complained:    1,
		Unsubscribed:  1,
	}
	metrics := entity.CampaignMetrics{
		CampaignID: 42,
		Counts:     counts,
		Rates:      counts.Rates(),
		Variants: []entity.CampaignVariantMetrics{{
			VariantID: 7,
			Label:     "A",
			Counts:    counts,
			Rates:     counts.Rates(),
		}},
	}

	got := ConvertEntityCampaignMetricsToPB(&metrics)

	if got.CampaignId != 42 || got.Counts.Total != 12 || got.Rates.DeliveryRate != 0.8 {
		t.Fatalf("campaign metrics conversion = %#v", got)
	}
	if len(got.Variants) != 1 ||
		got.Variants[0].VariantId != 7 ||
		got.Variants[0].Counts.TotalOpens != 6 ||
		got.Variants[0].Rates.ClickToOpenRate != 0.5 {
		t.Fatalf("variant metrics conversion = %#v", got.Variants)
	}
}
