package admin

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/shopspring/decimal"
)

// R-12: владелец говорит «после кропа картинка с БЕЛЫМ фоном становится СЕРОЙ».
// Опыт отвечает ровно на один вопрос: меняет ли РЕЗ белый пиксель.
func TestR12CropKeepsWhiteWhite(t *testing.T) {
	d := func(s string) decimal.Decimal { v, _ := decimal.NewFromString(s); return v }
	r := designUnitRect{x: d("0.1"), y: d("0.1"), w: d("0.5"), h: d("0.5")}

	white := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			white.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, white, nil); err != nil {
		t.Fatal(err)
	}
	asJPEG, _, err := image.Decode(bytes.NewReader(jb.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		src  image.Image
	}{{"RGBA белый", white}, {"JPEG белый (YCbCr)", asJPEG}} {
		out, err := designCropPNG(tc.src, tc.src.Bounds(), r)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got, err := png.Decode(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		b := got.Bounds()
		cr, cg, cb, ca := got.At(b.Min.X+1, b.Min.Y+1).RGBA()
		t.Logf("%s → %T %dx%d, пиксель = (%d,%d,%d,a=%d)",
			tc.name, got, b.Dx(), b.Dy(), cr>>8, cg>>8, cb>>8, ca>>8)
		if ca>>8 != 255 {
			t.Errorf("%s: рез отдал ПРОЗРАЧНЫЙ пиксель (a=%d) — вот это и читается серым", tc.name, ca>>8)
		}
		if cr>>8 < 250 || cg>>8 < 250 || cb>>8 < 250 {
			t.Errorf("%s: белый перестал быть белым: (%d,%d,%d)", tc.name, cr>>8, cg>>8, cb>>8)
		}
	}
}
