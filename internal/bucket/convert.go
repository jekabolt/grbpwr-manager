package bucket

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
)

func decodeImageFromB64(b64Image []byte, contentType ContentType) (image.Image, error) {
	reader := base64.NewDecoder(base64.StdEncoding, bytes.NewReader(b64Image))
	switch contentType {
	case contentTypeJPEG:
		return jpeg.Decode(reader)
	case contentTypePNG:
		return png.Decode(reader)
	default:
		return nil, fmt.Errorf("unsupported content type: %s", contentType)
	}
}

func encodeJPG(w io.Writer, img image.Image, quality int) error {
	var rgba *image.RGBA
	if nrgba, ok := img.(*image.NRGBA); ok && nrgba.Opaque() {
		rgba = &image.RGBA{
			Pix:    nrgba.Pix,
			Stride: nrgba.Stride,
			Rect:   nrgba.Rect,
		}
	}

	opts := &jpeg.Options{Quality: quality}
	if rgba != nil {
		return jpeg.Encode(w, rgba, opts)
	}
	return jpeg.Encode(w, img, opts)
}
