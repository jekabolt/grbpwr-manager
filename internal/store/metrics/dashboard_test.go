package metrics

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func hasAlert(alerts []entity.DashboardAlert, code string) bool {
	for _, a := range alerts {
		if a.Code == code {
			return true
		}
	}
	return false
}

func TestBuildDashboardAlerts_RefundRateNGate(t *testing.T) {
	d := &entity.Dashboard{CostCoveragePct: 100}
	rev := decimal.NewFromInt(1000)

	// 20% refund rate but only 10 orders (< floor of 30): no rate alert — too small a sample.
	got := buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 20, 10, 0, rev)
	assert.False(t, hasAlert(got, "high_refund_rate"), "should suppress refund alert below the n floor")

	// Same rate at 50 orders: alert fires.
	got = buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 20, 50, 0, rev)
	assert.True(t, hasAlert(got, "high_refund_rate"), "should fire refund alert above the n floor")

	// Above the floor but below the rate threshold: no alert.
	got = buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 5, 50, 0, rev)
	assert.False(t, hasAlert(got, "high_refund_rate"))
}

func TestBuildDashboardAlerts_ContributionTrustGate(t *testing.T) {
	rev := decimal.NewFromInt(1000)

	// Negative contribution with trustworthy coverage -> critical.
	d := &entity.Dashboard{CostCoveragePct: 90, ContributionMargin: decimal.NewFromInt(-5)}
	got := buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 0, 0, 0, rev)
	assert.True(t, hasAlert(got, "negative_contribution_margin"))

	// Same negative figure but coverage too low to trust the sign -> no critical.
	d = &entity.Dashboard{CostCoveragePct: 30, ContributionMargin: decimal.NewFromInt(-5)}
	got = buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 0, 0, 0, rev)
	assert.False(t, hasAlert(got, "negative_contribution_margin"), "don't trust contribution sign at low coverage")
	// Low coverage should instead surface the coverage warning.
	assert.True(t, hasAlert(got, "low_cost_coverage"))
}

func TestBuildDashboardAlerts_UncostedAndReorder(t *testing.T) {
	d := &entity.Dashboard{CostCoveragePct: 100, UncostedProductIds: []int{7, 8}}
	got := buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 0, 0, 3, decimal.NewFromInt(1000))
	assert.True(t, hasAlert(got, "uncosted_products"))
	assert.True(t, hasAlert(got, "reorder_needed"))

	// Clean state: no alerts.
	clean := &entity.Dashboard{CostCoveragePct: 100}
	assert.Empty(t, buildDashboardAlerts(clean, entity.DefaultAlertThresholds(), 0, 0, 0, decimal.NewFromInt(1000)))
}
