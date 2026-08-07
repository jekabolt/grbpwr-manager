package techcard

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
)

// The Ф3 queries run inside every marker save (the fingerprint), on both marker reads (the
// fingerprint and the norm peers) and on every designation. A bind error would take all of them down
// at request time, and the only other thing that would catch it is a MySQL-backed test. sqlx.Named
// reproduces the failure without a database: sqlx reads EVERY ':' as a named parameter — including
// one inside a `--` comment, which is why the rationale for these queries lives in Go comments above
// them rather than in the SQL.
func TestMarkerConditionQueriesBind(t *testing.T) {
	bom := sql.NullInt64{Int64: 7, Valid: true}
	cases := []struct {
		name  string
		query string
		args  map[string]any
		want  int
	}{
		{"piece set", markerPieceSetQuery, map[string]any{"card": 1}, 1},
		{"norm peers", markerNormPeersQuery, map[string]any{"card": 1}, 1},
		// The scope predicate binds :bom TWICE — once for the IS NULL arm and once for the equality
		// arm — and a repeated named parameter is exactly the shape that quietly stops binding when
		// someone "simplifies" the predicate.
		{"norm siblings", markerNormScopeSiblingsQuery,
			map[string]any{"card": 1, "bom": bom, "id": 2, "username": "u"}, 4},
		{"norm clear scope", markerNormClearScopeQuery,
			map[string]any{"card": 1, "bom": bom, "id": 2, "username": "u"}, 5},
		{"norm set", markerNormSetQuery, map[string]any{"id": 2, "is_norm": true, "username": "u"}, 3},
		// Ф4: the delete guard. It runs on EVERY manual delete of a раскладка, so a bind error would
		// make deleting any marker fail at request time — including markers no настил has ever heard
		// of. The query is small enough to look harmless and joins two tables from another domain.
		{"lay sections of a marker", markerLaySectionsQuery, map[string]any{"id": 2}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, args, err := sqlx.Named(c.query, c.args)
			if err != nil {
				t.Fatalf("%s query does not bind: %v", c.name, err)
			}
			if len(args) != c.want {
				t.Fatalf("bound args = %d, want %d", len(args), c.want)
			}
		})
	}
}

// The read and the write of a norm scope MUST use one predicate. If they ever drift, the response
// would name a previous norm the write did not actually clear — the one failure mode that turns a
// missing UNIQUE index from a contained risk into a silent one.
func TestMarkerNormScopeIsWrittenOnce(t *testing.T) {
	for _, q := range []string{markerNormScopeSiblingsQuery, markerNormClearScopeQuery} {
		if !strings.Contains(q, markerNormScopePredicate) {
			t.Fatal("both the read and the clear must be built from markerNormScopePredicate")
		}
	}
	// The «no cloth» scope is written longhand, not folded through a sentinel: bom_item_id's NULL is
	// meaningful, and a sentinel that happens to be unreachable today is one a migration can reach.
	if !strings.Contains(markerNormScopePredicate, "bom_item_id IS NULL AND :bom IS NULL") {
		t.Fatal("the unbound scope must be expressed with IS NULL on both sides")
	}
}

// The fingerprint must be a function of the SET. An ORDER BY here would suggest the order matters —
// it does not, entity.PieceSetFingerprint sorts by line_key — and a reader who added one would be
// tempted to drop the sort that actually guarantees it.
func TestMarkerPieceSetQuerySelectsOnlyTheHashedFields(t *testing.T) {
	if strings.Contains(markerPieceSetQuery, "ORDER BY") {
		t.Fatal("the piece-set read must not order: the fingerprint sorts by line_key itself")
	}
	for _, col := range []string{"display_order", "name", "grainline", "fused", "mirrored", "callout_number"} {
		if strings.Contains(markerPieceSetQuery, col) {
			t.Fatalf("%q must not enter the fingerprint — it is not part of what the card CUTS", col)
		}
	}
}
