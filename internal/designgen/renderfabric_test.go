package designgen

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────── THE GOLDEN PROMPTS ───────────────────────────
//
// These are the composed prompts of six single-cloth runs, CAPTURED FROM THE COMPOSER BEFORE the
// several-cloths wave was written and pasted here as literals.
//
// ⚠ WHY A GOLDEN AND NOT A HANDFUL OF Contains ASSERTIONS. The composed prompt is written into the
// run's history (RecordRunPrompt), so a single-cloth render's wording is not a matter of taste: it
// is what every run already frozen in that history says. Soften one clause, reorder two sentences
// or slip a new paragraph in, and every FUTURE single-cloth run starts saying something the past
// ones do not — silently, with nothing to compare and no way back. `Contains` cannot see any of
// that; only the whole text, byte for byte, can.
//
// ⚠ AND THEY ARE LITERALS, NEVER renderStyle + renderMaterial + …. A golden assembled out of the
// production constants has both sides of its own assertion rewritten by the same edit and goes on
// passing while the prompt changes underneath it — the trap renderprompt_test.go's own header
// already names. The four closing paragraphs are factored into goldenRenderTail because they are
// shared by all six cases, not because they are derived from anything.
const goldenRenderTail = "Style: photorealistic product photograph of the finished garment in real cloth, shown AS IF WORN — the fabric holds the volume of a body underneath it: chest, waist and hip shaping, sleeves and straps carrying their own weight, soft natural folds where the cloth falls, and the garment's own soft self-shadow inside those folds. There is no person, no body part, no mannequin, no dress form, no hanger and no visible support of any kind: the garment holds its shape in empty space. Even, soft, diffuse frontal studio light; no cast shadow on the background, no hot highlights, no vignette." + "\n\n" +
	"The material must read as real cloth: the weave or knit structure visible at close range, the fibre's own sheen or matte finish, sheer or open fabric slightly translucent where it overlaps itself, hems, bindings and edges finished the way a sewn edge is finished, seams and topstitching soft and pressed rather than drawn as lines." + "\n\n" +
	"Strictly excluded: any human body or body part, skin, hair, face, hands, mannequin, dress form, bust, hanger, rail, stand or clip; background objects, props, furniture, floor, wall, horizon line or scenery of any kind; drop shadow or reflection on the background; text, labels, watermarks, logos, measurements, callouts, arrows, dimension lines or drawn outlines of any kind; any colour, print or pattern that neither the fabric photograph nor the stated colour calls for." + "\n\n" +
	"Output: high resolution, sharp focus across the whole garment, seamless pure white background, true colour, e-commerce product photography aesthetic."

const goldenAllThree = "ASK-the words of the person" + "\n\n" +
	"colour:\ncolourway RED-01 — the exact value is #b1121a" + "\n\n" +
	"fabric in words:\nWORDS-brushed, slight sheen" + "\n\n" +
	"references:\n- image 1: current state of the garment — front view\n- image 2: current state of the garment — back view\n- image 3: fabric photograph — the material this garment is made of: read its weave, texture, sheen and drape from here" + "\n\n" +
	"Turn the garment shown in the reference images into a photorealistic photograph of the finished garment in real cloth. The technical drawings among them define the CONSTRUCTION and must be followed exactly — silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, darts, pleats, pockets, closures, waistband, cuffs, hems and bindings. Add nothing they do not show and leave out nothing they do. Each image is described in the reference list above; the markup drawn on a drawing is an instruction about that garment, never a thing to be drawn into the photograph." + "\n\n" +
	"Fabric. The cloth of this garment is stated in more than one way at once, and the statements may disagree. Resolve every disagreement in this fixed order of authority, the same way on every run:\n1. THE FABRIC PHOTOGRAPH (image 3) governs the MATERIAL of this garment: weave or knit structure, surface texture, pile, sheen, transparency, weight and the way the cloth drapes and folds. Read the cloth from that image and from nothing else.\n2. THE STATED COLOUR — the `colour` block above — governs the COLOUR of this garment, and OVERRIDES the colour of the fabric photograph. Where the two disagree, keep the photograph's material and re-colour it: the garment is the stated colour even when the swatch photograph is another one.\n3. THE FABRIC DESCRIPTION IN WORDS — the `fabric in words` block above — adds only what neither the photograph nor the stated colour already says. It never overrides either of them: a word that contradicts the photograph's material or the stated colour is to be ignored." + "\n\n" +
	"Layout: three views of the SAME garment on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced, with clear white space between them and none of them overlapping — left to right: FRONT, SIDE LEFT, BACK. It is one garment photographed from three angles, not three garments: the same cloth, the same colour and the same lighting in every view." + "\n\n" +
	goldenRenderTail

const goldenColourOnly = "ASK-the words of the person" + "\n\n" +
	"colour:\ncolourway RED-01 — the exact value is #b1121a" + "\n\n" +
	"references:\n- image 1: current state of the garment — front view\n- image 2: current state of the garment — back view" + "\n\n" +
	"Turn the garment shown in the reference images into a photorealistic photograph of the finished garment in real cloth. The technical drawings among them define the CONSTRUCTION and must be followed exactly — silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, darts, pleats, pockets, closures, waistband, cuffs, hems and bindings. Add nothing they do not show and leave out nothing they do. Each image is described in the reference list above; the markup drawn on a drawing is an instruction about that garment, never a thing to be drawn into the photograph." + "\n\n" +
	"Fabric. THE STATED COLOUR — the `colour` block above — is the colour of this garment: render the whole garment in exactly that colour, and do not shift it towards a more photogenic neighbour. This run states no fabric photograph and no description in words, so choose a plain, unpatterned mid-weight cloth in that colour and invent no print, no texture and no trim the drawings do not show." + "\n\n" +
	"Layout: two views of the SAME garment on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced, with clear white space between them and none of them overlapping — left to right: FRONT, BACK. It is one garment photographed from two angles, not two garments: the same cloth, the same colour and the same lighting in every view." + "\n\n" +
	goldenRenderTail

const goldenWordsOnly = "ASK-the words of the person" + "\n\n" +
	"fabric in words:\nWORDS-heavy melton" + "\n\n" +
	"references:\n- image 1: current state of the garment — front view\n- image 2: current state of the garment — back view" + "\n\n" +
	"Turn the garment shown in the reference images into a photorealistic photograph of the finished garment in real cloth. The technical drawings among them define the CONSTRUCTION and must be followed exactly — silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, darts, pleats, pockets, closures, waistband, cuffs, hems and bindings. Add nothing they do not show and leave out nothing they do. Each image is described in the reference list above; the markup drawn on a drawing is an instruction about that garment, never a thing to be drawn into the photograph." + "\n\n" +
	"Fabric. THE FABRIC DESCRIPTION IN WORDS — the `fabric in words` block above — is the only statement this run makes about the cloth, and it governs the material outright: build the weave or knit, the weight, the surface and the drape from those words. Where they do not say, choose the plainest reading of them and invent no print, no pattern and no trim the drawings do not show." + "\n\n" +
	"Layout: a single view — FRONT — the garment photographed once, isolated and centred on the canvas." + "\n\n" +
	goldenRenderTail

const goldenNoFabric = "ASK-the words of the person" + "\n\n" +
	"references:\n- image 1: current state of the garment — front view\n- image 2: current state of the garment — back view" + "\n\n" +
	"Turn the garment shown in the reference images into a photorealistic photograph of the finished garment in real cloth. The technical drawings among them define the CONSTRUCTION and must be followed exactly — silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, darts, pleats, pockets, closures, waistband, cuffs, hems and bindings. Add nothing they do not show and leave out nothing they do. Each image is described in the reference list above; the markup drawn on a drawing is an instruction about that garment, never a thing to be drawn into the photograph." + "\n\n" +
	"Fabric: this run states no fabric and no colour. Render the garment in a plain, unpatterned mid-weight cloth of a single neutral colour, and do not invent a print, a texture or a trim the drawings do not show." + "\n\n" +
	"Layout: two views of the SAME garment on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced, with clear white space between them and none of them overlapping — left to right: FRONT, BACK. It is one garment photographed from two angles, not two garments: the same cloth, the same colour and the same lighting in every view." + "\n\n" +
	goldenRenderTail

const goldenOneClothEchoed = "ASK-the words of the person" + "\n\n" +
	"colour:\ncolourway RED-01 — the exact value is #b1121a" + "\n\n" +
	"fabric in words:\nWORDS-brushed, slight sheen" + "\n\n" +
	"references:\n- image 1: current state of the garment — front view\n- image 2: current state of the garment — back view\n- image 3: fabric photograph — the material this garment is made of: read its weave, texture, sheen and drape from here" + "\n\n" +
	"Turn the garment shown in the reference images into a photorealistic photograph of the finished garment in real cloth. The technical drawings among them define the CONSTRUCTION and must be followed exactly — silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, darts, pleats, pockets, closures, waistband, cuffs, hems and bindings. Add nothing they do not show and leave out nothing they do. Each image is described in the reference list above; the markup drawn on a drawing is an instruction about that garment, never a thing to be drawn into the photograph." + "\n\n" +
	"Fabric. The cloth of this garment is stated in more than one way at once, and the statements may disagree. Resolve every disagreement in this fixed order of authority, the same way on every run:\n1. THE FABRIC PHOTOGRAPH (image 3) governs the MATERIAL of this garment: weave or knit structure, surface texture, pile, sheen, transparency, weight and the way the cloth drapes and folds. Read the cloth from that image and from nothing else.\n2. THE STATED COLOUR — the `colour` block above — governs the COLOUR of this garment, and OVERRIDES the colour of the fabric photograph. Where the two disagree, keep the photograph's material and re-colour it: the garment is the stated colour even when the swatch photograph is another one.\n3. THE FABRIC DESCRIPTION IN WORDS — the `fabric in words` block above — adds only what neither the photograph nor the stated colour already says. It never overrides either of them: a word that contradicts the photograph's material or the stated colour is to be ignored." + "\n\n" +
	"Layout: three views of the SAME garment on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced, with clear white space between them and none of them overlapping — left to right: FRONT, SIDE LEFT, BACK. It is one garment photographed from three angles, not three garments: the same cloth, the same colour and the same lighting in every view." + "\n\n" +
	goldenRenderTail

const goldenOneClothNoPhoto = "ASK-the words of the person" + "\n\n" +
	"colour:\ncolourway OLV — the exact value is #4a5a3c" + "\n\n" +
	"fabric in words:\nWORDS-only words" + "\n\n" +
	"references:\n- image 1: current state of the garment — front view\n- image 2: current state of the garment — back view" + "\n\n" +
	"Turn the garment shown in the reference images into a photorealistic photograph of the finished garment in real cloth. The technical drawings among them define the CONSTRUCTION and must be followed exactly — silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, darts, pleats, pockets, closures, waistband, cuffs, hems and bindings. Add nothing they do not show and leave out nothing they do. Each image is described in the reference list above; the markup drawn on a drawing is an instruction about that garment, never a thing to be drawn into the photograph." + "\n\n" +
	"Fabric. The cloth of this garment is stated in more than one way at once, and the statements may disagree. Resolve every disagreement in this fixed order of authority, the same way on every run:\n1. THE STATED COLOUR — the `colour` block above — governs the COLOUR of this garment, and OVERRIDES the colour of the fabric photograph. Where the two disagree, keep the photograph's material and re-colour it: the garment is the stated colour even when the swatch photograph is another one.\n2. THE FABRIC DESCRIPTION IN WORDS — the `fabric in words` block above — adds only what neither the photograph nor the stated colour already says. It never overrides either of them: a word that contradicts the photograph's material or the stated colour is to be ignored." + "\n\n" +
	"Layout: two views of the SAME garment on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced, with clear white space between them and none of them overlapping — left to right: FRONT, BACK. It is one garment photographed from two angles, not two garments: the same cloth, the same colour and the same lighting in every view." + "\n\n" +
	goldenRenderTail

// The six single-cloth submissions the goldens were captured from, EXACTLY as they were captured.
// Four of them predate `fabrics` entirely; the last two carry a one-member list, which is the
// ORDINARY shape of a new single-cloth run — the client repeats that cloth's texture, colour and
// words into the scalars, so the three-source machinery already describes it whole.
var singleClothRuns = []struct {
	name, params, golden string
}{
	{"photograph, picker and words together", `{"views":["front","side_l","back"],"layout":"one","colour":{"code":"RED-01","hex":"#b1121a","words":"WORDS-brushed, slight sheen","fabric_media_id":9}}`, goldenAllThree},
	{"a picked colour and nothing else", `{"views":["front","back"],"layout":"one","colour":{"code":"RED-01","hex":"#b1121a"}}`, goldenColourOnly},
	{"words and nothing else", `{"views":["front"],"layout":"per_view","colour":{"words":"WORDS-heavy melton"}}`, goldenWordsOnly},
	{"no cloth stated at all", `{"views":["front","back"],"layout":"one"}`, goldenNoFabric},
	{"one cloth, echoed into the scalars", `{"views":["front","side_l","back"],"layout":"one","colour":{"code":"RED-01","hex":"#b1121a","words":"WORDS-brushed, slight sheen","fabric_media_id":9,"fabrics":[{"asset_id":4,"name":"main jersey","media_id":9,"colour_code":"RED-01","colour_hex":"#b1121a","words":"WORDS-brushed, slight sheen","parts":"","repeat_mm":0}]}}`, goldenOneClothEchoed},
	{"one cloth with no photograph", `{"views":["front","back"],"layout":"one","colour":{"code":"OLV","hex":"#4a5a3c","words":"WORDS-only words","fabrics":[{"asset_id":5,"name":"main jersey","colour_code":"OLV","colour_hex":"#4a5a3c","words":"WORDS-only words","parts":"body","repeat_mm":40}]}}`, goldenOneClothNoPhoto},
}

// TestOneClothSaysExactlyWhatItSaidBEFORESeveralClothsExisted — THE test of this wave, and the one
// that has to be read before any of the others.
//
// Several cloths is an ADDITION, and the whole of its safety is that a run with one cloth is not
// touched by it. The history stores composed prompts; if the single-cloth wording moves by so much
// as a comma, every future single-cloth run disagrees with every frozen one, and nobody can tell
// afterwards whether a picture came back different because the fabric changed or because we did.
//
// ⚠ THE LAST TWO CASES ARE WHY THIS IS NOT A NO-OP TEST. They carry a `fabrics` list of one — the
// ordinary new submission — and they are held to the wording of a run that had no such field at
// all. That is the assertion that the ≤1 branch really is the old branch and not a re-creation of
// it that happens to look similar today.
func TestOneClothSaysExactlyWhatItSaidBEFORESeveralClothsExisted(t *testing.T) {
	for _, c := range singleClothRuns {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.golden, renderPrompt(t, c.params, renderSlots),
				"the composed prompt of a single-cloth run is frozen in the history and must not move")
		})
	}
}

// TestABlankClothRowIsNotACloth — an «add cloth» button pressed and abandoned leaves a row that
// says nothing, and an id says nothing: `asset_id` is provenance the contract forbids resolving.
// Counting such a row would restructure an ordinary submission around a second cloth the person
// never described, and the model would invent one to fill it.
func TestABlankClothRowIsNotACloth(t *testing.T) {
	withBlank := `{"views":["front","side_l","back"],"layout":"one","colour":{"code":"RED-01","hex":"#b1121a","words":"WORDS-brushed, slight sheen","fabric_media_id":9,"fabrics":[{"asset_id":4,"name":"main jersey","media_id":9,"colour_code":"RED-01","colour_hex":"#b1121a","words":"WORDS-brushed, slight sheen","parts":"","repeat_mm":0},{"asset_id":7}]}}`
	require.Equal(t, goldenOneClothEchoed, renderPrompt(t, withBlank, renderSlots),
		"a row stating nothing but an id is not a second cloth")
}

/* ─────────────────────────── several cloths ─────────────────────────── */

// clothLine pulls CLOTH n's own line out of a composed prompt. The list is one line per cloth
// exactly so that this is possible — for a reader of the stored prompt as much as for a test.
func clothLine(t *testing.T, prompt string, n string) string {
	t.Helper()
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, "CLOTH "+n) {
			return line
		}
	}
	t.Fatalf("the prompt has no line for CLOTH %s:\n%s", n, prompt)
	return ""
}

// twoCloths: a main jersey on the body and sleeves (its swatch is the recipe's own, image 4) and a
// contrast rib on the collar and cuffs (its texture rides in as an extra input, image 3).
//
// THE IMAGE NUMBERS ARE DELIBERATELY OUT OF ORDER — cloth 1 is image 4 and cloth 2 is image 3 —
// because the numbers come off the ATTACHED list and not off the cloth's position. A composer that
// numbered the cloths by their own index would agree with a sequential fixture and be wrong here.
const twoCloths = `{"views":["front","back"],"layout":"one","extra_input_media_ids":[10],` +
	`"colour":{"code":"RED-01","hex":"#b1121a","words":"WORDS-brushed","fabric_media_id":9,"fabrics":[` +
	`{"asset_id":4,"name":"main jersey","media_id":9,"colour_code":"RED-01","colour_hex":"#b1121a","words":"WORDS-brushed","parts":"body, sleeves"},` +
	`{"asset_id":5,"name":"contrast rib","media_id":10,"colour_code":"NVY-02","colour_hex":"#1b2a4a","words":"2x2 rib","parts":"collar, cuffs"}]}}`

// TestTwoClothsAreNamed_Pinned_AndPointedAtTheirOwnPicture — V-8 as one assertion: several cloths,
// each pinned by the marks to its own parts, each pointed at the picture that carries it.
func TestTwoClothsAreNamed_Pinned_AndPointedAtTheirOwnPicture(t *testing.T) {
	got := renderPrompt(t, twoCloths, renderSlots)

	require.Contains(t, got, "This garment is made of two different cloths")

	first, second := clothLine(t, got, "1"), clothLine(t, got, "2")

	// Both cloths are NAMED. An unnamed cloth reaches the model as the bare word «cloth», which is
	// the failure detail slots already went through once.
	require.Contains(t, first, "CLOTH 1 — main jersey.")
	require.Contains(t, second, "CLOTH 2 — contrast rib.")

	// Both cloths carry THEIR OWN PARTS, and each is shut out of everything else. Without the
	// second half the marks say only «this cloth is used here too», and a model hearing that about
	// two cloths puts both of them everywhere.
	require.Contains(t, first, "It is used on: body, sleeves — and on no other part of this garment.")
	require.Contains(t, second, "It is used on: collar, cuffs — and on no other part of this garment.")

	// Both cloths point at the picture that actually carries them — off the attached list, not off
	// their own position, which is why these two numbers are the wrong way round.
	require.Contains(t, first, "Its texture is image 4:")
	require.Contains(t, second, "Its texture is image 3:")
	require.Contains(t, got, "- image 3: additional reference image")
	require.Contains(t, got, "- image 4: fabric photograph")

	// Each cloth's own colour and own words travel with it. Glued together, two cloths would reach
	// the model as one cloth described twice.
	require.Contains(t, first, "Its colour is colourway RED-01 — the exact value is #b1121a.")
	require.Contains(t, second, "Its colour is colourway NVY-02 — the exact value is #1b2a4a.")
	require.Contains(t, second, "In words: 2x2 rib.")

	// And the boundary may not be enlarged: the prohibition is what stops two cloths from becoming
	// contrast cuffs, a contrast placket and a contrast pocket flap.
	require.Contains(t, got, renderClothNoInvention)
	require.Contains(t, got, renderClothPartsRule)
}

// TestAClothWhosePictureDidNotGoOutIsGivenNoImageNumber — the rule the ranked photograph clause has
// always obeyed, now owed to every cloth on the list: telling a model to read a weave off an image
// it was never shown is how a render comes back in an invented fabric. Here the contrast rib's
// media row went away between the snapshot and the pass, so it is not among the pictures.
func TestAClothWhosePictureDidNotGoOutIsGivenNoImageNumber(t *testing.T) {
	r := testRun(1, entity.DesignRunKindRender)
	r.Inputs = entity.RawJSON(renderSlots)
	r.Params = entity.RawJSON(twoCloths)

	// Media 10 — the contrast rib's texture — is deliberately absent from the resolver.
	job, err := buildJob(context.Background(), media(1, 2, 9), r, "medium")
	require.NoError(t, err)
	require.Len(t, job.References, 3, "the two plates and the jersey swatch attach; the rib does not")

	second := clothLine(t, job.Prompt, "2")
	require.NotContains(t, second, "image",
		"a cloth the model cannot see must not be given an image number, nor be sent to look for one")
	require.Contains(t, second, "No photograph of this cloth was sent, and its texture must not be borrowed from another cloth's photograph.",
		"the silence has to be spoken: beside a neighbour that does cite an image, an unmentioned texture is read off the neighbour")

	// The cloth that DID attach still gets its number, and it is the one the captions gave it.
	require.Contains(t, clothLine(t, job.Prompt, "1"), "Its texture is image 3:")
	require.Contains(t, job.Prompt, "- image 3: fabric photograph")
}

// TestAPatternClothStatesItsRepeatInWholeMillimetres — «какого размера располагать этот паттерн»
// (V-7). A number the model can act on, unlike «large»; and measured on the finished garment,
// because a tile photographed close up says nothing about the scale it is printed at.
func TestAPatternClothStatesItsRepeatInWholeMillimetres(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","back"],"layout":"one","colour":{"hex":"#b1121a","fabric_media_id":9,"fabrics":[`+
			`{"name":"main jersey","media_id":9,"colour_hex":"#b1121a","parts":"body"},`+
			`{"name":"floral print","colour_hex":"#efe6d0","parts":"yoke","repeat_mm":120}]}}`,
		renderSlots)

	require.Contains(t, clothLine(t, got, "2"),
		"Its pattern repeats every 120 mm on the finished garment.")
	require.NotContains(t, clothLine(t, got, "1"), "repeats every",
		"a plain cloth has no repeat and must not be given one")
}

// TestTheClothThatNamesNoPartsIsTheRemainder — the contract's own rule, and the one a reader gets
// wrong by instinct: an empty `parts` is not missing data, it is the statement «everything else».
// Dropped, it takes with it exactly the cloth nobody had to mark — usually the main one — and the
// garment comes back made entirely of the contrast.
func TestTheClothThatNamesNoPartsIsTheRemainder(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","back"],"layout":"one","colour":{"hex":"#b1121a","fabric_media_id":9,"fabrics":[`+
			`{"name":"main jersey","media_id":9,"colour_hex":"#b1121a"},`+
			`{"name":"contrast rib","colour_hex":"#1b2a4a","parts":"collar, cuffs"}]}}`,
		renderSlots)

	first := clothLine(t, got, "1")
	require.Contains(t, first, "CLOTH 1 — main jersey.",
		"the cloth that named no parts must still be on the list")
	require.Contains(t, first, "It names no parts, so it is the REMAINDER: every part of the garment that the other cloths on this list do not claim.")
	require.Contains(t, got, renderClothPartsRule, "and the rule that makes that line binding is stated")
	require.Contains(t, clothLine(t, got, "2"), "It is used on: collar, cuffs — and on no other part of this garment.")
}

// TestWhenNobodyMarkedAPartTheDivisionIsTheModelsToMake — the contract calls this legal and says
// what it means: «a run that states two cloths and no parts is telling the model to choose». The
// remainder rule cannot be spoken here — every cloth would be the remainder of every other, which
// is a circle rather than an instruction.
func TestWhenNobodyMarkedAPartTheDivisionIsTheModelsToMake(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","back"],"layout":"one","colour":{"hex":"#b1121a","fabric_media_id":9,"fabrics":[`+
			`{"name":"main jersey","media_id":9,"colour_hex":"#b1121a"},`+
			`{"name":"contrast rib","colour_hex":"#1b2a4a"}]}}`,
		renderSlots)

	require.Contains(t, got, renderClothPartsUnmarked)
	require.NotContains(t, got, renderClothPartsRule,
		"a remainder rule with nothing to be the remainder of is a circle")
	require.NotContains(t, got, "It names no parts, so it is the REMAINDER",
		"neither cloth can be the leftover of the other")
	require.Contains(t, got, "CLOTH 2 — contrast rib.", "both cloths are still listed and still used")
}

// TestSeveralClothsNarrowTheOrderOfAuthorityToTheFirstCloth — the collision this wave could not
// avoid and had to settle IN WORDS.
//
// The order-of-authority clauses are frozen into the history of every single-cloth run, so they
// cannot be reworded — and they say «the COLOUR of this garment» and «render the whole garment in
// exactly that colour». Standing after a list that has just pinned the rib to the collar, that is
// an instruction to overpaint the collar, and it is the LAST word on the subject. So the list ends
// by narrowing what «this garment» means in the paragraph below it.
func TestSeveralClothsNarrowTheOrderOfAuthorityToTheFirstCloth(t *testing.T) {
	got := renderPrompt(t, twoCloths, renderSlots)

	require.Contains(t, got, renderClothFirstIsTheScalar)
	require.Less(t, strings.Index(got, renderClothFirstIsTheScalar), strings.Index(got, authorityHeader),
		"the narrowing has to be read before the paragraph it narrows")
	require.Less(t, strings.Index(got, "CLOTH 1 — main jersey."), strings.Index(got, authorityHeader),
		"the cloth list stands before the order of authority, not after it")

	// The ranking itself is untouched: it still arbitrates the three ways of stating ONE cloth.
	require.Contains(t, got, "1. THE FABRIC PHOTOGRAPH (image 4)")
	require.Contains(t, got, "2. THE STATED COLOUR")
	require.Contains(t, got, "3. THE FABRIC DESCRIPTION IN WORDS")
}

// TestSeveralClothsNeverClaimTheRunStatedNoFabric — the fallback says «this run states no fabric
// and no colour», which is flatly false beside a list naming two of them. It can be reached by a
// writer that filled `fabrics` and left the echo scalars empty; the honest move is to drop the
// sentence that is wrong, not to keep a paragraph for the shape of it.
func TestSeveralClothsNeverClaimTheRunStatedNoFabric(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","back"],"layout":"one","colour":{"fabrics":[`+
			`{"name":"main jersey","colour_hex":"#b1121a","parts":"body"},`+
			`{"name":"contrast rib","colour_hex":"#1b2a4a","parts":"collar"}]}}`,
		renderSlots)

	require.NotContains(t, got, renderNoFabric,
		"two cloths were named: the prompt must not say none were")
	require.Contains(t, got, "CLOTH 2 — contrast rib.")
	require.NotContains(t, got, renderClothFirstIsTheScalar,
		"there is no paragraph below to narrow, so the sentence that narrows it points at nothing")
}
