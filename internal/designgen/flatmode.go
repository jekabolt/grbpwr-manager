package designgen

import (
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ THE THREE FLAT MODES (tmp/plans/flat-consistency/81-FINAL-MODES.md) ═══
//
//   - "" (photos, the default): the card's kept photos with roles and notes + the join list in words
//     when the card has one; two candidate sheets.
//   - hand_flat: the card's own hand-drawn technical flats (front_flat / back_flat) are redrawn
//     cleanly and the missing views derived; the kept photos travel for fit only; no join list.
//   - straps: the photos route with the designer-CONFIRMED join list.
//
// Every mode buys ONE sheet (wave 10: the candidate quiz is gone).
//
// The mode is frozen in params.flat.mode; "" (and every run before the modes) is the photos route.
const (
	FlatModePhotos   = ""
	FlatModeHandFlat = "hand_flat"
	FlatModeStraps   = "straps"
	// FlatMaxStructureRefs — the hand-drawn flats one run may trace (front + back).
	FlatMaxStructureRefs = 2
)

// NormalizeFlatMode — the mode a stored word means ("" and "photos" → ""); false for a word that is no
// mode.
func NormalizeFlatMode(mode string) (string, bool) {
	switch m := strings.TrimSpace(mode); m {
	case "", "photos":
		return FlatModePhotos, true
	case FlatModeHandFlat, FlatModeStraps:
		return m, true
	}
	return "", false
}

// FlatModelFor — the engine a NEW flat press of this mode is drawn by when the person named none. One
// switch, so a cheaper slug for one mode is a one-line change; today flare for all three.
func FlatModelFor(mode string) string {
	switch strings.TrimSpace(mode) {
	case FlatModeHandFlat, FlatModeStraps:
		return FlatDefaultEngine
	default:
		return FlatDefaultEngine
	}
}

// FlatStructureView — the view a structure role shows ("" for no structure role).
func FlatStructureView(role string) string {
	switch role {
	case entity.DesignRefRoleFrontFlat:
		return entity.DesignViewFront
	case entity.DesignRefRoleBackFlat:
		return entity.DesignViewBack
	}
	return ""
}

// JoinsListText — the ruler, the layers, every item on one line, the absences (r5.list_text). The
// labeller's construction block reads it too (admin design_parts_construction.go).
func JoinsListText(j entity.DesignJoinsDoc) string { return joinsListText(j) }

// JoinsUsable — whether a list says anything a prompt can use (at least one item or absence).
func JoinsUsable(j *entity.DesignJoinsDoc) bool { return flatJoinsUsable(j) }

// The trace bullets (60-FABLE-ROUTE out/f8/d/prompt-Dflare.txt, generalised). %IMG% is «image k» or
// «images k and j». A test holds a literal copy.
const (
	flatTraceKeep    = "- keep every edge, band, strap, seam, hem and dashed line exactly where %IMG% has it, with the same proportions and the same position of each view;"
	flatTraceAddNone = "- do not add ANY line, edge, band, panel, neckline, shoulder, seam or detail that is not in %IMG%; open areas stay plain white;"
	flatTraceRemove  = "- do not remove or straighten anything; where %IMG% shows a strap, the finished drawing shows a strap of the same width in the same place."
	flatTraceRender  = "Improve only the rendering: uniform precise vector-style line work, heavier weight for outer contours, thin lines for internal design lines, fine dashed lines for topstitching and seam stitching; the outline follows the given silhouette exactly; cloth drawn white."

	// 61-CODEX-ROUTE out/cx8/prompt-role.txt, generalised.
	flatRolesHead  = "Input roles are strict and non-interchangeable:"
	flatRolesAdapt = "Unlike a literal trace, adjust only the continuous OUTER-SILHOUETTE geometry and the rendering of details to the photos: reproduce the fit (waist suppression, ease, bust/hip curvature, body and sleeve length), the proportions of pockets, collars and bands, hems, cuffs, topstitching and hardware as seen on the real garment. The photos must NOT change the construction: where a photo disagrees with %IMG% about a connection, an endpoint, an opening or a view's orientation, %IMG% is right."
)

func withImg(s, img string) string { return strings.ReplaceAll(s, "%IMG%", img) }

// structImage — one structure flat among the attached: its 1-based number and the view it shows.
type structImage struct {
	n    int
	view string
}

func structImages(attached []refCaption) []structImage {
	var out []structImage
	for i, rc := range attached {
		if rc.IsStructure {
			out = append(out, structImage{n: i + 1, view: rc.StructView})
		}
	}
	return out
}

// flatStructureAttached — EVERY hand-drawn flat the frozen params name reached the job (a missing one
// would silently become a «derived» view).
func flatStructureAttached(p runParams, attached []refCaption) bool {
	if p.Flat == nil {
		return false
	}
	want := map[int]bool{}
	for _, r := range p.Flat.StructureRefs {
		if r.MediaID > 0 {
			want[r.MediaID] = true
		}
	}
	got := 0
	for _, rc := range attached {
		if rc.IsStructure && want[rc.MediaID] {
			got++
		}
	}
	return len(want) > 0 && got == len(want)
}

// imagesLabel — «image 1» / «images 1 and 2».
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

// flatHandFlatCraft — the craft of a hand_flat run: name each designer's flat and its view, lay out the
// requested sheet, trace the drawn views, derive the missing ones; the photos own only the fit; then the
// owner's style / exclusions / output verbatim.
func flatHandFlatCraft(p runParams, detailNames []string, attached []refCaption) string {
	st := structImages(attached)
	img := imagesLabel(st)
	var who []string
	drawn := map[string]bool{}
	for _, x := range st {
		who = append(who, "Image "+strconv.Itoa(x.n)+" is the designer's own hand-drawn technical flat of the garment's "+displayView(x.view)+" view.")
		drawn[x.view] = true
	}
	names := displayViews(p.Views, detailNames)
	var missing []string
	for i, v := range p.Views {
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
	plural := ""
	if len(st) > 1 {
		plural = "s"
	}
	paras := []string{
		strings.Join(who, " ") + " " + layout,
		"Redraw the designer's flat" + plural + " (" + img + ") cleanly as a finished professional fashion technical flat sketch (CAD-style tech pack drawing). For every view the designer drew this is a TRACING job, not a design job:\n" +
			withImg(flatTraceKeep, img) + "\n" + withImg(flatTraceAddNone, img) + "\n" + withImg(flatTraceRemove, img),
	}
	if len(missing) > 0 {
		paras = append(paras, "Derive the views the designer did not draw ("+strings.Join(missing, ", ")+
			") yourself, consistent with "+img+": the same construction, the same proportions and length, every edge, seam, band, strap, pocket and closure where the designer's flats put it, seen from that side; add nothing they do not imply. "+flatSideFacing)
	}
	paras = append(paras, flatTraceRender)
	if roles := flatPhotoRolesParagraph(attached, st, img); roles != "" {
		paras = append(paras, roles)
	}
	if mood := flatMoodSentence(attached); mood != "" {
		paras = append(paras, mood)
	}
	paras = append(paras, flatNoTextNoGrey, flatStyleGarment, flatExcludedGarment, flatOutput)
	return strings.Join(paras, "\n\n")
}

// flatPhotoRolesParagraph — the designer's flats own the construction, each photo owns only the fit of
// its side. "" when no photo attached.
func flatPhotoRolesParagraph(attached []refCaption, st []structImage, img string) string {
	var lines []string
	for i, rc := range attached {
		if rc.IsStructure || !rc.FromRef || rc.Role == entity.DesignRefRoleMood {
			// A mood picture is a DIFFERENT garment: never a fit or detail authority (M2) — it is
			// said once, in flatMoodSentence, after this paragraph.
			continue
		}
		label, words := flatPhotoRole(rc.Role)
		inner := words
		if n := strings.TrimSpace(oneLine(rc.Note)); n != "" {
			inner += " — " + n
		}
		line := "- Image " + strconv.Itoa(i+1) + " — " + label + " FIT AND DETAIL AUTHORITY only (" + inner + "); never a source of construction"
		if rc.Role == entity.DesignViewSideL || rc.Role == entity.DesignViewSideR {
			line += "; mirror its depth profile to the side views you derive"
		}
		lines = append(lines, line+".")
	}
	if len(lines) == 0 {
		return ""
	}
	var head []string
	for _, x := range st {
		head = append(head, "- Image "+strconv.Itoa(x.n)+" — STRUCTURE AUTHORITY: controls every endpoint, connection, crossing, opening, seam, pocket, closure and the "+displayView(x.view)+" view.")
	}
	return flatRolesHead + "\n" + strings.Join(head, "\n") + "\n" + strings.Join(lines, "\n") + "\n" + withImg(flatRolesAdapt, img)
}

// flatMoodImages — the 1-based numbers of the attached mood pictures (a reference the snapshot marks
// `mood`: a picture of a DIFFERENT garment from the card's moodboard).
func flatMoodImages(attached []refCaption) []int {
	var out []int
	for i, rc := range attached {
		if rc.FromRef && !rc.IsStructure && rc.Role == entity.DesignRefRoleMood {
			out = append(out, i+1)
		}
	}
	return out
}

// flatMoodSentence — what EVERY flat mode says about its mood pictures (M2 + Codex b6): style mood
// only, never a source of silhouette, fit, proportions, details or construction. "" when none.
func flatMoodSentence(attached []refCaption) string {
	nums := flatMoodImages(attached)
	if len(nums) == 0 {
		return ""
	}
	words := make([]string, len(nums))
	for i, n := range nums {
		words[i] = strconv.Itoa(n)
	}
	label, verb, what := "Image "+words[0], "is a mood picture", "it"
	if len(nums) > 1 {
		label = "Images " + strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
		verb, what = "are mood pictures", "them"
	}
	return label + " " + verb + " of a DIFFERENT garment: style mood only. Take NOTHING of this garment from " + what +
		" — no silhouette, no fit, no proportions, no length, no details, no straps, no construction; everything said above about the reference applies to the other images only."
}

// flatPhotoRole — the authority label and the role words of a photo by its reference role.
func flatPhotoRole(role string) (string, string) {
	switch role {
	case entity.DesignViewFront:
		return "FRONT", "front photo"
	case entity.DesignViewBack:
		return "BACK", "back photo"
	case entity.DesignViewSideL:
		return "SIDE", "side photo, the wearer's LEFT flank"
	case entity.DesignViewSideR:
		return "SIDE", "side photo, the wearer's RIGHT flank"
	case entity.DesignViewThreeQuarterL:
		return "THREE-QUARTER", "three-quarter photo from the wearer's left"
	case entity.DesignViewThreeQuarterR:
		return "THREE-QUARTER", "three-quarter photo from the wearer's right"
	case entity.DesignViewDetail:
		return "DETAIL", "detail photo"
	case entity.DesignRefRoleMood:
		return "MOOD", "a DIFFERENT garment; style mood only"
	}
	return "REFERENCE", "reference photo"
}

// flatRefCaption — the words a flat run says about one reference photo (81-FINAL-MODES): its role, then
// the designer's note («detail photo: the crossed straps on the back»), then the callouts; a mood
// screenshot is named as a DIFFERENT garment.
func flatRefCaption(r inputRef) string {
	var line string
	switch r.Role {
	case entity.DesignViewFront:
		line = "front photo"
	case entity.DesignViewBack:
		line = "back photo"
	case entity.DesignViewSideL:
		line = "side photo (wearer's left flank)"
	case entity.DesignViewSideR:
		line = "side photo (wearer's right flank)"
	case entity.DesignViewThreeQuarterL:
		line = "three-quarter photo (from the wearer's left)"
	case entity.DesignViewThreeQuarterR:
		line = "three-quarter photo (from the wearer's right)"
	case entity.DesignViewDetail:
		line = "detail photo"
	case entity.DesignRefRoleMood:
		line = "mood reference screenshot from the card (a DIFFERENT garment; style mood only, not this construction)"
	case "":
		line = "reference image"
	default:
		line = oneLine(r.Role)
	}
	if n := strings.TrimSpace(oneLine(r.Note)); n != "" {
		line += ": " + n
	}
	var marks []string
	for _, c := range r.Callouts {
		if t := strings.TrimSpace(c.Text); t != "" {
			marks = append(marks, t)
		}
	}
	if len(marks) > 0 {
		line += " [" + strings.Join(marks, "; ") + "]"
	}
	return line
}
