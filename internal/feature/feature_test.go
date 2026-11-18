package feature

import (
	"slices"
	"sort"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

func TestDefaultOnGolden(t *testing.T) {
	want := []Key{
		Tasks,
		Files,
		Media,
		Models,
		Fittings,
		Materials,
		Production,
		TechCards,
		AI,
	}
	if got := DefaultOn(); !slices.Equal(got, want) {
		t.Fatalf("DefaultOn() = %v, want %v", got, want)
	}

	flags := make(map[Key]bool, len(catalog))
	for _, info := range Catalog() {
		flags[info.Key] = info.DefaultOn
	}
	for _, key := range want {
		if !flags[key] {
			t.Errorf("catalog does not mark %q default-on", key)
		}
		delete(flags, key)
	}
	for key, on := range flags {
		if on {
			t.Errorf("catalog unexpectedly marks %q default-on", key)
		}
	}
}

func TestCatalogHasStableKeysAndGroups(t *testing.T) {
	wantGroups := map[string][]Key{
		"operations": {Analytics, Accounting, Orders, Fulfillment, Tasks, Files},
		"catalog":    {Products, Media, Hero, Promo, EmailCampaigns, Archive},
		"production": {Models, Fittings, TechCards, Materials, Production},
		"members":    {Membership, Support},
		"admin":      {Settings, AI, AIProviders, LocalAccounts},
	}
	gotGroups := make(map[string][]Key, len(wantGroups))
	seen := make(map[Key]bool, len(catalog))
	for _, info := range Catalog() {
		if seen[info.Key] {
			t.Errorf("duplicate catalog key %q", info.Key)
		}
		seen[info.Key] = true
		if info.Title == "" || info.Title != lower(info.Title) {
			t.Errorf("title for %q must be lowercase, got %q", info.Key, info.Title)
		}
		gotGroups[info.Group] = append(gotGroups[info.Group], info.Key)
	}
	for group, want := range wantGroups {
		if got := gotGroups[group]; !slices.Equal(got, want) {
			t.Errorf("group %q = %v, want %v", group, got, want)
		}
		delete(gotGroups, group)
	}
	if len(gotGroups) != 0 {
		t.Errorf("unexpected catalog groups: %v", gotGroups)
	}
	if Valid(Core) {
		t.Error("core must not be a switchable catalog key")
	}
	if Valid(Key("unknown")) {
		t.Error("unknown key reported valid")
	}
	for _, key := range []Key{AIProviders, LocalAccounts} {
		if !catalogInfo(key).TeamOneOnly {
			t.Errorf("%q must be team-one-only", key)
		}
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

func catalogInfo(key Key) Info {
	for _, info := range catalog {
		if info.Key == key {
			return info
		}
	}
	return Info{}
}

func TestEveryAdminMethodHasFeature(t *testing.T) {
	for _, method := range pb_admin.AdminService_ServiceDesc.Methods {
		keys, ok := ForMethod(rbac.MethodPrefix + method.MethodName)
		if !ok {
			t.Errorf("admin method %s has no feature mapping", method.MethodName)
			continue
		}
		if len(keys) == 0 {
			t.Errorf("admin method %s has an empty feature mapping", method.MethodName)
		}
		for _, key := range keys {
			if key != Core && !Valid(key) {
				t.Errorf("admin method %s maps to unknown feature %q", method.MethodName, key)
			}
		}
	}
}

func TestOverridesNameRealMethods(t *testing.T) {
	live := make(map[string]bool, len(pb_admin.AdminService_ServiceDesc.Methods))
	for _, method := range pb_admin.AdminService_ServiceDesc.Methods {
		live[method.MethodName] = true
	}

	for name := range methodOverrides {
		if !live[name] {
			t.Errorf("override %q has no AdminService method", name)
		}
	}
}

func TestAISpendingRPCsRequireDomainAndAI(t *testing.T) {
	// This table is deliberately independent of methodOverrides: it pins the
	// result of the handler sweep so adding or removing a paid RPC is a reviewable
	// test change.
	want := []struct {
		name   string
		domain Key
	}{
		{name: "AutoTranslateEmailCampaign", domain: EmailCampaigns},
		{name: "DraftFittingEdits", domain: Fittings},
		{name: "FormatLibraryNoteMarkdown", domain: Files},
		{name: "EnhanceText", domain: TechCards},
		{name: "SuggestPrompts", domain: TechCards},
		{name: "SuggestCallouts", domain: TechCards},
		{name: "AnalyzeTechCardConstruction", domain: TechCards},
		{name: "SuggestPatternPieces", domain: TechCards},
		{name: "ClassifyPatternPages", domain: TechCards},
		{name: "SuggestAssemblySkeleton", domain: TechCards},
		{name: "SuggestTechCardSeamHints", domain: TechCards},
		{name: "StartDesignRun", domain: TechCards},
		{name: "DraftDesignIdea", domain: TechCards},
		{name: "GenerateDesignQuiz", domain: TechCards},
		{name: "SuggestDesignParts", domain: TechCards},
		{name: "SuggestDesignPartsCard", domain: TechCards},
		{name: "GenerateDesignJoins", domain: TechCards},
	}

	seen := make(map[string]bool, len(want))
	for _, tc := range want {
		seen[tc.name] = true
		got, ok := ForMethod(rbac.MethodPrefix + tc.name)
		if !ok {
			t.Errorf("%s is unmapped", tc.name)
			continue
		}
		if expected := []Key{tc.domain, AI}; !slices.Equal(got, expected) {
			t.Errorf("%s requires %v, want %v", tc.name, got, expected)
		}
	}

	var extra []string
	for name, keys := range methodOverrides {
		if slices.Contains(keys, AI) && !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	if len(extra) != 0 {
		t.Errorf("unlisted AI-spending overrides: %v", extra)
	}
}

func TestExplicitMethodOverrides(t *testing.T) {
	tests := []struct {
		name string
		want []Key
	}{
		{name: "CreateMaterial", want: []Key{Materials}},
		{name: "GetCostingFxRates", want: []Key{Materials}},
		{name: "ListSuppliers", want: []Key{Materials}},
		{name: "GetAiProvidersConfig", want: []Key{AIProviders}},
		{name: "CreateAccount", want: []Key{LocalAccounts}},
		{name: "GetCurrentAccount", want: []Key{Core}},
	}
	for _, tc := range tests {
		got, ok := ForMethod(rbac.MethodPrefix + tc.name)
		if !ok || !slices.Equal(got, tc.want) {
			t.Errorf("ForMethod(%s) = %v, %v; want %v, true", tc.name, got, ok, tc.want)
		}
	}
	if keys, ok := ForMethod("/other.Service/CreateMaterial"); ok || keys != nil {
		t.Errorf("non-admin method mapped as %v, %v", keys, ok)
	}
}

func TestForRoute(t *testing.T) {
	tests := []struct {
		method  string
		pattern string
		want    Key
	}{
		{method: "POST", pattern: "/api/files/upload", want: Files},
		{method: "POST", pattern: "/api/files/{id}/preview", want: Files},
		{method: "POST", pattern: "/api/techcard-archive/upload", want: TechCards},
		{method: "GET", pattern: "/api/p/{token}", want: Core},
		{method: "HEAD", pattern: "/api/p/{token}", want: Core},
		{method: "GET", pattern: "/api/pv/{token}", want: Core},
		{method: "HEAD", pattern: "/api/pv/{token}", want: Core},
		{method: "GET", pattern: "/api/rp/{token}", want: Core},
		{method: "HEAD", pattern: "/api/rp/{token}", want: Core},
		{method: "GET", pattern: "/api/f/{token}", want: Core},
		{method: "HEAD", pattern: "/api/f/{token}", want: Core},
	}
	for _, tc := range tests {
		got, ok := ForRoute(tc.method, tc.pattern)
		if !ok || !slices.Equal(got, []Key{tc.want}) {
			t.Errorf("ForRoute(%s %s) = %v, %v; want [%s], true", tc.method, tc.pattern, got, ok, tc.want)
		}
	}
	for _, unknown := range []route{
		{method: "POST", pattern: "/api/unknown"},
		{method: "GET", pattern: "/api/files/upload"},
		{method: "get", pattern: "/api/p/{token}"},
	} {
		if keys, ok := ForRoute(unknown.method, unknown.pattern); ok || keys != nil {
			t.Errorf("unknown route %s %s mapped as %v, %v", unknown.method, unknown.pattern, keys, ok)
		}
	}
}

func TestEffective(t *testing.T) {
	t.Run("team one all", func(t *testing.T) {
		got := Effective(1, "all", nil)
		for _, info := range catalog {
			if !got[info.Key] {
				t.Errorf("%q is off", info.Key)
			}
		}
	})

	t.Run("team two standard", func(t *testing.T) {
		got := Effective(2, "standard", nil)
		on := make(map[Key]bool, len(defaultOn))
		for _, key := range defaultOn {
			on[key] = true
		}
		for _, info := range catalog {
			if got[info.Key] != on[info.Key] {
				t.Errorf("%q = %v, want %v", info.Key, got[info.Key], on[info.Key])
			}
		}
	})

	t.Run("overrides", func(t *testing.T) {
		got := Effective(2, "standard", map[string]bool{
			string(Tasks):  false,
			string(Orders): true,
			"unknown":      true,
		})
		if got[Tasks] || !got[Orders] {
			t.Errorf("overrides not applied: tasks=%v orders=%v", got[Tasks], got[Orders])
		}
		if len(got) != len(catalog) {
			t.Errorf("unknown override leaked into result: len=%d, want %d", len(got), len(catalog))
		}
	})

	t.Run("team one only forced off", func(t *testing.T) {
		got := Effective(2, "all", map[string]bool{
			string(AIProviders):   true,
			string(LocalAccounts): true,
		})
		if got[AIProviders] || got[LocalAccounts] {
			t.Errorf("team-one-only features enabled: ai_providers=%v local_accounts=%v", got[AIProviders], got[LocalAccounts])
		}
		for _, info := range catalog {
			if !info.TeamOneOnly && !got[info.Key] {
				t.Errorf("profile all left %q off", info.Key)
			}
		}
	})

	t.Run("team one standard override", func(t *testing.T) {
		got := Effective(1, "standard", map[string]bool{string(AIProviders): true})
		if !got[AIProviders] || got[LocalAccounts] {
			t.Errorf("team one overrides/defaults wrong: ai_providers=%v local_accounts=%v", got[AIProviders], got[LocalAccounts])
		}
	})
}

func TestAllowed(t *testing.T) {
	effective := map[Key]bool{Files: true, AI: true, TechCards: false}
	tests := []struct {
		name string
		keys []Key
		want bool
	}{
		{name: "empty", keys: nil, want: false},
		{name: "core", keys: []Key{Core}, want: true},
		{name: "enabled", keys: []Key{Files}, want: true},
		{name: "all enabled", keys: []Key{Core, Files, AI}, want: true},
		{name: "one disabled", keys: []Key{TechCards, AI}, want: false},
		{name: "unknown", keys: []Key{Key("unknown")}, want: false},
	}
	for _, tc := range tests {
		if got := Allowed(effective, tc.keys); got != tc.want {
			t.Errorf("%s: Allowed(%v) = %v, want %v", tc.name, tc.keys, got, tc.want)
		}
	}
}
