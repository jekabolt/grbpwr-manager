package techcard

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
)

// pieceUpsertParams mirrors the map upsertTechCardPieces builds, so a parameter added to the query and
// forgotten in the map (or the reverse) surfaces here as a bind failure instead of as a 500 on the
// card save path.
func pieceUpsertParams() map[string]any {
	return map[string]any{
		"tech_card_id":         1,
		"name":                 "полочка",
		"line_key":             "01ABCDEF0000000000000001",
		"pieces_per_garment":   2,
		"mirrored":             false,
		"cut_symmetry":         sql.NullString{String: "mirrored", Valid: true},
		"cut_symmetry_omitted": false,
		"grainline":            "lengthwise",
		"fused":                false,
		"callout_number":       sql.NullInt32{},
		"detached":             false,
		"note":                 sql.NullString{},
		"display_order":        0,
		"id":                   7,
	}
}

// The piece upsert grew a guarded column (0275). sqlx parses ':' ANYWHERE in a named query — including
// inside a `--` SQL comment — as a parameter, and a name the args map does not carry fails at BIND
// time, i.e. at request time on the save path, with nothing but a MySQL-backed test to catch it.
// sqlx.Named reproduces both failure modes without a database.
func TestPieceUpsertQueriesBind(t *testing.T) {
	for name, q := range map[string]string{"update": pieceUpdateQuery, "insert": pieceInsertQuery} {
		args, _, err := sqlx.Named(q, pieceUpsertParams())
		if err != nil {
			t.Fatalf("piece %s query does not bind: %v", name, err)
		}
		if strings.Contains(args, ":") {
			t.Fatalf("piece %s query still holds a ':' after binding: %s", name, args)
		}
	}
	if _, args, err := sqlx.Named(pieceReadQuery, map[string]any{"ids": []int{1, 2}}); err != nil || len(args) != 1 {
		t.Fatalf("piece read query does not bind: err=%v args=%d", err, len(args))
	}
}

// The UPDATE must keep the stored value when the payload omitted the field. Losing the IF() is
// invisible to every test that sends the field and catastrophic for the one client that cannot: it
// clears the marking on every piece of the card, and the marking cannot be reconstructed without a
// human holding the patterns.
func TestPieceUpdateGuardsCutSymmetryAgainstAStaleTab(t *testing.T) {
	if !strings.Contains(pieceUpdateQuery, "cut_symmetry=IF(:cut_symmetry_omitted, cut_symmetry, :cut_symmetry)") {
		t.Fatal("the piece UPDATE must carry the stored cut_symmetry forward when the payload omitted it")
	}
	// The INSERT has nothing to carry — a new row has no stored value — so it must NOT be guarded, or
	// it would read a column that does not exist yet for that row.
	if strings.Contains(pieceInsertQuery, "cut_symmetry_omitted") {
		t.Fatal("the piece INSERT must write cut_symmetry directly; there is no stored value to carry")
	}
}

// A column the write stores and the read never loads makes the write-side and read-side digest
// projections permanently disagree, so the sign-off they feed can never match its own stored value
// again — the failure already paid for once on the piece-material line_key. Cheap to assert, so
// assert it.
func TestPieceReadSelectsEveryWrittenColumn(t *testing.T) {
	for _, col := range []string{
		"pieces_per_garment", "mirrored", "cut_symmetry", "grainline", "fused",
		"callout_number", "detached", "note", "line_key",
	} {
		if !strings.Contains(pieceReadQuery, col) {
			t.Errorf("the piece read must SELECT %s: the digest hashes it on the write side", col)
		}
	}
}
