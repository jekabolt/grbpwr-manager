package designgen

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────── ARTWORK ON A RENDER (70-ROUND7 B7) ───────────────────────────

const artworkRenderParams = `{"views":["front","back"],"layout":"one",` +
	`"colour":{"code":"RED-01","hex":"#b1121a","fabric_media_id":9}}`

// One placed artwork: the chest embroidery on the FRONT flat (media 1), a quad tilted ~12°.
const artworkRenderInputs = `{"slots":[{"view_key":"front","media_id":1},{"view_key":"back","media_id":2}],` +
	`"artworks":[{"asset_id":7,"name":"chest embroidery","media_id":30,"view":"front","flat_media_id":1,` +
	`"corners":[{"x":0.34,"y":0.22},{"x":0.48,"y":0.25},{"x":0.47,"y":0.39},{"x":0.33,"y":0.36}],` +
	`"note":"embroidery"}]}`

const artworkGoldenParagraph = "ARTWORK. Image 3 is the artwork «chest embroidery» (embroidery). Apply it " +
	"on the front of the garment, wherever that side is visible, inside the four-cornered area marked on " +
	"the front flat (image 1) whose corners lie at — top-left 34 %, 22 %; top-right 48 %, 25 %; " +
	"bottom-right 47 %, 39 %; bottom-left 33 %, 36 % — of that flat's frame (x from its left edge, y " +
	"from its top edge), rotated about 12° clockwise. Make it real machine EMBROIDERY: dense satin-stitch " +
	"thread in the artwork's own colours, raised above the cloth by about 1–2 mm, visible stitch direction " +
	"and the soft sheen of thread, stitched edges that slightly gather the cloth around them. It is NOT a " +
	"print, NOT a flat patch, NOT an appliqué and has no backing or border of its own: only the artwork " +
	"itself is stitched, its exact shape, colours and letterforms kept. Its white ground is not part of it. " +
	"It fills that area at the area's own proportions and perspective, follows the folds and takes the " +
	"light of the photograph. Nowhere else on the garment."

// TestARenderCarriesItsPlacedArtwork — GOLDEN: the artwork picture goes out captioned as an artwork
// (after the plates, before the swatch), and one ARTWORK paragraph names it, its flat and the quad.
func TestARenderCarriesItsPlacedArtwork(t *testing.T) {
	got := renderPrompt(t, artworkRenderParams, artworkRenderInputs)
	require.Contains(t, got, "- image 3: artwork «chest embroidery» — embroidery: the artwork itself on a "+
		"plain ground, to be applied on the garment where the ARTWORK paragraph says")
	require.Contains(t, got, "\n\n"+artworkGoldenParagraph+"\n\n")
	// The swatch is numbered after the artwork.
	require.Contains(t, got, "(image 4)")
	// The paragraph stands after the cloth section and before the layout.
	require.Less(t, strings.Index(got, artworkGoldenParagraph), strings.Index(got, "Layout:"))
}

// TestARenderWithoutArtworksIsTheFrozenPrompt — no `artworks` key → byte-identical prompt.
func TestARenderWithoutArtworksIsTheFrozenPrompt(t *testing.T) {
	with := renderPrompt(t, artworkRenderParams, `{"slots":[{"view_key":"front","media_id":1},{"view_key":"back","media_id":2}],"artworks":[]}`)
	without := renderPrompt(t, artworkRenderParams, `{"slots":[{"view_key":"front","media_id":1},{"view_key":"back","media_id":2}]}`)
	require.Equal(t, without, with)
	require.NotContains(t, without, "ARTWORK")
	require.Equal(t, renderCraft(runParams{}, nil, nil), renderCraftWith(runParams{}, nil, nil, nil))
}

// TestAnArtworkWhoseFlatDidNotGoOutIsNotMentioned — the flat is not among the sent plates: no
// paragraph (the picture still travels, captioned, but nothing points the model at a frame it lacks).
func TestAnArtworkWhoseFlatDidNotGoOutIsNotMentioned(t *testing.T) {
	in := strings.Replace(artworkRenderInputs, `"flat_media_id":1`, `"flat_media_id":99`, 1)
	got := renderPrompt(t, artworkRenderParams, in)
	require.NotContains(t, got, "ARTWORK.")
}

// TestOnlyARenderAttachesArtworks — a frozen `artworks` key on any other kind attaches nothing.
func TestOnlyARenderAttachesArtworks(t *testing.T) {
	in := parseInputs([]byte(artworkRenderInputs))
	require.Len(t, in.Artworks, 1)
	for _, kind := range []string{entity.DesignRunKindFlat, entity.DesignRunKindThreed} {
		for _, rc := range referenceList(kind, runParams{}, in) {
			require.NotEqualf(t, 30, rc.MediaID, "%s must not attach the artwork", kind)
		}
	}
	list := referenceList(entity.DesignRunKindRender, runParams{}, in)
	require.True(t, list[len(list)-1].IsArtwork)
}

// TestArtworkRotationReadsTheTopEdge — clockwise positive, counter-clockwise negative, upright 0.
func TestArtworkRotationReadsTheTopEdge(t *testing.T) {
	require.Equal(t, 0, artworkRotation([]artworkCorner{{0.1, 0.1}, {0.5, 0.1}, {0.5, 0.3}, {0.1, 0.3}}))
	require.Equal(t, 45, artworkRotation([]artworkCorner{{0.1, 0.1}, {0.3, 0.3}, {0.1, 0.5}, {0, 0.3}}))
	require.Equal(t, -45, artworkRotation([]artworkCorner{{0.1, 0.3}, {0.3, 0.1}, {0.5, 0.3}, {0.3, 0.5}}))
}

// TestTheClientsMarkersNeverReachTheRenderPrompt — « · cut» and the picture markers are bookkeeping.
func TestTheClientsMarkersNeverReachTheRenderPrompt(t *testing.T) {
	in := strings.Replace(artworkRenderInputs, `"note":"embroidery"`, `"note":"embroidery, artwork = picture 1 · cut"`, 1)
	got := renderPrompt(t, artworkRenderParams, in)
	require.Contains(t, got, artworkGoldenParagraph)
	require.NotContains(t, got, "· cut")
	require.NotContains(t, got, "picture 1")
	for in, want := range map[string]string{
		"embroidery · cut":                    "embroidery",
		"screen print, Artwork = Picture 1":   "screen print",
		"woven; logo = picture 1 · cut · cut": "woven",
		"artwork = picture 1":                 "",
		"chest embroidery · ROSSO":            "chest embroidery · ROSSO",
	} {
		require.Equalf(t, want, entity.DesignArtworkTechniqueWords(in), "%q", in)
	}
}
