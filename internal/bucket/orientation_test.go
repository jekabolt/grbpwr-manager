package bucket

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// jpegWithOrientation encodes img as JPEG and splices an APP1/EXIF segment carrying
// the given Orientation right after SOI, the way a phone camera writes it.
func jpegWithOrientation(t *testing.T, img image.Image, o uint16) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1}
	entry := make([]byte, 12)
	binary.BigEndian.PutUint16(entry[0:], 0x0112)
	binary.BigEndian.PutUint16(entry[2:], 3) // SHORT
	binary.BigEndian.PutUint32(entry[4:], 1)
	binary.BigEndian.PutUint16(entry[8:], o)
	tiff = append(tiff, entry...)
	tiff = append(tiff, 0, 0, 0, 0)
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	seg = append(seg, payload...)
	raw := buf.Bytes()
	return append(append(append([]byte{}, raw[:2]...), seg...), raw[2:]...)
}

func TestDecodeJPEGAppliesEXIFOrientation(t *testing.T) {
	// 40x20 landscape: left half red, right half blue.
	src := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := color.RGBA{255, 0, 0, 255}
			if x >= 20 {
				c = color.RGBA{0, 0, 255, 255}
			}
			src.Set(x, y, c)
		}
	}
	isRed := func(c color.Color) bool { r, _, b, _ := c.RGBA(); return r > b }

	for _, tc := range []struct {
		o          uint16
		w, h       int
		redAtTopOr string // where the red half ends up
	}{
		{1, 40, 20, "left"},
		{3, 40, 20, "right"},
		{6, 20, 40, "top"},    // rotate 90 CW: left edge goes to the top
		{8, 20, 40, "bottom"}, // rotate 90 CCW: left edge goes to the bottom
	} {
		raw := jpegWithOrientation(t, src, tc.o)
		if got := jpegEXIFOrientation(raw); got != int(tc.o) {
			t.Fatalf("o=%d: parsed orientation %d", tc.o, got)
		}
		img, err := decodeImage(raw, contentTypeJPEG)
		if err != nil {
			t.Fatalf("o=%d: %v", tc.o, err)
		}
		b := img.Bounds()
		if b.Dx() != tc.w || b.Dy() != tc.h {
			t.Fatalf("o=%d: got %dx%d, want %dx%d", tc.o, b.Dx(), b.Dy(), tc.w, tc.h)
		}
		var probe color.Color
		switch tc.redAtTopOr {
		case "left":
			probe = img.At(b.Min.X+2, b.Min.Y+b.Dy()/2)
		case "right":
			probe = img.At(b.Max.X-3, b.Min.Y+b.Dy()/2)
		case "top":
			probe = img.At(b.Min.X+b.Dx()/2, b.Min.Y+2)
		case "bottom":
			probe = img.At(b.Min.X+b.Dx()/2, b.Max.Y-3)
		}
		if !isRed(probe) {
			t.Fatalf("o=%d: red half not on the %s", tc.o, tc.redAtTopOr)
		}
	}
}

func TestJPEGEXIFOrientationAbsent(t *testing.T) {
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
	if got := jpegEXIFOrientation(buf.Bytes()); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
	if got := jpegEXIFOrientation([]byte{0xFF, 0xD8, 0xFF}); got != 1 {
		t.Fatalf("truncated: got %d", got)
	}
}
