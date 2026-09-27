package designgen

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// craftOf composes the phase-2 paragraph the way buildJob does for an unmarked run: the attached
// list is the items in order (freeformReferences), so image k is the k-th distinct item.
func craftOf(t *testing.T, paramsJSON string) string {
	t.Helper()
	p := parseParams(entity.RawJSON(paramsJSON))
	require.NotNil(t, p.Freeform, "params did not parse: %s", paramsJSON)
	return freeformCraft(p, freeformReferences(p))
}

// TestEachPresetNAMES_ITS_PICTURES_BY_NUMBER — the roles decide the numbers, not the positions.
func TestEachPresetNAMES_ITS_PICTURES_BY_NUMBER(t *testing.T) {
	t.Run("tryon", func(t *testing.T) {
		c := craftOf(t, `{"freeform":{"preset":"tryon","items":[
		  {"media_id":12,"role":"product"},{"media_id":11,"role":"model"},{"media_id":13,"role":"product"}]}}`)
		require.Contains(t, c, "Keep the identity of the person of image 2 — the same face and skin tone")
		// G-02 M-1: tile 1's field is «Modify physical features & pose», so body, hair and pose are
		// kept only where the words do not change them — never «exactly». MUTATION (measured red):
		// the old «Keep the person of image N exactly — face, skin tone, body and hair.»
		require.Contains(t, c, "body, hair and pose stay as in the photo except where the words above change them")
		require.NotContains(t, c, "exactly — face", "the identity clause must not freeze body and hair")
		require.NotContains(t, c, "body and hair.", "body and hair are not frozen unconditionally")
		require.Contains(t, c, "garment of images 1 and 3")
		require.Contains(t, c, "Keep the scene of the model photo")
		require.NotContains(t, c, "outline", "an unmarked run is not told about outlines")
		// 20-PROMPTS D5: the product picture is a RENDER on white, and several are ONE garment.
		// MUTATION (measured red): the old «— reproduce its cut, colour, print and seams as they
		// are, worn the way that garment really sits on a body.» with no product-picture sentence.
		require.Contains(t, c, "garment of images 1 and 3: it is a product picture")
		require.Contains(t, c, "views of the same garment")
		require.Contains(t, c, "at its real scale")
		require.Contains(t, c, "Light it with the light of the scene")
		// MUTATION (measured red): the closing photograph sentence dropped.
		require.Contains(t, c, "The result is a photograph with the same lens and realism as the model photo. Return ONE picture.")
		// One product picture is «image N», never «images N».
		one := craftOf(t, `{"freeform":{"preset":"tryon","items":[
		  {"media_id":11,"role":"model"},{"media_id":12,"role":"product"}]}}`)
		require.Contains(t, one, "garment of image 2: it is a product picture")
	})
	t.Run("add_logo", func(t *testing.T) {
		c := craftOf(t, `{"freeform":{"preset":"add_logo","items":[
		  {"media_id":20,"role":"logo"},{"media_id":21}],"options":{"logo_size":"small"}}}`)
		require.Contains(t, c, "logo of image 1")
		require.Contains(t, c, "garment of image 2")
		require.Contains(t, c, "about 6 cm wide")
		require.Contains(t, craftOf(t, `{"freeform":{"preset":"add_logo","items":[{"media_id":21},{"media_id":20,"role":"logo"}]}}`),
			"about 10 cm wide", "an unstated size is medium")
	})
	t.Run("add_logo is applied, transparent and unstretched", func(t *testing.T) {
		// 20-PROMPTS D6: a logo PNG pasted with its own background is a white box on the cloth.
		// MUTATION (measured red): the old «Keep its exact shape, colours and letterforms; let it
		// follow the folds…» without the application clause.
		c := craftOf(t, `{"freeform":{"preset":"add_logo","items":[{"media_id":20,"role":"logo"},{"media_id":21}]}}`)
		require.Contains(t, c, "applied as a print or embroidery on the cloth")
		require.Contains(t, c, "transparent")
		require.Contains(t, c, "no box or patch around it")
		require.Contains(t, c, "it is not stretched")
		require.True(t, strings.HasSuffix(c, "Return ONE picture."))
	})
	t.Run("fabric_extract and ghost_mannequin", func(t *testing.T) {
		f := craftOf(t, `{"freeform":{"preset":"fabric_extract","items":[{"media_id":30}]}}`)
		require.Contains(t, f, "From image 1 extract the fabric")
		// 20-PROMPTS D6: a swatch is an asset that gets tiled — true scale and a full repeat.
		// MUTATION (measured red): the old «no garment shape, no folds, no shadows — the cloth
		// fills the whole frame.» with no repeat sentence.
		require.Contains(t, f, "full repeat")
		require.Contains(t, f, "true colours, motif and scale")
		require.Contains(t, f, "nothing else in the picture")
		g := craftOf(t, `{"freeform":{"preset":"ghost_mannequin","items":[{"media_id":30,"role":"subject"}]}}`)
		require.Contains(t, g, "garment of image 1")
		require.Contains(t, g, "ghost-mannequin")
		// MUTATION (measured red): the old «the inside of the back neck visible, a pure white…».
		require.Contains(t, g, "inside of the back neck and the label")
		require.Contains(t, g, "centred with a small margin")
		require.Contains(t, g, "no mannequin, hanger, body parts or shadow on the background")
	})
}

// TestTryonFRAMING_AND_ANGLE_ARE_WORDS_OR_NOTHING.
func TestTryonFRAMING_AND_ANGLE_ARE_WORDS_OR_NOTHING(t *testing.T) {
	const items = `"items":[{"media_id":11,"role":"model"},{"media_id":12,"role":"product"},{"media_id":13,"role":"scene"}]`
	c := craftOf(t, `{"freeform":{"preset":"tryon",`+items+`,"options":{"framing":"full_body","angle":"low_angle",`+
		`"scene_mode":"reference","scene_text":"a rooftop at dusk"}}}`)
	require.Contains(t, c, "full-length, head to feet")
	require.Contains(t, c, "low angle, from the ground")
	require.Contains(t, c, "The scene is image 3")
	require.Contains(t, c, "Scene: a rooftop at dusk.")

	auto := craftOf(t, `{"freeform":{"preset":"tryon",`+items+`,"options":{"framing":"auto","angle":"auto"}}}`)
	for _, w := range []string{"Frame it", "camera", "angle"} {
		require.NotContains(t, auto, w, "auto says nothing")
	}
	for framing, word := range map[string]string{
		"upper_body": "waist up", "portrait": "face and neck", "hands": "the hands",
		"feet": "the feet", "product_detail": "garment as worn",
	} {
		require.Contains(t, craftOf(t, `{"freeform":{"preset":"tryon",`+items+`,"options":{"framing":"`+framing+`"}}}`), word)
	}
}

// TestVariationsSAYS_HOW_FAR_PER_CREATIVITY_STEP.
func TestVariationsSAYS_HOW_FAR_PER_CREATIVITY_STEP(t *testing.T) {
	want := []string{
		"faithful variation", "reinterpret its details", "be free with the cut", "only as inspiration",
	}
	seen := map[string]bool{}
	for step, word := range want {
		c := craftOf(t, `{"freeform":{"preset":"variations","items":[{"media_id":40}],"options":{"creativity":`+
			itoa(step)+`}}}`)
		require.Containsf(t, c, word, "creativity %d", step)
		require.Contains(t, c, "image 1")
		// 20-PROMPTS D7: every level fixes the presentation, so the variation reads side by side.
		// MUTATION (measured red): the presentation sentence written only in case 0.
		require.Containsf(t, c, "Show it the same way as image 1 — the same kind of picture", "creativity %d", step)
		require.Containsf(t, c, "so the two read side by side. Return ONE picture.", "creativity %d", step)
		require.False(t, seen[c], "each step is its own paragraph")
		seen[c] = true
	}
}

// TestRetouchKEEPS_EVERYTHING_OUTSIDE_THE_AREA — the phase-2 wording, and its windowed twin.
func TestRetouchKEEPS_EVERYTHING_OUTSIDE_THE_AREA(t *testing.T) {
	p := parseParams(entity.RawJSON(`{"freeform":{"preset":"retouch","items":[{"media_id":50,"texts":["remove the stain"],
	  "regions":[{"kind":"TECH_CARD_ANNOTATION_KIND_POLYGON","points":[` +
		point("0.2", "0.2") + `,` + point("0.5", "0.2") + `,` + point("0.5", "0.5") + `]}]}]}}`))
	attached := freeformReferences(p)
	c := freeformCraft(p, attached)
	require.Contains(t, c, "area A on image 1")
	require.Contains(t, c, "outside")
	require.Contains(t, c, "pixel")
	require.Contains(t, c, freeformOutlineDisclaimer)

	windowed := []refCaption{{Caption: "a close crop", IsWindow: true}}
	w := freeformCraft(p, windowed)
	require.Contains(t, w, "Image 1 is a CLOSE CROP")
	require.Contains(t, w, "outside")
	require.Contains(t, w, "pixel")
	require.Contains(t, w, "SAME CROP")
	// G-02 Fable m-6: the window carries no outline, so no outline disclaimer. MUTATION (measured
	// red): the disclaimer appended after both branches again.
	require.NotContains(t, w, freeformOutlineDisclaimer, "a crop with no outline is not told about outlines")
	require.True(t, strings.HasSuffix(w, "Return ONE picture."))
}

// TestTheNewRolesAreCAPTIONED — a picture's caption says what it is in the ask.
func TestTheNewRolesAreCAPTIONED(t *testing.T) {
	for role, want := range map[string]string{
		entity.DesignFreeformRoleModel:   "the model — keep this person's identity",
		entity.DesignFreeformRoleProduct: "the garment to put on them",
		entity.DesignFreeformRoleScene:   "the scene",
		entity.DesignFreeformRoleLogo:    "the logo (PNG, keep exact)",
	} {
		require.Equal(t, want, freeformItemCaption(freeformItem{MediaID: 1, Role: role}))
	}
	// And a supporting role is never «the picture being worked on».
	ff := &freeformParams{Items: []freeformItem{{MediaID: 1, Role: entity.DesignFreeformRoleLogo}, {MediaID: 2}}}
	require.Equal(t, 2, freeformImageNumber(ff, []refCaption{{MediaID: 1}, {MediaID: 2}}, ""))
}

// TestAWordsOnlyFreeRunCOMPOSES_NO_CAPTION_BLOCK — tile 11: no picture, no «references» block, and
// a paragraph that does not talk about pictures.
func TestAWordsOnlyFreeRunCOMPOSES_NO_CAPTION_BLOCK(t *testing.T) {
	r := testRun(1, entity.DesignRunKindFreeform)
	r.Params = entity.RawJSON(`{"freeform":{"preset":"free"}}`)
	r.Ask = sql.NullString{String: "a red wool coat on a hanger", Valid: true}
	job, err := buildJob(context.Background(), media(), nil, r, "medium")
	require.NoError(t, err)
	require.Empty(t, job.References)
	require.True(t, strings.HasPrefix(job.Prompt, "a red wool coat on a hanger"))
	require.NotContains(t, job.Prompt, "references:")
	require.NotContains(t, job.Prompt, "pictures")
	require.True(t, strings.HasSuffix(job.Prompt, "Return ONE picture."))
	calls, err := imageCalls(job)
	require.NoError(t, err)
	require.Len(t, calls, 1)
}
