package migrationlint

import (
	"regexp"
	"sort"
	"testing"
)

// This file extends migrationlint's static, database-free guards (see doc.go) to the category ->
// size-system SEED itself (S10/WS5, migration 0175), not just its enum CHECK (see
// TestCategorySizeSystemDBCheckNoDrift in enum_drift_test.go). 0175's seed is name-keyed
// (`INSERT ... SELECT ... WHERE c.name = 'outerwear'`), and MySQL's classic footgun applies: a
// misspelled name matches zero rows and raises no error, silently dropping that category's mapping
// instead of failing loudly. These tests catch that class of typo statically, and pin the documented
// mapping (the WS5 Dump / 0175's header comment) so a future edit that drifts from either fails here.

// categorySizeSystemMappingPairRe matches one `SELECT 'category_name'[ AS name], 'system'[ AS
// size_system]` row from 0175's top-level seed mapping subquery. It requires TWO quoted string
// literals per SELECT so it does not match the single-value SELECTs used elsewhere in the same
// migration (the AUTONOMOUS-CALL composite_ta/composite_bo CROSS JOIN subqueries).
var categorySizeSystemMappingPairRe = regexp.MustCompile(`SELECT\s+'([a-z_]+)'(?:\s+AS\s+name)?,\s*'([a-z_]+)'(?:\s+AS\s+size_system)?`)

// topLevelCategoryRe matches one `('name', 1, NULL)` row from 0001's top-level category seed.
var topLevelCategoryRe = regexp.MustCompile(`\('([a-z_]+)',\s*1,\s*NULL\)`)

// wantCategorySizeSystemMapping is the top-level category -> permitted system(s) mapping WS5 commits
// to (0175's header comment / the WS5 Dump carry the full rationale). Pinning it here means the
// migration text and the documented intent cannot silently drift apart.
var wantCategorySizeSystemMapping = map[string][]string{
	"outerwear":            {"apparel"},
	"tops":                 {"apparel"},
	"bottoms":              {"apparel"},
	"dresses":              {"apparel"},
	"loungewear_sleepwear": {"apparel"},
	"accessories":          {"apparel"},
	"shoes":                {"shoe"},
}

// wantUngriddedTopLevelCategories are top-level categories that are real (present in 0001) but
// deliberately carry NO category_size_system row -- OS-fallback by design (entity.
// ResolveSizeSystemPolicy), not an oversight.
var wantUngriddedTopLevelCategories = []string{"bags", "objects"}

// TestCategorySizeSystemMappingCoversKnownCategories cross-checks 0175's seed against 0001's category
// seed and the documented mapping above:
//  1. every category name 0175 maps is a real top-level (level_id=1) category from 0001 (typo guard);
//  2. the extracted (name -> systems) pairs match wantCategorySizeSystemMapping exactly;
//  3. every entry in wantUngriddedTopLevelCategories is a real top-level category that 0175 leaves
//     unmapped (still real, not a typo of something else -- and still unmapped, not accidentally
//     picked up by a later edit).
func TestCategorySizeSystemMappingCoversKnownCategories(t *testing.T) {
	seedCategories := readMigrationFile(t, "0001_initial_setup.sql")
	topLevel := map[string]bool{}
	for _, m := range topLevelCategoryRe.FindAllStringSubmatch(seedCategories, -1) {
		topLevel[m[1]] = true
	}
	if len(topLevel) == 0 {
		t.Fatal("sanity: extracted zero top-level categories from 0001 -- the extractor regex is broken")
	}

	content := readMigrationFile(t, "0175_category_size_system.sql")
	got := map[string][]string{}
	for _, m := range categorySizeSystemMappingPairRe.FindAllStringSubmatch(content, -1) {
		name, system := m[1], m[2]
		if !topLevel[name] {
			t.Errorf("0175 maps category %q -> %q, but %q is not a top-level category in 0001 (typo?)", name, system, name)
		}
		got[name] = append(got[name], system)
	}

	for name, systems := range got {
		sort.Strings(systems)
		want := append([]string(nil), wantCategorySizeSystemMapping[name]...)
		sort.Strings(want)
		if !equalStringSlices(systems, want) {
			t.Errorf("category %q maps to %v, want %v", name, systems, want)
		}
	}
	for name := range wantCategorySizeSystemMapping {
		if _, ok := got[name]; !ok {
			t.Errorf("expected category %q to be mapped in 0175, found nothing (dropped seed row?)", name)
		}
	}

	for _, name := range wantUngriddedTopLevelCategories {
		if !topLevel[name] {
			t.Errorf("%q is expected to be a real top-level category (0001) deliberately left unmapped, but it is not in 0001's seed at all", name)
		}
		if systems, mapped := got[name]; mapped {
			t.Errorf("%q is expected to be left unmapped (OS-fallback by design) but 0175 maps it to %v", name, systems)
		}
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCategorySizeSystemMappingExtractorsDetectTamperedInput guards the extractor regexes themselves
// (mirrors the sibling *DetectTamperedInput tests in this package) so this suite cannot silently pass
// because a regex stopped matching.
func TestCategorySizeSystemMappingExtractorsDetectTamperedInput(t *testing.T) {
	m := topLevelCategoryRe.FindAllStringSubmatch("('outerwear', 1, NULL),\n    ('tops', 1, NULL),", -1)
	if len(m) != 2 || m[0][1] != "outerwear" || m[1][1] != "tops" {
		t.Fatalf("topLevelCategoryRe sanity failed: %v", m)
	}

	pairs := categorySizeSystemMappingPairRe.FindAllStringSubmatch(
		"SELECT 'outerwear'            AS name, 'apparel' AS size_system UNION ALL\n"+
			"    SELECT 'tops',                          'apparel'               UNION ALL\n"+
			"    SELECT 'shoes',                         'shoe'", -1)
	if len(pairs) != 3 {
		t.Fatalf("categorySizeSystemMappingPairRe sanity failed: expected 3 pairs, got %d: %v", len(pairs), pairs)
	}
	if pairs[0][1] != "outerwear" || pairs[0][2] != "apparel" {
		t.Errorf("first pair = %v, want (outerwear, apparel)", pairs[0])
	}
	if pairs[2][1] != "shoes" || pairs[2][2] != "shoe" {
		t.Errorf("third pair = %v, want (shoes, shoe)", pairs[2])
	}

	// Must NOT match the single-value composite_ta/composite_bo CROSS JOIN subqueries.
	if categorySizeSystemMappingPairRe.MatchString("CROSS JOIN (SELECT 'composite_ta' AS size_system UNION ALL SELECT 'apparel') y") {
		t.Error("categorySizeSystemMappingPairRe must not match a single-value SELECT")
	}
}
