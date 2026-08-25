package bucket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/gif"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/bbrks/go-blurhash"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/minio/minio-go/v7"
	"golang.org/x/image/draw"
	"golang.org/x/sync/errgroup"
)

type B64Image struct {
	content     []byte
	contentType ContentType
}

// uploadImageToBucket stores img and returns the CDN url plus the hex SHA-256 of the bytes
// that were actually stored.
//
// The hash is taken from `data` — the very slice handed to PutObject — and not from
// whatever the caller decoded or re-encoded upstream. That is the whole point: the archive
// export downloads this object and puts its sha in the archive, so a hash of any other
// representation of the same picture would never match and de-duplication would silently
// never fire.
func (b *Bucket) uploadImageToBucket(ctx context.Context, img io.Reader, folder, imageName string, contentType ContentType) (string, string, error) {
	ext, err := fileExtensionFromContentType(contentType)
	if err != nil {
		return "", "", fmt.Errorf("can't get file extension")
	}
	fp := b.constructFullPath(folder, imageName, ext)

	data, err := io.ReadAll(img)
	if err != nil {
		return "", "", err
	}

	r := bytes.NewReader(data)
	userMetaData := map[string]string{"x-amz-acl": "public-read"}
	cacheControl := "max-age=31536000"

	_, err = b.Client.PutObject(ctx, b.Config.S3BucketName, fp, r,
		int64(r.Len()), minio.PutObjectOptions{
			ContentType:  contentType.String(),
			CacheControl: cacheControl,
			UserMetadata: userMetaData,
		},
	)
	if err != nil {
		return "", "", fmt.Errorf("error putting object: %v", err)
	}

	sum := sha256.Sum256(data)
	return b.getCDNURL(fp), hex.EncodeToString(sum[:]), nil
}

// getB64ImageFromString extracts the content type and the byte content from a raw base64 image string.
// The expected format of the raw base64 string is "data:[<mediatype>];base64,[<base64-data>]".
func getB64ImageFromString(rawB64Image string) (*B64Image, error) {
	const base64Prefix = ";base64,"
	parts := strings.Split(rawB64Image, base64Prefix)

	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid base64 image format: expected 'data:[mediatype];base64,[data]'")
	}

	imgContentType := strings.Split(parts[0], ":")
	if len(imgContentType) != 2 {
		return nil, fmt.Errorf("invalid base64 image format: expected 'data:[mediatype];base64,[data]'")
	}

	return &B64Image{
		contentType: ContentType(imgContentType[1]),
		content:     []byte(parts[1]),
	}, nil
}

// rawImageFromString parses a "data:[mediatype];base64,[data]" string into its raw
// (base64-decoded) bytes plus the declared content type, without decoding the raster.
// Callers that must preserve the original bytes (animated GIF) use this; the WebP
// re-encode path decodes further via decodeImage.
func rawImageFromString(rawB64Image string) ([]byte, ContentType, error) {
	b64Img, err := getB64ImageFromString(rawB64Image)
	if err != nil {
		return nil, "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b64Img.content)))
	if err != nil {
		return nil, "", fmt.Errorf("invalid base64 payload: %w", err)
	}
	return raw, b64Img.contentType, nil
}

func imageFromString(rawB64Image string) (image.Image, error) {
	raw, ct, err := rawImageFromString(rawB64Image)
	if err != nil {
		return nil, err
	}
	return decodeImage(raw, ct)
}

// uploadSingleImage encodes img to WebP at the given quality, uploads it, and returns the
// variant descriptor plus the hex SHA-256 of the encoded bytes that were stored. Only the
// full-size variant's hash is ever persisted (see uploadImageObj); the others are returned
// so no call site has to guess which string belongs to which object.
func (b *Bucket) uploadSingleImage(ctx context.Context, img image.Image, quality int, folder, imageName string) (*pb_common.MediaInfo, string, error) {
	var buf bytes.Buffer

	if err := encodeWEBP(&buf, img, quality); err != nil {
		return nil, "", fmt.Errorf("failed to encode WebP: %v", err)
	}

	url, sha, err := b.uploadImageToBucket(ctx, &buf, folder, imageName, contentTypeWEBP)
	if err != nil {
		return nil, "", fmt.Errorf("failed to upload image to bucket: %v", err)
	}

	return &pb_common.MediaInfo{
		MediaUrl: url,
		Width:    int32(img.Bounds().Dx()),
		Height:   int32(img.Bounds().Dy()),
	}, sha, nil
}

// uploadImageObj composes 3 image variants (full-size, compressed, thumbnail) in parallel via errgroup,
// then computes blurhash from the thumbnail and records the result in the media DB table.
func (b *Bucket) uploadImageObj(ctx context.Context, img image.Image, folder, imageName string) (*pb_common.MediaFull, error) {
	fullSizeName := fmt.Sprintf("%s-%s", imageName, "og")
	compressedName := fmt.Sprintf("%s-%s", imageName, "compressed")
	thumbnailName := fmt.Sprintf("%s-%s", imageName, "thumb")

	thumbImg := resizeImage(img, 1080)

	var (
		mu                   sync.Mutex
		fullSize, compressed *pb_common.MediaInfo
		thumbnail            *pb_common.MediaInfo
		// Hash of the FULL-SIZE variant only: that is the object the archive export
		// downloads, so it is the only one an incoming archive can be compared against.
		// Written under the same mutex as the descriptors it belongs to.
		fullSizeSHA string
	)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		info, sha, err := b.uploadSingleImage(gctx, img, 100, folder, fullSizeName)
		if err != nil {
			return fmt.Errorf("full-size: %w", err)
		}
		mu.Lock()
		fullSize, fullSizeSHA = info, sha
		mu.Unlock()
		return nil
	})

	g.Go(func() error {
		info, _, err := b.uploadSingleImage(gctx, img, 60, folder, compressedName)
		if err != nil {
			return fmt.Errorf("compressed: %w", err)
		}
		mu.Lock()
		compressed = info
		mu.Unlock()
		return nil
	})

	g.Go(func() error {
		info, _, err := b.uploadSingleImage(gctx, thumbImg, 90, folder, thumbnailName)
		if err != nil {
			return fmt.Errorf("thumbnail: %w", err)
		}
		mu.Lock()
		thumbnail = info
		mu.Unlock()
		return nil
	})

	if err := g.Wait(); err != nil {
		// One or two variants may have uploaded before another failed; remove them so
		// the partial upload doesn't orphan S3 objects with no DB row referencing them.
		b.cleanupUploadedVariants(fullSize, compressed, thumbnail)
		return nil, fmt.Errorf("failed to upload image variants: %w", err)
	}

	h, err := getBlurHash(thumbImg)
	if err != nil {
		b.cleanupUploadedVariants(fullSize, compressed, thumbnail)
		return nil, fmt.Errorf("failed to get blurhash: %v", err)
	}

	mediaId, err := b.ms.AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL:   fullSize.MediaUrl,
		FullSizeWidth:      int(fullSize.Width),
		FullSizeHeight:     int(fullSize.Height),
		CompressedMediaURL: compressed.MediaUrl,
		CompressedWidth:    int(compressed.Width),
		CompressedHeight:   int(compressed.Height),
		ThumbnailMediaURL:  thumbnail.MediaUrl,
		ThumbnailWidth:     int(thumbnail.Width),
		ThumbnailHeight:    int(thumbnail.Height),
		BlurHash:           sql.NullString{String: h, Valid: true},
		ContentHash:        sql.NullString{String: fullSizeSHA, Valid: fullSizeSHA != ""},
	})
	if err != nil {
		// All three objects are in S3 but no row references them: clean them up.
		b.cleanupUploadedVariants(fullSize, compressed, thumbnail)
		return nil, fmt.Errorf("failed to add media to db: %v", err)
	}

	return &pb_common.MediaFull{
		Id: int32(mediaId),
		Media: &pb_common.MediaItem{
			FullSize:   fullSize,
			Compressed: compressed,
			Thumbnail:  thumbnail,
			Blurhash:   h,
		},
	}, nil
}

// uploadRawImageObj stores an image whose bytes must survive verbatim — an animated GIF —
// instead of re-encoding it to WebP, which would flatten the animation to a single frame.
// The raw payload is uploaded once and used for BOTH the full-size and compressed variants
// (campaign emails read the compressed URL, so the animation is preserved there). The list
// thumbnail is a re-encoded, resized STATIC first frame so the media library doesn't fetch a
// multi-MB animation per grid cell. Dimensions come from the GIF header; the blurhash is
// derived from that first frame and is best-effort.
func (b *Bucket) uploadRawImageObj(ctx context.Context, raw []byte, ct ContentType, folder, imageName string) (*pb_common.MediaFull, error) {
	cfg, err := gif.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to read GIF header: %w", err)
	}
	if cfg.Width > maxImageDimension || cfg.Height > maxImageDimension {
		return nil, fmt.Errorf("image dimensions %dx%d exceed maximum allowed %dpx", cfg.Width, cfg.Height, maxImageDimension)
	}
	// Enforce the same decompression-bomb budget as the WebP path (checkImagePixelBudget):
	// a small, highly-compressed GIF can declare a huge canvas and force gif.Decode to
	// allocate a first-frame buffer of Width×Height bytes.
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, fmt.Errorf("image too large: %dx%d exceeds %d-pixel limit", cfg.Width, cfg.Height, maxImagePixels)
	}

	firstFrame, err := gif.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to decode GIF: %w", err)
	}

	// rawSHA fingerprints the object under the full-size url. Here the stored bytes happen
	// to equal the posted bytes (the whole point of this path is that the GIF is not
	// re-encoded), but the hash is still taken from the upload, not from `raw` directly, so
	// that this path keeps agreeing with the WebP one if that ever stops being true.
	rawURL, rawSHA, err := b.uploadImageToBucket(ctx, bytes.NewReader(raw), folder, fmt.Sprintf("%s-%s", imageName, "og"), ct)
	if err != nil {
		return nil, fmt.Errorf("failed to upload GIF to bucket: %w", err)
	}
	full := &pb_common.MediaInfo{MediaUrl: rawURL, Width: int32(cfg.Width), Height: int32(cfg.Height)}

	thumbImg := resizeImage(firstFrame, 1080)
	thumbnail, _, err := b.uploadSingleImage(ctx, thumbImg, 90, folder, fmt.Sprintf("%s-%s", imageName, "thumb"))
	if err != nil {
		b.cleanupUploadedVariants(full)
		return nil, fmt.Errorf("failed to upload GIF thumbnail: %w", err)
	}

	h, err := getBlurHash(thumbImg)
	if err != nil {
		// The blurhash is a decorative placeholder; a GIF whose first frame won't
		// blur-hash should still upload. Store an empty hash rather than failing.
		slog.Default().WarnContext(ctx, "gif blurhash failed; storing empty",
			slog.String("err", err.Error()))
		h = ""
	}

	mediaId, err := b.ms.AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL:   rawURL,
		FullSizeWidth:      cfg.Width,
		FullSizeHeight:     cfg.Height,
		CompressedMediaURL: rawURL,
		CompressedWidth:    cfg.Width,
		CompressedHeight:   cfg.Height,
		ThumbnailMediaURL:  thumbnail.MediaUrl,
		ThumbnailWidth:     int(thumbnail.Width),
		ThumbnailHeight:    int(thumbnail.Height),
		BlurHash:           sql.NullString{String: h, Valid: h != ""},
		// Full-size and compressed are the SAME object here, so one hash describes both.
		ContentHash: sql.NullString{String: rawSHA, Valid: rawSHA != ""},
	})
	if err != nil {
		// The objects are in S3 but no row references them: clean them up.
		b.cleanupUploadedVariants(full, thumbnail)
		return nil, fmt.Errorf("failed to add media to db: %w", err)
	}

	return &pb_common.MediaFull{
		Id: int32(mediaId),
		Media: &pb_common.MediaItem{
			FullSize:   full,
			Compressed: full,
			Thumbnail:  thumbnail,
			Blurhash:   h,
		},
	}, nil
}

// cleanupUploadedVariants best-effort removes any variant objects that were
// already uploaded when a later step (a sibling upload, blurhash, or the DB
// insert) fails, so a partial upload does not orphan S3 objects. It uses a fresh
// context because the errgroup context may already be cancelled.
func (b *Bucket) cleanupUploadedVariants(variants ...*pb_common.MediaInfo) {
	urls := make([]string, 0, len(variants))
	for _, v := range variants {
		if v != nil && v.MediaUrl != "" {
			urls = append(urls, v.MediaUrl)
		}
	}
	if len(urls) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := b.DeleteObjects(ctx, urls...); err != nil {
		slog.Default().ErrorContext(ctx, "failed to clean up orphaned image variants after upload failure",
			slog.String("err", err.Error()))
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func getBlurHash(img image.Image) (string, error) {
	width := img.Bounds().Dx()
	height := img.Bounds().Dy()

	baseComponent := 4

	aspectRatio := float64(width) / float64(height)
	componentsX := int(math.Round(float64(baseComponent) * aspectRatio))
	componentsY := int(math.Round(float64(baseComponent) / aspectRatio))

	componentsX = clamp(componentsX, 1, 9)
	componentsY = clamp(componentsY, 1, 9)

	hash, err := blurhash.Encode(componentsX, componentsY, toGrayscale(img))
	if err != nil {
		return "", fmt.Errorf("failed to encode image to BlurHash: %v", err)
	}
	return hash, nil
}

// toGrayscale returns a monochrome copy of img (standard Rec. 601 luma), so the
// resulting blurhash is black-and-white rather than colored.
func toGrayscale(img image.Image) image.Image {
	gray := image.NewGray(img.Bounds())
	draw.Draw(gray, gray.Bounds(), img, img.Bounds().Min, draw.Src)
	return gray
}

// resizeImage resizes img so that its height is at most maxHeight px, preserving aspect ratio.
// Returns the original if no resizing is needed.
func resizeImage(img image.Image, maxHeight int) image.Image {
	bounds := img.Bounds()
	if bounds.Dy() <= maxHeight {
		return img
	}

	newWidth := maxHeight * bounds.Dx() / bounds.Dy()
	newImg := image.NewRGBA(image.Rect(0, 0, newWidth, maxHeight))
	draw.ApproxBiLinear.Scale(newImg, newImg.Bounds(), img, bounds, draw.Over, nil)
	return newImg
}
