package bucket

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/png"
	"math"

	"golang.org/x/image/draw"
)

const (
	// PixelPreviewWidth is the width in px of the colour preview frontends draw as a pixel mosaic
	// while the real image loads.
	PixelPreviewWidth = 6
	// pixelPreviewMaxHeight caps very tall pictures (a 1:4 strip still gets a sane mosaic).
	pixelPreviewMaxHeight = 24
	pixelPreviewPrefix    = "data:image/png;base64,"
)

// PixelPreview returns a tiny COLOUR preview of img: PixelPreviewWidth px wide, height by aspect
// ratio clamped to [1, 24], PNG-encoded as a data URI (~100-300 bytes). It is decorative: any
// failure returns "" and the caller stores nothing — an upload never fails because of it.
//
// It is deliberately NOT derived from the blurhash path, which is grayscale (toGrayscale).
func PixelPreview(img image.Image) string {
	if img == nil {
		return ""
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return ""
	}
	ph := clamp(int(math.Round(float64(PixelPreviewWidth)*float64(h)/float64(w))), 1, pixelPreviewMaxHeight)

	dst := image.NewNRGBA(image.Rect(0, 0, PixelPreviewWidth, ph))
	// CatmullRom scales its kernel by the downscale ratio, so every source pixel contributes —
	// a 1080 px thumbnail averages properly instead of point-sampling a few pixels.
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, dst); err != nil {
		return ""
	}
	return pixelPreviewPrefix + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// PixelPreviewFromBytes decodes an already-stored thumbnail (WebP, JPEG, PNG, HEIC or GIF — first
// frame) and returns PixelPreview of it. Used by the backfill, so old rows get exactly what a new
// upload gets. The decoder's pixel budget applies (decodeImage).
func PixelPreviewFromBytes(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("empty image")
	}
	var (
		img image.Image
		err error
	)
	if sniffImageType(raw) == contentTypeGIF {
		img, err = gif.Decode(bytes.NewReader(raw))
	} else {
		img, err = decodeImage(raw, "")
	}
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	pp := PixelPreview(img)
	if pp == "" {
		return "", errors.New("preview encode failed")
	}
	return pp, nil
}
