package campaignrender

import (
	"fmt"
	"testing"
)

func contrastRatio(a, b float64) float64 {
	hi, lo := a, b
	if lo > hi {
		hi, lo = lo, hi
	}
	return (hi + 0.05) / (lo + 0.05)
}

// TestNewPaletteAlwaysPicksMoreReadableText sweeps the full grey range and asserts
// newPalette never leaves contrast on the table: the chosen palette's Text must have
// at least the contrast the alternative palette's Text would have on that background.
// This guards the threshold regression where mid-tones (e.g. #999) were handed light
// text at ~2.5:1 when dark text gives ~6.9:1.
func TestNewPaletteAlwaysPicksMoreReadableText(t *testing.T) {
	lightText, _ := relativeLuminance(lightPalette.Text)
	darkText, _ := relativeLuminance(darkPalette.Text)

	for v := 0; v <= 255; v += 3 {
		bg := fmt.Sprintf("#%02x%02x%02x", v, v, v)
		bgLum, ok := relativeLuminance(bg)
		if !ok {
			t.Fatalf("could not parse %s", bg)
		}
		pal := newPalette(bg)
		chosen := lightText
		if pal.Text == darkPalette.Text {
			chosen = darkText
		}
		other := lightText + darkText - chosen

		got := contrastRatio(bgLum, chosen)
		alt := contrastRatio(bgLum, other)
		if got+1e-9 < alt {
			t.Fatalf("bg %s (lum %.3f): chose Text %s at %.2f:1 but the other palette gives %.2f:1",
				bg, bgLum, pal.Text, got, alt)
		}
	}
}

func TestNewPaletteMidTonesGetDarkText(t *testing.T) {
	// Regression anchors: these mid/high tones must resolve to the light palette (dark text).
	for _, bg := range []string{"#ffffff", "#999999", "#808080", "#aaaaaa"} {
		if got := newPalette(bg); got.Text != lightPalette.Text {
			t.Fatalf("bg %s got Text %s, want light palette dark text %s", bg, got.Text, lightPalette.Text)
		}
	}
	// Genuinely dark backgrounds still get the light palette.
	for _, bg := range []string{"#000000", "#222222", "#1a1a1a"} {
		if got := newPalette(bg); got.Text != darkPalette.Text {
			t.Fatalf("bg %s got Text %s, want dark palette light text %s", bg, got.Text, darkPalette.Text)
		}
	}
}
