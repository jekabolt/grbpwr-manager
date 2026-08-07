package techcard

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jmoiron/sqlx"
)

// sqlx reads EVERY ':' in the SQL text as a named parameter — including one inside a '--' comment —
// and a miss fails the bind at request time with "could not find name  in map", taking the whole
// endpoint down. This report's query builds its section list out of named params, so it has more of
// them than most; sqlx.Named reproduces both failure modes without a database.
func TestFabricDirectionGapsQueryBinds(t *testing.T) {
	args := rollGoodsSectionArgs(map[string]any{"card": 0})
	_, bound, err := sqlx.Named(fabricDirectionGapsQuery, args)
	if err != nil {
		t.Fatalf("fabric direction gaps query does not bind: %v", err)
	}
	// One arg per ':' occurrence: the four families once each, plus :card twice (the "all cards"
	// short-circuit reads it on both sides of the OR).
	if want := len(rollGoodsSectionList) + 2; len(bound) != want {
		t.Fatalf("bound args = %d, want %d", len(bound), want)
	}
}

// The report and the marker rule must not be able to disagree about which BOM families can even
// HAVE a направление. They agree by construction — one rollGoodsSectionList, one fragment builder —
// and this pins the construction: a hand-written family list in the report's SQL would be the
// silent, worst-direction drift the list's own doc comment warns about (the report would call the
// campaign finished on a family whose saves still refuse).
func TestFabricDirectionGapsQueryUsesTheSharedRollGoodsList(t *testing.T) {
	if !strings.Contains(fabricDirectionGapsQuery, rollGoodsSectionInOn("bi")) {
		t.Fatal("the report must filter families through rollGoodsSectionInOn, not a copy of the list")
	}
	for _, s := range rollGoodsSectionList {
		if !strings.Contains(fabricDirectionGapsQuery, ":"+rollGoodsSectionParam(s)) {
			t.Fatalf("family %q is missing from the query's named params", s)
		}
	}
	// The alias is load bearing: `material` carries a `section` column of its own, so the bare
	// fragment next to that LEFT JOIN is ambiguous and MySQL refuses the statement outright.
	if !strings.Contains(fabricDirectionGapsQuery, "bi.section IN (") {
		t.Fatal("the family filter must be qualified with the bom-item alias")
	}
}

// The name an operator scans for, and the order they scan it in — both are contracts with the BOM
// tab, and both fail silently if they drift: a ULID where the screen says «ВЕЛЬВЕТ ИЗ КАТАЛОГА», or
// a worklist that reshuffles between two loads days apart.
func TestFabricDirectionGapsQueryMatchesTheBomTab(t *testing.T) {
	for _, want := range []string{
		"LEFT JOIN material m",
		"COALESCE(NULLIF(bi.name, ''), m.name, '')",
		"ORDER BY tc.id, bi.display_order, bi.id",
	} {
		if !strings.Contains(fabricDirectionGapsQuery, want) {
			t.Fatalf("query must contain %q", want)
		}
	}
	// UNSET is decided in Go, over the same vocabulary map the marker rule reads. A direction
	// predicate in the SQL would be that vocabulary restated, free to drift from it.
	if strings.Contains(fabricDirectionGapsQuery, "fabric_direction IS NULL") {
		t.Fatal("unset-ness belongs to entity.FabricDirectionIsUnknown, not to the query")
	}
}

// Every family the fragment admits must be a section the report can also hand back on the wire —
// a family filtered in but not mappable would reach an operator as SECTION_UNKNOWN.
func TestRollGoodsFamiliesAreValidSections(t *testing.T) {
	for _, s := range rollGoodsSectionList {
		if !entity.IsValidTechCardBomSection(s) {
			t.Fatalf("roll-goods family %q is not a valid BOM section", s)
		}
	}
}
