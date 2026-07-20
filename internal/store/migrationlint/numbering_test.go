package migrationlint

import (
	"os"
	"strings"
	"testing"
)

// knownDuplicateMigrationNumbers records historical prefix collisions that are
// already applied on prod and must NOT be renamed: sql-migrate tracks each
// migration by its full filename, so renaming would change the Id and reapply it,
// halting startup. Every OTHER number must be unique; a new collision fails the
// test below. The value is the exact number of files expected to share the
// prefix, so even the grandfathered collision can't silently grow.
var knownDuplicateMigrationNumbers = map[string]int{
	"0003": 2, // 0003_add_announce_translations.sql + 0003_add_product_version.sql
	// 0195: two phase-2 accounting tracks landed on beta in parallel and both took 0195 —
	// 0195_accounting_delivered.sql (wave 2) and 0195_frs105_coa_accounts.sql (FRS-105 COA gap-fill).
	// Both are already applied on beta and both are idempotent, additive, and independent (different
	// accounts; the FRS-105 seed touches no CHECK). accounting_delivered can't be renumbered (0196's
	// source_type CHECK extension depends on it applying first), so the collision is grandfathered like
	// 0003. Neither may be renamed (sql-migrate tracks by full filename); no THIRD 0195 may be added.
	"0195": 2,
}

// TestMigrationNumbersUnique asserts migration number prefixes are unique except
// for the documented historical collisions. It protects the monotonic-unique
// numbering invariant the rest of the set (and CLAUDE.md) relies on: a future
// duplicate would make apply order depend on the full filename sort, which for
// interdependent DDL can break automigrate on boot.
func TestMigrationNumbersUnique(t *testing.T) {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", migrationsDir, err)
	}
	counts := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			continue
		}
		counts[prefix]++
	}

	for prefix, c := range counts {
		if c <= 1 {
			continue
		}
		if want, ok := knownDuplicateMigrationNumbers[prefix]; ok {
			if c != want {
				t.Errorf("migration number %s is shared by %d files, expected the grandfathered %d; do not add more files with this prefix", prefix, c, want)
			}
			continue
		}
		t.Errorf("migration number %s is used by %d files; numbers must be unique — use the next free number", prefix, c)
	}
}
