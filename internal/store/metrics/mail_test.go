package metrics

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
)

func TestEmailMetricsSummaryRates(t *testing.T) {
	tests := []struct {
		name            string
		row             emailMetricsSumRow
		wantDelivery    float64
		wantBounce      float64
		wantOpen        float64
		wantClick       float64
	}{
		{
			name: "all populated",
			row: emailMetricsSumRow{
				TotalSent:      100,
				TotalDelivered: 90,
				TotalBounced:   10,
				TotalOpened:    45,
				TotalClicked:   9,
			},
			wantDelivery: 90.0,
			wantBounce:   10.0,
			wantOpen:     50.0,
			wantClick:    10.0,
		},
		{
			name: "zero sent - rates are zero",
			row: emailMetricsSumRow{
				TotalSent:      0,
				TotalDelivered: 0,
				TotalBounced:   0,
				TotalOpened:    0,
				TotalClicked:   0,
			},
			wantDelivery: 0,
			wantBounce:   0,
			wantOpen:     0,
			wantClick:    0,
		},
		{
			name: "sent but no opens or clicks",
			row: emailMetricsSumRow{
				TotalSent:      50,
				TotalDelivered: 50,
				TotalBounced:   0,
				TotalOpened:    0,
				TotalClicked:   0,
			},
			wantDelivery: 100.0,
			wantBounce:   0,
			wantOpen:     0,
			wantClick:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary := &entity.EmailMetricsSummary{
				TotalSent:      tt.row.TotalSent,
				TotalDelivered: tt.row.TotalDelivered,
				TotalBounced:   tt.row.TotalBounced,
				TotalOpened:    tt.row.TotalOpened,
				TotalClicked:   tt.row.TotalClicked,
			}

			if tt.row.TotalSent > 0 {
				summary.DeliveryRate = float64(tt.row.TotalDelivered) / float64(tt.row.TotalSent) * 100
				summary.BounceRate = float64(tt.row.TotalBounced) / float64(tt.row.TotalSent) * 100
			}
			if tt.row.TotalDelivered > 0 {
				summary.OpenRate = float64(tt.row.TotalOpened) / float64(tt.row.TotalDelivered) * 100
				summary.ClickRate = float64(tt.row.TotalClicked) / float64(tt.row.TotalDelivered) * 100
			}

			assert.InDelta(t, tt.wantDelivery, summary.DeliveryRate, 0.001)
			assert.InDelta(t, tt.wantBounce, summary.BounceRate, 0.001)
			assert.InDelta(t, tt.wantOpen, summary.OpenRate, 0.001)
			assert.InDelta(t, tt.wantClick, summary.ClickRate, 0.001)
		})
	}
}
