package bucket

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/minio/minio-go/v7"
)

const (
	// maxLabelPayloadBytes caps an uploaded carrier label PDF.
	maxLabelPayloadBytes = 10 * 1024 * 1024 // 10 MB
	// labelFolder segregates carrier shipping-label PDFs from other media in the bucket.
	labelFolder = "shipping-labels"
)

// UploadLabelPDF stores a carrier-generated shipping-label PDF in object storage and returns its
// CDN url plus the stored byte size. Carrier label URLs (AfterShip Shipping / Postmen) expire, so
// the label is copied here for durable retrieval/printing. Like pattern PDFs it is kept out of the
// media library; the object key gets 128 bits of random entropy so the public url is unguessable.
func (b *Bucket) UploadLabelPDF(ctx context.Context, raw []byte, objectName string) (string, int64, error) {
	if len(raw) == 0 {
		return "", 0, fmt.Errorf("%w: payload is empty", ErrInvalidPattern)
	}
	if len(raw) > maxLabelPayloadBytes {
		return "", 0, fmt.Errorf("%w: payload too large: %d bytes, max %d bytes", ErrInvalidPattern, len(raw), maxLabelPayloadBytes)
	}
	if !isPDF(raw) {
		return "", 0, fmt.Errorf("%w: payload is not a PDF", ErrInvalidPattern)
	}

	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return "", 0, fmt.Errorf("can't generate label object name: %w", err)
	}
	objectName = objectName + "-" + hex.EncodeToString(suffix)

	ext, err := fileExtensionFromContentType(contentTypePDF)
	if err != nil {
		return "", 0, err
	}
	fp := b.constructFullPath(labelFolder, objectName, ext)

	r := bytes.NewReader(raw)
	_, err = b.Client.PutObject(ctx, b.S3BucketName, fp, r, int64(r.Len()),
		minio.PutObjectOptions{
			ContentType:  string(contentTypePDF),
			CacheControl: "max-age=31536000",
			UserMetadata: map[string]string{"x-amz-acl": "public-read"},
		})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload label pdf",
			slog.String("err", err.Error()))
		return "", 0, err
	}
	return b.getCDNURL(fp), int64(len(raw)), nil
}
