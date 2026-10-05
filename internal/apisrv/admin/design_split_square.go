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
	// A pixel is garment when, flattened onto white, it is clearly not white: its darkest channel
	// is below designSquareInkMin, or its chroma (max − min channel) exceeds designSquareChromaMax.
	// Transparency needs no rule of its own — flattened, a transparent pixel IS white.
	designSquareInkMin    = 235
	designSquareChromaMax = 18
	// designSquareMinNeighbours — a garment pixel counts toward the bounding box only when at least
	// this many of its 8 neighbours are garment too. That drops isolated specks (0 or 1 neighbour)
	// while a 1-px hairline of a flat (2 neighbours along the line) survives — a 3×3 opening would
	// erase exactly those hairlines.
	designSquareMinNeighbours = 2
	// designSquareMaxSide keeps the square inside the verbatim upload's ceilings
	// (bucket: ≤ 12000 px a side, ≤ 40 MP): a larger square is scaled down after squaring.
	designSquareMaxSide = 6000
)

// designSquarePiece flattens the cropped frame onto white, finds the garment inside it and returns
// it centred on a square white canvas. ok=false means the frame holds no garment at all; the caller
// then keeps the plain crop (the behaviour before T25).
func designSquarePiece(cropped image.Image) (out image.Image, ok bool) {
	b := cropped.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 {
		return nil, false
	}

	// Flatten onto opaque white: alpha-over a white canvas. draw.Draw has fast paths for the
	// decoders' concrete types (RGBA, NRGBA, YCbCr, Gray…).
	flat := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range flat.Pix {
		flat.Pix[i] = 0xff
	}
	draw.Draw(flat, flat.Bounds(), cropped, b.Min, draw.Over)

	ink := make([]bool, w*h)
	for y := 0; y < h; y++ {
		row := flat.Pix[y*flat.Stride : y*flat.Stride+w*4]
		for x := 0; x < w; x++ {
			r, g, bl := row[x*4], row[x*4+1], row[x*4+2]
			mn, mx := r, r
			if g < mn {
				mn = g
			}
			if bl < mn {
				mn = bl
			}
			if g > mx {
				mx = g
			}
			if bl > mx {
				mx = bl
			}
			ink[y*w+x] = mn < designSquareInkMin || int(mx)-int(mn) > designSquareChromaMax
		}
	}

	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !ink[y*w+x] {
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
					if ink[yy*w+xx] {
						n++
					}
				}
			}
			if n < designSquareMinNeighbours {
				continue
			}
			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	if maxX < 0 {
		return nil, false
	}

	content := image.Rect(minX, minY, maxX+1, maxY+1)
	long := content.Dx()
	if content.Dy() > long {
		long = content.Dy()
	}
	side := int(float64(long)/(1-2*designSquareMargin) + 0.999999)
	if side < long {
		side = long
	}

	// The cap is applied while placing, never after: a full-size square of a big frame would cost
	// side²·4 bytes (≈ 800 MB at 14 000 px) only to be thrown away by the downscale.
	scale := 1.0
	if side > designSquareMaxSide {
		scale = float64(designSquareMaxSide) / float64(side)
		side = designSquareMaxSide
	}
	cw := int(float64(content.Dx())*scale + 0.5)
	ch := int(float64(content.Dy())*scale + 0.5)
	if cw < 1 {
		cw = 1
	}
	if ch < 1 {
		ch = 1
	}

	canvas := image.NewNRGBA(image.Rect(0, 0, side, side))
	for i := range canvas.Pix {
		canvas.Pix[i] = 0xff
	}
	dst := image.Rect(0, 0, cw, ch).Add(image.Pt((side-cw)/2, (side-ch)/2))
	if scale == 1 {
		draw.Draw(canvas, dst, flat, content.Min, draw.Src)
	} else {
		xdraw.CatmullRom.Scale(canvas, dst, flat, content, xdraw.Src, nil)
	}
	return canvas, true
}
