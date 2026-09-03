package designgen

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// THE ORDER OF PRECEDENCE, SPELLED HERE AS ITS OWN LITERALS, for the reason flatprompt_test.go
// gives about the owner's paragraphs: a test that compared against the production constants would
// have both sides of the assertion rewritten by the same edit, and any reordering or softening of
// the rule would pass in silence. These copies ARE the rule; the code is held to them.
const (
	authorityHeader = "Resolve every disagreement in this fixed order of authority, the same way on every run:"

	authorityPhotoGoverns  = "governs the MATERIAL of this garment"
	authorityColourGoverns = "governs the COLOUR of this garment, and OVERRIDES the colour of the fabric photograph"
	authorityWordsYield    = "It never overrides either of them"

	// H-13: the three parts the ranked words clause gained — the GATE, and the two things that live
	// inside it — spelled here as their own literals for the same reason as everything above them.
	//
	// ⚠ THE GATE IS ITS OWN NEEDLE BECAUSE THE POINT IS WHERE IT STANDS, NOT THAT IT EXISTS. The
	// grant and the imperative are true only when this run carries no fabric photograph; a clause
	// that stated them first and took them back afterwards would contain every literal below and
	// still be an order the model may obey against a photograph. So the assertions are about the
	// gate's POSITION relative to the verb (TestTheImperativeIsGatedNotRetracted), not about the
	// presence of three strings.
	//
	// ⚠ wordsBuildTheCloth SHARES ITS VOCABULARY WITH THE SOLO CLAUSE AND DIFFERS IN ITS VERB —
	// solo says «build the weave or knit … from those words», the ranked form says «… are to be
	// built from those words». The needle is unique to the ranked clause, but only by that verb, so
	// every test that uses it also proves the solo form did NOT fire (authoritySoloWords below). A
	// needle that the other branch can satisfy is a sentinel standing at the wrong door.
	wordsNoPhotographGate = "WHEN THIS RUN CARRIES NO FABRIC PHOTOGRAPH, nothing ranked above this clause states the material at all"
	wordsGovernMaterial   = "a stated colour states COLOUR and nothing else — not how sheer or opaque the cloth is, not its weight, not its hand, not its drape — so a word about any of those adds rather than contradicts"
	wordsBuildTheCloth    = "the weave or knit, the weight, the surface and the drape of this garment are to be built from those words"
	wordsYieldToThePhoto  = "where a fabric photograph is ranked above this clause it states the transparency, the weight and the drape as surely as it states the weave, so against a photograph a word about any of those is description and not instruction"

	authoritySoloWords = "is the only statement this run makes about the cloth"
)

// renderPrompt builds a render run over frozen params/inputs and returns the words that would go
// out, with every snapshot picture resolving. Tests that need an unresolvable picture go through
// buildJob with a faked resolver instead.
func renderPrompt(t *testing.T, params, inputs string) string {
	t.Helper()
	r := testRun(1, entity.DesignRunKindRender)
	r.Params = entity.RawJSON(params)
	r.Inputs = entity.RawJSON(inputs)
	r.Ask = sql.NullString{String: "ASK-the words of the person", Valid: true}
	p, in := parseParams(r.Params), parseInputs(r.Inputs)
	return composePrompt(r, p, in, referenceList(r.Kind, p, in))
}

// One bench plate and one fabric swatch: the ordinary shape of a render submitted from the studio.
const renderSlots = `{"slots":[{"view_key":"front","media_id":1},{"view_key":"back","media_id":2}]}`

// TestRenderStatesTheOrderOfPrecedenceWhenAllThreeAreGiven — THE test of this wave.
//
// The owner allowed the three ways of stating cloth to be COMBINED («можно комбинировать»), which
// is exactly what makes them able to contradict each other: a blue swatch photograph beside a red
// picker is two statements about one garment. If the ranking between them lives only in our code,
// the model resolves the contradiction itself and resolves it differently from run to run. So the
// assertion is not "the fields travelled" — it is that the RULE IS IN THE WORDS, complete, and in
// the one order it is allowed to be in.
func TestRenderStatesTheOrderOfPrecedenceWhenAllThreeAreGiven(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","side_l","back"],"layout":"one","colour":{"code":"RED-01","hex":"#b1121a","words":"WORDS-brushed, slight sheen","fabric_media_id":9}}`,
		renderSlots)

	require.Contains(t, got, authorityHeader,
		"with three sources given, the prompt must announce that a fixed ranking settles their disagreements")

	photoAt := strings.Index(got, authorityPhotoGoverns)
	colourAt := strings.Index(got, authorityColourGoverns)
	wordsAt := strings.Index(got, authorityWordsYield)
	require.GreaterOrEqual(t, photoAt, 0, "the photograph's clause must be in the prompt")
	require.GreaterOrEqual(t, colourAt, 0, "the picked colour's clause must be in the prompt")
	require.GreaterOrEqual(t, wordsAt, 0, "the words' clause must be in the prompt")

	require.Less(t, photoAt, colourAt,
		"the photograph ranks first — it is the only input that can state a weave")
	require.Less(t, colourAt, wordsAt,
		"the picked colour ranks second and the free words last; words may add, never override")

	// The ranks are NUMBERED in the prompt, and the numbers are what a model reads as the order.
	// Asserting the relative positions alone would pass a build that emitted the clauses in the
	// right sequence under the wrong numbers.
	require.Contains(t, got, "1. THE FABRIC PHOTOGRAPH")
	require.Contains(t, got, "2. THE STATED COLOUR")
	require.Contains(t, got, "3. THE FABRIC DESCRIPTION IN WORDS")

	// …and the three sources are all actually present as material for the rule to arbitrate over.
	// The code and the hex are a NAME and an EXACT VALUE, not a comma-joined pair: a typed hex is
	// the only way to deviate from a dictionary code, so the typed value has to be the binding one.
	require.Contains(t, got, "colour:\ncolourway RED-01 — the exact value is #b1121a")
	require.Contains(t, got, "fabric in words:\nWORDS-brushed, slight sheen")
}

// rankLine pulls ONE numbered rank out of a composed prompt. The order of authority is written one
// rank per line exactly so that this is possible — for a reader of a stored prompt as much as for a
// test, and the assertions below are about WHERE things stand inside a rank, which needs the rank.
func rankLine(t *testing.T, prompt, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	require.FailNowf(t, "no such rank", "the prompt carries no line beginning %q", prefix)
	return ""
}

// sentenceWith returns the sentence of `text` that contains `needle`, bounded by full stops.
//
// ⚠ A SENTENCE IS THE UNIT AN INSTRUCTION IS OBEYED IN, WHICH IS WHY THE TESTS BELOW MEASURE ONE.
// «Contains(gate) && Contains(verb)» would pass a clause that commanded first and withdrew two
// sentences later — the exact shape this wave was sent back to fix. What has to be true is that the
// verb and its condition are read together.
func sentenceWith(t *testing.T, text, needle string) string {
	t.Helper()
	i := strings.Index(text, needle)
	require.GreaterOrEqualf(t, i, 0, "the text does not contain %q at all", needle)
	start := 0
	if cut := strings.LastIndex(text[:i], ". "); cut >= 0 {
		start = cut + 2
	}
	end := len(text)
	if cut := strings.Index(text[i:], ". "); cut >= 0 {
		end = i + cut + 1
	}
	return text[start:end]
}

// TestAStatedColourDoesNotOutrankAStatedClothProperty — H-13, and the case the studio actually
// submits.
//
// The owner asked to be able to say «что ткань полупрозрачная например или имеет примерно такую
// грамматуру» ON THE RUN, and the studio composes that into the `fabric in words` block. But those
// words practically never arrive alone: a colourway fills `code` and `hex`, so the run carries two
// sources and the words get the RANKED clause, not the affirmative solo one.
//
// ⚠ THIS IS THE TEST THAT SAYS THE RANKED CLAUSE IS NOT A PURE RESTRICTION. Written as one — «adds
// only what neither … already says … is to be ignored» — the sole imperative aimed at the words was
// a prohibition, and a prohibition builds nothing: the feature travelled the whole wire and did
// nothing at the far end. A colour states COLOUR, so transparency and grammage cannot contradict
// it; they are the «adds» case, and the clause has to say so with a verb.
func TestAStatedColourDoesNotOutrankAStatedClothProperty(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front"],"layout":"one","colour":{"code":"OLV","hex":"#4a5a3c","words":"semi-sheer, about 180 g/m²"}}`,
		renderSlots)

	// The two positive controls. Without them a green run could mean «the ranked branch was never
	// entered» rather than «the ranked branch says this».
	require.Contains(t, got, authorityHeader,
		"colour + words is two sources: this run must be in the ranked branch")
	require.NotContains(t, got, authoritySoloWords,
		"the affirmative SOLO clause must not be what is being read here")

	require.Contains(t, got, wordsNoPhotographGate,
		"the run carries no swatch, and the clause has to SAY that nothing above it states the material")
	require.Contains(t, got, wordsGovernMaterial,
		"a colour cannot state transparency or weight, so words about them add rather than contradict")
	require.Contains(t, got, wordsBuildTheCloth,
		"the ranked clause must tell the model to BUILD the cloth from those words, not merely permit them")

	// …and the limit is intact. The prohibition is the half that keeps the ranking binding, and it
	// is quoted here so that a future edit cannot buy the affirmative half by spending this one.
	require.Contains(t, got, authorityWordsYield)
	require.Contains(t, got,
		"a word that contradicts the photograph's material or the stated colour is to be ignored")

	// The words themselves travelled, whole, in the block the clause names.
	require.Contains(t, got, "fabric in words:\nsemi-sheer, about 180 g/m²")
}

// TestTheImperativeIsGatedNotRetracted — THE guard this wave was sent back for, and the one the
// three needles above cannot supply between them.
//
// A photograph ranks first because it is the only input that carries transparency, weight and
// drape. The first shape of this edit granted those to the words and commanded «build … from them»
// with no condition at all, and only afterwards said a photograph takes them back. As prose it
// scanned. As an INSTRUCTION, standing under a rank 1 that says «Read the cloth from that image and
// from nothing else», it was an order followed by its own retraction — and a model that obeys the
// order and stops reading returns sheer cloth against a photograph of melton.
//
// So the assertion is POSITIONAL and it is made on the configuration where getting it wrong costs a
// picture: with a photograph attached, the verb and the grant must both be read INSIDE the sentence
// that makes them conditional. Contains() cannot see this; only the sentence can.
func TestTheImperativeIsGatedNotRetracted(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","back"],"layout":"one","colour":{"code":"OLV","hex":"#4a5a3c","words":"sheer, about 40 g/m²","fabric_media_id":9}}`,
		renderSlots)

	// Positive control: this really is the three-source paragraph, and rank 1 really is a photograph
	// that the clause under test would be overriding if it fired.
	require.Contains(t, got, "1. THE FABRIC PHOTOGRAPH (image 3) governs the MATERIAL of this garment")
	require.Contains(t, got, "Read the cloth from that image and from nothing else.")

	words := rankLine(t, got, "3. THE FABRIC DESCRIPTION IN WORDS")

	commanded := sentenceWith(t, words, wordsBuildTheCloth)
	require.Contains(t, commanded, wordsNoPhotographGate,
		"the order to build the cloth from the words must be read together with the condition that "+
			"makes it true; a command in one sentence and its condition in another is a command")
	require.Less(t, strings.Index(commanded, wordsNoPhotographGate), strings.Index(commanded, wordsBuildTheCloth),
		"the condition must stand BEFORE the verb: a model reading forward has to meet it first")

	granted := sentenceWith(t, words, wordsGovernMaterial)
	require.Contains(t, granted, wordsNoPhotographGate,
		"«adds rather than contradicts» is false against a photograph and must be gated with the verb")
}

// TestAPhotographStillOutranksAStatedClothProperty — the other side of the same clause, and the one
// that must NOT move.
//
// «sheer» typed beside a photograph of an opaque melton is a contradiction and stays advice. This
// holds the boundary from the other end: rank 1 is untouched, and rank 3 names the photograph as
// the thing that keeps those properties.
func TestAPhotographStillOutranksAStatedClothProperty(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","back"],"layout":"one","colour":{"code":"OLV","hex":"#4a5a3c","words":"sheer, about 40 g/m²","fabric_media_id":9}}`,
		renderSlots)

	require.Contains(t, got, wordsYieldToThePhoto,
		"with a photograph ranked above, a stated cloth property is description and not instruction")

	// The yielding half belongs to rank 3 and not to some paragraph of its own: it is the same
	// clause that grants the words their material, or the two could be edited apart.
	words := rankLine(t, got, "3. THE FABRIC DESCRIPTION IN WORDS")
	require.Contains(t, words, wordsYieldToThePhoto)
	require.Contains(t, words, wordsNoPhotographGate)
	require.Less(t, strings.Index(words, wordsYieldToThePhoto), strings.Index(words, wordsNoPhotographGate),
		"the photograph keeps the material before the gated grant is ever reached")
}

// TestRenderNamesTheSwatchByTheImageNumberTheCaptionGaveIt — the rule says "the fabric photograph"
// and the model has to know WHICH picture that is. The number is taken off the attached list, the
// same list the caption block is numbered from, so «image k» in the rule and «- image k:» in the
// captions are the same picture BY CONSTRUCTION.
func TestRenderNamesTheSwatchByTheImageNumberTheCaptionGaveIt(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","back"],"layout":"one","colour":{"hex":"#b1121a","fabric_media_id":9}}`,
		renderSlots)

	// Two bench plates come first (front, back), so the swatch is the third picture out.
	require.Contains(t, got, "THE FABRIC PHOTOGRAPH (image 3)")
	require.Contains(t, got,
		"- image 3: fabric photograph — the material this garment is made of: read its weave, texture, sheen and drape from here")
}

// TestRenderWithoutASwatchDoesNotRankOne — a run stating only a colour has nothing to disagree
// with, so the announcement of a ranking and the clause about a photograph would both point at
// nothing. Telling a model to read a weave off an image it was never shown is how a render comes
// back in an invented fabric — the same defect flatIdentifyGarmentNoRef closes on the flat route.
func TestRenderWithoutASwatchDoesNotRankOne(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","back"],"layout":"one","colour":{"code":"RED-01","hex":"#b1121a"}}`,
		renderSlots)

	require.NotContains(t, got, authorityHeader, "one source cannot disagree with itself")
	require.NotContains(t, got, "THE FABRIC PHOTOGRAPH",
		"no swatch was given: the prompt must not speak about one")
	require.NotContains(t, got, "THE FABRIC DESCRIPTION IN WORDS",
		"no words were given: the prompt must not speak about them")
	require.Contains(t, got, "Fabric. "+"THE STATED COLOUR")
}

// TestRenderSwatchThatDidNotAttachLosesItsClause — the recipe names a fabric photograph whose media
// row went away between the snapshot and the pass. buildJob drops the picture; the clause must go
// with it, exactly as the caption does, or the prompt sends the model to look at «image 3» when
// only two pictures were attached.
func TestRenderSwatchThatDidNotAttachLosesItsClause(t *testing.T) {
	r := testRun(1, entity.DesignRunKindRender)
	r.Inputs = entity.RawJSON(renderSlots)
	r.Params = entity.RawJSON(
		`{"views":["front","back"],"layout":"one","colour":{"hex":"#b1121a","fabric_media_id":9}}`)

	// Media 9 — the swatch — is deliberately absent from the resolver.
	job, err := buildJob(context.Background(), media(1, 2), r, "medium")
	require.NoError(t, err)

	require.Len(t, job.References, 2, "only the two plates attach")
	require.NotContains(t, job.Prompt, "THE FABRIC PHOTOGRAPH",
		"a picture the model cannot see must not be given authority over anything")
	require.NotContains(t, job.Prompt, authorityHeader)
	require.Contains(t, job.Prompt, "THE STATED COLOUR", "the colour that did survive still rules")
}

// TestRenderStatesNoFabricWhenNothingIsGiven — the studio's own gate refuses this, so it can only
// arrive from an older client or a script. A prompt that simply said nothing about cloth would let
// the model pick one; naming the fallback keeps the picture explainable.
func TestRenderStatesNoFabricWhenNothingIsGiven(t *testing.T) {
	got := renderPrompt(t, `{"views":["front","back"],"layout":"one"}`, renderSlots)
	require.Contains(t, got, renderNoFabric)
	require.NotContains(t, got, authorityHeader)
}

// TestRenderCompositeAsksForOneGarmentInARow — the owner's own answer for this route: «Три вида в
// одной картинке — фронт, бок, спина в ряд», split into the slots afterwards.
//
// TWO THINGS ARE ASSERTED AND BOTH ARE LOAD-BEARING. The left-to-right list is params order, not
// the canonical front-first order, because the store's compositeViewsOf records p.Views VERBATIM
// and the splitter labels the cut frames off that record. And the row is ONE garment: three views
// that drift into three different whites cannot be cut into three slots of one style.
func TestRenderCompositeAsksForOneGarmentInARow(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","side_l","back"],"layout":"one","colour":{"hex":"#b1121a"}}`,
		renderSlots)

	require.Contains(t, got,
		"Layout: three views of the SAME garment on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced, with clear white space between them and none of them overlapping — left to right: FRONT, SIDE LEFT, BACK.")
	require.Contains(t, got, "not three garments: the same cloth, the same colour and the same lighting in every view.")
}

// TestRenderSingleViewHasNoRow — the same trap flatprompt guards: a row spec with one member asks
// for a canvas holding one small picture in a row.
func TestRenderSingleViewHasNoRow(t *testing.T) {
	got := renderPrompt(t, `{"views":["front"],"layout":"one","colour":{"hex":"#b1121a"}}`, renderSlots)
	require.Contains(t, got,
		"Layout: a single view — FRONT — the garment photographed once, isolated and centred on the canvas.")
	require.NotContains(t, got, "side by side")
}

// TestRenderCarriesTheSample — the owner's attached picture, as assertions: a garment as if worn,
// no body, no mannequin, no hanger, even light, white seamless background, cloth that reads.
func TestRenderCarriesTheSample(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","side_l","back"],"layout":"one","colour":{"hex":"#b1121a"}}`, renderSlots)
	require.Contains(t, got, renderStyle)
	require.Contains(t, got, renderMaterial)
	require.Contains(t, got, renderExcluded)
	require.Contains(t, got, renderOutput)
	require.Contains(t, got, "shown AS IF WORN")
	require.Contains(t, got, "no person, no body part, no mannequin, no dress form, no hanger")
}

// TestTheRenderCraftGetsTheLastWordOverTheHumanContext — the ordering decision of composePrompt,
// made testable on this route too. The rule of precedence arbitrates between the `colour` block,
// the `fabric in words` block and the swatch's caption: it has to stand after all three, or it
// arbitrates over half its subject.
func TestTheRenderCraftGetsTheLastWordOverTheHumanContext(t *testing.T) {
	got := renderPrompt(t,
		`{"views":["front","back"],"layout":"one","colour":{"code":"RED-01","words":"WORDS-brushed","fabric_media_id":9}}`,
		renderSlots)

	require.Less(t, strings.Index(got, "ASK-the words of the person"), strings.Index(got, authorityHeader))
	require.Less(t, strings.Index(got, "colour:\ncolourway RED-01"), strings.Index(got, authorityHeader))
	require.Less(t, strings.Index(got, "fabric in words:\nWORDS-brushed"), strings.Index(got, authorityHeader))
	require.Less(t, strings.Index(got, "- image 3: fabric photograph"), strings.Index(got, authorityHeader))
}

// TestARenderIsNotAFlatAndAFlatIsNotARender — the two craft blocks contradict each other on
// purpose, and the contradiction is only safe while exactly one of them can ride on a run.
func TestARenderIsNotAFlatAndAFlatIsNotARender(t *testing.T) {
	render := renderPrompt(t, `{"views":["front"],"layout":"one","colour":{"hex":"#b1121a"}}`, renderSlots)
	require.NotContains(t, render, "black vector line art",
		"a render that inherits the flat craft comes back as a drawing")
	require.NotContains(t, render, ownerExcludedGarment)

	flat := flatPrompt(t, `{"views":["front"],"layout":"one"}`, oneRef)
	require.NotContains(t, flat, "photorealistic",
		"a flat that inherits the render craft comes back as a photograph")
	require.NotContains(t, flat, authorityHeader)
}

// TestRenderCraftIsForRendersOnly — 3D is a Meshy build, vector redraws an approved raster and
// draft_idea never reaches the worker. None of them is a photograph composed by these words.
func TestRenderCraftIsForRendersOnly(t *testing.T) {
	for _, kind := range []string{
		entity.DesignRunKindThreed, entity.DesignRunKindVector, entity.DesignRunKindDraftIdea,
	} {
		r := testRun(1, kind)
		r.Params = entity.RawJSON(
			`{"views":["front","back"],"layout":"one","colour":{"hex":"#b1121a","fabric_media_id":9}}`)
		r.Inputs = entity.RawJSON(renderSlots)
		r.Ask = sql.NullString{String: "ASK", Valid: true}
		p, in := parseParams(r.Params), parseInputs(r.Inputs)
		got := composePrompt(r, p, in, referenceList(r.Kind, p, in))
		require.NotContains(t, got, renderStyle, "kind %s must keep its bare context", kind)
		require.NotContains(t, got, authorityHeader, "kind %s must keep its bare context", kind)
		require.Contains(t, got, "ASK")
	}
}

// TestRenderAuthorityCoversEveryClause — the ranking and the "is this source present" predicates
// are two lists that must stay the same length and the same order. They live beside each other in
// renderprompt.go for that reason; this is the assertion that says so out loud, because a fourth
// way of stating cloth added to one list and not the other would silently drop a clause or rank the
// wrong one.
func TestRenderAuthorityCoversEveryClause(t *testing.T) {
	all := fabricStated{photoImage: 3, colour: "colourway RED-01", words: "brushed"}
	require.Len(t, renderFabricPresence(all), len(renderFabricAuthority),
		"every rank must have a predicate that says whether this run carries it")
	for i, present := range renderFabricPresence(all) {
		require.True(t, present, "rank %d must read as present when all three sources are stated", i+1)
	}
}
