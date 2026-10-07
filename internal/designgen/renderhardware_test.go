package designgen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ─────────────────────────── HARDWARE ON A RENDER (ROUND 9 Ф1) ───────────────────────────

// Two painted cloths, the front map with its mockup, a FRONT BUTTON with its picture and a CUFF
// SNAP in words only (on the back, which carries no mockup).
const hardwareRenderParams = `{"views":["front","back"],"layout":"one",` +
	`"colour":{"code":"RED-01","hex":"#b1121a","fabric_media_id":9,` +
	`"colour_maps":[{"media_id":20,"view":"front","mockup_media_id":30}],` +
	`"fabrics":[` +
	`{"name":"main jersey","media_id":9,"map_hex":"#3a7bd5"},` +
	`{"name":"contrast rib","media_id":10,"map_hex":"#ff0000"},` +
	`{"asset_id":73,"name":"FRONT BUTTON","media_id":40,"words":"horn, black · 20L",` +
	`"parts":"left front body · 2 on the front","kind":"hardware"},` +
	`{"name":"CUFF SNAP","words":"brass snap","parts":"2 on the back","kind":"hardware"}]}}`

const hardwareGoldenParagraph = "HARDWARE. Image 7 is the hardware «FRONT BUTTON» (horn, black · 20L). " +
	"The mockup image 4 shows exactly where it sits and how big it is: left front body · 2 on the front. " +
	"Reproduce exactly this piece — shape, colour, holes, finish — at those places and that size, sewn on " +
	"with its own shadow; its white ground is not part of it. No other hardware of this kind anywhere else."

// TestHardwareIsSplitOffTheCloths — a hardware use is not a cloth: the list says TWO cloths, no
// «CLOTH 3», and its picture is captioned as the item, not as a fabric photograph.
func TestHardwareIsSplitOffTheCloths(t *testing.T) {
	got := renderPrompt(t, hardwareRenderParams, renderSlots)
	require.Contains(t, got, "This garment is made of two different cloths")
	require.NotContains(t, got, "CLOTH 3")
	require.NotContains(t, got, "fabric photograph — CLOTH 3")
	// plates 1, 2 · front map 3 · its mockup 4 · cloths 5, 6 · the button 7
	require.Contains(t, got, "- image 5: fabric photograph — CLOTH 1 — main jersey")
	require.Contains(t, got, "- image 6: fabric photograph — CLOTH 2 — contrast rib")
	require.Contains(t, got, "- image 7: hardware «FRONT BUTTON» — the button itself on a white ground")
	require.NotContains(t, got, "- image 8")

	list := referenceList("render", parseParams([]byte(hardwareRenderParams)), parseInputs([]byte(renderSlots)))
	require.Len(t, list, 7)
	require.True(t, list[6].IsHardware)
	require.Equal(t, 2, len(statedCloths(parseParams([]byte(hardwareRenderParams)).Colour)))
	require.Equal(t, 2, len(statedHardware(parseParams([]byte(hardwareRenderParams)).Colour)))
}

// TestHardwareParagraphPointsAtTheMockup — GOLDEN: the picture by number, the mockup of the view
// its parts name by number, where and how many, after the cloths and before the layout.
func TestHardwareParagraphPointsAtTheMockup(t *testing.T) {
	got := renderPrompt(t, hardwareRenderParams, renderSlots)
	require.Contains(t, got, "\n\n"+hardwareGoldenParagraph+"\n\n")
	require.Less(t, strings.Index(got, "The cloths of this garment."), strings.Index(got, hardwareGoldenParagraph))
	require.Less(t, strings.Index(got, hardwareGoldenParagraph), strings.Index(got, "Layout:"))
}

// TestWordsOnlyHardwareIsBuiltFromTheWords — no picture: the paragraph says so, and a hardware on
// a view with no mockup is not pointed at another view's mockup.
func TestWordsOnlyHardwareIsBuiltFromTheWords(t *testing.T) {
	got := renderPrompt(t, hardwareRenderParams, renderSlots)
	require.Contains(t, got, "HARDWARE. The hardware «CUFF SNAP» (brass snap): no picture is given — build it "+
		"from the words. It sits on the garment: 2 on the back. Make it as those words say — shape, colour, "+
		"holes, finish — at those places and that size, sewn on with its own shadow. No other hardware of "+
		"this kind anywhere else.")
	require.NotContains(t, got, "«CUFF SNAP» — the")
}

// TestHardwareIsNeverTheRemainder — a hardware use naming no parts and carrying no map label would
// have been the REMAINDER cloth before the split; now it is neither a cloth nor a remainder.
func TestHardwareIsNeverTheRemainder(t *testing.T) {
	p := strings.Replace(hardwareRenderParams, `"parts":"2 on the back",`, ``, 1)
	got := renderPrompt(t, p, renderSlots)
	require.NotContains(t, got, "It names no parts, so it is the REMAINDER")
	require.NotContains(t, got, "Parts left white on a map are made of the REMAINDER cloth")
	require.Contains(t, got, "This garment is made of two different cloths")
}

// TestOneClothWithHardwareKeepsTheFrozenClothWording — one cloth + a button: the single-cloth
// caption and paragraph are the frozen ones; the button has its own caption and paragraph.
func TestOneClothWithHardwareKeepsTheFrozenClothWording(t *testing.T) {
	one := `{"views":["front","back"],"layout":"one",` +
		`"colour":{"code":"RED-01","hex":"#b1121a","fabric_media_id":9,` +
		`"colour_maps":[{"media_id":20,"view":"front","mockup_media_id":30}],` +
		`"fabrics":[{"name":"main jersey","media_id":9},` +
		`{"name":"FRONT BUTTON","media_id":40,"parts":"2 on the front","kind":"hardware"}]}}`
	got := renderPrompt(t, one, renderSlots)
	require.Contains(t, got, "fabric photograph — the material this garment is made of")
	require.NotContains(t, got, "different cloths")
	require.Contains(t, got, "hardware «FRONT BUTTON» — the button itself on a white ground")
	require.Contains(t, got, "HARDWARE. Image 6 is the hardware «FRONT BUTTON». The mockup image 4 shows "+
		"exactly where it sits and how big it is: 2 on the front.")
}

// TestARenderWithoutHardwareSaysNothingOfIt — no hardware use: no paragraph, no caption.
func TestARenderWithoutHardwareSaysNothingOfIt(t *testing.T) {
	got := renderPrompt(t, twoClothsMaps(`,"mockup_media_id":30`, ""), renderSlots)
	require.NotContains(t, got, "HARDWARE")
	require.NotContains(t, got, "hardware «")
	require.Empty(t, renderHardwareParagraphs(nil, nil, nil))
}

// TestHardwareNounFollowsTheWords — the caption calls the item by what it is.
func TestHardwareNounFollowsTheWords(t *testing.T) {
	for _, c := range []struct{ name, words, want string }{
		{"FRONT BUTTON", "", "button"},
		{"fly", "metal zipper, 5 mm", "zip"},
		{"cuff", "snap button, brass", "snap"},
		{"pocket", "copper rivet", "rivet"},
		{"belt", "", "piece"},
	} {
		require.Equal(t, c.want, hardwareNoun(fabricUse{Name: c.name, Words: c.words}), c.name)
	}
}
