package bucket

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/gen2brain/heic"
)

func dataURL(mime string, raw []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	m.Set(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

func tinyJPEG(t *testing.T) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, nil); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	return buf.Bytes()
}

// imageFromString must decode by sniffing the real bytes and ignore a wrong
// data-URL MIME label. This is the production bug: Apple HEIC photos are
// uploaded under a data:image/jpeg label and used to fail with "missing SOI
// marker".
func TestImageFromString_SniffsRealFormatIgnoringLabel(t *testing.T) {
	heicBytes, err := os.ReadFile("files/test.heic")
	if err != nil {
		t.Fatalf("read heic fixture: %v", err)
	}

	cases := []struct {
		name         string
		url          string
		needsLibheif bool
	}{
		{"heic mislabeled as jpeg", dataURL("image/jpeg", heicBytes), true},
		{"png mislabeled as jpeg", dataURL("image/jpeg", tinyPNG(t)), false},
		{"jpeg mislabeled as png", dataURL("image/png", tinyJPEG(t)), false},
		{"png correctly labeled", dataURL("image/png", tinyPNG(t)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.needsLibheif {
				if derr := heic.Dynamic(); derr != nil {
					t.Skipf("libheif not available: %v", derr)
				}
			}
			img, err := imageFromString(tc.url)
			if err != nil {
				t.Fatalf("imageFromString: %v", err)
			}
			if img.Bounds().Empty() {
				t.Fatal("decoded image has empty bounds")
			}
		})
	}
}

// Unsupported and malformed payloads must yield clear, actionable errors instead
// of a cryptic decoder failure.
func TestImageFromString_UnsupportedAndGarbage(t *testing.T) {
	avif := append([]byte{0, 0, 0, 0x18}, "ftypavif"...)
	avif = append(avif, make([]byte, 16)...)
	if _, err := imageFromString(dataURL("image/jpeg", avif)); err == nil ||
		!strings.Contains(err.Error(), "image/avif") {
		t.Fatalf("want avif unsupported error, got %v", err)
	}

	if _, err := imageFromString(dataURL("image/jpeg", []byte("this is not an image"))); err == nil ||
		!strings.Contains(err.Error(), "unrecognized") {
		t.Fatalf("want unrecognized-format error, got %v", err)
	}
}

func TestSniffImageType(t *testing.T) {
	ftyp := func(brand string) []byte {
		b := append([]byte{0, 0, 0, 0x18}, "ftyp"...)
		b = append(b, brand...)
		return append(b, make([]byte, 8)...)
	}
	cases := []struct {
		name string
		in   []byte
		want ContentType
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, contentTypeJPEG},
		{"png", pngMagic, contentTypePNG},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), contentTypeWEBP},
		{"gif", []byte("GIF89a___"), contentTypeGIF},
		{"heic brand", ftyp("heic"), contentTypeHEIC},
		{"mif1 brand", ftyp("mif1"), contentTypeHEIC},
		{"avif brand", ftyp("avif"), contentTypeAVIF},
		{"unknown", []byte("\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b"), ContentType("")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sniffImageType(tc.in); got != tc.want {
				t.Fatalf("sniffImageType = %q, want %q", got, tc.want)
			}
		})
	}
}
