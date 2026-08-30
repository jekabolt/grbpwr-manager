package designgen

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// The owner's craft paragraphs, spelled HERE AS THEIR OWN LITERALS on purpose. If a test compared
// against the production constants, an edit to those constants would rewrite both sides of the
// assertion at once and any "improvement" of the owner's wording would pass silently. These copies
// are the reference the constants are held to.
const (
	ownerStyleGarment    = "Style: black vector line art on a plain white background. Uniform, precise lines; heavier weight for outer contours, thin lines for internal design lines; fine dashed lines for topstitching and seam stitching. Garment drawn flat and symmetrical with subtle body-form shaping. No human body, no mannequin, no hanger."
	ownerExcludedGarment = "Strictly excluded: color, fills, shading, gradients, shadows, fabric texture or print, logos, text, labels, measurements, callouts, background elements."
	ownerStyleDetail     = "Style: black vector line art on a plain white background. Heavier weight for outer contours, thin lines for internal design lines, fine dashed lines for topstitching and seam stitching. Flat, technical, true proportions. No human body, no mannequin, no hanger."
	ownerExcludedDetail  = "Strictly excluded: color, fills, shading, gradients, shadows, fabric texture or print, logos, text, labels, measurements, arrows, background elements."
	ownerOutput          = "Output: high resolution, crisp clean lines, white seamless background, apparel industry technical drawing aesthetic."
)

// flatRun builds a flat run over frozen params/inputs for prompt tests. One reference is the
// normal case; tests that need zero or many write their own inputs.
func flatPrompt(t *testing.T, params, inputs string) string {
	t.Helper()
	r := testRun(1, entity.DesignRunKindFlat)
	r.Params = entity.RawJSON(params)
	r.Inputs = entity.RawJSON(inputs)
	r.Ask = sql.NullString{String: "ASK-the words of the person", Valid: true}
	return composePrompt(r, parseParams(r.Params), parseInputs(r.Inputs))
}

const oneRef = `{"refs":[{"media_id":11,"role":"silhouette","note":"NOTE-collar"}]}`

// TestFlatCarriesTheOwnersCraftVerbatim — the craft paragraphs are the owner's reference, not a
// draft: Style, Strictly excluded and Output must ride into every flat prompt word for word.
func TestFlatCarriesTheOwnersCraftVerbatim(t *testing.T) {
	got := flatPrompt(t, `{"views":["front","back","side_l"],"layout":"one"}`, oneRef)
	require.Contains(t, got, ownerStyleGarment, "the owner's Style paragraph must appear verbatim")
	require.Contains(t, got, ownerExcludedGarment, "the owner's exclusion paragraph must appear verbatim")
	require.Contains(t, got, ownerOutput, "the owner's Output paragraph must appear verbatim")
	require.Contains(t, got,
		"Turn the garment shown in the reference image into a professional fashion technical flat sketch (CAD-style tech pack drawing).")
	// …and the human context is still there, before the craft.
	require.Contains(t, got, "ASK-the words of the person")
	require.Contains(t, got, "NOTE-collar")
}

// TestFlatSingleViewHasNoRow — case ①. A one-view run must not ask for "side by side": a row spec
// with a single member requests a canvas holding one small drawing in a row.
func TestFlatSingleViewHasNoRow(t *testing.T) {
	got := flatPrompt(t, `{"views":["front"],"layout":"one"}`, oneRef)
	require.Contains(t, got, "Layout: a single view — FRONT — the garment drawn once, isolated and centered on the canvas.")
	require.NotContains(t, got, "side by side")
	require.NotContains(t, got, "SIDE LEFT")
	require.Contains(t, got, ownerExcludedGarment)
}

// TestFlatCompositeNamesTheChosenViewsInParamsOrder — case ③, and the order IS params order.
//
// compositeViewsOf in the store records p.Views VERBATIM as "what is glued into this sheet" and
// the splitter labels the cut frames from that record. The prompt's left-to-right list and that
// record must be the same list in the same order — a prompt that re-sorted the views to the
// canonical front-first order would make every split frame of a back-first run mislabeled.
func TestFlatCompositeNamesTheChosenViewsInParamsOrder(t *testing.T) {
	got := flatPrompt(t, `{"views":["back","front","side_l"],"layout":"one"}`, oneRef)
	require.Contains(t, got,
		"Layout: three views on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced — left to right: BACK, FRONT, SIDE LEFT.")
}

// TestFlatTwoSidesOfAnAsymmetricGarment — the "two boks" reality the fixed three-view prompt
// cannot say: side_l and side_r as two members of one row, named apart.
func TestFlatTwoSidesOfAnAsymmetricGarment(t *testing.T) {
	got := flatPrompt(t, `{"views":["side_l","side_r"],"layout":"one"}`, oneRef)
	require.Contains(t, got,
		"Layout: two views on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced — left to right: SIDE LEFT, SIDE RIGHT.")
}

// TestFlatPerViewNamesTheSetWithoutARow — case ②: each chosen view is its own image and its own
// paid call, so the base prompt names the whole set for scale consistency but must not describe a
// multi-view canvas.
func TestFlatPerViewNamesTheSetWithoutARow(t *testing.T) {
	got := flatPrompt(t, `{"views":["front","back","side_l"],"layout":"per_view"}`, oneRef)
	require.Contains(t, got, "one drawing out of a set of three")
	require.Contains(t, got, "FRONT, BACK, SIDE LEFT, one view per image")
	require.NotContains(t, got, "side by side")
	require.NotContains(t, got, "on one horizontal canvas")
	require.Contains(t, got, ownerStyleGarment)

	// The per-call half: viewPrompt still names the one view this paid call is for, after the
	// whole base prompt — craft included.
	perCall := viewPrompt(got, "back")
	require.True(t, strings.HasSuffix(perCall, "view:\nback"))
}

// TestDetailRunsGetTheDetailEtalon — a detail is Эталон 2, not Эталон 1: callout drawing, single
// enlarged view, the detail exclusion list (arrows, not callouts).
func TestDetailRunsGetTheDetailEtalon(t *testing.T) {
	got := flatPrompt(t, `{"views":["detail"],"layout":"one"}`, oneRef)
	require.Contains(t, got,
		"Turn the construction detail shown in the reference image into a professional fashion technical detail sketch (CAD-style tech pack callout drawing).")
	require.Contains(t, got,
		"Layout: a single enlarged view of the detail, isolated and centered on the canvas, shown from the same angle as the reference. Include just enough of the surrounding garment panel or edge to make the construction readable, with the fragment ending in clean straight edges.")
	require.Contains(t, got, ownerStyleDetail)
	require.Contains(t, got, ownerExcludedDetail)
	require.Contains(t, got, ownerOutput)
	require.NotContains(t, got, "side by side")
	require.NotContains(t, got, "technical flat sketch", "a detail run must not carry the garment intro")
	require.NotContains(t, got, ownerStyleGarment)
}

// TestCraftIsForFlatsOnly — the owner gave the reference prompts for flats. A render is a garment
// in a scene, a 3D run is a Meshy build, the vector kind redraws an approved raster and
// draft_idea never reaches the worker: none of them may inherit "black vector line art".
func TestCraftIsForFlatsOnly(t *testing.T) {
	for _, kind := range []string{
		entity.DesignRunKindRender, entity.DesignRunKindThreed,
		entity.DesignRunKindVector, entity.DesignRunKindDraftIdea,
	} {
		r := testRun(1, kind)
		r.Params = entity.RawJSON(`{"views":["front","back"],"layout":"one"}`)
		r.Inputs = entity.RawJSON(oneRef)
		r.Ask = sql.NullString{String: "ASK", Valid: true}
		got := composePrompt(r, parseParams(r.Params), parseInputs(r.Inputs))
		require.NotContains(t, got, "Strictly excluded", "kind %s must keep its bare context", kind)
		require.NotContains(t, got, "black vector line art", "kind %s must keep its bare context", kind)
		require.Contains(t, got, "ASK")
	}
}

// TestTheCraftGetsTheLastWordOverTheColour — the ordering decision made testable. The colourway
// recipe is legitimate human context and it stays; but where it collides with the craft ("olive"
// against "Strictly excluded: color…"), the craft must be the later — therefore binding — word.
func TestTheCraftGetsTheLastWordOverTheColour(t *testing.T) {
	got := flatPrompt(t,
		`{"views":["front"],"layout":"one","colour":{"words":"COLOUR-olive drab","code":"OLV-03"}}`,
		oneRef)
	colourAt := strings.Index(got, "COLOUR-olive drab")
	excludedAt := strings.Index(got, ownerExcludedGarment)
	require.GreaterOrEqual(t, colourAt, 0, "the colour recipe must stay in the prompt")
	require.GreaterOrEqual(t, excludedAt, 0)
	require.Less(t, colourAt, excludedAt,
		"the exclusion paragraph must speak after the colour words, or the flat comes back olive")

	askAt := strings.Index(got, "ASK-the words of the person")
	craftAt := strings.Index(got, "Turn the garment")
	require.Less(t, askAt, craftAt, "every human word comes before the craft block")
}

// TestFlatWithoutReferencesSpeaksToTheDescription — a run launched from words alone has no
// reference image, and telling the model to be "true to the reference" it was never shown invites
// it to invent one.
func TestFlatWithoutReferencesSpeaksToTheDescription(t *testing.T) {
	got := flatPrompt(t, `{"views":["front"],"layout":"one"}`, `{"garment_note":"boxy overshirt"}`)
	require.Contains(t, got,
		"Draw the garment described above as a professional fashion technical flat sketch (CAD-style tech pack drawing).")
	require.NotContains(t, got, "shown in the reference")
	require.NotContains(t, got, "true to the reference")
	require.Contains(t, got, ownerExcludedGarment, "the craft rides along even without references")
}

// TestFlatManyReferencesPluralizeTheIntro — two pictures are "reference images"; the singular of
// the owner's original stays for the single-picture case it was written at.
func TestFlatManyReferencesPluralizeTheIntro(t *testing.T) {
	got := flatPrompt(t, `{"views":["front"],"layout":"one"}`,
		`{"refs":[{"media_id":11},{"media_id":12}]}`)
	require.Contains(t, got, "shown in the reference images into")
}

// TestBrokenSnapshotStillGetsTheCraft — a flat whose params do not parse still runs (lenient
// parse), and it must still run AS A FLAT: no views to lay out is no reason to drop the style,
// the exclusions and the output contract.
func TestBrokenSnapshotStillGetsTheCraft(t *testing.T) {
	got := flatPrompt(t, `{"views": [ this is not json`, oneRef)
	require.Contains(t, got, "Layout: the garment drawn once, isolated and centered on the canvas.")
	require.NotContains(t, got, "side by side")
	require.Contains(t, got, ownerStyleGarment)
	require.Contains(t, got, ownerExcludedGarment)
	require.Contains(t, got, ownerOutput)
}
