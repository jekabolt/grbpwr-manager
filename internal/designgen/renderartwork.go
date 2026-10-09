package designgen

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ─────────────────────────── ARTWORK ON A RENDER (70-ROUND7 B7) ───────────────────────────
//
// A placed artwork (print, embroidery, patch…) reaches a render as ONE picture plus ONE paragraph.
// The picture is the artwork shot alone on white (mode `artwork`, maybe cut out); the paragraph says
// which flat it stands on and WHERE — four corners of a quad in percentages of that flat's frame and
// the approximate rotation of its top edge — and how to apply it (wording lifted from the add_logo
// craft: its white ground is not part of it, its shape and colours are kept, it follows the folds).
//
// T27 (renderartwork_derive.go): the worker sends the picture CROPPED to its content on the cloth
// colour, shrinks the quad to that content, and adds one PLACEMENT GUIDE per side within the engine
// ceiling; the paragraph then points at the guide and states the technique (embroidery / print).

// artworkCaption — the caption of the artwork's picture in the reference list.
func artworkCaption(a artworkUse) string {
	c := "artwork «" + artworkName(a) + "»"
	if n := artworkTechnique(a); n != "" {
		c += " — " + n
	}
	return c + ": the artwork itself on a plain ground, to be applied on the garment where the ARTWORK paragraph says"
}

// artworkCaptionOn — the caption of a TIGHTENED artwork picture (T27): cropped to its edges and
// flattened onto the cloth colour (or a neutral grey when the recipe states none).
func artworkCaptionOn(a artworkUse, ground artworkGroundKind) string {
	c := "artwork «" + artworkName(a) + "»"
	if n := artworkTechnique(a); n != "" {
		c += " — " + n
	}
	on := "on the garment's own cloth colour"
	if ground != artworkGroundCloth {
		on = "on a neutral grey ground"
	}
	return c + ": the artwork itself, cropped to its edges, " + on + ", to be applied on the garment where the ARTWORK paragraph says"
}

func artworkName(a artworkUse) string {
	if n := oneLine(a.Name); n != "" {
		return n
	}
	return "artwork"
}

// artworkTechnique — the frozen note with the client's bookkeeping markers dropped (« · cut»,
// «artwork = picture 1»): the server strips them at freeze, and a snapshot frozen otherwise must
// not leak them either.
func artworkTechnique(a artworkUse) string {
	return oneLine(entity.DesignArtworkTechniqueWords(a.Note))
}

// artworkNumberOf — the number of the picture that went out AS this artwork; 0 = not sent.
func artworkNumberOf(attached []refCaption, mediaID int) int {
	if mediaID <= 0 {
		return 0
	}
	for i, rc := range attached {
		if rc.MediaID == mediaID {
			if !rc.IsArtwork {
				return 0
			}
			return i + 1
		}
	}
	return 0
}

// artworkPercent spells a fraction as a whole percentage.
func artworkPercent(f float64) string {
	return strconv.Itoa(int(math.Round(f*100))) + " %"
}

// artworkRotation — the approximate rotation of the quad's top edge (TL → TR), in whole degrees,
// positive clockwise (y grows downwards). Fractions of a non-square frame skew it a little, which
// is why the paragraph says «about».
func artworkRotation(c []artworkCorner) int {
	if len(c) < 2 {
		return 0
	}
	dx, dy := c[1].X-c[0].X, c[1].Y-c[0].Y
	if dx == 0 && dy == 0 {
		return 0
	}
	return int(math.Round(math.Atan2(dy, dx) * 180 / math.Pi))
}

// artworkTechniqueKind — embroidery | print | "" (unknown), read off the frozen technique words
// (placement note, else asset note, else the BOM line's spec — designFreezeArtworks), and off the
// name only when no words were frozen at all. Both or neither named → unknown: the neutral wording.
func artworkTechniqueKind(a artworkUse) string {
	w := strings.ToLower(artworkTechnique(a))
	if w == "" {
		w = strings.ToLower(oneLine(a.Name))
	}
	emb := strings.Contains(w, "embroider") || strings.Contains(w, "вышив")
	prn := strings.Contains(w, "print") || strings.Contains(w, "принт") || strings.Contains(w, "печат")
	switch {
	case emb && !prn:
		return "embroidery"
	case prn && !emb:
		return "print"
	}
	return ""
}

// artworkTechniqueSentence — HOW the artwork is made, by its technique (T27, wording measured on
// gpt-image-2: the B variant).
func artworkTechniqueSentence(a artworkUse) string {
	switch artworkTechniqueKind(a) {
	case "embroidery":
		return "Make it real machine EMBROIDERY: dense satin-stitch thread in the artwork's own colours" +
			artworkColoursAside(a) + ", " +
			"raised above the cloth by about 1–2 mm, visible stitch direction and the soft sheen of thread, " +
			"stitched edges that slightly gather the cloth around them. It is NOT a print, NOT a flat patch, " +
			"NOT an appliqué and has no backing or border of its own: only the artwork itself is stitched, " +
			"its exact shape, colours and letterforms kept."
	case "print":
		return "Make it a real PRINT: screen-printed ink" + artworkColoursAside(a) + " sitting in the weave — flat, no relief, the cloth " +
			"texture shows through the ink; its exact shape, colours and letterforms are kept. It is NOT " +
			"embroidery and NOT a patch."
	}
	if t := artworkTechnique(a); t != "" {
		return "Make it real " + t + " on the cloth: its exact shape, colours and letterforms are kept."
	}
	return "Make it a real print or embroidery on the cloth: its exact shape, colours and letterforms are kept."
}

// artworkGroundSentence — what the ground around the artwork in its picture is (never part of it).
func artworkGroundSentence(a artworkUse, k int) string {
	switch a.Ground {
	case artworkGroundCloth:
		return fmt.Sprintf("The plain ground around it in image %d is only the garment's cloth colour and is not part of it.", k)
	case artworkGroundGrey:
		return fmt.Sprintf("The plain grey ground around it in image %d is not part of it.", k)
	}
	return "Its white ground is not part of it."
}

// artworkGuideNumberOf — the number of the placement guide of that side; 0 = none went out.
func artworkGuideNumberOf(attached []refCaption, view string) int {
	if view == "" {
		return 0
	}
	for i, rc := range attached {
		if rc.GuideView == view {
			return i + 1
		}
	}
	return 0
}

func artworkCornersText(c []artworkCorner) string {
	return fmt.Sprintf("top-left %s, %s; top-right %s, %s; bottom-right %s, %s; bottom-left %s, %s",
		artworkPercent(c[0].X), artworkPercent(c[0].Y), artworkPercent(c[1].X), artworkPercent(c[1].Y),
		artworkPercent(c[2].X), artworkPercent(c[2].Y), artworkPercent(c[3].X), artworkPercent(c[3].Y))
}

// renderArtworkParagraphs — one ARTWORK paragraph per placed artwork whose picture AND flat both
// went out. An artwork whose picture or flat did not survive resolution is not mentioned: words
// about an image the model cannot see are an instruction about nothing.
//
// T27: with its side's PLACEMENT GUIDE attached the paragraph points at the guide — «reproduce that
// position and that size exactly» — and keeps the corners as a cross-check; without one (the engine
// ceiling dropped it, or a picture could not be read) the corners sentence is the only placement
// instruction. Either way a TECHNIQUE sentence says how it is made.
func renderArtworkParagraphs(arts []artworkUse, attached []refCaption) []string {
	var out []string
	for _, a := range arts {
		if len(a.Corners) != 4 {
			continue
		}
		k := artworkNumberOf(attached, a.MediaID)
		j := imageNumberOf(attached, a.FlatMediaID)
		if k == 0 || j == 0 || j == k {
			continue
		}
		view := viewWord(a.View)
		if view == "" {
			view = "front"
		}
		technique := artworkTechnique(a)
		var b strings.Builder
		fmt.Fprintf(&b, "ARTWORK. Image %d is the artwork «%s»", k, artworkName(a))
		if technique != "" {
			b.WriteString(" (" + technique + ")")
		}
		rotation := ""
		switch r := artworkRotation(a.Corners); {
		case r >= 3:
			rotation = fmt.Sprintf("rotated about %d° clockwise", r)
		case r <= -3:
			rotation = fmt.Sprintf("rotated about %d° counter-clockwise", -r)
		}
		if g := artworkGuideNumberOf(attached, a.View); g > 0 && a.Ground != "" {
			fmt.Fprintf(&b, ". Image %d shows EXACTLY where it goes and how big it is: on the %s of the garment, "+
				"where image %d draws it, at that size relative to the %s panel (its corners lie at %s of the "+
				"%s flat's frame", g, view, g, view, artworkCornersText(a.Corners), view)
			if rotation != "" {
				b.WriteString(", " + rotation)
			}
			b.WriteString("). Reproduce that position and that size exactly — do not enlarge it, do not move it. ")
			b.WriteString(artworkTechniqueSentence(a) + " " + artworkGroundSentence(a, k))
			b.WriteString(" It follows the folds and takes the light of the photograph. Nowhere else on the garment.")
			out = append(out, b.String())
			continue
		}
		fmt.Fprintf(&b, ". Apply it on the %s of the garment, wherever that side is visible, inside the "+
			"four-cornered area marked on the %s flat (image %d) whose corners lie at — %s — of that flat's "+
			"frame (x from its left edge, y from its top edge)", view, view, j, artworkCornersText(a.Corners))
		if rotation != "" {
			b.WriteString(", " + rotation)
		} else {
			b.WriteString(", upright")
		}
		b.WriteString(". " + artworkTechniqueSentence(a) + " " + artworkGroundSentence(a, k))
		b.WriteString(" It fills that area at the area's own proportions and perspective, follows the folds " +
			"and takes the light of the photograph. Nowhere else on the garment.")
		out = append(out, b.String())
	}
	return out
}

// artworkColoursAside — « (white #f4f4f4 — never the cloth's colour)» when the colours were read.
func artworkColoursAside(a artworkUse) string {
	if a.Colours == "" {
		return ""
	}
	return " (" + a.Colours + " — never the cloth's own colour, never tone-on-tone)"
}
