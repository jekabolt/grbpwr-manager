package designgen

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestFlatConstructionNote — wave 10: the card 51 description (beta run 170) as a flat reads it.
func TestFlatConstructionNote(t *testing.T) {
	in := "garment: blazer\nfit: regular\nSingle-breasted blazer, regular fit with easy chest room, slim body through the waist, cropped to the hip bone with a curved front hem and waist shaping, light shoulder pad. Notch lapel with matched gorge height. Welt pockets concealed in the side seams of the front body, inside chest pocket on the lining. Body seams plain, pressed open with overlocked edges; hem, front edge and sleeve openings blind hemmed. Fully lined. Mid-weight rustic linen with a slubby surface, soft tailored drape. Notch lapel with matched gorge height.\ndecided · collar: notch\n\ndecided with the designer (current card fields outrank these when they conflict):"
	got := FlatConstructionNote(in)
	require.Equal(t, "garment: blazer\nSingle-breasted blazer, slim body through the waist, cropped to the hip bone with a curved front hem and waist shaping, light shoulder pad. Notch lapel with matched gorge height. Welt pockets concealed in the side seams of the front body. Body seams plain.", got)
	require.Equal(t, "", FlatConstructionNote("fit: slim\nFully lined."))
	require.Equal(t, "garment: top", FlatConstructionNote("garment: top\ngarment: shirt"))
	// a knit rib band, a sheer layer and a fitted inner layer are drawn — they stay
	require.Equal(t, "neckline finished with a knit rib band; fitted inner tank under a sheer outer layer",
		FlatConstructionNote("neckline finished with a knit rib band; fitted inner tank under a sheer outer layer"))
}

// TestFlatVisibleJoins — hidden items (lining, an inside pocket) leave the list with every reference to
// them; a layer left with nothing to draw goes, and with one layer left the LAYERS block goes too.
func TestFlatVisibleJoins(t *testing.T) {
	j := entity.DesignJoinsDoc{
		Layers: []entity.DesignJoinLayer{{Index: 0, Name: "outer shell", Note: "opaque slubby mid-weight linen"}, {Index: 1, Name: "lining"}},
		Items: []entity.DesignJoinItem{
			{ID: "hem_back", Kind: "edge", From: "HEM_L", To: "HEM_R", Text: "straight back hem, blind hemmed"},
			{ID: "lining_body", Kind: "edge", From: "HEM_L", To: "HEM_R", Layer: 1, Visibility: entity.DesignJoinHidden, CaughtInto: []string{"hem_back"}},
			{ID: "side_L", Kind: "seam", From: "UA_L", To: "HEM_L", ContinuesInto: []string{"lining_body", "hem_back"}, Text: "designer words, pressed open", Edited: true},
		},
	}
	v := flatVisibleJoins(j)
	require.Len(t, v.Items, 2)
	require.Nil(t, v.Layers)
	require.Equal(t, "Straight back hem", v.Items[0].Text)
	require.Equal(t, []string{"hem_back"}, v.Items[1].ContinuesInto)
	require.Equal(t, "designer words, pressed open", v.Items[1].Text, "a designer's words stay verbatim")
	text := strings.Join(joinsCraft(v), "\n")
	require.NotContains(t, text, "lining")
	require.NotContains(t, text, "LAYERS")
	require.Len(t, j.Items, 3, "the frozen doc is not touched")
}

// TestGarmentLabelIsNotSaidTwice — «garment:\ngarment: blazer» (wave 10): WORDS that open with their own
// «garment:» line are written without the label; other words keep it.
func TestGarmentLabelIsNotSaidTwice(t *testing.T) {
	p := composePrompt(entity.DesignRun{Kind: entity.DesignRunKindRender}, runParams{}, runInputs{GarmentNote: "garment: blazer\nNotch lapel."}, nil)
	require.True(t, strings.HasPrefix(p, "garment: blazer\nNotch lapel."), p)
	p = composePrompt(entity.DesignRun{Kind: entity.DesignRunKindRender}, runParams{}, runInputs{GarmentNote: "Notch lapel."}, nil)
	require.True(t, strings.HasPrefix(p, "garment:\nNotch lapel."), p)
}
