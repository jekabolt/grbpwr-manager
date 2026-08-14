package migrationlint

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// The canonicalisation table of the nine legacy operation types exists in exactly two places, and it
// HAS to: Go canonicalises every incoming payload with it (entity.LegacyOperationMachineType, read by
// the dto write path and, inverted, by the digest's compat projection), and migration 0306 step 5
// restates it as a SQL CASE to move the rows already in the table. SQL cannot call the Go map and the
// Go map cannot be a migration, so the copy is unavoidable — which makes comparing them mechanically
// the only thing standing between the two.
//
// A drift here is not a compile error and not a failed query. It is a row that says
// operation_type='machine' with machine_type NULL (a step that lost the answer to «на чём» and shows
// as «not set» on a card whose CONSTRUCTION sign-off already hashed the old value), or a legacy token
// that survives the migration and then trips the strict chk_op_operation_type added two steps later —
// the second of which halts startup, because ADD CONSTRAINT re-validates the whole table.
//
// Both sides are EXTRACTED, never restated here: retyping the pairs in the test would just add a
// third copy to keep in sync.

var (
	// legacyCaseArmRe pulls `WHEN 'old' THEN 'new'` arms out of the step-5 CASE.
	legacyCaseArmRe = regexp.MustCompile(`WHEN\s+'([a-z_]+)'\s+THEN\s+'([a-z_]+)'`)
	// legacyInListRe pulls the WHERE ... IN (...) guard list of the same UPDATE. It spans newlines,
	// hence the (?s) and the non-greedy body.
	legacyInListRe = regexp.MustCompile(`(?s)WHERE\s+operation_type\s+IN\s*\((.*?)\)`)
)

// extractLegacyRemap returns the step-5 CASE mapping and its WHERE ... IN guard list, scoped to the
// one UPDATE that carries them (anchored on `machine_type = CASE operation_type`, which occurs once —
// step 3's UPDATE on the same table sets operation_type = LOWER(...) and has no CASE at all).
func extractLegacyRemap(t *testing.T, content string) (map[string]string, []string) {
	t.Helper()
	const anchor = "machine_type = CASE operation_type"
	start := strings.Index(content, anchor)
	if start < 0 {
		t.Fatalf("0306: step 5 anchor %q not found", anchor)
	}
	if strings.Contains(content[start+len(anchor):], anchor) {
		t.Fatal("0306: the legacy remap CASE appears more than once; this test would compare only the first")
	}
	stmt := content[start:]
	if end := strings.Index(stmt, ";"); end >= 0 {
		stmt = stmt[:end]
	}

	arms := legacyCaseArmRe.FindAllStringSubmatch(stmt, -1)
	if len(arms) == 0 {
		t.Fatal("0306: no `WHEN '<old>' THEN '<new>'` arms found in the step-5 CASE")
	}
	mapping := make(map[string]string, len(arms))
	for _, a := range arms {
		if prev, dup := mapping[a[1]]; dup {
			t.Fatalf("0306: legacy token %q appears twice in the CASE (%q then %q); MySQL takes the first arm silently",
				a[1], prev, a[2])
		}
		mapping[a[1]] = a[2]
	}

	m := legacyInListRe.FindStringSubmatch(stmt)
	if m == nil {
		t.Fatal("0306: step 5 has no `WHERE operation_type IN (...)` guard — without it the UPDATE would blank machine_type on every non-legacy row")
	}
	var guard []string
	for _, raw := range strings.Split(m[1], ",") {
		guard = append(guard, strings.Trim(strings.TrimSpace(raw), "'"))
	}
	return mapping, guard
}

// TestLegacyOperationRemapMatchesEntityMap is the load-bearing assertion: the SQL CASE of 0306 step 5
// and entity.LegacyOperationMachineType must be the same function, key for key and value for value.
func TestLegacyOperationRemapMatchesEntityMap(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	sqlMap, _ := extractLegacyRemap(t, content)

	goMap := make(map[string]string, len(entity.LegacyOperationMachineType))
	for legacy, machine := range entity.LegacyOperationMachineType {
		goMap[string(legacy)] = machine
	}

	for legacy, machine := range sqlMap {
		want, known := goMap[legacy]
		if !known {
			t.Errorf("0306 remaps legacy type %q, which entity.LegacyOperationMachineType does not know: the migrated row would canonicalise differently from every payload the dto accepts", legacy)
			continue
		}
		if want != machine {
			t.Errorf("legacy %q: 0306 maps it to %q, entity.LegacyOperationMachineType to %q", legacy, machine, want)
		}
	}
	for legacy, machine := range goMap {
		if _, ok := sqlMap[legacy]; !ok {
			t.Errorf("entity.LegacyOperationMachineType maps %q -> %q, but 0306 leaves rows with that operation_type untouched — and chk_op_operation_type (step 6) then refuses them, halting startup", legacy, machine)
		}
	}
}

// TestLegacyOperationRemapGuardMatchesItsCase locks the OTHER half of step 5, which the mapping
// comparison cannot see: the `WHERE operation_type IN (...)` list must be exactly the CASE's key set.
//
// The two failure modes are asymmetric and both silent. A token in the IN list with no CASE arm makes
// the CASE return NULL, so the row becomes operation_type='machine' with machine_type NULL — a step
// that lost «на чём» with nothing to say so. A CASE arm with no place in the IN list is simply never
// reached, leaving a legacy token in the column for step 6's strict CHECK to reject at startup.
func TestLegacyOperationRemapGuardMatchesItsCase(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	sqlMap, guard := extractLegacyRemap(t, content)

	keys := make([]string, 0, len(sqlMap))
	for k := range sqlMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assertSameSet(t, "0306 step 5: WHERE IN list vs CASE arms", guard, keys)
}

// TestLegacyOperationRemapTargetsAreKnownMachines checks the RIGHT-hand side of the CASE against the
// machine vocabulary, not just against the Go map: every value 0306 writes into machine_type has to
// be a token chk_op_machine_type (added two steps earlier, in the same file) accepts. If it is not,
// the migration fails on its own UPDATE — mid-file, after the DDL has auto-committed, leaving the
// schema half-applied with no gorp_migrations row.
func TestLegacyOperationRemapTargetsAreKnownMachines(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	sqlMap, _ := extractLegacyRemap(t, content)

	known := make(map[string]bool, len(entity.MachineTypeTokens))
	for _, tok := range entity.MachineTypeTokens {
		known[tok] = true
	}
	for legacy, machine := range sqlMap {
		if !known[machine] {
			t.Errorf("0306 maps legacy %q to machine %q, which is not in entity.MachineTypeTokens and therefore not in chk_op_machine_type: the UPDATE would fail mid-migration", legacy, machine)
		}
	}
}

// TestLegacyOperationRemapIsInjective guards the property the digest depends on. The compat
// projection hashes (machine, <machine_type>) byte-identically to the legacy string the row used to
// hold, and it gets that string by INVERTING this map. Two legacy types collapsing onto one machine
// would make the inverse ambiguous — entity.MachineTypeLegacyToken panics at init on exactly that,
// and this test catches it in the migration before the panic has a chance to be about the Go side.
//
// It is why `double_needle` maps to `lockstitch_double_needle` and not to `lockstitch`: the
// two-needle machine exists in the vocabulary so this row survives the move with its meaning.
func TestLegacyOperationRemapIsInjective(t *testing.T) {
	content := readMigrationFile(t, migration0306)
	sqlMap, _ := extractLegacyRemap(t, content)

	seen := make(map[string]string, len(sqlMap))
	for legacy, machine := range sqlMap {
		if prev, dup := seen[machine]; dup {
			t.Errorf("machine %q is the target of both %q and %q: the digest's compat projection cannot invert that, and the two steps would hash identically", machine, prev, legacy)
		}
		seen[machine] = legacy
	}
}

// TestLegacyRemapExtractorsDetectTamperedInput guards the extractors themselves, mirroring
// TestEnumDriftExtractorsDetectTamperedInput — a broken regex here would make every assertion above
// pass vacuously on an empty mapping, which is the one outcome none of them would notice.
func TestLegacyRemapExtractorsDetectTamperedInput(t *testing.T) {
	arms := legacyCaseArmRe.FindAllStringSubmatch(
		"machine_type = CASE operation_type WHEN 'blindhem' THEN 'blindstitch' END", -1)
	if len(arms) != 1 || arms[0][1] != "blindhem" || arms[0][2] != "blindstitch" {
		t.Fatalf("CASE-arm extractor broke: %v", arms)
	}
	m := legacyInListRe.FindStringSubmatch("WHERE operation_type IN ('a',\n  'b')")
	if m == nil {
		t.Fatal("IN-list extractor broke: it must span newlines")
	}
	var got []string
	for _, raw := range strings.Split(m[1], ",") {
		got = append(got, strings.Trim(strings.TrimSpace(raw), "'"))
	}
	assertSameSet(t, "sanity: multiline IN list", got, []string{"a", "b"})
}
