package migrationlint

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/materialattr"
)

// This file extends migrationlint's static, database-free guards (see doc.go) to catch enum-value
// drift between an entity Go const set and the DB CHECK constraint that is supposed to enforce the
// same values (problem 033/50-F: "enum single-source выполнен частично" — enum_drift_test.go in
// internal/dto only ever compared entity<->proto, never entity<->DB). Each test below greps the exact
// migration file that owns the constraint and extracts its literal value list, so a future edit to
// either side that forgets the other fails here instead of silently compiling.
//
// TechCardPurpose has no proto enum yet (techcard.proto still carries `string purpose`) — T-B owns
// that conversion; only entity<->DB is checked here. See the track Dump for a TODO marker to add the
// third leg once the proto enum lands.

// readMigrationFile reads one migration by file name from ../sql (see migrationsDir in
// idempotency_test.go, shared across this package's tests).
func readMigrationFile(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(migrationsDir, name))
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return string(body)
}

var (
	// regexpAlternationRe matches a MySQL CHECK's REGEXP alternation, e.g.
	// REGEXP '^(sellable|auxiliary)$' or (doubled quotes, when embedded in a dynamic PREPARE string)
	// REGEXP ''^(male|female|unisex)$''.
	regexpAlternationRe = regexp.MustCompile(`REGEXP\s+'+\^\(([a-zA-Z_|]+)\)\$'+`)
	// valueListRe matches a MySQL IN(...)/ENUM(...) quoted value list, doubled-quote tolerant.
	valueListRe = regexp.MustCompile(`(?:IN|ENUM)\s*\(([^)]*)\)`)
)

// extractDBEnumValues finds anchor in content, then extracts the REGEXP alternation or IN/ENUM value
// list appearing within the next window characters — bounding the search keeps it from matching an
// unrelated CHECK elsewhere in the same migration file.
func extractDBEnumValues(t *testing.T, content, anchor string, window int) []string {
	t.Helper()
	idx := strings.Index(content, anchor)
	if idx < 0 {
		t.Fatalf("anchor %q not found in migration content", anchor)
	}
	end := idx + window
	if end > len(content) {
		end = len(content)
	}
	scope := content[idx:end]

	if m := regexpAlternationRe.FindStringSubmatch(scope); m != nil {
		return strings.Split(m[1], "|")
	}
	if m := valueListRe.FindStringSubmatch(scope); m != nil {
		parts := strings.Split(m[1], ",")
		values := make([]string, 0, len(parts))
		for _, p := range parts {
			values = append(values, strings.Trim(strings.TrimSpace(p), "'"))
		}
		return values
	}
	t.Fatalf("no REGEXP alternation or IN/ENUM value list found within %d chars of anchor %q", window, anchor)
	return nil
}

// mapKeysAsStrings converts a set map (as every entity Valid* map here is shaped) to a plain string
// slice for comparison against DB-extracted values.
func mapKeysAsStrings[K ~string](m map[K]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, string(k))
	}
	return out
}

// assertSameSet fails with the specific missing/extra values on either side — deliberately not just
// a length check, so a same-count-but-different-value drift (e.g. a typo'd rename) is caught too.
func assertSameSet(t *testing.T, label string, dbValues, entityValues []string) {
	t.Helper()
	db := make(map[string]bool, len(dbValues))
	for _, v := range dbValues {
		if db[v] {
			t.Errorf("%s: DB value list has a duplicate: %q (%v)", label, v, dbValues)
		}
		db[v] = true
	}
	ent := make(map[string]bool, len(entityValues))
	for _, v := range entityValues {
		ent[v] = true
	}
	for v := range db {
		if !ent[v] {
			t.Errorf("%s: DB CHECK allows %q but the entity set does not", label, v)
		}
	}
	for v := range ent {
		if !db[v] {
			t.Errorf("%s: entity set allows %q but the DB CHECK does not", label, v)
		}
	}
}

// TestTechCardPurposeDBCheckNoDrift is the drift test the brief asks for by name: entity
// (TechCardPurpose/ValidTechCardPurposes) <-> DB CHECK (migration 0111, chk_tech_card_purpose).
func TestTechCardPurposeDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0111_new_flow_auxiliary_tech_card.sql")
	dbValues := extractDBEnumValues(t, content, "purpose REGEXP", 100)
	assertSameSet(t, "TechCardPurpose", dbValues, mapKeysAsStrings(entity.ValidTechCardPurposes))
}

// TestGenderDBCheckNoDrift extends the drift test to gender (entity.ValidProductTargetGenders), whose
// DB source of truth is migration 0067's tech_card.target_gender CHECK. product.target_gender was
// dropped by migration 0140 (PR6 style de-dup) so tech_card is the only remaining gender CHECK.
func TestGenderDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0067_add_tech_card_core.sql")
	dbValues := extractDBEnumValues(t, content, "target_gender REGEXP", 100)
	assertSameSet(t, "GenderEnum", dbValues, mapKeysAsStrings(entity.ValidProductTargetGenders))
}

// TestSeasonDBCheckNoDrift extends the drift test to season (entity.ValidSeasons), whose DB source of
// truth is migration 0134's tech_card_season_code_enum CHECK.
func TestSeasonDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0134_tech_card_season_normalize.sql")
	dbValues := extractDBEnumValues(t, content, "tech_card_season_code_enum", 300)
	assertSameSet(t, "SeasonEnum", dbValues, mapKeysAsStrings(entity.ValidSeasons))
}

// TestSizeSKUSystemDBCheckNoDrift extends the drift test to the size SKU system (entity.SizeSKUSystem/
// ValidSizeSKUSystems), whose DB source of truth is migration 0147's chk_size_sku_contract CHECK.
func TestSizeSKUSystemDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0147_size_sku_contract.sql")
	dbValues := extractDBEnumValues(t, content, "chk_size_sku_contract", 300)
	assertSameSet(t, "SizeSKUSystem", dbValues, mapKeysAsStrings(entity.ValidSizeSKUSystems))
}

// TestColorwayStatusDBCheckNoDrift extends the drift test to product lifecycle status
// (entity.ColorwayStatus/ValidColorwayStatuses). The DB source of truth is migration 0137's stored
// lifecycle_status with the named `chk_product_lifecycle_status CHECK (... BETWEEN <lo> AND <hi>)`.
// The entity side must be exactly the contiguous numeric range the CHECK stores — UNKNOWN=0 is a
// read-only fail-closed sentinel and must NOT be storable.
func TestColorwayStatusDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0137_product_status.sql")
	m := regexp.MustCompile(
		`chk_product_lifecycle_status CHECK \(lifecycle_status BETWEEN (\d+) AND (\d+)\)`,
	).FindStringSubmatch(content)
	if m == nil {
		t.Fatal("0137: named CHECK chk_product_lifecycle_status with BETWEEN bounds not found")
	}
	lo, _ := strconv.Atoi(m[1])
	hi, _ := strconv.Atoi(m[2])

	var got []int
	for s := range entity.ValidColorwayStatuses {
		got = append(got, int(s))
	}
	sort.Ints(got)
	var want []int
	for v := lo; v <= hi; v++ {
		want = append(want, v)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ColorwayStatus drift: entity storable set %v != DB CHECK range %v", got, want)
	}
	if entity.ValidColorwayStatuses[entity.ColorwayStatusUnknown] {
		t.Fatal("ColorwayStatusUnknown must not be storable")
	}
}

// TestMaterialClassDBCheckNoDrift extends the drift test to the material CTI discriminant
// (entity.MaterialClass/ValidMaterialClasses) <-> DB CHECK (migration 0157, chk_material_class).
func TestMaterialClassDBCheckNoDrift(t *testing.T) {
	content := readMigrationFile(t, "0157_material_cti.sql")
	dbValues := extractDBEnumValues(t, content, "material_class REGEXP", 120)
	assertSameSet(t, "MaterialClass", dbValues, mapKeysAsStrings(entity.ValidMaterialClasses))
}

// TestFabricDirectionFixtureVsDBCheck asserts the material-attributes fixture's fabric_direction set
// matches the DB CHECK (migration 0157, material_fabric_attr) — the fixture<->DB leg of the CTI drift
// guard (entity<->DB is TestMaterialClassDBCheckNoDrift; entity<->proto lives in internal/dto).
func TestFabricDirectionFixtureVsDBCheck(t *testing.T) {
	content := readMigrationFile(t, "0157_material_cti.sql")
	dbValues := extractDBEnumValues(t, content, "fabric_direction REGEXP", 120)
	assertSameSet(t, "fabric_direction", dbValues, materialattr.AllowedEnumValues("fabric", "fabric_direction"))
}

// TestEnumDriftExtractorsDetectTamperedInput guards the extractor helpers themselves (mirrors
// TestMigrationIdempotencyDetectors' rationale in idempotency_test.go) so this suite cannot silently
// pass because a regex stopped matching.
func TestEnumDriftExtractorsDetectTamperedInput(t *testing.T) {
	got := extractDBEnumValues(t, "CHECK (purpose REGEXP '^(sellable|auxiliary)$')", "purpose REGEXP", 100)
	assertSameSet(t, "sanity: REGEXP alternation", got, []string{"sellable", "auxiliary"})

	got = extractDBEnumValues(t, "CHECK (x IN (''SS'',''FW''))", "IN", 50)
	assertSameSet(t, "sanity: doubled-quote IN list", got, []string{"SS", "FW"})

	got = extractDBEnumValues(t, "ENUM(''active'',''hidden'',''archived'')", "ENUM(", 80)
	assertSameSet(t, "sanity: ENUM list", got, []string{"active", "hidden", "archived"})
}
