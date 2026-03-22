package bucket

import (
	"context"
	"fmt"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

const (
	// maxImagePayloadBytes is the maximum allowed size of a base64-encoded image string (~20 MB decoded).
	maxImagePayloadBytes = 28 * 1024 * 1024 // base64 overhead is ~1.37x, so 28 MB covers 20 MB decoded
	// maxImageDimension is the maximum allowed width or height of a decoded image in pixels.
	maxImageDimension = 12000
	// maxVideoPayloadBytes is the maximum allowed size of a raw video payload.
	maxVideoPayloadBytes = 50 * 1024 * 1024
)

// UploadContentImage decodes a base64 image and uploads full-size, compressed, and thumbnail
// variants to S3. Supported content types: image/jpeg, image/png, image/webp.
func (b *Bucket) UploadContentImage(ctx context.Context, rawB64Image, folder, imageName string) (*pb_common.MediaFull, error) {
	if len(rawB64Image) == 0 {
		return nil, fmt.Errorf("image payload is empty")
	}
	if len(rawB64Image) > maxImagePayloadBytes {
		return nil, fmt.Errorf("image payload too large: %d bytes, max %d bytes", len(rawB64Image), maxImagePayloadBytes)
	}

	img, err := imageFromString(rawB64Image)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 image: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() > maxImageDimension || bounds.Dy() > maxImageDimension {
		return nil, fmt.Errorf("image dimensions %dx%d exceed maximum allowed %dpx", bounds.Dx(), bounds.Dy(), maxImageDimension)
	}

	return b.uploadImageObj(ctx, img, folder, imageName)
}

// UploadContentVideo uploads a raw video payload to S3. Supported content types: video/mp4, video/webm.
func (b *Bucket) UploadContentVideo(ctx context.Context, raw []byte, folder, videoName string, contentType string) (*pb_common.MediaFull, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("video payload is empty")
	}
	if len(raw) > maxVideoPayloadBytes {
		return nil, fmt.Errorf("video payload too large: %d bytes, max %d bytes", len(raw), maxVideoPayloadBytes)
	}
	return b.uploadVideoObj(ctx, raw, folder, videoName, contentType)
}
