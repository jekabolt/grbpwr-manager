package designgen

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // registered so a provider that answers with gif is measured rather than skipped
	_ "image/jpeg" // ditto
	_ "image/png"  // the format this route asks for
	"math"
)

// ═══ CAN A SERVER TELL WHETHER A TILE ACTUALLY TILES — AND SHOULD IT? ═══════════════════════════
//
// It can, cheaply and without opinion, and this file is that measurement. What it must NOT do is
// decide the run's fate, and the reasoning is the money reasoning this whole package is built on:
//
//   · THE PICTURE IS ALREADY BOUGHT when this runs. Refusing to file it would throw away bytes that
//     were paid for and leave the person with nothing to look at — while the tile may well be usable
//     for a print where the repeat is not laid edge to edge.
//   · A RETRY WOULD PAY AGAIN FOR THE SAME KIND OF ANSWER. Nothing about re-sending an identical
//     prompt makes a general-purpose image model start wrapping its output. Five paid rounds of that
//     is exactly the failure classify.go exists to prevent.
//   · AND THE MEASUREMENT IS NOT THE JUDGEMENT. A tile can wrap perfectly and still be useless
//     because the motif is recognisable enough to announce the grid; a tile can measure slightly
//     seamed and be perfect on cloth. The eye decides; only the eye can.
//
// SO THE VERDICT CHOOSES THE WORDS, NEVER THE FATE. A tile that does not wrap comes back from the
// provider AS AN ARTIFACT TOGETHER WITH AN ERROR — the shape settle() already implements for a
// partial success: the picture is filed, the run closes `done`, and the ATTEMPT ROW carries
// `pattern_not_seamless`. The person sees the tile and, beside it, the reason it may not be what
// they asked for. Nothing is thrown away and nothing is bought twice.
//
// ⚠ AND THE SCREEN STILL OWES THE REAL CHECK. The only complete answer to «does this tile?» is a
// tile laid out: a 3×3 repeat preview next to the picture, which costs the client one CSS rule and
// answers the question the way a person actually asks it. This measurement is the half that can be
// done without a human present — it catches the obvious failure (a bordered, vignetted or plainly
// non-wrapping square) at the moment it is bought, instead of weeks later.

// ⚠ TWO MEASUREMENTS, BECAUSE A TILE FAILS TO TILE IN TWO OPPOSITE WAYS AND EITHER ONE ALONE IS
// BLIND TO THE OTHER. This was found by measurement, not by reasoning: the first version of this
// file carried only the wrap check, and a square with a hard white border sailed through it —
// column w-1 is white, column 0 is white, so the JOIN IS PERFECT and the defect is entirely
// interior. A vignette does the same thing more quietly.
//
//   · THE WRAP CHECK catches content that ENDS DIFFERENTLY on opposite edges: a gradient across the
//     square, a motif cut at one side and not continued at the other, a photograph crop.
//   · THE EDGE-BIAS CHECK catches a FRAME: a border, a matte, a vignette, a drop shadow. Its
//     premise is the definition of a tile rather than a heuristic — a repeating tile has no
//     privileged position, so the band along its edge must look statistically like the rest of it.
//     A tile whose edge is systematically brighter or darker has a frame, and that frame appears
//     four times in every cell of the laid-out grid.
//
// WHAT NEITHER OF THEM CATCHES, said out loud so nobody mistakes a green verdict for a guarantee: a
// tile that wraps perfectly and still announces its grid because the motif is large and
// recognisable. That is a judgement about how a thing LOOKS, and the only instrument for it is a
// person seeing the tile laid out — see the block above on what the screen still owes.

const (
	// seamFactor and seamFloor are the two halves of one verdict, and BOTH are needed.
	//
	// The factor makes the test RELATIVE: a seamless tile's wrap boundary is, by construction, no
	// more discontinuous than any other adjacent pair of columns inside it, so the honest question
	// is «is the join unusually abrupt FOR THIS PICTURE», not «is the join abrupt». An absolute
	// threshold alone would condemn every high-contrast pattern and pass every soft one.
	//
	// The floor stops the factor from firing on a nearly flat tile, where the interior baseline is
	// almost zero and any rounding difference is «three times» it. Units are 0..255 per channel.
	seamFactor = 3.0
	seamFloor  = 6.0

	// seamMaxPixels bounds what this check will decode. The bytes come from a provider, and an
	// image header is a cheap way to ask a 0.5 GiB process for a gigabyte of pixels. Over the cap
	// the tile is filed UNMEASURED rather than refused — see seamCheck's contract: this function
	// never costs anybody a picture.
	seamMaxPixels = 40 << 20

	// seamBiasFloor and seamBiasFactor are the edge-bias verdict, in the same 0..255 units and by
	// the same shape of argument as the pair above: an absolute floor so a nearly uniform tile is
	// not condemned by an arithmetic accident, and a relative term measured against the tile's OWN
	// spread so a busy pattern is judged by its own character. The relative term is deliberately
	// loose — a false «this has a frame» costs one scary word beside a picture that is kept anyway,
	// while a false silence costs nothing at all, so the threshold is set where an obvious frame
	// fails and an ordinary pattern does not.
	seamBiasFloor  = 8.0
	seamBiasFactor = 0.5

	// seamBandDivisor sets how thick the edge band is: one sixteenth of the shorter side, which on
	// a square tile samples roughly a fifth of the picture — enough for the mean to mean something.
	seamBandDivisor = 16

	// seamSamples bounds the interior baseline's cost. Sampling rather than exhausting keeps the
	// check in single-digit milliseconds on a 2048² tile; a baseline over 128 well-spread column
	// pairs is not meaningfully different from one over two thousand.
	seamSamples = 128
)

// errPatternNotSeamless is raised when a tile's wrap boundary is measurably more abrupt than the
// picture's own interior — that is, when the square does not join to itself.
//
// IT IS RETURNED BESIDE THE ARTIFACT, NEVER INSTEAD OF IT. classify() closes it as a DELIVERED
// attempt with its own code and marks it not retryable, so the picture is filed, the money is
// recorded once, and no second identical call is bought.
var errPatternNotSeamless = fmt.Errorf("designgen: the generated tile does not join to itself")

// seamVerdict is what one measurement found. It is a struct rather than a bool because the numbers
// are the point: «not seamless» with no figures beside it is an accusation a person cannot check.
type seamVerdict struct {
	// Measured is false when the bytes could not be decoded or were too large to decode. A tile
	// nobody could measure is NOT a tile that failed — the two must never read the same.
	Measured bool
	// Horizontal / Vertical are the mean per-channel discontinuity across each wrap boundary, in
	// 0..255 units. Baseline is the same figure for the picture's ordinary interior.
	Horizontal float64
	Vertical   float64
	Baseline   float64
	// EdgeBias is how far the mean brightness of the edge band sits from the mean of the interior,
	// and Spread is the tile's own standard deviation — the yardstick the bias is judged against.
	EdgeBias float64
	Spread   float64
	Width    int
	Height   int
}

// Seamless reports whether both boundaries are within the tolerance. An unmeasured tile is reported
// SEAMLESS, deliberately: silence about a picture we could not read must not become a complaint
// about it.
func (v seamVerdict) Seamless() bool {
	if !v.Measured {
		return true
	}
	return v.Horizontal <= v.WrapLimit() && v.Vertical <= v.WrapLimit() && v.EdgeBias <= v.BiasLimit()
}

// WrapLimit and BiasLimit are the two tolerances, exported to the package so the sentence a person
// reads can quote the number the verdict was measured against. An accusation without the figure
// beside it is one a reader cannot check.
func (v seamVerdict) WrapLimit() float64 { return v.Baseline*seamFactor + seamFloor }

func (v seamVerdict) BiasLimit() float64 {
	if rel := v.Spread * seamBiasFactor; rel > seamBiasFloor {
		return rel
	}
	return seamBiasFloor
}

// seamCheck measures one tile.
//
// IT NEVER RETURNS AN ERROR AND NEVER PANICS ON A HOSTILE PAYLOAD: an undecodable, truncated or
// enormous image yields Measured=false, which reads as «no complaint». Everything about this
// function is downstream of one rule — a measurement must not be able to cost the owner a picture
// he has already paid for.
func seamCheck(raw []byte) seamVerdict {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return seamVerdict{}
	}
	if cfg.Width < 4 || cfg.Height < 4 || int64(cfg.Width)*int64(cfg.Height) > seamMaxPixels {
		return seamVerdict{}
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return seamVerdict{}
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 4 || h < 4 {
		return seamVerdict{}
	}

	// THE WRAP BOUNDARIES. Column w-1 against column 0 is the join a horizontal repeat makes; row
	// h-1 against row 0 is the vertical one. Note that this is NOT «are the two edges identical» —
	// in a correct tile they are adjacent, therefore different. What is being asked is whether they
	// are as different as neighbours normally are in this picture.
	horiz := columnDiff(img, b.Min.X+w-1, b.Min.X, b.Min.Y, h)
	vert := rowDiff(img, b.Min.Y+h-1, b.Min.Y, b.Min.X, w)

	// THE INTERIOR BASELINE, from both axes, so a picture whose columns are smooth and whose rows
	// are busy is judged against its own character rather than against half of it.
	var sum float64
	var n int
	for _, s := range sampleOffsets(w-1, seamSamples) {
		sum += columnDiff(img, b.Min.X+s, b.Min.X+s+1, b.Min.Y, h)
		n++
	}
	for _, s := range sampleOffsets(h-1, seamSamples) {
		sum += rowDiff(img, b.Min.Y+s, b.Min.Y+s+1, b.Min.X, w)
		n++
	}
	if n == 0 {
		return seamVerdict{}
	}
	bias, spread := edgeBias(img, b, w, h)
	return seamVerdict{
		Measured:   true,
		Horizontal: horiz,
		Vertical:   vert,
		Baseline:   sum / float64(n),
		EdgeBias:   bias,
		Spread:     spread,
		Width:      w,
		Height:     h,
	}
}

// edgeBias returns how far the edge band's mean luma sits from the interior's, and the tile's own
// standard deviation.
//
// ⚠ THE PREMISE IS THE DEFINITION OF A TILE, NOT A GUESS ABOUT PICTURES. A repeating tile has no
// privileged position: laid out, its edge lands in the middle of the cloth like everything else. So
// a systematic difference between «near the edge» and «not near the edge» is not a property of the
// pattern, it is a frame — and a frame is the one artefact that appears in every single cell of the
// grid, four times over.
func edgeBias(img image.Image, b image.Rectangle, w, h int) (bias, spread float64) {
	band := w
	if h < band {
		band = h
	}
	band /= seamBandDivisor
	if band < 1 {
		band = 1
	}
	var edgeSum, inSum, all, allSq float64
	var edgeN, inN, allN int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			l := luma(img, b.Min.X+x, b.Min.Y+y)
			all += l
			allSq += l * l
			allN++
			if x < band || y < band || x >= w-band || y >= h-band {
				edgeSum += l
				edgeN++
			} else {
				inSum += l
				inN++
			}
		}
	}
	if edgeN == 0 || inN == 0 || allN == 0 {
		return 0, 0
	}
	mean := all / float64(allN)
	if v := allSq/float64(allN) - mean*mean; v > 0 {
		spread = math.Sqrt(v)
	}
	return math.Abs(edgeSum/float64(edgeN) - inSum/float64(inN)), spread
}

// luma is the perceptual brightness of one pixel in 0..255 units.
func luma(img image.Image, x, y int) float64 {
	r, g, bl, _ := img.At(x, y).RGBA()
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257.0
}

// sampleOffsets spreads at most k samples over 0..count-1, evenly. It returns every index when
// there are fewer than k of them, so a small tile is measured exhaustively rather than sparsely.
func sampleOffsets(count, k int) []int {
	if count <= 0 {
		return nil
	}
	if count <= k {
		out := make([]int, count)
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, 0, k)
	for i := 0; i < k; i++ {
		out = append(out, i*count/k)
	}
	return out
}

// columnDiff is the mean per-channel absolute difference between two columns, over `h` rows.
func columnDiff(img image.Image, xa, xb, y0, h int) float64 {
	var sum float64
	for y := y0; y < y0+h; y++ {
		sum += pixelDiff(img, xa, y, xb, y)
	}
	return sum / float64(h)
}

// rowDiff is the mean per-channel absolute difference between two rows, over `w` columns.
func rowDiff(img image.Image, ya, yb, x0, w int) float64 {
	var sum float64
	for x := x0; x < x0+w; x++ {
		sum += pixelDiff(img, x, ya, x, yb)
	}
	return sum / float64(w)
}

// pixelDiff is the mean absolute difference of the three colour channels, in 0..255 units.
//
// ALPHA IS IGNORED ON PURPOSE. The route asks for an opaque tile, so alpha carries no signal here;
// including it would let a uniformly transparent border read as a perfect match.
func pixelDiff(img image.Image, xa, ya, xb, yb int) float64 {
	r1, g1, b1, _ := img.At(xa, ya).RGBA()
	r2, g2, b2, _ := img.At(xb, yb).RGBA()
	// RGBA() returns ALPHA-PREMULTIPLIED 16-BIT values; dividing by 257 brings a channel back to the
	// 0..255 scale the two thresholds above are written in (65535/257 = 255 exactly).
	d := float64(absDiff(r1, r2)+absDiff(g1, g2)+absDiff(b1, b2)) / 3.0
	return d / 257.0
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}
