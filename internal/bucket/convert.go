package bucket

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/kolesa-team/go-webp/encoder"
	webpenc "github.com/kolesa-team/go-webp/webp"
	"golang.org/x/image/webp"
)

func decodeImageFromB64(b64Image []byte, contentType ContentType) (image.Image, error) {
	reader := base64.NewDecoder(base64.StdEncoding, bytes.NewReader(b64Image))
	switch contentType {
	case contentTypeJPEG:
		return jpeg.Decode(reader)
	case contentTypePNG:
		return png.Decode(reader)
	case contentTypeWEBP:
		return webp.Decode(reader)
	default:
		return nil, fmt.Errorf("unsupported content type: %s", contentType)
	}
}

func encodeWEBP(w io.Writer, img image.Image, quality int) error {
	options, err := encoder.NewLossyEncoderOptions(encoder.PresetPhoto, float32(quality))
	if err != nil {
		return err
	}
	return webpenc.Encode(w, img, options)
}
