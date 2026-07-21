package migrationlint

import (
	"regexp"
	"strings"
	"testing"
)

// The style-category backfill (0198) derives tech_card's top/sub/type triple from its single
// category_id. Its correctness rests on properties that are impossible to check without a database
// and easy to break with a plausible-looking edit, so they are asserted structurally here: the tests
// parse the statement and check the actual level-to-column mapping, rather than grepping for
// substrings that survive the mapping being wrong.

const styleCategoryBackfillMigration = "0198_backfill_style_category_triple.sql"

// categoryLevelForColumn is the mapping the migration must implement: the column named on the left is
// filled from the CASE arms testing the level on the right. Getting this wrong -- filling
// top_category_id from the level-2 arms, say -- produces a catastrophically miscategorised catalogue
// that still looks like a reasonable migration on a skim.
var categoryLevelForColumn = map[string]string{
	"tc.top_category_id": "1",
	"tc.sub_category_id": "2",
	"tc.type_id":         "3",
}

var (
	commentLineRe = regexp.MustCompile(`(?m)^\s*--.*$`)
	whitespaceRe  = regexp.MustCompile(`\s+`)
	// caseArmRe matches one derivation arm, capturing the alias tested, the level tested and the
	// alias whose id is produced -- e.g. "CASE WHEN p1.level_id = 1 THEN p1.id END".
	caseArmRe = regexp.MustCompile(`CASE WHEN (\w+)\.level_id = (\d+) THEN (\w+)\.id END`)
	// headlessGuardRe matches the parenthesised "chain must reach a top-level node" guard, the one
	// place in the WHERE where OR is legitimate.
	headlessGuardRe = regexp.MustCompile(`\(c\.level_id = 1 OR p1\.level_id = 1 OR p2\.level_id = 1\)`)
)

// normalizedBackfillSQL returns the migration's SQL with -- comments stripped and all whitespace runs
// collapsed to single spaces, so the assertions below survive any reformat (re-indentation, wrapping,
// alignment changes) while still pinning the semantics. Stripping comments matters: the file's prose
// describes the mapping in English and would otherwise satisfy substring checks on its own.
func normalizedBackfillSQL(t *testing.T) string {
	t.Helper()
	body := readMigrationFile(t, styleCategoryBackfillMigration)
	body = commentLineRe.ReplaceAllString(body, " ")
	return strings.TrimSpace(whitespaceRe.ReplaceAllString(body, " "))
}

// coalesceBodyFor extracts the argument list of the COALESCE assigned to the given column, by
// scanning forward from the assignment and balancing parentheses. A regex cannot do this reliably
// because the arms nest parentheses of their own.
func coalesceBodyFor(t *testing.T, sql, column string) string {
	t.Helper()
	head := column + " = COALESCE("
	start := strings.Index(sql, head)
	if start < 0 {
		t.Fatalf("%s does not assign %s from a COALESCE", styleCategoryBackfillMigration, column)
	}
	depth, open := 1, start+len(head)
	for i := open; i < len(sql); i++ {
		switch sql[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return sql[open:i]
			}
		}
	}
	t.Fatalf("%s has an unbalanced COALESCE for %s", styleCategoryBackfillMigration, column)
	return ""
}

// TestStyleCategoryBackfillMapsLevelsToMatchingColumns asserts the actual mapping: each column is
// filled ONLY from CASE arms testing its own level, one arm per ancestor alias, and each arm produces
// the id of the very node it tested. This is what makes the dresses shape come out right -- a dress
// type (level 3 hanging directly off a level-1 top category) must land in type_id with sub_category_id
// left NULL, which follows from classifying by level rather than by position in the chain.
func TestStyleCategoryBackfillMapsLevelsToMatchingColumns(t *testing.T) {
	sql := normalizedBackfillSQL(t)

	for column, wantLevel := range categoryLevelForColumn {
		body := coalesceBodyFor(t, sql, column)
		arms := caseArmRe.FindAllStringSubmatch(body, -1)
		if len(arms) != 3 {
			t.Errorf("%s should be derived from exactly 3 CASE arms (c, p1, p2), found %d in: %s",
				column, len(arms), body)
			continue
		}
		seen := map[string]bool{}
		for _, arm := range arms {
			whenAlias, level, thenAlias := arm[1], arm[2], arm[3]
			if level != wantLevel {
				t.Errorf("%s must be filled from level %s arms, but an arm tests level %s: %s",
					column, wantLevel, level, arm[0])
			}
			if whenAlias != thenAlias {
				t.Errorf("%s arm tests %s but produces %s.id -- an arm must yield the node it tested: %s",
					column, whenAlias, thenAlias, arm[0])
			}
			seen[whenAlias] = true
		}
		for _, alias := range []string{"c", "p1", "p2"} {
			if !seen[alias] {
				t.Errorf("%s is not derived from ancestor alias %s; got arms: %s", column, alias, body)
			}
		}
	}
}

// TestStyleCategoryBackfillOnlyFillsNulls asserts the guard that makes the migration both
// non-destructive and idempotent: it may only touch rows whose triple is ENTIRELY NULL, so a triple
// already written by the legacy UpdateStyle path is never overwritten, and every row it fills stops
// matching its own WHERE. The three IS NULL clauses must therefore be conjoined -- swapping any AND
// for an OR would let it overwrite a partially-populated triple.
func TestStyleCategoryBackfillOnlyFillsNulls(t *testing.T) {
	sql := normalizedBackfillSQL(t)
	idx := strings.Index(sql, " WHERE ")
	if idx < 0 {
		t.Fatalf("%s has no WHERE clause", styleCategoryBackfillMigration)
	}
	where := strings.TrimSuffix(strings.TrimSpace(sql[idx+len(" WHERE "):]), ";")

	for _, col := range []string{"tc.top_category_id", "tc.sub_category_id", "tc.type_id"} {
		if !strings.Contains(where, col+" IS NULL") {
			t.Errorf("%s must require %s IS NULL, WHERE was: %s", styleCategoryBackfillMigration, col, where)
		}
	}
	if !strings.Contains(where, "tc.category_id IS NOT NULL") {
		t.Errorf("%s must require a category_id to derive from, WHERE was: %s", styleCategoryBackfillMigration, where)
	}
	// The headless-path guard is the ONLY legitimate OR in the WHERE. Remove it, and any remaining OR
	// means one of the conjuncts above has been loosened into a disjunction.
	if !headlessGuardRe.MatchString(where) {
		t.Errorf("%s must skip rows whose chain reaches no top-level category, WHERE was: %s",
			styleCategoryBackfillMigration, where)
	}
	if rest := headlessGuardRe.ReplaceAllString(where, ""); strings.Contains(rest, " OR ") {
		t.Errorf("%s joins its guards with OR; they must all be AND or the backfill can overwrite an "+
			"existing triple and stop being idempotent. WHERE was: %s", styleCategoryBackfillMigration, where)
	}
}

// TestStyleCategoryBackfillHasNoColons guards a repo-specific footgun: a ':' anywhere in a migration
// -- including inside a '--' comment -- is parsed as a named bind parameter by sqlx and breaks the
// query with "could not find name in map". Keep prose colon-free; use a dash instead.
func TestStyleCategoryBackfillHasNoColons(t *testing.T) {
	body := readMigrationFile(t, styleCategoryBackfillMigration)
	for i, line := range strings.Split(body, "\n") {
		if strings.Contains(line, ":") {
			t.Errorf("%s line %d contains a ':' which sqlx reads as a bind parameter: %s",
				styleCategoryBackfillMigration, i+1, line)
		}
	}
}

// TestStyleCategoryBackfillIsPureDML asserts the migration stays a plain UPDATE. It is deliberately
// DDL-free, which is what lets it skip the guarded PREPARE/EXECUTE dance the schema migrations need
// and makes a mid-file failure leave nothing half-applied.
func TestStyleCategoryBackfillIsPureDML(t *testing.T) {
	sql := strings.ToUpper(normalizedBackfillSQL(t))
	for _, ddl := range []string{"ALTER TABLE", "CREATE TABLE", "DROP TABLE", "PREPARE ", "DELETE "} {
		if strings.Contains(sql, ddl) {
			t.Errorf("%s must stay pure DML, found %q", styleCategoryBackfillMigration, strings.TrimSpace(ddl))
		}
	}
	if n := strings.Count(sql, "UPDATE TECH_CARD"); n != 1 {
		t.Errorf("%s should contain exactly one UPDATE of tech_card, found %d", styleCategoryBackfillMigration, n)
	}
}
