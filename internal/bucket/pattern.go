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

// dxfHeadWindow bounds how far past the comment prologue the opening 0/SECTION pair
// (plus any blank lines before it) may sit. A DXF with no leading 999 comments must
// open within the first 64 KB of the payload, exactly as before the prologue allowance.
const dxfHeadWindow = 64 * 1024

// dxfMaxCommentPrologue caps the leading 999-comment prologue at 4 MiB. Our own
// importer writes a conversion manifest there as base64 999 chunks — 70–110 KB for
// 90–140-block patterns — so 4 MiB leaves ~40x headroom while still refusing a
// payload that is nothing but comments without walking the whole upload.
const dxfMaxCommentPrologue = 4 << 20

// isDXF reports whether raw looks like a DXF drawing. Binary DXF carries a fixed
// sentinel. ASCII DXF has no magic header — it is a sequence of group-code/value line
// pairs — so it is recognized by its mandatory opening: the first pair after an optional
// UTF-8 BOM and any leading 999-comment pairs must be group code 0 with value SECTION.
// Only the head of the payload is examined; anything past the opening pair is the
// drawing's own business.
//
// The head is walked line by line with an index (no split of a multi-MB payload). The
// window starts at dxfHeadWindow bytes; every complete 999 pair slides it so that it
// ends dxfHeadWindow bytes past that pair. Real exporters (AccuMark, Optitex, Lectra)
// and our importer's manifest front the file with 999 headers that can run far past
// 64 KB, so the prologue may span up to dxfMaxCommentPrologue bytes; a longer one is
// rejected. Lines end in \n; a trailing \r and surrounding whitespace are trimmed.
func isDXF(raw []byte) bool {
	if bytes.HasPrefix(raw, binaryDXFSentinel) {
		return true
	}
	// nextLine returns the line starting at p, cut at end, the offset after its
	// newline (or end), and whether a newline terminated it before end.
	nextLine := func(p, end int) ([]byte, int, bool) {
		i := bytes.IndexByte(raw[p:end], '\n')
		if i < 0 {
			return raw[p:end], end, false
		}
		return raw[p : p+i], p + i + 1, true
	}
	limit := min(len(raw), dxfHeadWindow)
	pos := 0
	if bytes.HasPrefix(raw[:limit], []byte{0xEF, 0xBB, 0xBF}) { // UTF-8 BOM
		pos = 3
	}
	for pos < limit {
		line, after, _ := nextLine(pos, limit)
		line = bytes.TrimSpace(line)
		pos = after
		if len(line) == 0 {
			continue
		}
		switch string(line) {
		case "999":
			// The value line of a 999 pair — arbitrary text, skip it. It may run past
			// the current window but not past the prologue cap.
			if pos >= dxfMaxCommentPrologue {
				return false
			}
			_, after, ok := nextLine(pos, min(len(raw), dxfMaxCommentPrologue))
			if !ok {
				// EOF inside the comment, or the prologue overruns the cap.
				return false
			}
			pos = after
			limit = min(len(raw), pos+dxfHeadWindow)
		case "0":
			// First real group code — must open a SECTION.
			for pos < limit {
				value, after, _ := nextLine(pos, limit)
				value = bytes.TrimSpace(value)
				pos = after
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
