// Package avatar serves account photos (P0 §3.2). The picture itself is processed as a team logo
// (teamlogo.Process: PNG or JPEG up to 1 MB, fitted into 256x256, re-encoded as PNG); this package
// only names and serves it. The URL is content-addressed: /api/auth/avatar/<sha256 of the PNG>, so it
// carries no id or handle, nothing can be enumerated, and a URL never changes meaning, which lets it
// be cached for a year as immutable.
package avatar

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Path is the public route prefix; the sha256 follows it.
const Path = "/api/auth/avatar/"

const (
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheNone      = "public, max-age=60"
	shaLen         = 64
)

// URL is the public address of the avatar with this sha256; "" when either part is empty.
func URL(apiBase, sha string) string {
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" || sha == "" {
		return ""
	}
	return apiBase + Path + sha
}

// MetadataReader returns the size and time of the avatar with this sha256 without its BLOB;
// found=false when no account has it (never set, removed or replaced).
type MetadataReader func(ctx context.Context, sha string) (size int64, updated time.Time, found bool, err error)

// ContentReader returns the PNG with this sha256; found=false when no account has it any more.
type ContentReader func(ctx context.Context, sha string) (png []byte, found bool, err error)

// Handler serves GET / HEAD Path+{sha256}: the PNG with ETag = "<sha256>", Last-Modified and an
// immutable year-long Cache-Control; If-None-Match / If-Modified-Since answer 304. HEAD and a
// matching ETag stop after metadata. A malformed sha, an unknown one, or bytes that are not that
// sha's any more: the same bodyless 404, cached for a minute.
func Handler(meta MetadataReader, content ContentReader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sha, ok := strings.CutPrefix(r.URL.Path, Path)
		if !ok || !plainSHA(sha) {
			notFound(w)
			return
		}
		size, updated, found, err := meta(r.Context(), sha)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if !found || size <= 0 {
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
		h.Set("Cache-Control", cacheImmutable)
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
		png, found, err := content(r.Context(), sha)
		if err != nil {
			serverError(w, r, err)
			return
		}
		if !found || int64(len(png)) != size {
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
	slog.Default().ErrorContext(r.Context(), "avatar", slog.String("err", err.Error()))
	clearImageHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func notFound(w http.ResponseWriter) {
	clearImageHeaders(w)
	w.Header().Set("Cache-Control", cacheNone)
	w.WriteHeader(http.StatusNotFound)
}

func clearImageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Del("Content-Length")
	h.Del("Content-Type")
	h.Del("ETag")
	h.Del("Last-Modified")
	h.Del("X-Content-Type-Options")
}

// plainSHA is a cheap shape check before any lookup: exactly 64 lowercase hex characters.
func plainSHA(s string) bool {
	if len(s) != shaLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
