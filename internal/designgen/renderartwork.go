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

// artworkCaption — the caption of the artwork's picture in the reference list.
func artworkCaption(a artworkUse) string {
	c := "artwork «" + artworkName(a) + "»"
	if n := artworkTechnique(a); n != "" {
		c += " — " + n
	}
	return c + ": the artwork itself on a plain ground, to be applied on the garment where the ARTWORK paragraph says"
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

// renderArtworkParagraphs — one ARTWORK paragraph per placed artwork whose picture AND flat both
// went out. An artwork whose picture or flat did not survive resolution is not mentioned: words
// about an image the model cannot see are an instruction about nothing.
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
		fmt.Fprintf(&b, ". Apply it on the %s of the garment, wherever that side is visible, inside the "+
			"four-cornered area marked on the %s flat (image %d) whose corners lie at — top-left %s, %s; "+
			"top-right %s, %s; bottom-right %s, %s; bottom-left %s, %s — of that flat's frame "+
			"(x from its left edge, y from its top edge)", view, view, j,
			artworkPercent(a.Corners[0].X), artworkPercent(a.Corners[0].Y),
			artworkPercent(a.Corners[1].X), artworkPercent(a.Corners[1].Y),
			artworkPercent(a.Corners[2].X), artworkPercent(a.Corners[2].Y),
			artworkPercent(a.Corners[3].X), artworkPercent(a.Corners[3].Y))
		switch r := artworkRotation(a.Corners); {
		case r >= 3:
			fmt.Fprintf(&b, ", rotated about %d° clockwise", r)
		case r <= -3:
			fmt.Fprintf(&b, ", rotated about %d° counter-clockwise", -r)
		default:
			b.WriteString(", upright")
		}
		b.WriteString(". Make it ")
		if technique != "" {
			b.WriteString("real " + technique)
		} else {
			b.WriteString("a real print or embroidery")
		}
		b.WriteString(" on the cloth: its white ground is not part of it; its exact shape, colours and " +
			"letterforms are kept; it fills that area at the area's own proportions and perspective, " +
			"follows the folds and takes the light of the photograph. Nowhere else on the garment.")
		out = append(out, b.String())
	}
	return out
}
