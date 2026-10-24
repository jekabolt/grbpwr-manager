package techcard

import (
	"strings"
	"time"
)

const (
	techCardSnapshotBucketWidth = 5 * time.Minute
	techCardSnapshotKeepCount   = 100
)

func techCardSnapshotBucket(now time.Time) time.Time {
	return now.UTC().Truncate(techCardSnapshotBucketWidth)
}

func mergeSnapshotChanges(existing, added []string) []string {
	out := make([]string, 0, len(existing)+len(added))
	seen := make(map[string]struct{}, len(existing)+len(added))
	for _, list := range [][]string{existing, added} {
		for _, change := range list {
			change = strings.TrimSpace(change)
			if change == "" {
				continue
			}
			if _, duplicate := seen[change]; duplicate {
				continue
			}
			seen[change] = struct{}{}
			out = append(out, change)
		}
	}
	return out
}
