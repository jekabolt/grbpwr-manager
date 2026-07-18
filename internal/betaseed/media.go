package betaseed

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"

	admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// UploadJPEG generates a small real JPEG in pure Go (no convert/sips dependency),
// uploads it via UploadContentImage as a data-URI, and returns the media id.
func (s *Seeder) UploadJPEG(ctx context.Context, label string) (int32, error) {
	dataURI, err := makeJPEGDataURI(label)
	if err != nil {
		return 0, err
	}
	r, err := s.C.UploadContentImage(ctx, &admin.UploadContentImageRequest{RawB64Image: dataURI})
	if err != nil {
		return 0, fmt.Errorf("upload image %q: %w", label, err)
	}
	id := r.GetMedia().GetId()
	if id == 0 {
		return 0, fmt.Errorf("upload image %q: response carried no media id", label)
	}
	return id, nil
}

// makeJPEGDataURI produces a 240x300 solid-colour JPEG (colour derived from label so
// distinct labels get distinct images) as a base64 data URI.
func makeJPEGDataURI(label string) (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, 240, 300))
	h := fnv.New32a()
	_, _ = h.Write([]byte(label))
	sum := h.Sum32()
	col := color.RGBA{R: uint8(sum), G: uint8(sum >> 8), B: uint8(sum >> 16), A: 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{C: col}, image.Point{}, draw.Src)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		return "", fmt.Errorf("encode jpeg for %q: %w", label, err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
