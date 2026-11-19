package teamlogo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testReaders(metaCalls, contentCalls *int) (MetadataReader, ContentReader) {
	meta := func(_ context.Context, slug string) (string, int64, time.Time, bool, error) {
		*metaCalls++
		switch slug {
		case "acme":
			return testSHA, int64(len("\x89PNG-bytes")), time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC), true, nil
		case "broken":
			return "", 0, time.Time{}, false, errors.New("db down")
		}
		// none, pending, deleted, unknown: the store answers the same.
		return "", 0, time.Time{}, false, nil
	}
	content := func(_ context.Context, slug, sha string) ([]byte, bool, error) {
		*contentCalls++
		if slug == "acme" && sha == testSHA {
			return []byte("\x89PNG-bytes"), true, nil
		}
		return nil, false, nil
	}
	return meta, content
}

func serve(t *testing.T, h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandlerServesTheLogo(t *testing.T) {
	metaCalls, contentCalls := 0, 0
	meta, content := testReaders(&metaCalls, &contentCalls)
	h := Handler(meta, content)
	w := serve(t, h, http.MethodGet, Path+"acme", nil)
	if w.Code != http.StatusOK || w.Body.String() != "\x89PNG-bytes" {
		t.Fatalf("GET = %d %q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("ETag"); got != `"`+testSHA+`"` {
		t.Errorf("ETag = %q", got)
	}
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != cacheBare {
		t.Errorf("bare URL Cache-Control = %q, want %q", got, cacheBare)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("no nosniff")
	}

	w = serve(t, h, http.MethodGet, Path+"acme?v="+testSHA[:VersionLen], nil)
	if got := w.Header().Get("Cache-Control"); got != cacheVersioned {
		t.Errorf("versioned URL Cache-Control = %q, want %q", got, cacheVersioned)
	}
	w = serve(t, h, http.MethodGet, Path+"acme?v=ffffffffffff", nil)
	if w.Code != http.StatusNotFound || w.Body.Len() != 0 || w.Header().Get("ETag") != "" {
		t.Errorf("stale version = %d, body %d, ETag %q; want the common bodyless 404", w.Code, w.Body.Len(), w.Header().Get("ETag"))
	}

	w = serve(t, h, http.MethodGet, Path+"acme", map[string]string{"If-None-Match": `"` + testSHA + `"`})
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Errorf("If-None-Match = %d, body %d bytes; want 304, empty", w.Code, w.Body.Len())
	}
	w = serve(t, h, http.MethodHead, Path+"acme", nil)
	if w.Code != http.StatusOK || w.Header().Get("ETag") == "" {
		t.Errorf("HEAD = %d, ETag %q", w.Code, w.Header().Get("ETag"))
	}
	if w.Body.Len() != 0 || w.Header().Get("Content-Length") != "10" {
		t.Errorf("HEAD body=%d Content-Length=%q", w.Body.Len(), w.Header().Get("Content-Length"))
	}
	w = serve(t, h, http.MethodHead, Path+"acme", map[string]string{"If-Modified-Since": "Thu, 19 Nov 2026 12:00:00 GMT"})
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Errorf("HEAD If-Modified-Since = %d, body %d", w.Code, w.Body.Len())
	}
	if metaCalls != 6 || contentCalls != 2 {
		t.Errorf("reads = metadata %d, content %d; HEAD, 304 and stale v must skip content", metaCalls, contentCalls)
	}
}

// No logo, a pending or deleted team (the reader's found=false), an unknown slug and a malformed
// one all get the same 404; a malformed one never reaches the store.
func TestHandlerOneNotFound(t *testing.T) {
	metaCalls, contentCalls := 0, 0
	meta, content := testReaders(&metaCalls, &contentCalls)
	h := Handler(meta, content)
	var first *httptest.ResponseRecorder
	for _, target := range []string{Path + "nologo", Path + "deleted-team", Path + "UPPER", Path + "a/b", Path + "", Path + strings.Repeat("a", 64), "/api/auth/other/acme"} {
		w := serve(t, h, http.MethodGet, target, nil)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, w.Code)
			continue
		}
		if first == nil {
			first = w
			continue
		}
		if w.Body.String() != first.Body.String() || w.Header().Get("Cache-Control") != first.Header().Get("Cache-Control") ||
			w.Header().Get("ETag") != "" {
			t.Errorf("%s: 404 differs from the first one", target)
		}
	}
	if metaCalls != 2 || contentCalls != 0 {
		t.Errorf("store reads metadata=%d content=%d, want 2/0 (only well-formed slugs)", metaCalls, contentCalls)
	}
	if w := serve(t, h, http.MethodGet, Path+"broken", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("store error = %d, want 500", w.Code)
	}
}
