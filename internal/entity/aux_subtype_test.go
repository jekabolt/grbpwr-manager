package entity

import "testing"

// TestAuxSubtypeFromName pins the name → sub-type heuristic that backfills existing auxiliary cards
// (migration 0173). It MUST stay identical to that migration's CASE; each case below hits one branch,
// in the same most-specific-first order, plus the "leave NULL" fallthrough the task requires for names
// the heuristic can't confidently classify.
func TestAuxSubtypeFromName(t *testing.T) {
	cases := []struct {
		name string
		want TechCardAuxSubtype
		ok   bool
	}{
		{"Dust Bag", AuxSubtypeDustBag, true},
		{"COTTON DUST POUCH", AuxSubtypeDustBag, true},
		{"Woven Hangtag", AuxSubtypeHangtag, true},
		{"Paper hang tag", AuxSubtypeHangtag, true},
		{"hang-tag string", AuxSubtypeHangtag, true},
		{"Care / Composition Label", AuxSubtypeCareLabel, true},
		{"Size Label sew-in", AuxSubtypeSizeLabel, true},
		{"Main Brand Label", AuxSubtypeBrandLabel, true},
		{"Box sticker", AuxSubtypeSticker, true}, // sticker branch precedes box → sticker wins
		{"Cardboard Insert", AuxSubtypeInsert, true},
		{"Shipping Box", AuxSubtypeBox, true},
		{"Kraft Shopper", AuxSubtypeDustBag, true},
		{"Garment Bag", AuxSubtypeDustBag, true},
		{"Woven neck label", "", false}, // "label" alone is not confident enough → NULL
		{"Polybag", "", false},          // a bare bag that isn't dust/garment/shopper → NULL, not guessed
		{"Mystery Item", "", false},
	}
	for _, tc := range cases {
		got, ok := AuxSubtypeFromName(tc.name)
		if ok != tc.ok || got != tc.want {
			t.Errorf("AuxSubtypeFromName(%q) = (%q,%v), want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
		if ok && !IsValidTechCardAuxSubtype(got) {
			t.Errorf("AuxSubtypeFromName(%q) returned non-valid subtype %q", tc.name, got)
		}
	}
}

// TestAuxSubtypeValidSetIsClosed guards that every constant is in the Valid set and vice-versa, so a new
// value can't be added to one without the other (the DB CHECK drift test then catches the DB leg).
func TestAuxSubtypeValidSetIsClosed(t *testing.T) {
	all := []TechCardAuxSubtype{
		AuxSubtypeBrandLabel, AuxSubtypeCareLabel, AuxSubtypeSizeLabel, AuxSubtypeHangtag,
		AuxSubtypeSticker, AuxSubtypeDustBag, AuxSubtypeBox, AuxSubtypeInsert, AuxSubtypeHanger, AuxSubtypeOther,
	}
	if len(all) != len(ValidTechCardAuxSubtypes) {
		t.Fatalf("constant list (%d) and ValidTechCardAuxSubtypes (%d) differ in size", len(all), len(ValidTechCardAuxSubtypes))
	}
	for _, s := range all {
		if !ValidTechCardAuxSubtypes[s] {
			t.Errorf("constant %q missing from ValidTechCardAuxSubtypes", s)
		}
	}
}
