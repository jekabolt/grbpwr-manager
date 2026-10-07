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
