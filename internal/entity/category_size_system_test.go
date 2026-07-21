package entity

import (
	"database/sql"
	"strings"
	"testing"
)

func validID(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

// Fixture tree, mirroring the real seed shape (0175) at a miniature scale:
//
//	1 outerwear (top)
//	  10 jackets (sub)
//	    100 blazer (type)
//	    101 bomber (type)
//	2 shoes (top)
//	3 bags (top, no grid at all)
const (
	catOuterwear = 1
	catShoes     = 2
	catBags      = 3
	catJackets   = 10
	catBlazer    = 100
	catBomber    = 101
)

func fixtureRules() []CategorySizeSystem {
	return []CategorySizeSystem{
		{ID: 1, CategoryID: validID(catOuterwear), SkuSystem: SizeSKUSystemApparel},
		{ID: 2, CategoryID: validID(catShoes), SkuSystem: SizeSKUSystemShoe},
		{ID: 3, TypeID: validID(catBlazer), SkuSystem: SizeSKUSystemApparel},
		{ID: 4, TypeID: validID(catBlazer), SkuSystem: SizeSKUSystemCompositeTA},
		// bags: no rows at all.
	}
}

func sz(name string, system SizeSKUSystem) Size {
	return Size{Name: name, SkuSystem: system}
}

func TestResolveSizeSystemPolicy_Unrestricted(t *testing.T) {
	// No category assigned at all -> unrestricted (nothing to validate against yet).
	policy := ResolveSizeSystemPolicy(StyleCategoryPath{}, fixtureRules())
	if !policy.Unrestricted {
		t.Fatalf("expected Unrestricted, got %+v", policy)
	}
	if !policy.Allows(sz("42", SizeSKUSystemShoe)) {
		t.Error("Unrestricted policy must allow any size")
	}
}

func TestResolveSizeSystemPolicy_TopCategoryMatch(t *testing.T) {
	path := StyleCategoryPath{TopCategoryID: validID(catOuterwear)}
	policy := ResolveSizeSystemPolicy(path, fixtureRules())
	if policy.Unrestricted || policy.OSFallback {
		t.Fatalf("expected a systems policy, got %+v", policy)
	}
	if !policy.Systems[SizeSKUSystemApparel] {
		t.Errorf("expected apparel allowed for outerwear, got %+v", policy.Systems)
	}
	if policy.Systems[SizeSKUSystemShoe] {
		t.Errorf("shoe must not be allowed for outerwear, got %+v", policy.Systems)
	}
	if !policy.Allows(sz("m", SizeSKUSystemApparel)) {
		t.Error("apparel size should be allowed")
	}
	if policy.Allows(sz("42", SizeSKUSystemShoe)) {
		t.Error("shoe size must be rejected for outerwear")
	}
}

func TestResolveSizeSystemPolicy_TypeMostSpecificWins(t *testing.T) {
	// A style filed under outerwear > jackets > blazer: the type-level rule (apparel + composite_ta)
	// must win outright over the top-level outerwear rule (apparel only) -- most-specific-first.
	path := StyleCategoryPath{
		TopCategoryID: validID(catOuterwear),
		SubCategoryID: validID(catJackets),
		TypeID:        validID(catBlazer),
	}
	policy := ResolveSizeSystemPolicy(path, fixtureRules())
	if len(policy.Systems) != 2 || !policy.Systems[SizeSKUSystemApparel] || !policy.Systems[SizeSKUSystemCompositeTA] {
		t.Fatalf("expected {apparel, composite_ta} for blazer, got %+v", policy.Systems)
	}
	if !policy.Allows(sz("m_38ta_f", SizeSKUSystemCompositeTA)) {
		t.Error("composite_ta size should be allowed for blazer")
	}
	if !policy.Allows(sz("m", SizeSKUSystemApparel)) {
		t.Error("apparel size should still be allowed for blazer (additive, not a replacement)")
	}
	if policy.Allows(sz("42", SizeSKUSystemShoe)) {
		t.Error("shoe size must be rejected for blazer")
	}
}

func TestResolveSizeSystemPolicy_TypeWithNoRuleFallsBackToParent(t *testing.T) {
	// bomber has no type-level rule -> falls back to sub_category_id (none here either) -> top
	// (outerwear -> apparel).
	path := StyleCategoryPath{
		TopCategoryID: validID(catOuterwear),
		SubCategoryID: validID(catJackets),
		TypeID:        validID(catBomber),
	}
	policy := ResolveSizeSystemPolicy(path, fixtureRules())
	if len(policy.Systems) != 1 || !policy.Systems[SizeSKUSystemApparel] {
		t.Fatalf("expected {apparel} for bomber (inherited from outerwear), got %+v", policy.Systems)
	}
}

func TestResolveSizeSystemPolicy_OSFallback(t *testing.T) {
	// bags: category IS set, but nothing on its path is mapped -> OS-fallback, not unrestricted.
	path := StyleCategoryPath{TopCategoryID: validID(catBags)}
	policy := ResolveSizeSystemPolicy(path, fixtureRules())
	if !policy.OSFallback {
		t.Fatalf("expected OSFallback for an ungridded category, got %+v", policy)
	}
	if !policy.Allows(sz("os", SizeSKUSystemApparel)) {
		t.Error("the 'os' size must be allowed under OS-fallback")
	}
	if policy.Allows(sz("m", SizeSKUSystemApparel)) {
		t.Error("a non-os apparel size must be rejected under OS-fallback")
	}
	if policy.Allows(sz("42", SizeSKUSystemShoe)) {
		t.Error("a shoe size must be rejected under OS-fallback")
	}
}

func TestValidateSizeAgainstCategory_Message(t *testing.T) {
	path := StyleCategoryPath{TopCategoryID: validID(catShoes)}
	err := ValidateSizeAgainstCategory("size_id", path, "shoes", fixtureRules(), sz("m", SizeSKUSystemApparel))
	if err == nil {
		t.Fatal("expected a validation error")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "size_id" {
		t.Errorf("Field = %q, want %q", ve.Field, "size_id")
	}
	want := "size system apparel not allowed for category shoes, allowed: shoe"
	if !strings.Contains(ve.Reason, want) {
		t.Errorf("Reason = %q, want it to contain %q", ve.Reason, want)
	}
}

func TestValidateSizeAgainstCategory_Allowed(t *testing.T) {
	path := StyleCategoryPath{TopCategoryID: validID(catShoes)}
	if err := ValidateSizeAgainstCategory("size_id", path, "shoes", fixtureRules(), sz("42", SizeSKUSystemShoe)); err != nil {
		t.Fatalf("expected no error for a permitted size, got %v", err)
	}
}

func TestValidateSizeAgainstCategory_EmptyLabelFallsBack(t *testing.T) {
	path := StyleCategoryPath{TopCategoryID: validID(catBags)}
	err := ValidateSizeAgainstCategory("size_id", path, "", fixtureRules(), sz("m", SizeSKUSystemApparel))
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %v", err)
	}
	if !strings.Contains(ve.Reason, "this style") {
		t.Errorf("Reason = %q, want it to fall back to \"this style\" when categoryLabel is empty", ve.Reason)
	}
}

func TestStyleCategoryPath_MostSpecificID(t *testing.T) {
	tests := []struct {
		name   string
		path   StyleCategoryPath
		wantID int
		wantOK bool
	}{
		{"none set", StyleCategoryPath{}, 0, false},
		{"top only", StyleCategoryPath{TopCategoryID: validID(catOuterwear)}, catOuterwear, true},
		{"top+sub", StyleCategoryPath{TopCategoryID: validID(catOuterwear), SubCategoryID: validID(catJackets)}, catJackets, true},
		{"top+sub+type", StyleCategoryPath{TopCategoryID: validID(catOuterwear), SubCategoryID: validID(catJackets), TypeID: validID(catBlazer)}, catBlazer, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := tt.path.MostSpecificID()
			if id != tt.wantID || ok != tt.wantOK {
				t.Errorf("MostSpecificID() = (%d, %v), want (%d, %v)", id, ok, tt.wantID, tt.wantOK)
			}
		})
	}
}

// The `dresses` shape: level-3 types hang DIRECTLY off the level-1 top category, so a dress style
// resolves with a SET type and a NULL sub-category (0001_initial_setup.sql, "Dresses types (no
// sub-category)"). That combination is only produced once tech-card writes derive the triple from
// category_id, so pin here that resolution handles it -- the sub-category step must be skipped, not
// treated as an unset category, and the outcome must never degrade to Unrestricted.
const (
	catDresses  = 4   // top, has a grid
	catMiniDres = 400 // type, child of catDresses directly -- no sub-category between them
	catMeshDres = 401 // type, child of catDresses, deliberately has no rule of its own
)

func dressRules() []CategorySizeSystem {
	return []CategorySizeSystem{
		{ID: 10, CategoryID: validID(catDresses), SkuSystem: SizeSKUSystemApparel},
		{ID: 11, TypeID: validID(catMiniDres), SkuSystem: SizeSKUSystemCompositeTA},
	}
}

func TestResolveSizeSystemPolicy_DressTypeWithNullSub(t *testing.T) {
	// A type-level rule wins outright even though the sub-category step in between is NULL.
	path := StyleCategoryPath{TopCategoryID: validID(catDresses), TypeID: validID(catMiniDres)}
	policy := ResolveSizeSystemPolicy(path, dressRules())
	if policy.Unrestricted || policy.OSFallback {
		t.Fatalf("expected a systems policy for a dress type, got %+v", policy)
	}
	if !policy.Systems[SizeSKUSystemCompositeTA] {
		t.Errorf("expected the type rule to win, got %+v", policy.Systems)
	}
	if policy.Systems[SizeSKUSystemApparel] {
		t.Errorf("the top rule must not leak in when a type rule matched, got %+v", policy.Systems)
	}
}

func TestResolveSizeSystemPolicy_DressTypeFallsThroughNullSubToTop(t *testing.T) {
	// No type rule for this dress type: resolution must step OVER the NULL sub-category and land on
	// the top-category rule rather than stopping or falling back to OS.
	path := StyleCategoryPath{TopCategoryID: validID(catDresses), TypeID: validID(catMeshDres)}
	policy := ResolveSizeSystemPolicy(path, dressRules())
	if policy.Unrestricted {
		t.Fatalf("a dress with a set top category must never be Unrestricted, got %+v", policy)
	}
	if policy.OSFallback {
		t.Fatalf("expected the top-category rule to match, got OS fallback: %+v", policy)
	}
	if !policy.Systems[SizeSKUSystemApparel] {
		t.Errorf("expected apparel from the dresses top rule, got %+v", policy.Systems)
	}
}

func TestStyleCategoryPath_MostSpecificID_DressTypeWithNullSub(t *testing.T) {
	path := StyleCategoryPath{TopCategoryID: validID(catDresses), TypeID: validID(catMiniDres)}
	id, ok := path.MostSpecificID()
	if !ok || id != catMiniDres {
		t.Errorf("MostSpecificID() = (%d, %v), want (%d, true) — the type must label a dress even with a NULL sub", id, ok, catMiniDres)
	}
}
