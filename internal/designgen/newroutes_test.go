package designgen

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/stretchr/testify/require"
)

// ─────────────────────── THE REFUSAL A PERSON READS ───────────────────────

// stubSink accepts everything, so a pre-flight test measures the ROUTE and not the bucket.
type allSink struct{}

func (allSink) Accepts(string) bool { return true }
func (allSink) Put(context.Context, []byte, string, string) (MintedMedia, error) {
	return MintedMedia{}, nil
}
func (allSink) Drop(context.Context, MintedMedia) {}

// TestEveryRouteWithoutItsKeyNAMES_THE_VARIABLE_AT_THE_DOOR.
//
// ⚠ THIS SENTENCE IS THE ONE THAT REACHES THE SCREEN, and every Execute in this package that names
// its variable is UNREACHABLE while the key is missing — preflight refuses first. Until this wave
// the door could only say the route's name («…is not configured: fal»), so an owner who had just
// typed a key into a dashboard could not tell from the button whether that was the missing piece.
//
// The reason is also the machine word the history row would have carried, so the two agree by
// construction rather than by two people writing the same string twice.
func TestEveryRouteWithoutItsKeyNAMES_THE_VARIABLE_AT_THE_DOOR(t *testing.T) {
	// Every provider is constructed with a NIL client — «no credentials», the state a fresh
	// deployment is in before the owner opens the dashboard.
	w := newWorker(&Config{}, nil, nil, allSink{}, Providers{
		Image:  NewImageProvider(nil),
		Threed: NewFalThreedProvider(nil),
	})

	for _, tc := range []struct {
		kind string
		want string
	}{
		// B-33: the sentence names the PROVIDER and the one place its key goes (admin → AI providers);
		// the env variable it once offered as an alternative is dead and must not be named.
		{entity.DesignRunKindThreed, "no key for fal — save it in admin → AI providers"},
		{entity.DesignRunKindFlat, "no key for openrouter — save it in admin → AI providers"},
		{entity.DesignRunKindRender, "no key for openrouter — save it in admin → AI providers"},
		{entity.DesignRunKindRecolor, "no key for openrouter — save it in admin → AI providers"},
		{entity.DesignRunKindPattern, "no key for openrouter — save it in admin → AI providers"},
		// ПЛЕЙГРАУНД ЕДЕТ К ТОМУ ЖЕ КЛЮЧУ, и это его единственная зависимость: `freeform` — пятый
		// род на маршруте картинок, а не свой провайдер. Строка здесь держит и это тоже: род,
		// названный у двери, но не попавший в forKind, отказал бы словами «no image route», то
		// есть человеку, который ключ как раз ввёл.
		{entity.DesignRunKindFreeform, "no key for openrouter — save it in admin → AI providers"},
	} {
		err := w.PreflightKind(tc.kind)
		require.Errorf(t, err, "kind %s", tc.kind)
		require.Containsf(t, err.Error(), tc.want,
			"kind %s must name the provider and the place a person can act on, not just the route", tc.kind)
		require.NotContainsf(t, err.Error(), "_KEY", "kind %s must not send a person to a dead env variable", tc.kind)

		var named *KindRefusal
		require.ErrorAs(t, err, &named)
		require.Equal(t, CodeKindNotAvailable, named.RefusalReason(),
			"«no key» is the same machine word wherever it is discovered")
	}

	// AND THE MESHY ROUTE KEEPS ITS OWN NAME, so switching DESIGN_THREED_PROVIDER switches the
	// sentence too — an operator told to set FAL_KEY on a Meshy deployment would set the wrong one.
	m := newWorker(&Config{}, nil, nil, allSink{}, Providers{Threed: NewThreedProvider(nil)})
	require.Contains(t, m.PreflightKind(entity.DesignRunKindThreed).Error(),
		"no key for meshy — save it in admin → AI providers")
}

// ─────────────────────── K-10: THE NAMED VIEWS ───────────────────────

// TestTheFalBuildMapsViewsBY_NAME_NOT_BY_POSITION.
//
// ⚠ THE POSITIONAL RULE IS SILENTLY WRONG, WHICH IS WHY THIS ROUTE EXISTS. «The first picture is
// the front» holds only while the list is sorted AND complete. A card whose bench holds a back and
// a side but no front — an ordinary state halfway through a session — would send the BACK as the
// face of the garment: the provider builds it faithfully, the run closes `done`, the money is gone,
// and the history cannot tell that build from an honest one.
func TestTheFalBuildMapsViewsBY_NAME_NOT_BY_POSITION(t *testing.T) {
	// The list deliberately does NOT start with the front.
	job := Job{
		Kind: entity.DesignRunKindThreed,
		References: []string{
			"https://cdn/back.png", "https://cdn/front.png", "https://cdn/left.png", "https://cdn/right.png",
		},
		ReferenceViews: []string{
			entity.DesignViewBack, entity.DesignViewFront, entity.DesignViewSideL, entity.DesignViewSideR,
		},
	}
	req, err := falViews(job)
	require.NoError(t, err)
	require.Equal(t, "https://cdn/front.png", req.FrontURL, "the front is the plate LABELLED front")
	require.Equal(t, "https://cdn/back.png", req.BackURL)
	require.Equal(t, "https://cdn/left.png", req.LeftURL)
	require.Equal(t, "https://cdn/right.png", req.RightURL)

	// NO FRONT AT ALL IS A REFUSAL, NOT A SUBSTITUTION.
	_, err = falViews(Job{
		References:     []string{"https://cdn/back.png"},
		ReferenceViews: []string{entity.DesignViewBack},
	})
	require.ErrorIs(t, err, fal.ErrNoFrontView)

	// AN UNLABELLED PICTURE IS REFUSED RATHER THAN GUESSED AT: a named-slot build has nowhere to
	// put it, and inventing a side is how a run becomes unaccountable.
	_, err = falViews(Job{
		References:     []string{"https://cdn/front.png", "https://cdn/mood.png"},
		ReferenceViews: []string{entity.DesignViewFront, ""},
	})
	require.ErrorIs(t, err, fal.ErrNoFrontView)
	require.Contains(t, err.Error(), "reference 2")
}

// TestTheFalRefusalsAreTERMINAL_NOT_WEATHER. Five paid rounds against a build with no front buys
// nothing, and a history row reading `provider_unavailable` sends a person to a status page.
func TestTheFalRefusalsAreTERMINAL_NOT_WEATHER(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{fal.ErrNoFrontView, CodeBadRequest},
		{fal.ErrBadRequest, CodeBadRequest},
		{fal.ErrModelUnavailable, CodeModelRetired},
		{fal.ErrUnauthorized, CodeUnauthorized},
		{fal.ErrOutOfCredit, CodeOutOfCredit},
		{fal.ErrNotConfigured, CodeKindNotAvailable},
	} {
		v := classify(tc.err)
		require.Equalf(t, tc.code, v.Code, "%v", tc.err)
		require.Falsef(t, v.Retryable, "%v must not spend the attempt cap", tc.err)
	}
	// ⚠ AND «THE MODEL IS GONE» MUST NOT READ AS «THE SERVICE IS BUSY» — the failure that once took
	// down both AI features at once.
	require.NotEqual(t, classify(fal.ErrModelUnavailable).Code, classify(fal.ErrRateLimited).Code)
	require.True(t, classify(fal.ErrRateLimited).Retryable, "a refused request was not billed")
	require.True(t, classify(fal.ErrNotReady).Retryable, "the free collect resumes a paid build")

	// A fal task the provider ended is `unknown`, NOT `failed`: unlike Meshy, fal makes no refund
	// promise, so «the money may be gone and there is nothing to show» is the honest word.
	require.Equal(t, entity.DesignAttemptUnknown, classify(fal.ErrTaskFailed).State)
}

// ─────────────────────── K-17: ONE CALL PER PHOTOGRAPH ───────────────────────

// TestARecolourIsONE_CALL_PER_PHOTOGRAPH_SHOWING_ONLY_ITS_OWN.
//
// ⚠ BOTH HALVES ARE MONEY. One call per photograph, because the provider's `n` returns n VARIANTS
// OF ONE PROMPT — four frames are four charges, and a single call would answer none of the four
// asks. And only its own picture in each call, because an image model handed a second reference
// COMPOSES: it returns a similar frame instead of the same one, and nothing in the history
// distinguishes that from a correct result.
func TestARecolourIsONE_CALL_PER_PHOTOGRAPH_SHOWING_ONLY_ITS_OWN(t *testing.T) {
	job := Job{
		Kind:   entity.DesignRunKindRecolor,
		Prompt: "recolour it",
		References: []string{
			"https://cdn/on-model-1.png", "https://cdn/on-model-2.png", "https://cdn/on-model-3.png",
		},
	}
	calls, err := imageCalls(job)
	require.NoError(t, err)
	require.Len(t, calls, 3, "three photographs are three paid edits")
	for i, c := range calls {
		require.Equal(t, 1, c.n)
		require.Lenf(t, c.refs, 1, "call %d must show the model exactly one picture", i)
		require.Equal(t, job.References[i], c.refs[0])
		require.Emptyf(t, c.view, "a recoloured photograph is not addressed to a side of the bench")
	}

	// NOTHING TO RECOLOUR IS A REFUSAL BEFORE ANY MONEY MOVES.
	_, err = imageCalls(Job{Kind: entity.DesignRunKindRecolor, Prompt: "recolour it"})
	require.Error(t, err)
	require.False(t, classify(err).Retryable, "an empty request does not improve by being re-sent")
}

// TestTheRecolourCraftFORBIDS_A_NEW_PHOTOGRAPH. The whole customer-facing value of ON MODEL is that
// the frame is REAL; an image model given «make this jacket olive» and nothing else returns a
// plausible new photograph, which answers a question nobody asked.
func TestTheRecolourCraftFORBIDS_A_NEW_PHOTOGRAPH(t *testing.T) {
	craft := recolorCraft(runParams{})
	low := strings.ToLower(craft)
	for _, must := range []string{"that same", "pose", "background", "lighting", "strictly excluded"} {
		require.Containsf(t, low, must, "a recolour must pin %q, or the model reinvents the frame", must)
	}
	// AND IT MUST CARRY THE MATERIAL THROUGH, or the answer is a paint bucket: a flat patch of
	// colour where a garment used to be. That is the difference the owner chose generation for.
	for _, must := range []string{"weave", "fold", "shadow"} {
		require.Containsf(t, low, must, "a recolour that loses %q is a filter, not a generation", must)
	}

	// THE PRINT SENTENCE APPEARS ONLY WHEN THE RUN STATES A PATTERNED CLOTH. Saying it on a plain
	// garment invites a model to invent a print that was never there.
	require.NotContains(t, low, "the garment carries a print")
	withPrint := strings.ToLower(recolorCraft(runParams{
		Colour: &colourRecipe{Fabrics: []fabricUse{{RepeatMM: 120}}},
	}))
	require.Contains(t, withPrint, "the garment carries a print")
}

// ─────────────────────── K-13: ONE PICTURE IN, ONE TILE OUT ───────────────────────

// TestAPatternIsBuiltFromEXACTLY_ONE_PICTURE. A tile glued out of two swatches cannot join to
// itself under any arrangement, which makes it useless in the one sense it was ordered for.
func TestAPatternIsBuiltFromEXACTLY_ONE_PICTURE(t *testing.T) {
	calls, err := imageCalls(Job{
		Kind: entity.DesignRunKindPattern, Prompt: "tile it",
		References: []string{"https://cdn/swatch.png"},
	})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Equal(t, []string{"https://cdn/swatch.png"}, calls[0].refs)

	for _, refs := range [][]string{nil, {"a", "b"}} {
		_, err := imageCalls(Job{Kind: entity.DesignRunKindPattern, References: refs})
		require.Errorf(t, err, "%d sources", len(refs))
		require.False(t, classify(err).Retryable)
	}
}

// TestThePatternCraftASKS_FOR_THE_WRAP_AND_FORBIDS_A_BORDER. «Seamless» politely requested returns
// a picture that merely looks patterned; the boundary has to be spelled out, and so does everything
// that would put a visible edge into every cell of the grid.
func TestThePatternCraftASKS_FOR_THE_WRAP_AND_FORBIDS_A_BORDER(t *testing.T) {
	low := strings.ToLower(patternCraft(patternParams{}, 1))
	for _, must := range []string{"right edge", "left edge", "bottom edge", "top edge", "seamless"} {
		require.Containsf(t, low, must, "the wrap must be stated, not implied (%q)", must)
	}
	for _, must := range []string{"border", "vignette", "shadow", "watermark"} {
		require.Containsf(t, low, must, "%q at the edge is a seam in every cell of the grid", must)
	}
	// THE SOURCE'S LIGHTING IS EXCLUDED: a swatch lit from one side tiles into visible stripes,
	// each of them a seam, none of them at the edge anybody thought to look at.
	require.Contains(t, low, "lighting of the source")

	// THE REPEAT IS SAID ONLY WHEN IT IS KNOWN, and it is said as SCALE, not as a pixel size.
	require.NotContains(t, low, " mm repeat")
	require.Contains(t, strings.ToLower(patternCraft(patternParams{RepeatMM: 120}, 1)), "120 mm repeat")
}

// TestTheSeamMeasurementCATCHES_A_BORDER_AND_PASSES_A_WRAPPING_TILE.
//
// The verdict never decides the run's fate (see seam.go) — it decides the WORDS. So what has to
// hold is that it is not noise: an obviously non-wrapping tile fails and a genuinely wrapping one,
// including a busy high-contrast one, passes.
func TestTheSeamMeasurementCATCHES_A_BORDER_AND_PASSES_A_WRAPPING_TILE(t *testing.T) {
	// A TRUE WRAP: a sinusoid whose period divides the width exactly, so column w-1 and column 0
	// are ordinary neighbours. Busy on purpose — a soft gradient would prove nothing.
	wrapping := renderPNG(t, 64, 64, func(x, y int) color.RGBA {
		v := 128 + 120*math.Sin(2*math.Pi*float64(x)/16)*math.Cos(2*math.Pi*float64(y)/16)
		return color.RGBA{uint8(v), uint8(v / 2), 40, 255}
	})
	v := seamCheck(wrapping)
	require.True(t, v.Measured)
	require.Truef(t, v.Seamless(), "a genuinely wrapping tile must not be complained about "+
		"(h=%.1f v=%.1f baseline=%.1f)", v.Horizontal, v.Vertical, v.Baseline)

	// FAILURE ONE, THE WRAP: a gradient across the square. Column 63 is bright, column 0 is dark,
	// so laid out the tile shows a hard vertical line at every cell boundary.
	gradient := renderPNG(t, 64, 64, func(x, y int) color.RGBA {
		v := uint8(4 * x)
		return color.RGBA{v, v, v, 255}
	})
	gv := seamCheck(gradient)
	require.Falsef(t, gv.Seamless(), "a tile that does not wrap must be complained about "+
		"(h=%.1f limit=%.1f)", gv.Horizontal, gv.WrapLimit())
	require.Greater(t, gv.Horizontal, gv.WrapLimit(), "and it must be the WRAP measurement that says so")

	// FAILURE TWO, THE FRAME — AND IT IS THE OPPOSITE SIGNATURE, which is why one measurement was
	// not enough. Both edges of a bordered square are the same white, so THE WRAP SEAM IS PERFECT
	// and the defect is entirely interior. Measured, not reasoned: the first version of this check
	// carried only the wrap test and passed this picture.
	bordered := renderPNG(t, 64, 64, func(x, y int) color.RGBA {
		if x < 3 || y < 3 || x > 60 || y > 60 {
			return color.RGBA{255, 255, 255, 255}
		}
		v := 128 + 120*math.Sin(2*math.Pi*float64(x)/16)*math.Cos(2*math.Pi*float64(y)/16)
		return color.RGBA{uint8(v), uint8(v / 2), 40, 255}
	})
	bv := seamCheck(bordered)
	require.False(t, bv.Seamless(), "a bordered square announces its own grid")
	require.LessOrEqual(t, bv.Horizontal, bv.WrapLimit(),
		"the wrap seam of a symmetric border really is perfect — this is the blind spot the second "+
			"measurement exists to cover, and pinning it stops anybody removing that measurement")
	require.Greater(t, bv.EdgeBias, bv.BiasLimit())

	// A VIGNETTE IS THE SAME BLIND SPOT MORE QUIETLY: darker toward every edge, both edges equally
	// dark, so the join looks fine and the cloth comes out in a lattice of shadows.
	vignette := renderPNG(t, 64, 64, func(x, y int) color.RGBA {
		dx, dy := float64(x-32)/32, float64(y-32)/32
		v := uint8(200 * (1 - 0.9*math.Min(1, dx*dx+dy*dy)))
		return color.RGBA{v, v, v, 255}
	})
	require.False(t, seamCheck(vignette).Seamless(), "a vignette repeats as a lattice of shadows")

	// A FLAT TILE PASSES. The floor exists precisely so that «three times an almost-zero baseline»
	// cannot condemn a plain cloth.
	flat := renderPNG(t, 32, 32, func(int, int) color.RGBA { return color.RGBA{90, 90, 90, 255} })
	require.True(t, seamCheck(flat).Seamless())

	// ⚠ AND A TILE NOBODY COULD MEASURE IS NOT A TILE THAT FAILED. Silence about a picture we could
	// not read must never become a complaint about it — the picture is already bought.
	unreadable := seamCheck([]byte("not an image at all"))
	require.False(t, unreadable.Measured)
	require.True(t, unreadable.Seamless())
}

// TestABadTileIsDELIVERED_KEPT_AND_NOT_BOUGHT_AGAIN.
//
// The three halves of the verdict, which is the whole reason the complaint is a classified sentinel
// rather than a log line: the attempt closes DELIVERED (the picture exists and is filed), it is NOT
// retryable (a second identical call buys the same kind of answer from the same model), and it
// carries a word of its own so a person can see WHY the tile may not be what they asked for.
func TestABadTileIsDELIVERED_KEPT_AND_NOT_BOUGHT_AGAIN(t *testing.T) {
	v := classify(errPatternNotSeamless)
	require.Equal(t, entity.DesignAttemptDelivered, v.State)
	require.False(t, v.Retryable)
	require.Equal(t, CodePatternNotSeamless, v.Code)
	require.NotEqual(t, CodeStorageFailed, v.Code, "«it does not tile» is not «we could not store it»")
}

// renderPNG builds a PNG from a per-pixel function.
func renderPNG(t *testing.T, w, h int, at func(x, y int) color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, at(x, y))
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// ─────────────────────── WHAT REACHES THE MODEL ───────────────────────

// TestARecolourSENDS_ONLY_THE_PICTURES_IT_WAS_GIVEN.
//
// The run's own reference list is assembled from four sources — bench plates, card references,
// named uploads and fabric swatches — which is right for a flat and wrong here twice over: a bench
// plate in a recolour call turns «return THIS frame» into «compose a similar one», and a mood
// reference in a pattern call makes a tile out of two cloths.
func TestARecolourSENDS_ONLY_THE_PICTURES_IT_WAS_GIVEN(t *testing.T) {
	params := map[string]any{
		"extra_input_media_ids": []int{77, 78},
		"colour":                map[string]any{"code": "OLV", "words": "deep olive"},
	}
	inputs := map[string]any{
		"garment_note": "a shirt",
		"slots": []map[string]any{
			{"view_key": "front", "media_id": 11},
			{"view_key": "back", "media_id": 12},
		},
		"refs": []map[string]any{{"media_id": 13, "note": "mood"}},
	}
	run := entity.DesignRun{
		Id: 5, TechCardId: 41, Kind: entity.DesignRunKindRecolor,
		Params: rawJSON(t, params), Inputs: rawJSON(t, inputs),
	}
	job, err := buildJob(context.Background(), media(11, 12, 13, 77, 78), nil, run, "medium")
	require.NoError(t, err)
	require.Len(t, job.References, 2, "the plates and the mood reference have no business here")
	require.Contains(t, job.References[0], "/77.")
	require.Contains(t, job.References[1], "/78.")

	// AND THE COLOUR STILL REACHES THE MODEL — it is the whole instruction.
	require.Contains(t, job.Prompt, "OLV")
	require.Contains(t, job.Prompt, "deep olive")
	require.Contains(t, strings.ToLower(job.Prompt), "recolour, not re-photograph")
}

// TestAPatternRunCARRIES_ITS_REPEAT_AND_ONE_PICTURE.
func TestAPatternRunCARRIES_ITS_REPEAT_AND_ONE_PICTURE(t *testing.T) {
	run := entity.DesignRun{
		Id: 6, TechCardId: 41, Kind: entity.DesignRunKindPattern,
		Params: rawJSON(t, map[string]any{
			"extra_input_media_ids": []int{90},
			"pattern":               map[string]any{"repeat_mm": 150},
		}),
		Inputs: rawJSON(t, map[string]any{
			"slots": []map[string]any{{"view_key": "front", "media_id": 11}},
		}),
	}
	job, err := buildJob(context.Background(), media(11, 90), nil, run, "medium")
	require.NoError(t, err)
	require.Len(t, job.References, 1)
	require.Contains(t, job.References[0], "/90.")
	// ⚠ THE KEYS ARE snake_case BECAUSE THE WRITER USES protojson WITH UseProtoNames. A mismatch
	// here is silent: the number simply never reaches the model.
	require.Contains(t, job.Prompt, "150 mm repeat")
	require.Contains(t, strings.ToLower(job.Prompt), "repeating tile")
}

// TestNeitherNewKindTakesACraftBlockThatIsNotITS_OWN. The four paragraphs contradict each other on
// purpose — «build the scene» against «do not touch the scene», «black line art on white» against
// «an even field of colour» — so a run that took two would end on whichever happens to be written
// last in the file.
func TestNeitherNewKindTakesACraftBlockThatIsNotITS_OWN(t *testing.T) {
	base := map[string]any{"extra_input_media_ids": []int{90}}
	for _, tc := range []struct {
		kind    string
		wants   string
		forbids []string
	}{
		{entity.DesignRunKindRecolor, "recolour, not re-photograph", []string{"repeating tile"}},
		{entity.DesignRunKindPattern, "repeating tile", []string{"recolour, not re-photograph"}},
	} {
		run := entity.DesignRun{
			Id: 7, Kind: tc.kind, Params: rawJSON(t, base),
			Inputs: rawJSON(t, map[string]any{}),
		}
		job, err := buildJob(context.Background(), media(90), nil, run, "medium")
		require.NoError(t, err)
		low := strings.ToLower(job.Prompt)
		require.Contains(t, low, tc.wants)
		for _, f := range tc.forbids {
			require.NotContainsf(t, low, f, "kind %s took a craft block that is not its own", tc.kind)
		}
	}
}

// TestATileIsNeverASKED_FOR_ON_A_TRANSPARENT_BACKGROUND. A transparent region inside a repeating
// tile is a hole that repeats — it shows through at the same spot in every cell of the grid.
func TestATileIsNeverASKED_FOR_ON_A_TRANSPARENT_BACKGROUND(t *testing.T) {
	require.Equal(t, "opaque", backgroundFor(entity.DesignRunKindPattern))
	// A RECOLOUR KEEPS THE PROVIDER'S DEFAULT: the background of the answer must be the background
	// of the SOURCE PHOTOGRAPH, and any value sent here is an instruction to change it.
	require.Empty(t, backgroundFor(entity.DesignRunKindRecolor))
}

func rawJSON(t *testing.T, v any) entity.RawJSON {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return entity.RawJSON(raw)
}

// TestARecolourNAMES_THE_GARMENT_FROM_THE_WORDS_AND_ASSUMES_NO_PERSON — 20-PROMPTS D4. Tiles 4/5 take
// flats and renders too, so the paragraph describes «a picture of a garment» and keeps «any person»;
// and the ask is where a person says WHICH garment, so the target is the one the words above name.
//
// MUTATIONS (each measured red): the old recolour opening «You are given a real photograph of a
// garment worn by a real person. Return THAT SAME PHOTOGRAPH with the garment recoloured…»; the old
// re-cloth opening «… (image 1) and a photograph of a cloth (image 2). Return THAT SAME PHOTOGRAPH
// with the garment made of the cloth in image 2…».
func TestARecolourNAMES_THE_GARMENT_FROM_THE_WORDS_AND_ASSUMES_NO_PERSON(t *testing.T) {
	re := recolorCraft(runParams{})
	require.True(t, strings.HasPrefix(re, "recolour, not re-photograph:\n"))
	require.Contains(t, re, "You are given a picture of a garment — a photograph on a person or a mannequin, a flat drawing or a render.")
	require.Contains(t, re, "the garment the words above name (the main garment when they name none)")
	require.Contains(t, re, "its colourway code, its name and its exact value")
	require.Contains(t, re, "including the parts the words above say to keep")
	require.Contains(t, re, "any person (face, skin, hair, hands)")
	require.NotContains(t, re, "real person", "a flat has no person to keep")
	require.Contains(t, re, "a colour that spills onto skin, hair or background", "the exclusion list stays")

	cl := recolorCraft(runParams{Colour: &colourRecipe{Fabrics: []fabricUse{{Name: "check", MediaID: 9}}}})
	require.True(t, strings.HasPrefix(cl, "re-cloth, not re-photograph:\n"))
	require.Contains(t, cl, "You are given a picture of a garment (image 1) — a photograph on a person or a mannequin, a flat drawing or a render — and a photograph of a cloth (image 2).")
	require.Contains(t, cl, "the garment the words above name (the main garment when they name none) made of the cloth in image 2")
	require.Contains(t, cl, "Parts the words above say to keep, and every other garment, keep their own cloth.")
	require.NotContains(t, cl, "real person")
}

// TestARecolourOfAFlatIS_ONE_STORY_FROM_CAPTION_TO_CRAFT — review MAJOR 1 / MINOR 6, r2 MINOR 3. The
// craft test above reads recolorCraft alone, which is why a caption still promising «the real
// photograph … the same person» passed next to a craft that takes flats. This one pins the WHOLE
// composed prompt buildJob hands the provider, both branches (recolour and re-cloth), with
// require.Equal: a sentence appended to any block — «Always return a studio photo with a wearer» —
// is red, not only the phrases a forbidden list happens to name. The ask, the colour and the captions
// come from the fixture; the crafts are written out in full on purpose (a craft built from
// recolorCraft itself would move with the mutation it is meant to catch).
// MUTATIONS (each measured red): the old caption «the photograph being recoloured — the real
// photograph this call must give back, with the same person, pose, framing, background and
// lighting»; a sentence appended to recolorCraft; a sentence appended to reclothCraft.
func TestARecolourOfAFlatIS_ONE_STORY_FROM_CAPTION_TO_CRAFT(t *testing.T) {
	const ask = "recolour the technical flat of this jacket"
	const caption = "- image 1: the source picture being recoloured — return this same picture, preserving " +
		"its presentation, crop and background, plus any lighting, person and pose that are present"
	const recolourCraft = `recolour, not re-photograph:
You are given a picture of a garment — a photograph on a person or a mannequin, a flat drawing or a render. Return THAT SAME PICTURE with the garment the words above name (the main garment when they name none) recoloured to the colour stated above — its colourway code, its name and its exact value — and change nothing else.
Keep exactly as they are: any person (face, skin, hair, hands), the pose and the framing; the background and the floor; the lighting, its direction and its colour temperature; every other garment, part, shoe and accessory in the frame, including the parts the words above say to keep; the image's resolution, crop and aspect ratio.
Keep the garment itself in every respect except its colour: the same cut, the same seams, topstitching, pockets, zips, buttons and labels, in the same places and at the same size.
Carry the material through the change instead of painting over it. The weave and the surface texture must still read at the same scale; the folds, creases and drape must fall exactly where they fall now; the highlights and the shadows on the cloth must keep their shape and their strength, re-tinted to the new colour rather than flattened out. The result must look like the same garment cut from cloth dyed differently, never like a colour laid over a photograph.
Strictly excluded: a different person, a different pose, a different background, a different crop, added or removed items, retouched skin, a smoothed or repainted surface, added logos or text, a colour that spills onto skin, hair or background.`
	const reclothCraftText = `re-cloth, not re-photograph:
You are given a picture of a garment (image 1) — a photograph on a person or a mannequin, a flat drawing or a render — and a photograph of a cloth (image 2). Return THAT SAME PICTURE with the garment the words above name (the main garment when they name none) made of the cloth in image 2, and change nothing else. Parts the words above say to keep, and every other garment, keep their own cloth.
Keep exactly as they are: any person (face, skin, hair, hands), the pose and the framing; the background and the floor; the lighting, its direction and its colour temperature; every other garment, shoe and accessory in the frame; the image's resolution, crop and aspect ratio.
Keep the garment's cut, seams, topstitching, pockets, zips, buttons and labels in the same places and at the same size.
Lay the cloth ON the garment: its weave, surface and print must follow the folds, creases and drape exactly where they fall now, with the highlights and the shadows keeping their shape and their strength on the new cloth. The result must look like the same garment cut from a different cloth, never like a picture of cloth pasted over a photograph.
The colour stated above, if any, governs the colour of the cloth: re-tint the cloth of image 2 to it and keep its motif, its weave and its scale.
Strictly excluded: a different person, a different pose, a different background, a different crop; cloth laid over the skin, the hair or the background; a flat printed sticker instead of woven or knitted cloth; a smoothed or repainted surface; added or removed items; retouched skin; added logos or text.`
	for _, tc := range []struct {
		name   string
		colour map[string]any
		ids    []int
		want   string
	}{
		{"recolour", map[string]any{"code": "19-4052", "words": "Classic Blue"}, []int{77},
			ask + "\n\ncolour:\ncolourway 19-4052\n\ncolour in words:\nClassic Blue\n\nreferences:\n" +
				caption + "\n\n" + recolourCraft},
		{"re-cloth", map[string]any{"code": "OLV", "fabric_media_id": 9,
			"fabrics": []map[string]any{{"name": "check", "media_id": 9}}}, []int{77, 9},
			ask + "\n\ncolour:\ncolourway OLV\n\nreferences:\n" + caption + "\n" +
				"- image 2: fabric photograph — the material this garment is made of: read its weave, texture, sheen and drape from here" +
				"\n\n" + reclothCraftText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := entity.DesignRun{
				Id: 5, TechCardId: 41, Kind: entity.DesignRunKindRecolor,
				Ask:    sql.NullString{String: ask, Valid: true},
				Params: rawJSON(t, map[string]any{"extra_input_media_ids": []int{77}, "colour": tc.colour}),
				Inputs: rawJSON(t, map[string]any{}),
			}
			job, err := buildJob(context.Background(), media(tc.ids...), nil, run, "medium")
			require.NoError(t, err)
			require.Len(t, job.References, 1)
			require.Equal(t, tc.want, job.Prompt)
			for _, f := range []string{"real photograph", "real person", "same person", "the photograph being recoloured"} {
				require.NotContains(t, job.Prompt, f, "a flat has no person and is not a photograph")
			}
		})
	}
}

// TestTheRecolourWordsBlockIsCOLOUR_IN_WORDS_AND_THE_RENDER_KEEPS_FABRIC_IN_WORDS — 20-PROMPTS §3.2.
// Tile 4 sends the Pantone's NAME as the words; under «fabric in words» it reads as a note about the
// cloth. The render route keeps its label: its order of authority names «the `fabric in words`
// block» in its own text. MUTATION (measured red): the kind check removed (always «fabric in words»).
func TestTheRecolourWordsBlockIsCOLOUR_IN_WORDS_AND_THE_RENDER_KEEPS_FABRIC_IN_WORDS(t *testing.T) {
	p := runParams{Colour: &colourRecipe{Code: "19-4052", Words: "Classic Blue"}}
	recolour := composePrompt(entity.DesignRun{Kind: entity.DesignRunKindRecolor}, p, runInputs{}, nil)
	require.Contains(t, recolour, "colour in words:\nClassic Blue")
	require.NotContains(t, recolour, "fabric in words")

	render := composePrompt(entity.DesignRun{Kind: entity.DesignRunKindRender}, p, runInputs{}, nil)
	require.Contains(t, render, "fabric in words:\nClassic Blue")
	require.NotContains(t, render, "colour in words")
}
