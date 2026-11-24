package bucket

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func solid(w, h int, c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func decodePreview(t *testing.T, s string) image.Image {
	t.Helper()
	if !strings.HasPrefix(s, "data:image/png;base64,") {
		t.Fatalf("missing data URI prefix: %q", s)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	return img
}

func TestPixelPreviewDimensions(t *testing.T) {
	cases := []struct {
		name       string
		w, h, outH int
	}{
		{"portrait 4:5", 864, 1080, 8},
		{"landscape 3:2", 1620, 1080, 4},
		{"square", 1080, 1080, 6},
		{"very wide clamps to 1", 4000, 100, 1},
		{"very tall clamps to 24", 100, 4000, 24},
		{"tiny source upscales", 3, 3, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := PixelPreview(solid(tc.w, tc.h, color.RGBA{10, 20, 30, 255}))
			img := decodePreview(t, s)
			if got := img.Bounds().Dx(); got != 6 {
				t.Fatalf("width = %d, want 6", got)
			}
			if got := img.Bounds().Dy(); got != tc.outH {
				t.Fatalf("height = %d, want %d", got, tc.outH)
			}
			if len(s) > 1024 {
				t.Fatalf("preview %d bytes exceeds the VARCHAR(1024) column", len(s))
			}
		})
	}
}

func TestPixelPreviewKeepsColour(t *testing.T) {
	img := decodePreview(t, PixelPreview(solid(800, 1000, color.RGBA{255, 0, 0, 255})))
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if r>>8 < 250 || g>>8 > 5 || bl>>8 > 5 || a>>8 < 250 {
				t.Fatalf("pixel (%d,%d) = %d,%d,%d,%d, want red", x, y, r>>8, g>>8, bl>>8, a>>8)
			}
		}
	}
}

func TestPixelPreviewKeepsLayout(t *testing.T) {
	// Left half red, right half blue: the mosaic must keep that split, not average it away.
	src := image.NewRGBA(image.Rect(0, 0, 600, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 600; x++ {
			if x < 300 {
				src.Set(x, y, color.RGBA{255, 0, 0, 255})
			} else {
				src.Set(x, y, color.RGBA{0, 0, 255, 255})
			}
		}
	}
	img := decodePreview(t, PixelPreview(src))
	r0, _, b0, _ := img.At(0, 3).RGBA()
	r5, _, b5, _ := img.At(5, 3).RGBA()
	if r0>>8 < 200 || b0>>8 > 50 || b5>>8 < 200 || r5>>8 > 50 {
		t.Fatalf("left/right colours lost: left=(%d,_,%d) right=(%d,_,%d)", r0>>8, b0>>8, r5>>8, b5>>8)
	}
}

func TestPixelPreviewEmptyInput(t *testing.T) {
	if got := PixelPreview(nil); got != "" {
		t.Fatalf("nil image: got %q", got)
	}
	if got := PixelPreview(image.NewRGBA(image.Rect(0, 0, 0, 10))); got != "" {
		t.Fatalf("zero-width image: got %q", got)
	}
}
