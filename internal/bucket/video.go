package bucket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/minio/minio-go/v7"
)

var allowedVideoTypes = map[ContentType]bool{
	contentTypeMP4:  true,
	contentTypeWEBM: true,
}

func (b *Bucket) uploadVideoObj(ctx context.Context, mp4Data []byte, folder, objectName string, contentType string) (*pb_common.MediaFull, error) {
	ct := ContentType(contentType)
	if !allowedVideoTypes[ct] {
		return nil, fmt.Errorf("unsupported video content type: %s, allowed: video/mp4, video/webm", contentType)
	}
	// Validate the actual bytes against the declared type instead of trusting the
	// client, matching the image (sniffImageType) and pattern (isPDF) paths.
	if sniffed := sniffVideoType(mp4Data); sniffed != ct {
		return nil, fmt.Errorf("video bytes do not match declared content type %s", ct)
	}

	userMetaData := map[string]string{"x-amz-acl": "public-read"}
	cacheControl := "max-age=31536000"

	ext, err := fileExtensionFromContentType(ct)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get extension from content type",
			slog.String("err", err.Error()))
		return nil, err
	}
	fp := b.constructFullPath(folder, objectName, ext)

	r := bytes.NewReader(mp4Data)

	_, err = b.Client.PutObject(ctx, b.S3BucketName, fp,
		r, int64(r.Len()),
		minio.PutObjectOptions{
			ContentType:  contentType,
			CacheControl: cacheControl,
			UserMetadata: userMetaData,
		})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't upload video object",
			slog.String("err", err.Error()))
		return nil, err
	}
	url := b.getCDNURL(fp)

	// Fingerprint of the stored object. Video is uploaded verbatim, so the bytes handed to
	// PutObject above ARE the object, and all three urls point at it — one hash covers the
	// row. Taken after the upload succeeded so a row never claims a hash for an object that
	// is not in the bucket.
	sum := sha256.Sum256(mp4Data)
	contentHash := hex.EncodeToString(sum[:])

	mediaId, err := b.ms.AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL:   url,
		CompressedMediaURL: url,
		ThumbnailMediaURL:  url,
		ContentHash:        sql.NullString{String: contentHash, Valid: true},
	})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add media to db",
			slog.String("err", err.Error()))
		return nil, err
	}

	mInfo := &pb_common.MediaInfo{
		MediaUrl: url,
	}

	mi := &pb_common.MediaItem{
		FullSize:   mInfo,
		Compressed: mInfo,
		Thumbnail:  mInfo,
	}

	return &pb_common.MediaFull{
		Id:    int32(mediaId),
		Media: mi,
	}, nil
}
