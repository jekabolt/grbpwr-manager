package bucket

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/minio/minio-go/v7"
)

const (
	// maxPatternPayloadBytes caps an uploaded PDF выкройка (cut pattern).
	maxPatternPayloadBytes = 25 * 1024 * 1024 // 25 MB
	// patternFolder segregates pattern PDFs from image/video media in the bucket.
	patternFolder = "tech-card-patterns"
)

// ErrInvalidPattern marks a rejected pattern payload (empty, too large, or not a PDF) so
// the API layer can map it to InvalidArgument rather than Internal (an S3 failure).
var ErrInvalidPattern = errors.New("invalid pattern file")

// UploadPatternPDF stores a raw PDF cut pattern (выкройка) in object storage and returns
// its CDN url plus the stored byte size. The payload must be a real PDF (sniffed by the
// %PDF- magic header, not the caller-declared type). Unlike images and videos it is NOT
// recorded in the media table — pattern files are kept out of the image library.
func (b *Bucket) UploadPatternPDF(ctx context.Context, raw []byte, objectName string) (string, int64, error) {
	if len(raw) == 0 {
		return "", 0, fmt.Errorf("%w: payload is empty", ErrInvalidPattern)
	}
	if len(raw) > maxPatternPayloadBytes {
		return "", 0, fmt.Errorf("%w: payload too large: %d bytes, max %d bytes", ErrInvalidPattern, len(raw), maxPatternPayloadBytes)
	}
	if !isPDF(raw) {
		return "", 0, fmt.Errorf("%w: payload is not a PDF", ErrInvalidPattern)
	}

	// Pattern PDFs are internal production IP (выкройки) but are stored public-read
	// because the admin app reads them by CDN url. Add 128 bits of random entropy to
	// the object key so the public url is effectively unguessable and non-enumerable
	// (the GetMediaName-derived key had only ~16 bits). The durable fix is to store
	// the object privately and serve it via a short-lived presigned url, which needs
	// a read-path (and admin frontend) change; this hardening is non-breaking.
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return "", 0, fmt.Errorf("can't generate pattern object name: %w", err)
	}
	objectName = objectName + "-" + hex.EncodeToString(suffix)

	ext, err := fileExtensionFromContentType(contentTypePDF)
	if err != nil {
		return "", 0, err
	}
	fp := b.constructFullPath(patternFolder, objectName, ext)

	r := bytes.NewReader(raw)
	_, err = b.Client.PutObject(ctx, b.S3BucketName, fp, r, int64(r.Len()),
		minio.PutObjectOptions{
			ContentType:  string(contentTypePDF),
			CacheControl: "max-age=31536000",
			UserMetadata: map[string]string{"x-amz-acl": "public-read"},
		})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload pattern pdf",
			slog.String("err", err.Error()))
		return "", 0, err
	}
	return b.getCDNURL(fp), int64(len(raw)), nil
}

// isPDF reports whether raw starts with the PDF magic header (%PDF-).
func isPDF(raw []byte) bool {
	return len(raw) >= 5 && string(raw[:5]) == "%PDF-"
}
