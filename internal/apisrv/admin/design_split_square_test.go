package admin

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"github.com/shopspring/decimal"
)

func t25Fill(img *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

// t25Check decodes a piece and asserts: square, opaque white corners, content bbox centred with
// ≈8 % margin on the long side, and the content of the expected size.
func t25Check(t *testing.T, name string, raw []byte, wantW, wantH int) {
	t.Helper()
	got, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	b := got.Bounds()
	if b.Dx() != b.Dy() {
		t.Fatalf("%s: not square: %dx%d", name, b.Dx(), b.Dy())
	}
	side := b.Dx()
	minX, minY, maxX, maxY := side, side, -1, -1
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			r, g, bl, a := got.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if a>>8 != 255 {
				t.Fatalf("%s: transparent pixel at %d,%d", name, x, y)
			}
			if r>>8 < 128 && g>>8 < 128 && bl>>8 < 128 {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x), max(maxY, y)
			} else if r>>8 != 255 || g>>8 != 255 || bl>>8 != 255 {
				t.Fatalf("%s: background not white at %d,%d: %d,%d,%d", name, x, y, r>>8, g>>8, bl>>8)
			}
		}
	}
	cw, ch := maxX-minX+1, maxY-minY+1
	if cw != wantW || ch != wantH {
		t.Fatalf("%s: content %dx%d, want %dx%d", name, cw, ch, wantW, wantH)
	}
	long := max(cw, ch)
	if m := float64(side-long) / 2 / float64(side); math.Abs(m-0.08) > 0.01 {
		t.Errorf("%s: margin %.3f of side, want ≈0.08", name, m)
	}
	left, right := minX, side-1-maxX
	top, bottom := minY, side-1-maxY
	if t25Abs(left-right) > 1 || t25Abs(top-bottom) > 1 {
		t.Errorf("%s: not centred: l=%d r=%d t=%d b=%d", name, left, right, top, bottom)
	}
	t.Logf("%s: side=%d content=%dx%d l/r=%d/%d t/b=%d/%d", name, side, cw, ch, left, right, top, bottom)
}

func t25Abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// T25: a composite of two garments with different aspect ratios — both pieces come out square,
// the garment centred, ≈8 % margin, white around it; a lone speck in a frame does not move the box.
func TestT25SplitPiecesComeOutSquare(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 400, 200))
	t25Fill(src, src.Bounds(), color.NRGBA{255, 255, 255, 255})
	t25Fill(src, image.Rect(30, 20, 130, 180), color.NRGBA{20, 20, 20, 255})  // tall 100×160 in the left half
	t25Fill(src, image.Rect(220, 70, 380, 130), color.NRGBA{30, 30, 30, 255}) // wide 160×60 in the right half
	src.SetNRGBA(195, 5, color.NRGBA{0, 0, 0, 255})                           // speck in the left frame

	d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
	left := designUnitRect{x: d("0"), y: d("0"), w: d("0.5"), h: d("1")}
	right := designUnitRect{x: d("0.5"), y: d("0"), w: d("0.5"), h: d("1")}

	rawL, err := designCropPNG(src, src.Bounds(), left)
	if err != nil {
		t.Fatal(err)
	}
	t25Check(t, "left/tall", rawL, 100, 160)
	rawR, err := designCropPNG(src, src.Bounds(), right)
	if err != nil {
		t.Fatal(err)
	}
	t25Check(t, "right/wide", rawR, 160, 60)
}

// T25: a source with alpha — transparent background becomes white, the garment stays.
func TestT25SplitAlphaFlattensToWhite(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 200, 200)) // fully transparent
	t25Fill(src, image.Rect(50, 40, 150, 160), color.NRGBA{10, 10, 10, 255})
	d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
	raw, err := designCropPNG(src, src.Bounds(), designUnitRect{x: d("0"), y: d("0"), w: d("1"), h: d("1")})
	if err != nil {
		t.Fatal(err)
	}
	t25Check(t, "alpha", raw, 100, 120)
}

// T25 review: the work is bounded — a frame longer than designSquareWorkMaxSide is brought down
// before any copy, so the square never exceeds 4096/0.84.
func TestT25SquareCapped(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 5400, 100))
	t25Fill(src, src.Bounds(), color.NRGBA{0, 0, 0, 255})
	out, ok := designSquarePiece(src)
	if !ok {
		t.Fatal("no content found")
	}
	b := out.Bounds()
	if b.Dx() != b.Dy() || b.Dx() > 4877 || b.Dx() < 4870 {
		t.Fatalf("got %v, want a square of ≈4877", b)
	}
}

// t25Dark — the box of dark pixels and the side of the square.
func t25Dark(t *testing.T, img image.Image) (side int, box image.Rectangle) {
	t.Helper()
	b := img.Bounds()
	if b.Dx() != b.Dy() {
		t.Fatalf("not square: %v", b)
	}
	minX, minY, maxX, maxY := b.Dx(), b.Dy(), -1, -1
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, _, _, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if a>>8 != 255 {
				t.Fatalf("transparent pixel at %d,%d", x, y)
			}
			if r>>8 < 128 {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	return b.Dx(), image.Rect(minX, minY, maxX+1, maxY+1)
}

// T25 review: a WHITE garment with a small dark print on a transparent sheet — the foreground comes
// from alpha, so the whole shape (120×160) is kept, not clipped to the print.
func TestT25WhiteGarmentOnAlphaKeepsWholeShape(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 200, 200))
	t25Fill(src, image.Rect(40, 20, 160, 180), color.NRGBA{255, 255, 255, 255})
	t25Fill(src, image.Rect(90, 90, 110, 110), color.NRGBA{10, 10, 10, 255})
	out, ok := designSquarePiece(src)
	if !ok {
		t.Fatal("no content")
	}
	side, print := t25Dark(t, out)
	if side != 191 { // ceil(160/0.84): the shape's long side, not the print's 20
		t.Fatalf("side %d, want 191 (whole shape kept)", side)
	}
	// shape at offset ((191-120)/2, (191-160)/2) = (35,15); print sits 50,70 inside it
	if want := image.Rect(85, 85, 105, 105); print != want {
		t.Fatalf("print at %v, want %v", print, want)
	}
}

// T25 review: an OPAQUE white garment on white — colour sees only the print, which covers < 20 %
// of the frame, so the frame is NOT tightened: square-padded as submitted.
func TestT25WhiteOnWhiteNotTightened(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 200, 150))
	t25Fill(src, src.Bounds(), color.NRGBA{255, 255, 255, 255})
	t25Fill(src, image.Rect(90, 60, 110, 80), color.NRGBA{10, 10, 10, 255})
	out, ok := designSquarePiece(src)
	if !ok {
		t.Fatal("no content")
	}
	side, print := t25Dark(t, out)
	if side != 239 { // ceil(200/0.84): the whole frame
		t.Fatalf("side %d, want 239 (frame kept whole)", side)
	}
	// frame at offset ((239-200)/2, (239-150)/2) = (19,44)
	if want := image.Rect(109, 104, 129, 124); print != want {
		t.Fatalf("print at %v, want %v", print, want)
	}
}

// T25 review: light shading of a white garment reaching past a large print — faint guard keeps it.
func TestT25FaintShadingBeyondInkNotTightened(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 200, 200))
	t25Fill(src, src.Bounds(), color.NRGBA{255, 255, 255, 255})
	t25Fill(src, image.Rect(10, 10, 190, 190), color.NRGBA{242, 242, 242, 255}) // garment shading
	t25Fill(src, image.Rect(60, 60, 140, 160), color.NRGBA{10, 10, 10, 255})    // print, 20 % of frame
	out, ok := designSquarePiece(src)
	if !ok {
		t.Fatal("no content")
	}
	if side := out.Bounds().Dx(); side != 239 {
		t.Fatalf("side %d, want 239 (frame kept whole)", side)
	}
}
