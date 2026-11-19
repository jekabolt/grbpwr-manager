package admin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/stretchr/testify/require"
)

func proxyRequest(t *testing.T, h http.Handler, authed bool, rawURL string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/files/proxy?url="+url.QueryEscape(rawURL), nil)
	if authed {
		req = req.WithContext(authsrv.PutAdminAuthz(req.Context(), authsrv.AdminAuthz{Perms: nil}))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFileProxyHandlerServesObject(t *testing.T) {
	var gotURL string
	h := FileProxyHandler(func(_ context.Context, raw string) (bucket.ProxyObject, error) {
		gotURL = raw
		return bucket.ProxyObject{Body: io.NopCloser(strings.NewReader("0\nSECTION")), Size: 9, ContentType: "application/dxf"}, nil
	})
	rec := proxyRequest(t, h, true, "https://cdn.lekalo.test/lk/t/x/p.dxf?a=1&b=2")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "https://cdn.lekalo.test/lk/t/x/p.dxf?a=1&b=2", gotURL, "the url arrives whole")
	require.Equal(t, "0\nSECTION", rec.Body.String())
	require.Equal(t, "application/dxf", rec.Header().Get("Content-Type"))
	require.Equal(t, "9", rec.Header().Get("Content-Length"))
	require.Equal(t, "private, max-age=300", rec.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Contains(t, rec.Header().Get("Content-Security-Policy"), "sandbox")
	require.Equal(t, "attachment", rec.Header().Get("Content-Disposition"))
}

func TestFileProxyHandlerUnknownTypeAndLength(t *testing.T) {
	h := FileProxyHandler(func(context.Context, string) (bucket.ProxyObject, error) {
		return bucket.ProxyObject{Body: io.NopCloser(strings.NewReader("abc")), Size: -1}, nil
	})
	rec := proxyRequest(t, h, true, "https://cdn.lekalo.test/a")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	require.Empty(t, rec.Header().Get("Content-Length"))
}

func TestFileProxyHandlerRefusals(t *testing.T) {
	for err, want := range map[error]int{
		bucket.ErrProxyBadURL:    http.StatusBadRequest,
		bucket.ErrProxyRefused:   http.StatusForbidden,
		bucket.ErrProxyNotFound:  http.StatusNotFound,
		bucket.ErrProxyTooLarge:  http.StatusRequestEntityTooLarge,
		context.DeadlineExceeded: http.StatusGatewayTimeout,
		fmt.Errorf("dial: boom"): http.StatusBadGateway,
	} {
		h := FileProxyHandler(func(context.Context, string) (bucket.ProxyObject, error) {
			return bucket.ProxyObject{}, fmt.Errorf("wrapped: %w", err)
		})
		rec := proxyRequest(t, h, true, "https://attacker.example/x")
		require.Equalf(t, want, rec.Code, "%v", err)
		require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	}
}

// Without the admin chain's authorization in ctx the opener is never called.
func TestFileProxyHandlerRequiresAdminAuthz(t *testing.T) {
	called := false
	h := FileProxyHandler(func(context.Context, string) (bucket.ProxyObject, error) {
		called = true
		return bucket.ProxyObject{}, nil
	})
	rec := proxyRequest(t, h, false, "https://cdn.lekalo.test/a")
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, called)
}

type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n > 0 {
		f.n = 0
		return copy(p, "abcd"), nil
	}
	return 0, bucket.ErrProxyTooLarge
}

// A stream that breaks half-way (oversized body without a length) aborts the connection instead of
// ending a short body as if it were whole.
func TestFileProxyHandlerAbortsBrokenStream(t *testing.T) {
	h := FileProxyHandler(func(context.Context, string) (bucket.ProxyObject, error) {
		return bucket.ProxyObject{Body: io.NopCloser(&failingReader{n: 1}), Size: -1}, nil
	})
	require.PanicsWithValue(t, http.ErrAbortHandler, func() {
		proxyRequest(t, h, true, "https://cdn.lekalo.test/a")
	})
}
