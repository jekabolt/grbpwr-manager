package entity

import "testing"

// TestProductionLayModeMatchesSchema pins the two strings against chk_prlay_mode (0281), spelling
// AND case. The CHECK is deliberately case-sensitive (MySQL's REGEXP is not, so 0281 adds an
// explicit STRCMP on the binary form) precisely so a 'Face_Up' cannot enter the column and then fail
// every Go comparison in silence. dto.LayFaceMode carries the same literals for the coverage
// arithmetic; if either side is ever "tidied", one of the two tests fails.
func TestProductionLayModeMatchesSchema(t *testing.T) {
	if string(ProductionLayModeFaceUp) != "face_up" {
		t.Errorf("face-up mode must be stored as %q, got %q", "face_up", ProductionLayModeFaceUp)
	}
	if string(ProductionLayModeFaceToFace) != "face_to_face" {
		t.Errorf("face-to-face mode must be stored as %q, got %q", "face_to_face", ProductionLayModeFaceToFace)
	}
	if !IsValidProductionLayMode(ProductionLayModeFaceUp) || !IsValidProductionLayMode(ProductionLayModeFaceToFace) {
		t.Error("both storable modes must validate")
	}
	for _, bad := range []ProductionLayMode{"", "Face_Up", "FACE_UP", "faceup", "face-to-face"} {
		if IsValidProductionLayMode(bad) {
			t.Errorf("%q must not validate: the column's dictionary is closed by spelling and by case", bad)
		}
	}
}

// TestProductionLayModeParity: only face-to-face pairs its plies. The rule lives on the mode rather
// than at the call site because both the save's refusal and the coverage arithmetic ask it, and two
// copies would eventually disagree about the one thing that turns half a set of выкройки into pairs.
func TestProductionLayModeParity(t *testing.T) {
	if ProductionLayModeFaceUp.RequiresEvenPlies() {
		t.Error("face up lays each ply the same way up; an odd count is perfectly ordinary")
	}
	if !ProductionLayModeFaceToFace.RequiresEvenPlies() {
		t.Error("face to face yields a left and a right from a PAIR of plies; the count must be even")
	}
}

// TestProductionLayKeyIsTheRunLineKeyRule guards the delegation. The lay and section keys are the
// same 26-character world as the run's plan lines, on purpose: one charset rule and one encoder, so
// the store can never mint a key its own validator rejects.
func TestProductionLayKeyIsTheRunLineKeyRule(t *testing.T) {
	if ProductionLayKeyLen != ProductionRunLineKeyLen {
		t.Fatalf("lay keys must be the same width as run line keys: %d vs %d",
			ProductionLayKeyLen, ProductionRunLineKeyLen)
	}
	key, err := MintProductionLayKey()
	if err != nil {
		t.Fatalf("minting a lay key must succeed: %v", err)
	}
	if !IsValidProductionLayKey(key) {
		t.Fatalf("the minter produced %q, which its own validator rejects", key)
	}
	for _, bad := range []string{"", "short", "01layseCtion0000000000000a", "01LAYSECTION!000000000000A"} {
		if IsValidProductionLayKey(bad) {
			t.Errorf("%q must not validate as a lay key", bad)
		}
	}
}

// TestNormalizeProductionRunLayQty: the canonical form is what makes "stale" a statement about the
// DATA. Duplicates are summed (a run may plan one colourway/size across several lines), non-positive
// quantities drop out, and the result is ordered by size — so two reads of unchanged data can never
// look different.
func TestNormalizeProductionRunLayQty(t *testing.T) {
	got := NormalizeProductionRunLayQty([]ProductionRunLayQtyEntry{
		{SizeId: 3, Qty: 5}, {SizeId: 1, Qty: 10}, {SizeId: 3, Qty: 7}, {SizeId: 2, Qty: 0}, {SizeId: 4, Qty: -3},
	})
	want := []ProductionRunLayQtyEntry{{SizeId: 1, Qty: 10}, {SizeId: 3, Qty: 12}}
	if len(got) != len(want) {
		t.Fatalf("normalized set = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalized set = %+v, want %+v", got, want)
		}
	}
	if empty := NormalizeProductionRunLayQty(nil); empty == nil || len(empty) != 0 {
		t.Errorf("an empty set normalizes to an empty slice, not nil: %+v", empty)
	}
}

// TestProductionRunLayQuantitiesStale is the badge, and probe §13.2's arithmetic half. It must be
// blind to order and to duplicate splitting, and it must notice every real difference: a changed
// quantity, a new size, a size that went away.
func TestProductionRunLayQuantitiesStale(t *testing.T) {
	snapshot := []ProductionRunLayQtyEntry{{SizeId: 1, Qty: 10}, {SizeId: 2, Qty: 20}}

	cases := []struct {
		name    string
		current []ProductionRunLayQtyEntry
		stale   bool
	}{
		{"same set", []ProductionRunLayQtyEntry{{SizeId: 1, Qty: 10}, {SizeId: 2, Qty: 20}}, false},
		{"same set, other order", []ProductionRunLayQtyEntry{{SizeId: 2, Qty: 20}, {SizeId: 1, Qty: 10}}, false},
		{"same totals, split lines", []ProductionRunLayQtyEntry{{SizeId: 1, Qty: 4}, {SizeId: 1, Qty: 6}, {SizeId: 2, Qty: 20}}, false},
		{"quantity moved", []ProductionRunLayQtyEntry{{SizeId: 1, Qty: 11}, {SizeId: 2, Qty: 20}}, true},
		{"size added", []ProductionRunLayQtyEntry{{SizeId: 1, Qty: 10}, {SizeId: 2, Qty: 20}, {SizeId: 3, Qty: 5}}, true},
		{"size removed", []ProductionRunLayQtyEntry{{SizeId: 1, Qty: 10}}, true},
		{"grid emptied", nil, true},
	}
	for _, c := range cases {
		if got := ProductionRunLayQuantitiesStale(snapshot, c.current); got != c.stale {
			t.Errorf("%s: stale = %v, want %v", c.name, got, c.stale)
		}
	}
}

// TestProductionRunLayTotalPlies: the end losses apply per ply, twice (both ends), so Σ plies is the
// multiplier the whole demand arithmetic hangs on.
func TestProductionRunLayTotalPlies(t *testing.T) {
	lay := ProductionRunLay{Sections: []ProductionRunLaySection{{Plies: 24}, {Plies: 12}, {Plies: 1}}}
	if got := lay.TotalPlies(); got != 37 {
		t.Errorf("total plies = %d, want 37", got)
	}
	if got := (ProductionRunLay{}).TotalPlies(); got != 0 {
		t.Errorf("a lay with no sections lays no plies, got %d", got)
	}
}

// TestProductionRunLayBroken: a lay whose BOM slot was deleted (fk_prlay_bom is SET NULL) is BROKEN.
// It still NAMES the slot it lost through the snapshotted line key — the price paid for SET NULL —
// so it can be reported instead of quietly dropping out of the plan.
func TestProductionRunLayBroken(t *testing.T) {
	broken := ProductionRunLay{BomLineKey: "01LAYBOMSLOT00000000000000"}
	if !broken.Broken() {
		t.Error("a lay without a BOM item is broken")
	}
	if broken.BomLineKey == "" {
		t.Error("a broken lay must still be able to name the slot it lost")
	}
}
