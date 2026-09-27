package entity

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// T45 (27.09) — the pure half of «a colourway is a free name + an ordered palette + an immutable
// SKU token»: palette validation, the per-language name write, the nearest dictionary family, the
// token minting rule and the «apply to slots» request. The store half runs only against MySQL and
// is covered by the CI probe (internal/store/colourway_t45_integration_test.go).

func wantViolation(t *testing.T, ve *ValidationError, field, reason string) {
	t.Helper()
	if ve == nil {
		t.Fatalf("want a violation on %s (%s), got none", field, reason)
	}
	if ve.Field != field || ve.Reason != reason {
		t.Fatalf("violation = %s (%s), want %s (%s): %s", ve.Field, ve.Reason, field, reason, ve.Message)
	}
}

// ─── palette (owner's decisions 5 and 6) ───

func TestNormalizeColorwayPaletteCanonicalises(t *testing.T) {
	got, ve := NormalizeColorwayPalette([]ColorwayColour{
		{Label: "  bone white ", Hex: "#efe9dc", Pantone: " 11-0602 ", PantoneSystem: " tcx "},
		{Pantone: "Black 6", PantoneSystem: "c"},
		{Label: "undyed"},
	}, "development.colours")
	if ve != nil {
		t.Fatalf("unexpected violation: %v", ve)
	}
	want := []ColorwayColour{
		{Label: "bone white", Hex: "#EFE9DC", Pantone: "11-0602", PantoneSystem: "TCX"},
		{Pantone: "Black 6", PantoneSystem: "C"},
		{Label: "undyed"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("normalised = %+v, want %+v", got, want)
	}
}

// Every rule refuses with the element's own path, so the client can bind the error to the chip.
// MUTATIONS: accept a colour with neither label nor Pantone; accept a book without a code; accept
// «black» as a hex; count bytes instead of characters; accept 9 colours or none.
func TestNormalizeColorwayPaletteRefuses(t *testing.T) {
	nine := make([]ColorwayColour, ColorwayPaletteMaxColours+1)
	for i := range nine {
		nine[i] = ColorwayColour{Label: fmt.Sprintf("c%d", i)}
	}
	cases := []struct {
		name    string
		in      []ColorwayColour
		field   string
		reason  string
		comment string
	}{
		{"empty palette", nil, "development.colours", "palette_size", "a palette holds at least one colour"},
		{"nine colours", nine, "development.colours", "palette_size", "owner's decision 5: 1…8"},
		{"neither label nor pantone", []ColorwayColour{{Label: "ok"}, {Hex: "#000000"}},
			"development.colours[1]", "empty_colour", "a colour is a Pantone code OR a label"},
		{"book without code", []ColorwayColour{{Label: "navy", PantoneSystem: "TCX"}},
			"development.colours[0].pantone_system", "system_without_pantone", ""},
		{"word as hex", []ColorwayColour{{Label: "black", Hex: "black"}},
			"development.colours[0].hex", "invalid_hex", ""},
		{"short hex", []ColorwayColour{{Label: "black", Hex: "#000"}},
			"development.colours[0].hex", "invalid_hex", "#RGB is not expanded"},
		{"label too long", []ColorwayColour{{Label: strings.Repeat("я", ColorwayColourLabelMaxRunes+1)}},
			"development.colours[0].label", "too_long", ""},
		{"pantone too long", []ColorwayColour{{Pantone: strings.Repeat("9", ColorwayColourPantoneMaxRunes+1)}},
			"development.colours[0].pantone", "too_long", ""},
		{"book too long", []ColorwayColour{{Pantone: "19-4052", PantoneSystem: "TEXTILEXX"}},
			"development.colours[0].pantone_system", "too_long", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ve := NormalizeColorwayPalette(tc.in, "development.colours")
			wantViolation(t, ve, tc.field, tc.reason)
		})
	}
	// Characters, not bytes: 64 Cyrillic letters are 128 bytes and still one legal label.
	if _, ve := NormalizeColorwayPalette([]ColorwayColour{{Label: strings.Repeat("я", ColorwayColourLabelMaxRunes)}}, "p"); ve != nil {
		t.Fatalf("a 64-character Cyrillic label is legal (VARCHAR counts characters): %v", ve)
	}
}

func TestNormalizeColorwayNameI18n(t *testing.T) {
	got, ve := NormalizeColorwayNameI18n(map[int]string{1: "  Nuit  ", 2: ""}, "development.name_i18n")
	if ve != nil {
		t.Fatal(ve)
	}
	if got[1] != "Nuit" {
		t.Fatalf("trimmed name = %q", got[1])
	}
	if v, ok := got[2]; !ok || v != "" {
		t.Fatal(`"" must survive as «delete this language», not vanish`)
	}
	if got, ve := NormalizeColorwayNameI18n(nil, "f"); got != nil || ve != nil {
		t.Fatal("no names = no change")
	}

	_, ve = NormalizeColorwayNameI18n(map[int]string{0: "x"}, "development.name_i18n")
	wantViolation(t, ve, "development.name_i18n[0]", "invalid_language")
	_, ve = NormalizeColorwayNameI18n(map[int]string{3: strings.Repeat("я", ColorwayNameI18nMaxRunes+1)}, "development.name_i18n")
	wantViolation(t, ve, "development.name_i18n[3]", "too_long")
	many := map[int]string{}
	for i := 1; i <= ColorwayNameI18nMaxEntries+1; i++ {
		many[i] = "x"
	}
	_, ve = NormalizeColorwayNameI18n(many, "development.name_i18n")
	wantViolation(t, ve, "development.name_i18n", "too_many")

	// Two faults: the refusal names the LOWER language id every time (sorted walk, not map order).
	for i := 0; i < 20; i++ {
		_, ve = NormalizeColorwayNameI18n(map[int]string{-5: "x", -1: "y", 7: strings.Repeat("я", 200)}, "n")
		wantViolation(t, ve, "n[-5]", "invalid_language")
	}
}

// ─── nearest dictionary family (owner's decision 4) ───

// seedDictionary is the colour dictionary as migration 0130 seeds it on every base.
func seedDictionary() []Color {
	hex := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	rows := [][2]string{
		{"BLK", "#000000"}, {"WHT", "#FFFFFF"}, {"OFW", "#F5F5F0"}, {"GRY", "#808080"},
		{"NAV", "#1A2238"}, {"BLU", "#2A4B8D"}, {"RED", "#B00020"}, {"GRN", "#2E7D32"},
		{"BRN", "#5D4037"}, {"BEI", "#D8CAB0"}, {"PNK", "#E59BB0"}, {"YLW", "#F2C14E"},
		{"ORG", "#E08A3C"}, {"PRP", "#6A4C93"}, {"SLV", "#C0C0C0"}, {"GLD", "#C9A227"},
	}
	out := make([]Color, 0, len(rows)+1)
	for i, r := range rows {
		out = append(out, Color{ID: i + 1, Code: r[0], Name: strings.ToLower(r[0]), Hex: hex(r[1])})
	}
	return append(out, Color{ID: 99, Code: "UNK", Name: "unknown"}) // no hex — cannot be compared
}

// Real fabric colours (Pantone TCX approximations) against the REAL dictionary.
//
// MUTATION (the reason this measure exists): replace familyDistance with plain Euclidean OKLab and
// drop the neutral gate — every black below files under NAV, because the dictionary's BLK is
// #000000 (lightness 0) while a fabric black sits at 0.27–0.30, next to NAV's 0.26.
func TestNearestColourFamilyFilesRealFabricColours(t *testing.T) {
	cases := []struct{ what, hex, want string }{
		{"near-black", "#0B0B0B", "BLK"},
		{"19-4005 TCX Stretch Limo", "#2B2C30", "BLK"},
		{"19-3911 TCX Black Beauty", "#26262A", "BLK"},
		{"19-0303 TCX Jet Black", "#2D2C2F", "BLK"},
		{"Black 6 C", "#101820", "BLK"},
		{"navy", "#1F2A44", "NAV"},
		{"19-3920 TCX Peacoat", "#2B2E43", "NAV"},
		{"19-4024 TCX Dress Blues", "#2A3244", "NAV"},
		{"19-4052 TCX Classic Blue", "#0F4C81", "BLU"},
		{"charcoal", "#36454F", "GRY"},
		{"heather grey", "#9A9A9A", "GRY"},
		{"light grey", "#BCBEC0", "SLV"},
		{"11-0602 TCX Bone", "#EFE9DC", "OFW"},
		{"white", "#FDFDFD", "WHT"},
		{"sand", "#C8B592", "BEI"},
		{"chocolate", "#4E3629", "BRN"},
		{"18-0625 TCX olive", "#5B6236", "GRN"},
		{"forest", "#2F4538", "GRN"},
		{"mint", "#A8D5BA", "GRN"},
		{"19-1725 TCX burgundy", "#5E2129", "RED"},
		{"red", "#C8102E", "RED"},
		{"plum", "#5A3A55", "PRP"},
		{"lavender", "#B4A7D6", "PRP"},
		{"blush", "#E8C4C0", "PNK"},
		{"mustard", "#D1A03B", "GLD"},
		{"lemon", "#F5E050", "YLW"},
		{"orange", "#FF6A13", "ORG"},
	}
	for _, tc := range cases {
		got, ok := NearestColourFamily(tc.hex, seedDictionary())
		if !ok || got.Code != tc.want {
			t.Errorf("%s %s → %s (%v), want %s", tc.what, tc.hex, got.Code, ok, tc.want)
		}
	}
}

func TestNearestColourFamilyEdges(t *testing.T) {
	hex := func(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
	// Either case of hex.
	if got, _ := NearestColourFamily("#fdfdfd", seedDictionary()); got.Code != "WHT" {
		t.Fatalf("lower-case hex → %s", got.Code)
	}
	// An ARCHIVED colour is never proposed — not even for its own exact hex: a new colourway cannot
	// be filed under a family the dictionary retired.
	dict := seedDictionary()
	for i := range dict {
		if dict[i].Code == "GRN" {
			dict[i].ArchivedAt = sql.NullTime{Valid: true}
		}
	}
	if got, _ := NearestColourFamily("#2E7D32", dict); got.Code == "GRN" {
		t.Fatal("archived GRN proposed")
	}
	for _, bad := range []string{"", "black", "#FFF", "#GGGGGG"} {
		if _, ok := NearestColourFamily(bad, seedDictionary()); ok {
			t.Errorf("%q is not a hex colour and proposes nothing", bad)
		}
	}
	if _, ok := NearestColourFamily("#000000", []Color{{Code: "UNK"}}); ok {
		t.Fatal("a dictionary without any hex proposes nothing")
	}
	// A class with no anchor of its own falls back to every candidate rather than proposing nothing.
	if got, ok := NearestColourFamily("#111111", []Color{{Code: "NAV", Hex: hex("#1A2238")}}); !ok || got.Code != "NAV" {
		t.Fatalf("fallback → %s, %v", got.Code, ok)
	}
	// Ties go to the lower code, so the proposal is deterministic.
	same := hex("#123456")
	got, _ := NearestColourFamily("#123456", []Color{{Code: "ZZZ", Hex: same}, {Code: "AAA", Hex: same}})
	if got.Code != "AAA" {
		t.Fatalf("tie resolved to %s, want AAA", got.Code)
	}
}

// ─── the SKU colour token (owner's decisions 1 and 2) ───

func TestIsValidSkuColorToken(t *testing.T) {
	for _, ok := range []string{"BLK", "B1K", "C01", "999"} {
		if !IsValidSkuColorToken(ok) {
			t.Errorf("%q is a token", ok)
		}
	}
	for _, bad := range []string{"", "blk", "BL", "BLKK", "B-K", "БЛК", " BL"} {
		if IsValidSkuColorToken(bad) {
			t.Errorf("%q is not a token", bad)
		}
	}
}

func TestMintColorwaySkuTokenReadsTheName(t *testing.T) {
	dict := []string{"BLK", "WHT", "BON", "OLV", "GRN"}
	cases := []struct {
		name, family string
		taken        []string
		want, why    string
	}{
		{"Black", "BLK", nil, "BLK", "one word: first, second and last skeleton letters"},
		{"black and white", "BLK", nil, "BKW", "filler words carry no colour"},
		{"Off White", "WHT", nil, "OFW", "two words"},
		{"Navy / Sand / Bone", "NAV", nil, "NSB", "three words: initials"},
		{"Red", "RED", nil, "RED", "a short skeleton takes the word itself"},
		{"Чёрный", "BLK", nil, "CHY", "Cyrillic is transliterated before anything else"},
		{"Écru", "OFW", nil, "ECR", "diacritics are stripped"},
		{"Black", "BLK", []string{"BLK"}, "BLC", "a taken token moves to the next pair of letters"},
		{"Olive", "OLV", nil, "OLV", "the colourway's OWN family code is allowed"},
		{"Olive", "GRN", nil, "OLO", "another family's dictionary code is never minted"},
		{"Bone", "WHT", nil, "BNO", "BON is the bone family's code — not this colourway's"},
		{"", "BLK", nil, "C01", "no name reads nothing: the C-sequence"},
		{"", "BLK", []string{"C01", "C02"}, "C03", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.family, func(t *testing.T) {
			taken := map[string]bool{}
			for _, x := range tc.taken {
				taken[x] = true
			}
			got := MintColorwaySkuToken(tc.name, tc.family, dict, taken)
			if got != tc.want {
				t.Fatalf("MintColorwaySkuToken(%q, %s) = %s, want %s (%s)", tc.name, tc.family, got, tc.want, tc.why)
			}
			if !IsValidSkuColorToken(got) {
				t.Fatalf("minted %q is not a token", got)
			}
			// Deterministic: the same inputs give the same token.
			if again := MintColorwaySkuToken(tc.name, tc.family, dict, taken); again != got {
				t.Fatalf("not deterministic: %s then %s", got, again)
			}
		})
	}
}

// Whatever the style already holds, the answer is free, well-formed and never a foreign family's
// code. MUTATION: drop the `taken` check in usable() — the second mint of a name collides.
func TestMintColorwaySkuTokenNeverCollides(t *testing.T) {
	dict := []string{"BLK", "WHT", "NAV"}
	taken := map[string]bool{}
	for i := 0; i < 300; i++ {
		tok := MintColorwaySkuToken("Black", "BLK", dict, taken)
		if !IsValidSkuColorToken(tok) {
			t.Fatalf("round %d: %q is not a token", i, tok)
		}
		if taken[tok] {
			t.Fatalf("round %d: %s minted twice", i, tok)
		}
		if tok == "WHT" || tok == "NAV" {
			t.Fatalf("round %d: minted another family's code %s", i, tok)
		}
		taken[tok] = true
	}
	// Past the name's letters and the C-sequence: the exhaustive walk still answers.
	all := map[string]bool{}
	for n := 1; n <= 99; n++ {
		all[fmt.Sprintf("C%02d", n)] = true
	}
	if got := MintColorwaySkuToken("", "BLK", dict, all); got != "AAA" {
		t.Fatalf("exhaustive fallback = %s, want AAA", got)
	}
}

// ─── «apply to slots» (owner's decision 7) ───

func TestNormalizePaletteSlotAssignments(t *testing.T) {
	got, ve := NormalizePaletteSlotAssignments([]ColorwayPaletteSlotAssignment{
		{BomLineKey: " BOM-1 ", ColourPosition: 0}, {BomLineKey: "BOM-2", ColourPosition: 7},
	})
	if ve != nil {
		t.Fatal(ve)
	}
	if got[0].BomLineKey != "BOM-1" || got[1].ColourPosition != 7 {
		t.Fatalf("normalised = %+v", got)
	}

	_, ve = NormalizePaletteSlotAssignments(nil)
	wantViolation(t, ve, "assignments", "assignment_count")
	tooMany := make([]ColorwayPaletteSlotAssignment, ColorwayPaletteApplyMaxAssignments+1)
	for i := range tooMany {
		tooMany[i] = ColorwayPaletteSlotAssignment{BomLineKey: fmt.Sprintf("K%d", i)}
	}
	_, ve = NormalizePaletteSlotAssignments(tooMany)
	wantViolation(t, ve, "assignments", "assignment_count")
	_, ve = NormalizePaletteSlotAssignments([]ColorwayPaletteSlotAssignment{{BomLineKey: "A"}, {BomLineKey: "  "}})
	wantViolation(t, ve, "assignments[1].bom_line_key", "required")
	_, ve = NormalizePaletteSlotAssignments([]ColorwayPaletteSlotAssignment{{BomLineKey: "A"}, {BomLineKey: " A"}})
	wantViolation(t, ve, "assignments[1].bom_line_key", "duplicate_slot")
	_, ve = NormalizePaletteSlotAssignments([]ColorwayPaletteSlotAssignment{{BomLineKey: "A", ColourPosition: -1}})
	wantViolation(t, ve, "assignments[0].colour_position", "out_of_palette")
	_, ve = NormalizePaletteSlotAssignments([]ColorwayPaletteSlotAssignment{{BomLineKey: "A", ColourPosition: ColorwayPaletteMaxColours}})
	wantViolation(t, ve, "assignments[0].colour_position", "out_of_palette")
}

func TestSlotPantone(t *testing.T) {
	cases := []struct {
		in   ColorwayColour
		want string
	}{
		{ColorwayColour{Pantone: "19-4052", PantoneSystem: "TCX"}, "19-4052 TCX"},
		{ColorwayColour{Pantone: "19-4052 tcx", PantoneSystem: "TCX"}, "19-4052 tcx"}, // already names its book
		{ColorwayColour{Pantone: "Black 6 C", PantoneSystem: "C"}, "Black 6 C"},
		{ColorwayColour{Pantone: "19-4052"}, "19-4052"},
		{ColorwayColour{Label: "undyed"}, ""},
		{ColorwayColour{Pantone: strings.Repeat("9", ColorwayColourPantoneMaxRunes), PantoneSystem: "TCX"},
			strings.Repeat("9", ColorwayColourPantoneMaxRunes)}, // the joined form would not fit the column
	}
	for _, tc := range cases {
		if got := SlotPantone(tc.in); got != tc.want {
			t.Errorf("SlotPantone(%+v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestColorwayDevelopmentPatchPaletteHelpers(t *testing.T) {
	var nilPatch *ColorwayDevelopmentPatch
	if !nilPatch.IsEmpty() || nilPatch.HasScalars() || nilPatch.MainColour() != nil {
		t.Fatal("a nil patch is empty")
	}
	palette := &ColorwayDevelopmentPatch{Colours: []ColorwayColour{{Label: "a"}, {Label: "b"}}}
	if palette.IsEmpty() || palette.HasScalars() {
		t.Fatal("a palette-only patch is not empty and has no scalars")
	}
	if palette.MainColour().Label != "a" {
		t.Fatal("the main colour is the first")
	}
	names := &ColorwayDevelopmentPatch{NameI18n: map[int]string{1: ""}}
	if names.IsEmpty() {
		t.Fatal("a translation delete is a change")
	}
	name := "Nuit"
	if !(&ColorwayDevelopmentPatch{Name: &name}).HasScalars() {
		t.Fatal("the name is a scalar")
	}
}
