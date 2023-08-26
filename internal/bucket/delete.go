package bucket

import (
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
)

// DeleteFromBucket deletes an object from the specified bucket.
func (b *Bucket) DeleteFromBucket(ctx context.Context, objectName string) error {
	err := b.Client.RemoveObject(ctx, b.Config.S3BucketName, objectName, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("error deleting object %s: %v", objectName, err)
	}
	return nil
}
