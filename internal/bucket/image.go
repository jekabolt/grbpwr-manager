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

// uploadRawImageObj stores an ANIMATED GIF: its bytes must survive verbatim, because the WebP
// re-encode path would flatten the animation to a single frame. The raw payload backs BOTH the
// full-size and the compressed variant (campaign emails read the compressed url, so the animation
// has to be there too); the list thumbnail is a re-encoded, resized STATIC first frame so the media
// library does not fetch a multi-MB animation per grid cell. Dimensions come from the GIF header.
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

	return b.uploadVerbatimImageObj(ctx, verbatimImage{
		raw:    raw,
		ct:     ct,
		width:  cfg.Width,
		height: cfg.Height,
		frame:  firstFrame,
		// One object under both urls: the point of this path is that the animation reaches
		// the compressed url too.
		compressedIsFullSize: true,
	}, folder, imageName)
}

// UploadContentImageVerbatim stores a picture whose FULL-SIZE OBJECT MUST BE THE BYTES IT WAS
// HANDED, unchanged, and derives the smaller variants from them.
//
// WHY THIS EXISTS AT ALL — the tech-card archive.
//
// media.content_hash is the sha256 of the full-size object AS STORED (uploadImageToBucket), and the
// archive export puts the sha of that very object into the archive. De-duplication on import is the
// comparison of those two numbers. UploadContentImage, however, re-encodes every JPEG/PNG/WebP into
// a fresh full-size WebP, so the hash it records belongs to bytes that did not exist before the
// upload: an archive re-imported into the same base can never match, every picture is stored again,
// and each generation adds another lossy re-encode on top of the previous one. Backup/restore and
// beta↔prod transfer are exactly the scenarios that re-import the same archive.
//
// So the full-size object here is the payload itself. The invariant "content_hash is the sha of the
// full-size bytes as they lie in the bucket" is UNCHANGED — what changes is which bytes lie there.
//
// The budgets of the WebP path are NOT relaxed: verbatim is not a reason to let a decompression
// bomb through. Byte ceiling (the decoded equivalent of maxImagePayloadBytes), pixel ceiling and
// dimension ceiling are all read from the HEADER, before anything is decoded or uploaded.
//
// An animated GIF routes into the pass-through path above, which already had this property.
// HEIC — decodable but not servable, and outside the archive's media list — is refused here rather
// than silently re-encoded: a caller that wants a re-encode has UploadContentImage.
func (b *Bucket) UploadContentImageVerbatim(ctx context.Context, raw []byte, folder, imageName string) (*pb_common.MediaFull, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("image payload is empty")
	}
	if len(raw) > maxRawImagePayloadBytes {
		return nil, fmt.Errorf("image payload too large: %d bytes, max %d bytes", len(raw), maxRawImagePayloadBytes)
	}

	ct := sniffImageType(raw)
	switch ct {
	case contentTypeGIF:
		return b.uploadRawImageObj(ctx, raw, contentTypeGIF, folder, imageName)
	case contentTypeJPEG, contentTypePNG, contentTypeWEBP:
	case "":
		return nil, fmt.Errorf("unrecognized image format; verbatim upload stores JPEG, PNG, WebP and GIF")
	default:
		return nil, fmt.Errorf("unsupported image format %q for verbatim upload; stores JPEG, PNG, WebP and GIF", ct)
	}

	// Header first, raster second. Both ceilings are decided from a few dozen bytes, so a payload
	// that declares a huge canvas is refused without ever allocating it.
	cfg, err := imageHeaderConfig(ct, raw)
	if err != nil {
		return nil, fmt.Errorf("can't read image header: %w", err)
	}
	if cfg.Width > maxImageDimension || cfg.Height > maxImageDimension {
		return nil, fmt.Errorf("image dimensions %dx%d exceed maximum allowed %dpx", cfg.Width, cfg.Height, maxImageDimension)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, fmt.Errorf("image too large: %dx%d exceeds %d-pixel limit", cfg.Width, cfg.Height, maxImagePixels)
	}

	// Decoded ONLY to derive the compressed variant, the thumbnail and the blurhash — never to
	// produce the full-size object. If the payload will not decode we refuse it rather than store
	// bytes nothing can render.
	img, err := decodeImage(raw, ct)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	return b.uploadVerbatimImageObj(ctx, verbatimImage{
		raw:    raw,
		ct:     ct,
		width:  cfg.Width,
		height: cfg.Height,
		frame:  img,
	}, folder, imageName)
}

// verbatimImage is a payload whose full-size object must land in the bucket byte-for-byte, plus
// everything the derived variants need.
type verbatimImage struct {
	// raw is the object: it is uploaded as it stands, and its sha becomes content_hash.
	raw []byte
	// ct decides the stored object's content-type and extension — it is the SNIFFED type, never
	// a caller's label.
	ct ContentType
	// width and height come from the payload's header and describe the full-size object.
	width, height int
	// frame is the raster the compressed variant, the thumbnail and the blurhash are derived
	// from: the FIRST frame of an animated GIF, the picture itself for a still.
	frame image.Image
	// compressedIsFullSize points the compressed url at the SAME verbatim object instead of a
	// re-encoded WebP. True only for an animated GIF (see uploadRawImageObj); a still gets a
	// small WebP there, exactly as on the re-encoding path.
	compressedIsFullSize bool
}

// uploadVerbatimImageObj puts v.raw under the full-size url untouched, builds the smaller variants
// from v.frame, and records the row. The hash it stores is taken from the UPLOAD (uploadImageToBucket
// hashes the very slice handed to PutObject), not from v.raw directly, so this path keeps agreeing
// with the re-encoding one about what content_hash means.
func (b *Bucket) uploadVerbatimImageObj(ctx context.Context, v verbatimImage, folder, imageName string) (*pb_common.MediaFull, error) {
	rawURL, rawSHA, err := b.uploadImageToBucket(ctx, bytes.NewReader(v.raw), folder,
		fmt.Sprintf("%s-%s", imageName, "og"), v.ct)
	if err != nil {
		return nil, fmt.Errorf("failed to upload full-size image to bucket: %w", err)
	}
	full := &pb_common.MediaInfo{MediaUrl: rawURL, Width: int32(v.width), Height: int32(v.height)}

	compressed := full
	if !v.compressedIsFullSize {
		compressed, _, err = b.uploadSingleImage(ctx, v.frame, 60, folder,
			fmt.Sprintf("%s-%s", imageName, "compressed"))
		if err != nil {
			b.cleanupUploadedVariants(full)
			return nil, fmt.Errorf("failed to upload compressed variant: %w", err)
		}
	}

	thumbImg := resizeImage(v.frame, 1080)
	thumbnail, _, err := b.uploadSingleImage(ctx, thumbImg, 90, folder, fmt.Sprintf("%s-%s", imageName, "thumb"))
	if err != nil {
		b.cleanupUploadedVariants(full, compressed)
		return nil, fmt.Errorf("failed to upload thumbnail: %w", err)
	}

	h, err := getBlurHash(thumbImg)
	if err != nil {
		// The blurhash is a decorative placeholder; a picture whose frame will not blur-hash
		// should still upload. Store an empty hash rather than failing.
		slog.Default().WarnContext(ctx, "verbatim image blurhash failed; storing empty",
			slog.String("err", err.Error()))
		h = ""
	}

	mediaId, err := b.ms.AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL:   full.MediaUrl,
		FullSizeWidth:      int(full.Width),
		FullSizeHeight:     int(full.Height),
		CompressedMediaURL: compressed.MediaUrl,
		CompressedWidth:    int(compressed.Width),
		CompressedHeight:   int(compressed.Height),
		ThumbnailMediaURL:  thumbnail.MediaUrl,
		ThumbnailWidth:     int(thumbnail.Width),
		ThumbnailHeight:    int(thumbnail.Height),
		BlurHash:           sql.NullString{String: h, Valid: h != ""},
		// The hash describes the FULL-SIZE object — the one the archive export downloads.
		ContentHash: sql.NullString{String: rawSHA, Valid: rawSHA != ""},
	})
	if err != nil {
		// The objects are in S3 but no row references them: clean them up.
		b.cleanupUploadedVariants(full, compressed, thumbnail)
		return nil, fmt.Errorf("failed to add media to db: %w", err)
	}

	return &pb_common.MediaFull{
		Id: int32(mediaId),
		Media: &pb_common.MediaItem{
			FullSize:   full,
			Compressed: compressed,
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
