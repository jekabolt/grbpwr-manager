package bucket

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

// makeAnimatedGIF builds a small 2-frame animated GIF so tests can assert the raw
// bytes survive the upload-routing path (rather than being flattened to one frame).
func makeAnimatedGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	pal := color.Palette{color.Black, color.White}
	frame := func(c uint8) *image.Paletted {
		p := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		for i := range p.Pix {
			p.Pix[i] = c
		}
		return p
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{
		Image:     []*image.Paletted{frame(0), frame(1)},
		Delay:     []int{10, 10},
		LoopCount: 0,
	}); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

// TestGIFRoutingPreservesRawBytes proves the animated-GIF branch is taken and the
// original multi-frame payload is what would be uploaded — the WebP re-encode path
// would collapse it to a single frame.
func TestGIFRoutingPreservesRawBytes(t *testing.T) {
	raw := makeAnimatedGIF(t, 8, 6)

	// The upload router branches on the sniffed type, not the declared label.
	if got := sniffImageType(raw); got != contentTypeGIF {
		t.Fatalf("sniffImageType = %q, want %q", got, contentTypeGIF)
	}

	// rawImageFromString must return the bytes verbatim (a mislabeled data URL still
	// routes by sniff, so the label here is deliberately wrong).
	decoded, ct, err := rawImageFromString(dataURL("image/png", raw))
	if err != nil {
		t.Fatalf("rawImageFromString: %v", err)
	}
	if ct != contentTypePNG {
		t.Fatalf("declared content type = %q, want the label image/png", ct)
	}
	if !bytes.Equal(decoded, raw) {
		t.Fatalf("raw bytes altered: got %d bytes, want %d", len(decoded), len(raw))
	}

	// Dimensions used for the media row come from the GIF header, and both frames
	// must still be present (proving nothing flattened the animation).
	cfg, err := gif.DecodeConfig(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.Width != 8 || cfg.Height != 6 {
		t.Fatalf("dims = %dx%d, want 8x6", cfg.Width, cfg.Height)
	}
	g, err := gif.DecodeAll(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if len(g.Image) != 2 {
		t.Fatalf("frames = %d, want 2 (animation preserved)", len(g.Image))
	}
}

// TestGIFExtensionRegistered guards the S3 key extension the pass-through relies on.
func TestGIFExtensionRegistered(t *testing.T) {
	ext, err := fileExtensionFromContentType(contentTypeGIF)
	if err != nil {
		t.Fatalf("gif extension: %v", err)
	}
	if ext != "gif" {
		t.Fatalf("gif extension = %q, want gif", ext)
	}
}
