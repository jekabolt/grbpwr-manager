package techcard

import (
	"testing"

	"github.com/jmoiron/sqlx"
)

// The direction guard runs on every marker save, and its query is assembled from the roll-goods
// fragment — so a missing bind arg or a stray ':' would take the whole save path down at request
// time, with nothing but a MySQL-backed test to catch it. sqlx.Named reproduces both without a
// database: it fails on an unparsable name and on a name the args map does not carry.
func TestFabricDirectionLinesQueryBinds(t *testing.T) {
	_, args, err := sqlx.Named(fabricDirectionLinesQuery, rollGoodsSectionArgs(map[string]any{"id": 1}))
	if err != nil {
		t.Fatalf("fabric direction query does not bind: %v", err)
	}
	// id + the four roll-goods sections; a fragment that stopped contributing its families would
	// silently narrow the guard to nothing.
	if len(args) != 5 {
		t.Fatalf("bound args = %d, want 5 (id + four roll-goods sections)", len(args))
	}
}
