package bucket

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"strings"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/minio/minio-go/v7"
)

type B64Image struct {
	content     []byte
	contentType string
}

type uploadedMedia struct {
	url string
	id  string
}

// upload image to bucket return url
func (b *Bucket) uploadImageToBucket(ctx context.Context, img io.Reader, folder, imageName, contentType string) (*uploadedMedia, error) {
	ext, err := fileExtensionFromContentType(contentType)
	if err != nil {
		return nil, fmt.Errorf("can't get file extension")
	}
	fp := b.constructFullPath(folder, imageName, ext)

	data, err := io.ReadAll(img)
	if err != nil {
		return nil, err
	}

	r := bytes.NewReader(data)
	userMetaData := map[string]string{"x-amz-acl": "public-read"}
	cacheControl := "max-age=31536000"

	ui, err := b.Client.PutObject(ctx, b.Config.S3BucketName, fp, r,
		int64(r.Len()), minio.PutObjectOptions{
			ContentType:  contentType,
			CacheControl: cacheControl,
			UserMetadata: userMetaData,
		},
	)

	if err != nil {
		return nil, fmt.Errorf("error putting object: %v", err)
	}

	return &uploadedMedia{
		url: b.getCDNURL(fp),
		id:  ui.Key,
	}, nil
}

// getB64ImageFromString extracts the content type and the byte content from a raw base64 image string.
// The expected format of the raw base64 string is "data:[<mediatype>];base64,[<base64-data>]".
func getB64ImageFromString(rawB64Image string) (*B64Image, error) {
	const base64Prefix = ";base64,"
	parts := strings.Split(rawB64Image, base64Prefix)

	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid base64 image format: expected 'data:[mediatype];base64,[data]'")
	}

	return &B64Image{
		contentType: parts[0],
		content:     []byte(parts[1]),
	}, nil
}

func (b64Img *B64Image) b64ToImage() (image.Image, error) {
	switch b64Img.contentType {
	case "data:image/jpeg":
		return decodeImageFromB64(b64Img.content, contentTypeJPEG)
	case "data:image/png":
		return decodeImageFromB64(b64Img.content, contentTypePNG)
	default:
		return nil, fmt.Errorf("b64ToImage: File type is not supported [%s]", b64Img.contentType)
	}
}
func imageFromString(rawB64Image string) (image.Image, error) {
	b64Img, err := getB64ImageFromString(rawB64Image)
	if err != nil {
		return nil, err
	}
	return b64Img.b64ToImage()
}

// upload single image with defined quality and	prefix to bucket
func (b *Bucket) uploadSingleImage(ctx context.Context, img image.Image, quality int, folder, imageName string) (*uploadedMedia, error) {
	var buf bytes.Buffer

	// Encode the image to JPEG format with given quality.
	if err := encodeJPG(&buf, img, quality); err != nil {
		return nil, fmt.Errorf("failed to encode JPG: %v", err)
	}

	// Upload the JPEG data to S3 bucket.
	url, err := b.uploadImageToBucket(ctx, &buf, folder, imageName, contentTypeJPEG)
	if err != nil {
		return nil, fmt.Errorf("failed to upload image to bucket: %v", err)
	}

	return url, nil
}

// compose internal image object (with FullSize & Compressed formats) and upload it to S3
func (b *Bucket) uploadImageObj(ctx context.Context, img image.Image, folder, imageName string) (*pb_common.Media, error) {
	imgObj := &pb_common.Media{}

	fullSizeName := fmt.Sprintf("%s_%s", imageName, "og")
	compressedName := fmt.Sprintf("%s_%s", imageName, "compressed")

	// Upload full size image
	if um, err := b.uploadSingleImage(ctx, img, 100, folder, fullSizeName); err != nil {
		return nil, fmt.Errorf("failed to upload full-size image: %v", err)
	} else {
		imgObj.FullSize = um.url
		imgObj.ObjectIds = append(imgObj.ObjectIds, um.id)
	}

	// Upload compressed image
	if um, err := b.uploadSingleImage(ctx, img, 60, folder, compressedName); err != nil {
		return nil, fmt.Errorf("failed to upload compressed image: %v", err)
	} else {
		imgObj.Compressed = um.url
		imgObj.ObjectIds = append(imgObj.ObjectIds, um.id)
	}

	return imgObj, nil
}
