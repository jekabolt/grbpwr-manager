package campaigndispatch

import (
	"fmt"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestAssignVariantDeterministicAndNormalized(t *testing.T) {
	variants := []int{11, 22, 33}
	first := AssignVariant(42, "Customer@Example.COM", true, 75, variants)
	for i := 0; i < 100; i++ {
		got := AssignVariant(42, " customer@example.com ", true, 75, variants)
		if got.Cohort != first.Cohort {
			t.Fatalf("cohort drift: %q != %q", got.Cohort, first.Cohort)
		}
		if (got.VariantID == nil) != (first.VariantID == nil) {
			t.Fatal("variant nilness drift")
		}
		if got.VariantID != nil && *got.VariantID != *first.VariantID {
			t.Fatalf("variant drift: %d != %d", *got.VariantID, *first.VariantID)
		}
	}
}

func TestAssignVariantNonABUsesSoleVariant(t *testing.T) {
	got := AssignVariant(7, "x@example.com", false, 0, []int{91})
	if got.Cohort != entity.EmailCampaignCohortRemainder ||
		got.VariantID == nil || *got.VariantID != 91 {
		t.Fatalf("assignment = %#v", got)
	}
}

func TestAssignVariantABDistribution(t *testing.T) {
	const total = 10000
	ab := 0
	variantCounts := map[int]int{}
	for i := 0; i < total; i++ {
		got := AssignVariant(99, fmt.Sprintf("person-%d@example.com", i), true, 40, []int{1, 2, 3, 4})
		if got.Cohort == entity.EmailCampaignCohortAB {
			ab++
			variantCounts[*got.VariantID]++
		}
	}
	if ab < 3700 || ab > 4300 {
		t.Fatalf("A/B population = %d, want approximately 4000", ab)
	}
	for id, count := range variantCounts {
		if count < 800 || count > 1200 {
			t.Fatalf("variant %d count = %d, want approximately 1000", id, count)
		}
	}
}
