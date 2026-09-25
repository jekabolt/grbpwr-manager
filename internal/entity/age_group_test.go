package entity

import (
	"testing"
)

// TestValidateStyleAgeGroup pins the write rule for StylePatch.AgeGroup (0366) in all three mask
// shapes. The rule is shared by the UpdateStyle handler and the store, so the cases here are the
// whole contract: named ⇒ a member is required; full replace ⇒ "" keeps the stored value but a
// non-member is refused; a mask that does not name it ⇒ nothing is checked, because nothing is
// written.
func TestValidateStyleAgeGroup(t *testing.T) {
	tests := []struct {
		name        string
		ag          AgeGroupEnum
		named       bool
		fullReplace bool
		wantReason  string // "" = accepted
	}{
		{"named member", AgeGroupKids, true, false, ""},
		{"named empty is required", "", true, false, "required"},
		{"named non-member", "senior", true, false, "unknown_age_group"},
		{"full replace empty keeps the stored value", "", false, true, ""},
		{"full replace member", AgeGroupBaby, false, true, ""},
		{"full replace non-member", "senior", false, true, "unknown_age_group"},
		{"unnamed mask ignores empty", "", false, false, ""},
		{"unnamed mask ignores a non-member it will not write", "senior", false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ve := ValidateStyleAgeGroup(tt.ag, tt.named, tt.fullReplace)
			if tt.wantReason == "" {
				if ve != nil {
					t.Fatalf("expected acceptance, got %v", ve)
				}
				return
			}
			if ve == nil {
				t.Fatalf("expected a %q refusal, got acceptance", tt.wantReason)
			}
			if ve.Field != "age_group" {
				t.Errorf("violation field = %q, want age_group — the client binds the refusal by it", ve.Field)
			}
			if ve.Reason != tt.wantReason {
				t.Errorf("violation reason = %q, want %q", ve.Reason, tt.wantReason)
			}
		})
	}
}

// TestValidAgeGroupsIsTheClosedVocabulary: five members, and "" is not one of them — the empty
// token is "no value" on every path, never a storable age group.
func TestValidAgeGroupsIsTheClosedVocabulary(t *testing.T) {
	for _, ag := range []AgeGroupEnum{AgeGroupAdult, AgeGroupTeen, AgeGroupKids, AgeGroupToddler, AgeGroupBaby} {
		if !IsValidAgeGroup(ag) {
			t.Errorf("%q must be a valid age group", ag)
		}
	}
	if len(ValidAgeGroups) != 5 {
		t.Errorf("ValidAgeGroups has %d members, want 5 (adult/teen/kids/toddler/baby)", len(ValidAgeGroups))
	}
	for _, ag := range []AgeGroupEnum{"", "ADULT", "Kids", "child", "unisex"} {
		if IsValidAgeGroup(ag) {
			t.Errorf("%q must not be a valid age group", ag)
		}
	}
}

// TestAgeGroupEnumScan mirrors GenderEnum's NULL tolerance: one NULL must not fail a whole
// multi-row read.
func TestAgeGroupEnumScan(t *testing.T) {
	var ag AgeGroupEnum = AgeGroupKids
	if err := ag.Scan(nil); err != nil || ag != "" {
		t.Errorf("Scan(nil) = %q, %v; want \"\", nil", ag, err)
	}
	if err := ag.Scan("teen"); err != nil || ag != AgeGroupTeen {
		t.Errorf("Scan(string) = %q, %v; want teen", ag, err)
	}
	if err := ag.Scan([]byte("baby")); err != nil || ag != AgeGroupBaby {
		t.Errorf("Scan([]byte) = %q, %v; want baby", ag, err)
	}
	if err := ag.Scan(42); err == nil {
		t.Error("Scan(int) must fail: the column is a string")
	}
}
