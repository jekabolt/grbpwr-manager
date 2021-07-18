package bucket

import (
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
)

func PNGFromB64(b64Image string) (image.Image, error) {
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64Image))
	i, err := png.Decode(reader)
	if err != nil {
		return nil, fmt.Errorf("PNGFromB64:image.Decode")
	}
	return i, nil
}

func JPGFromB64(b64Image string) (image.Image, error) {
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64Image))
	i, err := jpeg.Decode(reader)
	if err != nil {
		return nil, fmt.Errorf("JPGFromB64:image.Decode")
	}
	return i, nil
}

func Encode(w io.Writer, img image.Image) error {
	var err error

	var rgba *image.RGBA
	if nrgba, ok := img.(*image.NRGBA); ok {
		if nrgba.Opaque() {
			rgba = &image.RGBA{
				Pix:    nrgba.Pix,
				Stride: nrgba.Stride,
				Rect:   nrgba.Rect,
			}
		}
	}
	if rgba != nil {
		err = jpeg.Encode(w, rgba, &jpeg.Options{Quality: 95})
	} else {
		err = jpeg.Encode(w, img, &jpeg.Options{Quality: 95})
	}

	return err
}
