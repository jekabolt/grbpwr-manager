package admin

import (
	"image"
	"image/draw"

	xdraw "golang.org/x/image/draw"
)

// T25 — владелец: «добавь сейфспейса белого пустого что бы всегда картинка выходила квадратной с
// нормальными отступами от края». Every piece cut out of a composite is tightened to the garment
// inside its frame and set centred on a SQUARE white canvas with an even margin.
const (
	// designSquareMargin — the margin on each edge as a share of the square's side: the content's
	// long side fills 1 − 2·0.08 = 84 % of the square.
	designSquareMargin = 0.08
	// designSquareWorkMaxSide — the frame is brought down to this long side BEFORE any copy or
	// mask is made, so the work is bounded whatever the source: work RGBA ≤ 4096²·4 = 64 MB, mask
	// ≤ 16 MB, square ≤ 4877²·4 ≈ 95 MB — peak ≈ 175 MB on top of the decoded source. The square
	// (≤ 4877 px) then sits well inside the bucket's ceilings (12000 px a side, 40 MP).
	designSquareWorkMaxSide = 4096

	// ALPHA SOURCES. A frame «has meaningful transparency» when more than designSquareAlphaShare of
	// its pixels have alpha < designSquareAlphaOpaque; its foreground is then read from the ORIGINAL
	// alpha (alpha > designSquareAlphaInk), never from colour — a white garment on a transparent
	// sheet is all foreground, not just its print.
	designSquareAlphaOpaque = 250
	designSquareAlphaInk    = 10
	designSquareAlphaShare  = 0.01

	// OPAQUE SOURCES. A pixel is ink when its darkest channel is below designSquareInkMin or its
	// chroma (max − min) exceeds designSquareChromaMax.
	designSquareInkMin    = 235
	designSquareChromaMax = 18
	// …but colour cannot see a white garment on white. Two guards keep such a garment from being
	// clipped to its print — either one leaves the frame untightened (square-padded as submitted):
	//   - the ink box covers less than designSquareMinCover of the frame;
	//   - faint shading (darkest channel < designSquareFaintMin) reaches beyond the ink box by more
	//     than designSquareFaintSlack of the long side: something light is there that the ink rule
	//     would cut.
	designSquareMinCover    = 0.20
	designSquareFaintMin    = 248
	designSquareFaintSlack  = 0.02
	designSquareFaintMinPad = 4

	// designSquareMinNeighbours — a foreground pixel counts toward a box only when at least this many
	// of its 8 neighbours are foreground too. Isolated specks (0–1 neighbour) drop out; a 1-px
	// hairline of a flat (2 neighbours along the line) survives — a 3×3 opening would erase it.
	designSquareMinNeighbours = 2
)

type designBox struct{ minX, minY, maxX, maxY int } // inclusive; maxX < 0 = empty

func (b designBox) empty() bool { return b.maxX < 0 }

// designMaskBox — the bounding box of the mask's pixels that have ≥ designSquareMinNeighbours
// foreground neighbours.
func designMaskBox(m []bool, w, h int) designBox {
	bx := designBox{w, h, -1, -1}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !m[y*w+x] {
				continue
			}
			n := 0
			for dy := -1; dy <= 1 && n < designSquareMinNeighbours; dy++ {
				yy := y + dy
				if yy < 0 || yy >= h {
					continue
				}
				for dx := -1; dx <= 1; dx++ {
					xx := x + dx
					if (dx == 0 && dy == 0) || xx < 0 || xx >= w {
						continue
					}
					if m[yy*w+xx] {
						n++
					}
				}
			}
			if n < designSquareMinNeighbours {
				continue
			}
			bx.minX, bx.maxX = min(bx.minX, x), max(bx.maxX, x)
			bx.minY, bx.maxY = min(bx.minY, y), max(bx.maxY, y)
		}
	}
	return bx
}

// designSquarePiece returns the cropped frame centred on a square white canvas, tightened to the
// garment when that can be done safely. ok=false means the frame holds nothing at all; the caller
// then keeps the plain crop (the behaviour before T25).
func designSquarePiece(cropped image.Image) (out image.Image, ok bool) {
	b := cropped.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 {
		return nil, false
	}

	// 1. Bounded work copy, premultiplied RGBA with the ORIGINAL alpha kept. A big frame is scaled
	// straight into the bounded buffer; there is never a full-size duplicate.
	if long := max(w, h); long > designSquareWorkMaxSide {
		s := float64(designSquareWorkMaxSide) / float64(long)
		w, h = max(1, int(float64(w)*s+0.5)), max(1, int(float64(h)*s+0.5))
	}
	work := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() && h == b.Dy() {
		draw.Draw(work, work.Bounds(), cropped, b.Min, draw.Src)
	} else {
		xdraw.ApproxBiLinear.Scale(work, work.Bounds(), cropped, b, xdraw.Src, nil)
	}
	px := func(x, y int) []uint8 { i := y*work.Stride + x*4; return work.Pix[i : i+4 : i+4] }

	// 2. Is the transparency meaningful?
	translucent := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if px(x, y)[3] < designSquareAlphaOpaque {
				translucent++
			}
		}
	}
	alphaSource := float64(translucent) > designSquareAlphaShare*float64(w*h)

	// 3. Foreground box.
	mask := make([]bool, w*h)
	var content designBox
	if alphaSource {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				mask[y*w+x] = px(x, y)[3] > designSquareAlphaInk
			}
		}
		content = designMaskBox(mask, w, h)
		if content.empty() {
			return nil, false
		}
	} else {
		// Flattened onto white (premultiplied: c + 255 − a), then the colour rule; a faint mask is
		// built alongside for the white-on-white guard.
		faint := make([]bool, w*h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				p := px(x, y)
				bg := 255 - p[3]
				r, g, bl := p[0]+bg, p[1]+bg, p[2]+bg
				mn, mx := min(r, g, bl), max(r, g, bl)
				mask[y*w+x] = mn < designSquareInkMin || int(mx)-int(mn) > designSquareChromaMax
				faint[y*w+x] = mn < designSquareFaintMin
			}
		}
		ink := designMaskBox(mask, w, h)
		if ink.empty() {
			return nil, false
		}
		content = ink
		cover := float64((ink.maxX-ink.minX+1)*(ink.maxY-ink.minY+1)) / float64(w*h)
		if cover < designSquareMinCover {
			content = designBox{0, 0, w - 1, h - 1}
		} else if fb := designMaskBox(faint, w, h); !fb.empty() {
			pad := max(designSquareFaintMinPad, int(designSquareFaintSlack*float64(max(w, h))))
			if fb.minX < ink.minX-pad || fb.minY < ink.minY-pad ||
				fb.maxX > ink.maxX+pad || fb.maxY > ink.maxY+pad {
				content = designBox{0, 0, w - 1, h - 1}
			}
		}
	}

	// 4. The square, composited straight from the work copy and flattened onto white.
	cw, ch := content.maxX-content.minX+1, content.maxY-content.minY+1
	long := max(cw, ch)
	side := max(long, int(float64(long)/(1-2*designSquareMargin)+0.999999))
	canvas := image.NewNRGBA(image.Rect(0, 0, side, side))
	for i := range canvas.Pix {
		canvas.Pix[i] = 0xff
	}
	ox, oy := (side-cw)/2, (side-ch)/2
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			p := px(content.minX+x, content.minY+y)
			bg := 255 - p[3]
			i := (oy+y)*canvas.Stride + (ox+x)*4
			canvas.Pix[i], canvas.Pix[i+1], canvas.Pix[i+2] = p[0]+bg, p[1]+bg, p[2]+bg
		}
	}
	return canvas, true
}
