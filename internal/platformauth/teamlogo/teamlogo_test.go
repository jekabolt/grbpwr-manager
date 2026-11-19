package teamlogo

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	img.SetNRGBA(0, 0, color.NRGBA{}) // one transparent pixel
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(i)
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pngHeaderOnly is a PNG signature and an IHDR chunk declaring w×h RGBA, with no pixel data:
// the shape of a decompression bomb's header.
func pngHeaderOnly(w, h uint32) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // bit depth 8, colour type RGBA
	var b bytes.Buffer
	b.WriteString(pngMagic)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func TestProcessRefusals(t *testing.T) {
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	big := pngOf(t, 8, 8)
	big = append(big, make([]byte, MaxRawBytes)...) // a valid PNG padded past the cap
	cases := map[string][]byte{
		"empty":            nil,
		"svg":              []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"></svg>`),
		"svg with xml":     []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`),
		"gif":              gifBuf.Bytes(),
		"webp":             []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		"text":             []byte("hello"),
		"png magic only":   []byte(pngMagic),
		"broken png":       append([]byte(pngMagic), bytes.Repeat([]byte{7}, 64)...),
		"jpeg magic only":  []byte(jpegMagic),
		"over 1 MB":        big,
		"wider than 4096":  pngOf(t, MaxSide+1, 1),
		"taller than 4096": pngOf(t, 1, MaxSide+1),
		"bomb header":      pngHeaderOnly(30000, 30000),
		"header, no data":  pngHeaderOnly(64, 64),
		"truncated jpeg":   jpegOf(t, 64, 64)[:200],
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Process(raw); !errors.Is(err, ErrBadLogo) {
				t.Fatalf("Process(%s) = %v, want ErrBadLogo", name, err)
			}
		})
	}
}

func TestProcessFitsAndReencodes(t *testing.T) {
	cases := []struct {
		name         string
		raw          []byte
		wantW, wantH int
	}{
		{"wide png", pngOf(t, 600, 300), 256, 128},
		{"tall png", pngOf(t, 100, 1000), 26, 256},
		{"small png kept", pngOf(t, 40, 20), 40, 20},
		{"square jpeg", jpegOf(t, 400, 400), 256, 256},
		{"exactly 4096", pngOf(t, MaxSide, 2), 256, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Process(c.raw)
			if err != nil {
				t.Fatalf("Process: %v", err)
			}
			if !bytes.HasPrefix(got.PNG, []byte(pngMagic)) {
				t.Fatal("stored bytes are not PNG")
			}
			sum := sha256.Sum256(got.PNG)
			if got.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatal("sha256 does not match the stored bytes")
			}
			img, err := png.Decode(bytes.NewReader(got.PNG))
			if err != nil {
				t.Fatal(err)
			}
			if b := img.Bounds(); b.Dx() != c.wantW || b.Dy() != c.wantH {
				t.Fatalf("size %dx%d, want %dx%d", b.Dx(), b.Dy(), c.wantW, c.wantH)
			}
		})
	}
}

func TestProcessKeepsTransparency(t *testing.T) {
	got, err := Process(pngOf(t, 40, 20))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(got.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatalf("alpha at 0,0 = %d, want 0", a)
	}
	if _, _, _, a := img.At(5, 5).RGBA(); a != 0xffff {
		t.Fatalf("alpha at 5,5 = %d, want opaque", a)
	}
}

func TestFit(t *testing.T) {
	cases := []struct{ w, h, ww, wh int }{
		{256, 256, 256, 256},
		{300, 300, 256, 256},
		{512, 256, 256, 128},
		{256, 512, 128, 256},
		{1000, 3, 256, 1},
		{3, 1000, 1, 256},
		{1, 1, 1, 1},
		{257, 100, 256, 100},
	}
	for _, c := range cases {
		if w, h := Fit(c.w, c.h); w != c.ww || h != c.wh {
			t.Errorf("Fit(%d,%d) = %d,%d, want %d,%d", c.w, c.h, w, h, c.ww, c.wh)
		}
	}
}

func TestURL(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct{ base, slug, sha, want string }{
		{"https://api.lekalo.studio", "acme", sha, "https://api.lekalo.studio/api/auth/team-logo/acme?v=0123456789ab"},
		{"https://api.lekalo.studio/", "acme", sha, "https://api.lekalo.studio/api/auth/team-logo/acme?v=0123456789ab"},
		{"https://api.lekalo.studio", "acme", "", ""},
		{"", "acme", sha, ""},
		{"https://api.lekalo.studio", "", sha, ""},
		{"http://localhost:8081", "a-b", "abc", "http://localhost:8081/api/auth/team-logo/a-b?v=abc"},
	}
	for _, c := range cases {
		if got := URL(c.base, c.slug, c.sha); got != c.want {
			t.Errorf("URL(%q,%q,%q) = %q, want %q", c.base, c.slug, c.sha, got, c.want)
		}
	}
}
