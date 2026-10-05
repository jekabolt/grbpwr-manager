package designgen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ 80-BUILD-MODES: the three flat modes, golden ═══

var fourViews = []string{"front", "back", "side_l", "side_r"}

// The generalised prompt-Dflare paragraphs 1–3 (out/f8/d/prompt-Dflare.txt; the card-38 clauses moved
// into the CHECK sentences). A literal copy: an «improvement» of the craft goes red here.
const goldenTraceCoreImage1 = `Image 1 is the exact construction drawing of a garment, already laid out as four technical-flat views on one canvas, left to right: FRONT, BACK, SIDE LEFT, SIDE RIGHT.

Redraw image 1 as a finished professional fashion technical flat sketch (CAD-style tech pack drawing) in the same layout. This is a TRACING job, not a design job:
- keep every edge, band, strap, seam, hem and dashed line exactly where image 1 has it, with the same proportions and the same position of each view;
- do not add ANY line, edge, band, panel, neckline, shoulder, seam or detail that is not in image 1; open areas stay plain white;
- do not remove or straighten anything; where image 1 shows a strap, the finished drawing shows a strap of the same width in the same place.

Improve only the rendering: uniform precise vector-style line work, heavier weight for outer contours, thin lines for internal design lines, fine dashed lines for topstitching and seam stitching; subtle body-form shaping of the outline following the given silhouette; cloth drawn white.`

func under(n int) refCaption {
	return refCaption{MediaID: n, Caption: underdrawingCaption, IsUnderdrawing: true}
}

func photo(n int, role, note string) refCaption {
	return refCaption{MediaID: n, Caption: role, FromRef: true, Role: role, Note: note}
}

func drawingParams(mode string) runParams {
	return runParams{Views: fourViews, Layout: layoutOne, Flat: &flatParams{Mode: mode, UnderdrawingMediaID: 5}}
}

func TestFlatTraceCraftIsF8Verbatim(t *testing.T) {
	got := flatTraceCraft(drawingParams(FlatModeDrawing), nil, []refCaption{under(5)}, nil, FlatModeDrawing)
	require.True(t, strings.HasPrefix(got, goldenTraceCoreImage1+"\n\n"), got)
	require.True(t, strings.HasSuffix(got, flatNoTextNoGrey+"\n\n"+flatStyleGarment+"\n\n"+flatExcludedGarment+"\n\n"+flatOutput))
	for _, absent := range []string{"Turn the garment", "Automatically identify", "Layout:"} {
		require.NotContains(t, got, absent, "a trace does not speak to photos")
	}
	// image number 3: the drawing is wherever it attached
	got3 := flatTraceCraft(drawingParams(FlatModeDrawing), nil, []refCaption{photo(1, "front", ""), photo(2, "back", ""), under(5)}, nil, FlatModeDrawing)
	require.True(t, strings.HasPrefix(got3, strings.ReplaceAll(strings.ReplaceAll(goldenTraceCoreImage1, "Image 1", "Image 3"), "image 1", "image 3")), got3)
}

func TestFlatPhotoRolesAreCx8(t *testing.T) {
	att := []refCaption{under(5), photo(11, "front", "the waist"), photo(12, "back", ""), photo(13, "side_r", ""), photo(14, "detail", "")}
	got := flatTraceCraft(drawingParams(FlatModeDrawingPhotos), nil, att, nil, FlatModeDrawingPhotos)
	want := `Input roles are strict and non-interchangeable:
- Image 1 — STRUCTURE AUTHORITY: controls every endpoint, connection, crossing, opening, seam, pocket, closure and the view layout.
- Image 2 — FRONT FIT AND DETAIL AUTHORITY only (front photo — the waist); never a source of construction.
- Image 3 — BACK FIT AND DETAIL AUTHORITY only (back photo); never a source of construction.
- Image 4 — SIDE FIT AND DETAIL AUTHORITY only (right side photo, the wearer's RIGHT flank); never a source of construction; mirror its depth profile to image 1's side orientation.
- Image 5 — DETAIL FIT AND DETAIL AUTHORITY only (detail close-up); never a source of construction.
Unlike a literal trace, adjust only the continuous OUTER-SILHOUETTE geometry and the rendering of details to the photos: reproduce the fit (waist suppression, ease, bust/hip curvature, body and sleeve length), the proportions of pockets, collars and bands, hems, cuffs, topstitching and hardware as seen on the real garment. The photos must NOT change the construction: where a photo disagrees with image 1 about a connection, an endpoint, an opening or a view's orientation, image 1 is right.`
	require.Contains(t, got, "\n\n"+want+"\n\n")
	require.Less(t, strings.Index(got, flatTraceRender), strings.Index(got, want), "roles after the rendering paragraph")
	// drawing alone never names photos; drawing_photos with no photo attached is a plain trace
	require.NotContains(t, flatTraceCraft(drawingParams(FlatModeDrawing), nil, att, nil, FlatModeDrawing), flatRolesHead)
	require.NotContains(t, flatTraceCraft(drawingParams(FlatModeDrawingPhotos), nil, []refCaption{under(5)}, nil, FlatModeDrawingPhotos), flatRolesHead)
}

func TestTraceCraftCarriesTheCheckSentences(t *testing.T) {
	j := loadJoinsCase(t, "c38")
	got := flatTraceCraft(drawingParams(FlatModeDrawing), nil, []refCaption{under(5)}, &j, FlatModeDrawing)
	for _, s := range []string{
		"CHECK EVERY VIEW AGAINST IMAGE 1 AND THESE BEFORE DRAWING:\n- ",
		"its top end sits AT the neck point NP_L",
		"cross each other once, BELOW the neck points",
		"There is NO back neckline",
	} {
		require.Contains(t, got, s)
	}
	require.NotContains(t, got, "JOIN LIST", "the trace checks the sentences; the list itself is the drawing")
}

// The default mode is the text route, UNCHANGED: a run without params.flat and a run that states quick
// compose the same prompt and send the same pictures as before the modes; an underdrawing-role ref is
// never sent by a quick run.
func TestQuickCraftUnchanged(t *testing.T) {
	j := loadJoinsCase(t, "c38")
	inputs := runInputs{Refs: []inputRef{{MediaID: 11, Role: "front"}, {MediaID: 12, Role: "back"}}, Joins: &j}
	base := runParams{Views: fourViews, Layout: layoutOne}
	quick := base
	quick.Flat = &flatParams{Mode: FlatModeQuick}
	run := entity.DesignRun{Kind: entity.DesignRunKindFlat}
	att := referenceList(run.Kind, base, inputs)
	require.Equal(t, att, referenceList(run.Kind, quick, inputs))
	a, b := composePrompt(run, base, inputs, att), composePrompt(run, quick, inputs, att)
	require.Equal(t, a, b)
	require.Contains(t, a, flatCraftWith(base, nil, 2, &j), "quick is flatCraftWith as is")

	withUnder := inputs
	withUnder.Refs = append([]inputRef{{MediaID: 5, Role: entity.DesignRefRoleUnderdrawing}}, inputs.Refs...)
	for _, rc := range referenceList(run.Kind, base, withUnder) {
		require.NotEqual(t, 5, rc.MediaID, "a quick run never sends a construction drawing")
	}
}

func TestHandFlatCraft(t *testing.T) {
	p := runParams{Views: fourViews, Layout: layoutOne, Flat: &flatParams{Mode: FlatModeDrawing, StructureSource: FlatStructureHandFlat}}
	att := []refCaption{
		{MediaID: 7, IsUnderdrawing: true, StructView: "front"},
		{MediaID: 8, IsUnderdrawing: true, StructView: "back"},
	}
	got := flatTraceCraft(p, nil, att, nil, FlatModeDrawing)
	for _, s := range []string{
		"Image 1 is the designer's own hand-drawn technical flat of the garment's FRONT view. Image 2 is the designer's own hand-drawn technical flat of the garment's BACK view. Draw ONE finished sheet: four views on one horizontal canvas",
		"left to right: FRONT, BACK, SIDE LEFT, SIDE RIGHT.",
		"Redraw the designer's flats (images 1 and 2) cleanly",
		"For every view the designer drew this is a TRACING job",
		"exactly where images 1 and 2 has it",
		"Derive the views the designer did not draw (SIDE LEFT, SIDE RIGHT) yourself, consistent with images 1 and 2",
		flatSideFacing,
	} {
		require.Contains(t, got, s)
	}
	require.NotContains(t, got, "construction drawing of a garment", "not the rendered opening")
	// every requested view drawn → nothing to derive
	got2 := flatTraceCraft(runParams{Views: []string{"front"}, Layout: layoutOne, Flat: p.Flat}, nil, att[:1], nil, FlatModeDrawing)
	require.NotContains(t, got2, "Derive the views")
	require.Contains(t, got2, "a single view — FRONT —")
	// drawing_photos: one STRUCTURE line per flat, with its view
	roles := flatTraceCraft(p, nil, append(att, photo(11, "front", "")), nil, FlatModeDrawingPhotos)
	require.Contains(t, roles, "- Image 1 — STRUCTURE AUTHORITY: controls every endpoint, connection, crossing, opening, seam, pocket, closure and the FRONT view.")
	require.Contains(t, roles, "- Image 2 — STRUCTURE AUTHORITY: controls every endpoint, connection, crossing, opening, seam, pocket, closure and the BACK view.")
	require.Contains(t, roles, "where a photo disagrees with images 1 and 2")
}

func TestDrawingModeReferences(t *testing.T) {
	inputs := `{"refs":[{"media_id":5,"role":"underdrawing"},{"media_id":11,"role":"front"},{"media_id":12,"role":"back"}],` +
		`"slots":[{"view_key":"front","media_id":21}]}`
	in := parseInputs(entity.RawJSON(inputs))
	ids := func(p string) []int {
		var out []int
		for _, rc := range referenceList(entity.DesignRunKindFlat, parseParams(entity.RawJSON(p)), in) {
			out = append(out, rc.MediaID)
		}
		return out
	}
	require.Equal(t, []int{5}, ids(`{"views":["front","back"],"layout":"one","extra_input_media_ids":[9],"flat":{"mode":"drawing","underdrawing_media_id":5}}`),
		"drawing: the drawing alone — no photos, no plates, no extras")
	require.Equal(t, []int{5, 11, 12}, ids(`{"views":["front","back"],"layout":"one","flat":{"mode":"drawing_photos","underdrawing_media_id":5}}`))
	require.Equal(t, []int{5, 21}, ids(`{"views":["front","back"],"layout":"one","fix_targets":["front"],"flat":{"mode":"drawing","underdrawing_media_id":5}}`),
		"a fix carries the plate it corrects, after the drawing")
	require.Equal(t, []int{11, 12, 21}, ids(`{"views":["front","back"],"layout":"one","use_flat_slots":true}`), "quick: as before, no drawing")
	hand := parseInputs(entity.RawJSON(`{"refs":[{"media_id":8,"role":"underdrawing"},{"media_id":7,"role":"underdrawing"},{"media_id":11,"role":"front"}]}`))
	got := referenceList(entity.DesignRunKindFlat, parseParams(entity.RawJSON(
		`{"views":["front","back","side_l"],"layout":"one","flat":{"mode":"drawing_photos","structure_source":"hand_flat","structure_refs":[{"media_id":7,"view":"front"},{"media_id":8,"view":"back"}]}}`)), hand)
	require.Len(t, got, 3)
	require.Equal(t, []int{7, 8, 11}, []int{got[0].MediaID, got[1].MediaID, got[2].MediaID}, "the params order and views rule")
	require.Equal(t, "front", got[0].StructView)
	require.Equal(t, "back", got[1].StructView)
}

func TestBuildJobDrawingModes(t *testing.T) {
	r := testRun(1, entity.DesignRunKindFlat)
	r.RequestedOutputs = 4
	r.Params = entity.RawJSON(`{"views":["front","back"],"layout":"one","flat":{"mode":"drawing","underdrawing_media_id":5}}`)
	r.Inputs = entity.RawJSON(`{"refs":[{"media_id":5,"role":"underdrawing"}]}`)
	job, err := buildJob(context.Background(), media(5), nil, r, "medium")
	require.NoError(t, err)
	require.Equal(t, FlatModeDrawing, job.FlatMode)
	require.Len(t, job.References, 1)
	require.Contains(t, job.Prompt, "- image 1: "+underdrawingCaption)
	require.Contains(t, job.Prompt, "Redraw image 1 as a finished")
	calls, err := imageCalls(job)
	require.NoError(t, err)
	require.Equal(t, 4, calls[0].n)

	// the drawing's media row is gone → refused while building (free), terminal
	_, err = buildJob(context.Background(), media(), nil, r, "medium")
	require.True(t, errors.Is(err, errFlatUnderdrawingGone), "%v", err)
	require.False(t, classify(err).Retryable)

	// hand_flat: one of two flats gone → refused, never silently «derived»
	h := testRun(2, entity.DesignRunKindFlat)
	h.Params = entity.RawJSON(`{"views":["front","back"],"layout":"one","flat":{"mode":"drawing","structure_source":"hand_flat","structure_refs":[{"media_id":7,"view":"front"},{"media_id":8,"view":"back"}]}}`)
	h.Inputs = entity.RawJSON(`{"refs":[{"media_id":7,"role":"underdrawing"},{"media_id":8,"role":"underdrawing"}]}`)
	_, err = buildJob(context.Background(), media(7), nil, h, "medium")
	require.True(t, errors.Is(err, errFlatUnderdrawingGone))
	job, err = buildJob(context.Background(), media(7, 8), nil, h, "medium")
	require.NoError(t, err)
	require.Contains(t, job.Prompt, "Image 1 is the designer's own hand-drawn technical flat of the garment's FRONT view.")
}

func TestFlatModeCandidates(t *testing.T) {
	two := []string{"front", "back"}
	require.Equal(t, 1, FlatCandidatesFor(two, layoutOne, ""))
	require.Equal(t, 1, FlatCandidatesFor(two, layoutOne, FlatModeQuick))
	require.Equal(t, FlatCandidates, FlatCandidatesFor(two, layoutOne, FlatModeDrawing))
	require.Equal(t, FlatCandidates, FlatCandidatesFor(two, layoutOne, FlatModeDrawingPhotos))
	require.Equal(t, 0, FlatCandidatesFor(two, layoutPerView, FlatModeDrawing))
	require.Equal(t, 0, FlatCandidatesFor([]string{"detail"}, layoutOne, FlatModeDrawing))
	for _, m := range []string{"", FlatModeQuick, FlatModeDrawing, FlatModeDrawingPhotos} {
		require.Equal(t, FlatDefaultEngine, FlatModelFor(m))
	}
	// a quick sheet queued before the modes with 4 outputs still buys 4 (frozen number)
	calls, _ := imageCalls(Job{Kind: "flat", Views: two, Layout: layoutOne, Outputs: 4})
	require.Equal(t, 4, calls[0].n)
	calls, _ = imageCalls(Job{Kind: "flat", Views: two, Layout: layoutOne, Outputs: 1, FlatMode: FlatModeDrawing})
	require.Equal(t, 1, calls[0].n, "a drawing fix is one picture")
	for in, want := range map[string]string{"": FlatModeQuick, "quick": FlatModeQuick, "drawing": FlatModeDrawing, " drawing_photos ": FlatModeDrawingPhotos} {
		got, ok := NormalizeFlatMode(in)
		require.True(t, ok)
		require.Equal(t, want, got)
	}
	_, ok := NormalizeFlatMode("trace")
	require.False(t, ok)
}

// The ruler is ONE table: the Go ruler equals the renderer's (testdata/joins/ruler.json, exported from
// v9_render.py BASE+SIDE; the client's `yarn ruler:export` overwrites it with the same numbers).
func TestRulerMatchesClientExport(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "joins", "ruler.json"))
	require.NoError(t, err)
	var f struct {
		Landmarks map[string][3]float64 `json:"landmarks"`
	}
	require.NoError(t, json.Unmarshal(raw, &f))
	require.Equal(t, f.Landmarks, entity.DesignJoinRuler())
	for _, n := range []string{"HIP_L", "BREAK", "CROTCH_F", "SKIRT_HEM_R", "HOOD_TOP", "LAPEL_TIP_L", "GORGE_R", "KANG_LOW_L", "KNEE_OUT_R", "LEG_HEM_F_L", "NECK_SIDE_L", "ELB_IN_R"} {
		require.True(t, strings.Contains(entity.DesignJoinLandmarkHelp, strings.TrimSuffix(strings.TrimSuffix(n, "_L"), "_R")), n)
	}
}

// v9's lists survive the cleaner whole (every item; the fit block).
func TestSanitizeKeepsV9KindsAndLandmarks(t *testing.T) {
	for _, c := range []string{"c2", "c8", "c11", "c16"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "joins", "v9-"+c+"-joins.json"))
		require.NoError(t, err)
		var probe struct {
			Items []json.RawMessage `json:"items"`
		}
		require.NoError(t, json.Unmarshal(raw, &probe))
		a, ok := entity.ParseDesignJoinsAnswer(string(raw))
		require.True(t, ok, c)
		doc := a.Doc()
		require.Len(t, doc.Items, len(probe.Items), c)
		require.NotNil(t, doc.Fit, c)
		for _, it := range doc.Items {
			for _, pt := range it.Path() {
				require.True(t, entity.IsDesignJoinLandmark(pt), "%s %s %s", c, it.ID, pt)
			}
		}
	}
	// the new kinds and landmarks survive; bands are bands
	doc := entity.SanitizeDesignJoinsDoc(entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{
		{ID: "hood", Kind: "hood_edge", From: "HOOD_FRONT_L", Via: []string{"HOOD_TOP"}, To: "HOOD_FRONT_R"},
		{ID: "lapel_L", Kind: "lapel", From: "BREAK", Via: []string{"LAPEL_TIP_L"}, To: "GORGE_L"},
		{ID: "dart", Kind: "dart", From: "BUSTSIDE_L", To: "BUST_C"},
		{ID: "kanga", Kind: "pocket", From: "KANG_L", Via: []string{"KANG_LOW_L", "KANG_LOW_R"}, To: "KANG_R", Size: 0.06},
		{ID: "leg", Kind: "edge", From: "LEG_HEM_F_L", To: "ANKLE_OUT_L"},
		{ID: "cord", Kind: "drawcord_channel", From: "WL_L", To: "WL_R"},
	}})
	require.Len(t, doc.Items, 6)
	require.True(t, entity.DesignJoinIsBand("hood_edge") && entity.DesignJoinIsBand("drawcord_channel") && entity.DesignJoinIsBand("binding_wide"))
	require.Equal(t, []string{"KANG_L", "KANG_LOW_L", "KANG_LOW_R", "KANG_R"}, doc.Items[3].Path(), "a shaped pocket keeps its path")
	require.Equal(t, 0.06, doc.Items[3].Size)
}

func TestFitRoundTrip(t *testing.T) {
	a, ok := entity.ParseDesignJoinsAnswer(`{"fit":{"ease":"Oversized","waist":"straight"},"items":[{"id":"hem","kind":"edge","path":["HEM_L","HEM_R"]}]}`)
	require.True(t, ok)
	require.Equal(t, &entity.DesignJoinsFit{Ease: "oversized", Waist: "straight"}, a.Doc().Fit)
	require.Nil(t, entity.SanitizeDesignJoinsFit(&entity.DesignJoinsFit{Ease: "huge", Waist: "wavy"}))
	require.Equal(t, &entity.DesignJoinsFit{Waist: "fitted"}, entity.SanitizeDesignJoinsFit(&entity.DesignJoinsFit{Ease: "huge", Waist: "fitted"}))
	b, _ := json.Marshal(a.Doc())
	var back entity.DesignJoinsDoc
	require.NoError(t, json.Unmarshal(b, &back))
	require.Equal(t, a.Doc().Fit, back.Fit, "the frozen snapshot keeps the fit")
}
