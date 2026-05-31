package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReconcileEmailSummary(t *testing.T) {
	tests := []struct {
		name         string
		row          emailMetricsSumRow
		wantSent     int
		wantDeliv    int
		wantOpened   int
		wantClicked  int
		wantDelivery float64
		wantBounce   float64
		wantOpen     float64
		wantClick    float64
	}{
		{
			name: "all populated and consistent",
			row: emailMetricsSumRow{
				TotalSent: 100, TotalDelivered: 90, TotalBounced: 10, TotalOpened: 45, TotalClicked: 9,
			},
			wantSent: 100, wantDeliv: 90, wantOpened: 45, wantClicked: 9,
			wantDelivery: 90.0, wantBounce: 10.0, wantOpen: 50.0, wantClick: 10.0,
		},
		{
			name: "zero sent - rates are zero",
			row:  emailMetricsSumRow{},
		},
		{
			name: "sent but no opens or clicks",
			row: emailMetricsSumRow{
				TotalSent: 50, TotalDelivered: 50,
			},
			wantSent: 50, wantDeliv: 50,
			wantDelivery: 100.0,
		},
		{
			// Bug #1: delivered exceeds sent (webhook retries / cross-window attribution).
			// delivered must be capped at sent so deliveryRate <= 100%.
			name: "delivered exceeds sent is capped",
			row: emailMetricsSumRow{
				TotalSent: 188, TotalDelivered: 220, TotalOpened: 100, TotalClicked: 17,
			},
			wantSent: 188, wantDeliv: 188, wantOpened: 100, wantClicked: 17,
			wantDelivery: 100.0,
			wantOpen:     100.0 / 188 * 100,
			wantClick:    17.0 / 188 * 100,
		},
		{
			// Bug #2: clicks recorded with zero opens. A click implies an open, so
			// openRate must be >= clickRate (never 0% open with >0% click).
			name: "clicks with zero opens implies opens",
			row: emailMetricsSumRow{
				TotalSent: 188, TotalDelivered: 180, TotalOpened: 0, TotalClicked: 14,
			},
			wantSent: 188, wantDeliv: 180, wantOpened: 14, wantClicked: 14,
			wantDelivery: 180.0 / 188 * 100,
			wantOpen:     14.0 / 180 * 100,
			wantClick:    14.0 / 180 * 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := reconcileEmailSummary(tt.row)

			assert.Equal(t, tt.wantSent, s.TotalSent, "TotalSent")
			assert.Equal(t, tt.wantDeliv, s.TotalDelivered, "TotalDelivered")
			assert.Equal(t, tt.wantOpened, s.TotalOpened, "TotalOpened")
			assert.Equal(t, tt.wantClicked, s.TotalClicked, "TotalClicked")

			assert.InDelta(t, tt.wantDelivery, s.DeliveryRate, 0.001, "DeliveryRate")
			assert.InDelta(t, tt.wantBounce, s.BounceRate, 0.001, "BounceRate")
			assert.InDelta(t, tt.wantOpen, s.OpenRate, 0.001, "OpenRate")
			assert.InDelta(t, tt.wantClick, s.ClickRate, 0.001, "ClickRate")

			// Invariants that must hold for every input.
			assert.LessOrEqual(t, s.TotalDelivered, s.TotalSent, "delivered <= sent")
			assert.LessOrEqual(t, s.DeliveryRate, 100.0+1e-9, "deliveryRate <= 100")
			assert.GreaterOrEqual(t, s.OpenRate+1e-9, s.ClickRate, "openRate >= clickRate")
			for _, r := range []float64{s.DeliveryRate, s.BounceRate, s.OpenRate, s.ClickRate} {
				assert.GreaterOrEqual(t, r, 0.0)
				assert.LessOrEqual(t, r, 100.0+1e-9)
			}
		})
	}
}
