package designgen

import (
	"bytes"
	"image"
)

// ═══ THE GREY CHECK OF A FLAT CANDIDATE (flat route, 0397) ═══
//
// Round 7's remaining fault was style, not topology: the model shades bands and panels grey even
// when told not to. The prompt now says «no grey, no tint, no shading»; this is the cheap post-check
// beside it. It LABELS a candidate (Artifact.Flags → design_picture.qa_flags → DesignPicture.flags),
// it never refuses one: the designer still picks, and a faint grey may be the least of four evils.
//
// THE MEASURE. On a grid of at most greyGridMax cells on the long side (nearest-pixel samples, so no
// averaging invents grey at the line edges):
//   - ink = any pixel of the cell's block with luma < 60; the OUTSIDE is everything reachable from the border without crossing ink
//     (flood fill), the SILHOUETTE is what is left that is not ink;
//   - a grey cell is low-saturation (max − min channel < greySatMax) with luma from 60 up to the
//     PAPER less greyPaperGap (the paper = the brightest common luma of the outside cells): measured
//     on round 7's sheets the tint the owner rejected is luma 242–248 on 253–254 paper — far above a
//     fixed 235 — AND its four neighbours two cells away are grey too — a fill, not the anti-aliased
//     rim of a line;
//   - flag when grey cells are more than greyFlagFraction of the silhouette.
// A drawing whose outline is open leaks its inside into the outside and reads as no silhouette at
// all — the check then stays silent (a label is a hint; a false «grey» costs a designer's trust).

// FlagGrey — the label of a flat candidate with a grey fill inside its silhouette.
const FlagGrey = "grey"

const (
	greyGridMax  = 512
	greySatMax   = 24
	greyLumaLo   = 60
	greyPaperGap = 5
	// Calibrated on every sheet of rounds 4–7 (tmp/plans/flat-consistency/out): the rejected V tint
	// of c38/text-b2-1 reads 0.037, the cleanest-to-worst clean sheet 0.000–0.0026, the schematics
	// (grey by design) 0.78–0.93. Hatching (a rib neckband drawn as fine lines) is not a fill and is
	// not labelled.
	greyFlagFraction = 0.015
	greyMinInside    = 400 // cells: a silhouette smaller than this is not a garment
)

// flatPixelFlags — the labels of one flat candidate; nil when nothing is noticed or the bytes do not
// decode.
func flatPixelFlags(raw []byte) []string {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	if greyFillFraction(img) > greyFlagFraction {
		return []string{FlagGrey}
	}
	return nil
}

// greyFillFraction — grey-fill cells / silhouette cells (0 when there is no silhouette).
func greyFillFraction(img image.Image) float64 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return 0
	}
	step := 1
	for (w+step-1)/step > greyGridMax || (h+step-1)/step > greyGridMax {
		step++
	}
	gw, gh := (w+step-1)/step, (h+step-1)/step
	const (
		cOther = iota
		cInk
		cGrey
	)
	luma := make([]int16, gw*gh) // -1 = transparent (paper)
	sat := make([]int16, gw*gh)
	var hist [256]int
	for gy := 0; gy < gh; gy++ {
		for gx := 0; gx < gw; gx++ {
			i := gy*gw + gx
			r, g, bl, a := img.At(b.Min.X+gx*step, b.Min.Y+gy*step).RGBA()
			if a < 0x8000 {
				luma[i] = -1
				continue
			}
			r8, g8, b8 := int(r>>8), int(g>>8), int(bl>>8)
			l := (299*r8 + 587*g8 + 114*b8) / 1000
			luma[i], sat[i] = int16(l), int16(max(r8, g8, b8)-min(r8, g8, b8))
			if gx == 0 || gy == 0 || gx == gw-1 || gy == gh-1 {
				hist[l]++
			}
			// A line thinner than the step must still be a wall: any ink pixel of the cell's block
			// makes the cell ink (a sampled-only grid leaked the outside through every 2 px outline).
			if step > 1 && l >= greyLumaLo {
				for y := gy * step; y < min((gy+1)*step, h) && luma[i] >= greyLumaLo; y++ {
					for x := gx * step; x < min((gx+1)*step, w); x++ {
						pr, pg, pb, pa := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
						if pa >= 0x8000 && (299*int(pr>>8)+587*int(pg>>8)+114*int(pb>>8))/1000 < greyLumaLo {
							luma[i] = 0
							break
						}
					}
				}
			}
		}
	}
	// the paper: the brightest luma that at least a fifth of the border cells reach or exceed
	paper, acc, border := 255, 0, 0
	for _, n := range hist {
		border += n
	}
	for l := 255; l >= 0; l-- {
		acc += hist[l]
		if border > 0 && acc*5 >= border {
			paper = l
			break
		}
	}
	class := make([]uint8, gw*gh)
	for i, l := range luma {
		switch {
		case l < 0:
		case int(l) < greyLumaLo:
			class[i] = cInk
		case int(l) <= paper-greyPaperGap && sat[i] < greySatMax:
			class[i] = cGrey
		}
	}
	// flood the outside from the border through non-ink cells
	outside := make([]bool, gw*gh)
	stack := make([]int, 0, 2*(gw+gh))
	push := func(i int) {
		if !outside[i] && class[i] != cInk {
			outside[i] = true
			stack = append(stack, i)
		}
	}
	for x := 0; x < gw; x++ {
		push(x)
		push((gh-1)*gw + x)
	}
	for y := 0; y < gh; y++ {
		push(y * gw)
		push(y*gw + gw - 1)
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := i%gw, i/gw
		if x > 0 {
			push(i - 1)
		}
		if x < gw-1 {
			push(i + 1)
		}
		if y > 0 {
			push(i - gw)
		}
		if y < gh-1 {
			push(i + gw)
		}
	}
	greyAt := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < gw && y < gh && class[y*gw+x] == cGrey
	}
	inside, grey := 0, 0
	for y := 0; y < gh; y++ {
		for x := 0; x < gw; x++ {
			i := y*gw + x
			if outside[i] || class[i] == cInk {
				continue
			}
			inside++
			if class[i] == cGrey && greyAt(x-2, y) && greyAt(x+2, y) && greyAt(x, y-2) && greyAt(x, y+2) {
				grey++
			}
		}
	}
	if inside < greyMinInside {
		return 0
	}
	return float64(grey) / float64(inside)
}
