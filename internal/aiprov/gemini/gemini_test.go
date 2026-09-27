package gemini

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/endpoints"
	"github.com/jekabolt/grbpwr-manager/internal/bucket"
)

// ─── fixtures ────────────────────────────────────────────────────────────────────────────────────
//
// UNVERIFIED (G-05): every Gemini body below is written from memory of the REST reference, not
// captured from the live API — the request shape (systemInstruction / contents / inlineData /
// generationConfig / thinkingConfig.thinkingBudget), the response (candidates[].content.parts[].text,
// finishReason words, usageMetadata names, modelVersion, responseId, promptFeedback.blockReason) and
// the 400 bad-key body (status INVALID_ARGUMENT, details[].reason API_KEY_INVALID). G-05 checks them
// live with the owner's key.

// okBody is the smallest usable answer. UNVERIFIED (G-05).
const okBody = `{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`

// badKeyBody is Google's answer to a wrong key, HTTP 400. UNVERIFIED (G-05).
const badKeyBody = `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT",` +
	`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID","domain":"googleapis.com",` +
	`"metadata":{"service":"generativelanguage.googleapis.com"}}]}}`

// picBytes base64-encode to "+/+/" — two characters the URL-safe alphabet spells differently, so a
// golden catches the wrong encoding.
var picBytes = []byte{0xfb, 0xff, 0xbf}

// testBucket is a bucket config; ManagedObjectKeyFromURL bound to it is the REAL host check E3 wires.
var testBucket = &bucket.Config{
	SubdomainEndpoint: "files.grbpwr.example",
	S3Endpoint:        "fra1.digitaloceanspaces.com",
	S3BucketName:      "grbpwr",
}

func managedKey(raw string) (string, error) { return bucket.ManagedObjectKeyFromURL(testBucket, raw) }

// fakeObjects is the bucket: objects by key, an optional lying size, an optional error; every key
// asked for is recorded.
type fakeObjects struct {
	mu      sync.Mutex
	objects map[string][]byte
	sizes   map[string]int64
	err     error
	block   bool
	asked   []string
}

func (f *fakeObjects) GetManagedObject(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	f.asked = append(f.asked, key)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return nil, 0, ctx.Err()
	}
	if f.err != nil {
		return nil, 0, f.err
	}
	b, ok := f.objects[key]
	if !ok {
		return nil, 0, fmt.Errorf("no object %q", key)
	}
	size := int64(len(b))
	if s, ok := f.sizes[key]; ok {
		size = s
	}
	return io.NopCloser(bytes.NewReader(b)), size, nil
}

func (f *fakeObjects) askedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// recorder is a fake Google that records every request and answers with reply.
type recorder struct {
	mu      sync.Mutex
	bodies  []string
	headers []http.Header
	paths   []string
	queries []string
	methods []string
}

func (rec *recorder) server(t *testing.T, reply func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, string(b))
		rec.headers = append(rec.headers, r.Header.Clone())
		rec.paths = append(rec.paths, r.URL.Path)
		rec.queries = append(rec.queries, r.URL.RawQuery)
		rec.methods = append(rec.methods, r.Method)
		rec.mu.Unlock()
		reply(w)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (rec *recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.bodies)
}

func answer(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

func status(code int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}
}

func key(k string) func() string { return func() string { return k } }

// newAt is the transport pointed at a stand: the ONLY way to move the base is this package's
// unexported field.
func newAt(url string, cfg Config) *Client {
	if cfg.KeyFunc == nil {
		cfg.KeyFunc = key("k")
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 2 * time.Second
	}
	c := New(cfg)
	c.base = url
	return c
}

func callErr(t *testing.T, err error) *aiprov.CallError {
	t.Helper()
	require.Error(t, err)
	ce, ok := aiprov.AsCallError(err)
	require.True(t, ok, "every failure is a *aiprov.CallError, got %T: %v", err, err)
	return ce
}

// ─── request bytes ───────────────────────────────────────────────────────────────────────────────

// TestRequestBytes pins the EXACT body and path of every request shape. Golden strings, not field
// checks: a reordered key, a new key or a changed value is a new request to a paid API.
//
// MUTATIONS (each measured red → green): thinkingOff without the "flash" condition → the pro row;
// thinkingOff true for any effort → the "low effort" row; responseMimeType always set → the text
// rows; systemInstruction without the blank check → the "no system" row; base64.URLEncoding for the
// pictures → both picture rows; `omitempty` dropped from maxOutputTokens → the text rows; the
// "models/" prefix not stripped → the prefixed row (the slug is refused).
func TestRequestBytes(t *testing.T) {
	const sys = `"systemInstruction":{"parts":[{"text":"sys"}]}`
	const user = `"contents":[{"role":"user","parts":[{"text":"u"}]}]`
	objects := &fakeObjects{objects: map[string][]byte{"base/f/2026/sept/look.png": picBytes}}
	cases := []struct {
		name  string
		model string
		req   aiprov.ChatRequest
		path  string
		want  string
	}{
		{
			name: "text, provider defaults", model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{System: "sys", User: "u"},
			want: `{` + sys + `,` + user + `,"generationConfig":{"temperature":0.2}}`,
		},
		{
			name: "text, UserAsParts means nothing here", model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{System: "sys", User: "u", UserAsParts: true},
			want: `{` + sys + `,` + user + `,"generationConfig":{"temperature":0.2}}`,
		},
		{
			name: "no system", model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{System: "  ", User: "u"},
			want: `{` + user + `,"generationConfig":{"temperature":0.2}}`,
		},
		{
			name: "json mode + ceiling", model: "gemini-2.5-pro",
			req:  aiprov.ChatRequest{System: "sys", User: "u", JSONMode: true, MaxTokens: 900},
			path: "/v1beta/models/gemini-2.5-pro:generateContent",
			want: `{` + sys + `,` + user + `,"generationConfig":{"temperature":0.2,"maxOutputTokens":900,"responseMimeType":"application/json"}}`,
		},
		{
			name: "thinking off on flash (none)", model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{System: "sys", User: "u", Effort: "none"},
			want: `{` + sys + `,` + user + `,"generationConfig":{"temperature":0.2,"thinkingConfig":{"thinkingBudget":0}}}`,
		},
		{
			name: "thinking off on flash-lite (minimal)", model: "gemini-2.5-flash-lite",
			req:  aiprov.ChatRequest{System: "sys", User: "u", Effort: "minimal", MaxTokens: 50},
			path: "/v1beta/models/gemini-2.5-flash-lite:generateContent",
			want: `{` + sys + `,` + user + `,"generationConfig":{"temperature":0.2,"maxOutputTokens":50,"thinkingConfig":{"thinkingBudget":0}}}`,
		},
		{
			name: "thinking absent on pro", model: "gemini-2.5-pro",
			req:  aiprov.ChatRequest{System: "sys", User: "u", Effort: "none"},
			path: "/v1beta/models/gemini-2.5-pro:generateContent",
			want: `{` + sys + `,` + user + `,"generationConfig":{"temperature":0.2}}`,
		},
		{
			name: "thinking absent on flash with low effort", model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{System: "sys", User: "u", Effort: "low"},
			want: `{` + sys + `,` + user + `,"generationConfig":{"temperature":0.2}}`,
		},
		{
			name: "one inline picture from the bucket", model: "gemini-2.5-flash",
			req: aiprov.ChatRequest{System: "sys", User: "u", ImageURLs: []string{" https://files.grbpwr.example/base/f/2026/sept/look.png "},
				JSONMode: true, MaxTokens: 300, Effort: "minimal"},
			want: `{` + sys + `,"contents":[{"role":"user","parts":[{"text":"u"},{"inlineData":{"mimeType":"image/png","data":"+/+/"}}]}],` +
				`"generationConfig":{"temperature":0.2,"maxOutputTokens":300,"responseMimeType":"application/json","thinkingConfig":{"thinkingBudget":0}}}`,
		},
		{
			name: "a data URI is decoded and re-encoded", model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{User: "u", ImageURLs: []string{"data:image/jpg;base64,+/+/"}},
			want: `{"contents":[{"role":"user","parts":[{"text":"u"},{"inlineData":{"mimeType":"image/jpeg","data":"+/+/"}}]}],"generationConfig":{"temperature":0.2}}`,
		},
		{
			name: "Google's own models/ prefix is dropped", model: " models/gemini-2.5-flash ",
			req:  aiprov.ChatRequest{System: "sys", User: "u"},
			want: `{` + sys + `,` + user + `,"generationConfig":{"temperature":0.2}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(okBody))
			c := newAt(srv.URL+"/", Config{Objects: objects, KeyFromURL: managedKey})
			_, err := c.Chat(context.Background(), tc.model, tc.req)
			require.NoError(t, err)
			require.Equal(t, 1, rec.count())
			require.Equal(t, tc.want, rec.bodies[0])
			wantPath := tc.path
			if wantPath == "" {
				wantPath = "/v1beta/models/gemini-2.5-flash:generateContent"
			}
			require.Equal(t, wantPath, rec.paths[0], "one trailing slash of the base is not a second path segment")
			require.Equal(t, http.MethodPost, rec.methods[0])
		})
	}
}

// TestHeaders — the key in x-goog-api-key and nowhere else: no Authorization, nothing in the query
// (a query string lands in access logs).
//
// MUTATION: send the key as `?key=` instead of the header → red.
func TestHeaders(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	_, err := newAt(srv.URL, Config{KeyFunc: key("secret-k")}).Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	h := rec.headers[0]
	require.Equal(t, "secret-k", h.Get("X-Goog-Api-Key"))
	require.Equal(t, "application/json", h.Get("Content-Type"))
	require.Empty(t, h.Get("Authorization"))
	require.Empty(t, rec.queries[0], "the key never rides in the URL")
}

// TestTheBaseIsTheConstant — New points at endpoints.GeminiAPIBase and nothing in Config moves it.
func TestTheBaseIsTheConstant(t *testing.T) {
	c := New(Config{KeyFunc: key("k")})
	require.Equal(t, endpoints.GeminiAPIBase, c.BaseURL())
	require.Equal(t, "https://generativelanguage.googleapis.com", c.BaseURL())
}

// TestKeyFuncIsReadPerRequest — a key saved in the panel reaches the NEXT request; "" disables the
// transport before the wire.
//
// MUTATION: cache the key in New → red on the second header.
func TestKeyFuncIsReadPerRequest(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	var mu sync.Mutex
	current := "key-one"
	c := newAt(srv.URL, Config{KeyFunc: func() string {
		mu.Lock()
		defer mu.Unlock()
		return current
	}})
	set := func(k string) { mu.Lock(); current = k; mu.Unlock() }

	_, err := c.Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	set("  key-two  ")
	_, err = c.Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	require.Equal(t, "key-one", rec.headers[0].Get("X-Goog-Api-Key"))
	require.Equal(t, "key-two", rec.headers[1].Get("X-Goog-Api-Key"))

	require.True(t, c.Enabled())
	set("")
	require.False(t, c.Enabled())
	_, err = c.Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	require.ErrorIs(t, err, aiprov.ErrNotConfigured)
	require.Equal(t, 2, rec.count(), "a disabled transport reached the wire")
}

// TestOneKeyPerRequest — KeyFunc is read exactly once per Chat and the header carries that read.
//
// MUTATION: read the key again for the header (c.key() in post) → red: two reads, header "k2".
func TestOneKeyPerRequest(t *testing.T) {
	reads := 0
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Goog-Api-Key")
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()
	c := newAt(srv.URL, Config{KeyFunc: func() string {
		reads++
		return fmt.Sprintf("k%d", reads)
	}})
	_, err := c.Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	require.Equal(t, 1, reads, "one read per request")
	require.Equal(t, "k1", seen)
}

// ─── response facts ──────────────────────────────────────────────────────────────────────────────

// TestResponseFacts — text (all parts, thoughts skipped, trimmed), finish reason in the callers'
// words, reported model (else the requested slug), responseId, and the usage in aiprov's convention:
// Completion = candidates + thoughts, Reasoning = thoughts, Cached from cachedContentTokenCount. CostUSD
// is NULL (Google reports no charge). Asserted NON-ZERO: a misspelled name leaves zeros and a call
// that reads as cheap.
//
// MUTATIONS (each measured red → green): Completion without + thoughts → the full row; thought parts
// not skipped → the full row's text; modelVersion ignored → the full row's model; count() without
// the quote trim → the "oddly typed" row's quoted promptTokenCount reads 0.
func TestResponseFacts(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantText   string
		wantFinish string
		wantModel  string
		wantID     string
		wantUsage  aiprov.TokenUsage
	}{
		{
			name: "everything reported",
			body: `{"candidates":[{"content":{"parts":[{"text":"thinking…","thought":true},{"text":"  {\"a\":"},{"text":"1}  "}],"role":"model"},` +
				`"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1200,"candidatesTokenCount":80,` +
				`"thoughtsTokenCount":30,"cachedContentTokenCount":1000,"totalTokenCount":1310},` +
				`"modelVersion":"gemini-2.5-flash-preview-09-2025","responseId":"resp-123"}`,
			wantText: `{"a":1}`, wantFinish: "stop", wantModel: "gemini-2.5-flash-preview-09-2025", wantID: "resp-123",
			wantUsage: aiprov.TokenUsage{Prompt: 1200, Completion: 110, Cached: 1000, Reasoning: 30},
		},
		{
			name:     "cut at the ceiling",
			body:     `{"candidates":[{"content":{"parts":[{"text":"half"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":7}}`,
			wantText: "half", wantFinish: "length", wantModel: "gemini-2.5-flash",
			wantUsage: aiprov.TokenUsage{Prompt: 5, Completion: 7},
		},
		{
			name:     "an unknown finish word passes as-is",
			body:     `{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
			wantText: "x", wantFinish: "MALFORMED_FUNCTION_CALL", wantModel: "gemini-2.5-flash",
		},
		{
			name: "oddly typed usage costs those numbers, never the answer",
			body: `{"responseId":42,"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"STOP"}],` +
				`"usageMetadata":{"promptTokenCount":"12","candidatesTokenCount":{"n":1},"thoughtsTokenCount":null,"cachedContentTokenCount":-3}}`,
			wantText: "x", wantFinish: "stop", wantModel: "gemini-2.5-flash",
			wantUsage: aiprov.TokenUsage{Prompt: 12},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(tc.body))
			res, err := newAt(srv.URL, Config{}).Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
			require.NoError(t, err)
			require.Equal(t, tc.wantText, res.Text)
			require.Equal(t, tc.wantFinish, res.FinishReason)
			require.Equal(t, tc.wantModel, res.Model)
			require.Equal(t, tc.wantID, res.RequestID)
			require.Equal(t, tc.wantUsage, res.Usage)
			require.Equal(t, "google", res.Provider)
			require.True(t, res.Engaged, "an answer came back: the request was written and served")
			require.False(t, res.CostUSD.Valid, "Google reports no charge: NULL, never a zero")
		})
	}
}

// TestFinishReasonWords — the mapping table, row by row. UNVERIFIED (G-05): Google's words.
//
// MUTATION: RECITATION dropped from the content_filter row → red.
func TestFinishReasonWords(t *testing.T) {
	for in, want := range map[string]string{
		"STOP": "stop", "MAX_TOKENS": "length", "SAFETY": "content_filter", "RECITATION": "content_filter",
		"OTHER": "OTHER", "": "",
	} {
		require.Equal(t, want, finishReason(in), in)
	}
}

// ─── status → CallError ─────────────────────────────────────────────────────────────────────────

// TestStatusMatrix — every non-2xx is a refusal at the gate (NOT engaged), classified by the one
// matrix, plus Google's bad-key 400 as key_rejected. The sentence is "google: API error (HTTP n): "
// + error.message, bounded; 404 carries ErrModelUnavailable.
//
// MUTATIONS (each measured red → green): the keyInvalid override removed from statusError → the
// three bad-key rows; keyInvalid without the details loop → the "details only" row; keyInvalid
// without the INVALID_ARGUMENT fallback → the "message only" row; the override without its
// `status == 400` condition → the "500 carrying the mark" row turns into a terminal key_rejected;
// 404 without the ErrModelUnavailable wrap → the 404 row; statusError marks Engaged → every row.
func TestStatusMatrix(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		code      string
		retryable bool
		sentence  string
	}{
		{"400 API_KEY_INVALID", 400, badKeyBody, aiprov.CodeKeyRejected, false,
			"google: API error (HTTP 400): API key not valid. Please pass a valid API key."},
		{"400 details only", 400, `{"error":{"code":400,"message":"nope","details":[{"reason":"API_KEY_INVALID"}]}}`,
			aiprov.CodeKeyRejected, false, "google: API error (HTTP 400): nope"},
		{"400 message only", 400, `{"error":{"code":400,"message":"API key expired. Please renew the API key.","status":"INVALID_ARGUMENT"}}`,
			aiprov.CodeKeyRejected, false, "google: API error (HTTP 400): API key expired. Please renew the API key."},
		{"another 400", 400, `{"error":{"code":400,"message":"Invalid JSON payload received.","status":"INVALID_ARGUMENT"}}`,
			aiprov.CodeBadRequest, false, "google: API error (HTTP 400): Invalid JSON payload received."},
		{"401", 401, `{"error":{"message":"no"}}`, aiprov.CodeKeyRejected, false, "google: API error (HTTP 401): no"},
		{"402", 402, `{"error":{"message":"no"}}`, aiprov.CodeOutOfCredits, false, "google: API error (HTTP 402): no"},
		{"403 PERMISSION_DENIED", 403, `{"error":{"code":403,"message":"Permission denied","status":"PERMISSION_DENIED"}}`,
			aiprov.CodeKeyRejected, false, "google: API error (HTTP 403): Permission denied"},
		{"404", 404, `{"error":{"code":404,"message":"models/gemini-9 is not found","status":"NOT_FOUND"}}`, aiprov.CodeModelUnknown, false,
			"google: the configured model is not available at the provider: API error (HTTP 404): models/gemini-9 is not found"},
		{"408", 408, `{"error":{"message":"no"}}`, aiprov.CodeProviderError, true, "google: API error (HTTP 408): no"},
		{"422", 422, `{"error":{"message":"no"}}`, aiprov.CodeBadRequest, false, "google: API error (HTTP 422): no"},
		{"429 RESOURCE_EXHAUSTED", 429, `{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED"}}`,
			aiprov.CodeRateLimited, true, "google: API error (HTTP 429): Quota exceeded"},
		{"500", 500, `{"error":{"message":"no"}}`, aiprov.CodeProviderError, true, "google: API error (HTTP 500): no"},
		{"503", 503, `{"error":{"message":"The model is overloaded."}}`, aiprov.CodeProviderError, true,
			"google: API error (HTTP 503): The model is overloaded."},
		{"not json", 502, "<html>bad gateway</html>", aiprov.CodeProviderError, true, "google: API error (HTTP 502): <html>bad gateway</html>"},
		{"500 carrying the mark stays weather", 500, badKeyBody, aiprov.CodeProviderError, true,
			"google: API error (HTTP 500): API key not valid. Please pass a valid API key."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, status(tc.status, tc.body))
			res, err := newAt(srv.URL, Config{}).Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
			require.Nil(t, res)
			ce := callErr(t, err)
			require.Equal(t, "google", ce.Provider)
			require.Equal(t, tc.code, ce.Code)
			require.Equal(t, tc.status, ce.HTTPStatus)
			require.Equal(t, tc.retryable, ce.Retryable)
			require.False(t, ce.Engaged, "HTTP %d is a refusal at the gate, not a charge", tc.status)
			require.False(t, aiprov.Engaged(err))
			require.Equal(t, tc.sentence, err.Error())
			if tc.status == http.StatusNotFound {
				require.ErrorIs(t, err, aiprov.ErrModelUnavailable)
			} else {
				require.NotErrorIs(t, err, aiprov.ErrModelUnavailable)
			}
		})
	}
}

// TestTheProviderWordsAreBoundedAndKeyless — the provider's message reaches the sentence as ONE line,
// at most 300 runes, and a key it quotes back is replaced.
//
// MUTATION: bounded() without the ReplaceAll → red (the key reaches the sentence); without the
// truncate → red (length).
func TestTheProviderWordsAreBoundedAndKeyless(t *testing.T) {
	const k = "AIzaFakeKeyForTests0000"
	body := `{"error":{"code":400,"message":"key ` + k + ` refused\n` + strings.Repeat("я", 400) + `","status":"FAILED_PRECONDITION"}}`
	rec := &recorder{}
	srv := rec.server(t, status(400, body))
	_, err := newAt(srv.URL, Config{KeyFunc: key(k)}).Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeBadRequest, ce.Code)
	require.NotContains(t, err.Error(), k)
	require.NotContains(t, err.Error(), "\n")
	require.True(t, strings.HasPrefix(err.Error(), "google: API error (HTTP 400): key [key] refused я"), err.Error())
	msg := strings.TrimPrefix(err.Error(), "google: API error (HTTP 400): ")
	require.Equal(t, maxErrorMessage+1, len([]rune(msg)), "300 runes and the ellipsis")
}

// TestARedirectIsRefused — a 3xx is the answer, never followed: x-goog-api-key would travel to the
// Location's host. Not engaged (a gate, not a charge), not retryable.
//
// MUTATION: New without CheckRedirect → red: the second host receives the key.
func TestARedirectIsRefused(t *testing.T) {
	elsewhere := &recorder{}
	thief := elsewhere.server(t, answer(okBody))
	rec := &recorder{}
	srv := rec.server(t, func(w http.ResponseWriter) {
		w.Header().Set("Location", thief.URL+"/steal")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	_, err := newAt(srv.URL, Config{}).Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	ce := callErr(t, err)
	require.Zero(t, elsewhere.count(), "the key followed a redirect")
	require.Equal(t, http.StatusTemporaryRedirect, ce.HTTPStatus)
	require.Equal(t, aiprov.CodeProviderError, ce.Code)
	require.False(t, ce.Engaged)
	require.False(t, ce.Retryable)
}

// ─── the engaged boundary on the wire ───────────────────────────────────────────────────────────

func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return "http://" + addr
}

// hangingProvider RECEIVES the whole request and never answers until released or the client leaves.
func hangingProvider(t *testing.T) (url string, reached <-chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	got := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(got) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv.URL, got
}

// TestAWrittenRequestThatTimesOutIsEngaged — POST written, the budget expires: Google is billing
// right now. Engaged, NOT retryable, Code timeout (the router's D-16 door reads exactly this).
//
// MUTATION: Engaged `false` instead of wroteRequest() on the Do branch → red.
func TestAWrittenRequestThatTimesOutIsEngaged(t *testing.T) {
	url, reached := hangingProvider(t)
	c := newAt(url, Config{HTTPTimeout: 200 * time.Millisecond})
	start := time.Now()
	res, err := c.Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	require.Less(t, time.Since(start), 5*time.Second, "the call must end on its own budget")
	select {
	case <-reached:
	default:
		t.Fatal("the provider never got the request — the test measures something else")
	}
	require.Nil(t, res)
	ce := callErr(t, err)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable, "an engaged failure is never fed to the breaker as weather")
	require.Equal(t, aiprov.CodeTimeout, ce.Code)
	require.Zero(t, ce.HTTPStatus)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, strings.HasPrefix(err.Error(), "google: request failed: "), err.Error())
}

// TestAWrittenRequestTheCallerCancelledIsEngaged — a closed tab after the POST left: still paid, and
// "canceled", never "transport".
//
// MUTATION: the Do branch's Code hard-coded to CodeTransport instead of aiprov.Interruption → red.
func TestAWrittenRequestTheCallerCancelledIsEngaged(t *testing.T) {
	url, reached := hangingProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-reached
		cancel()
	}()
	_, err := newAt(url, Config{HTTPTimeout: 10 * time.Second}).Chat(ctx, "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	ce := callErr(t, err)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.Equal(t, aiprov.CodeCanceled, ce.Code)
}

// TestARefusedConnectionIsNotEngagedAndRetryable — nothing written, nobody pays: the router may fall
// back and the breaker counts it.
//
// MUTATION: Engaged true on the Do branch → red; Retryable false on the Do branch → red.
func TestARefusedConnectionIsNotEngagedAndRetryable(t *testing.T) {
	_, err := newAt(deadAddr(t), Config{}).Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	ce := callErr(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded, "a dead address must refuse, not time out")
	require.False(t, ce.Engaged)
	require.True(t, ce.Retryable)
	require.Equal(t, aiprov.CodeTransport, ce.Code)
}

// TestACallerWhoLeftBeforeTheWriteIsNotRetryable — the caller's context was already cancelled:
// nothing sent, nothing learnt about the provider, so the breaker must not count it.
//
// MUTATION: `retryable := !engaged` (drop the Canceled exception) → red.
func TestACallerWhoLeftBeforeTheWriteIsNotRetryable(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newAt(srv.URL, Config{}).Chat(ctx, "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	ce := callErr(t, err)
	require.False(t, ce.Engaged)
	require.False(t, ce.Retryable, "a closed tab is not a provider fault")
	require.Equal(t, aiprov.CodeCanceled, ce.Code)
	require.Zero(t, rec.count())
}

// TestAnUnusable2xxIsEngaged — Google accepted the request and did the work: whatever is wrong with
// the envelope is ours to carry, and the money moved. Empty answers come back WITH the partial.
//
// MUTATIONS (each measured red → green): the no-candidates branch returns a nil result → the
// blocked row; the MAX_TOKENS split removed → the budget row reads empty_answer; the broken-envelope
// branch not engaged → red; blocked() not appended → the blocked row's sentence.
func TestAnUnusable2xxIsEngaged(t *testing.T) {
	usage := `"usageMetadata":{"promptTokenCount":10149,"candidatesTokenCount":0,"thoughtsTokenCount":2500}`
	cases := []struct {
		name        string
		body        string
		code        string
		sentence    string
		sentinel    error
		wantPartial bool
		wantUsage   aiprov.TokenUsage
	}{
		{"broken envelope", `{"candidates":`, aiprov.CodeProviderError, "google: could not decode API response envelope: ", nil, false, aiprov.TokenUsage{}},
		{"error envelope", `{"error":{"message":"upstream died"}}`, aiprov.CodeProviderError, "google: API error: upstream died", nil, false, aiprov.TokenUsage{}},
		{"prompt blocked, no candidates",
			`{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":42},"modelVersion":"gemini-2.5-flash-001","responseId":"r-1"}`,
			aiprov.CodeEmptyAnswer, "google: API response contained no candidates (prompt blocked: SAFETY)", nil, true,
			aiprov.TokenUsage{Prompt: 42}},
		{"no candidates", `{"candidates":[],"modelVersion":"gemini-2.5-flash-001"}`,
			aiprov.CodeEmptyAnswer, "google: API response contained no candidates", nil, true, aiprov.TokenUsage{}},
		{"empty text, safety stop",
			`{"candidates":[{"content":{"parts":[{"text":"  "}]},"finishReason":"SAFETY"}],"modelVersion":"gemini-2.5-flash-001",` + usage + `}`,
			aiprov.CodeEmptyAnswer, "google: model returned an empty message (finish reason content_filter)", nil, true,
			aiprov.TokenUsage{Prompt: 10149, Completion: 2500, Reasoning: 2500}},
		{"thinking spent the ceiling",
			`{"candidates":[{"content":{"role":"model"},"finishReason":"MAX_TOKENS"}],"modelVersion":"gemini-2.5-flash-001",` + usage + `}`,
			aiprov.CodeBudgetExhausted, "google: the model spent the whole completion budget without answering (2500 completion tokens spent, none of them answer)",
			aiprov.ErrBudgetExhausted, true, aiprov.TokenUsage{Prompt: 10149, Completion: 2500, Reasoning: 2500}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(tc.body))
			res, err := newAt(srv.URL, Config{}).Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u", MaxTokens: 2500})
			ce := callErr(t, err)
			require.True(t, ce.Engaged, "a 2xx means the request was accepted and billed")
			require.True(t, aiprov.Engaged(err))
			require.False(t, ce.Retryable)
			require.Equal(t, tc.code, ce.Code)
			require.Equal(t, http.StatusOK, ce.HTTPStatus)
			require.True(t, strings.HasPrefix(err.Error(), tc.sentence), "%q", err.Error())
			require.NotContains(t, err.Error(), "\n")
			if tc.sentinel != nil {
				require.ErrorIs(t, err, tc.sentinel)
			}
			if !tc.wantPartial {
				require.Nil(t, res)
				return
			}
			require.NotNil(t, res, "an empty answer was paid for: its size must reach the caller")
			require.Empty(t, res.Text)
			require.Equal(t, tc.wantUsage, res.Usage)
			require.Equal(t, "gemini-2.5-flash-001", res.Model)
			require.True(t, res.Engaged)
		})
	}
}

// ─── budget and ceilings ────────────────────────────────────────────────────────────────────────

// TestTheCeilingBuysItsOwnTime — the same slow provider, the same tiny base; only the ceiling
// differs. Without one the call is cut at the base; with 8000 tokens the printing time is added.
//
// MUTATION: the budget from c.budgetBase instead of aiprov.CompletionBudget(c.budgetBase, ceiling)
// → the second half goes red.
func TestTheCeilingBuysItsOwnTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, okBody)
	}))
	t.Cleanup(srv.Close)
	c := newAt(srv.URL, Config{HTTPTimeout: 40 * time.Millisecond})

	_, err := c.Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	require.Equal(t, aiprov.CodeTimeout, callErr(t, err).Code, "no ceiling: the budget is the base")

	res, err := c.Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u", MaxTokens: 8000})
	require.NoError(t, err, "the ceiling must buy its own printing time")
	require.Equal(t, "ok", res.Text)
}

// TestPictureReadsShareTheCallBudget — a bucket that hangs cannot stretch the call past its budget:
// the read ends on the same deadline, as our bad request, before the wire.
//
// MUTATION: the WithTimeout moved from Chat into post (after the pictures) → red: the call hangs
// until the test's own 5 s guard.
func TestPictureReadsShareTheCallBudget(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	objects := &fakeObjects{block: true}
	c := newAt(srv.URL, Config{HTTPTimeout: 50 * time.Millisecond, Objects: objects, KeyFromURL: managedKey})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := c.Chat(ctx, "gemini-2.5-flash", aiprov.ChatRequest{User: "u", ImageURLs: []string{"https://files.grbpwr.example/base/a.png"}})
	require.Less(t, time.Since(start), 2*time.Second)
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeBadRequest, ce.Code)
	require.False(t, ce.Engaged)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, rec.count())
}

// TestCompletionBase — the base the wire uses is the base a lease must be derived from; nil-safe.
func TestCompletionBase(t *testing.T) {
	require.Equal(t, aiprov.DefaultBudgetBase, New(Config{}).CompletionBase())
	require.Equal(t, 5*time.Second, New(Config{HTTPTimeout: 5 * time.Second}).CompletionBase())
	var nilClient *Client
	require.Equal(t, aiprov.DefaultBudgetBase, nilClient.CompletionBase())
	require.False(t, nilClient.Enabled())
	require.Empty(t, nilClient.BaseURL())
	require.False(t, New(Config{}).Enabled())
}

// TestTheReadCeilingRefusesByName — exactly at the ceiling works; one byte over is refused as
// ErrResponseTooLarge, engaged (the answer WAS there).
//
// MUTATION: readCapped reads `limit` instead of `limit+1` → the second half goes red.
func TestTheReadCeilingRefusesByName(t *testing.T) {
	atCeiling := okBody + strings.Repeat(" ", MaxResponseBytes-len(okBody))
	rec := &recorder{}
	srv := rec.server(t, answer(atCeiling))
	res, err := newAt(srv.URL, Config{}).Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	require.NoError(t, err, "a body exactly at the ceiling must be accepted")
	require.Equal(t, "ok", res.Text)

	srv = rec.server(t, answer(atCeiling+" "))
	_, err = newAt(srv.URL, Config{}).Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	ce := callErr(t, err)
	require.ErrorIs(t, err, aiprov.ErrResponseTooLarge)
	require.Equal(t, aiprov.CodeTooLarge, ce.Code)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.Equal(t, http.StatusOK, ce.HTTPStatus)
	require.Equal(t, fmt.Sprintf("google: read response: google: the provider's response exceeded the read ceiling: "+
		"generateContent response is larger than %d bytes", MaxResponseBytes), err.Error())
}

// TestARefusedStatusWithAnUnreadableBodyIsStillARefusal — a 400 bad-key or a 404 whose excuse runs
// past the read ceiling is still the refusal at the gate: not engaged, its code and sentinel intact.
//
// MUTATION: judge the body before the status → red: Engaged true, Code too_large.
func TestARefusedStatusWithAnUnreadableBodyIsStillARefusal(t *testing.T) {
	rec := &recorder{}
	huge := `{"error":{"message":"` + strings.Repeat("x", MaxResponseBytes+16) + `"}}`
	srv := rec.server(t, status(http.StatusNotFound, huge))
	_, err := newAt(srv.URL, Config{}).Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	ce := callErr(t, err)
	require.ErrorIs(t, err, aiprov.ErrModelUnavailable)
	require.Equal(t, aiprov.CodeModelUnknown, ce.Code)
	require.False(t, ce.Engaged)
	require.NotErrorIs(t, err, aiprov.ErrResponseTooLarge)
	require.Contains(t, err.Error(), "API error (HTTP 404): response body unavailable: google: the provider's response exceeded the read ceiling")
}

// ─── pictures: bytes, ours, bounded ─────────────────────────────────────────────────────────────

// TestPicturesOverTheCeilingNeverReachTheWire — every way past MaxInlineBytes is refused as
// CodeTooLarge, NOT engaged, before any request: one object whose size says so (its body never
// read), two that fit alone but not together, a size that lies, a data URI that tips the total.
//
// MUTATIONS (each measured red → green): the Stat-size check removed → the "size says so" row (its
// one-byte body fits, so only the Stat size can refuse it) reaches the wire; room computed as
// MaxInlineBytes instead of what is left of it → the "two together" row reaches the wire; the
// `> room` check after the read removed → the "size lies" row reaches the wire; decodeDataURI given
// MaxInlineBytes instead of room → the "data URI tips the total" row reaches the wire.
func TestPicturesOverTheCeilingNeverReachTheWire(t *testing.T) {
	half := bytes.Repeat([]byte{1}, MaxInlineBytes/2+1)
	nearly := bytes.Repeat([]byte{1}, MaxInlineBytes-2)
	cases := []struct {
		name    string
		objects map[string][]byte
		sizes   map[string]int64
		urls    []string
		picture int
	}{
		{
			name:    "size says so",
			objects: map[string][]byte{"base/big.png": {1}},
			sizes:   map[string]int64{"base/big.png": MaxInlineBytes + 1},
			urls:    []string{"https://files.grbpwr.example/base/big.png"},
			picture: 1,
		},
		{
			name:    "two that fit alone but not together",
			objects: map[string][]byte{"base/a.png": half, "base/b.webp": half},
			urls:    []string{"https://files.grbpwr.example/base/a.png", "https://grbpwr.fra1.digitaloceanspaces.com/base/b.webp"},
			picture: 2,
		},
		{
			name:    "the size lies",
			objects: map[string][]byte{"base/liar.jpg": bytes.Repeat([]byte{1}, MaxInlineBytes+1)},
			sizes:   map[string]int64{"base/liar.jpg": 10},
			urls:    []string{"https://files.grbpwr.example/base/liar.jpg"},
			picture: 1,
		},
		{
			name:    "a data URI tips the total",
			objects: map[string][]byte{"base/nearly.png": nearly},
			urls:    []string{"https://files.grbpwr.example/base/nearly.png", "data:image/png;base64,+/+/"},
			picture: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(okBody))
			objects := &fakeObjects{objects: tc.objects, sizes: tc.sizes}
			res, err := newAt(srv.URL, Config{Objects: objects, KeyFromURL: managedKey}).Chat(context.Background(), "gemini-2.5-flash",
				aiprov.ChatRequest{User: "u", ImageURLs: tc.urls})
			require.Nil(t, res)
			ce := callErr(t, err)
			require.Equal(t, aiprov.CodeTooLarge, ce.Code)
			require.False(t, ce.Engaged, "nothing was sent")
			require.False(t, ce.Retryable, "the same pictures will not shrink")
			require.Zero(t, ce.HTTPStatus)
			require.Equal(t, fmt.Sprintf("google: picture %d takes the pictures past the %d-byte inline ceiling; nothing was sent",
				tc.picture, MaxInlineBytes), err.Error())
			require.Zero(t, rec.count(), "an oversized request reached the provider")
		})
	}
}

// TestAPictureExactlyAtTheCeilingIsSent — the ceiling is inclusive: MaxInlineBytes in total is sent.
//
// MUTATION: `size > room` → `size >= room` → red.
func TestAPictureExactlyAtTheCeilingIsSent(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	objects := &fakeObjects{objects: map[string][]byte{"base/full.png": bytes.Repeat([]byte{1}, MaxInlineBytes)}}
	_, err := newAt(srv.URL, Config{Objects: objects, KeyFromURL: managedKey}).Chat(context.Background(), "gemini-2.5-flash",
		aiprov.ChatRequest{User: "u", ImageURLs: []string{"https://files.grbpwr.example/base/full.png"}})
	require.NoError(t, err)
	require.Equal(t, 1, rec.count())
}

// TestRefusalsBeforeTheWire — every case is OUR mistake, our missing key or a picture we cannot
// vouch for; none reaches Google, none is engaged, none is weather, and the bucket is asked only for
// a picture that passed the host and type checks.
//
// MUTATIONS (each measured red → green): the host check removed from bucket.ManagedObjectKeyFromURL
// (the function E3 wires as KeyFromURL) → the "foreign host" and "lookalike host" rows ask the bucket
// and reach the wire; the extension check removed → the "svg" row asks the bucket; the MaxImageParts
// check removed → the 17-picture row reaches the wire; the slug pattern removed → the "path in the
// slug" and "OpenRouter slug" rows reach the wire.
func TestRefusalsBeforeTheWire(t *testing.T) {
	tooMany := make([]string, MaxImageParts+1)
	for i := range tooMany {
		tooMany[i] = "data:image/png;base64,+/+/"
	}
	cases := []struct {
		name      string
		cfg       *Config // nil = the full config
		nilClient bool
		model     string
		req       aiprov.ChatRequest
		code      string
		want      string
	}{
		{name: "no key", cfg: &Config{KeyFunc: key("  ")}, model: "gemini-2.5-flash", req: aiprov.ChatRequest{User: "u"},
			code: aiprov.CodeNotConfigured, want: "google: no API key is set"},
		{name: "nil client", nilClient: true, model: "gemini-2.5-flash", req: aiprov.ChatRequest{User: "u"},
			code: aiprov.CodeNotConfigured, want: "google: no API key is set"},
		{name: "empty model", model: "  ", req: aiprov.ChatRequest{User: "u"},
			code: aiprov.CodeBadRequest, want: "google: a completion needs a model slug"},
		{name: "path in the slug", model: "gemini-2.5-flash:generateContent?alt=sse#", req: aiprov.ChatRequest{User: "u"},
			code: aiprov.CodeBadRequest, want: `google: model slug "gemini-2.5-flash:generateContent?alt=sse#" is not a bare Gemini model name`},
		{name: "an OpenRouter slug", model: "google/gemini-2.5-flash", req: aiprov.ChatRequest{User: "u"},
			code: aiprov.CodeBadRequest, want: `google: model slug "google/gemini-2.5-flash" is not a bare Gemini model name`},
		{name: "blank prompt", model: "gemini-2.5-flash", req: aiprov.ChatRequest{System: "s", User: "  "},
			code: aiprov.CodeBadRequest, want: "google: a request needs a prompt, pictures alone say nothing"},
		{name: "too many pictures", model: "gemini-2.5-flash", req: aiprov.ChatRequest{User: "u", ImageURLs: tooMany},
			code: aiprov.CodeBadRequest, want: "google: 17 pictures exceeds the 16-picture limit for one request"},
		{name: "foreign host", model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{User: "u", ImageURLs: []string{"https://attacker.example/base/f/look.png"}},
			code: aiprov.CodeBadRequest, want: `google: picture 1 is not our media: media url host "attacker.example" is not a configured bucket host`},
		{name: "a lookalike host", model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{User: "u", ImageURLs: []string{"https://files.grbpwr.example.attacker.invalid/base/look.png"}},
			code: aiprov.CodeBadRequest, want: "google: picture 1 is not our media: "},
		{name: "plain http", model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{User: "u", ImageURLs: []string{"http://files.grbpwr.example/base/look.png"}},
			code: aiprov.CodeBadRequest, want: `google: picture 1: address must be our https media or a data:image/… URI, got "http://files.grbpwr.example/`},
		{name: "file url", model: "gemini-2.5-flash", req: aiprov.ChatRequest{User: "u", ImageURLs: []string{"file:///etc/passwd"}},
			code: aiprov.CodeBadRequest, want: "google: picture 1: address must be our https media"},
		{name: "empty address", model: "gemini-2.5-flash", req: aiprov.ChatRequest{User: "u", ImageURLs: []string{"data:image/png;base64,+/+/", " "}},
			code: aiprov.CodeBadRequest, want: "google: picture 2: empty picture address"},
		{name: "svg is not a picture we inline", model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{User: "u", ImageURLs: []string{"https://files.grbpwr.example/base/logo.svg"}},
			code: aiprov.CodeBadRequest, want: `google: picture 1: "logo.svg" is not a png, jpeg, webp or gif`},
		{name: "no reader configured", cfg: &Config{KeyFunc: key("k")}, model: "gemini-2.5-flash",
			req:  aiprov.ChatRequest{User: "u", ImageURLs: []string{"https://files.grbpwr.example/base/look.png"}},
			code: aiprov.CodeBadRequest, want: "google: picture 1: no media reader is configured for https pictures"},
		{name: "data URI without payload", model: "gemini-2.5-flash", req: aiprov.ChatRequest{User: "u", ImageURLs: []string{"data:image/png;base64,"}},
			code: aiprov.CodeBadRequest, want: "google: picture 1: data URI carries no payload"},
		{name: "data URI not base64", model: "gemini-2.5-flash", req: aiprov.ChatRequest{User: "u", ImageURLs: []string{"data:image/png,rawbytes"}},
			code: aiprov.CodeBadRequest, want: "google: picture 1: data URI is not base64"},
		{name: "data URI bad base64", model: "gemini-2.5-flash", req: aiprov.ChatRequest{User: "u", ImageURLs: []string{"data:image/png;base64,%%%%"}},
			code: aiprov.CodeBadRequest, want: "google: picture 1: data URI payload is not valid base64"},
		{name: "data URI of another type", model: "gemini-2.5-flash", req: aiprov.ChatRequest{User: "u", ImageURLs: []string{"data:image/svg+xml;base64,PHN2Zy8+"}},
			code: aiprov.CodeBadRequest, want: `google: picture 1: data URI type "image/svg+xml" is not a png, jpeg, webp or gif`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(okBody))
			objects := &fakeObjects{objects: map[string][]byte{
				"base/f/look.png": picBytes, "base/look.png": picBytes, "base/logo.svg": []byte("<svg/>"),
			}}
			var c *Client
			switch {
			case tc.nilClient:
			case tc.cfg != nil:
				c = newAt(srv.URL, *tc.cfg)
			default:
				c = newAt(srv.URL, Config{Objects: objects, KeyFromURL: managedKey})
			}
			res, err := c.Chat(context.Background(), tc.model, tc.req)
			require.Nil(t, res)
			ce := callErr(t, err)
			require.Equal(t, "google", ce.Provider)
			require.Equal(t, tc.code, ce.Code)
			require.False(t, ce.Engaged)
			require.False(t, ce.Retryable, "our own mistake does not improve by being sent again")
			require.Zero(t, ce.HTTPStatus)
			require.True(t, strings.HasPrefix(err.Error(), tc.want), "%q", err.Error())
			if tc.code == aiprov.CodeNotConfigured {
				require.ErrorIs(t, err, aiprov.ErrNotConfigured)
			}
			require.Empty(t, objects.askedKeys(), "the bucket was asked for a picture that was refused first")
			require.Zero(t, rec.count(), "a refused request reached the provider")
		})
	}
}

// TestAPictureTheBucketCannotReadIsOurs — a bucket failure is CodeBadRequest, not engaged, not
// retryable (it must not feed GOOGLE's breaker), and nothing is sent.
//
// MUTATION: the GetManagedObject error mapped retryable → red.
func TestAPictureTheBucketCannotReadIsOurs(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	gone := errors.New("the specified key does not exist")
	objects := &fakeObjects{err: gone}
	_, err := newAt(srv.URL, Config{Objects: objects, KeyFromURL: managedKey}).Chat(context.Background(), "gemini-2.5-flash",
		aiprov.ChatRequest{User: "u", ImageURLs: []string{"data:image/png;base64,+/+/", "https://files.grbpwr.example/base/f/2026/look.webp"}})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeBadRequest, ce.Code)
	require.False(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.ErrorIs(t, err, gone)
	require.Equal(t, "google: picture 2 could not be read: the specified key does not exist", err.Error())
	require.Equal(t, []string{"base/f/2026/look.webp"}, objects.askedKeys(), "the key is the host-checked path")
	require.Zero(t, rec.count())
}

// TestChatIsTheChatterSeam — through the interface the router holds.
func TestChatIsTheChatterSeam(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(`{"responseId":"r-9","candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	var chatter aiprov.Chatter = newAt(srv.URL, Config{})
	res, err := chatter.Chat(context.Background(), "gemini-2.5-flash", aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	require.Equal(t, &aiprov.ChatResult{Text: "ok", FinishReason: "stop", Provider: "google", Model: "gemini-2.5-flash", RequestID: "r-9", Engaged: true}, res)
}
