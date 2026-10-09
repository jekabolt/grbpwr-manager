package bucket

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// ─────────────── ПРОЗРАЧНЫЙ PNG ПО ОБЫЧНОЙ ЗАГРУЗКЕ (артворк указания, волна callout kinds) ───────────────
//
// alpha_test.go меряет encodeWEBP на *image.NRGBA, собранном в памяти. Артворк указания приезжает
// иначе: PNG-файл из медиатеки → data URL → UploadContentImage → rawImageFromString → decodeImage →
// те же три рецепта uploadImageObj. Между ними стоит png.Decode, и он отдаёт РАЗНЫЕ типы: 8-битный
// RGBA → *image.NRGBA, 16-битный → *image.NRGBA64, палитра с tRNS → *image.Paletted. Энкодер
// переводит не-NRGBA в NRGBA сам — здесь измерено, что альфа переживает и этот перевод, а не
// предположено. Без S3 и без БД.

func pngDataURL(t *testing.T, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// decodeUpload runs the upload path's front half exactly as UploadContentImage does.
func decodeUpload(t *testing.T, dataURL string) image.Image {
	t.Helper()
	raw, ct, err := rawImageFromString(dataURL)
	if err != nil {
		t.Fatalf("rawImageFromString: %v", err)
	}
	img, err := decodeImage(raw, ct)
	if err != nil {
		t.Fatalf("decodeImage: %v", err)
	}
	return img
}

func TestPNGUploadKeepsAlphaInEveryVariant(t *testing.T) {
	// Tall enough that the thumbnail recipe really rescales (resizeImage only acts above 1080).
	const w, h = 90, 1200

	nrgba64 := image.NewNRGBA64(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		a := uint16(bandAlpha(y, h)) * 0x101
		for x := 0; x < w; x++ {
			nrgba64.SetNRGBA64(x, y, color.NRGBA64{R: 200 * 0x101, G: 60 * 0x101, B: 30 * 0x101, A: a})
		}
	}

	pal := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{
		color.NRGBA{R: 200, G: 60, B: 30, A: 0},
		color.NRGBA{R: 200, G: 60, B: 30, A: 128},
		color.NRGBA{R: 200, G: 60, B: 30, A: 255},
	})
	for y := 0; y < h; y++ {
		idx := uint8(0)
		switch bandAlpha(y, h) {
		case 128:
			idx = 1
		case 255:
			idx = 2
		}
		for x := 0; x < w; x++ {
			pal.SetColorIndex(x, y, idx)
		}
	}

	for _, src := range []struct {
		name string
		img  image.Image
	}{
		{"rgba8", alphaBands(w, h)},
		{"rgba16", nrgba64},
		{"paletted+tRNS", pal},
	} {
		t.Run(src.name, func(t *testing.T) {
			img := decodeUpload(t, pngDataURL(t, src.img))
			// The very first claim: the decoder handed the encoder a picture that still HAS alpha.
			if a := alphaAt(img, w/2, h/6); a != 0 {
				t.Fatalf("decoded PNG lost alpha before encoding: A=%d at a transparent pixel (type %T)", a, img)
			}
			thumb := resizeImage(img, 1080)
			if thumb.Bounds().Dy() != 1080 {
				t.Fatalf("thumbnail recipe did not rescale (height %d)", thumb.Bounds().Dy())
			}
			// The three recipes of uploadImageObj: og q100, compressed q60, thumb q90 over resize.
			checkBands(t, "og q100", encodeDecode(t, img, 100), w, h)
			checkBands(t, "compressed q60", encodeDecode(t, img, 60), w, h)
			tb := thumb.Bounds()
			checkBands(t, "thumb q90", encodeDecode(t, thumb, 90), tb.Dx(), tb.Dy())
		})
	}
}
