package bucket

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"image"
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

// upload image to bucket return url
func (b *Bucket) uploadImageToBucket(ctx context.Context, img io.Reader, folder, imageName string, contentType ContentType) (string, error) {
	ext, err := fileExtensionFromContentType(contentType)
	if err != nil {
		return "", fmt.Errorf("can't get file extension")
	}
	fp := b.constructFullPath(folder, imageName, ext)

	data, err := io.ReadAll(img)
	if err != nil {
		return "", err
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
		return "", fmt.Errorf("error putting object: %v", err)
	}

	return b.getCDNURL(fp), nil
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

func imageFromString(rawB64Image string) (image.Image, error) {
	b64Img, err := getB64ImageFromString(rawB64Image)
	if err != nil {
		return nil, err
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b64Img.content)))
	if err != nil {
		return nil, fmt.Errorf("invalid base64 payload: %w", err)
	}

	return decodeImage(raw, b64Img.contentType)
}

// upload single image with defined quality and prefix to bucket
func (b *Bucket) uploadSingleImage(ctx context.Context, img image.Image, quality int, folder, imageName string) (*pb_common.MediaInfo, error) {
	var buf bytes.Buffer

	if err := encodeWEBP(&buf, img, quality); err != nil {
		return nil, fmt.Errorf("failed to encode WebP: %v", err)
	}

	url, err := b.uploadImageToBucket(ctx, &buf, folder, imageName, contentTypeWEBP)
	if err != nil {
		return nil, fmt.Errorf("failed to upload image to bucket: %v", err)
	}

	return &pb_common.MediaInfo{
		MediaUrl: url,
		Width:    int32(img.Bounds().Dx()),
		Height:   int32(img.Bounds().Dy()),
	}, nil
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
	)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		info, err := b.uploadSingleImage(gctx, img, 100, folder, fullSizeName)
		if err != nil {
			return fmt.Errorf("full-size: %w", err)
		}
		mu.Lock()
		fullSize = info
		mu.Unlock()
		return nil
	})

	g.Go(func() error {
		info, err := b.uploadSingleImage(gctx, img, 60, folder, compressedName)
		if err != nil {
			return fmt.Errorf("compressed: %w", err)
		}
		mu.Lock()
		compressed = info
		mu.Unlock()
		return nil
	})

	g.Go(func() error {
		info, err := b.uploadSingleImage(gctx, thumbImg, 90, folder, thumbnailName)
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
