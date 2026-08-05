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
	// maxPatternPayloadBytes caps an uploaded выкройка (cut pattern) file. 40 MB and not
	// the media-style 25 because ASCII DXF is far bulkier than the equivalent PDF, and the
	// transport envelopes still fit — the decoded payload rides one gRPC message
	// (grpcMaxRecvMsgSize 50 MiB) and its base64 form ~53 MB stays under the 72 MiB admin
	// JSON body cap.
	maxPatternPayloadBytes = 40 * 1024 * 1024 // 40 MB
	// patternFolder segregates pattern files from image/video media in the bucket.
	patternFolder = "tech-card-patterns"
)

// ErrInvalidPattern marks a rejected pattern payload (empty, too large, or neither a PDF
// nor a DXF) so the API layer can map it to InvalidArgument rather than Internal (an S3
// failure).
var ErrInvalidPattern = errors.New("invalid pattern file")

// UploadPatternFile stores a raw cut pattern (выкройка) in object storage and returns
// its CDN url plus the stored byte size. The payload must be a real PDF or DXF (sniffed
// from the bytes, not the caller-declared type); the sniffed type picks the object
// extension (.pdf / .dxf), which is how readers learn the file type — there is no
// content-type column anywhere. Unlike images and videos it is NOT recorded in the media
// table — pattern files are kept out of the image library.
func (b *Bucket) UploadPatternFile(ctx context.Context, raw []byte, objectName string) (string, int64, error) {
	if len(raw) == 0 {
		return "", 0, fmt.Errorf("%w: payload is empty", ErrInvalidPattern)
	}
	if len(raw) > maxPatternPayloadBytes {
		return "", 0, fmt.Errorf("%w: payload too large: %d bytes, max %d bytes", ErrInvalidPattern, len(raw), maxPatternPayloadBytes)
	}
	var contentType ContentType
	switch {
	case isPDF(raw):
		contentType = contentTypePDF
	case isDXF(raw):
		contentType = contentTypeDXF
	default:
		return "", 0, fmt.Errorf("%w: payload is not a PDF or DXF", ErrInvalidPattern)
	}

	// Pattern files are internal production IP (выкройки) but are stored public-read
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

	ext, err := fileExtensionFromContentType(contentType)
	if err != nil {
		return "", 0, err
	}
	fp := b.constructFullPath(patternFolder, objectName, ext)

	r := bytes.NewReader(raw)
	_, err = b.Client.PutObject(ctx, b.S3BucketName, fp, r, int64(r.Len()),
		minio.PutObjectOptions{
			ContentType:  string(contentType),
			CacheControl: "max-age=31536000",
			UserMetadata: map[string]string{"x-amz-acl": "public-read"},
		})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload pattern file",
			slog.String("err", err.Error()))
		return "", 0, err
	}
	return b.getCDNURL(fp), int64(len(raw)), nil
}

// isPDF reports whether raw starts with the PDF magic header (%PDF-).
func isPDF(raw []byte) bool {
	return len(raw) >= 5 && string(raw[:5]) == "%PDF-"
}

// binaryDXFSentinel opens every binary-encoded DXF (AutoCAD's 22-byte magic).
var binaryDXFSentinel = []byte("AutoCAD Binary DXF\r\n\x1a\x00")

// isDXF reports whether raw looks like a DXF drawing. Binary DXF carries a fixed
// sentinel. ASCII DXF has no magic header — it is a sequence of group-code/value line
// pairs — so it is recognized by its mandatory opening: the first pair after an optional
// UTF-8 BOM and any leading 999-comment pairs must be group code 0 with value SECTION.
// Only the head of the payload is examined; anything past the opening pair is the
// drawing's own business. The window is 64 KB because real exporters (AccuMark, Optitex,
// Lectra) front the file with multi-line 999 provenance headers that can run past a few
// KB — the opening pair must merely fall inside the window, not at the top.
func isDXF(raw []byte) bool {
	if bytes.HasPrefix(raw, binaryDXFSentinel) {
		return true
	}
	head := raw
	if len(head) > 64*1024 {
		head = head[:64*1024]
	}
	head = bytes.TrimPrefix(head, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	lines := bytes.Split(head, []byte("\n"))
	expectComment := false
	for i := 0; i < len(lines); i++ {
		line := bytes.TrimSpace(lines[i])
		if expectComment {
			// The value line of a 999 pair — arbitrary text, skip it.
			expectComment = false
			continue
		}
		if len(line) == 0 {
			continue
		}
		switch string(line) {
		case "999":
			expectComment = true
		case "0":
			// First real group code — must open a SECTION.
			for j := i + 1; j < len(lines); j++ {
				value := bytes.TrimSpace(lines[j])
				if len(value) == 0 {
					continue
				}
				return string(value) == "SECTION"
			}
			return false
		default:
			return false
		}
	}
	return false
}
