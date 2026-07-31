package techcard

import (
	"testing"

	"github.com/jmoiron/sqlx"
)

// A ':' anywhere in the SQL text — including inside a '--' comment — is parsed by sqlx as a named
// parameter and fails the bind at request time, taking the whole readiness endpoint down. This has
// now happened twice (the file's doc comment was written after the first time), so the bind is
// pinned by a test: no database needed, sqlx.Named alone reproduces the failure.
func TestTechCardReadinessQueryBinds(t *testing.T) {
	_, args, err := sqlx.Named(techCardReadinessQuery, map[string]any{"id": 1, "archived": "archived"})
	if err != nil {
		t.Fatalf("readiness query does not bind: %v", err)
	}
	if len(args) == 0 {
		t.Fatal("expected bound args")
	}
}
