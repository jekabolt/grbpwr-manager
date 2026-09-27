package techcardarchive

import (
	"strings"
	"testing"
)

// T45, format 1.1: a colourway's report ref and its token, which the commit and the press must
// build identically (FORMAT.md §5.3).

// MUTATION: separate family and token with a space — `color_code=BLK` becomes a row-prefix of the
// other colour's ref and a press supersedes the wrong colour's lines.
func TestColorwayRef(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    ColorwayPayload
		want string
	}{
		{"a 1.0 archive", ColorwayPayload{ColorCode: "BLK"}, "color_code=BLK"},
		{"a token that is its family", ColorwayPayload{ColorCode: "BLK", SkuColorToken: "BLK"}, "color_code=BLK"},
		{"the same, spelled differently", ColorwayPayload{ColorCode: "blk", SkuColorToken: "BLK"}, "color_code=blk"},
		{"a token of its own", ColorwayPayload{ColorCode: "BLK", SkuColorToken: "BKW"}, "color_code=BLK,sku_color_token=BKW"},
		{"verbatim, never normalised", ColorwayPayload{ColorCode: "BLK", SkuColorToken: "bkw"}, "color_code=BLK,sku_color_token=bkw"},
		{"a blank token is no token", ColorwayPayload{ColorCode: "BLK", SkuColorToken: "  "}, "color_code=BLK"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ColorwayRef(tc.p); got != tc.want {
				t.Fatalf("ColorwayRef = %q, want %q", got, tc.want)
			}
		})
	}
	// Two colourways of one family are two refs, and neither is a row-prefix («ref + space») of
	// the other — the property the press's supersedes/priorVerdict stand on.
	a := ColorwayRef(ColorwayPayload{ColorCode: "BLK"})
	b := ColorwayRef(ColorwayPayload{ColorCode: "BLK", SkuColorToken: "BKW"})
	if a == b || strings.HasPrefix(b, a+" ") || strings.HasPrefix(a, b+" ") {
		t.Fatalf("%q and %q collide", a, b)
	}
}

// MUTATION: return SkuColorToken unconditionally — a 1.0 payload restores an empty token.
func TestColorwayPayloadToken(t *testing.T) {
	if got := (ColorwayPayload{ColorCode: "BLK"}).Token(); got != "BLK" {
		t.Fatalf("a 1.0 archive's color_code was the token: %q", got)
	}
	if got := (ColorwayPayload{ColorCode: "BLK", SkuColorToken: "BKW"}).Token(); got != "BKW" {
		t.Fatalf("a 1.1 archive states its token: %q", got)
	}
	if got := (ColorwayPayload{ColorCode: "BLK", SkuColorToken: " "}).Token(); got != "BLK" {
		t.Fatalf("a blank token falls back to the colour code: %q", got)
	}
}

// The version the export writes is the one the reader compares against, and 1.1 is a MINOR: the
// reader takes it, and it takes a 1.0 archive too.
func TestFormatVersionIsOneOne(t *testing.T) {
	major, minor, err := ParseFormatVersion(FormatVersion)
	if err != nil || major != FormatMajor || minor != FormatMinor {
		t.Fatalf("FormatVersion %q = %d.%d (%v) disagrees with %d.%d", FormatVersion, major, minor, err, FormatMajor, FormatMinor)
	}
	if FormatMinor != 1 {
		t.Fatalf("T45 made the format 1.1 (the colourway identity); FormatMinor is %d", FormatMinor)
	}
	for _, v := range []string{"1.0", "1.1"} {
		m := &Manifest{Format: FormatName, FormatVersion: v, MoneyPolicy: MoneyPolicyStrippedV1}
		if err := checkManifestContract(m); err != nil {
			t.Fatalf("a %s archive must be read: %v", v, err)
		}
	}
}
