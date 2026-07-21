package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func bomLine(section entity.TechCardBomSection, name string, materialID int64, supplierRef string) entity.TechCardBomItem {
	return entity.TechCardBomItem{
		Section:     section,
		Name:        name,
		MaterialId:  sql.NullInt64{Int64: materialID, Valid: materialID > 0},
		SupplierRef: sql.NullString{String: supplierRef, Valid: supplierRef != ""},
	}
}

// TestBomNaturalKeyIsStableAcrossNameResolution is the regression guard for the interaction between
// two independently reasonable changes: BOM names are now RESOLVED from the linked material on read,
// and the admin client now sends name:'' for a linked line that never had its own name.
//
// preserveStoredCosting matches the STORED card (enriched, so a linked line carries the catalog
// material's name) against the INCOMING payload (raw, so the same line may carry an empty name). If
// the key included the name, those two would not match, the anti-erase restore would silently skip,
// and an account WITHOUT costing:write would blank the purchase price on every linked BOM line on
// save -- silent loss of confidential data, with no error anywhere. Keying a linked line on its
// material id makes the two sides agree by construction.
func TestBomNaturalKeyIsStableAcrossNameResolution(t *testing.T) {
	const fabric = entity.BomSectionFabric

	stored := bomLine(fabric, "Cotton Twill 240gsm", 42, "SR-1") // as read back, name resolved
	incoming := bomLine(fabric, "", 42, "SR-1")                  // as sent by the client for a linked line

	if bomNaturalKey(stored) != bomNaturalKey(incoming) {
		t.Fatalf("a linked line must key identically whether or not its name is resolved:\n stored   = %q\n incoming = %q",
			bomNaturalKey(stored), bomNaturalKey(incoming))
	}

	// A stale copy of the name must not change the key either -- renaming the material in the catalog
	// changes the resolved name on every unreleased card that links it.
	renamed := bomLine(fabric, "Cotton Twill 260gsm (renamed)", 42, "SR-1")
	if bomNaturalKey(stored) != bomNaturalKey(renamed) {
		t.Errorf("renaming the linked material must not change the key: %q vs %q",
			bomNaturalKey(stored), bomNaturalKey(renamed))
	}
}

// TestBomNaturalKeyStillSeparatesDistinctLines checks the material-id keying did not collapse lines
// that must stay distinct, and that free-text lines still key on their own name (they have no
// material to key on).
func TestBomNaturalKeyStillSeparatesDistinctLines(t *testing.T) {
	const fabric = entity.BomSectionFabric
	const hardware = entity.BomSectionHardware

	tests := []struct {
		name string
		a, b entity.TechCardBomItem
		same bool
	}{
		{
			name: "different materials are different lines",
			a:    bomLine(fabric, "", 1, ""),
			b:    bomLine(fabric, "", 2, ""),
		},
		{
			name: "same material in different sections are different lines",
			a:    bomLine(fabric, "", 1, ""),
			b:    bomLine(hardware, "", 1, ""),
		},
		{
			name: "same material with different supplier refs are different lines",
			a:    bomLine(fabric, "", 1, "SR-1"),
			b:    bomLine(fabric, "", 1, "SR-2"),
		},
		{
			name: "free-text lines still key on their own name",
			a:    bomLine(fabric, "hand-typed twill", 0, ""),
			b:    bomLine(fabric, "hand-typed poplin", 0, ""),
		},
		{
			name: "identical free-text lines still collide, as before",
			a:    bomLine(fabric, "hand-typed twill", 0, ""),
			b:    bomLine(fabric, "  Hand-Typed Twill  ", 0, ""), // case/space normalized
			same: true,
		},
		{
			// A free-text line must never collide with a linked one just because the linked line's
			// resolved name happens to match.
			name: "free text does not collide with a linked line of the same name",
			a:    bomLine(fabric, "Cotton Twill 240gsm", 0, ""),
			b:    bomLine(fabric, "Cotton Twill 240gsm", 42, ""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ka, kb := bomNaturalKey(tt.a), bomNaturalKey(tt.b)
			if tt.same && ka != kb {
				t.Errorf("expected the same key, got %q and %q", ka, kb)
			}
			if !tt.same && ka == kb {
				t.Errorf("expected different keys, both were %q", ka)
			}
		})
	}
}
