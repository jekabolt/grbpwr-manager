package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
)

func areaScope(rows ...PieceAreaRow) map[string]PieceAreaScope {
	return map[string]PieceAreaScope{"main": {ScopeKey: "main", Rows: rows}}
}

func areaRow(piece string, sizeID int, cm2 string) PieceAreaRow {
	r := PieceAreaRow{PieceLineKey: piece, AreaCm2: decimal.RequireFromString(cm2)}
	if sizeID > 0 {
		r.SizeId = sql.NullInt64{Int64: int64(sizeID), Valid: true}
	}
	return r
}

func width(cm string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(cm), Valid: true}
}

// TestAreaEstimateUnitConversion is the test this whole function exists to not fail.
//
// Area in cm² divided by width in cm yields CENTIMETRES. The price on the BOM line is per the SLOT's
// unit — metres, on every fabric line in the database. Skipping the conversion overstates a metre
// slot by a factor of ONE HUNDRED, and does it with a number that looks entirely plausible on
// screen: 71 instead of 0.71 reads as "an expensive coat", not as "a bug".
func TestAreaEstimateUnitConversion(t *testing.T) {
	// One piece, 14000 cm², cut once, on a 140 cm usable width → 100 cm → 1 m.
	areas := areaScope(areaRow("P1", 4, "14000"))
	pieces := []AreaEstimatePiece{{LineKey: "P1", PerGarment: 1}}

	for _, c := range []struct {
		unit string
		want string
	}{
		{"m", "1"},
		{"м", "1"}, // the same unit written in Cyrillic — vocabulary, not string compare
		{"cm", "100"},
		{"mm", "1000"},
	} {
		got, refusal := AreaEstimateNorm("main", pieces, areas, width("140"), c.unit, 4)
		if refusal != "" {
			t.Fatalf("unit %q: unexpected refusal %q", c.unit, refusal)
		}
		if !got.Equal(decimal.RequireFromString(c.want)) {
			t.Errorf("unit %q: got %s, want %s", c.unit, got, c.want)
		}
	}
}

// TestAreaEstimateRefusesKilograms: a kg norm is a length converted through the article's density
// and roll width. Approximating it would produce a plausible number in the wrong unit — the worst
// possible failure for a figure that becomes a purchase order.
func TestAreaEstimateRefusesKilograms(t *testing.T) {
	_, refusal := AreaEstimateNorm("main",
		[]AreaEstimatePiece{{LineKey: "P1", PerGarment: 1}},
		areaScope(areaRow("P1", 4, "14000")), width("140"), "kg", 4)
	if refusal != AreaEstimateUnitUnknown {
		t.Fatalf("kg accepted (refusal=%q); it must refuse until the density path is built", refusal)
	}
}

// TestAreaEstimateMultiplicity: pieces_per_garment multiplies. A sleeve cut twice costs twice.
func TestAreaEstimateMultiplicity(t *testing.T) {
	got, refusal := AreaEstimateNorm("main",
		[]AreaEstimatePiece{{LineKey: "P1", PerGarment: 2}},
		areaScope(areaRow("P1", 4, "14000")), width("140"), "m", 4)
	if refusal != "" {
		t.Fatalf("unexpected refusal %q", refusal)
	}
	if !got.Equal(decimal.RequireFromString("2")) {
		t.Errorf("got %s, want 2 (two instances of the same contour)", got)
	}
}

// TestAreaEstimateUngradedPieceEntersEverySize: a piece with no size is not "missing from this size",
// it is cut whole into every size — the same rule MarkerSizeAreasPerGarment applies.
func TestAreaEstimateUngradedPieceEntersEverySize(t *testing.T) {
	areas := areaScope(areaRow("GRADED", 4, "7000"), areaRow("FLAT", 0, "7000"))
	got, refusal := AreaEstimateNorm("main", []AreaEstimatePiece{
		{LineKey: "GRADED", PerGarment: 1}, {LineKey: "FLAT", PerGarment: 1},
	}, areas, width("140"), "m", 4)
	if refusal != "" {
		t.Fatalf("unexpected refusal %q", refusal)
	}
	if !got.Equal(decimal.RequireFromString("1")) {
		t.Errorf("got %s, want 1 (7000+7000 cm² over 140 cm = 100 cm)", got)
	}
}

// TestAreaEstimateRefusesIncompleteSet: a piece with no area for the requested size and no ungraded
// area refuses the SIZE, it does not quietly cost the pieces that happen to be measured. An
// understated norm surfaces in the warehouse, not on the screen where it was invented.
func TestAreaEstimateRefusesIncompleteSet(t *testing.T) {
	areas := areaScope(areaRow("P1", 4, "14000")) // nothing for P2
	_, refusal := AreaEstimateNorm("main", []AreaEstimatePiece{
		{LineKey: "P1", PerGarment: 1}, {LineKey: "P2", PerGarment: 1},
	}, areas, width("140"), "m", 4)
	if refusal != AreaEstimateIncomplete {
		t.Fatalf("incomplete set produced %q, want %q", refusal, AreaEstimateIncomplete)
	}
}

// TestAreaEstimateRefusesStaleAreas: re-uploaded patterns mean the measurement describes files that
// no longer exist. «Approximately the same» is not a property geometry has.
func TestAreaEstimateRefusesStaleAreas(t *testing.T) {
	areas := map[string]PieceAreaScope{"main": {
		ScopeKey: "main", Stale: true, Rows: []PieceAreaRow{areaRow("P1", 4, "14000")},
	}}
	_, refusal := AreaEstimateNorm("main",
		[]AreaEstimatePiece{{LineKey: "P1", PerGarment: 1}}, areas, width("140"), "m", 4)
	if refusal != AreaEstimateStale {
		t.Fatalf("stale areas produced %q, want %q", refusal, AreaEstimateStale)
	}
}

// TestAreaEstimateRefusalsAreDistinct: each refusal is a different next action, so none may collapse
// into another. A single «не посчитано» sent operators to look everywhere at once.
func TestAreaEstimateRefusalsAreDistinct(t *testing.T) {
	pieces := []AreaEstimatePiece{{LineKey: "P1", PerGarment: 1}}
	areas := areaScope(areaRow("P1", 4, "14000"))
	cases := []struct {
		name string
		want AreaEstimateRefusal
		run  func() (decimal.Decimal, AreaEstimateRefusal)
	}{
		{"no pieces", AreaEstimateNoAssignments, func() (decimal.Decimal, AreaEstimateRefusal) {
			return AreaEstimateNorm("main", nil, areas, width("140"), "m", 4)
		}},
		{"no areas", AreaEstimateNoAreas, func() (decimal.Decimal, AreaEstimateRefusal) {
			return AreaEstimateNorm("lining", pieces, areas, width("140"), "m", 4)
		}},
		{"no width", AreaEstimateNoWidth, func() (decimal.Decimal, AreaEstimateRefusal) {
			return AreaEstimateNorm("main", pieces, areas, decimal.NullDecimal{}, "m", 4)
		}},
		{"zero width", AreaEstimateNoWidth, func() (decimal.Decimal, AreaEstimateRefusal) {
			return AreaEstimateNorm("main", pieces, areas, width("0"), "m", 4)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, got := c.run(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
			if AreaEstimateRefusalText(c.want) == "" {
				t.Errorf("refusal %q has no operator-facing text", c.want)
			}
		})
	}
}
