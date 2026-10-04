package designgen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ─────────────────────────── T13: THE CLOTH MOCKUP OF A COLOUR MAP ───────────────────────────
//
// Measured (paint-parts t13 A/B): handed a mockup, the image model copies the motif's SCALE and
// colours from it; placement is already right from the map alone. So the mockup is attached right
// after its map, captioned as WHERE + SCALE and never as the look, and the cloth list names it by
// number. A run without one composes exactly the prompt it composed before.

const mockupCaptionFront = "cloth mockup of the front flat — each labelled part filled flat with its " +
	"cloth's tile at the cloth's true repeat; it shows WHERE each cloth goes and the motif's SCALE and " +
	"colours; it has no folds, no light and no volume — never copy its flat, unlit look"

func twoClothsMaps(frontMockup, backMockup string) string {
	return `{"views":["front","back"],"layout":"one",` +
		`"colour":{"code":"RED-01","hex":"#b1121a","fabric_media_id":9,` +
		`"colour_maps":[{"media_id":20,"view":"front"` + frontMockup + `},{"media_id":21,"view":"back"` + backMockup + `}],` +
		`"fabrics":[` +
		`{"name":"main jersey","media_id":9,"map_hex":"#3a7bd5"},` +
		`{"name":"contrast rib","media_id":10,"map_hex":"#ff0000"}]}}`
}

// TestEachMockupIsAttachedRightAfterItsMap — order, caption and the numbered sentence.
func TestEachMockupIsAttachedRightAfterItsMap(t *testing.T) {
	p := twoClothsMaps(`,"mockup_media_id":30`, `,"mockup_media_id":31`)
	got := renderPrompt(t, p, renderSlots)

	// plates 1, 2 · front map 3 · its mockup 4 · back map 5 · its mockup 6 · swatches 7, 8
	require.Contains(t, got, "- image 3: colour map of the front flat")
	require.Contains(t, got, "- image 4: "+mockupCaptionFront)
	require.Contains(t, got, "- image 5: colour map of the back flat")
	require.Contains(t, got, "- image 6: cloth mockup of the back flat — ")
	require.Contains(t, got, "Images 3 and 5 are colour maps of the front and back drawings")
	require.Contains(t, got, "Images 4 and 6 are cloth mockups of the same drawings: take the scale of "+
		"each cloth's motif from them; take the construction from the drawings and the material and "+
		"drape from the cloth pictures.")
	require.Contains(t, clothLine(t, got, "1"), "Its texture is image 7")
	require.Contains(t, clothLine(t, got, "2"), "Its texture is image 8")

	list := referenceList("render", parseParams([]byte(p)), parseInputs([]byte(renderSlots)))
	require.True(t, list[3].IsMockup && !list[3].IsColourMap)
	require.Equal(t, "front", list[3].View)
}

// TestOneMockupIsNamedInTheSingular — one mapped view carries a mockup, the other does not.
func TestOneMockupIsNamedInTheSingular(t *testing.T) {
	got := renderPrompt(t, twoClothsMaps("", `,"mockup_media_id":31`), renderSlots)
	require.Contains(t, got, "- image 5: cloth mockup of the back flat")
	require.Contains(t, got, "Image 5 is a cloth mockup of the same drawing: take the scale of each "+
		"cloth's motif from it; take the construction from the drawing and the material and drape "+
		"from the cloth pictures.")
	require.NotContains(t, got, "cloth mockups of the same drawings")
}

// TestNoMockupChangesNothing — a run without mockups composes the prompt it composed before, byte
// for byte: an explicit 0 is the same run as an absent field, and neither says «mockup».
func TestNoMockupChangesNothing(t *testing.T) {
	without := renderPrompt(t, twoClothsMaps("", ""), renderSlots)
	zero := renderPrompt(t, twoClothsMaps(`,"mockup_media_id":0`, `,"mockup_media_id":0`), renderSlots)
	require.Equal(t, without, zero)
	require.NotContains(t, without, "mockup")
	require.NotContains(t, renderPrompt(t, twoClothsOneMap, renderSlots), "mockup")
}

// TestAMockupThatIsAnotherPictureIsNotCalledAMockup — one picture, one role. A mockup that is a
// bench plate stays a plate; a mockup behind a map that never went out as a map is not attached.
func TestAMockupThatIsAnotherPictureIsNotCalledAMockup(t *testing.T) {
	got := renderPrompt(t, twoClothsMaps(`,"mockup_media_id":1`, ""), renderSlots)
	require.NotContains(t, got, "cloth mockup")

	// the front «map» is the front plate → it is not a map, and its mockup does not travel at all
	p := strings.Replace(twoClothsMaps(`,"mockup_media_id":30`, ""), `"media_id":20`, `"media_id":1`, 1)
	got = renderPrompt(t, p, renderSlots)
	require.NotContains(t, got, "cloth mockup")
	for _, rc := range referenceList("render", parseParams([]byte(p)), parseInputs([]byte(renderSlots))) {
		require.NotEqual(t, 30, rc.MediaID)
	}
}

// TestAMockupWhoseMediaVanishedIsNeitherNumberedNorMentioned — the sentence reads the ATTACHED
// list: a mockup that did not go out is not named.
func TestAMockupWhoseMediaVanishedIsNeitherNumberedNorMentioned(t *testing.T) {
	maps := []colourMap{{MediaID: 20, View: "front", MockupMediaID: 30}}
	attached := []refCaption{{MediaID: 1}, {MediaID: 20, IsColourMap: true}}
	require.NotContains(t, renderColourMapSentence(maps, []string{"front"}, attached, false), "mockup")

	attached = append(attached, refCaption{MediaID: 30, IsMockup: true})
	require.Contains(t, renderColourMapSentence(maps, []string{"front"}, attached, false),
		"Image 3 is a cloth mockup of the same drawing")
}
