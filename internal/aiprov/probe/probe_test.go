package probe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// fakeKey is obviously not a key. Its first six and last four characters are checked for in every
// message, the way a provider that masks a key would quote them.
const fakeKey = "fake-key-for-tests-only-0000-WXYZ"

// fixedNow pins the cost-report queries: 2026-09-27 10:30 UTC.
var fixedNow = time.Date(2026, 9, 27, 10, 30, 0, 0, time.UTC)

func pinClock(t *testing.T) {
	t.Helper()
	prev := clock
	clock = func() time.Time { return fixedNow }
	t.Cleanup(func() { clock = prev })
}

// ───────────────────────── rig ─────────────────────────

// sent is one request exactly as the probe built it, before the rig redirected it.
type sent struct {
	Method        string
	URL           string
	Header        http.Header
	Body          []byte
	HasBody       bool
	ContentLength int64
	Deadline      time.Time
	HasDeadline   bool
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// rig is one httptest server per probe. Its client RECORDS the request the probe built — the real
// provider URL, headers and body — and then delivers it to the server instead of the provider, so
// the package needs no base-url knob to be tested.
type rig struct {
	srv    *httptest.Server
	status int
	body   string
	header http.Header
	block  bool

	mu   sync.Mutex
	sent []sent
}

func newRig(t *testing.T, status int, body string) *rig {
	t.Helper()
	r := &rig{status: status, body: body}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// read the request to its end first: net/http notices a client that hung up only after that
		_, _ = io.Copy(io.Discard, req.Body)
		if r.block {
			<-req.Context().Done()
			return
		}
		for k, v := range r.header {
			w.Header()[k] = v
		}
		w.WriteHeader(r.status)
		_, _ = io.WriteString(w, r.body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) client() *http.Client {
	return &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		s := sent{
			Method:        req.Method,
			URL:           req.URL.String(),
			Header:        req.Header.Clone(),
			HasBody:       req.Body != nil && req.Body != http.NoBody,
			ContentLength: req.ContentLength,
		}
		if req.Body != nil {
			s.Body, _ = io.ReadAll(req.Body)
			_ = req.Body.Close()
		}
		s.Deadline, s.HasDeadline = req.Context().Deadline()
		r.mu.Lock()
		r.sent = append(r.sent, s)
		r.mu.Unlock()

		out := req.Clone(req.Context())
		out.URL.Scheme = "http"
		out.URL.Host = r.srv.Listener.Addr().String()
		out.Host = ""
		out.Body = http.NoBody
		if len(s.Body) > 0 {
			out.Body = io.NopCloser(bytes.NewReader(s.Body))
		}
		return r.srv.Client().Transport.RoundTrip(out)
	})}
}

func (r *rig) requests() []sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sent(nil), r.sent...)
}

// ───────────────────────── the matrix ─────────────────────────

type probeCase struct {
	provider string
	kind     entity.AIKeyKind
	url      string            // exact, clock pinned
	headers  map[string]string // the auth headers, exact; every other auth header must be absent
	okBody   string
	balance  string // Balance on a 200 with okBody
}

// authHeaders — every header any probe authenticates with.
var authHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Anthropic-Version"}

func matrix() []probeCase {
	bearer := map[string]string{"Authorization": "Bearer " + fakeKey}
	anthropic := map[string]string{"X-Api-Key": fakeKey, "Anthropic-Version": "2023-06-01"}
	fal := map[string]string{"Authorization": "Key " + fakeKey}
	return []probeCase{
		{entity.AIProviderOpenAI, entity.AIKeyAPI, "https://api.openai.com/v1/models",
			bearer, `{"object":"list","data":[]}`, ""},
		{entity.AIProviderAnthropic, entity.AIKeyAPI, "https://api.anthropic.com/v1/models",
			anthropic, `{"data":[]}`, ""},
		{entity.AIProviderGoogle, entity.AIKeyAPI, "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1",
			map[string]string{"X-Goog-Api-Key": fakeKey}, `{"models":[]}`, ""},
		{entity.AIProviderOpenRouter, entity.AIKeyAPI, "https://openrouter.ai/api/v1/key",
			bearer, `{"data":{"label":"x","usage":5.5,"limit":30,"limit_remaining":24.5}}`, "24.50 USD"},
		{entity.AIProviderApibost, entity.AIKeyAPI, "https://apibost.com/v1/models",
			bearer, `{"data":[]}`, ""},
		{entity.AIProviderFal, entity.AIKeyAPI, "https://api.fal.ai/v1/models/pricing?endpoint_id=fal-ai/birefnet/v2",
			fal, `{"prices":[]}`, ""},
		{entity.AIProviderMeshy, entity.AIKeyAPI, "https://api.meshy.ai/openapi/v1/balance",
			bearer, `{"balance":1200}`, "1200 credits"},
		{entity.AIProviderRunblob, entity.AIKeyAPI, "https://platform.runblob.io/v1/kling/generations/00000000-0000-0000-0000-000000000000",
			bearer, `{"status":"pending"}`, ""},
		// recraft's users/me carries credits, but no balance is promised for it: "" not a guess.
		{entity.AIProviderRecraft, entity.AIKeyAPI, "https://external.api.recraft.ai/v1/users/me",
			bearer, `{"credits":1000,"id":"x"}`, ""},
		{entity.AIProviderOpenAI, entity.AIKeyAdmin, "https://api.openai.com/v1/organization/costs?start_time=1790418600&limit=1",
			bearer, `{"object":"page","data":[]}`, ""},
		{entity.AIProviderAnthropic, entity.AIKeyAdmin,
			"https://api.anthropic.com/v1/organizations/cost_report?starting_at=2026-09-26T00:00:00Z&ending_at=2026-09-27T00:00:00Z",
			anthropic, `{"data":[]}`, ""},
		{entity.AIProviderFal, entity.AIKeyAdmin, "https://api.fal.ai/v1/account/billing?expand=credits",
			fal, `{"username":"someone","credits":{"current_balance":24.5,"currency":"USD"}}`, "24.50 USD"},
	}
}

func (c probeCase) name() string { return c.provider + "/" + string(c.kind) }

// requireNoKey fails when msg carries the key or the parts of it a provider quotes when it masks one.
func requireNoKey(t *testing.T, msg string) {
	t.Helper()
	low := strings.ToLower(msg)
	for _, part := range []string{fakeKey, fakeKey[:6], fakeKey[len(fakeKey)-4:]} {
		require.NotContains(t, low, strings.ToLower(part), "message %q carries key material", msg)
	}
	require.LessOrEqual(t, len(msg), maxMessage, "message %q is too long", msg)
}

// TestProbeRequestShape: every probe is ONE GET to the exact URL with exactly its auth scheme.
func TestProbeRequestShape(t *testing.T) {
	pinClock(t)
	for _, c := range matrix() {
		t.Run(c.name(), func(t *testing.T) {
			r := newRig(t, http.StatusOK, c.okBody)
			res := Probe(context.Background(), c.provider, c.kind, fakeKey, r.client())

			got := r.requests()
			require.Len(t, got, 1)
			require.Equal(t, http.MethodGet, got[0].Method)
			require.Equal(t, c.url, got[0].URL)
			for _, h := range authHeaders {
				want, ok := c.headers[h]
				if ok {
					require.Equal(t, want, got[0].Header.Get(h), "header %s", h)
				} else {
					require.Empty(t, got[0].Header.Values(h), "header %s must not be sent", h)
				}
			}
			require.Equal(t, "application/json", got[0].Header.Get("Accept"))
			require.Equal(t, Result{OK: true, Message: "key accepted", Balance: c.balance}, res)
		})
	}
}

// TestProbeStatusMapping: the verdict of every status, for every probe.
func TestProbeStatusMapping(t *testing.T) {
	pinClock(t)
	type verdict struct {
		ok   bool
		code string
		msg  string // "" = only checked for key material
	}
	base := map[int]verdict{
		200: {true, "", "key accepted"},
		204: {true, "", ""},
		401: {false, CodeKeyRejected, "key rejected (http 401)"},
		402: {false, CodeOutOfCredits, "out of credits (http 402)"},
		403: {false, CodeKeyRejected, "key refused: no permission for this check (http 403)"},
		404: {false, "", "probe refused (http 404)"},
		422: {false, "", "probe refused (http 422)"},
		429: {true, "", "rate limited"},
		500: {false, CodeUnreachable, "provider error (http 500)"},
		503: {false, CodeUnreachable, "provider error (http 503)"},
		529: {false, CodeUnreachable, "provider error (http 529)"},
	}
	for _, c := range matrix() {
		for status, want := range base {
			if status == http.StatusNotFound && c.provider == entity.AIProviderRunblob {
				// the one exception: a status read of the zero uuid answers 404 to an accepted key
				want = verdict{true, "", "key accepted"}
			}
			body := c.okBody
			if status >= 300 {
				// an error body that would read as a balance must not become one
				body = `{"balance":5,"data":{"limit_remaining":5}}`
			}
			t.Run(c.name()+"/"+http.StatusText(status), func(t *testing.T) {
				r := newRig(t, status, body)
				res := Probe(context.Background(), c.provider, c.kind, fakeKey, r.client())
				require.Len(t, r.requests(), 1)
				require.Equal(t, want.ok, res.OK, "ok for %d: %+v", status, res)
				require.Equal(t, want.code, res.Code, "code for %d: %+v", status, res)
				if want.msg != "" {
					require.Equal(t, want.msg, res.Message)
				}
				require.NotEmpty(t, res.Message)
				requireNoKey(t, res.Message)
				if status >= 300 {
					require.Empty(t, res.Balance, "a balance only comes from a 2xx")
				}
			})
		}
	}
}

// TestProbeTimeout: a provider that does not answer is unreachable, not a verdict on the key.
func TestProbeTimeout(t *testing.T) {
	pinClock(t)
	for _, c := range matrix() {
		t.Run(c.name(), func(t *testing.T) {
			r := newRig(t, http.StatusOK, c.okBody)
			r.block = true
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			res := Probe(ctx, c.provider, c.kind, fakeKey, r.client())
			require.Equal(t, Result{Code: CodeUnreachable, Message: "provider did not answer in time"}, res)
		})
	}
}

// TestProbeBoundsEveryProbe: the probe carries its own 8 s deadline even on a caller's client that
// has none.
func TestProbeBoundsEveryProbe(t *testing.T) {
	r := newRig(t, http.StatusOK, `{"data":[]}`)
	start := time.Now()
	Probe(context.Background(), entity.AIProviderOpenAI, entity.AIKeyAPI, fakeKey, r.client())
	end := time.Now()
	got := r.requests()
	require.Len(t, got, 1)
	require.True(t, got[0].HasDeadline, "the probe must bound itself")
	require.False(t, got[0].Deadline.Before(start.Add(8*time.Second)), "deadline %v", got[0].Deadline.Sub(start))
	require.False(t, got[0].Deadline.After(end.Add(8*time.Second)), "deadline %v", got[0].Deadline.Sub(start))
}

// TestProbeUnreachable: a refused connection and a cancelled probe are unreachable too.
func TestProbeUnreachable(t *testing.T) {
	r := newRig(t, http.StatusOK, "")
	r.srv.Close()
	res := Probe(context.Background(), entity.AIProviderMeshy, entity.AIKeyAPI, fakeKey, r.client())
	require.Equal(t, Result{Code: CodeUnreachable, Message: "could not reach the provider"}, res)

	r2 := newRig(t, http.StatusOK, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res = Probe(ctx, entity.AIProviderMeshy, entity.AIKeyAPI, fakeKey, r2.client())
	require.Equal(t, Result{Code: CodeUnreachable, Message: "probe cancelled"}, res)
}

// TestDefaultClient: nil gets the 8 s client; a caller's client is copied, never changed, and both
// refuse redirects.
func TestDefaultClient(t *testing.T) {
	c := httpClient(nil)
	require.Same(t, defaultClient, c)
	require.Equal(t, 8*time.Second, c.Timeout)
	require.NotNil(t, c.CheckRedirect)
	require.ErrorIs(t, c.CheckRedirect(nil, nil), http.ErrUseLastResponse)

	own := &http.Client{Transport: http.DefaultTransport}
	cp := httpClient(own)
	require.NotSame(t, own, cp)
	require.Nil(t, own.CheckRedirect, "the caller's client is not changed")
	require.Equal(t, own.Transport, cp.Transport, "the caller's transport (and pool) is kept")
	require.ErrorIs(t, cp.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}

// TestProbeRefusesRedirect: the key never follows a Location — x-api-key would.
func TestProbeRefusesRedirect(t *testing.T) {
	r := newRig(t, http.StatusFound, "")
	r.header = http.Header{"Location": {"https://elsewhere.example/steal"}}
	own := r.client()
	res := Probe(context.Background(), entity.AIProviderAnthropic, entity.AIKeyAPI, fakeKey, own)
	require.Len(t, r.requests(), 1, "a redirect is not followed")
	require.Equal(t, Result{Message: "unexpected redirect (http 302)"}, res)
	require.Nil(t, own.CheckRedirect)
}

// paidCall matches a path that buys something.
var paidCall = regexp.MustCompile(`chat/completions|/messages|generate|generations`)

// TestNoProbeIsAPaidCall walks the table itself: no probe URL names a completion or a generation,
// except runblob's status read of the zero uuid; every probe is https to a constant host.
func TestNoProbeIsAPaidCall(t *testing.T) {
	hosts := map[string]bool{
		"api.openai.com": true, "api.anthropic.com": true, "generativelanguage.googleapis.com": true,
		"openrouter.ai": true, "apibost.com": true, "api.fal.ai": true, "api.meshy.ai": true,
		"platform.runblob.io": true, "external.api.recraft.ai": true,
	}
	require.NotEmpty(t, endpoints)
	for k, ep := range endpoints {
		target := ep.target(fixedNow)
		u, err := url.Parse(target)
		require.NoError(t, err, target)
		require.Equal(t, "https", u.Scheme, target)
		require.True(t, hosts[u.Host], "unknown host in %s", target)
		if paidCall.MatchString(target) {
			require.Equal(t, probeKey{entity.AIProviderRunblob, entity.AIKeyAPI}, k, "%s looks like a paid call", target)
			require.Equal(t, "https://platform.runblob.io/v1/kling/generations/00000000-0000-0000-0000-000000000000", target)
			require.True(t, ep.notFoundIsOK)
		}
		if ep.notFoundIsOK {
			require.Equal(t, probeKey{entity.AIProviderRunblob, entity.AIKeyAPI}, k, "only runblob reads a 404 as an accepted key")
		}
	}
}

// TestEndpointTableCoverage: exactly the twelve probes of the brief — every provider's api key and the
// three reconciliation keys.
func TestEndpointTableCoverage(t *testing.T) {
	want := map[probeKey]bool{
		{entity.AIProviderOpenAI, entity.AIKeyAdmin}:    true,
		{entity.AIProviderAnthropic, entity.AIKeyAdmin}: true,
		{entity.AIProviderFal, entity.AIKeyAdmin}:       true,
	}
	for _, p := range entity.AIProviderKeys() {
		want[probeKey{p, entity.AIKeyAPI}] = true
	}
	got := map[probeKey]bool{}
	for k := range endpoints {
		got[k] = true
	}
	require.Equal(t, want, got)
	require.Len(t, matrix(), len(endpoints), "the matrix test covers every probe")
}

// TestProbeNeverSendsABody: a GET with no body, no length, no content type — for every probe.
func TestProbeNeverSendsABody(t *testing.T) {
	pinClock(t)
	for _, c := range matrix() {
		t.Run(c.name(), func(t *testing.T) {
			r := newRig(t, http.StatusOK, c.okBody)
			Probe(context.Background(), c.provider, c.kind, fakeKey, r.client())
			got := r.requests()
			require.Len(t, got, 1)
			require.Equal(t, http.MethodGet, got[0].Method)
			require.False(t, got[0].HasBody, "a probe sends no body")
			require.Empty(t, got[0].Body)
			require.Zero(t, got[0].ContentLength)
			require.Empty(t, got[0].Header.Get("Content-Type"))
		})
	}
}

// TestNoProbeWithoutACall: unknown providers and unsupported pairs are answered without a request,
// and a key passed in the provider's place is not printed back.
func TestNoProbeWithoutACall(t *testing.T) {
	for _, tc := range []struct {
		provider string
		kind     entity.AIKeyKind
		msg      string
	}{
		{entity.AIProviderMeshy, entity.AIKeyAdmin, "no probe for meshy/admin"},
		{entity.AIProviderGoogle, entity.AIKeyAdmin, "no probe for google/admin"},
		{entity.AIProviderOpenRouter, entity.AIKeyAdmin, "no probe for openrouter/admin"},
		{entity.AIProviderRunblob, entity.AIKeyAdmin, "no probe for runblob/admin"},
		{entity.AIProviderOpenAI, "", "no probe for openai/unknown kind"},
		{"nope", entity.AIKeyAPI, "no probe for unknown provider/api"},
		{fakeKey, entity.AIKeyAPI, "no probe for unknown provider/api"},
	} {
		r := newRig(t, http.StatusOK, "{}")
		res := Probe(context.Background(), tc.provider, tc.kind, fakeKey, r.client())
		require.Empty(t, r.requests(), "%s/%s", tc.provider, tc.kind)
		require.Equal(t, Result{Message: tc.msg}, res)
		requireNoKey(t, res.Message)
	}
}

// TestUnusableKey: an empty key and a key net/http could not send are refused without a request.
func TestUnusableKey(t *testing.T) {
	for _, tc := range []struct{ key, msg string }{
		{"", "no key"},
		{"   ", "no key"},
		{"\t\n", "no key"},
		{"fake-key\n", "key contains spaces or control characters"},
		{"fake key", "key contains spaces or control characters"},
		{"fake-kéy", "key contains spaces or control characters"},
	} {
		r := newRig(t, http.StatusOK, "{}")
		res := Probe(context.Background(), entity.AIProviderOpenAI, entity.AIKeyAPI, tc.key, r.client())
		require.Empty(t, r.requests(), "%q", tc.key)
		require.Equal(t, Result{Code: CodeKeyRejected, Message: tc.msg}, res, "%q", tc.key)
	}
}

// TestProviderSentenceIsScrubbed: an unexplained 4xx quotes the provider — never the key it quotes.
func TestProviderSentenceIsScrubbed(t *testing.T) {
	leaky := []string{
		`{"error":{"message":"Incorrect API key provided: fake-k****WXYZ. You can find your API key at https://example.test/keys."}}`,
		`{"message":"bad key ` + fakeKey + `"}`,
		`{"detail":"key ...WXYZ is invalid"}`,
		`{"error":"key fake-key-for is not valid"}`,
		`key ` + fakeKey + ` is not valid`,
		`{"error":{"message":"token sk0123456789abcdefghijklmnop is revoked"}}`,
		`{"error":{"message":"key sk-****1234 is revoked"}}`,
	}
	for _, body := range leaky {
		r := newRig(t, http.StatusBadRequest, body)
		res := Probe(context.Background(), entity.AIProviderOpenAI, entity.AIKeyAPI, fakeKey, r.client())
		require.False(t, res.OK)
		require.Empty(t, res.Code)
		require.True(t, strings.HasPrefix(res.Message, "probe refused (http 400): "), res.Message)
		require.Contains(t, res.Message, "[redacted]", body)
		requireNoKey(t, res.Message)
		require.NotContains(t, res.Message, "sk0123456789")
		require.NotContains(t, res.Message, "**")
	}

	// a key too short to have a prefix and a suffix is still never quoted back
	const short = "Zq7!Zq7"
	r := newRig(t, http.StatusBadRequest, `{"message":"key Zq7!Zq7 is invalid"}`)
	res := Probe(context.Background(), entity.AIProviderOpenAI, entity.AIKeyAPI, short, r.client())
	require.Equal(t, Result{Message: "probe refused (http 400): key [redacted] is invalid"}, res)

	// a harmless sentence is quoted, lowercased
	r = newRig(t, http.StatusBadRequest, `{"error":{"message":"Unknown parameter: pageSize"}}`)
	res = Probe(context.Background(), entity.AIProviderGoogle, entity.AIKeyAPI, fakeKey, r.client())
	require.Equal(t, Result{Message: "probe refused (http 400): unknown parameter: pagesize"}, res)

	// an HTML page is not quoted; a long sentence is clipped
	r = newRig(t, http.StatusBadRequest, "<html><body>nginx</body></html>")
	res = Probe(context.Background(), entity.AIProviderGoogle, entity.AIKeyAPI, fakeKey, r.client())
	require.Equal(t, "probe refused (http 400)", res.Message)
	r = newRig(t, http.StatusBadRequest, `{"message":"`+strings.Repeat("too long ", 40)+`"}`)
	res = Probe(context.Background(), entity.AIProviderGoogle, entity.AIKeyAPI, fakeKey, r.client())
	require.Len(t, res.Message, maxMessage)
	require.True(t, strings.HasSuffix(res.Message, "…"))
}

// TestBalances: what each balance endpoint's body turns into.
func TestBalances(t *testing.T) {
	type bal struct {
		provider string
		kind     entity.AIKeyKind
		body     string
		balance  string
		msg      string
	}
	accepted, unreadable := "key accepted", "key accepted; balance not readable"
	for _, tc := range []bal{
		{entity.AIProviderOpenRouter, entity.AIKeyAPI, `{"data":{"usage":5.5,"limit":30,"limit_remaining":24.5}}`, "24.50 USD", accepted},
		{entity.AIProviderOpenRouter, entity.AIKeyAPI, `{"data":{"usage":5.5,"limit":null,"limit_remaining":null}}`, "", accepted},
		{entity.AIProviderOpenRouter, entity.AIKeyAPI, `{"data":{"usage":5.5}}`, "", accepted},
		{entity.AIProviderOpenRouter, entity.AIKeyAPI, `{"credits":10}`, "", unreadable},
		{entity.AIProviderOpenRouter, entity.AIKeyAPI, `not json`, "", unreadable},
		{entity.AIProviderFal, entity.AIKeyAdmin, `{"credits":{"current_balance":24.5,"currency":"USD"}}`, "24.50 USD", accepted},
		{entity.AIProviderFal, entity.AIKeyAdmin, `{"credits":{"current_balance":7,"currency":"eur"}}`, "7.00 EUR", accepted},
		{entity.AIProviderFal, entity.AIKeyAdmin, `{"credits":{"current_balance":7}}`, "7.00 USD", accepted},
		{entity.AIProviderFal, entity.AIKeyAdmin, `{"credits":{"current_balance":7,"currency":"dollars"}}`, "", unreadable},
		{entity.AIProviderFal, entity.AIKeyAdmin, `{"username":"someone"}`, "", unreadable},
		{entity.AIProviderMeshy, entity.AIKeyAPI, `{"balance":1200}`, "1200 credits", accepted},
		{entity.AIProviderMeshy, entity.AIKeyAPI, `{"balance":0}`, "0 credits", accepted},
		{entity.AIProviderMeshy, entity.AIKeyAPI, `{"balance":12.5}`, "12.50 credits", accepted},
		{entity.AIProviderMeshy, entity.AIKeyAPI, `{}`, "", unreadable},
		// no balance promised: the body is not read for one
		{entity.AIProviderFal, entity.AIKeyAPI, `{"credits":{"current_balance":24.5,"currency":"USD"}}`, "", accepted},
		{entity.AIProviderRecraft, entity.AIKeyAPI, `{"credits":1000}`, "", accepted},
	} {
		r := newRig(t, http.StatusOK, tc.body)
		res := Probe(context.Background(), tc.provider, tc.kind, fakeKey, r.client())
		require.Equal(t, Result{OK: true, Message: tc.msg, Balance: tc.balance}, res, "%s/%s %s", tc.provider, tc.kind, tc.body)
	}
}

// TestBodyIsCapped: past 64 KiB the body is not read — the balance cut off there is unreadable.
func TestBodyIsCapped(t *testing.T) {
	body := `{"data":{"limit_remaining":24.5,"pad":"` + strings.Repeat("x", maxBody) + `"}}`
	r := newRig(t, http.StatusOK, body)
	res := Probe(context.Background(), entity.AIProviderOpenRouter, entity.AIKeyAPI, fakeKey, r.client())
	require.Equal(t, Result{OK: true, Message: "key accepted; balance not readable"}, res)

	// the same body under the cap reads
	r = newRig(t, http.StatusOK, `{"data":{"limit_remaining":24.5,"pad":"`+strings.Repeat("x", maxBody-100)+`"}}`)
	res = Probe(context.Background(), entity.AIProviderOpenRouter, entity.AIKeyAPI, fakeKey, r.client())
	require.Equal(t, "24.50 USD", res.Balance)
}

// TestTransportFailureClasses: the classes, from the errors net/http returns.
func TestTransportFailureClasses(t *testing.T) {
	require.Equal(t, CodeUnreachable, transportFailure(context.DeadlineExceeded).Code)
	require.Equal(t, CodeUnreachable, transportFailure(errors.New("dial tcp: connection refused")).Code)
	require.Equal(t, "probe cancelled", transportFailure(&url.Error{Op: "Get", URL: "x", Err: context.Canceled}).Message)
}
