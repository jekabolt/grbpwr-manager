package designgen

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ THE FLAT ROUTE'S JOIN LIST IN WORDS (tmp/plans/flat-consistency, rounds 5–7) ═══
//
// A flat run whose card has a confirmed join list carries it FROZEN in its input snapshot
// (`inputs.joins`, written by the door — design_joins.go in apisrv/admin; a rerun copies its
// parent's). The flat craft then says three more things, in this order, between the layout and the
// owner's style paragraphs — the order of the winning prompt (r7 «sentences only», 3/4 on card 38,
// 4/4 on card 49, the normal case intact):
//
//  1. the landmark ruler + the list itself (r5.list_text) + the absences;
//  2. «CHECK EVERY VIEW AGAINST THESE BEFORE DRAWING» — the failure-mode sentences the list implies
//     (r7.sentences, ported below line for line), plus the layer sentences of a multi-layer garment;
//  3. the side-facing convention (inside 2, last).
//
// The schematic picture of round 6 is NOT sent: once the sentences exist it added nothing and leaked
// a back-neck arc (round 7 verdict).

// flatNoTextNoGrey — round 7's remaining fault was style (grey fills in the bands); the sentence that
// answers it. Said on every flat run, with or without joins, AFTER every human word and before the
// owner's verbatim style paragraphs (which it does not replace).
const flatNoTextNoGrey = "No text, no view names, no grey, no tint, no shading anywhere on the drawing — the inside of every band, binding and collar stays white."

// flatSideFacing — the side-view convention (r5 layout, r7's last sentence says it per frame).
const flatSideFacing = "SIDE LEFT shows the wearer's LEFT flank with the front facing the LEFT edge; SIDE RIGHT the right flank with the front facing the RIGHT edge (mirror images)."

// flatJoinsUsable — whether a frozen list says anything a prompt can use.
func flatJoinsUsable(j *entity.DesignJoinsDoc) bool {
	return j != nil && (len(j.Items) > 0 || len(j.Absences) > 0)
}

// joinsListText — r5.list_text: the ruler, the layers (when the garment has more than one), every item
// on one line, the absences.
func joinsListText(j entity.DesignJoinsDoc) string {
	L := []string{entity.DesignJoinLandmarkHelp, ""}
	if len(j.Layers) > 0 {
		L = append(L, "LAYERS (depth levels; 0 = outermost):")
		for _, l := range j.Layers {
			sheer := ""
			if l.Sheer {
				sheer = " — SHEER"
			}
			L = append(L, "- layer "+strconv.Itoa(l.Index)+": "+l.Name+sheer+". "+l.Note)
		}
		L = append(L, "")
	}
	L = append(L, "JOIN LIST (the construction; every view must agree with it):")
	for _, it := range j.Items {
		path := it.Path()
		switch it.Kind {
		case entity.DesignJoinKindPocket:
			t := it.Type
			if t == "" {
				t = "patch"
			}
			L = append(L, fmt.Sprintf("- %s: %s pocket at %s. %s", it.ID, t, it.From, it.Text))
		case entity.DesignJoinKindOpening:
			L = append(L, fmt.Sprintf("- %s: OPENING (no cloth) bounded by %s. %s", it.ID, strings.Join(it.BoundedBy, ", "), it.Text))
		case entity.DesignJoinKindClosure:
			count := ""
			if it.Count > 0 {
				count = strconv.Itoa(it.Count)
			}
			L = append(L, fmt.Sprintf("- %s: %s closure, %s along %s. %s", it.ID, it.Type, count, strings.Join(path, " → "), it.Text))
		default:
			width := ""
			if it.Width != "" {
				width = " (" + it.Width + ")"
			}
			closed := ""
			if it.Closed {
				closed = " (closed loop)"
			}
			cont := ""
			if len(it.ContinuesInto) > 0 {
				cont = " continues into " + strings.Join(it.ContinuesInto, ", ") + "."
			}
			L = append(L, strings.TrimRight(fmt.Sprintf("- %s: %s%s from %s%s.%s %s", it.ID, it.Kind, width, strings.Join(path, " → "), closed, cont, it.Text), " "))
		}
	}
	if len(j.Absences) > 0 {
		L = append(L, "ABSENT (do not draw): "+strings.Join(j.Absences, "; "))
	}
	return strings.Join(L, "\n")
}

func joinsLmBase(n string) string {
	b, _, _ := strings.Cut(n, "..")
	return b
}

// joinsAtNeckT — how far out along the line from a neck point an end may sit and still be told it is
// AT the neck point (10 % of the way).
const joinsAtNeckT = 0.1

// joinsNeckEnd — a path end that starts from a neck point: the neck point, how far out it sits (t, 0
// at the neck point) and the landmark it heads to («NP_L..SP_L:0.3» → NP_L, 0.3, SP_L; «NP_L» → NP_L,
// 0, ""). ok = false for any other point.
func joinsNeckEnd(n string) (np string, t float64, toward string, ok bool) {
	n = strings.TrimSpace(n)
	a, rest, between := strings.Cut(n, "..")
	if !strings.HasPrefix(a, "NP_") {
		return "", 0, "", false
	}
	if !between {
		return a, 0, "", true
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return "", 0, "", false
	}
	v, err := strconv.ParseFloat(rest[i+1:], 64)
	if err != nil || v < 0 || v > 1 {
		return "", 0, "", false
	}
	return a, v, rest[:i], true
}

// joinsAtNeck — the point is a neck point or within joinsAtNeckT of one.
func joinsAtNeck(n string) bool {
	_, t, _, ok := joinsNeckEnd(n)
	return ok && t <= joinsAtNeckT
}

// joinsNeckEndsHug — an end of the neck item that may be told it hugs the base of the neck: a neck
// point (or within joinsAtNeckT of one) or the base of the neck at the side (NECK_SIDE_x). An end
// anywhere else — out on the shoulder line, at a shoulder tip — does not hug the neck (M1).
func joinsNeckEndsHug(n string) bool {
	n = strings.TrimSpace(n)
	return joinsAtNeck(n) || (strings.HasPrefix(n, "NECK_SIDE_") && !strings.Contains(n, ".."))
}

// joinsPct — 0.3 → «30%».
func joinsPct(t float64) string {
	return strconv.Itoa(int(math.Round(t*100))) + "%"
}

// joinsOutwardSentence — an end that starts from a neck point but sits further out along the line:
// where it really is, in the words of its path.
func joinsOutwardSentence(id, end, np, toward string, t float64) string {
	if strings.HasPrefix(toward, "SP_") {
		s := id + ": its top end sits ON THE SHOULDER LINE, " + joinsPct(t) + " of the way from the neck point " + np +
			" towards the shoulder tip " + toward + " (" + end + "); NOT at the neck point"
		if t < 1-joinsAtNeckT {
			s += " and NOT at the shoulder tip"
		}
		return s + "."
	}
	return id + ": its top end sits " + joinsPct(t) + " of the way from the neck point " + np + " towards " + toward +
		" (" + end + "); NOT at the neck point."
}

// joinsSentences — r7.sentences, ported line for line (golden tests: card 38 and card 49), plus the
// layer sentences of a multi-layer garment before the side-facing one. One deliberate difference: a
// band counts as DIAGONAL only when its ends are also apart in height (> 0.1 of the ruler) — a
// waistband or a hem band from one side to the other is horizontal, and r7 would have called it a
// diagonal strap.
func joinsSentences(j entity.DesignJoinsDoc) []string {
	items := j.Items
	var S []string
	var straps []entity.DesignJoinItem
	for _, it := range items {
		if entity.DesignJoinIsBand(it.Kind) && len(it.Path()) > 0 {
			straps = append(straps, it)
		}
	}
	// 1. bands that start/end at a neck point. Only an end AT the neck point (plain NP_x, or
	// NP_x..B:t with t ≤ joinsAtNeckT) is told it sits there; an end further out along the line is told
	// where it is (M1: «NP_L..SP_L:0.3» once came out as «AT the neck point», beside a list line that
	// says 30 % out to the shoulder tip).
	var npOrder []string
	npBands := map[string][]entity.DesignJoinItem{}
	var outward []string
	for _, it := range straps {
		if it.Closed {
			continue
		}
		p := it.Path()
		for _, end := range []string{p[0], p[len(p)-1]} {
			np, t, toward, ok := joinsNeckEnd(end)
			if !ok {
				continue
			}
			if t > joinsAtNeckT {
				if it.Kind != entity.DesignJoinKindCollar && it.Kind != entity.DesignJoinKindStand {
					outward = append(outward, joinsOutwardSentence(it.ID, end, np, toward, t))
				}
				continue
			}
			if _, ok := npBands[np]; !ok {
				npOrder = append(npOrder, np)
			}
			npBands[np] = append(npBands[np], it)
		}
	}
	for _, npn := range npOrder {
		other := "NP_L"
		if npn == "NP_L" {
			other = "NP_R"
		}
		twin := npBands[other]
		for _, it := range npBands[npn] {
			if it.Kind == entity.DesignJoinKindCollar || it.Kind == entity.DesignJoinKindStand {
				continue
			}
			t := it.ID + ": its top end sits AT the neck point " + npn + ", right beside the neck"
			if len(twin) > 0 {
				t += ", only a neck-width away from " + twin[0].ID
			}
			t += ", NOT at the shoulder tip; nothing of it rises above the neckline."
			S = append(S, t)
		}
	}
	S = append(S, outward...)
	// 2. bands that run to the opposite side → diagonal, crossing below the neck
	var diag []string
	for _, it := range straps {
		p := it.Path()
		a, b := p[0], p[len(p)-1]
		sa, sb := entity.DesignJoinSideOf(a), entity.DesignJoinSideOf(b)
		if sa == "" || sb == "" || sa == sb {
			continue
		}
		pa, oka := entity.DesignJoinPoint(a)
		pb, okb := entity.DesignJoinPoint(b)
		if !oka || !okb || abs(pa[2]-pb[2]) <= 0.1 {
			continue
		}
		sum := 0.0
		for _, n := range p {
			pt, _ := entity.DesignJoinPoint(n)
			sum += pt[1]
		}
		face := "front"
		if sum/float64(len(p)) < 0 {
			face = "back"
		}
		S = append(S, fmt.Sprintf("%s runs DIAGONALLY across the %s from %s to %s on the OPPOSITE side, as one straight band.", it.ID, face, a, b))
		diag = append(diag, it.ID)
	}
	if len(diag) >= 2 {
		S = append(S, strings.Join(diag, " and ")+" cross each other once, BELOW the neck points (between the shoulder blades), never above the neckline.")
	}
	// 3. the neck: is there cloth at the back neck?
	passesCBN := false
	var frontNeck []string
	for _, it := range items {
		p := it.Path()
		if it.Kind != entity.DesignJoinKindOpening && it.Kind != entity.DesignJoinKindStrap {
			for _, n := range p {
				if n == "CBN" || n == "CBN_LOW" {
					passesCBN = true
				}
			}
		}
		if it.Kind != entity.DesignJoinKindOpening {
			for _, n := range p {
				if b := joinsLmBase(n); b == "CFN" || b == "CFN_LOW" {
					frontNeck = append(frontNeck, it.ID)
					break
				}
			}
		}
	}
	if !passesCBN && len(frontNeck) > 0 {
		S = append(S, "There is NO back neckline: "+strings.Join(frontNeck, ", ")+" runs across the FRONT only. On the FRONT view draw NOTHING behind or inside the front neck band (no back-neck arc, no second curve). On the BACK view draw NOTHING between the tops of the bands at the neck points — no neckline, no band, no line.")
	} else if passesCBN {
		S = append(S, "The neckline goes all the way round: on the BACK view draw the back neckline (and its band/collar) between the neck points.")
	}
	// 4. bindings stop at their landmarks
	for _, it := range items {
		if p := it.Path(); it.Kind == entity.DesignJoinKindBinding && len(p) > 0 {
			S = append(S, fmt.Sprintf("%s starts at %s and stops at %s; it does not continue past them.", it.ID, p[0], p[len(p)-1]))
		}
	}
	// 5. openings
	for _, it := range items {
		if it.Kind == entity.DesignJoinKindOpening {
			S = append(S, "The area bounded by "+strings.Join(it.BoundedBy, ", ")+" is OPEN: leave it white — no cloth, no panel, no line across it.")
		}
	}
	// 6. absences
	for _, a := range j.Absences {
		S = append(S, "Draw NOTHING for: "+a+".")
	}
	// 7. pockets on one side
	for _, it := range items {
		if it.Kind != entity.DesignJoinKindPocket {
			continue
		}
		switch entity.DesignJoinSideOf(it.From) {
		case "L":
			S = append(S, it.ID+" is on the wearer's LEFT only: on the FRONT view it is on the picture-right; on the side views it shows only on SIDE LEFT.")
		case "R":
			S = append(S, it.ID+" is on the wearer's RIGHT only: on the FRONT view it is on the picture-left; on the side views it shows only on SIDE RIGHT.")
		}
	}
	// 8. sharp corners INSIDE a path (the point of a V): r5's `sharp` hint, which the schematic used
	// and the text route had lost — a V must not come back rounded. Ends are corners by nature.
	for _, it := range items {
		p := it.Path()
		var mid []string
		for _, pt := range it.Sharp {
			if len(p) > 2 && pt != p[0] && pt != p[len(p)-1] {
				mid = append(mid, pt)
			}
		}
		if len(mid) > 0 {
			S = append(S, it.ID+" comes to a SHARP corner at "+strings.Join(mid, ", ")+": a crisp point, never rounded.")
		}
	}
	S = append(S, "SIDE LEFT shows the wearer's LEFT flank with the front facing the LEFT edge of its frame; SIDE RIGHT is its mirror with the front facing the RIGHT edge.")
	// 8. layers (owner 05.10: garments can be multi-layer and sheer) — after, as l4.py tested them
	S = append(S, joinsLayerSentences(j)...)
	// 9. the v10 sentence types (81-FINAL-MODES; golden: out/v10/h3/c2/prompt.txt lines 62–66), then the
	// designer's own edits verbatim.
	S = append(S, joinsShapeSentences(j)...)
	S = append(S, joinsDesignerSentences(j)...)
	return S
}

// ─── v10 sentence types ───

// joinsNeckItem — the item that draws the front neckline: not an opening, its path passes the centre
// front neck (CFN / CFN_LOW / BREAK). The first one wins.
func joinsNeckItem(items []entity.DesignJoinItem) *entity.DesignJoinItem {
	for i, it := range items {
		if it.Kind == entity.DesignJoinKindOpening {
			continue
		}
		for _, n := range it.Path() {
			if b := joinsLmBase(n); b == "CFN" || b == "CFN_LOW" || b == "BREAK" {
				return &items[i]
			}
		}
	}
	return nil
}

// joinsNeckWord — «neck binding» / «neck band» / «neck collar»… for the neck item; «neckline» for none.
func joinsNeckWord(neck *entity.DesignJoinItem) string {
	if neck == nil {
		return "neckline"
	}
	return "neck " + strings.ReplaceAll(neck.Kind, "_", " ")
}

// joinsMids — the interior points of a path.
func joinsMids(p []string) []string {
	if len(p) <= 2 {
		return nil
	}
	return p[1 : len(p)-1]
}

// joinsBackLevel — the height word of a back edge from its centre-back point: «mid-back», «waist»…
func joinsBackLevel(p []string) string {
	for _, n := range p {
		switch joinsLmBase(n) {
		case "MB_C":
			return "mid-back"
		case "UB_C":
			return "upper-back"
		case "WB_C":
			return "waist"
		case "HEM_BC":
			return "hem"
		}
	}
	return ""
}

// joinsPointWhere — where the point of a V sits, in words.
func joinsPointWhere(n string) string {
	b := joinsLmBase(n)
	between := strings.Contains(n, "..")
	switch {
	case b == "BUST_C" && between:
		return "just below the bust"
	case b == "BUST_C":
		return "at the bust"
	case b == "CHEST_C":
		return "at the chest"
	case b == "WF_C":
		return "at the waist"
	}
	return "at " + n
}

// joinsBackFace — the item lies on the back (mean depth of its path).
func joinsBackFace(it entity.DesignJoinItem) bool {
	return it.View == entity.DesignViewBack || entity.DesignJoinFace(it.Path()) == entity.DesignViewBack
}

func joinsIsEdgeKind(k string) bool {
	return k == entity.DesignJoinKindBinding || k == entity.DesignJoinKindEdge || k == entity.DesignJoinKindBindingWide || k == entity.DesignJoinKindBand
}

// joinsNeckShapeSpeaks — whether the neck item may carry the neckline-shape sentence: it is an edge,
// binding or band (never a seam, a placket or anything else), and the list has no collar and no stand.
func joinsNeckShapeSpeaks(neck *entity.DesignJoinItem, items []entity.DesignJoinItem) bool {
	if neck == nil || !joinsIsEdgeKind(neck.Kind) {
		return false
	}
	for _, it := range items {
		if it.Kind == entity.DesignJoinKindCollar || it.Kind == entity.DesignJoinKindStand {
			return false
		}
	}
	return true
}

// joinsNeckShapeSentence — sentence type 1: the neckline's shape (the neck item's `type`).
func joinsNeckShapeSentence(neck *entity.DesignJoinItem) string {
	if neck == nil {
		return ""
	}
	p := neck.Path()
	if len(p) < 2 {
		return ""
	}
	a, z := p[0], p[len(p)-1]
	mid := strings.Join(joinsMids(p), ", ")
	if mid == "" {
		mid = "the centre front"
	}
	band := "band"
	if neck.Width != "" {
		band = neck.Width + " band"
	}
	switch neck.Type {
	case "crew":
		if !joinsNeckEndsHug(a) || !joinsNeckEndsHug(z) {
			// M1: ends written further out than the neck points («NP_L..SP_L:0.3») — the band does
			// not hug the base of the neck there, and the sentence must not say it does.
			return fmt.Sprintf("The FRONT neckline is a HIGH CREW neck at the centre front: %s is a %s from %s over %s (crew height at the centre front, just below the collarbone notch) to %s; its ends sit exactly where those points are, away from the base of the neck — NOT a V, NOT a plunge, NOT a halter ring, NOT a scoop.", neck.ID, band, a, mid, z)
		}
		return fmt.Sprintf("The FRONT neckline is a HIGH CREW neck: %s is a %s hugging the base of the neck from %s over %s (crew height, just below the collarbone notch) to %s — NOT a V, NOT a plunge, NOT a halter ring standing away from the neck, NOT a scoop.", neck.ID, band, a, mid, z)
	case "v":
		return fmt.Sprintf("The FRONT neckline is a V neck: %s runs from %s straight down to a SHARP point at %s and straight up to %s — NOT a crew, NOT a scoop, NOT a halter.", neck.ID, a, mid, z)
	case "scoop":
		return fmt.Sprintf("The FRONT neckline is a SCOOP neck: %s curves low and wide from %s through %s to %s — NOT a crew, NOT a V, NOT a halter.", neck.ID, a, mid, z)
	case "halter":
		return fmt.Sprintf("The neckline is a HALTER: %s rises from %s and goes AROUND the back of the neck to %s — NOT a crew, NOT a V, NOT a scoop; the shoulders stay bare.", neck.ID, a, z)
	case "boat":
		return fmt.Sprintf("The FRONT neckline is a wide BOAT neck: %s runs almost straight from %s to %s across the collarbones — NOT a crew, NOT a V, NOT a scoop.", neck.ID, a, z)
	case "square":
		return fmt.Sprintf("The FRONT neckline is a SQUARE neck: %s drops straight down from %s, runs straight across through %s and straight up to %s — NOT a crew, NOT a V, NOT a scoop.", neck.ID, a, mid, z)
	case "mock":
		return fmt.Sprintf("The neckline is a MOCK NECK: %s is a short band standing up around the base of the neck — NOT a crew band lying flat, NOT a V.", neck.ID)
	case "turtle":
		return fmt.Sprintf("The neckline is a TURTLENECK: %s is a tall band standing up around the neck and folded over — NOT a crew band lying flat, NOT a V.", neck.ID)
	}
	return ""
}

// joinsShapeSentences — the v10 types 1–5 (generated from the list's fields only).
func joinsShapeSentences(j entity.DesignJoinsDoc) []string {
	var S []string
	items := j.Items
	byID := map[string]entity.DesignJoinItem{}
	for _, it := range items {
		byID[it.ID] = it
	}
	neck := joinsNeckItem(items)
	// 1. the neckline's shape — only when the neck item is itself a band/binding/edge, and never on a
	// garment with a collar or a stand (D4, card 49: the model put type=crew on the neck SEAM under a
	// shirt collar, and the prompt said «HIGH CREW neck … hugging the base of the neck»).
	if joinsNeckShapeSpeaks(neck, items) {
		if t := joinsNeckShapeSentence(neck); t != "" {
			S = append(S, t)
		}
	}
	// 2. an inner edge seen through a sheer front (a multi-layer list says it in the layer sentences)
	if len(j.Layers) <= 1 {
		first := true
		for _, it := range items {
			p := it.Path()
			if it.Visibility != entity.DesignJoinThrough || joinsBackFace(it) || len(p) < 2 {
				continue
			}
			t := ""
			if first {
				t = "The outer front layer is CLOSED cloth from the " + joinsNeckWord(neck) + " down to the hem; there is no cut-out and no open V in the front. "
				first = false
			}
			t += it.ID + " is the edge of the opaque inner layer seen THROUGH the sheer outer layer: draw it as a FINE DASHED line only, "
			if mids := joinsMids(p); len(mids) == 1 {
				t += "from " + p[0] + " down to its point " + joinsPointWhere(mids[0]) + " and back up to " + p[len(p)-1] + "."
			} else {
				t += "from " + strings.Join(p, " → ") + "."
			}
			S = append(S, t)
		}
	}
	// straps that end on the back, and whether the back has a neckline
	var backStraps []entity.DesignJoinItem
	for _, it := range items {
		if it.Kind == entity.DesignJoinKindStrap && joinsBackFace(it) {
			backStraps = append(backStraps, it)
		}
	}
	backNeck := false
	for _, it := range items {
		if it.Kind == entity.DesignJoinKindOpening || it.Kind == entity.DesignJoinKindStrap {
			continue
		}
		for _, n := range it.Path() {
			if b := joinsLmBase(n); b == "CBN" || b == "CBN_LOW" {
				backNeck = true
			}
		}
	}
	// the opening's edge: bounded by straps and exactly one edge item on the back
	var openEdge *entity.DesignJoinItem
	for _, it := range items {
		if it.Kind != entity.DesignJoinKindOpening {
			continue
		}
		straps, edges := 0, []entity.DesignJoinItem{}
		for _, id := range it.BoundedBy {
			b, ok := byID[id]
			switch {
			case !ok:
			case b.Kind == entity.DesignJoinKindStrap:
				straps++
			case joinsIsEdgeKind(b.Kind):
				edges = append(edges, b)
			}
		}
		if straps > 0 && len(edges) == 1 && joinsBackFace(edges[0]) && len(edges[0].Path()) >= 2 {
			e := edges[0]
			openEdge = &e
			break
		}
	}
	// 3. armhole bindings on the FRONT only (a strap back with no back neckline)
	if len(backStraps) > 0 && !backNeck {
		var ids []string
		allBinding := true
		// Where the armholes start: AT the neck point only when every one of them does (M1); else
		// the one shared spot on the shoulder line, or the list's own words.
		atNeck, starts := true, map[string]bool{}
		for _, it := range items {
			p := it.Path()
			if !joinsIsEdgeKind(it.Kind) || it.Kind == entity.DesignJoinKindBand || len(p) < 2 || joinsBackFace(it) {
				continue
			}
			a, z := joinsLmBase(p[0]), joinsLmBase(p[len(p)-1])
			top := ""
			switch {
			case strings.HasPrefix(a, "NP_") && strings.HasPrefix(z, "UA_"):
				top = p[0]
			case strings.HasPrefix(z, "NP_") && strings.HasPrefix(a, "UA_"):
				top = p[len(p)-1]
			}
			if top != "" {
				ids = append(ids, it.ID)
				if it.Kind != entity.DesignJoinKindBinding {
					allBinding = false
				}
				if !joinsAtNeck(top) {
					atNeck = false
				}
				_, t, toward, _ := joinsNeckEnd(top)
				starts[joinsPct(t)+"|"+strings.TrimSuffix(strings.TrimSuffix(toward, "_L"), "_R")] = true
			}
		}
		if len(ids) > 0 {
			word := "binding"
			if !allBinding {
				word = "edge"
			}
			verb := " is"
			if len(ids) > 1 {
				word += "s"
				verb = " are"
			}
			above := "above the back opening"
			if openEdge == nil {
				above = "at the top of the back"
			}
			from := "from the neck point to the underarm"
			if !atNeck {
				from = "from where each starts on the shoulder line, as the join list gives it, down to the underarm"
				if len(starts) == 1 {
					for k := range starts {
						pct, toward, _ := strings.Cut(k, "|")
						if toward == "SP" {
							from = "from the shoulder line " + pct + " of the way out from the neck point towards the shoulder tip, NOT from the neck point, down to the underarm"
						}
					}
				}
			}
			S = append(S, joinsAnd(ids)+" "+word+verb+" on the FRONT view only (deep cut-in racer-style armhole "+from+"); on the BACK view the straps themselves are the only edges "+above+".")
		}
	}
	// 4. the opening's edge: one smooth U
	level := ""
	if openEdge != nil {
		p := openEdge.Path()
		level = joinsBackLevel(p)
		at := ""
		if level != "" {
			at = " at " + level + " level"
		}
		through := ""
		if mids := joinsMids(p); len(mids) > 0 {
			through = " down through " + strings.Join(mids, ", ") + " and up"
		}
		word := strings.ReplaceAll(openEdge.Kind, "_", " ")
		S = append(S, fmt.Sprintf("%s is ONE smooth U-shaped %s across the back%s, from %s%s to %s; the lower ends of the straps are sewn to it at its two top ends.",
			openEdge.ID, word, at, p[0], through, p[len(p)-1]))
	}
	// 5. the side view of a strap that continues the front neck over the shoulder to the back
	if neck != nil && len(backStraps) > 0 {
		cont := false
		for _, st := range backStraps {
			for _, c := range neck.ContinuesInto {
				if c == st.ID {
					cont = true
				}
			}
			for _, c := range st.ContinuesInto {
				if c == neck.ID {
					cont = true
				}
			}
		}
		if cont {
			t := "In SIDE LEFT and SIDE RIGHT the strap is ONE smooth continuous band: from the front " + joinsNeckWord(neck) + " up over the top of the shoulder and down the back to "
			if openEdge != nil {
				h := level
				if h == "" {
					h = "the opening's"
				}
				t += "the back opening edge — no loop, knot, hook, fold or notch; behind the strap the back body starts only at " + h + " height (open back), while the front body starts at the neck."
			} else {
				t += "its end — no loop, knot, hook, fold or notch."
			}
			S = append(S, t)
		}
	}
	return S
}

// joinsDesignerSentences — sentence type 6: every item or absence a designer added or changed, its
// words verbatim.
func joinsDesignerSentences(j entity.DesignJoinsDoc) []string {
	var S []string
	for _, it := range j.Items {
		if t := strings.TrimSpace(it.Text); it.Edited && t != "" {
			S = append(S, "designer: "+t)
		}
	}
	for _, a := range j.EditedAbsences {
		if t := strings.TrimSpace(a); t != "" {
			S = append(S, "designer: "+t)
		}
	}
	return S
}

// joinsAnd — «a», «a and b», «a, b and c».
func joinsAnd(ids []string) string {
	if len(ids) <= 1 {
		return strings.Join(ids, "")
	}
	return strings.Join(ids[:len(ids)-1], ", ") + " and " + ids[len(ids)-1]
}

// joinsLayerSentences — tmp/plans/flat-consistency/l4.py layer_sentences(), ported VERBATIM (the
// wording Fable tested live on card 38: the inner V as ONE fine dashed line 4/4, no grey 4/4, topology
// 3/4). Said after the side-facing sentence; a list with one layer (or none) says nothing here.
func joinsLayerSentences(j entity.DesignJoinsDoc) []string {
	var S []string
	layers := map[int]entity.DesignJoinLayer{}
	for _, l := range j.Layers {
		layers[l.Index] = l
	}
	if len(layers) <= 1 {
		return S
	}
	for _, l := range layers {
		if l.Sheer {
			S = append(S, "The sheer cloth is drawn exactly like opaque cloth: white, no grey, no hatching, no tint anywhere.")
			break
		}
	}
	outer := "outer layer"
	if l, ok := layers[0]; ok && l.Name != "" {
		outer = l.Name
	}
	for _, it := range j.Items {
		if it.Visibility == entity.DesignJoinThrough {
			name := "inner layer"
			if l, ok := layers[it.Layer]; ok && l.Name != "" {
				name = l.Name
			}
			S = append(S, it.ID+" is the edge of the layer BEHIND the sheer front ("+name+"), seen through it: draw it as ONE fine dashed line, lighter than seams; it does not break or touch the outline; no tint, no fill on either side of it.")
		}
		if p := it.Path(); it.FreeEdge && len(p) > 0 {
			via := "its point"
			if len(p) > 2 {
				via = strings.Join(p[1:len(p)-1], ", ")
			}
			S = append(S, it.ID+" is a free hanging edge: it runs from "+p[0]+" to "+p[len(p)-1]+" via "+via+" and does NOT run into the side seams or the hem.")
		}
		if len(it.CaughtInto) > 0 {
			S = append(S, it.ID+" ends INTO "+strings.Join(it.CaughtInto, ", ")+": at those points two edges meet — the "+outer+"'s binding and the inner layer's edge just inside it. On the SIDE views this shows as a second fine line just inside the armhole binding at the shoulder, running down into the dashed V.")
		}
		if it.Visibility == entity.DesignJoinHidden {
			S = append(S, "Draw NOTHING for "+it.ID+" on any view (construction only).")
		}
	}
	return S
}

// joinsCraft — the join paragraphs of a flat prompt: the list, then the checks.
func joinsCraft(j entity.DesignJoinsDoc) []string {
	return []string{
		joinsListText(j),
		"CHECK EVERY VIEW AGAINST THESE BEFORE DRAWING:\n- " + strings.Join(joinsSentences(j), "\n- "),
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
