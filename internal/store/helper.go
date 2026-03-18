package store

import (
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// truncatePagePath delegates to storeutil.TruncatePagePath for backward compatibility.
func truncatePagePath(s string) string {
	return storeutil.TruncatePagePath(s)
}

// joinInts delegates to storeutil.JoinInts for backward compatibility.
func joinInts(ids []int) string {
	return storeutil.JoinInts(ids)
}

// parseDateStr delegates to storeutil.ParseDateStr for backward compatibility.
func parseDateStr(s string) (time.Time, error) {
	return storeutil.ParseDateStr(s)
}
