package metrics

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

// alertByCode returns the alert carrying code, or ok=false. Complements hasAlert (dashboard_test.go)
// for the cases that also assert on severity.
func alertByCode(alerts []entity.DashboardAlert, code string) (entity.DashboardAlert, bool) {
	for _, a := range alerts {
		if a.Code == code {
			return a, true
		}
	}
	return entity.DashboardAlert{}, false
}

// The three accounting dashboard alerts (buildAcctDashboardAlerts) are pure over
// acctDashboardAlertInputs — the DB queries that populate those inputs are exercised by the store
// integration tests (TestAcctPostingWorker / TestAccountingReportsEndToEnd). These cases pin the
// trigger conditions, severities, and threshold boundaries of the alert logic itself.

func TestBuildAcctDashboardAlerts_ZeroValueYieldsNothing(t *testing.T) {
	assert.Empty(t, buildAcctDashboardAlerts(acctDashboardAlertInputs{}),
		"the zero value must raise no alerts (module inactive / nothing to report)")
}

func TestBuildAcctDashboardAlerts_PostingLag(t *testing.T) {
	assert.False(t, hasAlert(
		buildAcctDashboardAlerts(acctDashboardAlertInputs{PostingLagCount: 0}),
		"acct_posting_lag"), "no backlog -> no alert")

	a, ok := alertByCode(buildAcctDashboardAlerts(acctDashboardAlertInputs{
		PostingLagCount: 3, PostingLagThresholdHours: 24, PostingLagMaxAgeHours: 50,
	}), "acct_posting_lag")
	assert.True(t, ok, "a backlog older than the threshold raises the alert")
	assert.Equal(t, entity.AlertSeverityWarning, a.Severity)
}

func TestBuildAcctDashboardAlerts_ManualEntrySeverityFloor(t *testing.T) {
	assert.False(t, hasAlert(
		buildAcctDashboardAlerts(acctDashboardAlertInputs{ManualEntryCount: 0}),
		"acct_manual_entry_required"), "no manual entries -> no alert")

	a, ok := alertByCode(buildAcctDashboardAlerts(acctDashboardAlertInputs{
		ManualEntryCount: acctManualEntryWarnFloor - 1,
	}), "acct_manual_entry_required")
	assert.True(t, ok)
	assert.Equal(t, entity.AlertSeverityInfo, a.Severity, "below the warn floor -> info")

	a, ok = alertByCode(buildAcctDashboardAlerts(acctDashboardAlertInputs{
		ManualEntryCount: acctManualEntryWarnFloor,
	}), "acct_manual_entry_required")
	assert.True(t, ok)
	assert.Equal(t, entity.AlertSeverityWarning, a.Severity, "at/above the warn floor -> warning")
}

func TestBuildAcctDashboardAlerts_ReconciliationDrift(t *testing.T) {
	n := decimal.NewFromInt

	assert.False(t, hasAlert(buildAcctDashboardAlerts(acctDashboardAlertInputs{
		ReconLedger: decimal.Zero, ReconOperational: n(100),
	}), "acct_reconciliation_drift"), "zero ledger -> no alert (a % of zero is meaningless)")

	assert.False(t, hasAlert(buildAcctDashboardAlerts(acctDashboardAlertInputs{
		ReconLedger: n(1000), ReconOperational: n(995), // 0.5% <= 1% threshold
	}), "acct_reconciliation_drift"), "drift within the threshold -> no alert")

	a, ok := alertByCode(buildAcctDashboardAlerts(acctDashboardAlertInputs{
		ReconLedger: n(1000), ReconOperational: n(980), // 2% > 1% threshold
	}), "acct_reconciliation_drift")
	assert.True(t, ok, "drift beyond the threshold -> alert")
	assert.Equal(t, entity.AlertSeverityWarning, a.Severity)
}
