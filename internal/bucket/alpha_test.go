package bucket

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"testing"

	"golang.org/x/image/webp"
)

// ─────────────────────────── ДЕРЖИТ ЛИ ПРОИЗВОДНАЯ АЛЬФУ ───────────────────────────
//
// ВОПРОС, НА КОТОРЫЙ ОТВЕЧАЕТ ЭТОТ ФАЙЛ, — ДЕНЕЖНЫЙ И ОДИН: картинку с прозрачным фоном (вырез,
// `cutout`) кладут через UploadContentImageVerbatim, полноразмерный объект уходит в бакет
// ПОБАЙТОВО и альфу заведомо сохраняет, — а вот ОБЕ производные (`compressed` и `thumb`) и
// полноразмерная WebP-перекодировка обычного пути проходят через один и тот же encodeWEBP, который
// строит ЛОССИ-энкодер (encoder.NewLossyEncoderOptions). Лосси и альфа — не очевидная пара: если
// альфа схлопывается в непрозрачное, вырез в ленте и в библиотеке нарисуется на чёрном или на
// белом квадрате, а ПОЛНОРАЗМЕРНЫЙ объект при этом будет правильным — то есть дефект будет выглядеть
// как «сервер испортил картинку» ровно в тех местах, куда смотрят чаще всего.
//
// ⚠ ЭТО ЧИСТЫЕ ФУНКЦИИ, БЕЗ S3 И БЕЗ БД. Проверяется не загрузка, а РЕЦЕПТ производных — те же три
// вызова encodeWEBP с теми же качествами и тем же resizeImage, что делают uploadImageObj и
// uploadVerbatimImageObj. Тест, которому нужен бакет, не запускается там, где эта регрессия
// появится, — в обычном прогоне пакета.
//
// ⚠ И ЭТО ПОЛОЖИТЕЛЬНОЕ УТВЕРЖДЕНИЕ, А НЕ «НЕ УПАЛО». Значения альфы читаются обратно попиксельно
// и сверяются с ожидаемыми числами, потому что «webp.Decode вернул картинку» — не доказательство:
// непрозрачный квадрат декодируется так же успешно, как правильный.

// alphaBands is the test picture: three horizontal bands of one colour and three alphas —
// fully transparent, half transparent, fully opaque.
//
// ⚠ ЦВЕТ У ВСЕХ ТРЁХ ПОЛОС ОДИН, И ЭТО НАМЕРЕННО. Лосси-энкодер имеет право двигать ЦВЕТ; вопрос
// здесь только про КАНАЛ АЛЬФЫ, и одинаковый цвет не даёт спутать потерю прозрачности с потерей
// цвета: если альфа схлопнется, полосы станут неразличимы именно по альфе.
func alphaBands(w, h int) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		a := bandAlpha(y, h)
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, color.NRGBA{R: 200, G: 60, B: 30, A: a})
		}
	}
	return m
}

// bandAlpha is the alpha the band containing row y was painted with.
func bandAlpha(y, h int) uint8 {
	switch {
	case y < h/3:
		return 0
	case y < 2*h/3:
		return 128
	default:
		return 255
	}
}

// alphaAt reads the 8-bit alpha of one pixel through the color model, so it works whatever
// concrete image type the decoder returned — x/image/webp answers a lossy-with-alpha file with
// *image.NYCbCrA and a lossless one with *image.NRGBA.
func alphaAt(img image.Image, x, y int) int {
	_, _, _, a := img.At(x, y).RGBA()
	return int(a >> 8)
}

// checkBands asserts the three bands survived the round trip, sampling AWAY FROM THE BOUNDARIES:
// a resize blends across a band edge by design, and a pixel sampled there measures the
// interpolation, not the encoder.
func checkBands(t *testing.T, label string, got image.Image, w, h int) {
	t.Helper()
	type sample struct {
		y      int
		want   uint8
		lo, hi int
	}
	third := h / 3
	samples := []sample{
		// Fully transparent must stay fully transparent: a background that comes back even
		// slightly opaque is a grey haze over the whole cut-out.
		{y: third / 2, want: 0, lo: 0, hi: 0},
		// Half-transparent is the only band a lossy encoder is allowed to move at all, and the
		// window is generous on purpose (96..160 of 255) — the claim is «the gradation survived»,
		// not «the byte is identical».
		{y: third + third/2, want: 128, lo: 96, hi: 160},
		// Opaque must stay opaque: an opaque pixel that comes back translucent shows the page
		// through the garment.
		{y: 2*third + third/2, want: 255, lo: 255, hi: 255},
	}
	for _, s := range samples {
		for _, x := range []int{w / 4, w / 2, 3 * w / 4} {
			a := alphaAt(got, x, s.y)
			if a < s.lo || a > s.hi {
				t.Errorf("%s: pixel (%d,%d) painted A=%d came back A=%d, outside %d..%d",
					label, x, s.y, s.want, a, s.lo, s.hi)
			}
		}
	}
}

// encodeDecode runs one derivative recipe and hands back the decoded picture.
func encodeDecode(t *testing.T, img image.Image, quality int) image.Image {
	t.Helper()
	var buf bytes.Buffer
	if err := encodeWEBP(&buf, img, quality); err != nil {
		t.Fatalf("encodeWEBP(q%d): %v", quality, err)
	}
	got, err := webp.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("webp.Decode of the q%d derivative: %v", quality, err)
	}
	if b := got.Bounds(); b.Dx() != img.Bounds().Dx() || b.Dy() != img.Bounds().Dy() {
		t.Fatalf("q%d derivative came back %dx%d, wanted %dx%d",
			quality, b.Dx(), b.Dy(), img.Bounds().Dx(), img.Bounds().Dy())
	}
	return got
}

// TestWEBPDerivativesKeepAlpha walks the three encodeWEBP calls the upload paths actually make —
// full-size q100 (uploadImageObj), compressed q60 (both paths) and thumbnail q90 over
// resizeImage(img, 1080) (both paths) — and insists a transparent pixel is still transparent on
// the other side.
func TestWEBPDerivativesKeepAlpha(t *testing.T) {
	const w, h = 64, 64
	src := alphaBands(w, h)

	// The thumbnail recipe of a picture SMALLER than the ceiling: resizeImage returns the source
	// untouched, so this measures the q90 encode alone. The genuinely-scaled case is the test
	// below, and both are the production path — a cut-out arrives at whatever size it was drawn.
	thumb := resizeImage(src, 1080)
	if thumb != image.Image(src) {
		t.Fatalf("resizeImage rescaled a %dx%d picture against a %d ceiling; the recipe under test "+
			"is not the one production runs", w, h, 1080)
	}

	for _, tc := range []struct {
		name    string
		img     image.Image
		quality int
	}{
		{"full-size q100", src, 100},
		{"compressed q60", src, 60},
		{"thumbnail q90", thumb, 90},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeDecode(t, tc.img, tc.quality)
			checkBands(t, tc.name, got, w, h)
		})
	}
}

// TestWEBPThumbnailKeepsAlphaThroughResize is the half of the thumbnail recipe the 64×64 picture
// above cannot reach: resizeImage only does anything to a picture TALLER than the ceiling, and it
// scales into an *image.RGBA — a PREMULTIPLIED buffer — with draw.Over. Both of those are places
// alpha can be lost without the encoder having anything to do with it, so the scaled path is
// measured separately rather than assumed to behave like the unscaled one.
func TestWEBPThumbnailKeepsAlphaThroughResize(t *testing.T) {
	const w, h = 96, 1200
	src := alphaBands(w, h)

	thumb := resizeImage(src, 1080)
	tb := thumb.Bounds()
	if tb.Dy() != 1080 {
		t.Fatalf("resizeImage(%dx%d, 1080) produced height %d; the scaled path is not under test", w, h, tb.Dy())
	}

	got := encodeDecode(t, thumb, 90)
	checkBands(t, "thumbnail q90 after resize", got, tb.Dx(), tb.Dy())
}

// TestWEBPAlphaGradientSurvives is the sharper form of the same question: a cut-out's value is in
// its EDGE, where alpha is neither 0 nor 255 but a ramp a few pixels wide. A codec that kept only
// the two extremes would pass the banded test above and still leave every cut-out with a hard,
// aliased outline.
func TestWEBPAlphaGradientSurvives(t *testing.T) {
	const size = 64
	src := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// A horizontal ramp: alpha follows the column, 0 at the left edge, 252 at the right.
			src.SetNRGBA(x, y, color.NRGBA{R: 200, G: 60, B: 30, A: uint8(x * 4)})
		}
	}

	var buf bytes.Buffer
	if err := encodeWEBP(&buf, src, 60); err != nil {
		t.Fatalf("encodeWEBP(q60): %v", err)
	}
	got, err := webp.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("webp.Decode: %v", err)
	}

	// Read the ramp back off the middle row and insist it is still a ramp: monotonic within a
	// small slack, and hitting both ends.
	var report []string
	prev := -1
	for x := 0; x < size; x++ {
		a := alphaAt(got, x, size/2)
		if x%8 == 0 {
			report = append(report, fmt.Sprintf("x=%d want=%d got=%d", x, x*4, a))
		}
		if want := x * 4; a < want-16 || a > want+16 {
			t.Errorf("alpha ramp: column %d painted A=%d came back A=%d (samples: %v)", x, want, a, report)
			break
		}
		if a+8 < prev {
			t.Errorf("alpha ramp is no longer monotonic at column %d: %d after %d (samples: %v)", x, a, prev, report)
			break
		}
		prev = a
	}
}
