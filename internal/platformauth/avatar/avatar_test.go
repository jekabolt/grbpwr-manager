package avatar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testSHA   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	brokenSHA = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	goneSHA   = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	testPNG   = "\x89PNG-bytes"
)

func testReaders(metaCalls, contentCalls *int) (MetadataReader, ContentReader) {
	meta := func(_ context.Context, sha string) (int64, time.Time, bool, error) {
		*metaCalls++
		switch sha {
		case testSHA, goneSHA:
			return int64(len(testPNG)), time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC), true, nil
		case brokenSHA:
			return 0, time.Time{}, false, errors.New("db down")
		}
		return 0, time.Time{}, false, nil
	}
	content := func(_ context.Context, sha string) ([]byte, bool, error) {
		*contentCalls++
		if sha == testSHA {
			return []byte(testPNG), true, nil
		}
		return nil, false, nil // goneSHA: removed between the two reads
	}
	return meta, content
}

func serve(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestURL(t *testing.T) {
	for _, c := range []struct{ base, sha, want string }{
		{"https://api.example.com/", testSHA, "https://api.example.com" + Path + testSHA},
		{"https://api.example.com", testSHA, "https://api.example.com" + Path + testSHA},
		{"", testSHA, ""},
		{"https://api.example.com", "", ""},
	} {
		if got := URL(c.base, c.sha); got != c.want {
			t.Errorf("URL(%q, %q) = %q, want %q", c.base, c.sha, got, c.want)
		}
	}
}

func TestHandlerServesTheAvatar(t *testing.T) {
	metaCalls, contentCalls := 0, 0
	h := Handler(testReaders(&metaCalls, &contentCalls))

	w := serve(h, http.MethodGet, Path+testSHA, nil)
	if w.Code != http.StatusOK || w.Body.String() != testPNG {
		t.Fatalf("GET = %d %q", w.Code, w.Body.String())
	}
	for k, want := range map[string]string{
		"ETag":                   `"` + testSHA + `"`,
		"Content-Type":           "image/png",
		"Cache-Control":          "public, max-age=31536000, immutable",
		"X-Content-Type-Options": "nosniff",
		"Last-Modified":          "Thu, 19 Nov 2026 12:00:00 GMT",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

	metaCalls, contentCalls = 0, 0
	w = serve(h, http.MethodHead, Path+testSHA, nil)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || contentCalls != 0 || metaCalls != 1 {
		t.Fatalf("HEAD = %d body %d, meta %d content %d", w.Code, w.Body.Len(), metaCalls, contentCalls)
	}
	if got := w.Header().Get("Content-Length"); got != "10" {
		t.Errorf("HEAD Content-Length = %q", got)
	}

	contentCalls = 0
	w = serve(h, http.MethodGet, Path+testSHA, map[string]string{"If-None-Match": `W/"` + testSHA + `"`})
	if w.Code != http.StatusNotModified || contentCalls != 0 {
		t.Fatalf("If-None-Match = %d, content %d", w.Code, contentCalls)
	}
	w = serve(h, http.MethodGet, Path+testSHA, map[string]string{"If-Modified-Since": "Thu, 19 Nov 2026 12:00:00 GMT"})
	if w.Code != http.StatusNotModified {
		t.Fatalf("If-Modified-Since = %d", w.Code)
	}
}

func TestHandlerOneNotFound(t *testing.T) {
	metaCalls, contentCalls := 0, 0
	h := Handler(testReaders(&metaCalls, &contentCalls))
	for _, target := range []string{
		Path + strings.Repeat("a", 64),  // unknown
		Path + strings.ToUpper(testSHA), // upper-case hex
		Path + testSHA[:63],
		Path + testSHA + "0",
		Path + "../" + testSHA[3:],
		Path,
		"/api/auth/team-logo/" + testSHA,
		Path + goneSHA, // removed after the metadata read
	} {
		w := serve(h, http.MethodGet, target, nil)
		if w.Code != http.StatusNotFound || w.Body.Len() != 0 {
			t.Errorf("%s = %d body %q", target, w.Code, w.Body.String())
		}
		if got := w.Header().Get("Cache-Control"); got != "public, max-age=60" {
			t.Errorf("%s Cache-Control = %q", target, got)
		}
		if w.Header().Get("ETag") != "" || w.Header().Get("Content-Type") != "" {
			t.Errorf("%s leaked image headers: %v", target, w.Header())
		}
	}
	metaCalls = 0
	_ = serve(h, http.MethodGet, Path+"not-a-sha", nil)
	if metaCalls != 0 {
		t.Errorf("a malformed sha reached the store")
	}
}

func TestHandlerStoreErrorIsNotCached(t *testing.T) {
	metaCalls, contentCalls := 0, 0
	h := Handler(testReaders(&metaCalls, &contentCalls))
	w := serve(h, http.MethodGet, Path+brokenSHA, nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("store error = %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}
