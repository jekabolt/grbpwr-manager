package entity

import "testing"

// TestStorefrontShoppingPreferenceMatchesGender guards that the storefront shopping preference's
// gendered values stay identical to GenderEnum (they are derived from it), and that "all" is a
// distinct preference-only value — not accidentally collapsed onto a gender attribute.
func TestStorefrontShoppingPreferenceMatchesGender(t *testing.T) {
	if string(StorefrontShoppingMale) != string(Male) {
		t.Errorf("StorefrontShoppingMale %q != GenderEnum Male %q", StorefrontShoppingMale, Male)
	}
	if string(StorefrontShoppingFemale) != string(Female) {
		t.Errorf("StorefrontShoppingFemale %q != GenderEnum Female %q", StorefrontShoppingFemale, Female)
	}
	// "all" is the filter superset, deliberately not GenderEnum's product attribute "unisex".
	if string(StorefrontShoppingAll) == string(Unisex) {
		t.Error("StorefrontShoppingAll must not equal GenderEnum Unisex — 'all' is a filter superset, not a product attribute")
	}
	if !IsValidStorefrontShoppingPreference(string(StorefrontShoppingMale)) ||
		!IsValidStorefrontShoppingPreference(string(StorefrontShoppingFemale)) ||
		!IsValidStorefrontShoppingPreference(string(StorefrontShoppingAll)) {
		t.Error("all three shopping preferences must validate")
	}
}
