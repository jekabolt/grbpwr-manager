package teamlogo

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Cache lifetimes of the public route. A versioned URL is short enough that deleting a team or
// reusing its address cannot leave an intermediary serving the old tenant's picture for a year;
// the bare URL (the invite sheet) is revalidated by its ETag after a few minutes.
const (
	cacheVersioned = "public, max-age=86400"
	cacheBare      = "public, max-age=300"
	cacheNone      = "public, max-age=60"
	maxSlugLen     = 63
)

// MetadataReader returns a team's logo validators without its BLOB; found=false when the team has
// none, is pending or is deleted (one answer for all three).
type MetadataReader func(ctx context.Context, slug string) (sha string, size int64, updated time.Time, found bool, err error)

// ContentReader returns the PNG only if the slug still names the metadata version just read.
type ContentReader func(ctx context.Context, slug, sha string) (png []byte, found bool, err error)

// Handler serves GET / HEAD Path+{slug}: the PNG with ETag = "<sha256>", Last-Modified and a public
// Cache-Control; If-None-Match / If-Modified-Since answer 304. HEAD and a matching ETag stop after
// metadata. A supplied v must be exactly the current 12-hex prefix. No logo, stale v, pending or
// deleted team, unknown or malformed slug: the same bodyless 404.
func Handler(meta MetadataReader, content ContentReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug, ok := strings.CutPrefix(r.URL.Path, Path)
		if !ok || !plainSlug(slug) {
			notFound(w)
			return
		}
		sha, size, updated, found, err := meta(r.Context(), slug)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if !found || size <= 0 || len(sha) < VersionLen {
			notFound(w)
			return
		}
		if versions, supplied := r.URL.Query()["v"]; supplied && (len(versions) != 1 || versions[0] != sha[:VersionLen]) {
			notFound(w)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "image/png")
		h.Set("X-Content-Type-Options", "nosniff")
		etag := `"` + sha + `"`
		h.Set("ETag", etag)
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		if !updated.IsZero() {
			h.Set("Last-Modified", updated.UTC().Format(http.TimeFormat))
		}
		if _, supplied := r.URL.Query()["v"]; supplied {
			h.Set("Cache-Control", cacheVersioned)
		} else {
			h.Set("Cache-Control", cacheBare)
		}
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Header.Get("If-None-Match") == "" && notModifiedSince(r.Header.Get("If-Modified-Since"), updated) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		png, stillCurrent, err := content(r.Context(), slug, sha)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if !stillCurrent || int64(len(png)) != size {
			notFound(w)
			return
		}
		http.ServeContent(w, r, "", updated, bytes.NewReader(png))
	})
}

func etagMatches(header, etag string) bool {
	for item := range strings.SplitSeq(header, ",") {
		item = strings.TrimSpace(item)
		if item == "*" || item == etag || strings.TrimPrefix(item, "W/") == etag {
			return true
		}
	}
	return false
}

func notModifiedSince(header string, updated time.Time) bool {
	if header == "" || updated.IsZero() {
		return false
	}
	since, err := http.ParseTime(header)
	return err == nil && !updated.UTC().Truncate(time.Second).After(since)
}

func serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Default().ErrorContext(r.Context(), "team logo", slog.String("err", err.Error()))
	h := w.Header()
	h.Del("Content-Length")
	h.Del("Content-Type")
	h.Del("ETag")
	h.Del("Last-Modified")
	h.Del("X-Content-Type-Options")
	h.Set("Cache-Control", "no-store")
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func notFound(w http.ResponseWriter) {
	h := w.Header()
	h.Del("Content-Length")
	h.Del("Content-Type")
	h.Del("ETag")
	h.Del("Last-Modified")
	h.Del("X-Content-Type-Options")
	h.Set("Cache-Control", cacheNone)
	w.WriteHeader(http.StatusNotFound)
}

// plainSlug is a cheap shape check before any lookup: lowercase letters, digits and dashes.
func plainSlug(s string) bool {
	if s == "" || len(s) > maxSlugLen {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
