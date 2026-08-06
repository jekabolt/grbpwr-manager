package entity

import "testing"

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
	if got := CanonicalMaterialUnit(" МЕТRES "); got != "metres" {
		// mixed-script input is NOT a metre; it must survive untouched (trimmed + as typed)
		if got != "МЕТRES" {
			t.Errorf("mixed-script unit was rewritten to %q; an unmappable value must be preserved", got)
		}
	}
	if got := CanonicalMaterialUnit("  м "); got != "m" {
		t.Errorf("CanonicalMaterialUnit(\"  м \") = %q, want \"m\"", got)
	}
	if got := CanonicalMaterialUnit(" погонный метр "); got != "погонный метр" {
		t.Errorf("unmappable unit was not preserved: %q", got)
	}
}
