package migrationlint

import (
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
// that is the container probe's job (internal/store/colourway_t45_integration_test.go, CI=1). The
// load-bearing properties:
//
//  1. every statement parses as its own statement: PREPARE / EXECUTE / DEALLOCATE one per line
//     (prod runs without multiStatements; a joined trio is error 1064 at boot);
//  2. nothing is a CHECK (a retroactive CHECK validates the whole history and copies the table);
//  3. 0375 only adds tables, IF NOT EXISTS, both cascading from product;
//  4. 0376 adds the token column NULLABLE (an older binary still inserts), backfills it from
//     color_code without touching updated_at, adds the (style_id, sku_color_token) unique BEFORE it
//     drops uniq_product_style_color, and every DDL sits behind an information_schema gate.

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
	if len(up) != 16 {
		t.Errorf("%s: %d Up statements, want 16 (3 gated DDL × 5 statements + the backfill) — "+
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

// TestColourwayTokenMigrationSwapsTheUniqueSafely — property 4.
func TestColourwayTokenMigrationSwapsTheUniqueSafely(t *testing.T) {
	up, _ := t45Sections(t, t45TokenMigration)

	addCol := regexp.MustCompile(`(?i)ADD\s+COLUMN\s+sku_color_token\s+CHAR\(3\)\s+NULL\b`).FindStringIndex(up)
	backfill := regexp.MustCompile(`(?i)UPDATE\s+product\s+SET\s+sku_color_token\s*=\s*color_code\s*,\s*updated_at\s*=\s*updated_at\s+WHERE\s+sku_color_token\s+IS\s+NULL`).FindStringIndex(up)
	addUniq := regexp.MustCompile(`(?i)ADD\s+CONSTRAINT\s+uniq_product_style_sku_color_token\s+UNIQUE\s*\(\s*style_id\s*,\s*sku_color_token\s*\)`).FindStringIndex(up)
	dropOld := regexp.MustCompile(`(?i)DROP\s+INDEX\s+uniq_product_style_color\b`).FindStringIndex(up)

	switch {
	case addCol == nil:
		t.Fatalf("%s: the token column is not added as CHAR(3) NULL — NOT NULL would refuse an older binary's inserts", t45TokenMigration)
	case backfill == nil:
		t.Fatalf("%s: no idempotent backfill from color_code that keeps updated_at", t45TokenMigration)
	case addUniq == nil:
		t.Fatalf("%s: no unique on (style_id, sku_color_token)", t45TokenMigration)
	case dropOld == nil:
		t.Fatalf("%s: uniq_product_style_color is not dropped — two colourways could never share a family", t45TokenMigration)
	}
	if !(addCol[0] < backfill[0] && backfill[0] < addUniq[0] && addUniq[0] < dropOld[0]) {
		t.Errorf("%s: order must be add column → backfill → add token unique → drop the colour unique", t45TokenMigration)
	}
	for _, gate := range []string{
		`(?is)information_schema\.COLUMNS.*?COLUMN_NAME\s*=\s*'sku_color_token'`,
		`(?is)information_schema\.STATISTICS.*?INDEX_NAME\s*=\s*'uniq_product_style_sku_color_token'`,
		`(?is)information_schema\.STATISTICS.*?INDEX_NAME\s*=\s*'uniq_product_style_color'`,
	} {
		if !regexp.MustCompile(gate).MatchString(up) {
			t.Errorf("%s: a DDL is not behind its information_schema gate (%s) — a re-run after a failure would stop the boot", t45TokenMigration, gate)
		}
	}
}
