package anthropic

import (
	"context"
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
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/oaichat"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// contentIsStillAString is a COMPILE-TIME assertion (oaichat's, for this transport's turn type): the
// tempting edit is to retype textTurn.Content from string to `any`; it compiles everywhere and turns
// every text request into a runtime shape nobody checks. With this line it stops being valid Go.
var contentIsStillAString string = textTurn{}.Content

// ─── fixtures ────────────────────────────────────────────────────────────────────────────────────
//
// ⚠ UNVERIFIED (G-05). Every response body below is written from the Anthropic documentation bundled
// with the tooling (the claude-api reference, cached 2026-06-24), not captured from a live call:
// the envelope tags (id, type, role, model, content[].type/.text, stop_reason, stop_sequence,
// usage.input_tokens / output_tokens / cache_creation_input_tokens / cache_read_input_tokens), the
// stop_reason words (end_turn, stop_sequence, max_tokens, refusal, pause_turn), the error envelope
// ({"type":"error","error":{"type","message"},"request_id"}) and its statuses (402 billing_error,
// 413 request_too_large, 529 overloaded_error). G-05 replaces them with a recorded answer.

const okBody = `{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-5",` +
	`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,` +
	`"usage":{"input_tokens":10,"output_tokens":2}}`

// okJSONBody answers with an object, so a JSON-mode request succeeds as well as a plain one.
const okJSONBody = `{"id":"msg_02","type":"message","role":"assistant","model":"claude-sonnet-5",` +
	`"content":[{"type":"text","text":"{\"ok\":true}"}],"stop_reason":"end_turn","stop_sequence":null,` +
	`"usage":{"input_tokens":10,"output_tokens":4}}`

func errorBody(msg string) string {
	return `{"type":"error","error":{"type":"some_error","message":"` + msg + `"},"request_id":"req_01"}`
}

// recorder is a fake provider that records every request and answers with reply.
type recorder struct {
	mu      sync.Mutex
	bodies  []string
	headers []http.Header
	paths   []string
}

func (rec *recorder) server(t *testing.T, reply func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, string(b))
		rec.headers = append(rec.headers, r.Header.Clone())
		rec.paths = append(rec.paths, r.URL.Path)
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

func key(k string) func() string { return func() string { return k } }

// newAt is a client pointed at a stand instead of endpoints.AnthropicAPIBase.
func newAt(url string, base time.Duration) *Client {
	c := New(Config{KeyFunc: key("k"), HTTPTimeout: base})
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

// TestRequestBytes pins the EXACT body per request shape. Golden strings, not field checks: any
// reordering, new key or changed value is a new request to a paid provider. No row carries
// `temperature` — see messagesRequest for why that is a decision.
//
// MUTATIONS (each measured red → restored green): `Temperature float64 json:"temperature"` = 0.2 added
// to messagesRequest → every row; the DefaultMaxTokens fallback removed (0 sent) → the no-ceiling rows;
// the JSON instruction not appended → the two JSON rows; UserAsParts ignored → the "parts, no
// pictures" row; a data: URI sent as a url source → the pictures row; the text block placed after the
// pictures → the pictures row; Effort mapped to `"thinking":{"type":"disabled"}` → the effort row.
func TestRequestBytes(t *testing.T) {
	const user = `{"role":"user","content":"u"}`
	cases := []struct {
		name  string
		model string
		req   aiprov.ChatRequest
		want  string
	}{
		{
			name: "text, no ceiling: max_tokens is the default", model: "claude-sonnet-5",
			req:  aiprov.ChatRequest{System: "sys", User: "u"},
			want: `{"model":"claude-sonnet-5","max_tokens":4096,"system":"sys","messages":[` + user + `]}`,
		},
		{
			name: "text, the caller's ceiling; effort is not mapped", model: "claude-sonnet-5",
			req:  aiprov.ChatRequest{System: "sys", User: "u", MaxTokens: 900, Effort: "none"},
			want: `{"model":"claude-sonnet-5","max_tokens":900,"system":"sys","messages":[` + user + `]}`,
		},
		{
			name: "no system prompt: the field is omitted", model: "m",
			req:  aiprov.ChatRequest{User: "u"},
			want: `{"model":"m","max_tokens":4096,"messages":[` + user + `]}`,
		},
		{
			name: "negative ceiling is no ceiling", model: "m",
			req:  aiprov.ChatRequest{System: "sys", User: "u", MaxTokens: -5},
			want: `{"model":"m","max_tokens":4096,"system":"sys","messages":[` + user + `]}`,
		},
		{
			name: "JSON mode appends the instruction to the system prompt", model: "m",
			req:  aiprov.ChatRequest{System: "sys", User: "u", JSONMode: true, MaxTokens: 2500, Effort: "minimal"},
			want: `{"model":"m","max_tokens":2500,"system":"sys\n\nAnswer with exactly one JSON object and nothing else.","messages":[` + user + `]}`,
		},
		{
			name: "JSON mode with no system prompt: the instruction is the system prompt", model: "m",
			req:  aiprov.ChatRequest{User: "u", JSONMode: true},
			want: `{"model":"m","max_tokens":4096,"system":"Answer with exactly one JSON object and nothing else.","messages":[` + user + `]}`,
		},
		{
			name: "pictures: text first, a URL by reference, a data URI as base64", model: "m",
			req: aiprov.ChatRequest{System: "sys", User: "u", MaxTokens: 300,
				ImageURLs: []string{" https://x/1.png ", "data:image/png;base64,AAAA"}},
			want: `{"model":"m","max_tokens":300,"system":"sys","messages":[{"role":"user","content":[` +
				`{"type":"text","text":"u"},` +
				`{"type":"image","source":{"type":"url","url":"https://x/1.png"}},` +
				`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`,
		},
		{
			name: "parts with no pictures (UserAsParts)", model: "m",
			req:  aiprov.ChatRequest{System: "sys", User: "u", UserAsParts: true},
			want: `{"model":"m","max_tokens":4096,"system":"sys","messages":[{"role":"user","content":[{"type":"text","text":"u"}]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(okJSONBody))
			_, err := newAt(srv.URL+"/", 2*time.Second).Chat(context.Background(), tc.model, tc.req)
			require.NoError(t, err)
			require.Equal(t, 1, rec.count())
			require.Equal(t, tc.want, rec.bodies[0])
			require.Equal(t, "/v1/messages", rec.paths[0], "one trailing slash of the base is not a second path segment")
		})
	}
}

// TestHeaders — the key travels as x-api-key with the version header; never as a Bearer.
//
// MUTATION: `Authorization: Bearer <key>` in place of x-api-key → red; the anthropic-version line
// removed → red.
func TestHeaders(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	_, err := newAt(srv.URL, 2*time.Second).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	require.NoError(t, err)
	h := rec.headers[0]
	require.Equal(t, "k", h.Get("x-api-key"))
	require.Equal(t, "2023-06-01", h.Get("anthropic-version"))
	require.Equal(t, "application/json", h.Get("Content-Type"))
	require.Empty(t, h.Get("Authorization"), "the key is never sent as a Bearer")
}

// TestKeyFuncIsReadPerRequest — a key saved in the admin panel reaches the NEXT request; "" disables
// the transport before the wire.
//
// MUTATION: cache the key in New → red on the second x-api-key.
func TestKeyFuncIsReadPerRequest(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	var mu sync.Mutex
	current := "key-one"
	c := New(Config{KeyFunc: func() string {
		mu.Lock()
		defer mu.Unlock()
		return current
	}})
	c.base = srv.URL
	set := func(k string) { mu.Lock(); current = k; mu.Unlock() }

	_, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	set("  key-two  ")
	_, err = c.Chat(context.Background(), "m", aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	require.Equal(t, "key-one", rec.headers[0].Get("x-api-key"))
	require.Equal(t, "key-two", rec.headers[1].Get("x-api-key"))

	require.True(t, c.Enabled())
	set("")
	require.False(t, c.Enabled())
	_, err = c.Chat(context.Background(), "m", aiprov.ChatRequest{User: "u"})
	require.ErrorIs(t, err, aiprov.ErrNotConfigured)
	require.Equal(t, 2, rec.count(), "a disabled transport reached the wire")
}

// TestOneKeyPerRequest — KeyFunc is read exactly once per call, and the header carries that read: a
// rotation between two reads inside one request must be impossible.
//
// MUTATION: read the key again for the header (`c.key()` in post) → red: two reads, the header
// carries the second.
func TestOneKeyPerRequest(t *testing.T) {
	reads := 0
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()
	c := New(Config{KeyFunc: func() string {
		reads++
		return fmt.Sprintf("k%d", reads)
	}})
	c.base = srv.URL
	_, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	require.NoError(t, err)
	require.Equal(t, 1, reads, "one read per request")
	require.Equal(t, "k1", seen)
}

// ─── response facts ──────────────────────────────────────────────────────────────────────────────

// TestResponseFacts — text (text blocks only, concatenated), finish reason, reported model, id, the
// usage arithmetic, no price. Asserted NON-ZERO: a misspelled tag leaves zeros and a call that reads as
// free.
//
// MUTATIONS: cache_read not added to Prompt → the cache rows; cache_creation not added → the cache
// rows; Cached read from cache_creation → the cache rows; the `b.Type == "text"` filter removed → the
// "not text" row; stop_sequence not mapped → its row; max_tokens not mapped to "length" → its row;
// usage decoded as a typed envelope field instead of raw JSON → the "oddly typed" row (the whole answer
// lost to one number); mr.Model ignored → the reported-model rows.
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
			name: "everything reported, cache on both sides",
			body: `{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-5-20260801",` +
				`"content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"text","text":"  hel"},{"type":"text","text":"lo  "}],` +
				`"stop_reason":"end_turn","stop_sequence":null,` +
				`"usage":{"input_tokens":200,"cache_creation_input_tokens":300,"cache_read_input_tokens":1000,"output_tokens":80}}`,
			wantText: "hello", wantFinish: "stop", wantModel: "claude-sonnet-5-20260801", wantID: "msg_123",
			wantUsage: aiprov.TokenUsage{Prompt: 1500, Cached: 1000, Completion: 80},
		},
		{
			name:     "a block that is not text never reaches the answer",
			body:     `{"content":[{"type":"not_text","text":"leak"},{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`,
			wantText: "hi", wantFinish: "stop", wantModel: "requested/slug",
			wantUsage: aiprov.TokenUsage{Prompt: 5, Completion: 1},
		},
		{
			name:     "stop_sequence is a stop",
			body:     `{"content":[{"type":"text","text":"hi"}],"stop_reason":"stop_sequence","usage":{"input_tokens":5,"output_tokens":1}}`,
			wantText: "hi", wantFinish: "stop", wantModel: "requested/slug",
			wantUsage: aiprov.TokenUsage{Prompt: 5, Completion: 1},
		},
		{
			name:     "max_tokens with text is a cut answer, delivered as length",
			body:     `{"content":[{"type":"text","text":"hi"}],"stop_reason":"max_tokens","usage":{"input_tokens":5,"output_tokens":4096}}`,
			wantText: "hi", wantFinish: "length", wantModel: "requested/slug",
			wantUsage: aiprov.TokenUsage{Prompt: 5, Completion: 4096},
		},
		{
			name:     "an unknown stop reason passes through",
			body:     `{"content":[{"type":"text","text":"hi"}],"stop_reason":"pause_turn","usage":{"input_tokens":5,"output_tokens":1}}`,
			wantText: "hi", wantFinish: "pause_turn", wantModel: "requested/slug",
			wantUsage: aiprov.TokenUsage{Prompt: 5, Completion: 1},
		},
		{
			name:     "oddly typed id and usage cost those numbers, never the answer",
			body:     `{"id":42,"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":"many"}}`,
			wantText: "hi", wantFinish: "stop", wantModel: "requested/slug",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(tc.body))
			res, err := newAt(srv.URL, 2*time.Second).Chat(context.Background(), "requested/slug", aiprov.ChatRequest{System: "s", User: "u"})
			require.NoError(t, err)
			require.Equal(t, tc.wantText, res.Text)
			require.Equal(t, tc.wantFinish, res.FinishReason)
			require.Equal(t, tc.wantModel, res.Model)
			require.Equal(t, tc.wantID, res.RequestID)
			require.Equal(t, tc.wantUsage, res.Usage)
			require.Equal(t, "anthropic", res.Provider)
			require.True(t, res.Engaged, "an answer came back: the request was written and served")
			require.False(t, res.CostUSD.Valid, "the Messages API reports no charge: NULL, never zero")
		})
	}
}

// TestJSONModeCutsTheObject — the answer is the object, without the fence or the sentence around it.
//
// MUTATION: the ExtractJSONObject step skipped → red (the fenced text reaches the caller).
func TestJSONModeCutsTheObject(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(`{"content":[{"type":"text","text":"Here it is:\n`+"```json\\n"+`{\"a\":{\"b\":1}}\n`+"```"+`"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":9}}`))
	res, err := newAt(srv.URL, 2*time.Second).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u", JSONMode: true})
	require.NoError(t, err)
	require.Equal(t, `{"a":{"b":1}}`, res.Text)
}

// ─── status → CallError ─────────────────────────────────────────────────────────────────────────

// TestStatusMatrix — every non-2xx is a refusal at the gate (NOT engaged), classified by status alone
// through aiprov.ClassifyStatus, with the house sentence. 529 (overloaded) is weather.
//
// MUTATIONS: statusError marks Engaged → every row; the ErrModelUnavailable wrap removed from the 404
// → the 404 row; statusError passes retryable=false → the 408/429/5xx/529 rows; the error message read
// from the raw body instead of error.message → every row with a body.
func TestStatusMatrix(t *testing.T) {
	cases := []struct {
		status    int
		code      string
		retryable bool
	}{
		{400, aiprov.CodeBadRequest, false},
		{401, aiprov.CodeKeyRejected, false},
		{402, aiprov.CodeOutOfCredits, false},
		{403, aiprov.CodeKeyRejected, false},
		{404, aiprov.CodeModelUnknown, false},
		{408, aiprov.CodeProviderError, true},
		{413, aiprov.CodeBadRequest, false},
		{422, aiprov.CodeBadRequest, false},
		{429, aiprov.CodeRateLimited, true},
		{500, aiprov.CodeProviderError, true},
		{503, aiprov.CodeProviderError, true},
		{529, aiprov.CodeProviderError, true},
		{304, aiprov.CodeProviderError, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, errorBody("no"))
			})
			res, err := newAt(srv.URL, 2*time.Second).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
			require.Nil(t, res)
			ce := callErr(t, err)
			require.Equal(t, "anthropic", ce.Provider)
			require.Equal(t, tc.code, ce.Code)
			require.Equal(t, tc.status, ce.HTTPStatus)
			require.Equal(t, tc.retryable, ce.Retryable)
			require.False(t, ce.Engaged, "HTTP %d is a refusal at the gate, not a charge", tc.status)
			require.False(t, aiprov.Engaged(err))

			if tc.status == http.StatusNotFound {
				require.ErrorIs(t, err, aiprov.ErrModelUnavailable)
				require.Equal(t, "anthropic: the configured model is not available at the provider: API error (HTTP 404): no", err.Error())
				return
			}
			require.NotErrorIs(t, err, aiprov.ErrModelUnavailable)
			if tc.status == http.StatusNotModified {
				require.Equal(t, "anthropic: API error (HTTP 304): ", err.Error(), "a 304 carries no body")
				return
			}
			require.Equal(t, fmt.Sprintf("anthropic: API error (HTTP %d): no", tc.status), err.Error())
		})
	}
}

// TestTheProviderTextIsBoundedAndNeverTheKey — a gateway that echoes the key in a long error page
// puts neither the key nor the page into the sentence.
//
// MUTATIONS: the key scrub removed from boundedMessage → red; the truncate removed → red; truncate
// BEFORE the scrub with the key straddling the cut → red (a key prefix survives).
func TestTheProviderTextIsBoundedAndNeverTheKey(t *testing.T) {
	const secret = "sk-ant-api03-SECRET-KEY-VALUE"
	for _, tc := range []struct {
		name string
		body string
	}{
		{"error.message", errorBody("invalid x-api-key " + secret + " " + strings.Repeat("é", 1000))},
		{"raw page", "<html>" + strings.Repeat("x", 280) + secret + strings.Repeat("y", 1000) + "</html>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, tc.body)
			})
			c := New(Config{KeyFunc: key(secret)})
			c.base = srv.URL
			_, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
			ce := callErr(t, err)
			require.Equal(t, aiprov.CodeKeyRejected, ce.Code)
			require.NotContains(t, err.Error(), "SECRET", "the key never reaches a sentence")
			require.NotContains(t, err.Error(), "sk-ant-api03", "not even the prefix a cut before the scrub leaves standing")
			msg := strings.TrimPrefix(err.Error(), "anthropic: API error (HTTP 401): ")
			require.LessOrEqual(t, len([]rune(msg)), errorMessageLimit+1, "bounded to %d runes and an ellipsis", errorMessageLimit)
			require.True(t, strings.HasSuffix(msg, "…"))
		})
	}
}

// TestRedirectsAreRefused — a 3xx is the answer, never followed: the key must not travel to wherever
// Location points (net/http forwards x-api-key across hosts).
//
// MUTATION: CheckRedirect removed from New → red: the second host receives the request, key included.
func TestRedirectsAreRefused(t *testing.T) {
	elsewhere := &recorder{}
	target := elsewhere.server(t, answer(okBody))
	rec := &recorder{}
	srv := rec.server(t, func(w http.ResponseWriter) {
		w.Header().Set("Location", target.URL+"/v1/messages")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	res, err := newAt(srv.URL, 2*time.Second).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	require.Nil(t, res)
	ce := callErr(t, err)
	require.Equal(t, http.StatusTemporaryRedirect, ce.HTTPStatus)
	require.Equal(t, aiprov.CodeProviderError, ce.Code)
	require.False(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.Equal(t, 1, rec.count())
	require.Zero(t, elsewhere.count(), "the redirect was followed: the key went to another host")
}

// ─── the engaged boundary on the wire ───────────────────────────────────────────────────────────

// deadAddr is an address nobody listens on: a listener is opened to learn a free port and closed.
func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return "http://" + addr
}

// hangingProvider RECEIVES the whole request (the body is read — that IS "the provider got it") and
// never answers until released or the client leaves.
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

// TestAWrittenRequestThatTimesOutIsEngaged — POST written, the budget expires: the provider is billing
// right now. Engaged, NOT retryable, Code timeout (the router's D-16 door reads exactly this).
//
// MUTATION: Engaged from `false` instead of wroteRequest() on the Do branch → red.
func TestAWrittenRequestThatTimesOutIsEngaged(t *testing.T) {
	url, reached := hangingProvider(t)
	c := newAt(url, 200*time.Millisecond)
	start := time.Now()
	res, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	require.Less(t, time.Since(start), 5*time.Second, "the call must end on its own budget")
	select {
	case <-reached:
	default:
		t.Fatal("the provider never got the request — the probe measures something else")
	}
	require.Nil(t, res)
	ce := callErr(t, err)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable, "an engaged failure is never fed to the breaker as weather")
	require.Equal(t, aiprov.CodeTimeout, ce.Code)
	require.Zero(t, ce.HTTPStatus)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, strings.HasPrefix(err.Error(), "anthropic: request failed: "), err.Error())
}

// TestAWrittenRequestTheCallerCancelledIsEngaged — a closed tab after the POST left: still paid; and
// "canceled", never "transport".
//
// MUTATION: aiprov.Interruption bypassed on the Do branch (`code := aiprov.CodeTransport`) → red.
func TestAWrittenRequestTheCallerCancelledIsEngaged(t *testing.T) {
	url, reached := hangingProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-reached
		cancel()
	}()
	_, err := newAt(url, 10*time.Second).Chat(ctx, "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.Equal(t, aiprov.CodeCanceled, ce.Code)
}

// TestARefusedConnectionIsNotEngagedAndRetryable — nothing was written, nobody pays, the next attempt
// may reach a live provider: the router may fall back and the breaker counts it.
//
// MUTATIONS: Engaged true unconditionally on the Do branch → red; Retryable false on the Do branch → red.
func TestARefusedConnectionIsNotEngagedAndRetryable(t *testing.T) {
	_, err := newAt(deadAddr(t), 2*time.Second).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded, "a dead address must refuse, not time out")
	require.False(t, ce.Engaged)
	require.True(t, ce.Retryable)
	require.Equal(t, aiprov.CodeTransport, ce.Code)
}

// TestADeadlineBeforeTheWriteIsNotEngagedAndRetryable — the connection never came up before the budget
// ran out (a dial that hangs): nothing was sent, so it is weather.
//
// MUTATION: Engaged true unconditionally on the Do branch → red (shared with the refused dial).
func TestADeadlineBeforeTheWriteIsNotEngagedAndRetryable(t *testing.T) {
	c := newAt("http://provider.invalid", 100*time.Millisecond)
	c.http = &http.Client{CheckRedirect: refuseRedirect, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done() // a SYN nobody answers
			return nil, ctx.Err()
		},
	}}
	_, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.False(t, ce.Engaged)
	require.True(t, ce.Retryable)
	require.Equal(t, aiprov.CodeTimeout, ce.Code)
}

// TestACallerWhoLeftBeforeTheWriteIsNotRetryable — the caller's context was already cancelled: nothing
// was sent, nothing was learnt about the provider, the breaker must not count it.
//
// MUTATION: `retryable := !engaged` (drop the Canceled exception) → red.
func TestACallerWhoLeftBeforeTheWriteIsNotRetryable(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newAt(srv.URL, 2*time.Second).Chat(ctx, "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.False(t, ce.Engaged)
	require.False(t, ce.Retryable, "a closed tab is not a provider fault")
	require.Equal(t, aiprov.CodeCanceled, ce.Code)
	require.Zero(t, rec.count())
}

// TestAnUnusable2xxIsEngaged — the provider accepted the request and did the work: whatever is wrong
// with the answer is ours to carry, and the money moved. Every decoded one comes back WITH the partial
// result (usage, finish reason; Text "") so the spend reaches the caller.
//
// MUTATIONS: the refusal branch removed → the "refusal with partial text" row succeeds with the partial
// text → red; the max_tokens split removed → the budget row reads empty_answer → red; the JSON-mode
// no-object branch returns the prose → red; the empty branch returns a nil result → the usage rows red;
// res.Text not cleared on failure → the refusal-with-text row red.
func TestAnUnusable2xxIsEngaged(t *testing.T) {
	const usage = `"usage":{"input_tokens":10149,"output_tokens":2500}`
	cases := []struct {
		name        string
		body        string
		jsonMode    bool
		code        string
		sentence    string
		sentinel    error
		wantPartial bool
		wantFinish  string
	}{
		{name: "broken envelope", body: `{"content":`, code: aiprov.CodeProviderError,
			sentence: "anthropic: could not decode API response envelope: "},
		{name: "error envelope", body: errorBody("upstream died"), code: aiprov.CodeProviderError,
			sentence: "anthropic: API error: upstream died"},
		{name: "no content", body: `{"model":"x/y","content":[],"stop_reason":"end_turn",` + usage + `}`,
			code: aiprov.CodeEmptyAnswer, sentence: "anthropic: model returned an empty message", wantPartial: true, wantFinish: "stop"},
		{name: "only whitespace text", body: `{"model":"x/y","content":[{"type":"text","text":"  "}],"stop_reason":"end_turn",` + usage + `}`,
			code: aiprov.CodeEmptyAnswer, sentence: "anthropic: model returned an empty message", wantPartial: true, wantFinish: "stop"},
		{name: "refusal before any output", body: `{"model":"x/y","content":[],"stop_reason":"refusal",` + usage + `}`,
			code: aiprov.CodeEmptyAnswer, sentence: "anthropic: the model refused to answer (stop_reason refusal)", wantPartial: true, wantFinish: "refusal"},
		{name: "refusal with partial text", body: `{"model":"x/y","content":[{"type":"text","text":"Sure, the first half"}],"stop_reason":"refusal",` + usage + `}`,
			code: aiprov.CodeEmptyAnswer, sentence: "anthropic: the model refused to answer (stop_reason refusal)", wantPartial: true, wantFinish: "refusal"},
		{name: "budget spent on thinking", body: `{"model":"x/y","content":[{"type":"thinking","thinking":"","signature":"s"}],"stop_reason":"max_tokens",` + usage + `}`,
			code:     aiprov.CodeBudgetExhausted,
			sentence: "anthropic: the model spent the whole completion budget without answering (2500 completion tokens spent, none of them answer)",
			sentinel: aiprov.ErrBudgetExhausted, wantPartial: true, wantFinish: "length"},
		{name: "JSON mode, no object in the answer", jsonMode: true, body: `{"model":"x/y","content":[{"type":"text","text":"I cannot produce that."}],"stop_reason":"end_turn",` + usage + `}`,
			code: aiprov.CodeEmptyAnswer, sentence: "anthropic: JSON mode: the answer carries no JSON object (stop_reason end_turn)", wantPartial: true, wantFinish: "stop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(tc.body))
			res, err := newAt(srv.URL, 2*time.Second).Chat(context.Background(), "m",
				aiprov.ChatRequest{System: "s", User: "u", MaxTokens: 2500, JSONMode: tc.jsonMode})
			ce := callErr(t, err)
			require.True(t, ce.Engaged, "a 2xx means the request was accepted and billed")
			require.True(t, aiprov.Engaged(err))
			require.False(t, ce.Retryable)
			require.Equal(t, tc.code, ce.Code)
			require.Equal(t, http.StatusOK, ce.HTTPStatus)
			require.True(t, strings.HasPrefix(err.Error(), tc.sentence), "%q", err.Error())
			require.NotContains(t, err.Error(), "\n", "the sentence reaches a person as one line")
			if tc.sentinel != nil {
				require.ErrorIs(t, err, tc.sentinel)
			}
			if !tc.wantPartial {
				require.Nil(t, res)
				return
			}
			require.NotNil(t, res, "an unusable answer was paid for: its size must reach the caller")
			require.Empty(t, res.Text, "a partial result never carries an answer")
			require.Equal(t, tc.wantFinish, res.FinishReason)
			require.Equal(t, aiprov.TokenUsage{Prompt: 10149, Completion: 2500}, res.Usage)
			require.Equal(t, "x/y", res.Model)
			require.True(t, res.Engaged)
		})
	}
}

// ─── budget and ceiling ─────────────────────────────────────────────────────────────────────────

// slowProvider answers a valid message, but not before delay.
func slowProvider(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestTheCallersCeilingBuysItsOwnTime — the same slow provider, the same tiny base; the ONLY difference
// is the caller's ceiling. With none, DefaultMaxTokens still goes on the wire but buys NO time: the
// budget is the base, exactly what the router grants this call (router.budget) — so the call is cut.
// With 8000 tokens the printing time is added and the same provider makes it.
//
// MUTATIONS: the budget derived from the wire's ceiling (DefaultMaxTokens when none) → the first half
// goes red (136 s bought that the router never grants); post uses c.budgetBase alone → the second half
// goes red; context.WithTimeout removed from post → the first half goes red.
func TestTheCallersCeilingBuysItsOwnTime(t *testing.T) {
	srv := slowProvider(t, 300*time.Millisecond)
	c := newAt(srv.URL, 40*time.Millisecond)

	_, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeTimeout, ce.Code, "no ceiling: the budget is the base (40 ms) against a 300 ms answer")

	res, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u", MaxTokens: 8000})
	require.NoError(t, err, "the caller's ceiling must buy its own printing time")
	require.Equal(t, "ok", res.Text)
}

// TestTheReadCeilingRefusesByName — exactly at the ceiling works; one byte over is refused as
// ErrResponseTooLarge, engaged (the answer WAS there), never as "unexpected end of JSON".
//
// MUTATIONS: readCapped reads `limit` instead of `limit+1` → the second half goes red; the too-large
// branch not engaged → red.
func TestTheReadCeilingRefusesByName(t *testing.T) {
	atCeiling := okBody + strings.Repeat(" ", MaxResponseBytes-len(okBody))
	require.Len(t, atCeiling, MaxResponseBytes)
	rec := &recorder{}
	srv := rec.server(t, answer(atCeiling))
	res, err := newAt(srv.URL, 2*time.Second).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	require.NoError(t, err, "a body exactly at the ceiling must be accepted")
	require.Equal(t, "ok", res.Text)

	over := okBody + strings.Repeat(" ", MaxResponseBytes-len(okBody)+1)
	srv = rec.server(t, answer(over))
	_, err = newAt(srv.URL, 2*time.Second).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.ErrorIs(t, err, aiprov.ErrResponseTooLarge)
	require.Equal(t, aiprov.CodeTooLarge, ce.Code)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.Equal(t, http.StatusOK, ce.HTTPStatus)
	require.Equal(t, fmt.Sprintf("anthropic: read response: anthropic: the provider's response exceeded the read ceiling: "+
		"messages response is larger than %d bytes", MaxResponseBytes), err.Error())
}

// TestARefusedStatusWithAnUnreadableBodyIsStillARefusal — a 404 whose error body runs past the read
// ceiling is the provider's refusal at the gate: not engaged, the model-unknown code and sentinel
// intact, so the router still falls back; the sentence says the excuse could not be read, and why.
//
// MUTATION: judge the body before the status (the too-large branch first) → red: Engaged true, Code
// too_large, no ErrModelUnavailable.
func TestARefusedStatusWithAnUnreadableBodyIsStillARefusal(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(errorBody(strings.Repeat("x", MaxResponseBytes+16))))
	})
	_, err := newAt(srv.URL, 2*time.Second).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.ErrorIs(t, err, aiprov.ErrModelUnavailable)
	require.Equal(t, aiprov.CodeModelUnknown, ce.Code)
	require.False(t, ce.Engaged, "a refusal at the gate moved no money, however long its excuse")
	require.False(t, ce.Retryable)
	require.NotErrorIs(t, err, aiprov.ErrResponseTooLarge)
	require.Contains(t, err.Error(), "API error (HTTP 404): response body unavailable: anthropic: the provider's response exceeded the read ceiling")
}

// ─── refusals before the wire ───────────────────────────────────────────────────────────────────

// TestRefusalsBeforeTheWire — every case is OUR mistake or our missing key; none reaches the provider,
// none is engaged, none is weather.
//
// MUTATIONS: the MaxImageParts check removed → "too many pictures" reaches the provider → red; the
// base64 check removed from imageSourceOf → the percent-encoded row reaches the provider → red; the
// empty-prompt check removed → red.
func TestRefusalsBeforeTheWire(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	tooMany := make([]string, MaxImageParts+1)
	for i := range tooMany {
		tooMany[i] = "https://media.grbpwr.com/x.png"
	}
	at := func(c *Client) *Client { c.base = srv.URL; return c }
	cases := []struct {
		name   string
		client *Client
		model  string
		req    aiprov.ChatRequest
		code   string
		want   string
	}{
		{"no key func", at(New(Config{})), "m", aiprov.ChatRequest{User: "u"}, aiprov.CodeNotConfigured, "anthropic: no API key is set"},
		{"blank key", at(New(Config{KeyFunc: key("   ")})), "m", aiprov.ChatRequest{User: "u"}, aiprov.CodeNotConfigured, "anthropic: no API key is set"},
		{"nil client", nil, "m", aiprov.ChatRequest{User: "u"}, aiprov.CodeNotConfigured, "anthropic: no API key is set"},
		{"empty model", newAt(srv.URL, time.Second), "  ", aiprov.ChatRequest{User: "u"}, aiprov.CodeBadRequest, "anthropic: a message needs a model slug"},
		{"empty prompt", newAt(srv.URL, time.Second), "m", aiprov.ChatRequest{System: "s", User: "  "}, aiprov.CodeBadRequest, "anthropic: a message needs a user prompt"},
		{"pictures without a prompt", newAt(srv.URL, time.Second), "m", aiprov.ChatRequest{ImageURLs: []string{"https://x/y.png"}},
			aiprov.CodeBadRequest, "anthropic: a message needs a user prompt"},
		{"too many pictures", newAt(srv.URL, time.Second), "m", aiprov.ChatRequest{User: "p", ImageURLs: tooMany},
			aiprov.CodeBadRequest, "anthropic: 17 pictures exceeds the 16-picture limit for one request"},
		{"not a url", newAt(srv.URL, time.Second), "m", aiprov.ChatRequest{User: "p", ImageURLs: []string{"file:///etc/passwd"}},
			aiprov.CodeBadRequest, "anthropic: picture 1: picture address must be http(s)://"},
		{"empty address", newAt(srv.URL, time.Second), "m", aiprov.ChatRequest{User: "p", ImageURLs: []string{"https://x/1.png", "   "}},
			aiprov.CodeBadRequest, "anthropic: picture 2: empty picture address"},
		{"data uri without payload", newAt(srv.URL, time.Second), "m", aiprov.ChatRequest{User: "p", ImageURLs: []string{"data:image/png"}},
			aiprov.CodeBadRequest, "anthropic: picture 1: data URI carries no payload"},
		{"data uri with an empty payload", newAt(srv.URL, time.Second), "m", aiprov.ChatRequest{User: "p", ImageURLs: []string{"data:image/png;base64,"}},
			aiprov.CodeBadRequest, "anthropic: picture 1: data URI carries no payload"},
		{"data uri not base64", newAt(srv.URL, time.Second), "m", aiprov.ChatRequest{User: "p", ImageURLs: []string{"data:image/svg+xml,%3Csvg%3E"}},
			aiprov.CodeBadRequest, "anthropic: picture 1: data URI must be base64-encoded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.client.Chat(context.Background(), tc.model, tc.req)
			require.Nil(t, res)
			ce := callErr(t, err)
			require.Equal(t, "anthropic", ce.Provider)
			require.Equal(t, tc.code, ce.Code)
			require.False(t, ce.Engaged)
			require.False(t, ce.Retryable, "our own mistake does not improve by being sent again")
			require.Zero(t, ce.HTTPStatus)
			require.True(t, strings.HasPrefix(err.Error(), tc.want), "%q", err.Error())
			if tc.code == aiprov.CodeNotConfigured {
				require.ErrorIs(t, err, aiprov.ErrNotConfigured)
			}
		})
	}
	require.Zero(t, rec.count(), "a refused request reached the provider")
}

// ─── surface ────────────────────────────────────────────────────────────────────────────────────

// TestSurface — what the router and the wiring read: the Chatter seam, the billing key, the constant
// host, the budget base a lease derives from, the picture cap shared with the draft-idea door.
//
// MUTATIONS: MaxImageParts = 20 → red (the door admits what the transport would refuse, or the
// reverse); CompletionBase returns aiprov.DefaultBudgetBase whatever HTTPTimeout says → red (the lease
// would be sized from a base the wire does not use); the host built from AnthropicHost + "/v1" → red.
func TestSurface(t *testing.T) {
	require.Equal(t, oaichat.MaxImageParts, MaxImageParts, "the draft-idea door refuses by oaichat's number")
	require.Equal(t, entity.AIProviderAnthropic, provider)

	c := New(Config{KeyFunc: key("k")})
	require.Equal(t, endpoints.AnthropicAPIBase, c.BaseURL())
	require.Equal(t, aiprov.DefaultBudgetBase, c.CompletionBase())
	require.Equal(t, 5*time.Second, New(Config{HTTPTimeout: 5 * time.Second}).CompletionBase())
	require.True(t, c.Enabled())
	require.False(t, New(Config{}).Enabled())

	var nilClient *Client
	require.Equal(t, aiprov.DefaultBudgetBase, nilClient.CompletionBase())
	require.False(t, nilClient.Enabled())
	require.Empty(t, nilClient.BaseURL())

	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	var chatter aiprov.Chatter = newAt(srv.URL, time.Second)
	res, err := chatter.Chat(context.Background(), "claude-sonnet-5", aiprov.ChatRequest{System: "s", User: "u"})
	require.NoError(t, err)
	require.Equal(t, &aiprov.ChatResult{Text: "ok", FinishReason: "stop", Provider: "anthropic", Model: "claude-sonnet-5",
		RequestID: "msg_01", Usage: aiprov.TokenUsage{Prompt: 10, Completion: 2}, Engaged: true}, res)
}
