package bucket

import (
	"bytes"
	"context"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/minio/minio-go/v7"
	"golang.org/x/exp/slog"
)

func (b *Bucket) uploadVideoObj(ctx context.Context, mp4Data []byte, folder, objectName, contentType string) (*pb_common.Media, error) {

	userMetaData := map[string]string{"x-amz-acl": "public-read"}
	cacheControl := "max-age=31536000"

	ext, err := fileExtensionFromContentType(contentType)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get extension from content type",
			slog.String("err", err.Error()))
		return nil, err
	}
	fp := b.constructFullPath(folder, objectName, ext)

	r := bytes.NewReader(mp4Data)

	ui, err := b.Client.PutObject(ctx, b.S3BucketName, fp,
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

	return &pb_common.Media{
		FullSize:  url,
		ObjectIds: []string{ui.Key},
	}, nil
}
