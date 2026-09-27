package designgen

import (
	"image"
	"image/draw"
	"testing"

	"github.com/stretchr/testify/require"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// ═══ G-03 r3, Codex MAJOR 3 — the banded paste box-reduces its source ONCE, not once per band ═══

// r2LeanScale — leanScale as shipped at 5e05647, FROZEN here (not a call into the current code), so
// a change to the filter itself — not only to the band loop — shows up as a difference below.
func r2LeanScale(dst draw.Image, dr image.Rectangle, src image.Image, sr image.Rectangle) {
	if dr.Empty() || sr.Empty() {
		return
	}
	region := dst.Bounds().Intersect(dr)
	if region.Empty() {
		return
	}
	fx := float64(dr.Dx()) / float64(sr.Dx())
	fy := float64(dr.Dy()) / float64(sr.Dy())
	// s2d maps the ORIGINAL source coordinates onto dr; a box-reduced source is one pixel per k×k
	// block starting at sr.Min, so its coordinate u is (x − sr.Min.X) / k.
	s2d := f64.Aff3{
		fx, 0, float64(dr.Min.X) - float64(sr.Min.X)*fx,
		0, fy, float64(dr.Min.Y) - float64(sr.Min.Y)*fy,
	}
	from, fromRect := src, sr
	if k := min(sr.Dx()/dr.Dx(), sr.Dy()/dr.Dy()); k >= 2 {
		from = boxReduce(src, sr, k)
		fromRect = from.Bounds()
		s2d = f64.Aff3{fx * float64(k), 0, float64(dr.Min.X), 0, fy * float64(k), float64(dr.Min.Y)}
	}
	// The kernel writes only inside region: a float rounding of the transformed rectangle never
	// touches the pixel beside it.
	out, direct := dst.(*image.RGBA)
	if direct {
		out = out.SubImage(region).(*image.RGBA)
	} else {
		out = image.NewRGBA(region)
	}
	xdraw.CatmullRom.Transform(out, s2d, from, fromRect, xdraw.Src, nil)
	if !direct {
		draw.Draw(dst, region, out, region.Min, draw.Src)
	}
}

// perBandPasteReplacing / perBandPasteThroughMask — the r2 pastes verbatim (r2LeanScale called per
// band, each call rebuilding the whole reduced source): the reference the pre-reduced pastes must
// match pixel for pixel.
func perBandPasteReplacing(dst draw.Image, rect image.Rectangle, got image.Image, ab image.Rectangle) {
	same := ab.Size() == rect.Size()
	rows := min(compositeBandRows, rect.Dy())
	pix := make([]uint8, 4*rect.Dx()*rows)
	for y0 := rect.Min.Y; y0 < rect.Max.Y; y0 += rows {
		br := image.Rect(rect.Min.X, y0, rect.Max.X, min(y0+rows, rect.Max.Y))
		band := &image.RGBA{Pix: pix[:4*br.Dx()*br.Dy()], Stride: 4 * br.Dx(), Rect: br}
		if same {
			draw.Draw(band, br, got, ab.Min.Add(br.Min.Sub(rect.Min)), draw.Src)
		} else {
			r2LeanScale(band, rect, got, ab)
		}
		draw.Draw(dst, br, band, br.Min, draw.Src)
	}
}

func perBandPasteThroughMask(dst draw.Image, rect image.Rectangle, got image.Image, ab image.Rectangle, alpha *image.Alpha) {
	same := ab.Size() == rect.Size()
	rows := min(compositeBandRows, rect.Dy())
	pix := make([]uint8, 4*rect.Dx()*rows)
	for y0 := rect.Min.Y; y0 < rect.Max.Y; y0 += rows {
		br := image.Rect(rect.Min.X, y0, rect.Max.X, min(y0+rows, rect.Max.Y))
		band := &image.RGBA{Pix: pix[:4*br.Dx()*br.Dy()], Stride: 4 * br.Dx(), Rect: br}
		if same {
			draw.Draw(band, br, got, ab.Min.Add(br.Min.Sub(rect.Min)), draw.Src)
		} else {
			r2LeanScale(band, rect, got, ab)
		}
		draw.DrawMask(dst, br, band, br.Min, alpha, br.Min, draw.Over)
	}
}

// noisyNRGBA — a picture no filter can pass by accident: every channel varies per pixel, alpha included.
func noisyNRGBA(r image.Rectangle, seed uint32) *image.NRGBA {
	img := image.NewNRGBA(r)
	s := seed
	for i := range img.Pix {
		s = s*1664525 + 1013904223
		img.Pix[i] = uint8(s >> 24)
	}
	return img
}

// TestThePreReducedPasteMATCHES_THE_PER_BAND_PASTE — the pre-reduced pastes paint the same bytes the
// r2 per-band pastes did, on every scale path (an integer shrink ≥ 2, a shrink by 3 with partial
// blocks, a shrink < 2 with no reduction, an enlargement, a same-size copy), into an RGBA (the
// kernel's direct path) and an NRGBA (drawn in place, the buffered path), with an answer whose own
// rectangle does not start at the origin, over several bands.
// MUTATIONS (measured red): newLeanScaler without the boxReduce branch (the kernel widened over the
// full source instead) → the two «shrink by 2+/3» rows differ; the paste's scaler built for rect
// shifted one row (a reused scaler that no longer maps every band onto rect) → every resampled row.
func TestThePreReducedPasteMATCHES_THE_PER_BAND_PASTE(t *testing.T) {
	for _, tc := range []struct {
		name string
		ab   image.Rectangle
		rect image.Rectangle
	}{
		{"shrink by 2+", image.Rect(5, 7, 522, 396), image.Rect(31, 17, 31+173, 17+151)},
		{"shrink by 3, partial blocks", image.Rect(0, 0, 517, 461), image.Rect(3, 9, 3+150, 9+140)},
		{"shrink under 2", image.Rect(0, 0, 517, 389), image.Rect(0, 11, 300, 11+250)},
		{"enlarge", image.Rect(2, 2, 99, 63), image.Rect(20, 30, 20+250, 30+180)},
		{"same size", image.Rect(4, 4, 204, 154), image.Rect(40, 20, 240, 170)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := noisyNRGBA(tc.ab, 7)
			frame := image.Rect(0, 0, 320, 300)
			alpha := image.NewAlpha(tc.rect)
			for i := range alpha.Pix {
				if (i/7+i/301)%3 != 0 {
					alpha.Pix[i] = 0xff
				}
			}
			for _, kind := range []string{"rgba", "nrgba"} {
				mk := func() draw.Image {
					base := noisyNRGBA(frame, 99)
					if kind == "nrgba" {
						return base
					}
					out := image.NewRGBA(frame)
					draw.Draw(out, frame, base, frame.Min, draw.Src)
					return out
				}
				pix := func(img draw.Image) []uint8 {
					if n, ok := img.(*image.NRGBA); ok {
						return n.Pix
					}
					return img.(*image.RGBA).Pix
				}

				want, have := mk(), mk()
				perBandPasteReplacing(want, tc.rect, got, tc.ab)
				pasteReplacing(have, tc.rect, got, tc.ab)
				require.Equalf(t, pix(want), pix(have), "%s pasteReplacing", kind)

				want, have = mk(), mk()
				perBandPasteThroughMask(want, tc.rect, got, tc.ab, alpha)
				pasteThroughMask(have, tc.rect, got, tc.ab, alpha)
				require.Equalf(t, pix(want), pix(have), "%s pasteThroughMask", kind)
			}
		})
	}
}

// TestTheBandedPasteREDUCES_ITS_SOURCE_ONCE — Codex's own case: a 4096² answer fitted into 2048²
// (k = 2, 32 bands of 64 rows). One reduction is 2048² × 4 B = 16 MiB; the r2 paste rebuilt it per
// band, ≈ 512 MiB of scratch after the money. Both pastes must stay under two reductions.
// MUTATION (measured red): scaler.scaleInto(band) → leanScale(band, rect, got, ab) in either paste →
// 531 MiB (bound 32 MiB).
func TestTheBandedPasteREDUCES_ITS_SOURCE_ONCE(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a 64 MiB answer")
	}
	const S, D = 4096, 2048
	got := image.NewRGBA(image.Rect(0, 0, S, S))
	for i := 0; i < len(got.Pix); i += 4 {
		got.Pix[i], got.Pix[i+1], got.Pix[i+2], got.Pix[i+3] = uint8(i>>2), uint8(i>>14), 7, 0xff
	}
	rect := image.Rect(0, 0, D, D)
	dst := image.NewRGBA(rect)
	alpha := image.NewAlpha(rect)
	for i := range alpha.Pix {
		alpha.Pix[i] = 0xff
	}
	oneReduction := uint64(4 * D * D)
	require.Equal(t, D/compositeBandRows, 32, "the case Codex measured: 32 bands")

	alloc := totalAllocDuring(func() { pasteReplacing(dst, rect, got, got.Bounds()) })
	require.Lessf(t, alloc, 2*oneReduction, "pasteReplacing allocated %d MiB for a %d MiB reduction",
		alloc>>20, oneReduction>>20)

	alloc = totalAllocDuring(func() { pasteThroughMask(dst, rect, got, got.Bounds(), alpha) })
	require.Lessf(t, alloc, 2*oneReduction, "pasteThroughMask allocated %d MiB for a %d MiB reduction",
		alloc>>20, oneReduction>>20)
}
