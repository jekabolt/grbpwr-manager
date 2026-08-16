package bucket

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
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
// downloadName is the filename the attachment disposition names the object under; it must
// come from stored data (the upload's own filename), NEVER from a request parameter — it
// lands in a response header. An empty value falls back to the object key's basename.
func (b *Bucket) PresignPatternObject(ctx context.Context, objectKey string, download bool, downloadName string) (string, time.Time, error) {
	return b.presignManagedObject(ctx, objectKey, patternObjectPathSegment, download, downloadName)
}

// PresignLibraryObject is the same mechanism for files-library objects (the file
// itself and its preview image, which lives under files-library/previews/ and is
// therefore covered by the same segment).
//
// It is a SEPARATE method rather than an extra segment accepted by
// PresignPatternObject on purpose: that one is reachable from the unauthenticated
// token endpoints (/api/p, /api/pv, /api/rp), and widening the set of keys it can
// sign would widen what those endpoints could be talked into serving. This one is
// only ever called from RBAC-gated admin handlers, and keeping the two apart is
// what keeps that difference true by construction rather than by discipline.
func (b *Bucket) PresignLibraryObject(ctx context.Context, objectKey string, download bool, downloadName string) (string, time.Time, error) {
	return b.presignManagedObject(ctx, objectKey, libraryFolder, download, downloadName)
}

// presignManagedObject holds the shared mechanics: the managed-key guard, the
// window snapping that keeps the url string stable, the memoization and its
// prune, and the download-name sanitisation.
func (b *Bucket) presignManagedObject(ctx context.Context, objectKey, requiredSegment string, download bool, downloadName string) (string, time.Time, error) {
	key := strings.Trim(objectKey, "/")
	if !isManagedKeyInSegment(key, requiredSegment) {
		return "", time.Time{}, fmt.Errorf("object key %q is not a managed %q key", objectKey, requiredSegment)
	}

	name := sanitizeDownloadName(downloadName)
	if name == "" {
		name = path.Base(key)
	}

	now := time.Now().UTC()
	windowStart := now.Truncate(presignWindow)
	expiresAt := windowStart.Add(2 * presignWindow)

	cacheKey := fmt.Sprintf("%s|%t|%s|%d", key, download, name, windowStart.Unix())
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
			fmt.Sprintf("attachment; filename=%q", name))
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

// isManagedKeyInSegment reports whether the key sits under the given dedicated folder
// (any base-folder prefix, same recognition rule as storeutil.PatternObjectKey).
// Relative segments are refused outright: they cannot escape this bucket (the host is
// client-config-fixed and any normalizing intermediary would break SigV4), but the rule is
// reused by the label path (Ф7b) and the files library, and must not depend on that
// argument holding there.
//
// The segment must not be the LAST one, which is what makes "the folder itself" an
// invalid target: a key has to name an object, not a prefix.
func isManagedKeyInSegment(key, requiredSegment string) bool {
	if requiredSegment == "" {
		return false
	}
	// A multi-part segment spec (files-library/previews) is matched as a prefix run,
	// but callers pass the top folder and rely on nesting being covered — keep the
	// simple case simple and refuse anything with a separator.
	if strings.Contains(requiredSegment, "/") {
		return false
	}
	found := false
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		// An empty segment means a trailing slash or a doubled separator: that names a
		// prefix, not an object, and would presign a key with no basename.
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		if segment == requiredSegment && i < len(segments)-1 {
			found = true
		}
	}
	return found
}

// sanitizeDownloadName reduces a stored filename to a bare, header-safe basename. Quotes,
// control characters and path separators are dropped rather than escaped — the value is
// interpolated into a quoted Content-Disposition parameter.
func sanitizeDownloadName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = path.Base(filepath.ToSlash(name))
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '/' {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
