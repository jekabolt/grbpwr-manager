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
	got := buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 20, 10, 0, rev, nil, 0, 0)
	assert.False(t, hasAlert(got, "high_refund_rate"), "should suppress refund alert below the n floor")

	// Same rate at 50 orders: alert fires.
	got = buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 20, 50, 0, rev, nil, 0, 0)
	assert.True(t, hasAlert(got, "high_refund_rate"), "should fire refund alert above the n floor")

	// Above the floor but below the rate threshold: no alert.
	got = buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 5, 50, 0, rev, nil, 0, 0)
	assert.False(t, hasAlert(got, "high_refund_rate"))
}

func TestBuildDashboardAlerts_ContributionTrustGate(t *testing.T) {
	rev := decimal.NewFromInt(1000)

	// Negative contribution with trustworthy coverage -> critical.
	d := &entity.Dashboard{CostCoveragePct: 90, ContributionMargin: decimal.NewFromInt(-5)}
	got := buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 0, 0, 0, rev, nil, 0, 0)
	assert.True(t, hasAlert(got, "negative_contribution_margin"))

	// Same negative figure but coverage too low to trust the sign -> no critical.
	d = &entity.Dashboard{CostCoveragePct: 30, ContributionMargin: decimal.NewFromInt(-5)}
	got = buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 0, 0, 0, rev, nil, 0, 0)
	assert.False(t, hasAlert(got, "negative_contribution_margin"), "don't trust contribution sign at low coverage")
	// Low coverage should instead surface the coverage warning.
	assert.True(t, hasAlert(got, "low_cost_coverage"))
}

func TestBuildDashboardAlerts_GA4TrackingCoverage(t *testing.T) {
	rev := decimal.NewFromInt(1000)
	th := entity.DefaultAlertThresholds() // GA4CoverageWarnPct = 80, RateFloorN = 30

	// GA4 saw 50% of DB revenue over enough orders: warn.
	d := &entity.Dashboard{CostCoveragePct: 100, Revenue: rev, GA4Revenue: decimal.NewFromInt(500), TrackingCoveragePct: 50}
	assert.True(t, hasAlert(buildDashboardAlerts(d, th, 0, 50, 0, rev, nil, 0, 0), "low_ga4_tracking_coverage"))

	// Healthy coverage (95%): no alert.
	d = &entity.Dashboard{CostCoveragePct: 100, Revenue: rev, GA4Revenue: decimal.NewFromInt(950), TrackingCoveragePct: 95}
	assert.False(t, hasAlert(buildDashboardAlerts(d, th, 0, 50, 0, rev, nil, 0, 0), "low_ga4_tracking_coverage"))

	// Below the order floor: suppressed even at low coverage (too small a sample).
	d = &entity.Dashboard{CostCoveragePct: 100, Revenue: rev, GA4Revenue: decimal.NewFromInt(500), TrackingCoveragePct: 50}
	assert.False(t, hasAlert(buildDashboardAlerts(d, th, 0, 10, 0, rev, nil, 0, 0), "low_ga4_tracking_coverage"))

	// GA4 synced nothing (0 revenue → 0% coverage): treated as "not synced", not a 0% alarm.
	d = &entity.Dashboard{CostCoveragePct: 100, Revenue: rev, GA4Revenue: decimal.Zero, TrackingCoveragePct: 0}
	assert.False(t, hasAlert(buildDashboardAlerts(d, th, 0, 50, 0, rev, nil, 0, 0), "low_ga4_tracking_coverage"))

	// Near-total tracking loss: GA4 synced a positive but tiny amount, so coverage rounds to 0.00.
	// This is the WORST case and MUST alarm (regression guard: an earlier `coverage > 0` gate wrongly
	// suppressed it).
	d = &entity.Dashboard{CostCoveragePct: 100, Revenue: rev, GA4Revenue: decimal.NewFromInt(1), TrackingCoveragePct: 0}
	assert.True(t, hasAlert(buildDashboardAlerts(d, th, 0, 50, 0, rev, nil, 0, 0), "low_ga4_tracking_coverage"))

	// DB revenue is zero (coverage denominator zero → TrackingCoveragePct never computed): a positive
	// GA4 figure must NOT trigger a spurious "0% coverage" alarm.
	d = &entity.Dashboard{CostCoveragePct: 100, Revenue: decimal.Zero, GA4Revenue: decimal.NewFromInt(500), TrackingCoveragePct: 0}
	assert.False(t, hasAlert(buildDashboardAlerts(d, th, 0, 50, 0, rev, nil, 0, 0), "low_ga4_tracking_coverage"))
}

func TestBuildDashboardAlerts_UncostedAndReorder(t *testing.T) {
	d := &entity.Dashboard{CostCoveragePct: 100, UncostedProductIds: []int{7, 8}}
	got := buildDashboardAlerts(d, entity.DefaultAlertThresholds(), 0, 0, 3, decimal.NewFromInt(1000), nil, 0, 0)
	assert.True(t, hasAlert(got, "uncosted_products"))
	assert.True(t, hasAlert(got, "reorder_needed"))

	// Clean state: no alerts.
	clean := &entity.Dashboard{CostCoveragePct: 100}
	assert.Empty(t, buildDashboardAlerts(clean, entity.DefaultAlertThresholds(), 0, 0, 0, decimal.NewFromInt(1000), nil, 0, 0))
}

// TestBuildDashboardAlerts_MaterialAndRun covers the two NF-09 alerts: low material stock (with the
// most-below names in the detail) and stale open production runs.
func TestBuildDashboardAlerts_MaterialAndRun(t *testing.T) {
	d := &entity.Dashboard{CostCoveragePct: 100}
	th := entity.DefaultAlertThresholds() // ProductionRunStaleDays = 60
	rev := decimal.NewFromInt(1000)

	// Low material stock fires when count > 0; the alert names the supplied materials.
	got := buildDashboardAlerts(d, th, 0, 0, 0, rev, []string{"Wool", "Zipper"}, 2, 0)
	assert.True(t, hasAlert(got, "low_material_stock"))
	for _, a := range got {
		if a.Code == "low_material_stock" {
			assert.Contains(t, a.Detail, "Wool")
			assert.Contains(t, a.Detail, "Zipper")
		}
	}

	// Zero below-min materials: no alert.
	assert.False(t, hasAlert(buildDashboardAlerts(d, th, 0, 0, 0, rev, nil, 0, 0), "low_material_stock"))

	// Stale open runs fire when count > 0.
	assert.True(t, hasAlert(buildDashboardAlerts(d, th, 0, 0, 0, rev, nil, 0, 3), "stale_open_production_run"))
	assert.False(t, hasAlert(buildDashboardAlerts(d, th, 0, 0, 0, rev, nil, 0, 0), "stale_open_production_run"))
}
