package bucket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

// WIPE OF A DELETED TEAM'S FILES (teams D3, lanes/D3-design.md §2.3). Every object of a team other
// than #1 lives under {base}/t/{file_key}/ (teamprefix.go); team #1's objects are everything outside
// {base}/t/, so no prefix this file builds can ever reach them. The prefix is built only by
// TeamWipePrefix, which refuses teams mode off, an empty base folder and a file key that is not 32
// lower-case hex; every listed key is checked against the prefix again before it joins a delete.

// ErrWipePrefix: the wipe prefix cannot be built (teams mode off, no base folder, bad file key).
var ErrWipePrefix = errors.New("bucket: refusing to build a team wipe prefix")

const (
	// wipeBatch is one RemoveObjects request (the S3 multi-delete maximum).
	wipeBatch = 1000
	// wipePause spaces the delete requests (~5 per second).
	wipePause = 200 * time.Millisecond
)

// TeamWipePrefix is "{base}/t/{fileKey}/" for a team's files, or ErrWipePrefix.
func TeamWipePrefix(c *Config, fileKey string) (string, error) {
	if c == nil || !c.TeamsMode {
		return "", fmt.Errorf("%w: teams mode is off", ErrWipePrefix)
	}
	base := strings.Trim(strings.TrimSpace(c.BaseFolder), "/")
	if base == "" {
		return "", fmt.Errorf("%w: no base folder", ErrWipePrefix)
	}
	if !validFileKey(fileKey) {
		return "", fmt.Errorf("%w: file key is not 32 lower-case hex", ErrWipePrefix)
	}
	return base + "/" + teamSegment + "/" + fileKey + "/", nil
}

// wipeClient is the part of the S3 client a wipe uses (*minio.Client; a fake in tests).
type wipeClient interface {
	GetBucketVersioning(ctx context.Context, bucketName string) (minio.BucketVersioningConfiguration, error)
	ListObjects(ctx context.Context, bucketName string, opts minio.ListObjectsOptions) <-chan minio.ObjectInfo
	RemoveObjects(ctx context.Context, bucketName string, objectsCh <-chan minio.ObjectInfo, opts minio.RemoveObjectsOptions) <-chan minio.RemoveObjectError
}

// WipeStats is one wipe call: objects deleted (or, in a preview, found) and their bytes; Done when a
// full listing of the prefix came back empty.
type WipeStats struct {
	Objects int64
	Bytes   int64
	Done    bool
	// Foreign: listed keys outside the prefix, skipped (never deleted). Should always be 0.
	Foreign int64
}

// WipeTeamFiles deletes the objects (every version, when the bucket keeps versions) under the
// team's prefix, at most maxBatches delete requests per call; Done when the prefix lists empty.
func (b *Bucket) WipeTeamFiles(ctx context.Context, fileKey string, maxBatches int) (WipeStats, error) {
	prefix, err := TeamWipePrefix(b.Config, fileKey)
	if err != nil {
		return WipeStats{}, err
	}
	return wipePrefix(ctx, b.Client, b.S3BucketName, prefix, maxBatches, false)
}

// PreviewTeamFiles counts the objects and bytes under the team's prefix; nothing is deleted.
func (b *Bucket) PreviewTeamFiles(ctx context.Context, fileKey string) (WipeStats, error) {
	prefix, err := TeamWipePrefix(b.Config, fileKey)
	if err != nil {
		return WipeStats{}, err
	}
	return wipePrefix(ctx, b.Client, b.S3BucketName, prefix, 0, true)
}

// versioned reports whether the bucket may hold old versions (versioning enabled or suspended).
func versioned(ctx context.Context, cli wipeClient, bucketName string) (bool, error) {
	v, err := cli.GetBucketVersioning(ctx, bucketName)
	if err != nil {
		return false, fmt.Errorf("bucket versioning: %w", err)
	}
	return v.Enabled() || v.Suspended(), nil
}

// wipePrefix lists prefix (with versions when the bucket keeps them) and, unless preview, deletes
// what it lists in batches of wipeBatch, at most maxBatches batches, then lists again to prove the
// prefix empty. A per-key delete error fails the call after the batch (the next call resumes).
func wipePrefix(ctx context.Context, cli wipeClient, bucketName, prefix string, maxBatches int, preview bool) (WipeStats, error) {
	var st WipeStats
	if bucketName == "" || prefix == "" || !strings.HasSuffix(prefix, "/") || strings.Count(prefix, "/") < 3 {
		return st, fmt.Errorf("%w: prefix %q", ErrWipePrefix, prefix)
	}
	withVersions, err := versioned(ctx, cli, bucketName)
	if err != nil {
		return st, err
	}
	list := func() ([]minio.ObjectInfo, error) {
		lctx, cancel := context.WithCancel(ctx)
		defer cancel()
		limit := maxBatches * wipeBatch
		var out []minio.ObjectInfo
		for obj := range cli.ListObjects(lctx, bucketName, minio.ListObjectsOptions{
			Prefix: prefix, Recursive: true, WithVersions: withVersions,
		}) {
			if obj.Err != nil {
				return nil, fmt.Errorf("list team files: %w", obj.Err)
			}
			if !strings.HasPrefix(obj.Key, prefix) {
				st.Foreign++
				slog.Default().ErrorContext(ctx, "team wipe: listed key outside the prefix skipped")
				continue
			}
			if preview {
				st.Objects++
				st.Bytes += obj.Size
				continue
			}
			out = append(out, minio.ObjectInfo{Key: obj.Key, VersionID: obj.VersionID, Size: obj.Size})
			if len(out) >= limit {
				break
			}
		}
		return out, nil
	}
	objs, err := list()
	if err != nil || preview {
		st.Done = err == nil
		return st, err
	}
	if len(objs) == 0 {
		st.Done = true
		return st, nil
	}
	for start := 0; start < len(objs); start += wipeBatch {
		if start > 0 {
			select {
			case <-ctx.Done():
				return st, ctx.Err()
			case <-time.After(wipePause):
			}
		}
		end := min(start+wipeBatch, len(objs))
		ch := make(chan minio.ObjectInfo, end-start)
		for _, o := range objs[start:end] {
			if !strings.HasPrefix(o.Key, prefix) { // second fence: never a key outside the prefix
				continue
			}
			ch <- o
		}
		close(ch)
		failed := map[string]bool{}
		var firstErr error
		for e := range cli.RemoveObjects(ctx, bucketName, ch, minio.RemoveObjectsOptions{}) {
			if e.Err != nil {
				failed[e.ObjectName+"\x00"+e.VersionID] = true
				if firstErr == nil {
					firstErr = e.Err
				}
			}
		}
		for _, o := range objs[start:end] {
			if !failed[o.Key+"\x00"+o.VersionID] {
				st.Objects++
				st.Bytes += o.Size
			}
		}
		if firstErr != nil {
			return st, fmt.Errorf("delete team files: %d failed: %w", len(failed), firstErr)
		}
	}
	rest, err := list()
	if err != nil {
		return st, err
	}
	st.Done = len(rest) == 0
	return st, nil
}
