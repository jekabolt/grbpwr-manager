package entity

import "testing"

// TestDraftIsAValidRunStatus locks the value set: `draft` must be accepted everywhere a status is
// validated, or a calculation batch cannot be saved at all.
func TestDraftIsAValidRunStatus(t *testing.T) {
	if !IsValidProductionRunStatus(ProductionRunDraft) {
		t.Fatal("draft is not an accepted run status")
	}
	if ProductionRunDraft == ProductionRunPlanned {
		t.Fatal("draft collapsed into planned; the whole point is that it carries no obligations")
	}
}

// TestDraftStringMatchesTheColumnConstraint keeps the stored value in step with the CHECK the
// migration writes. A mismatch does not fail a test suite — it fails an INSERT, in production,
// on the first calculation somebody tries to save.
func TestDraftStringMatchesTheColumnConstraint(t *testing.T) {
	if string(ProductionRunDraft) != "draft" {
		t.Fatalf("stored value is %q; migration 0298 admits only 'draft'", ProductionRunDraft)
	}
}
