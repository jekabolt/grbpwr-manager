package reconcile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	hosts "github.com/jekabolt/grbpwr-manager/internal/aiprov/endpoints"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/probe"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Every test names the mutation that turns it red. No test here reaches a provider or a database: the
// client delivers every request to an httptest server, and the store is a recorder.

// fixedNow — 10:30 UTC on 28.09: yesterday = 27.09, today = 28.09, tomorrow = 29.09.
var fixedNow = time.Date(2026, 9, 28, 10, 30, 0, 0, time.UTC)

// The four keys, distinct per provider so a test can tell which one went where.
const (
	openAIAdminKey    = "sk-admin-OPENAI-cost-key-1234"
	anthropicAdminKey = "sk-ant-admin01-ANTHROPIC-5678"
	falAdminKey       = "fal-admin-FAL-key-9012"
	openRouterAPIKey  = "sk-or-v1-OPENROUTER-key-3456"
)

// ───────────────────────── fixtures (bytes → rows) ─────────────────────────
//
// UNVERIFIED (G-05) — ALL FOUR BODIES ARE WRITTEN FROM MEMORY of the providers' docs, not recorded
// from a live answer. G-05 replaces each with the first live answer on beta and records it in REVIEW.

// openAICostsBody — GET /v1/organization/costs?start_time=<27.09 00:00 UTC>&bucket_width=1d&limit=2.
// UNVERIFIED (G-05).
const openAICostsBody = `{"object":"page","data":[
 {"object":"bucket","start_time":1790467200,"end_time":1790553600,"results":[
   {"object":"organization.costs.result","amount":{"value":0.06,"currency":"usd"},"line_item":null,"project_id":null},
   {"object":"organization.costs.result","amount":{"value":1.23456789,"currency":"usd"},"line_item":null,"project_id":null}]},
 {"object":"bucket","start_time":1790553600,"end_time":1790640000,"results":[]}
],"has_more":false,"next_page":null}`

// anthropicCostBody — GET /v1/organizations/cost_report; amounts are CENTS as decimal strings.
// UNVERIFIED (G-05): cents, not dollars, is the plan's reading of the docs.
const anthropicCostBody = `{"data":[
 {"starting_at":"2026-09-27T00:00:00Z","ending_at":"2026-09-28T00:00:00Z","results":[
   {"currency":"USD","amount":"12345.6789","workspace_id":null,"description":null,"cost_type":"tokens","model":"claude-sonnet-4","token_type":"uncached_input_tokens"},
   {"currency":"USD","amount":"55","workspace_id":null,"description":null}]},
 {"starting_at":"2026-09-28T00:00:00Z","ending_at":"2026-09-29T00:00:00Z","results":[{"currency":"USD","amount":"10"}]}
],"has_more":false,"next_page":null}`

// falUsageBody — GET /v1/models/usage?…&timeframe=day. One row names its amount cost_total (the plan),
// one cost (the API reference as remembered); tomorrow's empty bucket is outside the window.
// UNVERIFIED (G-05): the least certain shape of the four.
const falUsageBody = `{"next_cursor":null,"has_more":false,"time_series":[
 {"bucket":"2026-09-27T00:00:00Z","results":[
   {"endpoint_id":"fal-ai/birefnet/v2","unit":"image","quantity":3,"cost_total":0.03,"currency":"USD"},
   {"endpoint_id":"fal-ai/trellis","unit":"request","quantity":1,"cost_total":1.5,"currency":"USD"}]},
 {"bucket":"2026-09-28T00:00:00Z","results":[
   {"endpoint_id":"fal-ai/birefnet/v2","unit":"image","quantity":25,"unit_price":0.01,"cost":0.25,"currency":"USD"}]},
 {"bucket":"2026-09-29T00:00:00Z","results":[]}
]}`

// openRouterKeyBody — GET /api/v1/key: usage_daily is the running total of the current UTC day.
// UNVERIFIED (G-05).
const openRouterKeyBody = `{"data":{"label":"grbpwr","usage":42.1,"usage_daily":1.234,"usage_weekly":7.5,` +
	`"usage_monthly":30.2,"limit":null,"limit_remaining":null,"is_free_tier":false}}`

type wantRow struct{ day, usd string }

type fixture struct {
	provider string
	path     string // the provider path the rig answers on
	url      string // the exact URL the fetch must build at fixedNow
	headers  map[string]string
	body     string
	rows     []wantRow
}

func fixtures() []fixture {
	return []fixture{
		{entity.AIProviderOpenAI, "/v1/organization/costs",
			"https://api.openai.com/v1/organization/costs?start_time=1790467200&bucket_width=1d&limit=2",
			map[string]string{"Authorization": "Bearer " + openAIAdminKey},
			openAICostsBody, []wantRow{{"2026-09-27", "1.294568"}, {"2026-09-28", "0"}}},
		{entity.AIProviderAnthropic, "/v1/organizations/cost_report",
			"https://api.anthropic.com/v1/organizations/cost_report?starting_at=2026-09-27T00:00:00Z&ending_at=2026-09-29T00:00:00Z&bucket_width=1d",
			map[string]string{"X-Api-Key": anthropicAdminKey, "Anthropic-Version": "2023-06-01"},
			anthropicCostBody, []wantRow{{"2026-09-27", "124.006789"}, {"2026-09-28", "0.1"}}},
		{entity.AIProviderOpenRouter, "/api/v1/key",
			"https://openrouter.ai/api/v1/key",
			map[string]string{"Authorization": "Bearer " + openRouterAPIKey},
			openRouterKeyBody, []wantRow{{"2026-09-28", "1.234"}}},
		{entity.AIProviderFal, "/v1/models/usage",
			"https://api.fal.ai/v1/models/usage?start=2026-09-27T00:00:00Z&end=2026-09-29T00:00:00Z&timeframe=day&timezone=UTC",
			map[string]string{"Authorization": "Key " + falAdminKey},
			falUsageBody, []wantRow{{"2026-09-27", "1.53"}, {"2026-09-28", "0.25"}}},
	}
}

func fixtureOf(t *testing.T, provider string) fixture {
	t.Helper()
	for _, f := range fixtures() {
		if f.provider == provider {
			return f
		}
	}
	t.Fatalf("no fixture for %s", provider)
	return fixture{}
}

// authHeaders — every header any fetch authenticates with.
var authHeaders = []string{"Authorization", "X-Api-Key", "Anthropic-Version"}

// ───────────────────────── the rig ─────────────────────────

type answer struct {
	status int
	body   string
	header http.Header
}

type sent struct {
	Method      string
	URL         string
	Header      http.Header
	HasBody     bool
	Deadline    time.Time
	HasDeadline bool
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// rig is one httptest server. Its client RECORDS the request a fetch built — the real provider URL
// and headers — and then delivers it to the server instead of the provider (the probe's rig).
type rig struct {
	handler http.Handler

	mu      sync.Mutex
	answers map[string]answer // by path; a path with none answers 404
	sent    []sent
	// block holds every request until its context ends, then waits linger before failing it.
	block  bool
	linger time.Duration
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{answers: map[string]answer{}}
	for _, f := range fixtures() {
		r.answers[f.path] = answer{status: http.StatusOK, body: f.body}
	}
	r.handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		a, ok := r.answers[req.URL.Path]
		r.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		for k, v := range a.header {
			w.Header()[k] = v
		}
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	})
	return r
}

func (r *rig) answer(path string, a answer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[path] = a
}

func (r *rig) client() *http.Client {
	return &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		s := sent{
			Method:  req.Method,
			URL:     req.URL.String(),
			Header:  req.Header.Clone(),
			HasBody: req.Body != nil && req.Body != http.NoBody,
		}
		s.Deadline, s.HasDeadline = req.Context().Deadline()
		r.mu.Lock()
		r.sent = append(r.sent, s)
		block, linger := r.block, r.linger
		r.mu.Unlock()
		if block {
			<-req.Context().Done()
			time.Sleep(linger)
			return nil, req.Context().Err()
		}
		rr := httptest.NewRecorder()
		r.handler.ServeHTTP(rr, req)
		resp := rr.Result()
		resp.Request = req
		return resp, nil
	})}
}

func (r *rig) requests() []sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sent(nil), r.sent...)
}

// keys is a KeySource that records which slot was asked for which provider.
type keys struct {
	mu         sync.Mutex
	admin, api map[string]string
	askedAdmin []string
	askedAPI   []string
}

func allKeys() *keys {
	return &keys{
		admin: map[string]string{
			entity.AIProviderOpenAI: openAIAdminKey, entity.AIProviderAnthropic: anthropicAdminKey, entity.AIProviderFal: falAdminKey,
		},
		api: map[string]string{entity.AIProviderOpenRouter: openRouterAPIKey},
	}
}

// only keeps the key of one provider.
func only(provider string) *keys {
	k := allKeys()
	for p := range k.admin {
		if p != provider {
			delete(k.admin, p)
		}
	}
	for p := range k.api {
		if p != provider {
			delete(k.api, p)
		}
	}
	return k
}

func (k *keys) AdminKey(p string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.askedAdmin = append(k.askedAdmin, p)
	return k.admin[p]
}

func (k *keys) KeyFunc(p string) func() string {
	return func() string {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.askedAPI = append(k.askedAPI, p)
		return k.api[p]
	}
}

func (k *keys) set(admin, api map[string]string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.admin, k.api = admin, api
}

// recStore records every UpsertCostDaily; fail(n) answers the n-th call (1-based).
type recStore struct {
	mu     sync.Mutex
	calls  [][]entity.AICostDaily
	fail   func(n int) error
	notify chan int
}

func newRecStore() *recStore { return &recStore{notify: make(chan int, 64)} }

func (s *recStore) UpsertCostDaily(_ context.Context, rows []entity.AICostDaily) error {
	s.mu.Lock()
	s.calls = append(s.calls, append([]entity.AICostDaily(nil), rows...))
	n := len(s.calls)
	fail := s.fail
	s.mu.Unlock()
	s.notify <- n
	if fail != nil {
		return fail(n)
	}
	return nil
}

func (s *recStore) written() [][]entity.AICostDaily {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]entity.AICostDaily(nil), s.calls...)
}

// syncBuffer is a log sink the loop goroutine and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs routes slog.Default into a buffer for the test (tests here never run in parallel).
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	b := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return b
}

func newWorker(r *rig, st *recStore, k KeySource, clock func() time.Time) *Worker {
	if clock == nil {
		clock = func() time.Time { return fixedNow }
	}
	return New(Config{Enabled: true}, st, k, WithClock(clock), WithHTTPClient(r.client()))
}

// requireNoKeyMaterial fails when s carries any key or the parts of one a provider quotes back.
func requireNoKeyMaterial(t *testing.T, s string) {
	t.Helper()
	for _, key := range []string{openAIAdminKey, anthropicAdminKey, falAdminKey, openRouterAPIKey} {
		for _, part := range []string{key, key[:8], key[len(key)-4:]} {
			require.NotContains(t, s, part, "key material in %q", s)
		}
	}
}

// ───────────────────────── the adapters ─────────────────────────

// TestFixturesBecomeRows — per provider: ONE GET to the exact URL with exactly its auth, no body, a
// deadline within fetchTimeout, and the fixture's bytes become exactly its rows: the provider's UTC
// day, the amount in USD rounded to the column's six places, currency USD, bucket_tz UTC, fetched_at
// = the clock.
//
// MUTATIONS IT CATCHES: Anthropic's cents not divided by 100 (124.006789 → 12400.6789); an OpenAI
// bucket day read in the server's local zone; the fal row named `cost` ignored (28.09 → 0 or refused);
// OpenRouter's number filed under yesterday; the amounts not rounded (1.29456789 reaches DECIMAL(12,6)
// for the server to cut); a wrong query (start_time of today, ending_at = today, a missing
// bucket_width); the admin key sent as x-api-key to OpenAI or as Bearer to fal; the zone left "".
func TestFixturesBecomeRows(t *testing.T) {
	for _, f := range fixtures() {
		t.Run(f.provider, func(t *testing.T) {
			r := newRig(t)
			st := newRecStore()
			w := newWorker(r, st, only(f.provider), nil)

			require.True(t, w.runOnce(context.Background()))

			reqs := r.requests()
			require.Len(t, reqs, 1, "one GET per provider per tick")
			got := reqs[0]
			require.Equal(t, http.MethodGet, got.Method)
			require.Equal(t, f.url, got.URL)
			require.False(t, got.HasBody)
			require.Equal(t, "application/json", got.Header.Get("Accept"))
			for _, h := range authHeaders {
				want, ok := f.headers[h]
				if ok {
					require.Equal(t, want, got.Header.Get(h), h)
				} else {
					require.Empty(t, got.Header.Values(h), "%s must not be sent to %s", h, f.provider)
				}
			}
			require.True(t, got.HasDeadline)
			require.LessOrEqual(t, time.Until(got.Deadline), fetchTimeout)

			calls := st.written()
			require.Len(t, calls, 1)
			var rows []wantRow
			for _, row := range calls[0] {
				require.Equal(t, f.provider, row.ProviderKey)
				require.Equal(t, "USD", row.Currency)
				require.Equal(t, "UTC", row.BucketTZ)
				require.Equal(t, fixedNow, row.FetchedAt)
				rows = append(rows, wantRow{row.Day, row.AmountUSD.String()})
			}
			require.Equal(t, f.rows, rows)
			require.False(t, w.LastSuccess().IsZero())
		})
	}
}

// TestEveryProviderInOneTick — all four keys: four GETs in the panel's provider order, four writes,
// one clean tick.
//
// MUTATION IT CATCHES: a tick that stops at the first provider (or skips one: its line never gets
// their number).
func TestEveryProviderInOneTick(t *testing.T) {
	r := newRig(t)
	st := newRecStore()
	w := newWorker(r, st, allKeys(), nil)
	require.True(t, w.runOnce(context.Background()))

	var order []string
	for _, c := range st.written() {
		order = append(order, c[0].ProviderKey)
	}
	require.Equal(t, []string{"openai", "anthropic", "openrouter", "fal"}, order)
	require.Len(t, r.requests(), 4)
}

// failure is one answer that must write nothing.
type failure struct {
	name   string
	answer answer
	want   string // in the logged error
}

func genericFailures() []failure {
	return []failure{
		{"refused key", answer{status: http.StatusUnauthorized, body: `{"error":{"message":"bad key ` + openAIAdminKey + `"}}`}, "http 401"},
		{"no permission", answer{status: http.StatusForbidden}, "http 403"},
		{"rate limited", answer{status: http.StatusTooManyRequests}, "http 429"},
		{"provider down", answer{status: http.StatusBadGateway}, "http 502"},
		{"redirect", answer{status: http.StatusFound, header: http.Header{"Location": {"https://elsewhere.example/steal"}}}, "redirects are not followed"},
		{"not json", answer{status: http.StatusOK, body: `<html>maintenance</html>`}, "not understood"},
		{"empty 2xx", answer{status: http.StatusOK, body: `{}`}, "not understood"},
	}
}

// shapeFailures — per provider, answers that parse as JSON and still must not be read as a number.
func shapeFailures(provider string) []failure {
	switch provider {
	case entity.AIProviderOpenAI:
		return []failure{
			{"more pages", answer{status: 200, body: `{"data":[{"start_time":1790467200,"results":[]}],"has_more":true}`}, "more pages"},
			{"result without amount", answer{status: 200, body: `{"data":[{"start_time":1790467200,"results":[{"object":"x"}]}]}`}, "without its amount"},
			{"amount without value", answer{status: 200, body: `{"data":[{"start_time":1790467200,"results":[{"amount":{"currency":"usd"}}]}]}`}, "without its amount"},
			{"not dollars", answer{status: 200, body: `{"data":[{"start_time":1790467200,"results":[{"amount":{"value":1,"currency":"eur"}}]}]}`}, "not USD"},
			{"bucket off midnight", answer{status: 200, body: `{"data":[{"start_time":1790474400,"results":[]}]}`}, "not at a UTC midnight"},
			{"bucket without results", answer{status: 200, body: `{"data":[{"start_time":1790467200}]}`}, "without start_time or results"},
			{"day twice", answer{status: 200, body: `{"data":[{"start_time":1790467200,"results":[]},{"start_time":1790467200,"results":[]}]}`}, "reported twice"},
			{"a year ago", answer{status: 200, body: `{"data":[{"start_time":1758931200,"results":[]}]}`}, "no bucket falls on the days asked for"},
		}
	case entity.AIProviderAnthropic:
		return []failure{
			{"more pages", answer{status: 200, body: `{"data":[],"has_more":true}`}, "more pages"},
			{"bucket without start", answer{status: 200, body: `{"data":[{"results":[]}]}`}, "without starting_at"},
			{"start not rfc3339", answer{status: 200, body: `{"data":[{"starting_at":"27.09.2026","results":[]}]}`}, "not RFC 3339"},
			{"start in another zone", answer{status: 200, body: `{"data":[{"starting_at":"2026-09-27T00:00:00+02:00","results":[]}]}`}, "not at a UTC midnight"},
			{"amount missing", answer{status: 200, body: `{"data":[{"starting_at":"2026-09-27T00:00:00Z","results":[{"currency":"USD"}]}]}`}, "without its amount"},
			{"amount not a number", answer{status: 200, body: `{"data":[{"starting_at":"2026-09-27T00:00:00Z","results":[{"amount":"ten","currency":"USD"}]}]}`}, "not understood"},
		}
	case entity.AIProviderFal:
		return []failure{
			{"no time series", answer{status: 200, body: `{"summary":[]}`}, "no time_series"},
			{"more pages", answer{status: 200, body: `{"time_series":[],"has_more":true,"next_cursor":"c"}`}, "more pages"},
			{"row without any cost", answer{status: 200, body: `{"time_series":[{"bucket":"2026-09-27T00:00:00Z","results":[{"quantity":3,"unit_price":0.01}]}]}`}, "without its amount"},
			{"bucket in another zone", answer{status: 200, body: `{"time_series":[{"bucket":"2026-09-27T00:00:00-05:00","results":[]}]}`}, "not at a UTC midnight"},
		}
	case entity.AIProviderOpenRouter:
		return []failure{
			{"no data", answer{status: 200, body: `{"error":"x"}`}, "no data object"},
			{"no usage_daily", answer{status: 200, body: `{"data":{"usage":42.1}}`}, "without its amount"},
			{"usage_daily null", answer{status: 200, body: `{"data":{"usage_daily":null}}`}, "without its amount"},
			// A body just over the cap: valid JSON, so only the cap refuses it.
			{"over the cap", answer{status: 200, body: `{"data":{"label":"` + strings.Repeat("x", maxBody) + `","usage_daily":1}}`}, "larger than"},
		}
	}
	return nil
}

// TestAFailedFetchWritesNothing — every refused, unreadable, partial or unknown answer: nothing is
// written for that provider, the tick is not clean, the tracker holds the error, one line says
// `reconcile: <provider> fetch failed`, and neither that line nor the error carries key material or
// the provider's words.
//
// MUTATIONS IT CATCHES: a write despite a failed parse (a partial sum stored as their number); the
// has_more check dropped (the first page's sum stored as the day's); a redirect followed (the key
// sent to Location — two requests); a result without an amount read as zero; a non-USD amount summed
// as dollars; a bucket off a UTC midnight labelled UTC; the body cap dropped; an error body quoted
// into the log (it echoes the key here); MarkSuccess on a failed tick.
func TestAFailedFetchWritesNothing(t *testing.T) {
	for _, f := range fixtures() {
		for _, c := range append(genericFailures(), shapeFailures(f.provider)...) {
			t.Run(f.provider+"/"+c.name, func(t *testing.T) {
				logs := captureLogs(t)
				r := newRig(t)
				r.answer(f.path, c.answer)
				st := newRecStore()
				w := newWorker(r, st, only(f.provider), nil)

				require.False(t, w.runOnce(context.Background()))
				require.Len(t, r.requests(), 1, "one request, and a redirect is not followed")
				require.Empty(t, st.written(), "a failed fetch writes nothing")
				require.True(t, w.LastSuccess().IsZero())
				require.Contains(t, w.LastError(), f.provider+": ")
				require.Contains(t, w.LastError(), c.want)
				require.Contains(t, logs.String(), "reconcile: "+f.provider+" fetch failed")
				requireNoKeyMaterial(t, logs.String())
				requireNoKeyMaterial(t, w.LastError())
				require.NotContains(t, logs.String(), "maintenance", "provider text is never quoted")
			})
		}
	}
}

// TestOneProviderFailingDoesNotStopTheOthers.
//
// MUTATION IT CATCHES: a tick that returns at the first failure (every provider after OpenAI loses
// its number for as long as OpenAI is down); a failed tick reported clean.
func TestOneProviderFailingDoesNotStopTheOthers(t *testing.T) {
	r := newRig(t)
	r.answer("/v1/organization/costs", answer{status: http.StatusInternalServerError})
	st := newRecStore()
	w := newWorker(r, st, allKeys(), nil)

	require.False(t, w.runOnce(context.Background()))
	var order []string
	for _, c := range st.written() {
		order = append(order, c[0].ProviderKey)
	}
	require.Equal(t, []string{"anthropic", "openrouter", "fal"}, order)
	require.True(t, w.LastSuccess().IsZero())
	require.Contains(t, w.LastError(), "openai: cost api answered http 500")
}

// TestAWriteFailureIsAFailedTick.
//
// MUTATION IT CATCHES: the store's error swallowed (the tick reads clean while nothing was stored).
// TestAPanickingTickIsAFailedTick — a panic inside a tick (here: the store) is recovered by saferun,
// and the tick is reported FAILED: the loop backs off, LastSuccess stays zero and LastError names it.
// Without this a panic would unwind past `ok = true` and read as a clean tick.
//
// MUTATION: the deferred `if !done { ok = false }` in runOnce removed → red (runOnce answers true).
func TestAPanickingTickIsAFailedTick(t *testing.T) {
	logs := captureLogs(t)
	r := newRig(t)
	w := New(Config{Enabled: true}, panickingStore{}, only(entity.AIProviderOpenAI),
		WithClock(func() time.Time { return fixedNow }), WithHTTPClient(r.client()))

	require.False(t, w.runOnce(context.Background()), "a panicking tick is not a clean one")
	require.True(t, w.LastSuccess().IsZero())
	require.Contains(t, w.LastError(), "panicked")
	require.Contains(t, logs.String(), "panic recovered")
	requireNoKeyMaterial(t, logs.String())
}

// panickingStore is a CostStore whose write panics (a bug in the store or the driver, not an error).
type panickingStore struct{}

func (panickingStore) UpsertCostDaily(context.Context, []entity.AICostDaily) error {
	panic("reconcile test: the store panicked")
}

func TestAWriteFailureIsAFailedTick(t *testing.T) {
	logs := captureLogs(t)
	r := newRig(t)
	st := newRecStore()
	st.fail = func(int) error { return errors.New("deadlock found") }
	w := newWorker(r, st, only(entity.AIProviderOpenAI), nil)

	require.False(t, w.runOnce(context.Background()))
	require.True(t, w.LastSuccess().IsZero())
	require.Equal(t, "deadlock found", w.LastError())
	require.Contains(t, logs.String(), "reconcile: openai upsert failed")
}

// TestNoKeyIsNotAFetch — a provider with no key is not asked, and that is not a failure.
//
// MUTATION IT CATCHES: a fetch with an empty key (an Authorization header of «Bearer » to four
// providers every hour, and four «fetch failed» lines for a deployment that simply has no keys).
func TestNoKeyIsNotAFetch(t *testing.T) {
	r := newRig(t)
	st := newRecStore()
	w := newWorker(r, st, &keys{}, nil)
	require.True(t, w.runOnce(context.Background()))
	require.Empty(t, r.requests())
	require.Empty(t, st.written())
	require.False(t, w.LastSuccess().IsZero())
}

// TestEachAPIReadsItsOwnKey — openai/anthropic/fal read the reconciliation key (AdminKey, not gated by
// enabled) and nothing else; openrouter reads the key that pays for its calls (KeyFunc: gated by
// enabled, env fallback) and nothing else. A key in the other slot is never sent.
//
// MUTATIONS IT CATCHES: OpenRouter asked with an admin key it does not have (their number never
// appears) or OpenAI's cost report asked with the generation key (refused: it needs an admin key);
// the kind of an adapter flipped.
func TestEachAPIReadsItsOwnKey(t *testing.T) {
	r := newRig(t)
	st := newRecStore()
	k := &keys{
		// every key in the WRONG slot: nothing may be fetched
		admin: map[string]string{entity.AIProviderOpenRouter: openRouterAPIKey},
		api: map[string]string{entity.AIProviderOpenAI: openAIAdminKey, entity.AIProviderAnthropic: anthropicAdminKey,
			entity.AIProviderFal: falAdminKey},
	}
	w := newWorker(r, st, k, nil)
	require.True(t, w.runOnce(context.Background()))
	require.Empty(t, r.requests(), "a key in the other slot is never sent")
	require.Equal(t, []string{"openai", "anthropic", "fal"}, k.askedAdmin)
	require.Equal(t, []string{"openrouter"}, k.askedAPI)

	// Every adapter that reads an admin key is exactly a provider the panel takes one for (the
	// probe's admin table, which the panel's field follows — admin.TestAiKeyAdminKindMatchesTheProbeTable).
	var admin []string
	for _, a := range adapters {
		if a.kind == entity.AIKeyAdmin {
			admin = append(admin, a.provider)
		}
	}
	var probed []string
	for _, p := range entity.AIProviderKeys() {
		if probe.Probe(context.Background(), p, entity.AIKeyAdmin, "", nil).Code == probe.CodeKeyRejected {
			probed = append(probed, p)
		}
	}
	require.Equal(t, probed, admin)
}

// TestOpenRouterSnapshotAcrossMidnight — the running total is not written when the request straddles
// a UTC midnight (± the guard): it may be either day's. The skip is not a failure.
//
// MUTATIONS IT CATCHES: the guard dropped (at 23:59:30 an answer computed after midnight — the new
// day's first cents — overwrites the old day's last snapshot for good); the guard reading only the
// build time (a slow answer that crosses midnight is written); the skip counted as a failure (a
// «fetch failed» line and a backoff every night).
func TestOpenRouterSnapshotAcrossMidnight(t *testing.T) {
	late := time.Date(2026, 9, 28, 23, 59, 0, 0, time.UTC)
	early := time.Date(2026, 9, 29, 0, 1, 0, 0, time.UTC)
	slowStart := time.Date(2026, 9, 28, 23, 40, 0, 0, time.UTC)
	for name, clock := range map[string]func() time.Time{
		"built a minute before midnight": func() time.Time { return late },
		"built a minute after midnight":  func() time.Time { return early },
		"answered after midnight": func() func() time.Time {
			var mu sync.Mutex
			reads := 0
			return func() time.Time {
				mu.Lock()
				defer mu.Unlock()
				reads++
				if reads == 1 {
					return slowStart // the request is built
				}
				return early // the answer arrives
			}
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			st := newRecStore()
			w := newWorker(r, st, only(entity.AIProviderOpenRouter), clock)
			require.True(t, w.runOnce(context.Background()), "a skipped snapshot is not a failure")
			require.Len(t, r.requests(), 1)
			require.Empty(t, st.written())
			require.Empty(t, w.LastError())
		})
	}

	// Far from midnight the same answer is today's row.
	r := newRig(t)
	st := newRecStore()
	w := newWorker(r, st, only(entity.AIProviderOpenRouter), func() time.Time { return time.Date(2026, 9, 28, 23, 50, 0, 0, time.UTC) })
	require.True(t, w.runOnce(context.Background()))
	require.Len(t, st.written(), 1)
	require.Equal(t, "2026-09-28", st.written()[0][0].Day)
}

// paidCall matches a path that buys something (probe.TestNoProbeIsAPaidCall).
var paidCall = regexp.MustCompile(`chat/completions|/messages|generate|generations|/queue|fal\.run`)

// TestEveryFetchIsAFreeReadOfAConstantHost — the table itself: https to one of aiprov/endpoints'
// hosts, a cost/usage path, no key in the query; one adapter per provider; the redirect refusal
// survives a caller's client.
//
// MUTATIONS IT CATCHES: a URL built on a host that is not a constant (or http://); a path that buys
// something; a key put in the query string (it lands in every proxy log); a second adapter for one
// provider (two GETs a tick, two writes racing); WithHTTPClient keeping the caller's redirect policy.
func TestEveryFetchIsAFreeReadOfAConstantHost(t *testing.T) {
	allowed := map[string]bool{}
	for _, h := range []string{hosts.OpenAIHost, hosts.AnthropicHost, hosts.FalHost, hosts.OpenRouterHost} {
		u, err := url.Parse(h)
		require.NoError(t, err)
		allowed[u.Host] = true
	}
	win := windowAt(fixedNow)
	seen := map[string]bool{}
	for _, a := range adapters {
		require.False(t, seen[a.provider], "two adapters for %s", a.provider)
		seen[a.provider] = true
		require.True(t, entity.IsAIProviderKey(a.provider))
		u, err := url.Parse(a.target(win))
		require.NoError(t, err)
		require.Equal(t, "https", u.Scheme, a.provider)
		require.True(t, allowed[u.Host], "%s fetches from %s", a.provider, u.Host)
		require.False(t, paidCall.MatchString(u.Path), "%s looks like a paid call: %s", a.provider, u.Path)
		for k := range u.Query() {
			require.NotContains(t, strings.ToLower(k), "key", "%s puts a key in the query", a.provider)
		}
	}
	keysOf := make([]string, 0, len(seen))
	for p := range seen {
		keysOf = append(keysOf, p)
	}
	slices.Sort(keysOf)
	require.Equal(t, []string{"anthropic", "fal", "openai", "openrouter"}, keysOf)

	own := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	w := New(Config{}, newRecStore(), &keys{}, WithHTTPClient(own))
	require.ErrorIs(t, w.client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
	require.NoError(t, own.CheckRedirect(nil, nil), "the caller's client is not changed")
	require.ErrorIs(t, New(Config{}, newRecStore(), &keys{}).client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}

// TestAKeyThatIsNotAHeaderIsRefusedBeforeARequest.
//
// MUTATION IT CATCHES: a pasted newline reaching net/http (its refusal reads as «unreachable» and
// hides that the key is what is wrong).
func TestAKeyThatIsNotAHeaderIsRefusedBeforeARequest(t *testing.T) {
	r := newRig(t)
	st := newRecStore()
	k := &keys{admin: map[string]string{entity.AIProviderOpenAI: "sk-admin\nX-Injected: 1"}}
	w := newWorker(r, st, k, nil)
	require.False(t, w.runOnce(context.Background()))
	require.Empty(t, r.requests())
	require.Contains(t, w.LastError(), "not a header value")
	require.NotContains(t, w.LastError(), "Injected")
}

// ───────────────────────── the loop ─────────────────────────

// TestTickLoopRunsAtStartThenOnTicksAndBacksOff — the fxsync shape: a tick at once (no wait for the
// first interval), then one per Interval; a failed tick waits backoffDelay(n) extra before the next
// one, a clean tick resets n; Stop ends a backoff wait.
//
// MUTATIONS IT CATCHES: the startup tick dropped (a fresh boot shows no their-number for an hour);
// the ticker built on the default instead of Config.Interval; a failure not backed off; the failure
// count not reset by a clean tick (the next failure waits 4 min instead of 1); a backoff wait that
// ignores Stop.
func TestTickLoopRunsAtStartThenOnTicksAndBacksOff(t *testing.T) {
	r := newRig(t)
	st := newRecStore()
	failing := map[int]bool{2: true, 3: true, 5: true}
	st.fail = func(n int) error {
		if failing[n] {
			return errors.New("write failed")
		}
		return nil
	}
	w := New(Config{Enabled: true, Interval: 3 * time.Hour}, st, only(entity.AIProviderOpenRouter),
		WithClock(func() time.Time { return fixedNow }), WithHTTPClient(r.client()))

	ticks := make(chan time.Time)
	interval := make(chan time.Duration, 1)
	w.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		interval <- d
		return ticks, func() {}
	}
	timers := make(chan time.Duration)
	fire := make(chan time.Time)
	w.newTimer = func(d time.Duration) (<-chan time.Time, func() bool) {
		timers <- d
		return fire, func() bool { return true }
	}

	waitWrite := func(want int) {
		t.Helper()
		select {
		case n := <-st.notify:
			require.Equal(t, want, n)
		case <-time.After(5 * time.Second):
			t.Fatalf("write %d never came", want)
		}
	}
	tick := func() {
		t.Helper()
		select {
		case ticks <- fixedNow:
		case d := <-timers:
			t.Fatalf("the loop backed off %s where it should wait for a tick", d)
		case <-time.After(5 * time.Second):
			t.Fatal("the loop does not take a tick")
		}
	}
	backoff := func(want time.Duration) {
		t.Helper()
		select {
		case d := <-timers:
			require.Equal(t, want, d)
		case <-time.After(5 * time.Second):
			t.Fatalf("no backoff of %s after a failed tick", want)
		}
	}

	require.NoError(t, w.Start(context.Background()))
	require.Equal(t, 3*time.Hour, <-interval)
	waitWrite(1) // at start, before any tick

	tick()
	waitWrite(2) // fails
	backoff(backoffBase)
	fire <- fixedNow

	tick()
	waitWrite(3) // fails again
	backoff(2 * backoffBase)
	fire <- fixedNow

	tick()
	waitWrite(4) // clean: no backoff, the count resets

	tick()
	waitWrite(5) // fails
	backoff(backoffBase)

	// Stop while the loop waits out the backoff.
	stopped := make(chan error, 1)
	go func() { stopped <- w.Stop() }()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not end the backoff wait")
	}
	require.Error(t, w.Stop(), "a second Stop is refused")
}

// TestBackoffGrowsAndIsCapped.
//
// MUTATION IT CATCHES: an uncapped doubling (a provider down for a day stretches the wait past the
// next several ticks).
func TestBackoffGrowsAndIsCapped(t *testing.T) {
	require.Equal(t, backoffBase, backoffDelay(1))
	require.Equal(t, 2*backoffBase, backoffDelay(2))
	require.Equal(t, backoffMax, backoffDelay(50))
}

// TestDefaults — on, hourly; a zero interval is the hour, not a busy loop.
//
// MUTATION IT CATCHES: DefaultConfig off (no their-number on any deployment that does not set the
// variable); a zero Interval passed to NewTicker (it panics at boot).
func TestDefaults(t *testing.T) {
	require.Equal(t, Config{Enabled: true, Interval: time.Hour}, DefaultConfig())
	require.Equal(t, time.Hour, New(Config{Enabled: true}, newRecStore(), &keys{}).c.Interval)
	require.Equal(t, "ai-reconcile", New(Config{}, nil, nil).Name())
}

// ───────────────────────── RunNow ─────────────────────────

// TestRunNowFetchesOneProvider — the after-save run: that provider only, at once, with the key the
// source answers now; refused before Start and after Stop; a provider with no cost API is an error.
//
// MUTATIONS IT CATCHES: RunNow running a whole tick (four GETs for one saved key); RunNow admitted on
// a stopped worker (a write after app.Stop closed the database); a provider without an adapter
// silently «done».
func TestRunNowFetchesOneProvider(t *testing.T) {
	r := newRig(t)
	st := newRecStore()
	k := &keys{}
	w := newWorker(r, st, k, nil)
	ctx := context.Background()

	require.ErrorIs(t, w.RunNow(ctx, entity.AIProviderAnthropic), ErrNotRunning, "not started")

	w.newTicker = func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} } // no tick ever
	require.NoError(t, w.Start(ctx))
	require.Eventually(t, func() bool { return !w.LastSuccess().IsZero() }, 5*time.Second, 5*time.Millisecond,
		"the startup tick (no keys: nothing fetched)")
	require.Empty(t, r.requests())

	k.set(allKeys().admin, allKeys().api) // every key present: only the one asked for is fetched
	require.NoError(t, w.RunNow(ctx, entity.AIProviderAnthropic))
	reqs := r.requests()
	require.Len(t, reqs, 1)
	require.Equal(t, fixtureOf(t, entity.AIProviderAnthropic).url, reqs[0].URL)
	require.Len(t, st.written(), 1)
	require.Equal(t, "anthropic", st.written()[0][0].ProviderKey)

	require.ErrorContains(t, w.RunNow(ctx, entity.AIProviderGoogle), "no cost API")
	require.ErrorContains(t, w.RunNow(ctx, "sk-live-typo"), "unknown provider", "a non-provider is not echoed")

	require.NoError(t, w.Stop())
	require.ErrorIs(t, w.RunNow(ctx, entity.AIProviderAnthropic), ErrNotRunning, "stopped")
	require.Len(t, r.requests(), 1)
}

// TestStopCancelsAndWaitsForARunNow — a RunNow in flight when Stop begins is cancelled with the loop,
// and Stop returns only after it has.
//
// MUTATIONS IT CATCHES: a RunNow not counted in the worker's WaitGroup (Stop returns while it is still
// running, and its write lands on the closed pool); a RunNow not cancelled by Stop (a redeploy waits
// out a provider's full timeout).
func TestStopCancelsAndWaitsForARunNow(t *testing.T) {
	r := newRig(t)
	st := newRecStore()
	k := &keys{}
	w := newWorker(r, st, k, nil)
	w.newTicker = func(time.Duration) (<-chan time.Time, func()) { return nil, func() {} }
	require.NoError(t, w.Start(context.Background()))
	require.Eventually(t, func() bool { return !w.LastSuccess().IsZero() }, 5*time.Second, 5*time.Millisecond)

	k.set(map[string]string{entity.AIProviderOpenAI: openAIAdminKey}, nil)
	r.mu.Lock()
	r.block, r.linger = true, 200*time.Millisecond
	r.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- w.RunNow(context.WithoutCancel(context.Background()), entity.AIProviderOpenAI) }()
	require.Eventually(t, func() bool { return len(r.requests()) == 1 }, 5*time.Second, 5*time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		_ = w.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel the run in flight")
	}
	select {
	case err := <-done:
		require.Error(t, err, "the cancelled run failed")
	default:
		t.Fatal("Stop returned before the run in flight did")
	}
	require.Empty(t, st.written())
}

// gatedKeys — a KeySource whose FIRST admin-key read blocks until gate closes and answers old; every
// later read answers new. It stands for an admin saving a new key while a tick is under way.
type gatedKeys struct {
	mu       sync.Mutex
	reads    int
	entered  chan struct{}
	gate     chan struct{}
	old, new string
}

func (g *gatedKeys) AdminKey(p string) string {
	if p != entity.AIProviderOpenAI {
		return ""
	}
	g.mu.Lock()
	g.reads++
	first := g.reads == 1
	g.mu.Unlock()
	if first {
		g.entered <- struct{}{}
		<-g.gate
		return g.old
	}
	return g.new
}

func (g *gatedKeys) KeyFunc(string) func() string { return func() string { return "" } }

// TestTheNewestKeysNumberLandsLast (Codex REVIEW-E #4) — a tick is reading the OLD admin key when an
// admin saves a NEW one and the after-save RunNow fires. Whatever the order of the two fetches, the
// row that stands at the end is the NEW key's: the key is read under the same lock as the fetch and
// the write, so the run that fetches later reads the newer key.
//
// MUTATION (measured red → restored green): keyOf read before fetchMu.Lock → RunNow, not held back by a
// lock the tick does not yet hold, writes the new key's number FIRST and the tick's old number lands
// last: the written order flips from [old, new] to [new, old].
func TestTheNewestKeysNumberLandsLast(t *testing.T) {
	const oldKey, newKey = "sk-admin-old", "sk-admin-new"
	r := newRig(t)
	r.handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		amount := "0"
		switch req.Header.Get("Authorization") {
		case "Bearer " + oldKey:
			amount = "1"
		case "Bearer " + newKey:
			amount = "2"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"page","data":[{"object":"bucket","start_time":1790467200,"end_time":1790553600,`+
			`"results":[{"object":"organization.costs.result","amount":{"value":`+amount+`,"currency":"usd"}}]}],"has_more":false,"next_page":null}`)
	})
	ks := &gatedKeys{entered: make(chan struct{}, 1), gate: make(chan struct{}), old: oldKey, new: newKey}
	st := newRecStore()
	w := newWorker(r, st, ks, nil)
	// Start's first tick is the tick under way: it blocks inside the old key's read.
	require.NoError(t, w.Start(context.Background()))
	t.Cleanup(func() { _ = w.Stop() })
	<-ks.entered

	// The admin's save: RunNow with the new key while the tick still holds the old one.
	done := make(chan error, 1)
	go func() { done <- w.RunNow(context.Background(), entity.AIProviderOpenAI) }()
	time.Sleep(50 * time.Millisecond) // long enough for a RunNow the lock does NOT hold back to write first
	close(ks.gate)

	require.NoError(t, <-done)
	for range 2 {
		select {
		case <-st.notify:
		case <-time.After(5 * time.Second):
			t.Fatal("two writes expected: the tick's and RunNow's")
		}
	}
	writes := st.written()
	require.Len(t, writes, 2)
	require.Equal(t, "1", writes[0][0].AmountUSD.String(), "the tick, with the old key, writes first")
	require.Equal(t, "2", writes[1][0].AmountUSD.String(), "the new key's number lands last")
}
