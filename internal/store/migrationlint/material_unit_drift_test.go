package migrationlint

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Migration 0271 rewrote the CATALOGUE's legacy free-text units onto the canonical spelling of the
// closed vocabulary (Ф5а.3). Its CASE mapping and entity.materialUnitSynonyms are the same table
// written twice, in two languages — and they must not drift: a synonym added to Go but not to the
// migration leaves rows the server can read but the catalogue never normalised; one added to the
// migration but not to Go rewrites stored data the server then reports as UNKNOWN.
//
// This is a static, database-free check in the spirit of the rest of this package.

var (
	unitCaseArmRe = regexp.MustCompile(`WHEN\s+'([^']+)'\s+THEN\s+'([^']+)'`)
	unitInListRe  = regexp.MustCompile(`(?s)LOWER\(TRIM\(unit\)\) IN \((.*?)\)`)
	quotedRe      = regexp.MustCompile(`'([^']*)'`)
)

func TestMaterialUnitMigrationMatchesTheVocabulary(t *testing.T) {
	content := readMigrationFile(t, "0271_normalize_material_unit.sql")

	arms := unitCaseArmRe.FindAllStringSubmatch(content, -1)
	if len(arms) == 0 {
		t.Fatal("sanity: extracted zero CASE arms from 0271 — the extractor regex is broken")
	}
	got := make(map[string]string, len(arms))
	for _, a := range arms {
		if prev, dup := got[a[1]]; dup {
			t.Errorf("0271 maps %q twice (%q then %q)", a[1], prev, a[2])
		}
		got[a[1]] = a[2]
	}

	want := entity.MaterialUnitSynonyms()
	for raw, unit := range want {
		mapped, ok := got[raw]
		if !ok {
			t.Errorf("entity knows synonym %q -> %q, but 0271 never rewrites it: catalogue rows keep the legacy spelling", raw, unit)
			continue
		}
		if mapped != string(unit) {
			t.Errorf("0271 maps %q -> %q, entity maps it -> %q", raw, mapped, unit)
		}
	}
	for raw, mapped := range got {
		unit, ok := want[raw]
		if !ok {
			t.Errorf("0271 rewrites %q -> %q, but the vocabulary does not know %q: the server would report the rewritten rows as UNKNOWN", raw, mapped, raw)
			continue
		}
		if mapped != string(unit) {
			t.Errorf("0271 maps %q -> %q, entity maps it -> %q", raw, mapped, unit)
		}
	}

	// The WHERE list decides which rows the CASE even sees; a synonym present in the CASE but absent
	// from the IN list is dead code and its rows stay un-normalised.
	m := unitInListRe.FindStringSubmatch(content)
	if m == nil {
		t.Fatal("sanity: 0271 has no LOWER(TRIM(unit)) IN (...) guard — the extractor regex is broken")
	}
	inList := map[string]bool{}
	for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
		inList[q[1]] = true
	}
	var missing []string
	for raw := range want {
		if !inList[raw] {
			missing = append(missing, raw)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("0271's WHERE guard does not select these synonyms, so their rows are never rewritten: %s", strings.Join(missing, ", "))
	}
	for raw := range inList {
		if _, ok := want[raw]; !ok {
			t.Errorf("0271's WHERE guard selects %q, which the vocabulary does not know", raw)
		}
	}
}

// TestMaterialUnitMigrationLeavesBomLinesAlone pins a deliberate decision, so a later "let's finish
// the job" edit has to argue with it first: tech_card_bom_item.unit is INSIDE the signed MATERIALS
// digest (dto.materialsProjection), so respelling «м» -> "m" there would mark the MATERIALS sign-off
// of every card that spells a unit non-canonically as stale — for a change that alters nothing the
// card BUYS. The functional benefit is taken in code (normalised comparison), which costs no
// sign-off.
func TestMaterialUnitMigrationLeavesBomLinesAlone(t *testing.T) {
	// Strip `--` comments first: the migration's header DISCUSSES tech_card_bom_item at length, and a
	// naive scan would flag the very explanation of why it is left alone.
	var sql strings.Builder
	for _, line := range strings.Split(readMigrationFile(t, "0271_normalize_material_unit.sql"), "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		sql.WriteString(line)
		sql.WriteString("\n")
	}
	for _, stmt := range strings.Split(sql.String(), ";") {
		if !strings.Contains(strings.ToUpper(stmt), "UPDATE ") {
			continue
		}
		if strings.Contains(stmt, "tech_card_bom_item") {
			t.Error("0271 must not rewrite tech_card_bom_item.unit — that column is inside the signed MATERIALS digest and rewriting it stales sign-offs (see the migration header)")
		}
	}
}
