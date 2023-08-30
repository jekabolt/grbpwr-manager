package bucket

import (
	"context"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
	"golang.org/x/exp/slog"
)

// toRemoveCh converts a string slice to a <-chan minio.ObjectToDelete
func toRemoveCh(keys []string) <-chan minio.ObjectInfo {
	ch := make(chan minio.ObjectInfo, len(keys))
	go func() {
		for _, key := range keys {
			ch <- minio.ObjectInfo{Key: key}
		}
		close(ch)
	}()
	return ch
}

// DeleteFromBucket deletes objects from the specified bucket.
func (b *Bucket) DeleteFromBucket(ctx context.Context, objectKeys []string) error {
	var deleteErrors []error // Slice to store all errors

	// Initialize a channel of DeleteObjectError to receive errors from RemoveObjects()
	errorCh := b.Client.RemoveObjects(ctx, b.Config.S3BucketName, toRemoveCh(objectKeys), minio.RemoveObjectsOptions{})

	// Loop over the DeleteError channel to receive errors
	for dErr := range errorCh {
		slog.Default().ErrorCtx(ctx, "failed to delete object from s3 bucket",
			slog.String("object_key", dErr.ObjectName),
			slog.String("err", dErr.Err.Error()),
		)

		// Store each error
		deleteErrors = append(deleteErrors, dErr.Err)
	}

	if len(deleteErrors) > 0 {
		var errMsgs []string
		for _, err := range deleteErrors {
			errMsgs = append(errMsgs, err.Error())
		}
		return fmt.Errorf("errors during deletion: %s", strings.Join(errMsgs, "; "))
	}

	return nil
}
