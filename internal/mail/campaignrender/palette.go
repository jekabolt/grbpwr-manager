package campaignrender

import (
	"math"
	"strconv"
	"strings"
)

// palette is the set of foreground (text / rule / hairline) tokens chosen to
// stay legible on the campaign's configured background. The email background is
// admin-configurable to any color (see Input.BackgroundColor / safeColor), so a
// fixed dark-on-light token set would be invisible on a dark background. All
// foreground colors in the templates are driven by this struct; only true
// background fills continue to follow the background directly.
type palette struct {
	Text     string // primary body text, headings, links, product name
	Muted    string // captions, struck-through prices, secondary footer text
	Subhead  string // header subheading (one notch stronger than Muted)
	Faint    string // fine print: unsubscribe + legal lines
	Hairline string // hairline rules / borders
	Rule     string // dividers and CTA border/fill (the "ink")
	OnRule   string // label sitting on a Rule-filled (solid) CTA/badge
}

// lightPalette preserves the historical light-background appearance exactly.
var lightPalette = palette{
	Text:     "#0E0E0C",
	Muted:    "#9a978c",
	Subhead:  "#5d5a51",
	Faint:    "#b3aea2",
	Hairline: "#DBD8CE",
	Rule:     "#0E0E0C",
	OnRule:   "#ffffff",
}

// darkPalette is the light-on-dark counterpart. Contrast on pure black:
// Text #f4f2ec 18.8:1, Rule #f4f2ec 18.8:1, Subhead #cfccc3 13.1:1,
// Muted #b3aea2 9.5:1 (all AAA), Faint #8b887f 5.9:1 (AA fine print, and
// higher than the light Faint's 2.2:1). Hairline is a decorative rule.
var darkPalette = palette{
	Text:     "#f4f2ec",
	Muted:    "#b3aea2",
	Subhead:  "#cfccc3",
	Faint:    "#8b887f",
	Hairline: "#3a3a38",
	Rule:     "#f4f2ec",
	OnRule:   "#0E0E0C",
}

// darkBackgroundLuminance is the WCAG relative-luminance threshold below which a
// background is treated as dark (→ light text). It is the crossover point where the
// two palettes' Text colors give equal contrast: solving
// (L+0.05)/(Lᵈᵃʳᵏ+0.05) = (Lˡⁱᵍʰᵗ+0.05)/(L+0.05) for the light Text #0E0E0C
// (lum ≈ 0.0033) and dark Text #f4f2ec (lum ≈ 0.887) yields L ≈ 0.173. Above it, dark
// text is the more legible choice — so a mid grey like #999 (lum ≈ 0.318) correctly
// gets dark text (~6.9:1) instead of light text (~2.5:1). A higher cutoff (the old 0.4)
// pushed that whole mid-tone band to unreadable light text. (palette_test.go sweeps the
// grey range to prove newPalette always picks the higher-contrast text at this value.)
const darkBackgroundLuminance = 0.176

// newPalette selects the foreground token set for a resolved background color.
// bg is expected to be a #rgb or #rrggbb string (safeColor guarantees this);
// anything unparseable falls back to the light palette (the default background
// is white).
func newPalette(bg string) palette {
	if l, ok := relativeLuminance(bg); ok && l < darkBackgroundLuminance {
		return darkPalette
	}
	return lightPalette
}

// relativeLuminance returns the WCAG 2.x relative luminance (0..1) of a hex
// color and whether it could be parsed.
func relativeLuminance(hex string) (float64, bool) {
	r, g, b, ok := parseHexColor(hex)
	if !ok {
		return 0, false
	}
	return 0.2126*linearizeChannel(r) + 0.7152*linearizeChannel(g) + 0.0722*linearizeChannel(b), true
}

// linearizeChannel converts an 8-bit sRGB channel to linear light per WCAG.
func linearizeChannel(c uint8) float64 {
	s := float64(c) / 255.0
	if s <= 0.03928 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}

// parseHexColor accepts "#rgb" and "#rrggbb" (case-insensitive, "#" optional).
func parseHexColor(hex string) (r, g, b uint8, ok bool) {
	hex = strings.TrimPrefix(strings.TrimSpace(hex), "#")
	switch len(hex) {
	case 3:
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	case 6:
	default:
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return uint8(v >> 16), uint8(v >> 8), uint8(v), true
}
