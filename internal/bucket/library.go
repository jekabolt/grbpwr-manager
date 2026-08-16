package bucket

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"

	"github.com/minio/minio-go/v7"
)

const (
	// libraryFolder segregates files-library objects from image/video media,
	// patterns and shipping labels. It is also the segment PresignLibraryObject
	// requires, so it is what makes a key "ours" for signing purposes.
	libraryFolder = "files-library"
	// libraryPreviewFolder holds the small browser-rendered preview images. It
	// sits UNDER libraryFolder so a single segment check covers both.
	libraryPreviewFolder = libraryFolder + "/previews"
	// libraryUploadPartSize bounds memory: minio buffers one part at a time when
	// the total size is unknown (streamed upload), so peak use is roughly twice
	// this per concurrent upload rather than the whole file.
	libraryUploadPartSize = 16 * 1024 * 1024
	// maxLibraryPreviewBytes caps the client-rendered preview image. A preview is
	// a thumbnail of one page; anything larger is a mistake or an abuse.
	maxLibraryPreviewBytes = 2 * 1024 * 1024
	// libraryFallbackExt names an object whose filename carried no usable
	// extension. Without it constructFullPath would produce a trailing-dot key.
	libraryFallbackExt = "bin"
)

// ErrInvalidLibraryUpload marks a rejected library payload so the API layer can
// answer InvalidArgument rather than Internal (which is what an S3 failure is).
var ErrInvalidLibraryUpload = errors.New("invalid library upload")

// safeExtRe is the whole allowlist for an object-key extension. The extension
// here comes from a CLIENT-SUPPLIED filename — unlike patterns, whose type is
// sniffed from the bytes — and a library accepts arbitrary types, so there is
// nothing to sniff against. Rather than trust it, we reduce it to something that
// cannot surprise an S3 key or a URL: lowercase alphanumerics, at most ten.
var safeExtRe = regexp.MustCompile(`^[a-z0-9]{1,10}$`)

// sanitizeExt reduces a filename extension to a key-safe token, returning the
// fallback when nothing usable survives.
func sanitizeExt(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
	if !safeExtRe.MatchString(ext) {
		return libraryFallbackExt
	}
	return ext
}

// libraryObjectName builds an unguessable object name. Library objects are
// private and only ever reached through a presigned url, but the key still gets
// 128 bits of entropy so that possession of one url never suggests another key —
// the same reasoning as pattern objects.
func libraryObjectName() (string, error) {
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("can't generate library object name: %w", err)
	}
	return GetMediaName() + "-" + hex.EncodeToString(suffix), nil
}

// UploadLibraryObject streams r into a PRIVATE object and returns its key, the
// hex sha256 computed from the very bytes that were stored, and the byte count.
//
// Privacy is the ABSENCE of the `x-amz-acl: public-read` metadata that the
// image, video, pattern and label paths all set. That asymmetry is the whole
// point of this bucket folder: guidelines, mockups and internal documents must
// never become publicly addressable, so they are reachable only through a
// short-lived presigned GET.
//
// The size is not known ahead of time (the body is a multipart part being read
// as it arrives), so PutObject is given -1 and a fixed PartSize; minio then
// buffers one part at a time instead of the whole payload.
func (b *Bucket) UploadLibraryObject(ctx context.Context, r io.Reader, contentType, ext string) (string, string, int64, error) {
	if r == nil {
		return "", "", 0, fmt.Errorf("%w: no payload", ErrInvalidLibraryUpload)
	}
	name, err := libraryObjectName()
	if err != nil {
		return "", "", 0, err
	}
	key := b.constructFullPath(libraryFolder, name, sanitizeExt(ext))

	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	// TeeReader hashes exactly what is uploaded: the hash cannot describe bytes
	// other than the ones that reached the bucket, which is what makes it usable
	// as a duplicate key later.
	hasher := sha256.New()
	info, err := b.Client.PutObject(ctx, b.S3BucketName, key, io.TeeReader(r, hasher), -1,
		minio.PutObjectOptions{
			ContentType: contentType,
			// Short private cache only: the object is served through presigned urls
			// whose window is 6-12h, so a year-long immutable cache (what the public
			// media paths use) would outlive every url that can reach it.
			CacheControl: "private, max-age=21600",
			PartSize:     libraryUploadPartSize,
		})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload library object",
			slog.String("key", key), slog.String("err", err.Error()))
		return "", "", 0, fmt.Errorf("upload library object: %w", err)
	}
	return key, hex.EncodeToString(hasher.Sum(nil)), info.Size, nil
}

// UploadLibraryPreview stores the browser-rendered preview image for a library
// file. Only PNG and WebP are accepted, and the format is sniffed from the bytes
// rather than trusted from the caller — this is the one library object that gets
// served inline, so what it actually is has to be established, not declared.
func (b *Bucket) UploadLibraryPreview(ctx context.Context, raw []byte, ext string) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("%w: preview is empty", ErrInvalidLibraryUpload)
	}
	if len(raw) > maxLibraryPreviewBytes {
		return "", fmt.Errorf("%w: preview too large: %d bytes, max %d",
			ErrInvalidLibraryUpload, len(raw), maxLibraryPreviewBytes)
	}
	ct := sniffImageType(raw)
	if ct != contentTypePNG && ct != contentTypeWEBP {
		return "", fmt.Errorf("%w: preview is not a PNG or WebP", ErrInvalidLibraryUpload)
	}
	previewExt, err := fileExtensionFromContentType(ct)
	if err != nil {
		return "", err
	}
	name, err := libraryObjectName()
	if err != nil {
		return "", err
	}
	key := b.constructFullPath(libraryPreviewFolder, name, previewExt)

	r := bytes.NewReader(raw)
	if _, err := b.Client.PutObject(ctx, b.S3BucketName, key, r, int64(r.Len()),
		minio.PutObjectOptions{
			ContentType:  string(ct),
			CacheControl: "private, max-age=21600",
		}); err != nil {
		slog.Default().ErrorContext(ctx, "can't upload library preview",
			slog.String("key", key), slog.String("err", err.Error()))
		return "", fmt.Errorf("upload library preview: %w", err)
	}
	return key, nil
}

// RemoveObjectsByKeys best-effort deletes objects addressed by key. Library
// files store keys rather than urls — a private object has no durable url to
// store — so DeleteObjects (which parses urls) does not apply to them. Every key
// is attempted and the first error is returned, so one transient failure does
// not skip the rest.
func (b *Bucket) RemoveObjectsByKeys(ctx context.Context, keys ...string) error {
	var firstErr error
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.Trim(strings.TrimSpace(key), "/")
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if err := b.Client.RemoveObject(ctx, b.S3BucketName, key, minio.RemoveObjectOptions{}); err != nil {
			slog.Default().ErrorContext(ctx, "can't remove object by key",
				slog.String("key", key), slog.String("err", err.Error()))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
