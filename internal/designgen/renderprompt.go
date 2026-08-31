package designgen

import (
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// This file is the CRAFT of the render route: the words that turn "the model coloured something in"
// into the picture the owner actually asked for — a photorealistic garment in real cloth, built on
// top of the marked-up flats of this card.
//
// WHY IT EXISTS AT ALL. Until now the owner had supplied reference prompts for FLATS only, and
// flatprompt.go says so in as many words ("FLAT ONLY: the owner supplied these reference prompts
// for flats"). A render therefore left with the bare human context — the ask, the garment note, the
// fit, the captions — and every question the picture actually turns on was left to the model to
// answer for itself: is there a body in it, is there a hanger, is the background white, is the row
// one garment or three, and which of the fabric statements wins when they disagree. A model that
// answers those itself answers them DIFFERENTLY EACH RUN, which is what "затюнить наш промпт" is
// about: not a nicer sentence, but the removal of the run-to-run lottery.
//
// THE OWNER'S SAMPLE, DESCRIBED (round 4, §2). Three views of one white vest in a row on pure
// white: front, side, back. The garment is shown AS IF WORN — bust and waist volume, soft folds,
// the cloth's own shadows on itself — but there is no person, no mannequin and no hanger. The
// material reads as knit: slightly sheer, finished edges, soft seams. Even light, no shadow on the
// background. So the target is a photograph of cloth, standing in empty space, whose CONSTRUCTION
// is the flat it was built from.
//
// WHAT THIS FILE IS NOT. It is not a second opinion about the flats: the drawings arrive as images
// with captions (snapshot.go) and the craft only says what to do with them. And it is not the
// layout authority either — which views and how many canvases come from the frozen params, exactly
// as on the flat route, because both are read back by the splitter through the store's
// compositeViewsOf.
const (
	// The look. "As if worn" is the whole of the owner's sample in three words, and the three
	// negations after it are what stop a model from reaching for the nearest way to produce
	// volume — a body, a dress form, a hanger — every one of which is a picture we cannot use.
	renderStyle = "Style: photorealistic product photograph of the finished garment in real cloth, shown AS IF WORN — the fabric holds the volume of a body underneath it: chest, waist and hip shaping, sleeves and straps carrying their own weight, soft natural folds where the cloth falls, and the garment's own soft self-shadow inside those folds. There is no person, no body part, no mannequin, no dress form, no hanger and no visible support of any kind: the garment holds its shape in empty space. Even, soft, diffuse frontal studio light; no cast shadow on the background, no hot highlights, no vignette."

	// The material. A render whose cloth does not read is a flat that was merely filled in, which
	// is the failure this paragraph is aimed at.
	renderMaterial = "The material must read as real cloth: the weave or knit structure visible at close range, the fibre's own sheen or matte finish, sheer or open fabric slightly translucent where it overlaps itself, hems, bindings and edges finished the way a sewn edge is finished, seams and topstitching soft and pressed rather than drawn as lines."

	// The exclusions. Everything here has been seen on a generated render at least once: a torso
	// under the vest, a wooden hanger, a studio floor, a caption baked into the pixels.
	renderExcluded = "Strictly excluded: any human body or body part, skin, hair, face, hands, mannequin, dress form, bust, hanger, rail, stand or clip; background objects, props, furniture, floor, wall, horizon line or scenery of any kind; drop shadow or reflection on the background; text, labels, watermarks, logos, measurements, callouts, arrows, dimension lines or drawn outlines of any kind; any colour, print or pattern that neither the fabric photograph nor the stated colour calls for."

	renderOutput = "Output: high resolution, sharp focus across the whole garment, seamless pure white background, true colour, e-commerce product photography aesthetic."

	// The no-fabric fallback. The client's own gate refuses a render with no colour stated, so this
	// is the path of a run launched by something that is not that screen — an older client, a
	// script, a recalled snapshot. Guessing a colour would be worse than naming the guess.
	renderNoFabric = "Fabric: this run states no fabric and no colour. Render the garment in a plain, unpatterned mid-weight cloth of a single neutral colour, and do not invent a print, a texture or a trim the drawings do not show."
)

/* ─────────────────────────── the order of precedence ─────────────────────────── */

// fabricSource is one of the three ways a person may state the cloth. The owner asked for all three
// AT ONCE («можно комбинировать»), which is precisely what makes an order of precedence necessary:
// a blue swatch photograph beside a red picker is not a typo to be validated away, it is two
// statements about one garment, and something has to decide which one the model obeys.
type fabricSource struct {
	// clause is the RANKED sentence — the one that goes into the prompt when this run states the
	// cloth in more than one way and the ranking has real work to do.
	clause func(fabricStated) string
	// solo is the same source's sentence when it is THE ONLY ONE THIS RUN CARRIES.
	//
	// ⚠ IT EXISTS BECAUSE A RANK IS A STATEMENT ABOUT ITS NEIGHBOURS, AND ALONE IT HAS NONE. The
	// ranked clauses are written subordinate on purpose — rank 3 says «adds only what neither the
	// photograph nor the stated colour already says. It never overrides either of them». Emitted
	// alone, that produced a prompt whose ONLY sentence about the cloth subordinated itself to two
	// things the prompt never mentioned: a person who described the fabric in words and nothing
	// else was told, in effect, to defer to nothing — and the model was left with no positive
	// instruction about the material at all. A clause standing alone must stand affirmatively.
	//
	// Nil means the ranked wording is ALREADY affirmative and needs no second form (rank 1 governs
	// the material outright and mentions nobody).
	solo func(fabricStated) string
}

// fabricStated is what this particular run said about the cloth — used to skip the clauses whose
// source is absent, and to name the swatch by its image number.
type fabricStated struct {
	photoImage int    // 1-based index of the fabric photograph among the attached pictures; 0 = none
	colour     string // the colourway code / hex actually written into the `colour` block above
	words      string // the free description written into the `fabric in words` block above
}

func (f fabricStated) hasPhoto() bool  { return f.photoImage > 0 }
func (f fabricStated) hasColour() bool { return strings.TrimSpace(f.colour) != "" }
func (f fabricStated) hasWords() bool  { return strings.TrimSpace(f.words) != "" }

// ⚠ THE ORDER OF THIS SLICE IS THE ORDER OF AUTHORITY, AND IT IS THE WHOLE POINT OF THE FILE.
//
// Photograph → picker → words, and each clause says out loud what it governs and what it may not
// touch:
//
//  1. THE PHOTOGRAPH GOVERNS THE MATERIAL. It is the only input that carries weave, pile, sheen,
//     transparency and drape; no hex and no adjective can state those.
//  2. THE PICKER GOVERNS THE COLOUR AND BEATS THE PHOTOGRAPH ON COLOUR. A person who opens a colour
//     picker and chooses a value has made a deliberate, exact statement; the swatch's colour is
//     incidental to why the swatch was attached (it was attached for its cloth). So the texture is
//     kept and re-coloured.
//  3. THE WORDS FILL THE GAPS AND OVERRIDE NOTHING. Prose is the vaguest of the three and the only
//     one that can be written without looking at anything, so it may add ("brushed", "slight
//     sheen") but never contradict.
//
// WHY THE RULE IS IN THE PROMPT TEXT AND NOT ONLY IN THIS CODE. Code can pick which fields travel;
// it cannot pick what the model does with two fields that disagree once both have travelled. If the
// order lives only here, the model resolves the conflict itself — and does it differently on
// Tuesday than it did on Monday, which is exactly the complaint. Written into the prompt, the same
// conflict resolves the same way on every run, and a person reading the stored prompt can see why
// the picture came back the colour it did.
var renderFabricAuthority = []fabricSource{
	{clause: func(f fabricStated) string {
		return "THE FABRIC PHOTOGRAPH (image " + strconv.Itoa(f.photoImage) + ") governs the MATERIAL of this garment: weave or knit structure, surface texture, pile, sheen, transparency, weight and the way the cloth drapes and folds. Read the cloth from that image and from nothing else."
	}},
	{
		clause: func(fabricStated) string {
			return "THE STATED COLOUR — the `colour` block above — governs the COLOUR of this garment, and OVERRIDES the colour of the fabric photograph. Where the two disagree, keep the photograph's material and re-colour it: the garment is the stated colour even when the swatch photograph is another one."
		},
		solo: func(fabricStated) string {
			return "THE STATED COLOUR — the `colour` block above — is the colour of this garment: render the whole garment in exactly that colour, and do not shift it towards a more photogenic neighbour. This run states no fabric photograph and no description in words, so choose a plain, unpatterned mid-weight cloth in that colour and invent no print, no texture and no trim the drawings do not show."
		},
	},
	{
		clause: func(fabricStated) string {
			return "THE FABRIC DESCRIPTION IN WORDS — the `fabric in words` block above — adds only what neither the photograph nor the stated colour already says. It never overrides either of them: a word that contradicts the photograph's material or the stated colour is to be ignored."
		},
		solo: func(fabricStated) string {
			return "THE FABRIC DESCRIPTION IN WORDS — the `fabric in words` block above — is the only statement this run makes about the cloth, and it governs the material outright: build the weave or knit, the weight, the surface and the drape from those words. Where they do not say, choose the plainest reading of them and invent no print, no pattern and no trim the drawings do not show."
		},
	},
}

// present reports, for each entry of renderFabricAuthority IN ITS OWN ORDER, whether this run
// carries that source. Kept beside the slice so the two cannot drift: adding a fourth way to state
// cloth means adding one clause and one predicate, in the same place, in the same order.
func renderFabricPresence(f fabricStated) []bool {
	return []bool{f.hasPhoto(), f.hasColour(), f.hasWords()}
}

// renderFabricParagraph is the order of precedence, written for THIS run: only the sources it
// actually carries, numbered in the standing order of authority.
//
// THE CONFLICT SENTENCE IS CONDITIONAL AND THE ORDER IS NOT. With one source there is nothing to
// resolve, so the header that announces a resolution would be noise — but the surviving clause is
// still the same clause, saying the same thing about what that source governs. With two or more,
// the header is the instruction that makes the ranking binding rather than decorative.
func renderFabricParagraph(f fabricStated) string {
	presence := renderFabricPresence(f)
	var carried []fabricSource
	for i, src := range renderFabricAuthority {
		if i < len(presence) && presence[i] {
			carried = append(carried, src)
		}
	}
	switch len(carried) {
	case 0:
		return renderNoFabric
	case 1:
		// ОДИНОКИЙ КЛОЗ ГОВОРИТСЯ УТВЕРДИТЕЛЬНО, а не в ранговой форме: см. fabricSource.solo.
		// Ранжировать здесь нечего — «порядок старшинства» из одного члена это не порядок.
		if solo := carried[0].solo; solo != nil {
			return "Fabric. " + solo(f)
		}
		return "Fabric. " + carried[0].clause(f)
	}
	clauses := make([]string, 0, len(carried))
	for _, src := range carried {
		clauses = append(clauses, src.clause(f))
	}
	lines := make([]string, 0, len(clauses)+1)
	lines = append(lines,
		"Fabric. The cloth of this garment is stated in more than one way at once, and the statements may disagree. Resolve every disagreement in this fixed order of authority, the same way on every run:")
	for i, c := range clauses {
		lines = append(lines, strconv.Itoa(i+1)+". "+c)
	}
	return strings.Join(lines, "\n")
}

/* ─────────────────────────── the block ─────────────────────────── */

// renderCraft assembles the craft block for one render run: intro, the fabric authority, the layout
// paragraph built from the frozen params, then style, material, exclusions and output.
//
// `attached` IS THE PICTURES ACTUALLY GOING OUT, in the order they go out — the same slice the
// caption block is numbered off (see composePrompt). It is taken whole rather than as a count
// because the fabric clause names the swatch BY ITS IMAGE NUMBER, and a number computed against any
// other list would point the model at somebody else's picture. A swatch whose media row vanished
// between the snapshot and the pass is therefore not in this slice, and its clause is not written:
// telling a model to read the cloth off an image it was never shown is how a render comes back in
// an invented fabric.
func renderCraft(p runParams, detailNames []string, attached []refCaption) string {
	stated := fabricStated{}
	if c := p.Colour; c != nil {
		stated.photoImage = imageNumberOf(attached, c.FabricMediaID)
		stated.colour = colourStatement(c)
		stated.words = c.Words
	}

	paras := []string{
		renderIntro(len(attached)),
		renderFabricParagraph(stated),
		renderLayoutParagraph(p.Views, detailNames, p.Layout),
		renderStyle,
		renderMaterial,
		renderExcluded,
		renderOutput,
	}
	return strings.Join(paras, "\n\n")
}

// renderIntro opens on what the run actually has. A render is normally built ON the flats of the
// card, and when there are none it is built on the description — the same distinction flatIntro
// draws, for the same reason: "true to the drawings" is a lie when there are no drawings.
func renderIntro(refs int) string {
	switch {
	case refs == 1:
		return "Turn the garment shown in the reference image into a photorealistic photograph of the finished garment in real cloth. The reference defines the CONSTRUCTION and must be followed exactly — silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, darts, pleats, pockets, closures, waistband, cuffs, hems and bindings. Add nothing it does not show and leave out nothing it does."
	case refs > 1:
		return "Turn the garment shown in the reference images into a photorealistic photograph of the finished garment in real cloth. The technical drawings among them define the CONSTRUCTION and must be followed exactly — silhouette, proportions, neckline, collar, sleeves, straps, cut-outs, seam lines, darts, pleats, pockets, closures, waistband, cuffs, hems and bindings. Add nothing they do not show and leave out nothing they do. Each image is described in the reference list above; the markup drawn on a drawing is an instruction about that garment, never a thing to be drawn into the photograph."
	default:
		return "Render the garment described above as a photorealistic photograph of the finished garment in real cloth."
	}
}

// renderLayoutParagraph is the render's own answer to the question flatLayoutParagraph answers for
// drawings: which views, how many, and on how many canvases.
//
// ⚠ LEFT-TO-RIGHT ORDER IS params.views ORDER, FOR THE REASON GIVEN IN flatprompt.go AND IT APPLIES
// HERE WORD FOR WORD. The store's compositeViewsOf is kind-agnostic: a render with layout=one and
// two or more views has its p.Views recorded VERBATIM as "what is glued into this image", and the
// splitter labels the cut frames from that record. Re-sorting the views into the canonical order
// here would hand back a sheet whose frames are systematically mislabeled, and the owner's own
// answer for this route is a sheet («три вида в одной картинке… в слоты кладётся уже после
// разреза»), so the sheet case is the normal one and not the exotic one.
//
// THE "ONE GARMENT" SENTENCE IS NOT DECORATION. A row of three views is three chances for a model
// to drift — a slightly different neckline, a slightly different white, a slightly different light
// — and three views of three garments cannot be cut into three slots of one.
//
// THE DETAIL FRAMES ARE NAMED HERE TOO, through the same displayViews the flat route uses. A render
// run may carry detail views like any other, and «DETAIL, DETAIL» in a left-to-right list is the
// same unusable instruction on this route as on that one.
func renderLayoutParagraph(views, detailNames []string, layout string) string {
	names := displayViews(views, detailNames)

	switch {
	case len(views) == 0:
		return "Layout: the garment photographed once, isolated and centred on the canvas."
	case len(views) == 1:
		return "Layout: a single view — " + names[0] + " — the garment photographed once, isolated and centred on the canvas."
	case layout == layoutPerView:
		return "Layout: one photograph out of a set of " + countWord(len(views)) +
			", each view of the same garment as its own image — " + strings.Join(names, ", ") +
			", one view per image. This image shows exactly one of those views, the garment isolated and centred on the canvas. It is ONE garment across the whole set: the same cloth, the same colour, the same lighting and the same scale in every image."
	default:
		return "Layout: " + countWord(len(views)) +
			" views of the SAME garment on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced, with clear white space between them and none of them overlapping — left to right: " +
			strings.Join(names, ", ") +
			". It is one garment photographed from " + countWord(len(views)) +
			" angles, not " + countWord(len(views)) +
			" garments: the same cloth, the same colour and the same lighting in every view."
	}
}

/* ─────────────────────────── the two small readers ─────────────────────────── */

// imageNumberOf finds a media id among the pictures that ACTUALLY ATTACHED and answers with the
// same 1-based number the caption block uses for it. 0 means "this picture is not going out", which
// is a different answer from "it was never asked for" and is treated the same way on purpose: in
// both cases the model cannot see it.
func imageNumberOf(attached []refCaption, mediaID int) int {
	if mediaID <= 0 {
		return 0
	}
	for i, rc := range attached {
		if rc.MediaID == mediaID {
			return i + 1
		}
	}
	return 0
}

// colourStatement is what the `colour` block above actually says — the code and the hex, and NOT
// the free words, which are their own block and their own rank in the order of authority. Keeping
// the two apart in the prompt is what lets the precedence rule address them separately; glued into
// one comma-joined line, "the stated colour" and "the words" would be one clause the model has to
// take apart itself, and rank 2 and rank 3 would collapse into each other.
// ⚠ THE CODE AND THE HEX ARE ALSO TWO STATEMENTS, AND THE SECOND ONE WINS. A dictionary colour
// arrives as a pair — the code the humans use and the hex that code stands for — but a person may
// then type a different hex, and that typed value IS a deliberate deviation from the code (there is
// no other way to produce one on the screen). Joined with a bare comma, «colourway OLV, #b1121a»
// left the model to decide whether it was being told olive or red, which is the same run-to-run
// lottery the order of precedence exists to end, one level down. So the pair is written as a name
// and an exact value rather than as a list.
func colourStatement(c *colourRecipe) string {
	if c == nil {
		return ""
	}
	code, hex := strings.TrimSpace(c.Code), strings.TrimSpace(c.Hex)
	switch {
	case code != "" && hex != "":
		return "colourway " + code + " — the exact value is " + hex
	case code != "":
		return "colourway " + code
	default:
		return hex
	}
}

// renderIsTheKind keeps the one kind check in one place, beside the words it lets through.
func renderIsTheKind(kind string) bool { return kind == entity.DesignRunKindRender }
