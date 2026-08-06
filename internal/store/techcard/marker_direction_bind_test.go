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
	if !strings.Contains(fabricDirectionLinesQuery, "ORDER BY display_order, id") {
		t.Fatal("the guard must read the BOM in the same order the card read returns it")
	}
	// And it must read the WHOLE BOM: filtering roll goods in SQL would renumber the rows.
	if strings.Contains(fabricDirectionLinesQuery, "section IN") {
		t.Fatal("roll goods are filtered in Go so the index stays the card's, not the fabric-only one")
	}
}

// The catalogue join must stay OFF this query. It runs inside a SERIALIZABLE transaction, where
// InnoDB promotes a plain SELECT to FOR SHARE — so joining `material` here would let a concurrent
// material edit block or deadlock an unrelated marker save, reported as a 500. The names are prose
// for a refusal and are resolved by fabricLineNamer, on the path that is already failing.
func TestFabricDirectionLinesQueryDoesNotTouchTheCatalogue(t *testing.T) {
	if strings.Contains(fabricDirectionLinesQuery, "material") {
		t.Fatal("the hot path must not read the material catalogue; names are resolved lazily on refusal")
	}
}
