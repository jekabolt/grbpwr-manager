package bucket

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/gen2brain/heic"
	"github.com/kolesa-team/go-webp/encoder"
	webpenc "github.com/kolesa-team/go-webp/webp"
	"golang.org/x/image/webp"
)

// pngMagic is the 8-byte PNG file signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}

// decodeImage decodes raw (already base64-decoded) image bytes. The actual
// format is detected from the leading bytes rather than trusting the
// caller-declared content type, because browsers routinely upload HEIC photos
// (e.g. from Apple devices) mislabeled as image/jpeg. declared is used only to
// enrich error messages.
// maxImagePixels bounds the decoded raster size. It is checked from the image
// header (DecodeConfig) BEFORE the full decode, so a small but highly-compressed
// payload that expands to a huge raster (a decompression bomb) is rejected before
// allocating hundreds of MB. ~40 MP is generous for a storefront master image.
const maxImagePixels = 40_000_000

func decodeImage(raw []byte, declared ContentType) (image.Image, error) {
	ct := sniffImageType(raw)
	if err := checkImagePixelBudget(ct, raw); err != nil {
		return nil, err
	}
	switch ct {
	case contentTypeJPEG:
		return jpeg.Decode(bytes.NewReader(raw))
	case contentTypePNG:
		return png.Decode(bytes.NewReader(raw))
	case contentTypeWEBP:
		return webp.Decode(bytes.NewReader(raw))
	case contentTypeHEIC:
		return decodeHEIC(raw)
	case "":
		return nil, fmt.Errorf("unrecognized image format (declared %q); supported formats: JPEG, PNG, WebP, HEIC", declared)
	default:
		return nil, fmt.Errorf("unsupported image format %q (declared %q); supported formats: JPEG, PNG, WebP, HEIC", ct, declared)
	}
}

// checkImagePixelBudget reads only the image header (cheap, no full raster) and
// rejects images whose pixel count exceeds maxImagePixels. HEIC has no header
// config path here and is bounded separately (see decodeHEIC).
func checkImagePixelBudget(ct ContentType, raw []byte) error {
	var (
		cfg image.Config
		err error
	)
	switch ct {
	case contentTypeJPEG:
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(raw))
	case contentTypePNG:
		cfg, err = png.DecodeConfig(bytes.NewReader(raw))
	case contentTypeWEBP:
		cfg, err = webp.DecodeConfig(bytes.NewReader(raw))
	default:
		return nil
	}
	if err != nil {
		return fmt.Errorf("can't read image header: %w", err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return fmt.Errorf("image too large: %dx%d exceeds %d-pixel limit", cfg.Width, cfg.Height, maxImagePixels)
	}
	return nil
}

// decodeHEIC decodes a HEIC payload via native libheif, loaded at runtime by
// github.com/gen2brain/heic (purego, dynamic mode). It deliberately refuses to
// fall back to that library's pure-Go WASM decoder, which corrupts colors
// (wrong chroma-plane strides) on real photos. Because libheif is resolved by
// symbol name at call time there is no build-time version coupling: the same
// binary works against the dev machine's libheif and the older one in the
// runtime image. The decode is wrapped in recover, since the decoder can panic
// on malformed input instead of returning an error.
func decodeHEIC(raw []byte) (img image.Image, err error) {
	if derr := heic.Dynamic(); derr != nil {
		return nil, fmt.Errorf("heic decoding unavailable: libheif not loaded (%w)", derr)
	}
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, fmt.Errorf("heic decode failed: %v", r)
		}
	}()
	return heic.Decode(bytes.NewReader(raw))
}

// sniffImageType reports the actual content type of an image payload from its
// magic bytes, or "" if unrecognized. It may return a format that is not
// decodable here (AVIF/HEIF/GIF) so callers can produce a precise error.
func sniffImageType(b []byte) ContentType {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return contentTypeJPEG
	case len(b) >= 8 && bytes.Equal(b[:8], pngMagic):
		return contentTypePNG
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return contentTypeWEBP
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return contentTypeGIF
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return sniffISOBMFF(b)
	default:
		return ""
	}
}

// sniffISOBMFF classifies an ISO base media file (`ftyp` box) by its major and
// compatible brands, distinguishing HEIC (HEVC-coded, decodable here) from AVIF
// (AV1-coded, not) and other HEIF variants.
func sniffISOBMFF(b []byte) ContentType {
	size := int(binary.BigEndian.Uint32(b[0:4]))
	if size < 16 || size > len(b) {
		size = len(b)
	}

	brands := []string{string(b[8:12])} // major brand
	for i := 16; i+4 <= size; i += 4 {  // compatible brands
		brands = append(brands, string(b[i:i+4]))
	}

	for _, br := range brands {
		if br == "avif" || br == "avis" {
			return contentTypeAVIF
		}
	}
	for _, br := range brands {
		switch br {
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "hevm", "hevs", "mif1", "msf1":
			return contentTypeHEIC
		}
	}
	return contentTypeHEIF
}

// sniffVideoType reports the container type of a video payload from its magic
// bytes, or "" if unrecognized. WebM (Matroska/EBML) starts with 0x1A45DFA3;
// MP4/ISO-BMFF carries the "ftyp" box at bytes 4:8. Mirrors sniffImageType so the
// video path validates bytes instead of trusting the client-declared content type.
func sniffVideoType(b []byte) ContentType {
	switch {
	case len(b) >= 4 && b[0] == 0x1A && b[1] == 0x45 && b[2] == 0xDF && b[3] == 0xA3:
		return contentTypeWEBM
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return contentTypeMP4
	default:
		return ""
	}
}

func encodeWEBP(w io.Writer, img image.Image, quality int) error {
	options, err := encoder.NewLossyEncoderOptions(encoder.PresetPhoto, float32(quality))
	if err != nil {
		return err
	}
	return webpenc.Encode(w, img, options)
}
