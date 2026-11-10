package entity

import (
	"database/sql"
	"testing"

	"github.com/shopspring/decimal"
)

// legacyPlanLengthUnits is the metre synonym set as it existed in dto.planLengthUnits before Ф5а.3 —
// the ONLY unit synonyms the codebase ever had. The vocabulary was required to absorb it verbatim
// rather than invent a fresh one, because it is what every legacy metre value was already matched
// against; this copy makes that promise a test instead of a claim in a comment.
var legacyPlanLengthUnits = []string{"m", "м", "meter", "meters", "metre", "metres"}

func TestMaterialUnitAbsorbsTheLegacyMetreSynonyms(t *testing.T) {
	for _, s := range legacyPlanLengthUnits {
		u, ok := NormalizeMaterialUnit(s)
		if !ok || u != MaterialUnitM {
			t.Errorf("legacy metre synonym %q resolved to (%q, %v), want (m, true)", s, u, ok)
		}
	}
	// And nothing else claims to be a metre: a widened metre set would start folding other units
	// into length, which is the failure mode the vocabulary exists to prevent.
	for raw, u := range MaterialUnitSynonyms() {
		if u != MaterialUnitM {
			continue
		}
		found := false
		for _, s := range legacyPlanLengthUnits {
			if s == raw {
				found = true
			}
		}
		if !found {
			t.Errorf("%q was added to the metre synonyms; that set must stay the pre-existing one", raw)
		}
	}
}

func TestNormalizeMaterialUnit(t *testing.T) {
	for _, c := range []struct {
		in   string
		want MaterialUnit
		ok   bool
	}{
		{"m", MaterialUnitM, true},
		{"М", MaterialUnitM, true}, // upper-case Cyrillic
		{"  metres  ", MaterialUnitM, true},
		{"KG", MaterialUnitKg, true},
		{"кг", MaterialUnitKg, true},
		{"pc", MaterialUnitPcs, true},
		{"шт.", MaterialUnitPcs, true},
		{"cone", MaterialUnitCone, true},
		// Not guessed. These are exactly the values the migration leaves alone and the wire reports
		// as UNKNOWN — silence is the correct answer, because a wrong canonical unit makes two
		// different quantities addable.
		{"", "", false},
		{"погонный метр", "", false},
		{"yd", "", false},
		{"м2", "", false}, // Cyrillic м + 2: deliberately unmapped, not folded into m2
	} {
		got, ok := NormalizeMaterialUnit(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeMaterialUnit(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSameMaterialUnit(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		// The whole point: the silently-wrong compare the material plan used to make.
		{"м", "m", true},
		{"metres", "M", true},
		{"m", "kg", false},
		{"pcs", "pc", true},
		// Both unknown → the old raw compare, so nothing that worked stops working.
		{"yd", "yd", true},
		{"yd", "YD", true},
		{"yd", "погонный метр", false},
		// One known, one not: not the same unit. Guessing here is what must not happen.
		{"m", "погонный метр", false},
		// An empty side is never "same" — the caller decides what a missing unit means.
		{"", "m", false},
		{"m", "", false},
		{"", "", false},
	} {
		if got := SameMaterialUnit(c.a, c.b); got != c.want {
			t.Errorf("SameMaterialUnit(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCanonicalMaterialUnitPreservesTheUnmappable(t *testing.T) {
	// Cyrillic МЕТ + Latin RES lower-cases to "метres", which the vocabulary does not know — so it is
	// NOT a metre and must come back trimmed but otherwise byte-for-byte as typed. Asserted as one
	// equality on purpose: the earlier nested-if form accepted "metres" (the value silently rewritten
	// into a unit it is not) just as happily as the preservation it claims to test.
	if got := CanonicalMaterialUnit(" МЕТRES "); got != "МЕТRES" {
		t.Errorf("CanonicalMaterialUnit(\" МЕТRES \") = %q, want \"МЕТRES\" — a mixed-script value is "+
			"unmappable and must be preserved, never folded into the metre it merely looks like", got)
	}
	if got := CanonicalMaterialUnit("  м "); got != "m" {
		t.Errorf("CanonicalMaterialUnit(\"  м \") = %q, want \"m\"", got)
	}
	if got := CanonicalMaterialUnit(" погонный метр "); got != "погонный метр" {
		t.Errorf("unmappable unit was not preserved: %q", got)
	}
}

func TestResolveBomLineUnit(t *testing.T) {
	for _, tc := range []struct {
		name                string
		section             TechCardBomSection
		slot, article, want string
	}{
		{"thread metre slot keeps metres", BomSectionThread, "m", "cone", "m"},
		{"thread metre synonym is preserved", BomSectionThread, "м", "roll", "м"},
		{"thread cone slot takes article", BomSectionThread, "cone", "roll", "roll"},
		{"fabric keeps article first", BomSectionFabric, "m", "kg", "kg"},
		{"empty article falls back", BomSectionFabric, "m", "", "m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveBomLineUnit(tc.section, tc.slot, tc.article); got != tc.want {
				t.Fatalf("ResolveBomLineUnit(%q, %q, %q) = %q, want %q", tc.section, tc.slot, tc.article, got, tc.want)
			}
		})
	}
}

func threadUnitArticle(unit, length string) MaterialWithPrice {
	m := MaterialWithPrice{Material: Material{MaterialInsert: MaterialInsert{
		Unit: sql.NullString{String: unit, Valid: unit != ""},
	}}}
	if length != "" {
		m.ThreadAttr = &MaterialThreadAttr{LengthPerConeM: decimal.NewNullDecimal(decimal.RequireFromString(length))}
	}
	return m
}

func TestMetresPerArticleUnit(t *testing.T) {
	for _, tc := range []struct {
		name, unit, length, want string
		ok                       bool
	}{
		{"metres", "m", "", "1", true},
		{"centimetres", "cm", "", "0.01", true},
		{"millimetres", "mm", "", "0.001", true},
		{"cone", "cone", "5000", "5000", true},
		{"pieces with length", "pcs", "3000", "3000", true},
		{"free text package", "spool", "2500", "2500", true},
		{"pieces without length", "pcs", "", "", false},
		{"kilograms reject stale length", "kg", "5000", "", false},
		{"area rejects stale length", "m²", "5000", "", false},
		{"volume rejects stale length", "litres", "5000", "", false},
		{"empty unit", "", "5000", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := MetresPerArticleUnit(threadUnitArticle(tc.unit, tc.length))
			if ok != tc.ok || (ok && got.String() != tc.want) {
				t.Fatalf("MetresPerArticleUnit(%q, %q) = (%s, %v), want (%s, %v)", tc.unit, tc.length, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestThreadArticleUsesMetreSlotWithoutPackageLength(t *testing.T) {
	if !ThreadArticleUsesMetreSlot(threadUnitArticle("cone", "")) {
		t.Fatal("a cone article must create a metre slot even before its length is filled")
	}
	if ThreadArticleUsesMetreSlot(threadUnitArticle("kg", "5000")) {
		t.Fatal("a mass article must never create a metre thread slot")
	}
}

func TestPricePerMetreRoundsHalfUpToBomScale(t *testing.T) {
	for _, tc := range []struct{ price, factor, want string }{
		{"5", "5000", "0.001"},
		{"4.20", "5000", "0.0008"},
		{"2.99", "5000", "0.0006"},
		{"0.25", "5000", "0.0001"},
		{"5", "3000", "0.0017"},
		{"1.23", "10000", "0.0001"},
	} {
		got := PricePerMetre(decimal.RequireFromString(tc.price), decimal.RequireFromString(tc.factor))
		if got.String() != tc.want {
			t.Errorf("PricePerMetre(%s, %s) = %s, want %s", tc.price, tc.factor, got, tc.want)
		}
	}
}
