package tenantbind

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// errDir is a directory whose ByID fails once err is set.
type errDir struct {
	*mapDir
	err error
}

func (d *errDir) ByID(ctx context.Context, id int64) (tenant.Team, error) {
	if d.err != nil {
		return tenant.Team{}, d.err
	}
	return d.mapDir.ByID(ctx, id)
}

type errDirPlane struct {
	*fakePlane
	dir *errDir
}

func (p errDirPlane) Directory() tenant.Directory { return p.dir }

// teamOneRow is team #1 as the control plane stores it.
func teamOneRow(profile string) tenant.Team {
	t := tenant.TeamOne("grbpwr_beta")
	t.Profile = profile
	return t
}

// teamOneBinder is a teams-mode binder whose directory holds team #1 with the given row profile and
// overrides.
func teamOneBinder(t *testing.T, profile string, overrides map[string]bool) (*Binder, *fakePlane, *errDir) {
	t.Helper()
	b := newBinder(t)
	dir := &errDir{mapDir: &mapDir{teams: map[int64]tenant.Team{tenant.TeamOneID: teamOneRow(profile), 7: acme}}}
	fp := &fakePlane{dir: dir.mapDir, overrides: map[int64]map[string]bool{tenant.TeamOneID: overrides}}
	b.usePlane(errDirPlane{fakePlane: fp, dir: dir}, platformstore.Config{BaseDomain: "beta.lekalo.studio"})
	return b, fp, dir
}

// listOf is the console's rendering of an effective set: catalog order, switched-on keys only.
func listOf(eff map[feature.Key]bool) []feature.Key {
	var out []feature.Key
	for _, info := range feature.Catalog() {
		if eff[info.Key] {
			out = append(out, info.Key)
		}
	}
	return out
}

func TestTeamOneFeaturesUseStoredProfile(t *testing.T) {
	ov := map[string]bool{
		string(feature.Products): true, string(feature.AIProviders): true, string(feature.LocalAccounts): true,
	}
	b, _, _ := teamOneBinder(t, tenant.ProfileStandard, ov)
	// The request path hands the binder the boot-time team (profile all).
	eff, err := b.Features(context.Background(), teamOne)
	if err != nil {
		t.Fatal(err)
	}
	want := []feature.Key{feature.Tasks, feature.Files, feature.Products, feature.Media, feature.Models,
		feature.Fittings, feature.TechCards, feature.Materials, feature.Production, feature.AI,
		feature.AIProviders, feature.LocalAccounts}
	got := listOf(eff)
	gotSorted, wantSorted := slices.Clone(got), slices.Clone(want)
	slices.Sort(gotSorted)
	slices.Sort(wantSorted)
	if !slices.Equal(gotSorted, wantSorted) {
		t.Fatalf("team #1 standard features = %v, want %v", got, want)
	}
	for _, k := range []feature.Key{feature.Promo, feature.Hero, feature.Archive, feature.EmailCampaigns,
		feature.Analytics, feature.Orders, feature.Accounting, feature.Settings} {
		if eff[k] {
			t.Fatalf("%s on for team #1 with profile standard", k)
		}
	}
	// Equal by construction to the console's computation from the same row.
	console := feature.Effective(tenant.TeamOneID, tenant.ProfileStandard, ov)
	if !slices.Equal(listOf(console), got) {
		t.Fatalf("workspace %v != console %v", got, listOf(console))
	}
}

func TestTeamOneFeaturesProfileAllEverythingOn(t *testing.T) {
	b, _, _ := teamOneBinder(t, tenant.ProfileAll, nil)
	eff, err := b.Features(context.Background(), teamOne)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range feature.Catalog() {
		if !eff[info.Key] {
			t.Fatalf("%s off for team #1 with profile all", info.Key)
		}
	}
}

func TestTeamOneFeaturesModeOffEverythingOn(t *testing.T) {
	b := newBinder(t)
	eff, err := b.Features(context.Background(), teamOne)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range feature.Catalog() {
		if !eff[info.Key] {
			t.Fatalf("%s off for team #1 with teams mode off", info.Key)
		}
	}
}

func TestTeamOneFeaturesOverrideOffHonoured(t *testing.T) {
	b, _, _ := teamOneBinder(t, tenant.ProfileStandard, map[string]bool{string(feature.Tasks): false})
	eff, err := b.Features(context.Background(), teamOne)
	if err != nil {
		t.Fatal(err)
	}
	if eff[feature.Tasks] {
		t.Fatal("override off on a default-on key ignored for team #1")
	}
	if !eff[feature.Files] {
		t.Fatal("default-on key off without an override")
	}
}

func TestTeamOneFeaturesDirectoryErrorIsNotAll(t *testing.T) {
	b, _, dir := teamOneBinder(t, tenant.ProfileStandard, nil)
	dir.err = errors.New("plane down")
	eff, err := b.Features(context.Background(), teamOne)
	if err == nil {
		t.Fatalf("directory error swallowed: %v", listOf(eff))
	}
	if eff != nil {
		t.Fatal("features returned with an error")
	}
	// TeamByID for team #1 stays the boot team without a plane read.
	if got, err := b.TeamByID(context.Background(), tenant.TeamOneID); err != nil || got.DBName != teamOne.DBName {
		t.Fatalf("TeamByID(1) during a plane blip = %v, %v", got, err)
	}
	// Recovery: the error was not cached.
	dir.err = nil
	eff, err = b.Features(context.Background(), teamOne)
	if err != nil || eff[feature.Promo] || !eff[feature.Tasks] {
		t.Fatalf("after recovery: %v %v", listOf(eff), err)
	}
}

func TestTeamOneFeaturesMissingRowIsError(t *testing.T) {
	b, _, dir := teamOneBinder(t, tenant.ProfileStandard, nil)
	delete(dir.teams, tenant.TeamOneID)
	if _, err := b.Features(context.Background(), teamOne); err == nil {
		t.Fatal("missing team #1 row fell back to a profile")
	}
}

func TestOtherTeamFeaturesUnchanged(t *testing.T) {
	b, _, dir := teamOneBinder(t, tenant.ProfileAll, nil)
	dir.err = errors.New("plane down") // other teams never read the directory here
	eff, err := b.Features(context.Background(), acme)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(listOf(eff), listOf(feature.Effective(7, tenant.ProfileStandard, nil))) {
		t.Fatalf("acme features changed: %v", listOf(eff))
	}
}
