package designgen

import (
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ THE THREE FLAT MODES (tmp/plans/flat-consistency/80-BUILD-MODES.md) ═══
//
//   - quick: today's route — the card's kept photos + the frozen join list in words; ONE sheet.
//   - drawing: the construction drawing the client rendered from the join list (the underdrawing) is
//     the ONLY picture sent, and the model TRACES it (route D, 60-FABLE-ROUTE: 14/14 sheets); four
//     candidates.
//   - drawing_photos: the underdrawing + the kept photos, each photo captioned as a FIT authority
//     only (61-CODEX-ROUTE: 4/4 topology, 3/4 fit); four candidates.
//
// The mode is frozen in params.flat.mode; "" (every run before the modes) is quick for ever.
const (
	FlatModeQuick         = "quick"
	FlatModeDrawing       = "drawing"
	FlatModeDrawingPhotos = "drawing_photos"
)

// NormalizeFlatMode — the mode a stored word means ("" → quick); false for a word that is no mode.
func NormalizeFlatMode(mode string) (string, bool) {
	switch m := strings.TrimSpace(mode); m {
	case "", FlatModeQuick:
		return FlatModeQuick, true
	case FlatModeDrawing, FlatModeDrawingPhotos:
		return m, true
	}
	return "", false
}

// Structure sources of a drawing mode (params.flat.structure_source; "" = rendered).
const (
	FlatStructureRendered = "rendered"
	FlatStructureHandFlat = "hand_flat"
	// FlatMaxStructureRefs — how many hand-drawn flats one run may trace.
	FlatMaxStructureRefs = 4
)

// NormalizeFlatStructureSource — "" → rendered; false for a word that is no source.
func NormalizeFlatStructureSource(src string) (string, bool) {
	switch s := strings.TrimSpace(src); s {
	case "", FlatStructureRendered:
		return FlatStructureRendered, true
	case FlatStructureHandFlat:
		return s, true
	}
	return "", false
}

// FlatIsDrawingMode — the mode traces the underdrawing (drawing | drawing_photos).
func FlatIsDrawingMode(mode string) bool {
	m := strings.TrimSpace(mode)
	return m == FlatModeDrawing || m == FlatModeDrawingPhotos
}

// FlatModelFor — the engine a NEW flat press of this mode is drawn by when the person named none. One
// switch, so a cheaper quick slug is a one-line change (80-BUILD-MODES §3.4); today flare for all three.
func FlatModelFor(mode string) string {
	switch strings.TrimSpace(mode) {
	case FlatModeDrawing, FlatModeDrawingPhotos:
		return FlatDefaultEngine
	default:
		return FlatDefaultEngine
	}
}

// underdrawingCaption — the words the references block says about the construction drawing.
const underdrawingCaption = "construction drawing (the underdrawing rendered from the join list — trace it)"

// JoinsListText — the ruler, the layers, every item on one line, the absences (r5.list_text). The
// labeller's construction block reads it too (admin design_parts_card.go).
func JoinsListText(j entity.DesignJoinsDoc) string { return joinsListText(j) }

// JoinsUsable — whether a list says anything a prompt can use (at least one item or absence).
func JoinsUsable(j *entity.DesignJoinsDoc) bool { return flatJoinsUsable(j) }

// The trace core (60-FABLE-ROUTE out/f8/d/prompt-Dflare.txt, generalised: its card-38 clauses moved
// into the CHECK sentences the join list implies). ⚠ The bullets and the rendering paragraph are the
// probe's words; a test holds a literal copy. %IMG% is «image k» (one structure picture) or «images k
// and j» (the designer's flats).
const (
	flatTraceKeep    = "- keep every edge, band, strap, seam, hem and dashed line exactly where %IMG% has it, with the same proportions and the same position of each view;"
	flatTraceAddNone = "- do not add ANY line, edge, band, panel, neckline, shoulder, seam or detail that is not in %IMG%; open areas stay plain white;"
	flatTraceRemove  = "- do not remove or straighten anything; where %IMG% shows a strap, the finished drawing shows a strap of the same width in the same place."
	flatTraceRender  = "Improve only the rendering: uniform precise vector-style line work, heavier weight for outer contours, thin lines for internal design lines, fine dashed lines for topstitching and seam stitching; subtle body-form shaping of the outline following the given silhouette; cloth drawn white."

	// 61-CODEX-ROUTE out/cx8/prompt-role.txt, generalised.
	flatRolesHead  = "Input roles are strict and non-interchangeable:"
	flatRolesAdapt = "Unlike a literal trace, adjust only the continuous OUTER-SILHOUETTE geometry and the rendering of details to the photos: reproduce the fit (waist suppression, ease, bust/hip curvature, body and sleeve length), the proportions of pockets, collars and bands, hems, cuffs, topstitching and hardware as seen on the real garment. The photos must NOT change the construction: where a photo disagrees with %IMG% about a connection, an endpoint, an opening or a view's orientation, %IMG% is right."
)

func withImg(s, img string) string { return strings.ReplaceAll(s, "%IMG%", img) }

// structImage — one structure picture among the attached: its 1-based number and the view it shows
// ("" = a rendered sheet of every view).
type structImage struct {
	n    int
	view string
}

func structImages(attached []refCaption) []structImage {
	var out []structImage
	for i, rc := range attached {
		if rc.IsUnderdrawing {
			out = append(out, structImage{n: i + 1, view: rc.StructView})
		}
	}
	return out
}

// underdrawingNumber — the 1-based number of the first structure picture; 0 when none attached.
func underdrawingNumber(attached []refCaption) int {
	if s := structImages(attached); len(s) > 0 {
		return s[0].n
	}
	return 0
}

// flatStructureAttached — every structure picture the frozen params name reached the job: a rendered
// sheet, or EVERY hand-drawn flat (a missing one would silently become a derived view).
func flatStructureAttached(p runParams, attached []refCaption) bool {
	got := len(structImages(attached))
	if flatStructureOf(p) == FlatStructureHandFlat && p.Flat != nil {
		want := 0
		seen := map[int]bool{}
		for _, r := range p.Flat.StructureRefs {
			if r.MediaID > 0 && !seen[r.MediaID] {
				seen[r.MediaID] = true
				want++
			}
		}
		return want > 0 && got == want
	}
	return got > 0
}

// imagesLabel — «image 1» / «images 1 and 2» / «images 1, 2 and 3».
func imagesLabel(st []structImage) string {
	if len(st) == 0 {
		return "image 1"
	}
	if len(st) == 1 {
		return "image " + strconv.Itoa(st[0].n)
	}
	nums := make([]string, len(st))
	for i, x := range st {
		nums[i] = strconv.Itoa(x.n)
	}
	return "images " + strings.Join(nums[:len(nums)-1], ", ") + " and " + nums[len(nums)-1]
}

// flatTraceCraft — the craft of a DRAWING-mode flat (80-BUILD-MODES §3.2/3.3): the trace core (by
// structure source), the photo roles (drawing_photos), the CHECK sentences of the frozen list, then the
// owner's style / exclusions / output verbatim. No flatIntro / identification paragraph: they speak to
// photos.
func flatTraceCraft(p runParams, detailNames []string, attached []refCaption, joins *entity.DesignJoinsDoc, mode string) string {
	st := structImages(attached)
	img := imagesLabel(st)
	var paras []string
	if flatStructureOf(p) == FlatStructureHandFlat {
		paras = flatHandFlatCore(p.Views, detailNames, st, img)
	} else {
		k := 1
		if len(st) > 0 {
			k = st[0].n
		}
		paras = []string{
			flatTraceOpening(p.Views, detailNames, k),
			"Redraw image " + strconv.Itoa(k) + " as a finished professional fashion technical flat sketch (CAD-style tech pack drawing) in the same layout. This is a TRACING job, not a design job:\n" +
				withImg(flatTraceKeep, img) + "\n" + withImg(flatTraceAddNone, img) + "\n" + withImg(flatTraceRemove, img),
			flatTraceRender,
		}
	}
	if mode == FlatModeDrawingPhotos {
		if roles := flatPhotoRolesParagraph(attached, st, img); roles != "" {
			paras = append(paras, roles)
		}
	}
	if flatJoinsUsable(joins) {
		paras = append(paras, "CHECK EVERY VIEW AGAINST "+strings.ToUpper(img)+" AND THESE BEFORE DRAWING:\n- "+
			strings.Join(joinsSentences(*joins), "\n- "))
	}
	paras = append(paras, flatNoTextNoGrey, flatStyleGarment, flatExcludedGarment, flatOutput)
	return strings.Join(paras, "\n\n")
}

// flatTraceOpening — paragraph 1 of a RENDERED trace: what image k is and which views it holds, left
// to right, in params.views order (the order the splitter labels the cut frames by).
func flatTraceOpening(views, detailNames []string, k int) string {
	names := displayViews(views, detailNames)
	img := "Image " + strconv.Itoa(k) + " is the exact construction drawing of a garment, "
	switch len(names) {
	case 0:
		return img + "already laid out as a technical-flat sheet."
	case 1:
		return img + "already laid out as a single technical-flat view: " + names[0] + "."
	}
	return img + "already laid out as " + countWord(len(names)) + " technical-flat views on one canvas, left to right: " +
		strings.Join(names, ", ") + "."
}

// flatHandFlatCore — the trace core when the structure is the DESIGNER'S OWN flats (structure_source
// hand_flat): name each flat and its view, lay out the requested sheet, trace the drawn views, derive
// the missing ones consistently.
func flatHandFlatCore(views, detailNames []string, st []structImage, img string) []string {
	var who []string
	drawn := map[string]bool{}
	for _, x := range st {
		who = append(who, "Image "+strconv.Itoa(x.n)+" is the designer's own hand-drawn technical flat of the garment's "+displayView(x.view)+" view.")
		drawn[x.view] = true
	}
	names := displayViews(views, detailNames)
	var missing []string
	for i, v := range views {
		if !drawn[v] {
			missing = append(missing, names[i])
		}
	}
	layout := "Draw ONE finished sheet: "
	if len(names) == 1 {
		layout += "a single view — " + names[0] + " — isolated and centered on the canvas."
	} else {
		layout += countWord(len(names)) + " views on one horizontal canvas, side by side, equal scale, aligned on a common baseline, evenly spaced — left to right: " + strings.Join(names, ", ") + "."
	}
	trace := "Redraw the designer's flat" + map[bool]string{true: "s", false: ""}[len(st) > 1] + " (" + img +
		") cleanly as a finished professional fashion technical flat sketch (CAD-style tech pack drawing). For every view the designer drew this is a TRACING job, not a design job:\n" +
		withImg(flatTraceKeep, img) + "\n" + withImg(flatTraceAddNone, img) + "\n" + withImg(flatTraceRemove, img)
	out := []string{strings.Join(who, " ") + " " + layout, trace}
	if len(missing) > 0 {
		out = append(out, "Derive the views the designer did not draw ("+strings.Join(missing, ", ")+
			") yourself, consistent with "+img+": the same construction, the same proportions and length, every edge, seam, band, strap, pocket and closure where the designer's flats put it, seen from that side; add nothing they do not imply. "+flatSideFacing)
	}
	out = append(out, flatTraceRender)
	return out
}

// flatPhotoRolesParagraph — drawing_photos: the structure pictures own the construction, each photo
// owns only the fit of its side. "" when no photo attached (the run is then a plain trace).
func flatPhotoRolesParagraph(attached []refCaption, st []structImage, img string) string {
	var lines []string
	for i, rc := range attached {
		if rc.IsUnderdrawing || !rc.FromRef {
			continue
		}
		label, words := flatPhotoRole(rc.Role)
		inner := words
		if n := strings.TrimSpace(oneLine(rc.Note)); n != "" {
			inner += " — " + n
		}
		line := "- Image " + strconv.Itoa(i+1) + " — " + label + " FIT AND DETAIL AUTHORITY only (" + inner + "); never a source of construction"
		if rc.Role == entity.DesignViewSideL || rc.Role == entity.DesignViewSideR {
			line += "; mirror its depth profile to " + img + "'s side orientation"
		}
		lines = append(lines, line+".")
	}
	if len(lines) == 0 {
		return ""
	}
	var head []string
	for _, x := range st {
		what := "the view layout"
		if x.view != "" {
			what = "the " + displayView(x.view) + " view"
		}
		head = append(head, "- Image "+strconv.Itoa(x.n)+" — STRUCTURE AUTHORITY: controls every endpoint, connection, crossing, opening, seam, pocket, closure and "+what+".")
	}
	return flatRolesHead + "\n" + strings.Join(head, "\n") + "\n" + strings.Join(lines, "\n") + "\n" + withImg(flatRolesAdapt, img)
}

// flatPhotoRole — the authority label and the role words of a photo by its reference role.
func flatPhotoRole(role string) (string, string) {
	switch role {
	case entity.DesignViewFront:
		return "FRONT", "front photo"
	case entity.DesignViewBack:
		return "BACK", "back photo"
	case entity.DesignViewSideL:
		return "SIDE", "left side photo, the wearer's LEFT flank"
	case entity.DesignViewSideR:
		return "SIDE", "right side photo, the wearer's RIGHT flank"
	case entity.DesignViewThreeQuarterL:
		return "THREE-QUARTER", "three-quarter photo from the wearer's left"
	case entity.DesignViewThreeQuarterR:
		return "THREE-QUARTER", "three-quarter photo from the wearer's right"
	case entity.DesignViewDetail:
		return "DETAIL", "detail close-up"
	}
	return "MOOD", "reference photo"
}
