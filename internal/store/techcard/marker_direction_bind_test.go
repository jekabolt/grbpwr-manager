package techcard

import (
	"strings"
	"testing"

	"github.com/jmoiron/sqlx"
)

// The direction guard runs on every marker save, and a bind error would take the whole save path
// down at request time with nothing but a MySQL-backed test to catch it. sqlx.Named reproduces both
// failure modes without a database: an unparsable ':' and a name the args map does not carry.
func TestFabricDirectionLinesQueryBinds(t *testing.T) {
	_, args, err := sqlx.Named(fabricDirectionLinesQuery, map[string]any{"id": 1})
	if err != nil {
		t.Fatalf("fabric direction query does not bind: %v", err)
	}
	if len(args) != 1 {
		t.Fatalf("bound args = %d, want 1 (the card id)", len(args))
	}
}

// The refusal this query feeds is field-tagged `bom_items[i].fabric_direction`, and i is a position
// in the array the CLIENT holds — which the card read builds with ORDER BY display_order, id (the
// bom load in materials.go). If the two orderings ever drift, the index keeps being emitted and
// keeps looking plausible while pointing at the wrong row: the worst kind of wrong.
func TestFabricDirectionLinesQueryOrdersLikeTheCardRead(t *testing.T) {
	if !strings.Contains(fabricDirectionLinesQuery, "ORDER BY bi.display_order, bi.id") {
		t.Fatal("the guard must read the BOM in the same order the card read returns it")
	}
	// And it must read the WHOLE BOM: filtering roll goods in SQL would renumber the rows.
	if strings.Contains(fabricDirectionLinesQuery, "section IN") {
		t.Fatal("roll goods are filtered in Go so the index stays the card's, not the fabric-only one")
	}
	// The NAME must resolve through the catalogue exactly as the card read resolves it: a
	// material-linked line legitimately has no name of its own, and reading bi.name alone printed a
	// ULID at the operator where the BOM tab shows a fabric.
	for _, want := range []string{"LEFT JOIN material m", "COALESCE(NULLIF(bi.name, ''), m.name"} {
		if !strings.Contains(fabricDirectionLinesQuery, want) {
			t.Fatalf("the guard must resolve the line name like the card read does (missing %q)", want)
		}
	}
}
