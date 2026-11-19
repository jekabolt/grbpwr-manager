package teamlogo

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Cache lifetimes of the public route. A versioned URL (?v= equal to the stored sha's prefix) never
// changes its bytes; the bare URL (the invite sheet) is revalidated by its ETag after a few minutes.
const (
	cacheVersioned = "public, max-age=31536000, immutable"
	cacheBare      = "public, max-age=300"
	cacheNone      = "public, max-age=60"
	maxSlugLen     = 63
)

// Reader returns a team's stored logo by slug; found=false when the team has none, is pending or
// is deleted (one answer for all three).
type Reader func(ctx context.Context, slug string) (png []byte, sha string, updated time.Time, found bool, err error)

// Handler serves GET / HEAD Path+{slug}: the PNG with ETag = "<sha256>", Last-Modified and a public
// Cache-Control; If-None-Match / If-Modified-Since answer 304. No logo, a pending or deleted team,
// an unknown or malformed slug: the same 404.
func Handler(read Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug, ok := strings.CutPrefix(r.URL.Path, Path)
		if !ok || !plainSlug(slug) {
			notFound(w)
			return
		}
		png, sha, updated, found, err := read(r.Context(), slug)
		if err != nil {
			slog.Default().ErrorContext(r.Context(), "team logo", slog.String("err", err.Error()))
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !found || len(png) == 0 || sha == "" {
			notFound(w)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "image/png")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("ETag", `"`+sha+`"`)
		if v := r.URL.Query().Get("v"); len(sha) >= VersionLen && v == sha[:VersionLen] {
			h.Set("Cache-Control", cacheVersioned)
		} else {
			h.Set("Cache-Control", cacheBare)
		}
		http.ServeContent(w, r, "", updated, bytes.NewReader(png))
	})
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", cacheNone)
	http.Error(w, "not found", http.StatusNotFound)
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
