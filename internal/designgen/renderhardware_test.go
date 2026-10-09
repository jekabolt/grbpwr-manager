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

// TestHardwareOnAThreeQuarterViewCitesThatMockup — R9 fix 7: a button only on the three-quarter
// view cites that view's mockup and no other, in the prompt's view words and in the client's bare
// key spelling alike; a parts text naming a view word only as a prefix names nothing.
func TestHardwareOnAThreeQuarterViewCitesThatMockup(t *testing.T) {
	params := func(parts string) string {
		return `{"views":["front","back"],"layout":"one",` +
			`"colour":{"code":"RED-01","hex":"#b1121a","fabric_media_id":9,` +
			`"colour_maps":[{"media_id":20,"view":"front","mockup_media_id":30},` +
			`{"media_id":21,"view":"three_quarter_l","mockup_media_id":31}],` +
			`"fabrics":[{"name":"main jersey","media_id":9,"map_hex":"#3a7bd5"},` +
			`{"name":"contrast rib","media_id":10,"map_hex":"#ff0000"},` +
			`{"name":"CUFF BUTTON","media_id":40,"parts":"` + parts + `","kind":"hardware"}]}}`
	}
	// plates 1, 2 · front map 3 · its mockup 4 · the three-quarter map 5 · its mockup 6
	for _, parts := range []string{"cuff · 1 on the three-quarter from the left", "cuff · 1 on the three quarter l"} {
		got := renderPrompt(t, params(parts), renderSlots)
		require.Contains(t, got, "The mockup image 6 shows exactly where it sits and how big it is: "+parts+".", parts)
		require.NotContains(t, got, "The mockup images 4 and 6", parts)
	}
	require.True(t, partsNameView("2 on the front", "front"))
	require.False(t, partsNameView("2 on the frontal yoke", "front"))
	require.False(t, partsNameView("1 on the three quarter left", "three_quarter_l"))
	require.True(t, partsNameView("1 on the three quarter r, 2 on the back", "three_quarter_r"))
}

// hardwareOnlyParams — only a button painted: its map labels nothing (no cloth carries a map_hex).
func hardwareOnlyParams(fabrics string) string {
	return `{"views":["front","back"],"layout":"one",` +
		`"colour":{"words":"black wool flannel",` +
		`"colour_maps":[{"media_id":20,"view":"front","mockup_media_id":30}],` +
		`"fabrics":[` + fabrics + `{"name":"FRONT BUTTON","media_id":40,"parts":"2 on the front","kind":"hardware"}]}}`
}

// TestHardwareWithNoClothIsAPlacementRun — R9 fix 2/3: no cloth bound (the cloth in words), a
// painted button: the map is captioned as labelling nothing, the mockup as a PLACEMENT mockup, and
// the prompt never sends the model to it for a cloth or a motif scale.
func TestHardwareWithNoClothIsAPlacementRun(t *testing.T) {
	p := hardwareOnlyParams("")
	got := renderPrompt(t, p, renderSlots)
	require.Contains(t, got, "- image 3: unlabelled map of the front flat — nothing on it is labelled")
	require.Contains(t, got, "- image 4: placement mockup of the front flat — the drawing with each painted piece of hardware")
	require.Contains(t, got, "Image 4 is a placement mockup of the same drawing: take each piece of hardware's place and size from it")
	require.NotContains(t, got, "cloth mockup")
	require.NotContains(t, got, "motif")
	require.Contains(t, got, "black wool flannel")
	require.Contains(t, got, "HARDWARE. Image 5 is the hardware «FRONT BUTTON». The mockup image 4 shows exactly where it sits")
	list := referenceList("render", parseParams([]byte(p)), parseInputs([]byte(renderSlots)))
	require.True(t, list[3].IsMockup && list[3].IsPlacement)
}

// TestHardwareOnlyOverTwoClothsSpeaksNoLabels — two unpainted cloths and a painted button: no
// «colour map … LABELS» sentence (nothing is labelled), the mockup named as placement.
func TestHardwareOnlyOverTwoClothsSpeaksNoLabels(t *testing.T) {
	got := renderPrompt(t, hardwareOnlyParams(
		`{"name":"main jersey","media_id":9,"parts":"body"},{"name":"satin","media_id":10,"parts":"lapel"},`), renderSlots)
	require.Contains(t, got, "This garment is made of two different cloths")
	require.NotContains(t, got, "is a colour map of the")
	require.NotContains(t, got, "LABELS that say which cloth")
	require.Contains(t, got, "Image 4 is a placement mockup of the same drawing")
	require.NotContains(t, got, "cloth mockup")
}

// TestHardwareOnlyOverOneClothKeepsTheClothMockup — one cloth is the whole garment: the client
// skins the mockup with it, so it stays a cloth mockup; only the map is unlabelled.
func TestHardwareOnlyOverOneClothKeepsTheClothMockup(t *testing.T) {
	got := renderPrompt(t, hardwareOnlyParams(`{"name":"main jersey","media_id":9},`), renderSlots)
	require.Contains(t, got, "- image 3: unlabelled map of the front flat")
	require.Contains(t, got, "- image 4: cloth mockup of the front flat")
	require.NotContains(t, got, "placement mockup")
}
