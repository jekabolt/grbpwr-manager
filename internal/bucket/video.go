package bucket

import (
	"bytes"
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/minio/minio-go/v7"
	"golang.org/x/exp/slog"
)

func (b *Bucket) uploadVideoObj(ctx context.Context, mp4Data []byte, folder, objectName string, contentType string) (*pb_common.Media, error) {

	userMetaData := map[string]string{"x-amz-acl": "public-read"}
	cacheControl := "max-age=31536000"

	ext, err := fileExtensionFromContentType(ContentType(contentType))
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get extension from content type",
			slog.String("err", err.Error()))
		return nil, err
	}
	fp := b.constructFullPath(folder, objectName, ext)

	r := bytes.NewReader(mp4Data)

	_, err = b.Client.PutObject(ctx, b.S3BucketName, fp,
		r, int64(r.Len()),
		minio.PutObjectOptions{
			ContentType:     contentType,
			CacheControl:    cacheControl,
			UserMetadata:    userMetaData,
			ContentEncoding: "gzip",
		})
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't upload video object",
			slog.String("err", err.Error()))
		return nil, err
	}
	url := b.getCDNURL(fp)

	mediaId, err := b.ms.AddMedia(ctx, &entity.MediaInsert{
		FullSize:   url,
		Compressed: url,
		Thumbnail:  url,
	})
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't add media to db",
			slog.String("err", err.Error()))
		return nil, err
	}

	mi := &pb_common.MediaInsert{
		FullSize:   url,
		Compressed: url,
		Thumbnail:  url,
	}

	return &pb_common.Media{
		Id:    int32(mediaId),
		Media: mi,
	}, nil
}
