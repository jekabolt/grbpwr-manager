package runblob

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

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/endpoints"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ─── fixtures ────────────────────────────────────────────────────────────────────────────────────
//
// FROM THE DOCS (11-PROVIDER-RESEARCH.md §runblob, the Kling pages read 2026-09-27): the submit answer
// {generation_id, status:"pending", price:"0.2900" | "calculating", description}; the status answer
// {status, video_url, model, error}; the 401 body {"detail":"Invalid API key"}; Bearer auth.
//
// ⚠ UNVERIFIED (G-06) — written here without a live answer, each to be replaced by a recorded one:
//   - the VALUES of description, video_url and the status word "completed" (the docs show the fields);
//   - that generation_id is a uuid (the probe assumed the same);
//   - every request body field (model, prompt, duration, aspect_ratio) — the adapter passes the
//     caller's map through untouched, so the goldens pin the marshalling, not runblob's schema;
//   - the veo family (named on the landing page only);
//   - every error body but the 401: the 404 {"detail":"Generation not found"}, the FastAPI-style
//     422 {"detail":[{"loc":…,"msg":…}]}, and the status codes 402 / 422 / 429 / 5xx themselves.

const genID = "3f2b1c4e-8a7d-4e21-9b0c-5d6e7f8a9b10"

// zeroGeneration is the probe's id (probe.runblobZeroGeneration): a status read of it is the key check.
const zeroGeneration = "00000000-0000-0000-0000-000000000000"

const okSubmitBody = `{"generation_id":"` + genID + `","status":"pending","price":"0.2900",` +
	`"description":"Kling 2.5 Turbo, 5 s, 16:9"}`

const okStatusBody = `{"status":"completed","video_url":"https://cdn.runblob.io/v/abc.mp4",` +
	`"model":"kling_2.5_turbo","error":null}`

func detailBody(msg string) string { return `{"detail":"` + msg + `"}` }

// recorder is a fake runblob that records every request and answers with reply.
type recorder struct {
	mu      sync.Mutex
	methods []string
	paths   []string
	queries []string
	bodies  []string
	headers []http.Header
}

func (rec *recorder) server(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.methods = append(rec.methods, r.Method)
		rec.paths = append(rec.paths, r.URL.EscapedPath())
		rec.queries = append(rec.queries, r.URL.RawQuery)
		rec.bodies = append(rec.bodies, string(b))
		rec.headers = append(rec.headers, r.Header.Clone())
		rec.mu.Unlock()
		reply(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (rec *recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.paths)
}

func answer(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func key(k string) func() string { return func() string { return k } }

// testKey is the stand's key. Not "k": the scrub would find it inside the provider's own words
// ("Invalid API key") and the sentences under test would read «[key]».
const testKey = "rb-test-0001"

// newAt is a client pointed at a stand instead of endpoints.RunblobHost.
func newAt(url string, base time.Duration) *Client {
	c := New(Config{KeyFunc: key(testKey), HTTPTimeout: base})
	c.base = url
	return c
}

func callErr(t *testing.T, err error) *aiprov.CallError {
	t.Helper()
	require.Error(t, err)
	ce, ok := aiprov.AsCallError(err)
	require.True(t, ok, "every failure is a *aiprov.CallError, got %T: %v", err, err)
	require.Equal(t, entity.AIProviderRunblob, ce.Provider)
	return ce
}

func klingBody() map[string]any {
	return map[string]any{"model": "kling_2.5_turbo", "prompt": "a coat on a hanger", "duration": 5}
}

// ─── the surface ─────────────────────────────────────────────────────────────────────────────────

// TestSurface — the provider key, the constant host, the budget base, the key hook, nil-safety.
//
// MUTATION (measured red → green): Enabled returns `c != nil` (the key not consulted) → red.
func TestSurface(t *testing.T) {
	c := New(Config{})
	require.Equal(t, "runblob", c.Provider())
	require.Equal(t, entity.AIProviderRunblob, c.Provider())
	require.Equal(t, endpoints.RunblobHost, c.BaseURL())
	require.Equal(t, "https://platform.runblob.io", c.BaseURL())
	require.Equal(t, aiprov.DefaultBudgetBase, c.CompletionBase(), "no timeout = the default base")
	require.False(t, c.Enabled(), "no KeyFunc = disabled")
	require.Equal(t, 7*time.Second, New(Config{HTTPTimeout: 7 * time.Second}).CompletionBase())

	k := ""
	c = New(Config{KeyFunc: func() string { return k }})
	require.False(t, c.Enabled())
	k = "  rb-key  "
	require.True(t, c.Enabled(), "a key saved in the panel enables the adapter without a rebuild")
	k = "   "
	require.False(t, c.Enabled(), "a blank key is no key")

	var nilClient *Client
	require.False(t, nilClient.Enabled())
	require.Equal(t, "", nilClient.BaseURL())
	require.Equal(t, aiprov.DefaultBudgetBase, nilClient.CompletionBase())
	require.Equal(t, "runblob", nilClient.Provider())
	_, err := nilClient.Submit(context.Background(), FamilyKling, klingBody())
	require.Equal(t, aiprov.CodeNotConfigured, callErr(t, err).Code)
	_, err = nilClient.Status(context.Background(), FamilyKling, genID)
	require.Equal(t, aiprov.CodeNotConfigured, callErr(t, err).Code)
}

// TestNoPurposeRoutesToRunblob — D-05 as a fact of the vocabulary, not of a comment: runblob serves
// video only, and no purpose asks for video, so a route row naming it is refused when it is saved
// (store/ai: provider_cannot_serve) and skipped when it is read (registry: AIProviderServes). When
// the owner names a video purpose (B-27b) this test is the one to change, on purpose.
//
// MUTATION (measured red → green): AICapabilityImage added to runblob's row of
// entity.AIProviderCapabilities → red (the capability pin; the loop reads image.generate as routable).
func TestNoPurposeRoutesToRunblob(t *testing.T) {
	require.Equal(t, []string{entity.AICapabilityVideo}, entity.AIProviderCapabilities(New(Config{}).Provider()))
	for _, purpose := range entity.AIPurposes() {
		capability := entity.AIPurposeCapability(purpose)
		require.NotEmpty(t, capability, purpose)
		require.False(t, entity.AIProviderServes(entity.AIProviderRunblob, capability),
			"purpose %s (%s) would route to runblob — D-05 says no purpose does until the owner names one", purpose, capability)
	}
}

// ─── goldens ─────────────────────────────────────────────────────────────────────────────────────

// TestSubmitGoldenPerFamily pins the EXACT request per family: method, path, query, headers and body
// bytes (the caller's map, keys sorted by encoding/json — nothing added, nothing removed). And the
// answer: id, status, price as a number, description, engaged.
//
// MUTATION (measured red → green): the path built as "/v1/kling/generate" whatever the family → the
// veo row red.
func TestSubmitGoldenPerFamily(t *testing.T) {
	cases := []struct {
		family string
		body   map[string]any
		want   string
	}{
		{FamilyKling, klingBody(),
			`{"duration":5,"model":"kling_2.5_turbo","prompt":"a coat on a hanger"}`},
		{FamilyVeo, map[string]any{"model": "veo-3-fast", "prompt": "p", "aspect_ratio": "16:9",
			"callback_url": "https://backend.grbpwr.com/hook?x=1&y=<2>"},
			// encoding/json escapes & < > inside strings: the same JSON value, pinned as it goes out.
			`{"aspect_ratio":"16:9","callback_url":"https://backend.grbpwr.com/hook?x=1` + "\\u0026y=\\u003c2\\u003e" + `","model":"veo-3-fast","prompt":"p"}`},
	}
	for _, tc := range cases {
		t.Run(tc.family, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(http.StatusCreated, okSubmitBody))
			sub, err := newAt(srv.URL+"/", 2*time.Second).Submit(context.Background(), tc.family, tc.body)
			require.NoError(t, err)
			require.Equal(t, 1, rec.count())
			require.Equal(t, http.MethodPost, rec.methods[0])
			require.Equal(t, "/v1/"+tc.family+"/generate", rec.paths[0], "one trailing slash of the base is not a second segment")
			require.Empty(t, rec.queries[0])
			require.Equal(t, tc.want, rec.bodies[0])
			h := rec.headers[0]
			require.Equal(t, "Bearer "+testKey, h.Get("Authorization"))
			require.Equal(t, "application/json", h.Get("Content-Type"))
			require.Equal(t, "application/json", h.Get("Accept"))

			require.Equal(t, &Submission{
				ID: genID, Status: "pending",
				PriceUSD:    decimal.NullDecimal{Decimal: decimal.RequireFromString("0.2900"), Valid: true},
				Description: "Kling 2.5 Turbo, 5 s, 16:9", Engaged: true,
			}, sub)
		})
	}
}

// TestStatusReadsTheGeneration pins the status GET (method, path, no body, no Content-Type, the
// Bearer) and the fields it reads — including a failed generation's error text in its three shapes.
//
// MUTATION (measured red → green): VideoURL's tag misspelled `json:"videoUrl"` → red.
func TestStatusReadsTheGeneration(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Generation
	}{
		{"completed", okStatusBody, Generation{Status: "completed",
			VideoURL: "https://cdn.runblob.io/v/abc.mp4", Model: "kling_2.5_turbo"}},
		{"pending, no video yet", `{"status":"pending","video_url":null,"model":"kling_2.5_turbo","error":null}`,
			Generation{Status: "pending", Model: "kling_2.5_turbo"}},
		{"failed, error as a string", `{"status":"failed","video_url":null,"model":"kling_3","error":"content policy"}`,
			Generation{Status: "failed", Model: "kling_3", Error: "content policy"}},
		{"failed, error as an object", `{"status":"failed","model":"kling_3","error":{"code":"x","message":"upstream timeout"}}`,
			Generation{Status: "failed", Model: "kling_3", Error: "upstream timeout"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(http.StatusOK, tc.body))
			g, err := newAt(srv.URL, 2*time.Second).Status(context.Background(), FamilyKling, genID)
			require.NoError(t, err)
			require.Equal(t, &tc.want, g)
			require.Equal(t, 1, rec.count())
			require.Equal(t, http.MethodGet, rec.methods[0])
			require.Equal(t, "/v1/kling/generations/"+genID, rec.paths[0])
			require.Empty(t, rec.bodies[0], "a GET carries no body")
			require.Empty(t, rec.headers[0].Get("Content-Type"))
			require.Equal(t, "Bearer "+testKey, rec.headers[0].Get("Authorization"))
		})
	}
}

// ─── the status matrix ───────────────────────────────────────────────────────────────────────────

// TestStatusMatrix — every refusal, on both requests, by status alone; none engaged. The one split is
// the 404: on the submit it is the FAMILY (ErrModelUnavailable, model_unknown — a setting), on the
// status read it is the ID (ErrGenerationNotFound, not_found — what the probe reads on the zero uuid
// as «key accepted»; a model_unknown there would tell a person to fix a route that is fine).
//
// MUTATION (measured red → green): the submit-only guard dropped from the 404 row of statusError
// (`case status == http.StatusNotFound:` first) → the status-read 404 reads model_unknown → red.
func TestStatusMatrix(t *testing.T) {
	cases := []struct {
		status    int
		body      string
		code      string
		retryable bool
		msg       string
	}{
		{400, detailBody("bad"), aiprov.CodeBadRequest, false, "bad"},
		{401, detailBody("Invalid API key"), aiprov.CodeKeyRejected, false, "Invalid API key"},
		{402, detailBody("Insufficient balance"), aiprov.CodeOutOfCredits, false, "Insufficient balance"},
		{403, detailBody("forbidden"), aiprov.CodeKeyRejected, false, "forbidden"},
		{404, detailBody("Not Found"), "", false, "Not Found"},
		{408, detailBody("slow"), aiprov.CodeProviderError, true, "slow"},
		{422, `{"detail":[{"loc":["body","prompt"],"msg":"field required","type":"missing"},{"loc":["body","duration"],"msg":"must be 5 or 10"}]}`,
			aiprov.CodeBadRequest, false, "field required; must be 5 or 10"},
		{429, detailBody("slow down"), aiprov.CodeRateLimited, true, "slow down"},
		{500, `<html>oops</html>`, aiprov.CodeProviderError, true, "<html>oops</html>"},
		{503, detailBody("busy"), aiprov.CodeProviderError, true, "busy"},
	}
	for _, tc := range cases {
		for _, op := range []string{"submit", "status"} {
			t.Run(fmt.Sprintf("%d/%s", tc.status, op), func(t *testing.T) {
				rec := &recorder{}
				srv := rec.server(t, answer(tc.status, tc.body))
				c := newAt(srv.URL, 2*time.Second)
				var err error
				if op == "submit" {
					var sub *Submission
					sub, err = c.Submit(context.Background(), FamilyKling, klingBody())
					require.Nil(t, sub)
				} else {
					var g *Generation
					g, err = c.Status(context.Background(), FamilyKling, zeroGeneration)
					require.Nil(t, g)
					require.Equal(t, "/v1/kling/generations/"+zeroGeneration, rec.paths[0],
						"the probe's key check (probe.go: runblob row) reads exactly this path")
				}
				ce := callErr(t, err)
				require.Equal(t, tc.status, ce.HTTPStatus)
				require.False(t, ce.Engaged, "HTTP %d is a refusal at the gate, not a charge", tc.status)
				require.False(t, aiprov.Engaged(err))
				require.Equal(t, tc.retryable, ce.Retryable)

				if tc.status != http.StatusNotFound {
					require.Equal(t, tc.code, ce.Code)
					require.Equal(t, fmt.Sprintf("runblob: API error (HTTP %d): %s", tc.status, tc.msg), err.Error())
					require.NotErrorIs(t, err, aiprov.ErrModelUnavailable)
					require.NotErrorIs(t, err, ErrGenerationNotFound)
					return
				}
				if op == "submit" {
					require.Equal(t, aiprov.CodeModelUnknown, ce.Code, "a 404 on the submit is the family: a setting")
					require.ErrorIs(t, err, aiprov.ErrModelUnavailable)
					require.NotErrorIs(t, err, ErrGenerationNotFound)
					require.Equal(t, "runblob: the configured model is not available at the provider: API error (HTTP 404): Not Found", err.Error())
					return
				}
				require.Equal(t, aiprov.CodeNotFound, ce.Code, "a 404 on the status read is the id, never model_unknown")
				require.ErrorIs(t, err, ErrGenerationNotFound)
				require.NotErrorIs(t, err, aiprov.ErrModelUnavailable)
				require.Equal(t, "runblob: no such generation: API error (HTTP 404): Not Found", err.Error())
			})
		}
	}
}

// TestRedirectsAreRefused — a 3xx is the answer, never followed: the Bearer and the prompt must not
// travel to wherever Location points.
//
// MUTATION (measured red → green): CheckRedirect removed from New → red: the second host receives the
// re-POSTed request.
func TestRedirectsAreRefused(t *testing.T) {
	elsewhere := &recorder{}
	target := elsewhere.server(t, answer(http.StatusCreated, okSubmitBody))
	rec := &recorder{}
	srv := rec.server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/v1/kling/generate")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	sub, err := newAt(srv.URL, 2*time.Second).Submit(context.Background(), FamilyKling, klingBody())
	require.Nil(t, sub)
	ce := callErr(t, err)
	require.Equal(t, http.StatusTemporaryRedirect, ce.HTTPStatus)
	require.Equal(t, aiprov.CodeProviderError, ce.Code)
	require.False(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.Equal(t, 1, rec.count())
	require.Zero(t, elsewhere.count(), "the redirect was followed: the key and the prompt went to another host")
}

// TestTheProviderTextIsBoundedAndNeverTheKey — a gateway that echoes the key in a long error page (or
// in a generation's error) puts neither the key nor the page into the sentence.
//
// MUTATION (measured red → green): the key scrub removed from boundedMessage → red.
func TestTheProviderTextIsBoundedAndNeverTheKey(t *testing.T) {
	const secret = "rb_live_SECRET_KEY_VALUE"
	for _, tc := range []struct {
		name string
		body string
	}{
		{"detail", detailBody("Invalid API key " + secret + " " + strings.Repeat("é", 1000))},
		{"raw page", "<html>" + strings.Repeat("x", 280) + secret + strings.Repeat("y", 1000) + "</html>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(http.StatusUnauthorized, tc.body))
			c := New(Config{KeyFunc: key(secret)})
			c.base = srv.URL
			_, err := c.Submit(context.Background(), FamilyKling, klingBody())
			ce := callErr(t, err)
			require.Equal(t, aiprov.CodeKeyRejected, ce.Code)
			require.NotContains(t, err.Error(), "SECRET", "the key never reaches a sentence")
			require.NotContains(t, err.Error(), "rb_live", "not even the prefix a cut before the scrub leaves standing")
			msg := strings.TrimPrefix(err.Error(), "runblob: API error (HTTP 401): ")
			require.LessOrEqual(t, len([]rune(msg)), errorMessageLimit+1, "bounded to %d runes and an ellipsis", errorMessageLimit)
			require.True(t, strings.HasSuffix(msg, "…"))
		})
	}
	t.Run("a generation's error", func(t *testing.T) {
		rec := &recorder{}
		srv := rec.server(t, answer(http.StatusOK, `{"status":"failed","error":"bad header Bearer `+secret+`"}`))
		c := New(Config{KeyFunc: key(secret)})
		c.base = srv.URL
		g, err := c.Status(context.Background(), FamilyKling, genID)
		require.NoError(t, err)
		require.Equal(t, "bad header Bearer [key]", g.Error)
	})
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

// TestASubmitWrittenThenTimedOutIsEngaged — the POST was written, the deadline expired: the job may be
// queued and billed right now. Engaged, NOT retryable (a resubmit would buy a second generation beside
// a first whose id is lost), Code timeout.
//
// MUTATION (measured red → green): `engaged := false` in place of `submit && wroteRequest()` on the Do
// branch → red.
func TestASubmitWrittenThenTimedOutIsEngaged(t *testing.T) {
	url, reached := hangingProvider(t)
	start := time.Now()
	sub, err := newAt(url, 200*time.Millisecond).Submit(context.Background(), FamilyKling, klingBody())
	require.Less(t, time.Since(start), 5*time.Second, "the call must end on its own deadline")
	select {
	case <-reached:
	default:
		t.Fatal("the provider never got the request — the probe measures something else")
	}
	require.Nil(t, sub)
	ce := callErr(t, err)
	require.True(t, ce.Engaged, "a written submit is money")
	require.True(t, aiprov.Engaged(err))
	require.False(t, ce.Retryable, "an engaged failure is never weather")
	require.Equal(t, aiprov.CodeTimeout, ce.Code)
	require.Zero(t, ce.HTTPStatus)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, strings.HasPrefix(err.Error(), "runblob: request failed: "), err.Error())
}

// TestASubmitToARefusedDialIsFree — nothing was written, nobody pays, the next attempt may reach a
// live provider.
//
// MUTATION (measured red → green): `engaged := true` on the Do branch → red.
func TestASubmitToARefusedDialIsFree(t *testing.T) {
	_, err := newAt(deadAddr(t), 2*time.Second).Submit(context.Background(), FamilyKling, klingBody())
	ce := callErr(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded, "a dead address must refuse, not time out")
	require.False(t, ce.Engaged)
	require.True(t, ce.Retryable)
	require.Equal(t, aiprov.CodeTransport, ce.Code)
}

// TestAStatusReadThatTimesOutIsFree — the GET WAS written and then hung: it bought nothing. Not
// engaged, retryable (looking again is free), Code timeout.
//
// MUTATION (measured red → green): `engaged := wroteRequest()` (the submit guard dropped) → red.
func TestAStatusReadThatTimesOutIsFree(t *testing.T) {
	url, reached := hangingProvider(t)
	g, err := newAt(url, 200*time.Millisecond).Status(context.Background(), FamilyKling, genID)
	select {
	case <-reached:
	default:
		t.Fatal("the provider never got the request — the probe measures something else")
	}
	require.Nil(t, g)
	ce := callErr(t, err)
	require.False(t, ce.Engaged, "a GET buys nothing, however it ends")
	require.False(t, aiprov.Engaged(err))
	require.True(t, ce.Retryable)
	require.Equal(t, aiprov.CodeTimeout, ce.Code)
}

// ─── the price ───────────────────────────────────────────────────────────────────────────────────

// TestSubmitPrice — the provider's number, NULL when it gave none, and an engaged bad answer when it
// gave something that is not a price (the generation still runs: the partial carries its id).
//
// MUTATION (measured red → green): the "calculating" word removed from parsePrice → the calculating row
// becomes an error → red.
func TestSubmitPrice(t *testing.T) {
	body := func(price string) string {
		if price == "" {
			return `{"generation_id":"` + genID + `","status":"pending","description":"d"}`
		}
		return `{"generation_id":"` + genID + `","status":"pending","price":` + price + `,"description":"d"}`
	}
	cases := []struct {
		name  string
		price string // raw JSON; "" = the key is absent
		want  string // "" = NULL
		bad   string // the sentence of an engaged bad answer
	}{
		{name: "a decimal string", price: `"0.2900"`, want: "0.29"},
		{name: "calculating", price: `"calculating"`},
		{name: "Calculating, any case", price: `"Calculating"`},
		{name: "absent", price: ""},
		{name: "null", price: `null`},
		{name: "an empty string", price: `""`},
		{name: "a JSON number", price: `1.25`, want: "1.25"},
		{name: "zero is not a known price", price: `"0.0000"`},
		{name: "not a number", price: `"abc"`, bad: `runblob: the kling submit answered with a price that is not a number: "abc"`},
		{name: "negative", price: `"-0.10"`, bad: `runblob: the kling submit answered with a negative price: "-0.10"`},
		{name: "an object", price: `{"usd":1}`, bad: `runblob: the kling submit answered with a price that is not a number: "{\"usd\":1}"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(http.StatusCreated, body(tc.price)))
			sub, err := newAt(srv.URL, 2*time.Second).Submit(context.Background(), FamilyKling, klingBody())
			require.NotNil(t, sub, "an accepted submit always hands back what was bought")
			require.True(t, sub.Engaged)
			require.Equal(t, genID, sub.ID, "the generation runs whatever its price says: its id must reach the caller")
			if tc.bad != "" {
				ce := callErr(t, err)
				require.True(t, ce.Engaged, "the provider accepted the submit: money may have moved")
				require.False(t, ce.Retryable)
				require.Equal(t, aiprov.CodeProviderError, ce.Code)
				require.Equal(t, http.StatusCreated, ce.HTTPStatus)
				require.Equal(t, tc.bad, err.Error())
				require.False(t, sub.PriceUSD.Valid, "a price that is not one is never booked")
				return
			}
			require.NoError(t, err)
			if tc.want == "" {
				require.False(t, sub.PriceUSD.Valid, "unknown is NULL, never zero")
				return
			}
			require.True(t, sub.PriceUSD.Valid)
			require.True(t, sub.PriceUSD.Decimal.Equal(decimal.RequireFromString(tc.want)),
				"%s != %s", sub.PriceUSD.Decimal, tc.want)
		})
	}
}

// TestAnUnusableSubmitAnswerIsEngaged — the provider accepted the submit (a 2xx): whatever is wrong
// with the answer is ours to carry, and the money may have moved. A decoded one comes back WITH the
// partial Submission, so its price reaches the caller.
//
// MUTATION (measured red → green): the `sub.ID == ""` case removed from Submit → the no-id row reads
// provider_error instead of empty_answer → red.
func TestAnUnusableSubmitAnswerIsEngaged(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		code     string
		sentence string
		partial  *Submission
	}{
		{name: "broken envelope", status: 201, body: `{"generation_id":`, code: aiprov.CodeProviderError,
			sentence: "runblob: could not decode the kling submit answer: "},
		{name: "no body at all", status: 200, body: ``, code: aiprov.CodeProviderError,
			sentence: "runblob: could not decode the kling submit answer: "},
		{name: "no generation_id", status: 201, body: `{"status":"pending","price":"0.2900","description":"d"}`,
			code: aiprov.CodeEmptyAnswer, sentence: "runblob: the kling submit was accepted (HTTP 201) with no generation_id",
			partial: &Submission{Status: "pending", Description: "d", Engaged: true,
				PriceUSD: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.2900"), Valid: true}}},
		{name: "a blank generation_id", status: 200, body: `{"generation_id":"  ","status":"pending","price":"calculating"}`,
			code: aiprov.CodeEmptyAnswer, sentence: "runblob: the kling submit was accepted (HTTP 200) with no generation_id",
			partial: &Submission{Status: "pending", Engaged: true}},
		{name: "a generation_id that is not a uuid", status: 201, body: `{"generation_id":"../../v1/x","status":"pending","price":"0.5"}`,
			code: aiprov.CodeProviderError, sentence: `runblob: the kling submit returned a generation_id that is not a uuid: "../../v1/x"`,
			partial: &Submission{Status: "pending", Engaged: true,
				PriceUSD: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.5"), Valid: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(tc.status, tc.body))
			sub, err := newAt(srv.URL, 2*time.Second).Submit(context.Background(), FamilyKling, klingBody())
			ce := callErr(t, err)
			require.True(t, ce.Engaged, "a 2xx means the submit was accepted and may be billed")
			require.True(t, aiprov.Engaged(err))
			require.False(t, ce.Retryable)
			require.Equal(t, tc.code, ce.Code)
			require.Equal(t, tc.status, ce.HTTPStatus)
			require.True(t, strings.HasPrefix(err.Error(), tc.sentence), "%q", err.Error())
			require.Equal(t, tc.partial, sub)
		})
	}
}

// TestAnUnusableStatusAnswerIsFree — a 2xx status answer that cannot be read bought nothing and can be
// read again: not engaged, retryable.
//
// MUTATION (measured red → green): the broken-envelope branch of Status marked engaged → red.
func TestAnUnusableStatusAnswerIsFree(t *testing.T) {
	cases := []struct {
		name string
		body string
		code string
	}{
		{"broken envelope", `{"status":`, aiprov.CodeProviderError},
		{"no status", `{"video_url":"https://x/y.mp4"}`, aiprov.CodeEmptyAnswer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := rec.server(t, answer(http.StatusOK, tc.body))
			g, err := newAt(srv.URL, 2*time.Second).Status(context.Background(), FamilyKling, genID)
			require.Nil(t, g)
			ce := callErr(t, err)
			require.False(t, ce.Engaged, "a GET buys nothing")
			require.True(t, ce.Retryable, "looking again is free")
			require.Equal(t, tc.code, ce.Code)
			require.Equal(t, http.StatusOK, ce.HTTPStatus)
		})
	}
}

// ─── the key, the ceiling, the refusals ─────────────────────────────────────────────────────────

// TestOneKeyPerRequest — KeyFunc is read exactly once per request, and the header carries that read;
// the next request reads it again (a key saved in the panel reaches it).
//
// MUTATION (measured red → green): the header built from `c.key()` in exchange instead of the key read
// in Submit/Status → red: two reads, the header carries the second.
func TestOneKeyPerRequest(t *testing.T) {
	reads := 0
	var seen []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, okSubmitBody)
			return
		}
		_, _ = io.WriteString(w, okStatusBody)
	}))
	defer srv.Close()
	c := New(Config{KeyFunc: func() string {
		reads++
		return fmt.Sprintf("k%d", reads)
	}})
	c.base = srv.URL

	_, err := c.Submit(context.Background(), FamilyKling, klingBody())
	require.NoError(t, err)
	require.Equal(t, 1, reads, "one read per submit")
	_, err = c.Status(context.Background(), FamilyKling, genID)
	require.NoError(t, err)
	require.Equal(t, 2, reads, "one read per status read")
	require.Equal(t, []string{"Bearer k1", "Bearer k2"}, seen)
}

// TestTheReadCeilingRefusesByName — exactly at the ceiling works; one byte over is refused as
// ErrResponseTooLarge: engaged on the submit (the answer WAS there), free and retryable on the status
// read.
//
// MUTATION (measured red → green): readCapped reads `limit` instead of `limit+1` → red: the body cut at
// the ceiling (the answer less one trailing space) PARSES, and the over-the-ceiling submit is accepted as
// a whole answer — the exact failure the extra byte exists for.
func TestTheReadCeilingRefusesByName(t *testing.T) {
	pad := func(body string, n int) string { return body + strings.Repeat(" ", n-len(body)) }

	rec := &recorder{}
	srv := rec.server(t, answer(http.StatusCreated, pad(okSubmitBody, MaxResponseBytes)))
	sub, err := newAt(srv.URL, 2*time.Second).Submit(context.Background(), FamilyKling, klingBody())
	require.NoError(t, err, "a body exactly at the ceiling must be accepted")
	require.Equal(t, genID, sub.ID)

	srv = rec.server(t, answer(http.StatusCreated, pad(okSubmitBody, MaxResponseBytes+1)))
	sub, err = newAt(srv.URL, 2*time.Second).Submit(context.Background(), FamilyKling, klingBody())
	require.Nil(t, sub)
	ce := callErr(t, err)
	require.ErrorIs(t, err, aiprov.ErrResponseTooLarge)
	require.Equal(t, aiprov.CodeTooLarge, ce.Code)
	require.True(t, ce.Engaged, "the submit's answer was there: it was accepted")
	require.False(t, ce.Retryable)
	require.Equal(t, fmt.Sprintf("runblob: read response: runblob: the provider's response exceeded the read ceiling: "+
		"generate response is larger than %d bytes", MaxResponseBytes), err.Error())

	srv = rec.server(t, answer(http.StatusOK, pad(okStatusBody, MaxResponseBytes+1)))
	g, err := newAt(srv.URL, 2*time.Second).Status(context.Background(), FamilyKling, genID)
	require.Nil(t, g)
	ce = callErr(t, err)
	require.ErrorIs(t, err, aiprov.ErrResponseTooLarge)
	require.Equal(t, aiprov.CodeTooLarge, ce.Code)
	require.False(t, ce.Engaged, "a GET buys nothing")
	require.True(t, ce.Retryable)
}

// TestARefusedStatusWithAnUnreadableBodyIsStillARefusal — a 404 on the submit whose error body runs
// past the read ceiling is still the provider's refusal at the gate: not engaged, the model-unknown
// code and sentinel intact; the sentence says the excuse could not be read, and why.
//
// MUTATION (measured red → green): the body judged before the status (the read-error branch first) →
// red: Engaged true, Code too_large, no ErrModelUnavailable.
func TestARefusedStatusWithAnUnreadableBodyIsStillARefusal(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(http.StatusNotFound, detailBody(strings.Repeat("x", MaxResponseBytes+16))))
	_, err := newAt(srv.URL, 2*time.Second).Submit(context.Background(), FamilyKling, klingBody())
	ce := callErr(t, err)
	require.ErrorIs(t, err, aiprov.ErrModelUnavailable)
	require.Equal(t, aiprov.CodeModelUnknown, ce.Code)
	require.False(t, ce.Engaged, "a refusal at the gate moved no money, however long its excuse")
	require.False(t, ce.Retryable)
	require.NotErrorIs(t, err, aiprov.ErrResponseTooLarge)
	require.Contains(t, err.Error(), "API error (HTTP 404): response body unavailable: runblob: the provider's response exceeded the read ceiling")
}

// TestRefusalsBeforeTheWire — a family off the closed list or not a plain segment, an id that is not a
// uuid, an empty or unmarshalable body, no key: each is OUR mistake, none reaches the provider (the
// stand sees nothing), none is engaged, none is weather.
//
// MUTATION (measured red → green): the uuid check removed from Status → the path-climbing ids reach
// the stand → red.
func TestRefusalsBeforeTheWire(t *testing.T) {
	rec := &recorder{}
	srv := rec.server(t, answer(http.StatusCreated, okSubmitBody))
	c := newAt(srv.URL, 2*time.Second)
	ctx := context.Background()

	type call func() error
	submit := func(family string, body map[string]any) call {
		return func() error { _, err := c.Submit(ctx, family, body); return err }
	}
	status := func(family, id string) call {
		return func() error { _, err := c.Status(ctx, family, id); return err }
	}
	cases := []struct {
		name     string
		do       call
		code     string
		sentence string
	}{
		{"submit: an unknown family", submit("sora", klingBody()), aiprov.CodeBadRequest,
			`runblob: unknown family "sora" (known: kling, veo)`},
		{"submit: an upper-case family", submit("Kling", klingBody()), aiprov.CodeBadRequest,
			`runblob: a family is lower-case letters, digits and underscores, got "Kling"`},
		{"submit: a family that climbs the path", submit("kling/../admin", klingBody()), aiprov.CodeBadRequest,
			`runblob: a family is lower-case letters, digits and underscores, got "kling/../admin"`},
		{"submit: an empty family", submit("", klingBody()), aiprov.CodeBadRequest,
			`runblob: a family is lower-case letters, digits and underscores, got ""`},
		{"submit: a nil body", submit(FamilyKling, nil), aiprov.CodeBadRequest,
			"runblob: a kling generation needs a request body"},
		{"submit: an empty body", submit(FamilyKling, map[string]any{}), aiprov.CodeBadRequest,
			"runblob: a kling generation needs a request body"},
		{"submit: a body JSON cannot carry", submit(FamilyKling, map[string]any{"x": make(chan int)}), aiprov.CodeBadRequest,
			"runblob: marshal kling request: "},
		{"status: an unknown family", status("sora", genID), aiprov.CodeBadRequest,
			`runblob: unknown family "sora" (known: kling, veo)`},
		{"status: an id that climbs the path", status(FamilyKling, "../../admin"), aiprov.CodeBadRequest,
			`runblob: a generation id must be a uuid, got "../../admin"`},
		{"status: a uuid with a query behind it", status(FamilyKling, genID+"?x=1"), aiprov.CodeBadRequest,
			`runblob: a generation id must be a uuid, got "` + genID + `?x=1"`},
		{"status: a uuid with a slash behind it", status(FamilyKling, genID+"/x"), aiprov.CodeBadRequest,
			`runblob: a generation id must be a uuid, got "` + genID + `/x"`},
		{"status: an empty id", status(FamilyKling, ""), aiprov.CodeBadRequest,
			`runblob: a generation id must be a uuid, got ""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.do()
			ce := callErr(t, err)
			require.Equal(t, tc.code, ce.Code)
			require.False(t, ce.Engaged)
			require.False(t, ce.Retryable)
			require.Zero(t, ce.HTTPStatus)
			require.True(t, strings.HasPrefix(err.Error(), tc.sentence), "%q", err.Error())
		})
	}

	t.Run("no key", func(t *testing.T) {
		off := New(Config{KeyFunc: key("")})
		off.base = srv.URL
		_, err := off.Submit(ctx, FamilyKling, klingBody())
		ce := callErr(t, err)
		require.Equal(t, aiprov.CodeNotConfigured, ce.Code)
		require.ErrorIs(t, err, aiprov.ErrNotConfigured)
		require.False(t, ce.Engaged)
		_, err = off.Status(ctx, FamilyKling, genID)
		require.Equal(t, aiprov.CodeNotConfigured, callErr(t, err).Code)
	})
	require.Zero(t, rec.count(), "a refused request reached the provider")
}
