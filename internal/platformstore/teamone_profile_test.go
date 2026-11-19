package platformstore_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// onKeys is the sorted list of the switched-on features.
func onKeys(eff map[feature.Key]bool) []string {
	var out []string
	for k, on := range eff {
		if on {
			out = append(out, string(k))
		}
	}
	sort.Strings(out)
	return out
}

func sorted(keys ...feature.Key) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}

// productionKeys is the exact production set: the standard default-on features plus ai,
// ai_providers, local_accounts and products.
func productionKeys() []string {
	return sorted(append(feature.DefaultOn(), feature.AI, feature.AIProviders, feature.LocalAccounts, feature.Products)...)
}

func dedupe(in []string) []string {
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// TestTeam1ProfilePlan — what each profile writes gives exactly the features it promises.
func TestTeam1ProfilePlan(t *testing.T) {
	db, on, _, err := platformstore.Team1ProfilePlan("production")
	if err != nil {
		t.Fatal(err)
	}
	if db != tenant.ProfileStandard {
		t.Fatalf("production rides on team.profile %q, want standard", db)
	}
	ov := map[string]bool{}
	for _, k := range on {
		ov[string(k)] = true
	}
	if got, want := onKeys(feature.Effective(1, db, ov)), dedupe(productionKeys()); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("production features\n got %v\nwant %v", got, want)
	}

	db, on, _, err = platformstore.Team1ProfilePlan("")
	if err != nil || db != tenant.ProfileAll || len(on) != 0 {
		t.Fatalf("default profile = %q %v %v, want all with no rows", db, on, err)
	}
	if got := onKeys(feature.Effective(1, db, nil)); len(got) != len(feature.Catalog()) {
		t.Fatalf("all: %d of %d features on", len(got), len(feature.Catalog()))
	}
	if _, _, _, err := platformstore.Team1ProfilePlan("standard"); err == nil {
		t.Fatal("an unknown profile was accepted")
	}
}

// TestTeamOneProfileOnEveryBoot — TENANT_TEAM1_PROFILE is applied on create and on every boot,
// and a switch an operator made survives every reboot and every profile change.
func TestTeamOneProfileOnEveryBoot(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	admins := []platformstore.TeamOneAdmin{{Username: "boss", PasswordHash: "h", Super: true}}
	boot := func(profile string) map[feature.Key]bool {
		t.Helper()
		if err := st.EnsureTeamOneProfile(ctx, "grbpwr_test", admins, profile); err != nil {
			t.Fatalf("boot %s: %v", profile, err)
		}
		team, err := st.Directory().ByID(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		ov, err := st.FeatureOverrides(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		return feature.Effective(1, team.Profile, ov)
	}

	// Create with production: exactly the production set.
	if got, want := onKeys(boot("production")), dedupe(productionKeys()); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("create production\n got %v\nwant %v", got, want)
	}

	// The operator turns products off and orders on (updated_by set).
	if err := st.SetFeature(ctx, 1, string(feature.Products), false, 7); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFeature(ctx, 1, string(feature.Orders), true, 7); err != nil {
		t.Fatal(err)
	}
	withOperator := func(base []string) []string {
		out := []string{string(feature.Orders)}
		for _, k := range base {
			if k != string(feature.Products) {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return dedupe(out)
	}
	if got, want := onKeys(boot("production")), withOperator(productionKeys()); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("reboot production\n got %v\nwant %v", got, want)
	}

	// Switch to all: every feature on except what the operator turned off.
	eff := boot("all")
	if eff[feature.Products] || !eff[feature.Orders] || !eff[feature.Analytics] {
		t.Fatalf("reboot all: products=%v orders=%v analytics=%v", eff[feature.Products], eff[feature.Orders], eff[feature.Analytics])
	}
	ov, err := st.FeatureOverrides(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ov[string(feature.AIProviders)]; ok {
		t.Fatalf("all left the production system row ai_providers behind: %v", ov)
	}

	// And back: the production rows return, the operator's switches still stand.
	if got, want := onKeys(boot("production")), withOperator(productionKeys()); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("back to production\n got %v\nwant %v", got, want)
	}
}
