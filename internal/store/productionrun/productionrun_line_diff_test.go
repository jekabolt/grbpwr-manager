package productionrun

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func ni32(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

func line(key string, product int32, size, planned int) entity.ProductionRunLine {
	ln := entity.ProductionRunLine{LineKey: key, SizeId: size, PlannedQty: planned}
	if product > 0 {
		ln.ProductId = ni32(product)
	}
	return ln
}

func stored(id int, key string, product int32, size int) runLineIdentity {
	row := runLineIdentity{Id: id, LineKey: key, SizeId: size}
	if product > 0 {
		row.ProductId = ni32(product)
	}
	return row
}

func plan(t *testing.T, rows []runLineIdentity, lines []entity.ProductionRunLine) runLineDiff {
	t.Helper()
	keys, err := resolveRunLineKeys(lines)
	if err != nil {
		t.Fatalf("resolveRunLineKeys: %v", err)
	}
	return planRunLineDiff(rows, lines, keys)
}

// TestRunLineDiffKeepsMatchedRowIds is the whole point of migration 0230: a line whose key is already
// stored is UPDATEd, never delete+reinserted, so its id (the future receipt-line FK target) survives.
func TestRunLineDiffKeepsMatchedRowIds(t *testing.T) {
	rows := []runLineIdentity{
		stored(41, "AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1),
		stored(42, "BBBBBBBBBBBBBBBBBBBBBBBBBB", 11, 2),
	}
	// A plain quantity edit: same keys, same slots.
	got := plan(t, rows, []entity.ProductionRunLine{
		line("AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1, 90),
		line("BBBBBBBBBBBBBBBBBBBBBBBBBB", 11, 2, 10),
	})
	if len(got.deletes) != 0 || len(got.inserts) != 0 {
		t.Fatalf("a quantity-only edit must not delete or insert rows: %+v", got)
	}
	if len(got.updates) != 2 {
		t.Fatalf("want 2 in-place updates, got %d", len(got.updates))
	}
	for i, want := range []int{41, 42} {
		if got.updates[i].id != want {
			t.Errorf("update %d hits id %d, want the stored id %d", i, got.updates[i].id, want)
		}
		if got.updates[i].park {
			t.Errorf("update %d parks its product although the slot did not move", i)
		}
	}
}

func TestRunLineDiffDeletesVanishedAndInsertsNewKeys(t *testing.T) {
	rows := []runLineIdentity{
		stored(41, "AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1),
		stored(42, "BBBBBBBBBBBBBBBBBBBBBBBBBB", 11, 2),
	}
	got := plan(t, rows, []entity.ProductionRunLine{
		line("AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1, 5),
		line("CCCCCCCCCCCCCCCCCCCCCCCCCC", 11, 3, 7),
	})
	if len(got.deletes) != 1 || got.deletes[0] != 42 {
		t.Errorf("the key that vanished from the payload must be deleted by id, got %v", got.deletes)
	}
	if len(got.inserts) != 1 || got.inserts[0] != 1 {
		t.Errorf("the unknown key must be inserted, got %v", got.inserts)
	}
	if len(got.updates) != 1 || got.updates[0].id != 41 {
		t.Errorf("the surviving key must be updated in place, got %+v", got.updates)
	}
}

// A stored row with no line_key predates the 0230 backfill: it has no identity to match on, so the
// diff can only replace it — but it must never be silently kept and duplicated either.
func TestRunLineDiffDropsKeylessStoredRows(t *testing.T) {
	rows := []runLineIdentity{stored(41, "", 11, 1)}
	got := plan(t, rows, []entity.ProductionRunLine{line("AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1, 5)})
	if len(got.deletes) != 1 || got.deletes[0] != 41 {
		t.Errorf("a keyless stored row must be deleted, got %v", got.deletes)
	}
	if len(got.inserts) != 1 || len(got.updates) != 0 {
		t.Errorf("the incoming keyed line must be inserted, got %+v", got)
	}
}

// uniq_prl (run_id, product_id, size_id) is a SECOND unique key over the same rows. A line that moves
// to another slot must be parked at product_id = NULL for the duration of the diff, or the UPDATE
// collides with whatever still sits in the target slot.
func TestRunLineDiffParksOnlyRowsThatMoveOntoARealSlot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stored   runLineIdentity
		incoming entity.ProductionRunLine
		wantPark bool
	}{
		{
			name:     "size changes",
			stored:   stored(41, "AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1),
			incoming: line("AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 2, 5),
			wantPark: true,
		},
		{
			name:     "planning line finally gets its colour-model",
			stored:   stored(41, "AAAAAAAAAAAAAAAAAAAAAAAAAA", 0, 1),
			incoming: line("AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1, 5),
			wantPark: true,
		},
		{
			name:     "nothing moves",
			stored:   stored(41, "AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1),
			incoming: line("AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1, 5),
			wantPark: false,
		},
		{
			// A NULL product_id never makes a unique-index entry duplicate, so a product-less line
			// occupies no slot and can move in a single statement.
			name:     "product-less line changes size",
			stored:   stored(41, "AAAAAAAAAAAAAAAAAAAAAAAAAA", 0, 1),
			incoming: line("AAAAAAAAAAAAAAAAAAAAAAAAAA", 0, 2, 5),
			wantPark: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := plan(t, []runLineIdentity{tc.stored}, []entity.ProductionRunLine{tc.incoming})
			if len(got.updates) != 1 {
				t.Fatalf("want one in-place update, got %+v", got)
			}
			if got.updates[0].park != tc.wantPark {
				t.Errorf("park = %v, want %v", got.updates[0].park, tc.wantPark)
			}
		})
	}
}

// Two lines swapping slots in one save is the case a naive keyed UPDATE cannot survive: both must
// park, so neither ever holds the slot the other is moving into.
func TestRunLineDiffParksBothSidesOfASlotSwap(t *testing.T) {
	rows := []runLineIdentity{
		stored(41, "AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1),
		stored(42, "BBBBBBBBBBBBBBBBBBBBBBBBBB", 11, 2),
	}
	got := plan(t, rows, []entity.ProductionRunLine{
		line("AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 2, 5),
		line("BBBBBBBBBBBBBBBBBBBBBBBBBB", 11, 1, 6),
	})
	if len(got.updates) != 2 {
		t.Fatalf("want 2 updates, got %+v", got.updates)
	}
	for i, u := range got.updates {
		if !u.park {
			t.Errorf("swap side %d (id %d) is not parked; its UPDATE would collide with uniq_prl", i, u.id)
		}
	}
	if got.updates[0].id != 41 || got.updates[1].id != 42 {
		t.Errorf("a swap must still keep both ids, got %+v", got.updates)
	}
}

// The receive modal writes counted quantities through an ordinary section save, so the diff's column
// set has to carry them; a hand-written UPDATE list that forgot one would erase counted facts.
func TestRunLineParamsCarryTheFullColumnSet(t *testing.T) {
	ln := entity.ProductionRunLine{
		LineKey:     "AAAAAAAAAAAAAAAAAAAAAAAAAA",
		ProductId:   ni32(11),
		SizeId:      2,
		PlannedQty:  100,
		ReceivedQty: sql.NullInt64{Int64: 97, Valid: true},
		DefectQty:   sql.NullInt64{Int64: 3, Valid: true},
	}
	params := runLineParams(7, &ln, ln.LineKey)
	for column, want := range map[string]any{
		"run_id":       7,
		"line_key":     ln.LineKey,
		"product_id":   ln.ProductId,
		"size_id":      2,
		"planned_qty":  100,
		"received_qty": ln.ReceivedQty,
		"defect_qty":   ln.DefectQty,
	} {
		got, ok := params[column]
		if !ok {
			t.Errorf("column %q is missing from the line params", column)
			continue
		}
		if got != want {
			t.Errorf("column %q = %v, want %v", column, got, want)
		}
	}
	if len(params) != 7 {
		t.Errorf("line params carry %d columns, want exactly the 7 written columns: %v", len(params), params)
	}
}

func TestResolveRunLineKeysMintsAndRejectsDuplicates(t *testing.T) {
	keys, err := resolveRunLineKeys([]entity.ProductionRunLine{
		line("", 11, 1, 5),
		line("  AAAAAAAAAAAAAAAAAAAAAAAAAA  ", 11, 2, 5),
	})
	if err != nil {
		t.Fatalf("resolveRunLineKeys: %v", err)
	}
	if !entity.IsValidProductionRunLineKey(keys[0]) {
		t.Errorf("a keyless line was minted %q, which is not a valid line key", keys[0])
	}
	if keys[1] != "AAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("a submitted key must be trimmed and kept, got %q", keys[1])
	}
	if _, err := resolveRunLineKeys([]entity.ProductionRunLine{
		line("AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 1, 5),
		line("AAAAAAAAAAAAAAAAAAAAAAAAAA", 11, 2, 5),
	}); err == nil {
		t.Error("two lines claiming the same identity must be rejected, not collapsed onto one row")
	}
}
