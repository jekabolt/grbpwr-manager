package designgen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestFlatConstructionNote — wave 10: the card 51 description (beta run 170) as a flat reads it.
func TestFlatConstructionNote(t *testing.T) {
	// Wave 10: by default a flat sends only the class; the description filter is behind the switch.
	require.Equal(t, "garment: blazer", FlatConstructionNote("garment: blazer\nfit: regular\nSingle-breasted blazer, slim body through the waist."))
	defer func(v bool) { FlatWordsCarryDescription = v }(FlatWordsCarryDescription)
	FlatWordsCarryDescription = true
	in := "garment: blazer\nfit: regular\nSingle-breasted blazer, regular fit with easy chest room, slim body through the waist, cropped to the hip bone with a curved front hem and waist shaping, light shoulder pad. Notch lapel with matched gorge height. Welt pockets concealed in the side seams of the front body, inside chest pocket on the lining. Body seams plain, pressed open with overlocked edges; hem, front edge and sleeve openings blind hemmed. Fully lined. Mid-weight rustic linen with a slubby surface, soft tailored drape. Notch lapel with matched gorge height.\ndecided · collar: notch\n\ndecided with the designer (current card fields outrank these when they conflict):"
	got := FlatConstructionNote(in)
	require.Equal(t, "garment: blazer\nSingle-breasted blazer, slim body through the waist, cropped to the hip bone with a curved front hem and waist shaping, light shoulder pad. Notch lapel with matched gorge height. Welt pockets concealed in the side seams of the front body. Body seams plain.", got)
	require.Equal(t, "", FlatConstructionNote("fit: slim\nFully lined."))
	require.Equal(t, "garment: top", FlatConstructionNote("garment: top\ngarment: shirt"))
	// a material word next to construction loses the word, not the construction (Codex review)
	// whole clauses only — a material word drops its clause, never leaves a fragment (owner, wave 10)
	require.Equal(t, "", FlatConstructionNote("Linen jacket with patch pockets."))
	require.Equal(t, "", FlatConstructionNote("hem finished with a self-fabric band at the hip bone."))
	require.Equal(t, "", FlatConstructionNote("soft stretch jersey drape"))
	require.Equal(t, "Back vent.", FlatConstructionNote("back vent, shell with soft tailored drape."))
	require.Equal(t, "Single-breasted blazer.", FlatConstructionNote("Single-breasted blazer, soft tailored drape."))
	// the hidden goes whole, even next to a construction word
	require.Equal(t, "", FlatConstructionNote("inside chest pocket on the lining"))
	// a knit rib band, a sheer layer and a fitted inner layer are drawn — they stay
	require.Equal(t, "neckline finished with a knit rib band; fitted inner tank under a sheer outer layer",
		FlatConstructionNote("neckline finished with a knit rib band; fitted inner tank under a sheer outer layer"))
}

// TestGarmentLabelIsNotSaidTwice — «garment:\ngarment: blazer» (wave 10): WORDS that open with their own
// «garment:» line are written without the label; other words keep it.
func TestGarmentLabelIsNotSaidTwice(t *testing.T) {
	p := composePrompt(entity.DesignRun{Kind: entity.DesignRunKindRender}, runParams{}, runInputs{GarmentNote: "garment: blazer\nNotch lapel."}, nil)
	require.True(t, strings.HasPrefix(p, "garment: blazer\nNotch lapel."), p)
	p = composePrompt(entity.DesignRun{Kind: entity.DesignRunKindRender}, runParams{}, runInputs{GarmentNote: "Notch lapel."}, nil)
	require.True(t, strings.HasPrefix(p, "garment:\nNotch lapel."), p)
}

// TestFlatGarmentNote — M14 (owner 07.10: «показывай в WORDS только то, что уходит»): a flat sends
// the description's class line and then the person's own flat words as typed. The same cases stand
// in the client's probe of its mirror (flat-route.ts flatWordsSent, `yarn flat:words`).
//
// MUTATIONS IT CATCHES: the description's other words travelling again; the human words filtered
// by the junk rules (they are a person's); a card with no flat words sending anything new; blank
// lines or the line's own spaces reaching the prompt.
func TestFlatGarmentNote(t *testing.T) {
	const desc = "garment: tank top\nfit: slim\nTwo-layer sleeveless top, slim body-hugging silhouette, close through the chest."
	for _, c := range []struct{ desc, human, want string }{
		{desc, "", "garment: tank top"},
		{desc, "inner V neckline under the sheer layer\n\n  crossed straps meet at the back neck  ", "garment: tank top\ninner V neckline under the sheer layer\ncrossed straps meet at the back neck"},
		{"", "a line", "a line"},
		{"", "  \n ", ""},
		{"Two-layer sleeveless top.", "x", "x"},
		{"garment:\ngarment: blazer", "no topstitching", "garment: blazer\nno topstitching"},
		// a person's words are sent as written — even a word the description filter would drop
		{"- garment: shirt", "fit: slim\nlinen, fully lined", "garment: shirt\nfit: slim\nlinen, fully lined"},
		{"garment: top\r\n", "a\r\nb", "garment: top\na\nb"},
	} {
		require.Equal(t, c.want, FlatGarmentNote(c.desc, c.human), "%q + %q", c.desc, c.human)
	}
	// no flat words → exactly the words a flat sent before M14
	require.Equal(t, FlatConstructionNote(desc), FlatGarmentNote(desc, ""))
	require.Equal(t, FlatConstructionNote(desc), FlatGarmentNote(desc, " \n\t\n"))
}
