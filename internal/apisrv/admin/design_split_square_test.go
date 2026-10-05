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

// T25: the square respects the size ceiling — it is scaled while placing, not after.
func TestT25SquareCapped(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 5400, 100))
	t25Fill(src, src.Bounds(), color.NRGBA{0, 0, 0, 255})
	out, ok := designSquarePiece(src)
	if !ok {
		t.Fatal("no content found")
	}
	if b := out.Bounds(); b.Dx() != designSquareMaxSide || b.Dy() != designSquareMaxSide {
		t.Fatalf("got %v, want %d square", b, designSquareMaxSide)
	}
}
