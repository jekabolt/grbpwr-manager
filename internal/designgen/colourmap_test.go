package designgen

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ─────────────────────────── THE COLOUR MAP (Feature A) ───────────────────────────
//
// A colour map is a picture ABOUT the garment rather than a picture OF it: the flat, flooded part
// by part in colours that LABEL which cloth goes where. Every probe in this file exists because the
// same picture, read under the wrong caption, is an instruction to render a steel blue garment.
//
// ⚠ THE SINGLE-CLOTH GOLDENS IN renderfabric_test.go ARE THE OTHER HALF OF THIS FILE and they are
// where the safety of the whole feature actually lives: a run that carries no map must compose the
// prompt it composed yesterday, byte for byte. Those six literals were captured before any of this
// existed; nothing here may be read as covering them.

// twoClothsOneMap: the front flat is painted, the body is the main jersey (steel blue on the map)
// and the collar is the contrast rib (red). Both cloths carry a texture, so the image numbers are
// worth reading: two plates, then the map, then the two swatches.
const twoClothsOneMap = `{"views":["front","back"],"layout":"one",` +
	`"colour":{"code":"RED-01","hex":"#b1121a","words":"WORDS-brushed","fabric_media_id":9,` +
	`"colour_maps":[{"media_id":20,"view":"front"}],` +
	`"fabrics":[` +
	`{"asset_id":4,"name":"main jersey","media_id":9,"colour_code":"RED-01","colour_hex":"#b1121a","words":"WORDS-brushed","map_hex":"#3a7bd5"},` +
	`{"asset_id":5,"name":"contrast rib","media_id":10,"colour_code":"NVY-02","colour_hex":"#1b2a4a","parts":"collar, cuffs","map_hex":"#ff0000"}]}}`

// TestRenderPromptTwoClothsOneMap — the whole of Feature A's backend half, as one prompt.
//
// It asserts the three things the map has to buy, and each of them is a thing the model does
// WRONG when the sentence is missing:
//
//   - the map is captioned as a map, not as «additional reference image» (else: a steel blue
//     garment);
//   - the cloth list names the map by its IMAGE NUMBER and says the colours are labels (else: the
//     model has a picture it cannot place);
//   - each cloth is pinned to its painted colour BY NAME AND BY HEX (else: two cloths and no
//     boundary, which is the invented-contrast-collar failure the list already exists to stop).
func TestRenderPromptTwoClothsOneMap(t *testing.T) {
	got := renderPrompt(t, twoClothsOneMap, renderSlots)

	// THE CAPTION. Image 3 is the map — after the two plates, before the two swatches — and it says
	// what it is in the same breath as what its colours are NOT.
	require.Contains(t, got, "- image 3: colour map of the front flat — the same drawing with each "+
		"part flooded in one flat colour; those colours LABEL which cloth covers which part and are "+
		"not the garment's own colours, which the cloth list states",
		"a colour map read under the extra-input caption is a drawing of the garment in improbable colours")

	// THE HEADING. The number comes off the ATTACHED list, so it is the same picture the caption
	// block numbered — by construction, not by coincidence.
	require.Contains(t, got, "Image 3 is a colour map of the front drawing — the same drawing with "+
		"each part flooded in one flat colour. Those flat colours are LABELS that say which cloth "+
		"covers which part; they are not the garment's colours, which the list below states.")

	// THE UNPAINTED VIEW IS NAMED. Without this sentence a model handed a front map and an unmapped
	// back either carries the division over or treats the back as a second garment.
	require.Contains(t, got, "The back drawing carries no colour map: on that view, divide the "+
		"cloths as the mapped views imply.")

	// THE MARKED RULE, not the «nobody marked anything, the division is yours» one. A painted run
	// falling under the unmarked rule would be told to ignore the very maps it was handed.
	require.Contains(t, got, renderClothPartsRule)
	require.NotContains(t, got, renderClothPartsUnmarked)

	first, second := clothLine(t, got, "1"), clothLine(t, got, "2")
	// CLOTH 1 has a label and no words for its parts: the picture is the whole address.
	require.Contains(t, first, "It is used on the parts painted steel blue (#3a7bd5) on the colour "+
		"map — and on no other part of this garment.")
	// CLOTH 2 has both, and both are printed: the words are what a person recognises, the colour is
	// what the picture shows.
	require.Contains(t, second, "It is used on: collar, cuffs — the parts painted red (#ff0000) on "+
		"the colour map — and on no other part of this garment.")

	// The swatches still point at their own pictures, numbered AFTER the map.
	require.Contains(t, first, "Its texture is image 4")
	require.Contains(t, second, "Its texture is image 5")
}

// TestAClothPlacedONLYByPaintIsStillACloth — a row whose only content is a map label.
//
// ⚠ MUTATION THIS KILLS: leaving `map_hex` out of statedCloths. Such a row states nothing by the
// old reading, so a two-cloth painted submission would collapse to ONE cloth — the prompt would
// take the frozen single-cloth path, the second cloth would never be named, and the run would come
// back in one fabric with nothing in the history to explain it.
func TestAClothPlacedONLYByPaintIsStillACloth(t *testing.T) {
	params := `{"views":["front"],"layout":"one","colour":{"code":"RED-01","hex":"#b1121a",` +
		`"colour_maps":[{"media_id":20,"view":"front"}],` +
		`"fabrics":[{"name":"main jersey","map_hex":"#3a7bd5"},{"map_hex":"#ff0000"}]}}`
	got := renderPrompt(t, params, renderSlots)

	require.Contains(t, got, "This garment is made of two different cloths",
		"a row placed by paint alone is a cloth somebody deliberately put on the garment")
	require.Contains(t, clothLine(t, got, "2"),
		"It is used on the parts painted red (#ff0000) on the colour map")
}

// TestAColourMapWhoseMediaVanishedIsNeitherNumberedNorMentioned — the rule cloth pictures already
// keep: `attached` is the survivors of media resolution, and a sentence pointing at an image nobody
// was shown is an instruction about nothing.
//
// The fixture reaches that state the way the code does: the map's media id is not among the
// pictures the reference list actually built (it is simply absent here, standing in for a row that
// went away between the snapshot and the pass).
func TestAColourMapWhoseMediaVanishedIsNeitherNumberedNorMentioned(t *testing.T) {
	attached := []refCaption{{MediaID: 1, Caption: "front"}, {MediaID: 2, Caption: "back"}}
	got := renderColourMapSentence([]colourMap{{MediaID: 20, View: "front"}}, []string{"front"}, attached)
	require.Equal(t, "", got,
		"a map that did not go out must not be described — the model cannot see it")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: та же карта, доехавшая, говорит.
	attached = append(attached, refCaption{MediaID: 20, Caption: "map"})
	require.Contains(t, renderColourMapSentence([]colourMap{{MediaID: 20, View: "front"}}, []string{"front"}, attached),
		"Image 3 is a colour map of the front drawing")
}

// TestOneClothWithAColourMapKeepsTheFrozenParagraph — the load-bearing line of this wave, read from
// the single-cloth side.
//
// One cloth IS the whole garment, so there is no assignment for a map to make. The fabric paragraph
// must therefore be the frozen one word for word; only the image number the swatch happens to have
// may move, because the map really is a picture that went out.
func TestOneClothWithAColourMapKeepsTheFrozenParagraph(t *testing.T) {
	params := `{"views":["front","side_l","back"],"layout":"one","colour":{"code":"RED-01",` +
		`"hex":"#b1121a","words":"WORDS-brushed, slight sheen","fabric_media_id":9,` +
		`"colour_maps":[{"media_id":20,"view":"front"}],` +
		`"fabrics":[{"asset_id":4,"name":"main jersey","media_id":9,"colour_code":"RED-01",` +
		`"colour_hex":"#b1121a","words":"WORDS-brushed, slight sheen","map_hex":"#3a7bd5"}]}}`
	got := renderPrompt(t, params, renderSlots)

	var authority string
	for _, p := range strings.Split(got, "\n\n") {
		if strings.HasPrefix(p, "Fabric. ") {
			authority = p
		}
	}
	// ⚠ HELD AGAINST THE FROZEN SINGLE-CLOTH GOLDEN ITSELF, not against a literal written here: a
	// second copy of that paragraph beside this probe would be a second thing to soften. The swatch
	// is image 4 in this fixture (two plates, then the map, then the swatch) and image 3 in the
	// golden, and that ONE substitution is the whole of the licensed difference.
	require.Contains(t, goldenAllThree, strings.Replace(authority, "(image 4)", "(image 3)", 1),
		"one cloth takes the frozen paragraph whether or not a map was painted")
	require.True(t, strings.HasPrefix(authority, "Fabric. "), "положительный контроль: абзац найден")

	// AND NOTHING IS SAID ABOUT LABELS. A sentence about which colour means which cloth, beside a
	// garment made of one cloth, is an instruction to divide something that is not divided.
	require.NotContains(t, got, "are LABELS that say which cloth covers which part")
	require.NotContains(t, got, "the parts painted")
	// The picture itself still travels, captioned for what it is.
	require.Contains(t, got, "- image 3: colour map of the front flat")
}

// TestSurfaceSteerIgnoresMapHex — the 3D hint reads WORDS and never a map label.
//
// ⚠ WHY THIS IS A RULE AND NOT AN OMISSION. `texture_prompt` reaches a texturing stage that is shown
// the render plates and NOTHING else (threed_inputs.go). «Painted steel blue on the colour map»
// addresses a picture that stage has never seen: it is a sentence about nothing, spent out of a
// 600-rune ceiling that already drops its own tail when it overflows.
func TestSurfaceSteerIgnoresMapHex(t *testing.T) {
	ctx := context.Background()
	p := runParams{Colour: &colourRecipe{
		Code: "BLK", Hex: "#0a0a0a", Words: "matte heavy jersey",
		ColourMaps: []colourMap{{MediaID: 20, View: "front"}},
		Fabrics: []fabricUse{
			{Name: "body cloth", ColourCode: "BLK", ColourHex: "#0a0a0a", Words: "matte heavy jersey", MapHex: "#3a7bd5"},
			{Name: "contrast rib", ColourCode: "RED", Words: "ribbed knit", Parts: "cuffs and collar", MapHex: "#ff0000"},
		},
	}}
	steer := surfaceSteer(ctx, p)

	require.NotContains(t, steer, "#3a7bd5", "a map label addresses a picture the texturing stage never sees")
	require.NotContains(t, steer, "#ff0000")
	require.NotContains(t, steer, "colour map")
	require.NotContains(t, steer, "steel blue")
	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: «не читать метку» не имеет права стать «не слать список вовсе».
	require.Contains(t, steer, "cuffs and collar: contrast rib, colourway RED, ribbed knit")
}

// TestColourWordNamesThePaintedLabel — the word beside the hex.
//
// ⚠ WHY A NAME AT ALL. An image model acts on «steel blue» far more reliably than on `#3a7bd5`, and
// the sentence has to be an instruction rather than a datum. The hex travels beside it for the
// precision, and the map itself is the final arbiter when the name is coarse.
//
// The cases below are chosen where the arithmetic can go wrong rather than where it obviously
// cannot: pure blue must not be pulled into the muted anchor beside it, a near-grey must not be
// named by a hue it does not really have, and a hue near the 0°/360° seam must not travel the long
// way round the wheel.
func TestColourWordNamesThePaintedLabel(t *testing.T) {
	for _, c := range []struct{ hex, want string }{
		{"#3a7bd5", "steel blue"},
		{"#0000ff", "blue"},
		{"#ff0000", "red"},
		{"#fe0505", "red"}, // just past the seam: hue is circular, 359° is not far from 1°
		{"#00ff00", "green"},
		{"#ffff00", "yellow"},
		{"#7f7f7f", "grey"},
		{"#828082", "grey"}, // a near-grey has no honest hue; weighting by saturation is what says so
		{"#ff9a00", "orange"},
		{"#8b4513", "brown"},
		{"#ff00ff", "magenta"},
	} {
		require.Equalf(t, c.want, colourWord(c.hex), "colourWord(%s)", c.hex)
	}

	// A LABEL THAT IS NOT A LABEL FALLS BACK TO ITSELF. The door refuses anything that is not
	// #rrggbb, so this is only reachable through a snapshot frozen by something that bypassed it —
	// and there the honest answer is the value we were handed, not a colour name invented for it.
	require.Equal(t, "not a hex", colourWord("not a hex"))
	require.Equal(t, "#zzzzzz", colourWord("#zzzzzz"))
}

// TestColourMapSentenceListsSeveralMapsAndSeveralBareViews — plural grammar, because the sentence
// is read by a model and «Images 3, 4 are colour maps of the front, back drawings» is a list a
// model may take apart into separate instructions.
func TestColourMapSentenceListsSeveralMapsAndSeveralBareViews(t *testing.T) {
	attached := []refCaption{
		{MediaID: 1}, {MediaID: 2}, {MediaID: 20}, {MediaID: 21},
	}
	got := renderColourMapSentence(
		[]colourMap{{MediaID: 20, View: "front"}, {MediaID: 21, View: "back"}},
		[]string{"front", "back", "side_l", "side_r"}, attached)

	require.Contains(t, got, "Images 3 and 4 are colour maps of the front and back drawings")
	require.Contains(t, got, "The left side and right side drawings carry no colour map: on those "+
		"views, divide the cloths as the mapped views imply.")

	// Every view painted → no bare-view sentence at all. Naming an empty set would be a sentence
	// about nothing, which is the one thing this whole block exists to avoid.
	all := renderColourMapSentence(
		[]colourMap{{MediaID: 20, View: "front"}, {MediaID: 21, View: "back"}},
		[]string{"front", "back"}, attached)
	require.NotContains(t, all, "carries no colour map")
	require.NotContains(t, all, "carry no colour map")
}
