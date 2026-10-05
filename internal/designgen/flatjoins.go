package designgen

import (
	"fmt"
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
			L = append(L, fmt.Sprintf("- %s: %s%s from %s%s.%s %s", it.ID, it.Kind, width, strings.Join(path, " → "), closed, cont, it.Text))
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
	// 1. bands that start/end at a neck point
	var npOrder []string
	npBands := map[string][]entity.DesignJoinItem{}
	for _, it := range straps {
		if it.Closed {
			continue
		}
		p := it.Path()
		for _, end := range []string{p[0], p[len(p)-1]} {
			b := joinsLmBase(end)
			if strings.HasPrefix(b, "NP_") {
				if _, ok := npBands[b]; !ok {
					npOrder = append(npOrder, b)
				}
				npBands[b] = append(npBands[b], it)
			}
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
	S = append(S, "SIDE LEFT shows the wearer's LEFT flank with the front facing the LEFT edge of its frame; SIDE RIGHT is its mirror with the front facing the RIGHT edge.")
	// 8. layers (owner 05.10: garments can be multi-layer and sheer) — after, as l4.py tested them
	S = append(S, joinsLayerSentences(j)...)
	return S
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
