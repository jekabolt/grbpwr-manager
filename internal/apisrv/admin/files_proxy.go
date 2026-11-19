package admin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// FileProxyPath is the route of the authenticated file proxy (GET, `?url=<file url>`).
const FileProxyPath = "/files/proxy"

// fileProxyTimeout bounds one proxied read end to end (open + stream).
const fileProxyTimeout = 2 * time.Minute

// ProxyOpener opens the file behind a url for the caller in ctx (bucket.(*Bucket).OpenProxyObject).
type ProxyOpener func(ctx context.Context, rawURL string) (bucket.ProxyObject, error)

// FileProxyHandler is GET /api/files/proxy?url=<url>: the bytes of a file the admin client already
// holds a url for (DXF quick view, marker layout, crop, 3D model), because the CDN sends no CORS
// headers. It replaces the Vercel function api/media-proxy for installations without functions.
//
// Mounted behind WithAdminAuthz (the admin chain: token, team, origin lock, session, feature Core).
// Every decision about WHICH url may be read is bucket.OpenProxyObject's: managed hosts only, the
// caller's team prefix, never an arbitrary url. No RBAC section: the caller is a member of the team
// whose file it is, and holds the url only because a read it was allowed returned it.
//
// The answer is the object itself — Content-Type, Content-Length, `Cache-Control: private` — and
// is never rendered on the API origin (nosniff, a sandboxing CSP, attachment), since a stored file
// may be HTML or SVG.
func FileProxyHandler(open ProxyOpener) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authsrv.GetAdminAuthz(r.Context()); !ok {
			writeUploadError(w, http.StatusForbidden, "forbidden")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), fileProxyTimeout)
		defer cancel()

		raw := r.URL.Query().Get("url")
		obj, err := open(ctx, raw)
		if err != nil {
			code, msg := fileProxyErrorStatus(ctx, err)
			attrs := []any{slog.Int("status", code), slog.String("host", urlHost(raw)), slog.String("err", err.Error())}
			if t, ok := tenant.From(ctx); ok {
				attrs = append(attrs, slog.Int64("tid", t.ID))
			}
			if code >= http.StatusInternalServerError {
				slog.Default().ErrorContext(ctx, "file proxy: read failed", attrs...)
			} else {
				slog.Default().WarnContext(ctx, "file proxy: refused", attrs...)
			}
			writeUploadError(w, code, msg)
			return
		}
		defer obj.Body.Close()

		h := w.Header()
		ct := obj.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
		if obj.Size >= 0 {
			h.Set("Content-Length", strconv.FormatInt(obj.Size, 10))
		}
		h.Set("Cache-Control", "private, max-age=300")
		h.Set("Vary", "Origin, "+authsrv.AuthMetadataKey)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
		h.Set("Content-Disposition", "attachment")
		w.WriteHeader(http.StatusOK)

		if _, err := io.Copy(w, obj.Body); err != nil {
			slog.Default().WarnContext(ctx, "file proxy: stream broken",
				slog.String("host", urlHost(raw)), slog.String("err", err.Error()))
			// The status is out already: drop the connection so a cut body never passes as whole.
			panic(http.ErrAbortHandler)
		}
	})
}

func fileProxyErrorStatus(ctx context.Context, err error) (int, string) {
	switch {
	case errors.Is(err, bucket.ErrProxyBadURL):
		return http.StatusBadRequest, "bad_url"
	case errors.Is(err, bucket.ErrProxyRefused):
		return http.StatusForbidden, "not_a_team_file"
	case errors.Is(err, bucket.ErrProxyNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, bucket.ErrProxyTooLarge):
		return http.StatusRequestEntityTooLarge, "too_large"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout"
	}
	return http.StatusBadGateway, "upstream_failed"
}

// urlHost is the host of raw for a log line (never the path: keys are unguessable on purpose).
func urlHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return ""
}
