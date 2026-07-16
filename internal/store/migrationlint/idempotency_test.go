package migrationlint

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// migrationsDir is the migrations directory relative to this package's source
// directory (go test sets the working directory to the package dir).
const migrationsDir = "../sql"

// grandfatheredMigrationMax is the highest migration number that existed when
// this lint was added. Files at or below it are already applied on prod and are
// immutable — renaming or editing them changes their sql-migrate Id and forces a
// reapply, which halts startup — so they are exempt. Every NEW migration (a
// higher number) must be idempotent: MySQL DDL auto-commits, so a mid-file
// failure under MYSQL_AUTOMIGRATE leaves the schema half-applied with no
// gorp_migrations row, and the next boot re-runs the whole file from the top.
// A non-idempotent CREATE then fails on "table already exists" and the process
// never starts. See migration 0079 for the incident this guards against.
const grandfatheredMigrationMax = 92

var (
	migrationPrefixRe = regexp.MustCompile(`^(\d+)_`)
	// createTableRe matches a CREATE TABLE, capturing the optional IF NOT EXISTS.
	createTableRe = regexp.MustCompile(`(?i)\bcreate\s+table\s+(if\s+not\s+exists\s+)?`)
	// autoCheckDropRe matches a DROP CHECK that names a MySQL auto-generated
	// constraint (<table>_chk_<n>). Those names are assigned positionally at
	// CREATE TABLE time and drift across schema history, so dropping them by the
	// hardcoded name is fragile.
	autoCheckDropRe = regexp.MustCompile(`(?i)\bdrop\s+check\s+\w+_chk_\d+`)
)

// TestNewMigrationsAreIdempotent lints every migration numbered above the
// grandfathered maximum: each CREATE TABLE must be CREATE TABLE IF NOT EXISTS,
// and no DROP CHECK may reference an auto-generated <table>_chk_<n> name.
func TestNewMigrationsAreIdempotent(t *testing.T) {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", migrationsDir, err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		m := migrationPrefixRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		num, err := strconv.Atoi(m[1])
		if err != nil || num <= grandfatheredMigrationMax {
			continue // malformed prefix, or already applied and immutable
		}
		checked++
		body, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		content := string(body)

		for _, sub := range createTableRe.FindAllStringSubmatch(content, -1) {
			if sub[1] == "" {
				t.Errorf("%s: uses CREATE TABLE without IF NOT EXISTS; new migrations must be idempotent so a retried partial apply does not fail on \"table already exists\"", name)
			}
		}
		if hit := autoCheckDropRe.FindString(content); hit != "" {
			t.Errorf("%s: drops an auto-generated CHECK by positional name (%q); name the constraint explicitly and drop it by that stable name", name, strings.TrimSpace(hit))
		}
	}
	t.Logf("linted %d migration(s) numbered above grandfathered max %04d", checked, grandfatheredMigrationMax)
}

// TestMigrationIdempotencyDetectors guards the detectors themselves so the lint
// cannot silently pass because a regex broke.
func TestMigrationIdempotencyDetectors(t *testing.T) {
	if createTableRe.FindStringSubmatch("CREATE TABLE foo (")[1] != "" {
		t.Error("CREATE TABLE without IF NOT EXISTS should yield an empty group")
	}
	if createTableRe.FindStringSubmatch("create table if not exists foo (")[1] == "" {
		t.Error("CREATE TABLE IF NOT EXISTS should be recognized")
	}
	if !autoCheckDropRe.MatchString("ALTER TABLE t DROP CHECK t_chk_2") {
		t.Error("auto-generated CHECK drop should be detected")
	}
	if autoCheckDropRe.MatchString("ALTER TABLE t DROP CHECK t_positive_qty") {
		t.Error("explicit CHECK name should not be flagged")
	}
}

// TestDestructiveStyleMigrationIsConflictGated locks the data-safety contract around the PR6
// expand/backfill/contract chain. These migrations are still pending in production, so a future
// cleanup must not accidentally restore MIN(product.id)-wins or drop the source before checking the
// persisted reconciliation report.
func TestDestructiveStyleMigrationIsConflictGated(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(body)
	}

	spine := read("0138_pr6_product_style_id.sql")
	expand := read("0139_pr6_style_fields_add.sql")
	contract := read("0140_pr6_drop_product_style_cols.sql")

	for _, token := range []string{"season_code, season_year", "YEAR(p.created_at)"} {
		if !strings.Contains(spine, token) {
			t.Errorf("0138 synthetic styles must persist a complete typed season; missing %q", token)
		}
	}
	for _, token := range []string{
		"migration_0139_style_field_conflict",
		"'sibling_mismatch'",
		"'target_mismatch'",
		"'orphan_reference'",
		"migration_0139_no_style_field_conflicts",
		"CAST(a.brand AS BINARY) <=> CAST(b.brand AS BINARY)",
		"COALESCE(t.brand, p.brand)",
	} {
		if !strings.Contains(expand, token) {
			t.Errorf("0139 must retain its fail-fast reconciliation invariant; missing %q", token)
		}
	}
	for _, token := range []string{
		"migration_0139_style_field_guard",
		"COUNT(*) FROM migration_0139_style_field_conflict",
	} {
		if !strings.Contains(contract, token) {
			t.Errorf("0140 must refuse destructive contract with unresolved conflicts; missing %q", token)
		}
	}
}
