package migrationlint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real tree: every migration above the baseline declares itself correctly, and no breaking
// migration removes a name Go code still writes.
func TestMigrationsDeclareCompatibility(t *testing.T) {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	var migrations []namedMigration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(migrationsDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		migrations = append(migrations, namedMigration{Name: e.Name(), Content: string(body)})
	}
	problems, removed, err := detectCompatProblems(migrations, compatEnforceAfter)
	if err != nil {
		t.Fatal(err)
	}
	more, err := breakingStillNeeded(migrations, removed, []string{"../../../internal", "../../../app"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range append(problems, more...) {
		t.Errorf("%s: %s", p.Migration, p.Problem)
	}
}

func TestCompatLintRefusesUndeclaredDestructiveChanges(t *testing.T) {
	cases := map[string]string{
		"0486_a.sql": `ALTER TABLE product DROP COLUMN old_price;`,
		"0487_b.sql": `ALTER TABLE product RENAME COLUMN a TO b;`,
		"0488_c.sql": `DROP TABLE IF EXISTS legacy;`,
		"0489_d.sql": `ALTER TABLE product CHANGE COLUMN a b INT;`,
		"0490_e.sql": `RENAME TABLE a TO b;`,
		"0491_f.sql": `SET @ddl = (SELECT IF(COUNT(*) = 1, 'ALTER TABLE product DROP COLUMN x', 'SELECT 1') FROM information_schema.COLUMNS);`,
	}
	for name, up := range cases {
		problems, _, err := detectCompatProblems([]namedMigration{{name, migrationSQL(up)}}, compatEnforceAfter)
		if err != nil {
			t.Fatal(err)
		}
		if len(problems) != 1 || !strings.Contains(problems[0].Problem, "breaks the code one release back") {
			t.Fatalf("%s: problems %+v", name, problems)
		}
	}
}

func TestCompatLintAcceptsAdditiveAndIndexDrops(t *testing.T) {
	for _, up := range []string{
		`ALTER TABLE product ADD COLUMN x INT NULL, ALGORITHM=INSTANT;`,
		`ALTER TABLE product DROP INDEX idx_x, DROP CHECK chk_x, DROP FOREIGN KEY fk_x;`,
		`ALTER TABLE product RENAME INDEX a TO b;`,
		`CREATE TABLE IF NOT EXISTS t (id INT PRIMARY KEY);`,
	} {
		problems, _, err := detectCompatProblems([]namedMigration{{"0486_a.sql", migrationSQL(up)}}, compatEnforceAfter)
		if err != nil || len(problems) != 0 {
			t.Fatalf("%q: %+v %v", up, problems, err)
		}
	}
	problems, _, _ := detectCompatProblems([]namedMigration{{"0400_a.sql", migrationSQL(`DROP TABLE x;`)}}, compatEnforceAfter)
	if len(problems) != 0 {
		t.Fatal("old migrations are grandfathered")
	}
}

func TestCompatLintDeclarations(t *testing.T) {
	bad := []namedMigration{
		{"0486_breaking_drop.sql", migrationSQL(`DROP TABLE x;`)},                            // no header
		{"0487_drop.sql", "-- compat: breaking drop x\n" + migrationSQL(`DROP TABLE x;`)},    // no name marker
		{"0488_x.sql", "-- compat: lazy\n" + migrationSQL(`CREATE INDEX i ON t (a);`)},       // no reason
		{"0489_x.sql", "-- compat: soon later\n" + migrationSQL(`CREATE INDEX i ON t (a);`)}, // unknown word
	}
	for _, m := range bad {
		problems, _, err := detectCompatProblems([]namedMigration{m}, compatEnforceAfter)
		if err != nil || len(problems) != 1 {
			t.Fatalf("%s: %+v %v", m.Name, problems, err)
		}
	}
	ok := namedMigration{"0490_breaking_drop_legacy.sql", "-- compat: breaking legacy unused since 0489\n" + migrationSQL(`DROP TABLE IF EXISTS legacy;`)}
	problems, removed, err := detectCompatProblems([]namedMigration{ok}, compatEnforceAfter)
	if err != nil || len(problems) != 0 || len(removed[ok.Name]) != 1 || removed[ok.Name][0].Name != "legacy" {
		t.Fatalf("%+v %+v %v", problems, removed, err)
	}
}

// A declared breaking migration still fails when the code of the same tree writes the name.
func TestCompatLintRefusesBreakingWhileCodeStillNeedsTheName(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("repo.go", "package x\nconst q = `SELECT id, old_price FROM product`\n")
	write("repo_test.go", "package x\nconst legacy = \"SELECT legacy_col FROM t\"\n")
	write("other.go", "package x\nvar s = \"old_prices_total\"\n")

	m := namedMigration{"0486_breaking_drop_old_price.sql", "-- compat: breaking old price unused\n" + migrationSQL(`ALTER TABLE product DROP COLUMN old_price;`)}
	_, removed, err := detectCompatProblems([]namedMigration{m}, compatEnforceAfter)
	if err != nil {
		t.Fatal(err)
	}
	problems, err := breakingStillNeeded([]namedMigration{m}, removed, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Problem, "repo.go") {
		t.Fatalf("problems %+v, want one in repo.go", problems)
	}

	m2 := namedMigration{"0487_breaking_drop_legacy_col.sql", "-- compat: breaking unused\n" + migrationSQL(`ALTER TABLE t DROP COLUMN legacy_col;`)}
	_, removed2, _ := detectCompatProblems([]namedMigration{m2}, compatEnforceAfter)
	if problems, _ := breakingStillNeeded([]namedMigration{m2}, removed2, []string{dir}); len(problems) != 0 {
		t.Fatalf("a test file must not block: %+v", problems)
	}

	m.Content = "-- migrationlint:name-still-used old_price the report table has its own old_price\n" + m.Content
	if problems, _ := breakingStillNeeded([]namedMigration{m}, removed, []string{dir}); len(problems) != 0 {
		t.Fatalf("allowance ignored: %+v", problems)
	}
}

// Review X3 major 1: shape changes older code may not survive under STRICT_ALL_TABLES.
func TestCompatLintRefusesUndeclaredShapeChanges(t *testing.T) {
	for name, up := range map[string]string{
		"0486_x.sql": `ALTER TABLE product MODIFY COLUMN name VARCHAR(10) NOT NULL;`, // the review's proof case
		"0487_x.sql": `ALTER TABLE product MODIFY name TEXT NULL;`,
		"0488_x.sql": `ALTER TABLE product ADD COLUMN sku_code VARCHAR(20) NOT NULL;`,
		"0489_x.sql": `ALTER TABLE product ALTER COLUMN price DROP DEFAULT;`,
		"0490_x.sql": `SET @ddl = (SELECT IF(COUNT(*) = 0, 'ALTER TABLE product ADD COLUMN x INT NOT NULL', 'SELECT 1') FROM information_schema.COLUMNS);`,
	} {
		problems, removed, err := detectCompatProblems([]namedMigration{{name, migrationSQL(up)}}, compatEnforceAfter)
		if err != nil {
			t.Fatal(err)
		}
		if len(problems) != 1 || !strings.Contains(problems[0].Problem, "compat: safe") {
			t.Fatalf("%s: problems %+v", name, problems)
		}
		if len(removed) != 0 {
			t.Fatalf("%s: a shape change removes no name: %+v", name, removed)
		}
		for _, header := range []string{"-- compat: safe older code always writes this column\n", "-- compat: breaking narrows name\n"} {
			id := name
			if strings.Contains(header, "breaking") {
				id = strings.Replace(name, "_x.sql", "_breaking_x.sql", 1)
			}
			problems, removed, err := detectCompatProblems([]namedMigration{{id, header + migrationSQL(up)}}, compatEnforceAfter)
			if err != nil || len(problems) != 0 || len(removed) != 0 {
				t.Fatalf("%s with %q: %+v %+v %v", id, header, problems, removed, err)
			}
		}
	}
	for _, up := range []string{
		`ALTER TABLE product ADD COLUMN x INT NOT NULL DEFAULT 0;`,
		`ALTER TABLE product ADD COLUMN x INT NULL;`,
		`ALTER TABLE product ADD COLUMN id2 BIGINT NOT NULL AUTO_INCREMENT;`,
		`ALTER TABLE product ADD COLUMN y INT AS (x + 1) STORED NOT NULL;`,
		`ALTER TABLE product ALTER COLUMN price SET DEFAULT 0;`,
		`ALTER TABLE product ADD UNIQUE KEY uq (a), ADD CONSTRAINT c CHECK (a IS NOT NULL);`,
	} {
		problems, _, err := detectCompatProblems([]namedMigration{{"0486_a.sql", migrationSQL(up)}}, compatEnforceAfter)
		if err != nil || len(problems) != 0 {
			t.Fatalf("%q: %+v %v", up, problems, err)
		}
	}
	// "safe" never excuses a removal.
	problems, _, _ := detectCompatProblems([]namedMigration{{"0486_a.sql", "-- compat: safe trust me\n" + migrationSQL(`ALTER TABLE product DROP COLUMN x;`)}}, compatEnforceAfter)
	if len(problems) != 1 {
		t.Fatalf("safe must not excuse a DROP: %+v", problems)
	}
}
