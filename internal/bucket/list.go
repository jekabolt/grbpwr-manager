package bucket

import (
	"context"
	"path/filepath"
	"strings"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/minio/minio-go/v7"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ListObjectsByPage lists objects in a S3 bucket by page
func (b *Bucket) ListObjects(ctx context.Context) ([]*pb_common.ListEntityMedia, error) {

	objectCh := b.Client.ListObjects(context.Background(), b.S3BucketName, minio.ListObjectsOptions{
		Prefix:    b.BaseFolder,
		Recursive: true,
	})

	allObjects := []*pb_common.ListEntityMedia{}
	for o := range objectCh {
		if o.Err != nil {
			return nil, o.Err
		}

		// Get the file extension
		ext := filepath.Ext(o.Key)

		// Check if the file name ends with "-og.jpg"
		isOgJpg := strings.HasSuffix(strings.ToLower(o.Key), "-og.jpg")

		// Filter based on extension
		if isOgJpg || ext == ".mp4" || ext == ".webm" {
			allObjects = append(allObjects, &pb_common.ListEntityMedia{
				Url:          b.getCDNURL(o.Key),
				LastModified: timestamppb.New(o.LastModified),
			})
		}
	}
	return allObjects, nil
}
