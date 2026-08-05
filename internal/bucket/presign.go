package bucket

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// presignWindow is the granularity of pattern presign expiry. The expiry is snapped to
// the END of the NEXT window boundary (exp = ceil(now/6h)*6h + 6h), so a presigned url
// is valid between 6 and 12 hours and — crucially — the WINDOW identity is stable for
// hours at a time, which lets us memoize the url string. A stable string means the
// browser's HTTP cache works and <object>/viewer consumers are not remounted on every
// admin API response. Presigning itself stamps the signing time into the url, so
// stability requires serving the memoized copy, not re-signing per request.
const presignWindow = 6 * time.Hour

// patternObjectPathSegment mirrors storeutil's segment constant: only managed pattern
// objects may be presigned through this method — it is reachable from an
// unauthenticated (token-guarded) endpoint and must never sign arbitrary bucket keys.
const patternObjectPathSegment = "tech-card-patterns"

type presignEntry struct {
	url       string
	expiresAt time.Time
}

var (
	presignMu    sync.Mutex
	presignCache = map[string]presignEntry{}
)

// PresignPatternObject returns a presigned GET url for a managed pattern object key,
// targeting the ORIGIN endpoint (presigned requests never pass the CDN — SigV4 binds the
// Host, and DO Spaces does not cache presigned traffic anyway). download=true overrides
// the response content-disposition to attachment.
//
// Deviation from the design note "url string constant within the window": minio-go stamps
// the signing time (X-Amz-Date) at call time and offers no way to pin it, so equality of
// strings is achieved by memoizing the first signature of each (key, dl, window) — stable
// per process. Multiple instances mint different-but-equally-valid strings; the cost is a
// cold browser cache after a hit lands on another instance, accepted like the in-memory
// rate limiter (design R6).
func (b *Bucket) PresignPatternObject(ctx context.Context, objectKey string, download bool) (string, time.Time, error) {
	key := strings.Trim(objectKey, "/")
	if !isManagedPatternKey(key) {
		return "", time.Time{}, fmt.Errorf("object key %q is not a managed pattern key", objectKey)
	}

	now := time.Now().UTC()
	windowStart := now.Truncate(presignWindow)
	expiresAt := windowStart.Add(2 * presignWindow)

	cacheKey := fmt.Sprintf("%s|%t|%d", key, download, windowStart.Unix())
	presignMu.Lock()
	if e, ok := presignCache[cacheKey]; ok && now.Before(e.expiresAt) {
		presignMu.Unlock()
		return e.url, e.expiresAt, nil
	}
	// Opportunistic prune: entries from past windows are dead weight; the map holds at
	// most a few hundred live keys (patterns of recently viewed cards).
	for k, e := range presignCache {
		if !now.Before(e.expiresAt) {
			delete(presignCache, k)
		}
	}
	presignMu.Unlock()

	reqParams := make(url.Values)
	if download {
		reqParams.Set("response-content-disposition",
			fmt.Sprintf("attachment; filename=%q", path.Base(key)))
	}
	u, err := b.Client.PresignedGetObject(ctx, b.S3BucketName, key, time.Until(expiresAt), reqParams)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("presign pattern object %q: %w", key, err)
	}
	presignMu.Lock()
	presignCache[cacheKey] = presignEntry{url: u.String(), expiresAt: expiresAt}
	presignMu.Unlock()
	return u.String(), expiresAt, nil
}

// isManagedPatternKey reports whether the key sits under the dedicated pattern folder
// (any base-folder prefix, same recognition rule as storeutil.PatternObjectKey).
func isManagedPatternKey(key string) bool {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		if segment == patternObjectPathSegment && i < len(segments)-1 {
			return true
		}
	}
	return false
}
