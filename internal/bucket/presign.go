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
// sign would widen what those endpoints could be talked into serving. Keeping the
// two apart is what keeps that difference true by construction rather than by
// discipline — a pattern token can never reach a library object and vice versa.
//
// С Ф7 У НЕГО ДВА ЗАКОННЫХ ВЫЗЫВАЮЩИХ, А НЕ ОДИН: админские хендлеры под RBAC и
// ПУБЛИЧНЫЙ маршрут /api/f/{token} (internal/fileaccess). Второй — неаутентифицированный,
// поэтому охранное правило теперь надо назвать вслух: objectKey приходит ТОЛЬКО из строки
// library_file (entity.LibraryFileLinkTarget) и НИКОГДА из запроса. Сегментный гейт ниже
// сужает подписываемое до files-library/, но он не отличает чужой ключ библиотеки от своего —
// «ключ только из строки БД» и есть то, что не даёт этому методу стать оракулом, выдающим
// произвольный объект библиотеки по подобранному имени.
//
// ACL ОБЪЕКТА НЕ ТРОГАЕТСЯ НИ ОДНИМ ИЗ ДВУХ. Публичность файла — это наш маршрут, который
// подписывает короткоживущий url на каждое попадание; публичный ACL пережил бы и смену уровня
// доступа, и удаление файла, и отозвать его было бы нечем.
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
		reqParams.Set("response-content-disposition", contentDisposition(name))
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

// contentDisposition builds an attachment disposition that survives a non-ASCII
// filename.
//
// The naive `filename="макет бирки.pdf"` is a trap: the header is defined over
// ISO-8859-1, and S3 rejects a response-content-disposition override carrying raw
// UTF-8 with 400 InvalidArgument. Since Russian filenames are the norm here rather
// than the exception, that would have meant the download link is dead for most of
// the library while inline viewing kept working — a failure that looks like a
// broken file rather than a broken header.
//
// RFC 5987/6266 form: an ASCII-only `filename` that any client can read, plus a
// percent-encoded `filename*` that modern clients prefer. Both are always emitted;
// they cost nothing when the name is already ASCII.
func contentDisposition(name string) string {
	ascii := asciiFallbackName(name)
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s",
		ascii, percentEncodeRFC5987(name))
}

// asciiFallbackName replaces every non-ASCII rune with '_' so the legacy
// `filename` parameter stays inside what the header grammar allows. An entirely
// non-ASCII name would collapse to underscores, so it falls back to a neutral
// stand-in — clients that understand filename* never see it anyway.
func asciiFallbackName(name string) string {
	var b strings.Builder
	meaningful := false
	for _, r := range name {
		switch {
		case r > 0x7f || r < 0x20 || r == 0x7f:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
			if r != '_' && r != ' ' && r != '.' {
				meaningful = true
			}
		}
	}
	if !meaningful {
		// Keep the extension when it is itself readable ASCII — it is what tells the
		// OS how to open the file once it lands on disk. An extension that is also
		// non-ASCII carries nothing, so appending it would only produce "file.file".
		if i := strings.LastIndexByte(name, '.'); i >= 0 && i < len(name)-1 {
			if ext := name[i+1:]; isASCIIAlnum(ext) {
				return "file." + ext
			}
		}
		return "file"
	}
	return b.String()
}

// isASCIIAlnum reports whether s is non-empty and made only of ASCII letters and
// digits — the shape a file extension has to have to be worth keeping.
func isASCIIAlnum(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// percentEncodeRFC5987 encodes a filename for the `filename*` parameter: every
// byte outside the attr-char set is percent-encoded. url.QueryEscape is not a
// substitute — it encodes a space as '+', which is wrong here.
func percentEncodeRFC5987(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[c>>4])
		b.WriteByte(upperhex[c&0x0f])
	}
	return b.String()
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
