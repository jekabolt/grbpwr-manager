package oaichat

import (
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
)

// contentIsStillAString is a COMPILE-TIME assertion (moved here from internal/openrouter with the type
// it guards). The whole hazard of multimodal input is the tempting edit: retype textMessage.Content from
// string to `any`. That edit compiles everywhere and turns every text feature into a runtime shape
// nobody checks; with this line it stops being valid Go.
var contentIsStillAString string = textMessage{}.Content

const okBody = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

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

func newOpenRouter(url string) *Client {
	return New(Config{
		Provider: "openrouter", BaseURL: url, Dialect: DialectOpenRouter, KeyFunc: key("k"),
		HTTPTimeout: 2 * time.Second, Title: "grbpwr-products-manager", Referer: "https://admin.grbpwr.com",
	})
}

func newOpenAI(url string) *Client {
	return New(Config{
		Provider: "openai", BaseURL: url, Dialect: DialectOpenAI, KeyFunc: key("k"),
		HTTPTimeout: 2 * time.Second, Title: "must-not-be-sent", Referer: "https://must-not-be-sent",
	})
}

func callErr(t *testing.T, err error) *aiprov.CallError {
	t.Helper()
	require.Error(t, err)
	ce, ok := aiprov.AsCallError(err)
	require.True(t, ok, "every failure is a *aiprov.CallError, got %T: %v", err, err)
	return ce
}

// ─── request bytes ───────────────────────────────────────────────────────────────────────────────

// TestRequestBytesPerDialect pins the EXACT body of each dialect. Golden strings, not field checks: any
// reordering, new key or changed value is a new request to a live paid feature.
//
// OpenRouter: today's openrouter bytes + `"usage":{"include":true}` LAST. OpenAI: max_completion_tokens,
// reasoning_effort, no usage object.
//
// MUTATIONS (each measured red → restored green): move Usage above Reasoning in openRouterRequest →
// the OpenRouter rows; send `max_tokens` in openAIRequest → the OpenAI rows; drop the `if req.Effort
// != ""` guard (always send reasoning) → the plain OpenRouter row; PartsAlways ignored → the "parts
// with no pictures" row.
func TestRequestBytesPerDialect(t *testing.T) {
	const sys = `{"role":"system","content":"sys"}`
	cases := []struct {
		name   string
		openAI bool
		model  string
		req    aiprov.ChatRequest
		opt    Options
		want   string
	}{
		{
			name: "openrouter text, provider defaults", model: "shared/slug",
			req:  aiprov.ChatRequest{System: "sys", User: "u"},
			want: `{"model":"shared/slug","messages":[` + sys + `,{"role":"user","content":"u"}],"temperature":0.2,"usage":{"include":true}}`,
		},
		{
			name: "openrouter text, json + ceiling + effort", model: "shared/slug",
			req:  aiprov.ChatRequest{System: "sys", User: "u", JSONMode: true, MaxTokens: 2500, Effort: "none"},
			want: `{"model":"shared/slug","messages":[` + sys + `,{"role":"user","content":"u"}],"max_tokens":2500,"temperature":0.2,"response_format":{"type":"json_object"},"reasoning":{"effort":"none"},"usage":{"include":true}}`,
		},
		{
			name: "openrouter pictures", model: "openai/gpt-5-mini",
			req: aiprov.ChatRequest{System: "sys", User: "u", ImageURLs: []string{" https://x/1.png ", "data:image/png;base64,AAAA"},
				JSONMode: true, MaxTokens: 300, Effort: "minimal"},
			want: `{"model":"openai/gpt-5-mini","messages":[` + sys + `,{"role":"user","content":[{"type":"text","text":"u"},{"type":"image_url","image_url":{"url":"https://x/1.png"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}],"max_tokens":300,"temperature":0.2,"response_format":{"type":"json_object"},"reasoning":{"effort":"minimal"},"usage":{"include":true}}`,
		},
		{
			name: "openrouter parts with no pictures (legacy CompleteWithImages)", model: "shared/slug",
			req: aiprov.ChatRequest{System: "sys", User: "u"}, opt: Options{PartsAlways: true},
			want: `{"model":"shared/slug","messages":[` + sys + `,{"role":"user","content":[{"type":"text","text":"u"}]}],"temperature":0.2,"usage":{"include":true}}`,
		},
		{
			name: "openrouter negative ceiling is no ceiling", model: "m",
			req:  aiprov.ChatRequest{System: "sys", User: "u", MaxTokens: -5},
			want: `{"model":"m","messages":[` + sys + `,{"role":"user","content":"u"}],"temperature":0.2,"usage":{"include":true}}`,
		},
		{
			name: "openai text, provider defaults", openAI: true, model: "gpt-5-mini",
			req:  aiprov.ChatRequest{System: "sys", User: "u"},
			want: `{"model":"gpt-5-mini","messages":[` + sys + `,{"role":"user","content":"u"}],"temperature":0.2}`,
		},
		{
			name: "openai text, json + ceiling + effort", openAI: true, model: "gpt-5-mini",
			req:  aiprov.ChatRequest{System: "sys", User: "u", JSONMode: true, MaxTokens: 900, Effort: "low"},
			want: `{"model":"gpt-5-mini","messages":[` + sys + `,{"role":"user","content":"u"}],"max_completion_tokens":900,"temperature":0.2,"response_format":{"type":"json_object"},"reasoning_effort":"low"}`,
		},
		{
			name: "openai pictures", openAI: true, model: "gpt-5-mini",
			req:  aiprov.ChatRequest{System: "sys", User: "u", ImageURLs: []string{"https://x/1.png"}, MaxTokens: 300, Effort: "minimal"},
			want: `{"model":"gpt-5-mini","messages":[` + sys + `,{"role":"user","content":[{"type":"text","text":"u"},{"type":"image_url","image_url":{"url":"https://x/1.png"}}]}],"max_completion_tokens":300,"temperature":0.2,"reasoning_effort":"minimal"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(okBody))
			c := newOpenRouter(srv.URL + "/")
			if tc.openAI {
				c = newOpenAI(srv.URL)
			}
			_, err := c.Send(context.Background(), tc.model, tc.req, tc.opt)
			require.NoError(t, err)
			require.Equal(t, 1, rec.count())
			require.Equal(t, tc.want, rec.bodies[0])
			require.Equal(t, "/chat/completions", rec.paths[0], "one trailing slash of the base URL is not a second path segment")
		})
	}
}

// TestHeadersPerDialect — the key per request as a Bearer, OpenRouter's attribution headers in that
// dialect only.
//
// MUTATION: drop the `c.cfg.Dialect == DialectOpenRouter` guard around the attribution headers → the
// OpenAI half goes red.
func TestHeadersPerDialect(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))

	_, err := newOpenRouter(srv.URL).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	require.NoError(t, err)
	_, err = newOpenAI(srv.URL).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	require.NoError(t, err)

	or, oai := rec.headers[0], rec.headers[1]
	require.Equal(t, "Bearer k", or.Get("Authorization"))
	require.Equal(t, "application/json", or.Get("Content-Type"))
	require.Equal(t, "grbpwr-products-manager", or.Get("X-Title"))
	require.Equal(t, "https://admin.grbpwr.com", or.Get("HTTP-Referer"))

	require.Equal(t, "Bearer k", oai.Get("Authorization"))
	require.Empty(t, oai.Get("X-Title"), "OpenAI gets no OpenRouter attribution")
	require.Empty(t, oai.Get("HTTP-Referer"))
}

// TestKeyFuncIsReadPerRequest — a key saved in the admin panel reaches the NEXT request; "" disables
// the transport before the wire.
//
// MUTATION: cache the key in New → red on the second Authorization.
func TestKeyFuncIsReadPerRequest(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	var mu sync.Mutex
	current := "key-one"
	c := New(Config{Provider: "openrouter", BaseURL: srv.URL, Dialect: DialectOpenRouter, KeyFunc: func() string {
		mu.Lock()
		defer mu.Unlock()
		return current
	}})
	set := func(k string) { mu.Lock(); current = k; mu.Unlock() }

	_, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	set("  key-two  ")
	_, err = c.Chat(context.Background(), "m", aiprov.ChatRequest{User: "u"})
	require.NoError(t, err)
	require.Equal(t, "Bearer key-one", rec.headers[0].Get("Authorization"))
	require.Equal(t, "Bearer key-two", rec.headers[1].Get("Authorization"))

	require.True(t, c.Enabled())
	set("")
	require.False(t, c.Enabled())
	_, err = c.Chat(context.Background(), "m", aiprov.ChatRequest{User: "u"})
	require.ErrorIs(t, err, aiprov.ErrNotConfigured)
	require.Equal(t, 2, rec.count(), "a disabled transport reached the wire")
}

// ─── response facts ──────────────────────────────────────────────────────────────────────────────

// TestResponseFacts — text, finish reason, reported model, id, usage with its cached / reasoning parts,
// OpenRouter's usage.cost as a KNOWN price only when present and > 0, and nothing read from a "cost" in
// the OpenAI dialect. Asserted NON-ZERO: a misspelled tag leaves zeros and a call that reads as free.
//
// MUTATIONS: parseCost accepts zero (`d.IsNegative()` instead of `!d.IsPositive()`) → the "cost 0" row;
// read cost in every dialect → the OpenAI row; drop the prompt_tokens_details read → Cached rows;
// ignore cr.Model → the "reported model" rows; decode id / the token details as typed fields instead
// of raw JSON → the "oddly typed" row (the whole answer is lost to one number).
func TestResponseFacts(t *testing.T) {
	const fullUsage = `"usage":{"prompt_tokens":1200,"completion_tokens":80,"total_tokens":1280,` +
		`"prompt_tokens_details":{"cached_tokens":1000},"completion_tokens_details":{"reasoning_tokens":30}`
	cases := []struct {
		name       string
		openAI     bool
		body       string
		wantModel  string
		wantID     string
		wantUsage  aiprov.TokenUsage
		wantTotal  int
		wantCost   string // "" = NULL
		wantFinish string
	}{
		{
			name:       "openrouter, everything reported",
			body:       `{"id":"gen-123","model":"anthropic/claude-sonnet-5","choices":[{"message":{"content":"  hi  "},"finish_reason":"stop"}],` + fullUsage + `,"cost":0.00123}}`,
			wantModel:  "anthropic/claude-sonnet-5",
			wantID:     "gen-123",
			wantUsage:  aiprov.TokenUsage{Prompt: 1200, Completion: 80, Cached: 1000, Reasoning: 30},
			wantTotal:  1280,
			wantCost:   "0.00123",
			wantFinish: "stop",
		},
		{
			name:      "openrouter, cost as a string",
			body:      `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":"0.5"}}`,
			wantModel: "requested/slug",
			wantUsage: aiprov.TokenUsage{Prompt: 1, Completion: 2},
			wantCost:  "0.5",
		},
		{
			name:      "openrouter, cost 0 is not a known price",
			body:      `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":0}}`,
			wantModel: "requested/slug",
			wantUsage: aiprov.TokenUsage{Prompt: 1, Completion: 2},
		},
		{
			name:      "openrouter, cost null / absent details",
			body:      `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":null}}`,
			wantModel: "requested/slug",
			wantUsage: aiprov.TokenUsage{Prompt: 1, Completion: 2},
		},
		{
			name:      "openrouter, oddly typed new fields cost those numbers, never the answer",
			body:      `{"id":42,"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"cost":"n/a","prompt_tokens_details":"x","completion_tokens_details":[1]}}`,
			wantModel: "requested/slug",
			wantUsage: aiprov.TokenUsage{Prompt: 1, Completion: 2},
		},
		{
			name:      "openai, a cost field is not read",
			openAI:    true,
			body:      `{"id":"chatcmpl-9","model":"gpt-5-mini-2025-08-07","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],` + fullUsage + `,"cost":0.5}}`,
			wantModel: "gpt-5-mini-2025-08-07",
			wantID:    "chatcmpl-9",
			wantUsage: aiprov.TokenUsage{Prompt: 1200, Completion: 80, Cached: 1000, Reasoning: 30},
			wantTotal: 1280, wantFinish: "stop",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(tc.body))
			c := newOpenRouter(srv.URL)
			provider := "openrouter"
			if tc.openAI {
				c, provider = newOpenAI(srv.URL), "openai"
			}
			reply, err := c.Send(context.Background(), "requested/slug", aiprov.ChatRequest{System: "s", User: "u"}, Options{})
			require.NoError(t, err)
			require.Equal(t, "hi", reply.Text, "the text is trimmed, as it always was")
			require.Equal(t, tc.wantFinish, reply.FinishReason)
			require.Equal(t, tc.wantModel, reply.Model)
			require.Equal(t, tc.wantID, reply.RequestID)
			require.Equal(t, tc.wantUsage, reply.Usage)
			require.Equal(t, tc.wantTotal, reply.TotalTokens)
			require.Equal(t, provider, reply.Provider)
			require.True(t, reply.Engaged, "an answer came back: the request was written and served")
			if tc.wantCost == "" {
				require.False(t, reply.CostUSD.Valid, "cost must be NULL, got %s", reply.CostUSD.Decimal)
			} else {
				require.True(t, reply.CostUSD.Valid)
				require.Equal(t, tc.wantCost, reply.CostUSD.Decimal.String())
			}
		})
	}
}

// ─── status → CallError ─────────────────────────────────────────────────────────────────────────

// TestStatusMatrix — every non-2xx is a refusal at the gate (NOT engaged), classified by status alone,
// with the sentence the openrouter client always wrote (a consumer regex reads its start until B-18).
//
// MUTATIONS, one per row of classifyStatus (each measured red → green): 401 → out_of_credits; 403 row
// removed (falls to bad_request); 402 → key_rejected; 404 → bad_request; 408 row removed (becomes a
// terminal bad_request); 429 retryable false; `>= 500` row removed; `>= 400` retryable true; the
// default row retryable true. Plus: 404 without the ErrModelUnavailable wrap → the sentinel row;
// statusError marks Engaged → every row.
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
		{409, aiprov.CodeBadRequest, false},
		{422, aiprov.CodeBadRequest, false},
		{429, aiprov.CodeRateLimited, true},
		{500, aiprov.CodeProviderError, true},
		{502, aiprov.CodeProviderError, true},
		{503, aiprov.CodeProviderError, true},
		{304, aiprov.CodeProviderError, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":{"message":"no"}}`)
			})
			res, err := newOpenRouter(srv.URL).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
			require.Nil(t, res)
			ce := callErr(t, err)
			require.Equal(t, "openrouter", ce.Provider)
			require.Equal(t, tc.code, ce.Code)
			require.Equal(t, tc.status, ce.HTTPStatus)
			require.Equal(t, tc.retryable, ce.Retryable)
			require.False(t, ce.Engaged, "HTTP %d is a refusal at the gate, not a charge", tc.status)
			require.False(t, aiprov.Engaged(err))

			if tc.status == http.StatusNotFound {
				require.ErrorIs(t, err, aiprov.ErrModelUnavailable)
				require.Equal(t, "openrouter: the configured model is not available at the provider: API error (HTTP 404): no", err.Error())
				return
			}
			require.NotErrorIs(t, err, aiprov.ErrModelUnavailable)
			if tc.status == http.StatusNotModified {
				// A 304 carries no body; the sentence falls back to the (empty) raw body.
				require.Equal(t, "openrouter: API error (HTTP 304): ", err.Error())
				return
			}
			require.Equal(t, fmt.Sprintf("openrouter: API error (HTTP %d): no", tc.status), err.Error())
		})
	}
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
// right now. Engaged, NOT retryable (a second candidate would pay twice), Code timeout (the router's
// D-16 door reads exactly this).
//
// MUTATION: Engaged from `false` instead of wroteRequest() on the Do branch → red.
func TestAWrittenRequestThatTimesOutIsEngaged(t *testing.T) {
	url, reached := hangingProvider(t)
	c := New(Config{Provider: "openrouter", BaseURL: url, Dialect: DialectOpenRouter, KeyFunc: key("k"), HTTPTimeout: 200 * time.Millisecond})
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
	require.True(t, strings.HasPrefix(err.Error(), "openrouter: request failed: "), err.Error())
}

// TestAWrittenRequestTheCallerCancelledIsEngaged — a closed tab after the POST left: still paid; and
// "canceled", never "transport" — the provider did nothing wrong.
//
// MUTATION: interruption() without the Canceled case → Code transport → red.
func TestAWrittenRequestTheCallerCancelledIsEngaged(t *testing.T) {
	url, reached := hangingProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-reached
		cancel()
	}()
	c := New(Config{Provider: "openrouter", BaseURL: url, Dialect: DialectOpenRouter, KeyFunc: key("k"), HTTPTimeout: 10 * time.Second})
	_, err := c.Chat(ctx, "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.Equal(t, aiprov.CodeCanceled, ce.Code)
}

// TestARefusedConnectionIsNotEngagedAndRetryable — nothing was written, nobody pays, the next attempt
// may reach a live provider: the router may fall back and the breaker counts it.
//
// MUTATION: Engaged true unconditionally on the Do branch → red; Retryable false on the Do branch → red.
func TestARefusedConnectionIsNotEngagedAndRetryable(t *testing.T) {
	c := New(Config{Provider: "openrouter", BaseURL: deadAddr(t), Dialect: DialectOpenRouter, KeyFunc: key("k"), HTTPTimeout: 2 * time.Second})
	_, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded, "a dead address must refuse, not time out")
	require.False(t, ce.Engaged)
	require.True(t, ce.Retryable)
	require.Equal(t, aiprov.CodeTransport, ce.Code)
}

// TestADeadlineBeforeTheWriteIsNotEngagedAndRetryable — the connection never came up before the budget
// ran out (a dial that hangs): nothing was sent, so it is weather — retryable, not engaged, "timeout".
//
// MUTATION: interruption() without the DeadlineExceeded case (and without the net.Error one) → Code
// transport → red.
func TestADeadlineBeforeTheWriteIsNotEngagedAndRetryable(t *testing.T) {
	c := New(Config{Provider: "openrouter", BaseURL: "http://provider.invalid", Dialect: DialectOpenRouter, KeyFunc: key("k"), HTTPTimeout: 100 * time.Millisecond})
	c.http = &http.Client{Transport: &http.Transport{
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
// was sent, and nothing about the provider was learnt either, so the breaker must not count it.
//
// MUTATION: `retryable := !engaged` (drop the Canceled exception) → red.
func TestACallerWhoLeftBeforeTheWriteIsNotRetryable(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newOpenRouter(srv.URL).Chat(ctx, "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.False(t, ce.Engaged)
	require.False(t, ce.Retryable, "a closed tab is not a provider fault")
	require.Equal(t, aiprov.CodeCanceled, ce.Code)
	require.Zero(t, rec.count())
}

// TestAnUnusable2xxIsEngaged — the provider accepted the request and did the work: whatever is wrong
// with the envelope is ours to carry, and the money moved. Empty answers come back WITH the partial
// result so the spend reaches the caller.
//
// MUTATIONS: the "no choices" branch not engaged → red; the empty-answer branch returns a nil result →
// the usage rows go red; `length` not split out → the budget row goes red.
func TestAnUnusable2xxIsEngaged(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		code        string
		sentence    string
		sentinel    error
		wantPartial bool
	}{
		{"no choices", `{"choices":[]}`, aiprov.CodeEmptyAnswer, "openrouter: API response contained no choices", nil, false},
		{"broken envelope", `{"choices":`, aiprov.CodeProviderError, "openrouter: could not decode API response envelope: ", nil, false},
		{"error envelope", `{"error":{"message":"upstream died"}}`, aiprov.CodeProviderError, "openrouter: API error: upstream died", nil, false},
		{"empty message", `{"model":"x/y","choices":[{"message":{"content":"  "},"finish_reason":"stop"}],"usage":{"prompt_tokens":10149,"completion_tokens":2500,"total_tokens":12649}}`,
			aiprov.CodeEmptyAnswer, "openrouter: model returned an empty message", nil, true},
		{"budget spent", `{"model":"x/y","choices":[{"message":{"content":""},"finish_reason":"length"}],"usage":{"prompt_tokens":10149,"completion_tokens":2500,"total_tokens":12649,"cost":0.02}}`,
			aiprov.CodeBudgetExhausted, "openrouter: the model spent the whole completion budget without answering (2500 completion tokens spent, none of them answer)", aiprov.ErrBudgetExhausted, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(tc.body))
			res, err := newOpenRouter(srv.URL).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u", MaxTokens: 2500})
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
			require.NotNil(t, res, "an empty answer was paid for: its size must reach the caller")
			require.Empty(t, res.Text)
			require.Equal(t, aiprov.TokenUsage{Prompt: 10149, Completion: 2500}, res.Usage)
			require.Equal(t, "x/y", res.Model)
			require.True(t, res.Engaged)
		})
	}
}

// ─── budget and ceiling ─────────────────────────────────────────────────────────────────────────

// slowProvider answers a valid completion, but not before delay.
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

// TestTheCeilingBuysItsOwnTime — the same slow provider, the same tiny base; the ONLY difference is the
// requested ceiling. Without one the budget is the base and the call is cut; with 8000 tokens the
// printing time (8000/30 s) is added and the same provider makes it.
//
// MUTATION: post uses `c.budgetBase` instead of aiprov.CompletionBudget(c.budgetBase, ceiling) → the
// second half goes red; context.WithTimeout removed from post → the first half goes red.
func TestTheCeilingBuysItsOwnTime(t *testing.T) {
	srv := slowProvider(t, 300*time.Millisecond)
	c := New(Config{Provider: "openrouter", BaseURL: srv.URL, Dialect: DialectOpenRouter, KeyFunc: key("k"), HTTPTimeout: 40 * time.Millisecond})

	_, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeTimeout, ce.Code, "no ceiling: the budget is the base (40 ms) against a 300 ms answer")

	res, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u", MaxTokens: 8000})
	require.NoError(t, err, "the ceiling must buy its own printing time")
	require.Equal(t, "ok", res.Text)
}

// TestCompletionBase — the base the wire uses is the base a lease must be derived from.
func TestCompletionBase(t *testing.T) {
	require.Equal(t, aiprov.DefaultBudgetBase, New(Config{}).CompletionBase())
	require.Equal(t, 5*time.Second, New(Config{HTTPTimeout: 5 * time.Second}).CompletionBase())
	var nilClient *Client
	require.Equal(t, aiprov.DefaultBudgetBase, nilClient.CompletionBase())
	require.False(t, nilClient.Enabled())
	require.Empty(t, nilClient.BaseURL())
}

// TestTheReadCeilingRefusesByName — exactly at the ceiling works; one byte over is refused as
// ErrResponseTooLarge, engaged (the answer WAS there), never as the provider's "unexpected end of JSON".
//
// MUTATION: readCapped reads `limit` instead of `limit+1` → the second half goes red (a trimmed prefix
// of spaces still parses); the too-large branch not engaged → red.
func TestTheReadCeilingRefusesByName(t *testing.T) {
	valid := okBody

	atCeiling := valid + strings.Repeat(" ", MaxResponseBytes-len(valid))
	require.Len(t, atCeiling, MaxResponseBytes)
	rec := &recorder{}
	srv := rec.server(t, answer(atCeiling))
	res, err := newOpenRouter(srv.URL).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	require.NoError(t, err, "a body exactly at the ceiling must be accepted")
	require.Equal(t, "ok", res.Text)

	over := valid + strings.Repeat(" ", MaxResponseBytes-len(valid)+1)
	srv = rec.server(t, answer(over))
	_, err = newOpenRouter(srv.URL).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.ErrorIs(t, err, aiprov.ErrResponseTooLarge)
	require.Equal(t, aiprov.CodeTooLarge, ce.Code)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.Equal(t, http.StatusOK, ce.HTTPStatus)
	require.NotContains(t, err.Error(), "unexpected end of JSON input")
	require.Equal(t, fmt.Sprintf("openrouter: read response: openrouter: the provider's response exceeded the read ceiling: "+
		"chat/completions response is larger than %d bytes", MaxResponseBytes), err.Error())
}

// TestARefusedStatusWithAnUnreadableBodyIsStillARefusal — a 404 whose error body runs past the read
// ceiling (or is cut) is the provider's refusal at the gate: not engaged, the model-unknown code and
// sentinel intact, so the router still falls back (Codex C review, P2).
//
// MUTATION: judge the body before the status (the old order) → red: Engaged true, Code too_large,
// no ErrModelUnavailable.
func TestARefusedStatusWithAnUnreadableBodyIsStillARefusal(t *testing.T) {
	rec := &recorder{}
	huge := `{"error":{"message":"` + strings.Repeat("x", MaxResponseBytes+16) + `"}}`
	srv := rec.server(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(huge))
	})
	_, err := newOpenRouter(srv.URL).Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	ce := callErr(t, err)
	require.ErrorIs(t, err, aiprov.ErrModelUnavailable)
	require.Equal(t, aiprov.CodeModelUnknown, ce.Code)
	require.False(t, ce.Engaged, "a refusal at the gate moved no money, however long its excuse")
	require.False(t, ce.Retryable)
	require.Equal(t, http.StatusNotFound, ce.HTTPStatus)
	require.NotErrorIs(t, err, aiprov.ErrResponseTooLarge)
}

// TestOneKeyPerRequest — KeyFunc is read exactly once per Send, and the header carries that read
// (Codex C review, P3): a rotation between two reads inside one request must be impossible.
//
// MUTATION: read the key again for the header → red: two reads, and the header carries the second.
func TestOneKeyPerRequest(t *testing.T) {
	reads := 0
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()
	c := New(Config{Provider: "openrouter", BaseURL: srv.URL, Dialect: DialectOpenRouter, KeyFunc: func() string {
		reads++
		return fmt.Sprintf("k%d", reads)
	}})
	_, err := c.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	require.NoError(t, err)
	require.Equal(t, 1, reads, "one read per request")
	require.Equal(t, "Bearer k1", seen)
}

// ─── refusals before the wire ───────────────────────────────────────────────────────────────────

// TestRefusalsBeforeTheWire — every case is OUR mistake or our missing key; none reaches the provider,
// none is engaged, none is weather.
//
// MUTATION: drop the MaxImageParts check → "too many pictures" reaches the provider → red; drop the
// empty-model check → red.
func TestRefusalsBeforeTheWire(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(okBody))
	tooMany := make([]string, MaxImageParts+1)
	for i := range tooMany {
		tooMany[i] = "https://media.grbpwr.com/x.png"
	}
	cases := []struct {
		name   string
		client *Client
		model  string
		req    aiprov.ChatRequest
		opt    Options
		code   string
		want   string
	}{
		{"no key func", New(Config{Provider: "openrouter", BaseURL: srv.URL, Dialect: DialectOpenRouter}), "m",
			aiprov.ChatRequest{User: "u"}, Options{}, aiprov.CodeNotConfigured, "openrouter: no API key is set"},
		{"blank key", New(Config{Provider: "openrouter", BaseURL: srv.URL, KeyFunc: key("   ")}), "m",
			aiprov.ChatRequest{User: "u"}, Options{}, aiprov.CodeNotConfigured, "openrouter: no API key is set"},
		{"nil client", nil, "m", aiprov.ChatRequest{User: "u"}, Options{}, aiprov.CodeNotConfigured, "ai: no API key is set"},
		{"empty model", newOpenRouter(srv.URL), "  ", aiprov.ChatRequest{User: "u"}, Options{}, aiprov.CodeBadRequest, "openrouter: a completion needs a model slug"},
		{"pictures without a prompt", newOpenRouter(srv.URL), "m", aiprov.ChatRequest{User: "  ", ImageURLs: []string{"https://x/y.png"}}, Options{},
			aiprov.CodeBadRequest, "openrouter: a multimodal request needs a prompt, pictures alone say nothing"},
		{"parts without a prompt", newOpenRouter(srv.URL), "m", aiprov.ChatRequest{User: ""}, Options{PartsAlways: true},
			aiprov.CodeBadRequest, "openrouter: a multimodal request needs a prompt"},
		{"too many pictures", newOpenRouter(srv.URL), "m", aiprov.ChatRequest{User: "p", ImageURLs: tooMany}, Options{},
			aiprov.CodeBadRequest, "openrouter: 17 pictures exceeds the 16-picture limit for one request"},
		{"not a url", newOpenRouter(srv.URL), "m", aiprov.ChatRequest{User: "p", ImageURLs: []string{"file:///etc/passwd"}}, Options{},
			aiprov.CodeBadRequest, "openrouter: picture 1: picture address must be http(s)://"},
		{"empty address", newOpenAI(srv.URL), "m", aiprov.ChatRequest{User: "p", ImageURLs: []string{"https://x/1.png", "   "}}, Options{},
			aiprov.CodeBadRequest, "openai: picture 2: empty picture address"},
		{"data uri without payload", newOpenRouter(srv.URL), "m", aiprov.ChatRequest{User: "p", ImageURLs: []string{"data:image/png"}}, Options{},
			aiprov.CodeBadRequest, "openrouter: picture 1: data URI carries no payload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.client.Send(context.Background(), tc.model, tc.req, tc.opt)
			require.Nil(t, res)
			ce := callErr(t, err)
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

// TestChatIsTheChatterSeam — through the interface the router will hold.
func TestChatIsTheChatterSeam(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(`{"id":"gen-1","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	var chatter aiprov.Chatter = newOpenRouter(srv.URL)
	res, err := chatter.Chat(context.Background(), "m", aiprov.ChatRequest{System: "s", User: "u"})
	require.NoError(t, err)
	require.Equal(t, &aiprov.ChatResult{Text: "ok", FinishReason: "stop", Provider: "openrouter", Model: "m", RequestID: "gen-1", Engaged: true}, res)
	require.False(t, errors.Is(err, aiprov.ErrNotConfigured))
}
