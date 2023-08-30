package bucket

import (
	"context"
	"fmt"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// UploadContentImage get raw image from b64 encoded string and upload full size and compressed images to s3
func (b *Bucket) UploadContentImage(ctx context.Context, rawB64Image, folder, imageName string) (*pb_common.Media, error) {
	img, err := imageFromString(rawB64Image)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 image: %v", err)
	}
	return b.uploadImageObj(ctx, img, folder, imageName)
}

// UploadContentVideo get raw video from uint8 array and upload video to s3
func (b *Bucket) UploadContentVideo(ctx context.Context, raw []byte, folder, videoName, contentType string) (*pb_common.Media, error) {
	return b.uploadVideoObj(ctx, raw, folder, videoName, contentType)
}
