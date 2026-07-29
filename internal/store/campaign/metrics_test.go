package campaign

import (
	"strings"
	"testing"
)

func TestCampaignVariantMetricsStayOnABTestCohort(t *testing.T) {
	if !strings.Contains(
		normalizedSQL(campaignVariantMetricsSQL),
		"AND ecr.cohort = 'ab'",
	) {
		t.Fatal("per-variant metrics no longer constrain the ledger join to cohort='ab'")
	}
	if strings.Contains(campaignTotalMetricsSQL, "cohort = 'ab'") {
		t.Fatal("campaign-level metrics must continue aggregating all cohorts")
	}
}
