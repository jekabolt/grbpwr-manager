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

func structRC(n int, view string) refCaption {
	return refCaption{MediaID: n, IsStructure: true, StructView: view}
}

func photo(n int, role, note string) refCaption {
	return refCaption{MediaID: n, Caption: role, FromRef: true, Role: role, Note: note}
}

func handParams(views []string) runParams {
	return runParams{Views: views, Layout: layoutOne, Flat: &flatParams{Mode: FlatModeHandFlat}}
}

// The hand_flat craft: name the designer's flats, trace them (the F8 bullets, literal), derive the views
// they do not cover, the photos own only the fit, the owner's paragraphs close it.
func TestHandFlatCraft(t *testing.T) {
	att := []refCaption{structRC(7, "front"), structRC(8, "back")}
	got := flatHandFlatCraft(handParams(fourViews), nil, att)
	for _, s := range []string{
		"Image 1 is the designer's own hand-drawn technical flat of the garment's FRONT view. Image 2 is the designer's own hand-drawn technical flat of the garment's BACK view. Draw ONE finished sheet: four views on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced — left to right: FRONT, BACK, SIDE LEFT, SIDE RIGHT.",
		"Redraw the designer's flats (images 1 and 2) cleanly as a finished professional fashion technical flat sketch (CAD-style tech pack drawing). For every view the designer drew this is a TRACING job, not a design job:\n" +
			"- keep every edge, band, strap, seam, hem and dashed line exactly where images 1 and 2 has it, with the same proportions and the same position of each view;\n" +
			"- do not add ANY line, edge, band, panel, neckline, shoulder, seam or detail that is not in images 1 and 2; open areas stay plain white;\n" +
			"- do not remove or straighten anything; where images 1 and 2 shows a strap, the finished drawing shows a strap of the same width in the same place.",
		"Derive the views the designer did not draw (SIDE LEFT, SIDE RIGHT) yourself, consistent with images 1 and 2",
		flatSideFacing, flatTraceRender,
	} {
		require.Contains(t, got, s)
	}
	require.True(t, strings.HasSuffix(got, flatNoTextNoGrey+"\n\n"+flatStyleGarment+"\n\n"+flatExcludedGarment+"\n\n"+flatOutput))
	require.NotContains(t, got, flatRolesHead, "no photo, no roles")
	got2 := flatHandFlatCraft(handParams([]string{"front"}), nil, att[:1])
	require.NotContains(t, got2, "Derive the views")
	require.Contains(t, got2, "a single view — FRONT —")
	roles := flatHandFlatCraft(handParams(fourViews), nil, append(att, photo(11, "front", "the waist"), photo(12, "side_r", "")))
	want := `Input roles are strict and non-interchangeable:
- Image 1 — STRUCTURE AUTHORITY: controls every endpoint, connection, crossing, opening, seam, pocket, closure and the FRONT view.
- Image 2 — STRUCTURE AUTHORITY: controls every endpoint, connection, crossing, opening, seam, pocket, closure and the BACK view.
- Image 3 — FRONT FIT AND DETAIL AUTHORITY only (front photo — the waist); never a source of construction.
- Image 4 — SIDE FIT AND DETAIL AUTHORITY only (side photo, the wearer's RIGHT flank); never a source of construction; mirror its depth profile to the side views you derive.
Unlike a literal trace, adjust only the continuous OUTER-SILHOUETTE geometry and the rendering of details to the photos: reproduce the fit (waist suppression, ease, bust/hip curvature, body and sleeve length), the proportions of pockets, collars and bands, hems, cuffs, topstitching and hardware as seen on the real garment. The photos must NOT change the construction: where a photo disagrees with images 1 and 2 about a connection, an endpoint, an opening or a view's orientation, images 1 and 2 is right.`
	require.Contains(t, roles, "\n\n"+want+"\n\n")
}

// The photos route and straps compose the SAME text route (flatCraftWith); the mode only changes the
// count and the door. A structure-role ref never travels outside hand_flat.
func TestPhotosAndStrapsAreTheTextRoute(t *testing.T) {
	j := loadJoinsCase(t, "c38")
	inputs := runInputs{Refs: []inputRef{{MediaID: 11, Role: "front"}, {MediaID: 12, Role: "back", Note: "open back"}}, Joins: &j}
	base := runParams{Views: fourViews, Layout: layoutOne}
	straps := base
	straps.Flat = &flatParams{Mode: FlatModeStraps}
	run := entity.DesignRun{Kind: entity.DesignRunKindFlat}
	att := referenceList(run.Kind, base, inputs)
	require.Equal(t, att, referenceList(run.Kind, straps, inputs))
	a := composePrompt(run, base, inputs, att)
	require.Equal(t, a, composePrompt(run, straps, inputs, att))
	require.Contains(t, a, flatCraftWith(base, nil, 2, &j))
	require.Contains(t, a, "- image 1: front photo\n- image 2: back photo: open back\n")

	withFlat := inputs
	withFlat.Refs = append([]inputRef{{MediaID: 5, Role: entity.DesignRefRoleFrontFlat}}, inputs.Refs...)
	for _, rc := range referenceList(run.Kind, base, withFlat) {
		require.NotEqual(t, 5, rc.MediaID, "a photos run never sends the designer's flat as a structure")
	}
}

func TestFlatRefCaptions(t *testing.T) {
	require.Equal(t, "detail photo: the crossed straps on the back", flatRefCaption(inputRef{Role: "detail", Note: "the crossed straps on the back"}))
	require.Equal(t, "side photo (wearer's right flank)", flatRefCaption(inputRef{Role: "side_r"}))
	require.Equal(t, "mood reference screenshot from the card (a DIFFERENT garment; style mood only, not this construction)", flatRefCaption(inputRef{Role: "mood"}))
	require.Equal(t, "front photo: only the cut [collar; hem]", flatRefCaption(inputRef{Role: "front", Note: "only the\ncut", Callouts: []inputCallout{{Text: "collar"}, {Text: "hem"}}}))
}

func TestHandFlatReferences(t *testing.T) {
	in := parseInputs(entity.RawJSON(`{"refs":[{"media_id":8,"role":"back_flat"},{"media_id":7,"role":"front_flat"},{"media_id":11,"role":"front"}],"slots":[{"view_key":"front","media_id":21}]}`))
	got := referenceList(entity.DesignRunKindFlat, parseParams(entity.RawJSON(
		`{"views":["front","back","side_l"],"layout":"one","flat":{"mode":"hand_flat","structure_refs":[{"media_id":7,"role":"front_flat"},{"media_id":8,"role":"back_flat"}]}}`)), in)
	require.Len(t, got, 3)
	require.Equal(t, []int{7, 8, 11}, []int{got[0].MediaID, got[1].MediaID, got[2].MediaID}, "the flats in params order, then the photos; no plates")
	require.Equal(t, "front", got[0].StructView)
	require.Equal(t, "back", got[1].StructView)
	fix := referenceList(entity.DesignRunKindFlat, parseParams(entity.RawJSON(
		`{"views":["front","back"],"layout":"one","fix_targets":["front"],"flat":{"mode":"hand_flat","structure_refs":[{"media_id":7,"role":"front_flat"}]}}`)), in)
	require.Equal(t, 21, fix[len(fix)-1].MediaID, "a fix carries the plate it corrects, last")
}

func TestBuildJobHandFlat(t *testing.T) {
	h := testRun(2, entity.DesignRunKindFlat)
	h.RequestedOutputs = 2
	h.Params = entity.RawJSON(`{"views":["front","back"],"layout":"one","flat":{"mode":"hand_flat","structure_refs":[{"media_id":7,"role":"front_flat"},{"media_id":8,"role":"back_flat"}]}}`)
	h.Inputs = entity.RawJSON(`{"refs":[{"media_id":7,"role":"front_flat"},{"media_id":8,"role":"back_flat"}]}`)
	_, err := buildJob(context.Background(), media(7), nil, h, "medium")
	require.True(t, errors.Is(err, errFlatStructureGone), "one flat gone → refused, never silently derived")
	require.False(t, classify(err).Retryable)
	job, err := buildJob(context.Background(), media(7, 8), nil, h, "medium")
	require.NoError(t, err)
	require.Equal(t, FlatModeHandFlat, job.FlatMode)
	require.Contains(t, job.Prompt, "Image 1 is the designer's own hand-drawn technical flat of the garment's FRONT view.")
	calls, err := imageCalls(job)
	require.NoError(t, err)
	require.Equal(t, 2, calls[0].n)
}

func TestFlatModeCandidates(t *testing.T) {
	two := []string{"front", "back"}
	require.Equal(t, 2, FlatCandidatesFor(two, layoutOne, ""))
	require.Equal(t, 2, FlatCandidatesFor(two, layoutOne, "photos"))
	require.Equal(t, 2, FlatCandidatesFor(two, layoutOne, FlatModeHandFlat))
	require.Equal(t, FlatCandidates, FlatCandidatesFor(two, layoutOne, FlatModeStraps))
	require.Equal(t, 0, FlatCandidatesFor(two, layoutPerView, FlatModeStraps))
	require.Equal(t, 0, FlatCandidatesFor([]string{"detail"}, layoutOne, FlatModeStraps))
	for _, m := range []string{"", FlatModeHandFlat, FlatModeStraps} {
		require.Equal(t, FlatDefaultEngine, FlatModelFor(m))
	}
	calls, _ := imageCalls(Job{Kind: "flat", Views: two, Layout: layoutOne, Outputs: 4})
	require.Equal(t, 4, calls[0].n, "a sheet queued before the modes keeps its frozen number")
	calls, _ = imageCalls(Job{Kind: "flat", Views: two, Layout: layoutOne, Outputs: 1, FlatMode: FlatModeStraps})
	require.Equal(t, 1, calls[0].n, "a fix is one picture")
	_, ok := NormalizeFlatMode("drawing")
	require.False(t, ok)
}

// Sentence types 1 and 6 beyond the c2 golden: every neck shape speaks, the designer's edits are said
// verbatim.
func TestNeckShapesAndDesignerLines(t *testing.T) {
	for shape, want := range map[string]string{
		"v": "is a V neck", "scoop": "SCOOP neck", "halter": "HALTER", "boat": "BOAT neck",
		"square": "SQUARE neck", "mock": "MOCK NECK", "turtle": "TURTLENECK",
	} {
		doc := entity.SanitizeDesignJoinsDoc(entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{
			{ID: "neck", Kind: "binding", From: "NP_R", Via: []string{"CFN"}, To: "NP_L", Type: shape}}})
		require.Contains(t, strings.Join(joinsSentences(doc), "\n"), want, shape)
	}
	doc := entity.SanitizeDesignJoinsDoc(entity.DesignJoinsDoc{Items: []entity.DesignJoinItem{
		{ID: "hem", Kind: "edge", From: "HEM_L", To: "HEM_R", Text: "raw hem, cut 2 cm longer at the back"}}})
	doc.Items[0].Edited = true
	doc.EditedAbsences = []string{"no pocket on the left"}
	got := joinsSentences(doc)
	require.Equal(t, []string{"designer: raw hem, cut 2 cm longer at the back", "designer: no pocket on the left"}, got[len(got)-2:])
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

// TestMoodIsNeverAnAuthority — M2 + Codex b6: a mood picture (a DIFFERENT garment) is never a fit or
// detail authority. hand_flat: it is left out of the roles list and said once, as style mood only;
// photos/straps: the craft says the same right after the identification, and the owner's «true to the
// reference» counts only the other pictures (a run whose only picture is mood gets the no-reference
// wording). Golden: the exact paragraphs.
// MUTATIONS IT CATCHES: «MOOD FIT AND DETAIL AUTHORITY» coming back; the mood sentence missing in any
// mode; a mood picture counted as «the reference image»; a run without mood changing by a byte.
func TestMoodIsNeverAnAuthority(t *testing.T) {
	att := []refCaption{structRC(7, "front"), photo(11, "front", ""), photo(12, entity.DesignRefRoleMood, "")}
	got := flatHandFlatCraft(handParams(fourViews), nil, att)
	require.NotContains(t, got, "MOOD FIT AND DETAIL AUTHORITY")
	require.NotContains(t, got, "- Image 3 —")
	wantMood := "Image 3 is a mood picture of a DIFFERENT garment: style mood only. Take NOTHING of this garment from it — no silhouette, no fit, no proportions, no length, no details, no straps, no construction; everything said above about the reference applies to the other images only."
	require.Contains(t, got, "image 1 is right.\n\n"+wantMood+"\n\n"+flatNoTextNoGrey)
	require.Contains(t, got, "- Image 2 — FRONT FIT AND DETAIL AUTHORITY only (front photo); never a source of construction.")

	// only mood beside the flat: no roles paragraph at all, the mood sentence still
	onlyMood := flatHandFlatCraft(handParams(fourViews), nil, []refCaption{structRC(7, "front"), photo(12, entity.DesignRefRoleMood, "")})
	require.NotContains(t, onlyMood, flatRolesHead)
	require.Contains(t, onlyMood, "Image 2 is a mood picture of a DIFFERENT garment: style mood only.")

	// photos / straps
	p := runParams{Views: fourViews, Layout: layoutOne}
	plain := []refCaption{photo(11, "front", ""), photo(13, "back", "")}
	require.Equal(t, flatCraftWith(p, nil, 2, nil), flatCraftAttached(p, nil, plain, nil), "no mood: byte for byte the old craft")
	withMood := flatCraftAttached(p, nil, append(plain, photo(12, entity.DesignRefRoleMood, ""), photo(14, entity.DesignRefRoleMood, "")), nil)
	require.Contains(t, withMood, flatIdentifyGarment+"\n\nImages 3 and 4 are mood pictures of a DIFFERENT garment: style mood only. Take NOTHING of this garment from them — no silhouette, no fit, no proportions, no length, no details, no straps, no construction; everything said above about the reference applies to the other images only.\n\n")
	require.True(t, strings.HasPrefix(withMood, flatIntro(false, 0, 2)), "two garment pictures, not four")
	moodOnly := flatCraftAttached(p, nil, []refCaption{photo(12, entity.DesignRefRoleMood, "")}, nil)
	require.Contains(t, moodOnly, flatIdentifyGarmentNoRef, "a mood picture is not the reference the garment is true to")
	require.Contains(t, moodOnly, "Image 1 is a mood picture of a DIFFERENT garment")
}
