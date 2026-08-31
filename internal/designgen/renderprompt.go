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

	/* ─── SEVERAL CLOTHS (V-8). The four sentences below are the whole of the requirement in
	   words, and they are constants for the same reason every sentence above is one: they are the
	   part of the prompt a person may want to read, quote or argue with, and a sentence assembled
	   out of fragments at three call sites cannot be read anywhere. ─── */

	// THE READING RULE FOR THE LIST, AND THE POINT OF THE WHOLE WAVE. The owner's ask is not «send
	// two swatches», it is «на флетах показать маркером какая часть какой тканью должна быть» — the
	// marks say WHICH PART. So the list has to be read as an assignment of cloth to part, and the
	// two halves of that assignment are stated here: a cloth that names parts is pinned to them,
	// and a cloth that names none is what is left over.
	//
	// ⚠ «NAMES NONE» IS A STATEMENT, NOT A HOLE. The contract says it outright: an empty `parts`
	// means the whole garment, and among several cloths that is the remainder — never «unknown».
	// A reader that treated it as missing would drop exactly the cloth nobody had to mark, which is
	// almost always the main one, and the render would come back in one contrast fabric.
	renderClothPartsRule = "A cloth that names its parts is used ON THOSE PARTS AND ON NO OTHER PART OF THIS GARMENT; a cloth that names no parts is the REMAINDER — every part of the garment that the other cloths on this list do not claim."

	// The same rule when NOBODY marked anything. The contract calls this legal and says what it
	// means: «a run that states two cloths and no parts is telling the model to choose». Emitting
	// the rule above here would be a sentence about a remainder of nothing — every cloth would be
	// the remainder of every other, which is not an instruction but a circle.
	renderClothPartsUnmarked = "None of these cloths names a part: the drawings carry no mark saying which cloth goes where, so the division is yours to make. Use every cloth on this list, and change cloth only on a seam, a panel edge or a finished edge the drawings actually show."

	// THE PROHIBITION, in the spirit of renderExcluded and for the identical reason: everything a
	// model is not told is a thing it will supply. Given two cloths and no boundary, it invents
	// contrast cuffs, a contrast collar and a contrast placket, because that is what two cloths
	// usually mean in the pictures it was trained on — and none of those are in our drawings.
	//
	// THE SECOND SENTENCE SETTLES A COLLISION WITH renderExcluded, WHICH THIS BLOCK CANNOT EDIT.
	// That list forbids «any colour, print or pattern that neither the fabric photograph nor the
	// stated colour calls for», and it was written when a run had exactly one of each. Read beside
	// a second cloth it would forbid that cloth's own colour — the prompt would be telling the
	// model to use the contrast rib and to leave it out. Two of our own sentences that disagree
	// about the same thing are worse than either alone, so the collision is resolved here, in
	// words, rather than left for the model to resolve differently on every run.
	renderClothNoInvention = "Change cloth only where this list says the cloth changes. Where it does not, the garment is ONE cloth throughout: no extra panel, block, yoke, trim, binding, facing, cuff or collar in a cloth of its own, no cloth beyond the ones listed here, and no colour or pattern on a part that was not given one. Every cloth on this list, with its own colour and its own pattern, IS called for: the exclusion of colours and patterns stated further down removes only what this list does not name."

	// THE SAME PROHIBITION WHEN NOBODY MARKED A PART, AND IT HAS TO BE A SECOND SENTENCE RATHER
	// THAN THE ONE ABOVE.
	//
	// ⚠ THE SENTENCE ABOVE CONTRADICTS THE HEADING IN THIS ONE CASE, AND ONLY IN THIS ONE. It is
	// conditional on the list — «change cloth only where THIS LIST says the cloth changes» — and a
	// list in which no cloth names a part says it NOWHERE. Read together with the heading three
	// lines above it («the division is yours to make. Use every cloth on this list»), the pair
	// tells the model to use both cloths and forbids every place the second one could begin. Two
	// of our own sentences commanding opposite things is the worst thing this file can contain:
	// the model obeys whichever it prefers, and prefers a different one on every run — the
	// run-to-run lottery the whole craft block exists to remove.
	//
	// WHAT IS DROPPED IS ONLY THE CLAUSE THAT FROZE THE GARMENT INTO ONE CLOTH. What survives is
	// the half that is still true and still load-bearing — nothing beyond this list — because that
	// half is what stops two cloths from becoming a contrast collar, a contrast placket and a
	// contrast pocket flap, and it is what settles the collision with renderExcluded that this
	// block cannot edit.
	renderClothNoInventionUnmarked = "Beyond the division you make yourself, invent nothing: no cloth that is not on this list, no extra panel, block, yoke, trim, binding, facing, cuff or collar in a cloth of its own, and no colour or pattern anywhere on this garment that this list does not name. Every cloth on this list, with its own colour and its own pattern, IS called for: the exclusion of colours and patterns stated further down removes only what this list does not name."

	// WHAT THE PARAGRAPH AFTER THE LIST IS ABOUT, once there is more than one cloth. The `colour`
	// block, the `fabric in words` block and the order of authority all speak about ONE cloth —
	// the first — because that is what the contract makes them carry (DesignColourRecipe:
	// «a NEW run states its cloths in `fabrics` and ALSO repeats the first one's texture here»).
	//
	// ⚠ WITHOUT THIS SENTENCE THE PROMPT CONTRADICTS ITSELF IN THE WORST POSSIBLE PLACE. The
	// order-of-authority clauses say «the COLOUR of this garment» and, in their solo form, «render
	// the WHOLE GARMENT in exactly that colour». Beside a list that has just pinned a second cloth
	// to the collar, that is a direct instruction to overpaint the collar — and it is the LAST
	// word on the subject, which is the half a model obeys. The clauses themselves cannot be
	// reworded: they are frozen into the history of every single-cloth run ever composed. So the
	// scope is narrowed here instead, before them, in one sentence that costs nothing when the
	// list is absent because the list is what emits it.
	renderClothFirstIsTheScalar = "The `colour` block and the `fabric in words` block above, and the paragraph that follows this list, all speak about CLOTH 1 and about nothing else: wherever they say `this garment` or `the whole garment`, they mean CLOTH 1 and the parts CLOTH 1 covers, never a part another cloth on this list claims. The order of authority in that paragraph — the photograph, then the stated colour, then the words — settles a disagreement INSIDE one cloth, and it settles it the same way inside every other cloth on this list."
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

/* ─────────────────────────── several cloths ─────────────────────────── */

// statedCloths is the cloths this run actually stated — the frozen list with the rows that say
// NOTHING dropped.
//
// ⚠ A BLANK ROW IS NOT A CLOTH, AND DROPPING IT IS NOT TIDINESS. The whole shape of the fabric
// paragraph turns on the COUNT: one cloth keeps the wording every frozen run already has, two
// rebuild it around a list. A client that left a half-filled row behind — an «add cloth» button
// pressed and abandoned — would otherwise flip an ordinary single-cloth submission into a
// two-cloth prompt whose second member has nothing to say, and the model would dutifully invent a
// second fabric to fill it. A row that states nothing states nothing.
//
// asset_id ALONE DOES NOT COUNT. It is provenance — «which shelf row was this» — and the contract
// is explicit that a reader never resolves it. An entry carrying only an id therefore reaches the
// model as the bare word «cloth», which is the failure detail slots already went through once.
func statedCloths(c *colourRecipe) []fabricUse {
	if c == nil {
		return nil
	}
	out := make([]fabricUse, 0, len(c.Fabrics))
	for _, f := range c.Fabrics {
		stated := f.MediaID > 0 || f.RepeatMM > 0 ||
			oneLine(f.Name) != "" || strings.TrimSpace(f.ColourCode) != "" ||
			strings.TrimSpace(f.ColourHex) != "" || oneLine(f.Words) != "" ||
			oneLine(f.Parts) != ""
		if stated {
			out = append(out, f)
		}
	}
	return out
}

// renderFabricSection is the fabric half of the craft block: the cloth list when there is more than
// one cloth, and the order of precedence.
//
// ⚠ ONE CLOTH TAKES THE OLD PATH LITERALLY, AND THAT IS THE LOAD-BEARING LINE OF THIS WAVE. The
// composed prompt is written into the run's history, so the wording of a single-cloth render is not
// a style choice — it is the sentence every frozen run already says, and the sentence every future
// single-cloth run has to keep saying if the two are ever to be compared. A one-member `fabrics`
// list is the ORDINARY new submission, not an exotic case: the client repeats that cloth's texture
// into `fabric_media_id`, its colour into `code`/`hex` and its words into `words`, exactly as the
// contract instructs, so the existing three-source machinery already describes it completely. So
// the ≤1 branch does not merely produce the same words — it calls the same function, which is the
// only version of «identical» that cannot rot.
func renderFabricSection(f fabricStated, cloths []fabricUse, attached []refCaption) []string {
	if len(cloths) < 2 {
		return []string{renderFabricParagraph(f)}
	}
	lines := renderClothLines(cloths, attached)
	authority := renderFabricParagraph(f)
	if authority == renderNoFabric {
		// THE FALLBACK IS FALSE HERE AND MUST NOT BE SPOKEN. It says «this run states no fabric and
		// no colour» — a flat contradiction of a list that has just named two of them. It can only
		// be reached by a writer that filled `fabrics` and left the echo scalars empty; the list
		// still says everything the model needs, so the honest move is to drop the sentence that
		// is wrong rather than to keep a paragraph for the shape of it.
		return []string{strings.Join(lines, "\n")}
	}
	return []string{strings.Join(append(lines, renderClothFirstIsTheScalar), "\n"), authority}
}

// renderClothLines is the list itself: a heading that states the reading rule, one line per cloth,
// and the prohibition that closes it.
//
// THE RULE COMES BEFORE THE LIST AND THE PROHIBITION AFTER IT, deliberately. A reader — model or
// human — meets «It is used on: collar, cuffs» already knowing that such a line excludes every
// other part; met the other way round, each line first reads as a loose hint and is only narrowed
// afterwards. The prohibition closes because it is the negative half, and the negative half of this
// prompt is written last everywhere else in the file too (renderExcluded).
func renderClothLines(cloths []fabricUse, attached []refCaption) []string {
	anyParts := false
	for _, c := range cloths {
		if oneLine(c.Parts) != "" {
			anyParts = true
			break
		}
	}

	// ⚠ ОБА ПРАВИЛА ВЫБИРАЮТСЯ ОДНИМ И ТЕМ ЖЕ ПРИЗНАКОМ, И ЭТО НЕСУЩЕЕ. Заголовок и закрывающий
	// запрет — две половины ОДНОГО высказывания о том, где ткань меняется. Пока признак у них был
	// один, а закрывающий запрет — безусловный, размеченный вариант заголовка стоял рядом с
	// правилом, которое ему подходит, а неразмеченный — рядом с правилом, которое ему прямо
	// противоречит (см. renderClothNoInventionUnmarked).
	rule, closing := renderClothPartsUnmarked, renderClothNoInventionUnmarked
	if anyParts {
		rule, closing = renderClothPartsRule, renderClothNoInvention
	}
	lines := []string{
		"The cloths of this garment. This garment is made of " + countWord(len(cloths)) +
			" different cloths, and the marks drawn on the drawings say which part is made of which. " +
			rule + " The cloths, in the order they were stated:",
	}
	for i, c := range cloths {
		lines = append(lines, renderClothLine(i+1, c, attached, anyParts))
	}
	return append(lines, closing)
}

// renderClothLine is ONE cloth: what to call it, which picture carries it, what colour it is, what
// was said about it, how big its repeat is, and WHICH PARTS it is for.
//
// ⚠ IT IS «CLOTH 1», NOT «1.», AND THE DIFFERENCE IS NOT COSMETIC. The paragraph that follows this
// list is itself a numbered list — the three ranks of the order of authority — and two numbered
// lists standing one under the other make «2» mean the picked colour in one of them and the
// contrast rib in the other. The cloths are numbered by a word that carries its own noun.
//
// A CLOTH WHOSE PICTURE DID NOT GO OUT IS NOT GIVEN A NUMBER, exactly as the ranked photograph
// clause refuses one: imageNumberOf answers 0 both for «there was never a texture» and for «the
// media row went away between the snapshot and the pass», and from where the model sits those are
// the same fact. But the silence is spoken rather than left blank, on the doctrine fabricSource.solo
// records: a cloth listed beside a neighbour that DOES cite an image, and itself citing none, is a
// cloth whose texture the model will happily read off the neighbour's photograph.
//
// name, words AND parts ARE FLATTENED TO ONE LINE for the reason oneLine exists: these lines are
// one-per-cloth and a newline typed into a cloth's name would write a line of its own into the
// prompt — including, if the person happens to type it, a line that reads like another cloth.
func renderClothLine(n int, c fabricUse, attached []refCaption, anyParts bool) string {
	var b strings.Builder
	b.WriteString("CLOTH " + strconv.Itoa(n))
	if name := oneLine(c.Name); name != "" {
		b.WriteString(" — " + name)
	}
	b.WriteString(".")

	if img := imageNumberOf(attached, c.MediaID); img > 0 {
		b.WriteString(" Its texture is image " + strconv.Itoa(img) +
			": read this cloth's weave or knit structure, surface, sheen and drape from that image.")
	} else {
		b.WriteString(" No photograph of this cloth was sent, and its texture must not be borrowed from another cloth's photograph.")
	}
	if col := colourPhrase(c.ColourCode, c.ColourHex); col != "" {
		b.WriteString(" Its colour is " + col + ".")
	}
	if w := oneLine(c.Words); w != "" {
		b.WriteString(" In words: " + w + ".")
	}
	// THE REPEAT IS A NUMBER IN MILLIMETRES BECAUSE «large» AND «small» ARE NOT INSTRUCTIONS. It is
	// the owner's own «какого размера располагать этот паттерн», and it is measured ON THE FINISHED
	// GARMENT rather than on the swatch — a tile photographed close up says nothing about the scale
	// it is printed at, which is exactly why the number was put on the wire.
	if c.RepeatMM > 0 {
		b.WriteString(" Its pattern repeats every " + strconv.Itoa(c.RepeatMM) + " mm on the finished garment.")
	}

	switch parts := oneLine(c.Parts); {
	case parts != "":
		b.WriteString(" It is used on: " + parts + " — and on no other part of this garment.")
	case anyParts:
		b.WriteString(" It names no parts, so it is the REMAINDER: every part of the garment that the other cloths on this list do not claim.")
	}
	return b.String()
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

	// THE FABRIC HALF IS SPLICED IN AS A SLICE, not written as one more element, because with
	// several cloths it is TWO paragraphs — the list and the order of precedence — and with one it
	// is the single paragraph it has always been. A section that decides its own paragraph count
	// keeps the ≤1 case producing a slice identical to the one this literal used to hold.
	paras := []string{renderIntro(len(attached))}
	paras = append(paras, renderFabricSection(stated, statedCloths(p.Colour), attached)...)
	paras = append(paras,
		renderLayoutParagraph(p.Views, detailNames, p.Layout),
		renderStyle,
		renderMaterial,
		renderExcluded,
		renderOutput,
	)
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
	return colourPhrase(c.Code, c.Hex)
}

// colourPhrase is the ONE writer of «what a stated colour reads like», shared by the run's own
// colour block and by every cloth on the cloth list.
//
// IT IS SHARED RATHER THAN COPIED FOR THE REASON THE COMMENT ABOVE ALREADY PAID FOR ONCE. The
// name-and-exact-value shape is not formatting, it is the resolution of a disagreement between a
// dictionary code and a typed hex; a second copy of that shape, written beside the cloth list,
// would be a second place for that resolution to drift — and the drift would show up as two cloths
// on one prompt describing their colours by two different rules.
func colourPhrase(code, hex string) string {
	code, hex = strings.TrimSpace(code), strings.TrimSpace(hex)
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
