package migrationlint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rubenv/sql-migrate/sqlparse"
)

// GUARDS OVER THE T45 MIGRATIONS (27.09): 0375 (palette + per-language name) and 0376 (the SKU
// colour token leaves the dictionary). Like every guard in this package they read the files as
// text and through the SAME parser the application runs at boot; they do not apply Up or Down —
// that is the container probe's job (internal/store/colourway_t45_integration_test.go, built only
// with -tags integration and refused outside a named disposable database). The load-bearing
// properties:
//
//  1. every statement parses as its own statement: PREPARE / EXECUTE / DEALLOCATE one per line
//     (prod runs without multiStatements; a joined trio is error 1064 at boot);
//  2. nothing is a CHECK (a retroactive CHECK validates the whole history and copies the table);
//  3. 0375 only adds tables, IF NOT EXISTS, both cascading from product — and its Down refuses
//     BEFORE its first DROP TABLE while either table holds a row (a palette and a translated
//     name have no place in the pre-T45 schema);
//  4. 0376 is ADDITIVE (D-69): it adds the token column NULLABLE (an older binary still inserts),
//     backfills it from color_code without touching updated_at and adds the (style_id,
//     sku_color_token) unique, every DDL behind an information_schema gate — and it does NOT drop
//     uniq_product_style_color: that destructive step is a later migration in a later push;
//  5. 0376 Down refuses BEFORE any DDL when the data holds what the pre-T45 schema cannot (a family
//     shared within a style, a token that is not its family), with a message that fits a MySQL
//     identifier.

const (
	t45PaletteMigration = "0375_colourway_palette.sql"
	t45TokenMigration   = "0376_colourway_sku_token.sql"
)

func t45Parse(t *testing.T, name string) *sqlparse.ParsedMigration {
	t.Helper()
	f, err := os.Open(filepath.Join(migrationsDir, name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	parsed, err := sqlparse.ParseMigration(f)
	if err != nil {
		t.Fatalf("%s does not parse — the application would not start: %v", name, err)
	}
	return parsed
}

func t45Sections(t *testing.T, name string) (up, down string) {
	t.Helper()
	body := readMigrationFile(t, name)
	_, rest, ok := strings.Cut(body, "-- +migrate Up")
	if !ok {
		t.Fatalf("%s: no -- +migrate Up", name)
	}
	up, down, ok = strings.Cut(rest, "-- +migrate Down")
	if !ok {
		t.Fatalf("%s: no -- +migrate Down", name)
	}
	return sqlOnly(up), sqlOnly(down)
}

// TestColourwayT45MigrationsParseOneStatementEach — property 1. A statement that holds more than one
// PREPARE/EXECUTE/DEALLOCATE is the joined form prod refuses.
func TestColourwayT45MigrationsParseOneStatementEach(t *testing.T) {
	// A statement HEAD: at the start of the parsed statement or right after a semicolon inside it.
	// («DEALLOCATE PREPARE stmt» is one head, not two.)
	trio := regexp.MustCompile(`(?i)(^|;)\s*(PREPARE|EXECUTE|DEALLOCATE)\b`)
	for _, name := range []string{t45PaletteMigration, t45TokenMigration} {
		parsed := t45Parse(t, name)
		if len(parsed.UpStatements) == 0 {
			t.Fatalf("%s: Up has no statement", name)
		}
		for _, stmts := range [][]string{parsed.UpStatements, parsed.DownStatements} {
			for _, s := range stmts {
				if n := len(trio.FindAllString(strings.TrimSpace(sqlOnly(s)), -1)); n > 1 {
					t.Errorf("%s: one parsed statement carries %d of PREPARE/EXECUTE/DEALLOCATE — prod runs "+
						"without multiStatements and fails it with 1064:\n%s", name, n, s)
				}
			}
		}
	}
	// Positive control: the token migration really has the guarded-DDL shape the check above reads.
	up := t45Parse(t, t45TokenMigration).UpStatements
	if len(up) != 11 {
		t.Errorf("%s: %d Up statements, want 11 (2 gated DDL × 5 statements + the backfill) — "+
			"a changed count means the file changed shape; re-read this guard", t45TokenMigration, len(up))
	}
}

// TestColourwayT45MigrationsHaveNoCheck — property 2.
func TestColourwayT45MigrationsHaveNoCheck(t *testing.T) {
	for _, name := range []string{t45PaletteMigration, t45TokenMigration} {
		up, down := t45Sections(t, name)
		for _, part := range []string{up, down} {
			if regexp.MustCompile(`(?i)\bCHECK\s*\(`).MatchString(part) || addCheckRe.MatchString(part) {
				t.Errorf("%s: carries a CHECK constraint", name)
			}
		}
	}
}

// TestColourwayPaletteMigrationOnlyAddsCascadingTables — property 3.
func TestColourwayPaletteMigrationOnlyAddsCascadingTables(t *testing.T) {
	up, down := t45Sections(t, t45PaletteMigration)
	for _, table := range []string{"product_colour", "product_colour_name_i18n"} {
		create := regexp.MustCompile(`(?is)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+` + table + `\s*\((.*?)\)\s*ENGINE`)
		m := create.FindStringSubmatch(up)
		if m == nil {
			t.Errorf("%s: Up does not add %s idempotently", t45PaletteMigration, table)
			continue
		}
		fk := regexp.MustCompile(`(?i)FOREIGN\s+KEY\s*\(\s*product_id\s*\)\s+REFERENCES\s+product\s*\(\s*id\s*\)\s+ON\s+DELETE\s+CASCADE`)
		if !fk.MatchString(m[1]) {
			t.Errorf("%s: %s does not cascade from product — DeleteColorwayByID would grow a blocker", t45PaletteMigration, table)
		}
		if !regexp.MustCompile(`(?i)DROP\s+TABLE\s+IF\s+EXISTS\s+` + table + `\b`).MatchString(down) {
			t.Errorf("%s: Down does not drop %s idempotently", t45PaletteMigration, table)
		}
	}
	if regexp.MustCompile(`(?i)\bALTER\s+TABLE\b|\bUPDATE\s+\w+\s+SET\b|\bDELETE\s+FROM\b`).MatchString(up) {
		t.Errorf("%s: Up must only add tables — no existing row or table is touched", t45PaletteMigration)
	}
	if !regexp.MustCompile(`(?i)PRIMARY\s+KEY\s*\(\s*product_id\s*,\s*position\s*\)`).MatchString(up) {
		t.Errorf("%s: the palette is keyed (product_id, position)", t45PaletteMigration)
	}
	if !regexp.MustCompile(`(?i)PRIMARY\s+KEY\s*\(\s*product_id\s*,\s*language_id\s*\)`).MatchString(up) {
		t.Errorf("%s: the translations are keyed (product_id, language_id)", t45PaletteMigration)
	}
}

// TestColourwayPaletteMigrationDownRefusesFirst — property 3, the Down half (REVIEW-T45-codex-2). A
// palette colour or a translated name has no place in the pre-T45 schema, so while either table holds
// a row the Down refuses — one house refusal per table, each on the count its own table's gated
// SELECT … INTO wrote — BEFORE its first DROP TABLE. A count runs only when its table exists: a
// re-run after a partial Down must not fail with 1146 for the wrong reason.
func TestColourwayPaletteMigrationDownRefusesFirst(t *testing.T) {
	_, down := t45Sections(t, t45PaletteMigration)
	for _, p := range t45DownRefusalProblems(down, 2) {
		t.Errorf("%s: %s", t45PaletteMigration, p)
	}
	firstDrop := regexp.MustCompile(`(?i)\bDROP\s+TABLE\b`).FindStringIndex(down)
	if firstDrop == nil {
		t.Fatalf("%s: Down drops no table — the guard would prove nothing", t45PaletteMigration)
	}
	for _, table := range []string{"product_colour", "product_colour_name_i18n"} {
		has := regexp.MustCompile(`(?is)SET\s+(@\w+)\s*:=\s*\(\s*SELECT\s+COUNT\(\*\)\s+FROM\s+information_schema\.TABLES\s+` +
			`WHERE\s+TABLE_SCHEMA\s*=\s*DATABASE\(\)\s+AND\s+TABLE_NAME\s*=\s*'` + table + `'\s*\)`).FindStringSubmatch(down)
		if has == nil {
			t.Errorf("%s: Down does not look up whether %s exists before it counts its rows", t45PaletteMigration, table)
			continue
		}
		count := regexp.MustCompile(`(?is)IF\(\s*` + regexp.QuoteMeta(has[1]) + `\s*=\s*0\s*,\s*'SELECT 0 INTO (@\w+)'\s*,\s*` +
			`'SELECT COUNT\(\*\) INTO (@\w+) FROM ` + table + `'\s*\)`).FindStringSubmatchIndex(down)
		if count == nil {
			t.Errorf("%s: the row count of %s is not a prepared SELECT … INTO gated on the table existing", t45PaletteMigration, table)
			continue
		}
		rows := down[count[2]:count[3]]
		if other := down[count[4]:count[5]]; other != rows {
			t.Errorf("%s: the %s gate writes %s when the table is missing but %s when it exists", t45PaletteMigration, table, rows, other)
			continue
		}
		refusal := regexp.MustCompile(`(?is)IF\(\s*` + regexp.QuoteMeta(rows) + `\s*=\s*0\s*,\s*'SELECT 1'\s*,\s*` +
			"CONCAT\\('SELECT `[^`']*',\\s*" + regexp.QuoteMeta(rows) + `\s*,`).FindStringIndex(down)
		switch {
		case refusal == nil:
			t.Errorf("%s: no refusal reads %s, the row count of %s", t45PaletteMigration, rows, table)
		case refusal[0] < count[0]:
			t.Errorf("%s: the %s refusal comes before its count", t45PaletteMigration, table)
		case refusal[0] > firstDrop[0]:
			t.Errorf("%s: the %s refusal comes after the first DROP TABLE — the Down would destroy the rows it was "+
				"meant to refuse over", t45PaletteMigration, table)
		}
	}
}

// TestColourwayTokenMigrationIsAdditive — property 4.
func TestColourwayTokenMigrationIsAdditive(t *testing.T) {
	up, _ := t45Sections(t, t45TokenMigration)

	addCol := regexp.MustCompile(`(?i)ADD\s+COLUMN\s+sku_color_token\s+CHAR\(3\)\s+NULL\b`).FindStringIndex(up)
	backfill := regexp.MustCompile(`(?i)UPDATE\s+product\s+SET\s+sku_color_token\s*=\s*color_code\s*,\s*updated_at\s*=\s*updated_at\s+WHERE\s+sku_color_token\s+IS\s+NULL`).FindStringIndex(up)
	addUniq := regexp.MustCompile(`(?i)ADD\s+CONSTRAINT\s+uniq_product_style_sku_color_token\s+UNIQUE\s*\(\s*style_id\s*,\s*sku_color_token\s*\)`).FindStringIndex(up)

	switch {
	case addCol == nil:
		t.Fatalf("%s: the token column is not added as CHAR(3) NULL — NOT NULL would refuse an older binary's inserts", t45TokenMigration)
	case backfill == nil:
		t.Fatalf("%s: no idempotent backfill from color_code that keeps updated_at", t45TokenMigration)
	case addUniq == nil:
		t.Fatalf("%s: no unique on (style_id, sku_color_token)", t45TokenMigration)
	}
	if !(addCol[0] < backfill[0] && backfill[0] < addUniq[0]) {
		t.Errorf("%s: order must be add column → backfill → add the token unique", t45TokenMigration)
	}
	// D-69: the family unique survives this file. Its drop is the destructive half and ships in its
	// own migration and its own push; until then a second colourway of one family is the ordinary
	// «exists» refusal.
	if regexp.MustCompile(`(?i)\bDROP\s+(INDEX|KEY)\b|\bDROP\s+COLUMN\b|\bDROP\s+TABLE\b|\bDROP\s+FOREIGN\b`).MatchString(up) {
		t.Errorf("%s: Up drops something — the first push is additive only (D-69)", t45TokenMigration)
	}
	for _, gate := range []string{
		`(?is)information_schema\.COLUMNS.*?COLUMN_NAME\s*=\s*'sku_color_token'`,
		`(?is)information_schema\.STATISTICS.*?INDEX_NAME\s*=\s*'uniq_product_style_sku_color_token'`,
	} {
		if !regexp.MustCompile(gate).MatchString(up) {
			t.Errorf("%s: a DDL is not behind its information_schema gate (%s) — a re-run after a failure would stop the boot", t45TokenMigration, gate)
		}
	}
}

// t45DownRefusalRe is the house refusal: a prepared SELECT of a backticked column whose name is
// the message (SIGNAL cannot be prepared). Group 1 is the text before the count, group 2 after it.
var t45DownRefusalRe = regexp.MustCompile("CONCAT\\('SELECT `([^`']*)',\\s*@\\w+\\s*,\\s*'([^`']*)`'\\)")

// t45DownRefusalProblems lists what is wrong with a Down section's refusals: not exactly `want` of
// them, one placed after the first DDL, or a message that does not fit a MySQL identifier (64
// characters, ASCII) with a 10-digit count. Empty = sound. A pure function so the detector itself
// can be run against a tampered Down.
func t45DownRefusalProblems(down string, want int) []string {
	var problems []string
	refusals := t45DownRefusalRe.FindAllStringSubmatchIndex(down, -1)
	if len(refusals) != want {
		problems = append(problems, fmt.Sprintf("Down carries %d refusal(s), want %d", len(refusals), want))
	}
	ddl := regexp.MustCompile(`(?i)'ALTER\s+TABLE|\bDROP\s+TABLE|\bCREATE\s+TABLE`).FindStringIndex(down)
	if ddl == nil {
		return append(problems, "Down has no DDL — the guard would prove nothing")
	}
	for _, r := range refusals {
		if r[0] > ddl[0] {
			problems = append(problems, "a refusal comes after the first DDL — a Down that fails half-way "+
				"leaves a schema neither model holds")
		}
		msg := down[r[2]:r[3]] + "1234567890" + down[r[4]:r[5]]
		if len(msg) > 64 {
			problems = append(problems, fmt.Sprintf("refusal %q is %d characters — MySQL identifiers stop at 64", msg, len(msg)))
		}
		for _, c := range msg {
			if c > 127 {
				problems = append(problems, fmt.Sprintf("refusal %q is not ASCII — a cut identifier would split a character", msg))
				break
			}
		}
	}
	return problems
}

// TestColourwayTokenMigrationDownRefusesFirst — property 5.
func TestColourwayTokenMigrationDownRefusesFirst(t *testing.T) {
	_, down := t45Sections(t, t45TokenMigration)
	for _, p := range t45DownRefusalProblems(down, 2) {
		t.Errorf("%s: %s", t45TokenMigration, p)
	}

	family := regexp.MustCompile(`(?is)GROUP\s+BY\s+style_id\s*,\s*color_code\s+HAVING\s+COUNT\(\*\)\s*>\s*1`).FindStringIndex(down)
	minted := regexp.MustCompile(`(?i)sku_color_token\s+IS\s+NOT\s+NULL\s+AND\s+sku_color_token\s*<>\s*color_code`).FindStringIndex(down)
	if family == nil {
		t.Errorf("%s: Down does not refuse a family shared within a style", t45TokenMigration)
	}
	if minted == nil {
		t.Errorf("%s: Down does not refuse a token that is not its family — the column would take that identity with it", t45TokenMigration)
	}
	// The minted count names the column, so it runs only when the column exists: a re-run after a
	// partial Down must not fail with 1054 for the WRONG reason.
	gated := regexp.MustCompile(`(?is)IF\(\s*@\w+\s*=\s*0\s*,\s*'SELECT 0 INTO @\w+'\s*,\s*'SELECT COUNT\(\*\) INTO @\w+ FROM product WHERE sku_color_token IS NOT NULL`).MatchString(down)
	if !gated {
		t.Errorf("%s: the minted-token count is not gated on the column existing", t45TokenMigration)
	}
	// Restored before the token unique goes: the pre-T45 model is (style_id, color_code) unique.
	restore := regexp.MustCompile(`(?i)ADD\s+CONSTRAINT\s+uniq_product_style_color\s+UNIQUE\s*\(\s*style_id\s*,\s*color_code\s*\)`).FindStringIndex(down)
	dropTokenUniq := regexp.MustCompile(`(?i)DROP\s+INDEX\s+uniq_product_style_sku_color_token`).FindStringIndex(down)
	dropCol := regexp.MustCompile(`(?i)DROP\s+COLUMN\s+sku_color_token`).FindStringIndex(down)
	if restore == nil || dropTokenUniq == nil || dropCol == nil {
		t.Fatalf("%s: Down must restore the family unique, drop the token unique and drop the column", t45TokenMigration)
	}
	if !(restore[0] < dropTokenUniq[0] && dropTokenUniq[0] < dropCol[0]) {
		t.Errorf("%s: Down order must be restore family unique → drop token unique → drop column", t45TokenMigration)
	}
}

// The refusal detector itself, on tampered Downs: a refusal below the first DDL and a message longer
// than an identifier are both caught, and the shipped shape passes.
func TestColourwayT45RefusalDetectorCatchesTamperedDowns(t *testing.T) {
	refusal := func(text string) string {
		return "SET @ddl := IF(@n = 0, 'SELECT 1', CONCAT('SELECT `0376 Down blocked: ', @n, '" + text + "`'));\n"
	}
	ddl := "SET @ddl := IF(@x > 0, 'ALTER TABLE product DROP INDEX i', 'SELECT 1');\n"

	if p := t45DownRefusalProblems(refusal(" families are shared within a style")+ddl, 1); len(p) != 0 {
		t.Errorf("the shipped shape must pass, got %v", p)
	}
	if p := t45DownRefusalProblems(ddl+refusal(" families are shared within a style"), 1); len(p) == 0 {
		t.Error("a refusal after the first DDL must be reported")
	}
	if p := t45DownRefusalProblems(refusal(" families are shared within one single style today")+ddl, 1); len(p) == 0 {
		t.Error("a refusal longer than a MySQL identifier must be reported")
	}
	if p := t45DownRefusalProblems(ddl, 1); len(p) == 0 {
		t.Error("a Down without its refusal must be reported")
	}
	// 0375's Down drops its tables with a bare DROP TABLE, not a prepared ALTER: the same order rule.
	drop := "DROP TABLE IF EXISTS product_colour;\n"
	if p := t45DownRefusalProblems(refusal(" palette colours would be lost")+drop, 1); len(p) != 0 {
		t.Errorf("a refusal above a bare DROP TABLE must pass, got %v", p)
	}
	if p := t45DownRefusalProblems(drop+refusal(" palette colours would be lost"), 1); len(p) == 0 {
		t.Error("a refusal after a bare DROP TABLE must be reported")
	}
}
