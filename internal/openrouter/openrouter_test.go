package openrouter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnabled_NilSafe(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Error("nil client must be disabled")
	}
	if c.Model() != "" {
		t.Error("nil client model must be empty")
	}
	if New(Config{}).Enabled() {
		t.Error("client without api key must be disabled")
	}
	if !New(Config{APIKey: "k"}).Enabled() {
		t.Error("client with api key must be enabled")
	}
}

func TestComplete_NotConfigured(t *testing.T) {
	_, err := New(Config{}).Complete(context.Background(), "sys", "user", false)
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("want ErrNotConfigured, got %v", err)
	}
}

func TestNew_Defaults(t *testing.T) {
	c := New(Config{APIKey: "k"})
	if c.Model() != defaultModel {
		t.Errorf("default model = %q, want %q", c.Model(), defaultModel)
	}
}

// TestComplete_RoundTrip stubs OpenRouter with httptest: it verifies the request (path, auth
// header, model, JSON body carrying the prompt) and that a well-formed chat response comes back as
// the assistant's content — the full path minus a real API key.
func TestComplete_RoundTrip(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"model":"stub-model","choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"}}]}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "secret-key", Model: "test/model", BaseURL: srv.URL})
	content, err := c.Complete(context.Background(), "sys", "assemble it", true)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"test/model"`) {
		t.Errorf("request body missing model: %s", gotBody)
	}
	if !strings.Contains(gotBody, "assemble it") {
		t.Errorf("request body missing the user prompt: %s", gotBody)
	}
	if content != `{"ok":true}` {
		t.Errorf("content = %q", content)
	}
}

func TestComplete_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"No auth credentials found","code":401}}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "bad", BaseURL: srv.URL})
	_, err := c.Complete(context.Background(), "sys", "user", false)
	if err == nil {
		t.Fatal("expected an API error")
	}
	if !strings.Contains(err.Error(), "No auth credentials found") || !strings.Contains(err.Error(), "401") {
		t.Errorf("error should surface the API message and status: %v", err)
	}
}

// TestChat_ModelUnavailableIsAConfigurationFault reproduces the beta outage of 2026-08-17 byte for
// byte: `anthropic/claude-3.5-sonnet` was retired at the provider, the call came back in 0.2 s with
// HTTP 404 and the body below, and every caller reported it as "unavailable right now — try again
// in a moment". It was never going to become available, and the retry never had a chance.
//
// What is pinned here is the SPLIT, not the wording: a 404 is a configuration fault (a sentinel the
// caller can branch on), while 5xx and a broken transport stay ordinary errors. The provider's own
// sentence must still travel inside the error — it is what a log reader needs — but it must not be
// what decides the classification.
func TestChat_ModelUnavailableIsAConfigurationFault(t *testing.T) {
	// The exact body the live provider returned; kept verbatim so this test breaks if we ever start
	// depending on parsing it.
	const liveBody = `{"error":{"message":"No endpoints found for anthropic/claude-3.5-sonnet.","code":404}}`

	t.Run("404 becomes ErrModelUnavailable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, liveBody)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", Model: "anthropic/claude-3.5-sonnet", BaseURL: srv.URL})

		// Both text entry points must classify identically: the note assistant goes through
		// Complete, the construction analysis through CompleteWithMeta, and they share one client.
		_, errComplete := c.Complete(context.Background(), "sys", "user", false)
		if !errors.Is(errComplete, ErrModelUnavailable) {
			t.Errorf("Complete: want ErrModelUnavailable, got %v", errComplete)
		}
		_, _, _, errMeta := c.CompleteWithMeta(context.Background(), "sys", "user", true, 100)
		if !errors.Is(errMeta, ErrModelUnavailable) {
			t.Errorf("CompleteWithMeta: want ErrModelUnavailable, got %v", errMeta)
		}

		// The provider's sentence and the status still ride along for the log: the sentinel says
		// what KIND of fault it is, it does not swallow what happened.
		for _, err := range []error{errComplete, errMeta} {
			if err == nil {
				continue
			}
			if !strings.Contains(err.Error(), "No endpoints found for anthropic/claude-3.5-sonnet.") {
				t.Errorf("provider message must survive into the error text: %v", err)
			}
			if !strings.Contains(err.Error(), "404") {
				t.Errorf("status must survive into the error text: %v", err)
			}
		}
	})

	t.Run("5xx stays an ordinary retryable error", func(t *testing.T) {
		// The other half of the split. Without this, "everything is a configuration fault" would
		// pass the test above and mislabel every genuine outage.
		for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				io.WriteString(w, `{"error":{"message":"upstream is having a moment"}}`)
			}))
			_, err := New(Config{APIKey: "k", BaseURL: srv.URL}).Complete(context.Background(), "sys", "user", false)
			srv.Close()
			if err == nil {
				t.Fatalf("HTTP %d: expected an error", code)
			}
			if errors.Is(err, ErrModelUnavailable) {
				t.Errorf("HTTP %d must NOT be a configuration fault: %v", code, err)
			}
		}
	})

	t.Run("a dead transport stays an ordinary error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close() // nothing is listening any more
		_, err := New(Config{APIKey: "k", BaseURL: url}).Complete(context.Background(), "sys", "user", false)
		if err == nil {
			t.Fatal("expected a transport error")
		}
		if errors.Is(err, ErrModelUnavailable) {
			t.Errorf("a transport failure must NOT be a configuration fault: %v", err)
		}
	})
}

// TestCheckModel pins the STARTUP PROBE, and mostly it pins the two ways of building it wrong.
// Both were measured against the live API before this was written:
//
//   - GET /models/{slug} — the obvious route — answers 404 for every slug, including live ones.
//     A probe on that route shouts on every boot of every deployment.
//   - GET /models/{slug}/endpoints answers 200 for the RETIRED slug as well. What separates it
//     from a live one is an EMPTY endpoints array — 9 endpoints for anthropic/claude-sonnet-5,
//     0 for anthropic/claude-3.5-sonnet. So the empty array is the alarm, not the status.
//
// The rest is the silence contract: anything that is not a clear verdict must produce no alarm,
// because an alarm that fires on a slow network teaches people to ignore the real one.
func TestCheckModel(t *testing.T) {
	// Bodies trimmed from the real responses, keeping the shape that matters.
	const liveBody = `{"data":{"id":"anthropic/claude-sonnet-5","endpoints":[{"provider_name":"Anthropic"},{"provider_name":"Azure"}]}}`
	const retiredBody = `{"data":{"id":"anthropic/claude-3.5-sonnet","endpoints":[]}}`

	probe := func(t *testing.T, status int, body string) (error, string) {
		t.Helper()
		var gotPath, gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, body)
		}))
		defer srv.Close()
		c := New(Config{APIKey: "k", Model: "anthropic/claude-sonnet-5", BaseURL: srv.URL})
		err := c.CheckModel(context.Background())
		if gotAuth != "" {
			t.Errorf("the probe must not send the key to a public route, sent %q", gotAuth)
		}
		return err, gotPath
	}

	t.Run("a slug with live endpoints is silent", func(t *testing.T) {
		err, path := probe(t, http.StatusOK, liveBody)
		if err != nil {
			t.Errorf("want no verdict, got %v", err)
		}
		if path != "/models/anthropic/claude-sonnet-5/endpoints" {
			t.Errorf("probe hit %q — the /models/{slug} route 404s for every slug, live ones too", path)
		}
	})

	t.Run("200 with an EMPTY endpoints array is the alarm", func(t *testing.T) {
		// This is the exact case that broke beta, and the case a status-only probe misses.
		err, _ := probe(t, http.StatusOK, retiredBody)
		if !errors.Is(err, ErrModelUnavailable) {
			t.Errorf("a retired slug answers 200 with zero endpoints; want ErrModelUnavailable, got %v", err)
		}
	})

	t.Run("404 (slug never existed) is the alarm too", func(t *testing.T) {
		err, _ := probe(t, http.StatusNotFound, `{"error":{"message":"No endpoints found."}}`)
		if !errors.Is(err, ErrModelUnavailable) {
			t.Errorf("want ErrModelUnavailable, got %v", err)
		}
	})

	t.Run("everything unclear is silence, not bad news", func(t *testing.T) {
		for name, tc := range map[string]struct {
			status int
			body   string
		}{
			"provider outage":    {http.StatusInternalServerError, `{"error":{"message":"boom"}}`},
			"proxy demands auth": {http.StatusUnauthorized, `{"error":{"message":"no credentials"}}`},
			"not json at all":    {http.StatusOK, `<html>proxy error</html>`},
			"reshaped api":       {http.StatusOK, `{"data":{"id":"x"}}`},
			"no data envelope":   {http.StatusOK, `{"items":[]}`},
		} {
			t.Run(name, func(t *testing.T) {
				err, _ := probe(t, tc.status, tc.body)
				if err == nil {
					t.Errorf("want an ordinary error, got a clean bill of health")
				}
				if errors.Is(err, ErrModelUnavailable) {
					t.Errorf("must NOT raise the alarm on an unclear answer: %v", err)
				}
			})
		}
	})

	t.Run("an unreachable provider is silence", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()
		err := New(Config{APIKey: "k", BaseURL: url}).CheckModel(context.Background())
		if err == nil || errors.Is(err, ErrModelUnavailable) {
			t.Errorf("a boot with no network must not accuse the model: %v", err)
		}
	})

	t.Run("no key: the provider is not called at all", func(t *testing.T) {
		called := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
		defer srv.Close()
		if err := New(Config{BaseURL: srv.URL}).CheckModel(context.Background()); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("want ErrNotConfigured, got %v", err)
		}
		if called {
			t.Error("a disabled client must not probe")
		}
	})
}

// TestWarnIfModelRetired pins the START-UP CONTRACT, which matters more here than the message:
// this runs while the process is coming up, and a check that can block or crash a boot is a worse
// defect than the one it reports.
func TestWarnIfModelRetired(t *testing.T) {
	t.Run("returns immediately and probes in the background", func(t *testing.T) {
		hit := make(chan string, 1)
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release // the handler is held: a caller that waited for it would be stuck here
			hit <- r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":{"endpoints":[]}}`)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", Model: "m/x", BaseURL: srv.URL})
		start := time.Now()
		c.WarnIfModelRetired()
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("start-up was blocked for %v", elapsed)
		}
		close(release)
		select {
		case path := <-hit:
			if path != "/models/m/x/endpoints" {
				t.Errorf("probed %q", path)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the background probe never ran")
		}
	})

	t.Run("no key: no goroutine, no call", func(t *testing.T) {
		called := make(chan struct{}, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called <- struct{}{} }))
		defer srv.Close()
		New(Config{BaseURL: srv.URL}).WarnIfModelRetired()
		select {
		case <-called:
			t.Error("a disabled client must not probe at boot")
		case <-time.After(300 * time.Millisecond):
		}
	})

	t.Run("a nil client is a no-op, not a boot crash", func(t *testing.T) {
		var c *Client
		c.WarnIfModelRetired()
	})
}

// TestCompleteWithMeta_CarriesFinishReasonAndUsage pins the two numbers the analysis pass cannot
// work without, and it asserts them NON-ZERO on purpose.
//
// Both failure modes here are silent. A forgotten `json:"usage"` tag (the field was not parsed at
// all before this change) leaves every count at zero while the call still succeeds — so a test that
// only checked "no error", or that compared against a zero-valued Usage, would pass on exactly the
// bug it is meant to catch. A dropped finish_reason turns a reply truncated by the token cap into
// something indistinguishable from a model that emitted broken JSON.
func TestCompleteWithMeta_CarriesFinishReasonAndUsage(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		// Shaped like a real OpenRouter reply: usage is a sibling of choices, not a member of one.
		io.WriteString(w, `{"model":"stub-model","choices":[{"message":{"role":"assistant","content":"{\"findings\":[]}"},"finish_reason":"length"}],`+
			`"usage":{"prompt_tokens":4321,"completion_tokens":2500,"total_tokens":6821}}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", Model: "shared/slug", BaseURL: srv.URL})
	text, finishReason, usage, err := c.CompleteWithMeta(context.Background(), "sys", "user", true, 2500)
	if err != nil {
		t.Fatalf("CompleteWithMeta: %v", err)
	}
	if text != `{"findings":[]}` {
		t.Errorf("content = %q", text)
	}
	if finishReason != "length" {
		t.Errorf("finishReason = %q, want %q — without it a truncated reply reads as malformed JSON", finishReason, "length")
	}
	if usage.Prompt != 4321 {
		t.Errorf("usage.Prompt = %d, want 4321 — zero means the `json:\"prompt_tokens\"` field never decoded", usage.Prompt)
	}
	if usage.Completion != 2500 {
		t.Errorf("usage.Completion = %d, want 2500 — zero means the `json:\"completion_tokens\"` field never decoded", usage.Completion)
	}
	if usage.Total != 6821 {
		t.Errorf("usage.Total = %d, want 6821 — zero means the response has no `json:\"usage\"` tag and every run logs as free", usage.Total)
	}
	if !strings.Contains(gotBody, `"max_tokens":2500`) {
		t.Errorf("request body carries no explicit completion cap: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"response_format":{"type":"json_object"}`) {
		t.Errorf("jsonMode did not reach the request: %s", gotBody)
	}
}

// TestCompleteWithMeta_UsesTheAnalysisSlugWhileCompleteKeepsTheSharedOne pins the whole point of
// OPENROUTER_MODEL_ANALYSIS: it escalates the analysis pass ALONE. If it leaked into Complete, note
// formatting and campaign translation would silently move to a different (and pricier) model; if it
// never reached CompleteWithMeta, the variable would be decoration.
func TestCompleteWithMeta_UsesTheAnalysisSlugWhileCompleteKeepsTheSharedOne(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", Model: "shared/slug", ModelAnalysis: "escalated/slug", BaseURL: srv.URL})
	if _, _, _, err := c.CompleteWithMeta(context.Background(), "sys", "user", false, 0); err != nil {
		t.Fatalf("CompleteWithMeta: %v", err)
	}
	if _, err := c.Complete(context.Background(), "sys", "user", false); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("want 2 requests, got %d", len(bodies))
	}
	if !strings.Contains(bodies[0], `"model":"escalated/slug"`) {
		t.Errorf("CompleteWithMeta did not send the analysis slug: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], `"model":"shared/slug"`) {
		t.Errorf("Complete must stay on the shared slug, sent: %s", bodies[1])
	}
	// maxTokens <= 0 must leave the provider default in force rather than send a cap of zero,
	// which the API would read as "no room for an answer".
	if strings.Contains(bodies[1], "max_tokens") {
		t.Errorf("an unset cap must be omitted from the request, not sent as zero: %s", bodies[1])
	}
}

// TestAnalysisModel_UnsetOverrideMeansTheSharedSlug covers the state every deployment is actually
// in. Empty is not an error and must not become a second baked-in default.
func TestAnalysisModel_UnsetOverrideMeansTheSharedSlug(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  Config
		want string
	}{
		"override unset":        {Config{Model: "shared/slug"}, "shared/slug"},
		"override blank":        {Config{Model: "shared/slug", ModelAnalysis: "   "}, "shared/slug"},
		"override set":          {Config{Model: "shared/slug", ModelAnalysis: " escalated/slug "}, "escalated/slug"},
		"nothing configured":    {Config{}, defaultModel},
		"override with no base": {Config{ModelAnalysis: "escalated/slug"}, "escalated/slug"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := New(tc.cfg).AnalysisModel(); got != tc.want {
				t.Errorf("AnalysisModel() = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("a nil client is not a panic", func(t *testing.T) {
		var c *Client
		if got := c.AnalysisModel(); got != "" {
			t.Errorf("AnalysisModel() = %q, want empty", got)
		}
	})
}

// TestWarnIfModelRetired_ProbesEveryEffectiveSlugOnce extends the boot warning to the SET of slugs
// the client can send. A retired analysis slug is the same invisible fault as a retired shared one
// — and probing a single slug twice, when the override happens to equal the shared value, would
// shout twice about one fault and double the boot traffic.
func TestWarnIfModelRetired_ProbesEveryEffectiveSlugOnce(t *testing.T) {
	probe := func(t *testing.T, cfg Config, want int) []string {
		t.Helper()
		var mu sync.Mutex
		paths := []string{}
		done := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			if len(paths) == want {
				close(done)
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":{"endpoints":[]}}`) // the retired shape: 200, zero endpoints
		}))
		defer srv.Close()

		cfg.APIKey, cfg.BaseURL = "k", srv.URL
		New(cfg).WarnIfModelRetired()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			mu.Lock()
			got := append([]string{}, paths...)
			mu.Unlock()
			t.Fatalf("want %d probes, saw %v", want, got)
		}
		// Give a stray extra probe a chance to arrive before counting.
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if len(paths) != want {
			t.Fatalf("want %d probes, got %d: %v", want, len(paths), paths)
		}
		return append([]string{}, paths...)
	}

	// The subtests below pin the shared/analysis pair, so they switch the Ideas door off; the ideas
	// slugs have subtests of their own at the end.
	t.Run("both slugs are probed when the override differs", func(t *testing.T) {
		paths := probe(t, Config{Model: "shared/slug", ModelAnalysis: "escalated/slug", ModelIdeas: IdeasModelOff}, 2)
		seen := map[string]bool{}
		for _, p := range paths {
			seen[p] = true
		}
		if !seen["/models/shared/slug/endpoints"] {
			t.Errorf("the shared slug was not probed: %v", paths)
		}
		if !seen["/models/escalated/slug/endpoints"] {
			t.Errorf("the analysis slug was not probed — a retired override would stay invisible: %v", paths)
		}
	})

	t.Run("one probe when the override repeats the shared slug", func(t *testing.T) {
		paths := probe(t, Config{Model: "shared/slug", ModelAnalysis: "shared/slug", ModelIdeas: IdeasModelOff}, 1)
		if paths[0] != "/models/shared/slug/endpoints" {
			t.Errorf("probed %v", paths)
		}
	})

	t.Run("one probe when no override is set", func(t *testing.T) {
		paths := probe(t, Config{Model: "shared/slug", ModelIdeas: IdeasModelOff}, 1)
		if paths[0] != "/models/shared/slug/endpoints" {
			t.Errorf("probed %v", paths)
		}
	})

	// PLAYGROUND B-15: the ideas slug and its fallback are baked in, so both are probed — a retired
	// default would otherwise be found by the first person pressing Ideas.
	// Mutation: drop the ideas block from effectiveModels → 1 probe, not 3 → red.
	t.Run("the ideas default and its fallback are probed", func(t *testing.T) {
		paths := probe(t, Config{Model: "shared/slug"}, 3)
		want := []string{"/models/shared/slug/endpoints", "/models/" + DefaultIdeasModel + "/endpoints",
			"/models/" + IdeasFallbackModel + "/endpoints"}
		for i := range want {
			if paths[i] != want[i] {
				t.Errorf("probe %d = %q, want %q (all: %v)", i, paths[i], want[i], paths)
			}
		}
	})
	t.Run("an ideas override equal to the fallback is probed once", func(t *testing.T) {
		probe(t, Config{Model: "shared/slug", ModelIdeas: IdeasFallbackModel}, 2)
	})
	t.Run("ideas off: neither ideas slug is probed", func(t *testing.T) {
		paths := probe(t, Config{Model: "shared/slug", ModelIdeas: "Off"}, 1)
		if paths[0] != "/models/shared/slug/endpoints" {
			t.Errorf("probed %v", paths)
		}
	})
}

// TestEffectiveModels_NameWhatStopsWorking pins the `affects` half of the boot warning: every slug
// the client can send is listed with the features that refuse once the provider stops serving it. A
// feature missing from its slug's label turns the one log line meant to send somebody to the right
// button into a line about the wrong ones — EnhanceText rides CompleteWithMeta, so it lives on the
// analysis slug and has to be named there.
//
// Mutation: drop EnhanceText from analysisModelFeatures → red.
func TestEffectiveModels_NameWhatStopsWorking(t *testing.T) {
	type want struct {
		slug     string
		features []string
	}
	check := func(t *testing.T, cfg Config, wants []want) {
		t.Helper()
		got := New(cfg).effectiveModels()
		if len(got) != len(wants) {
			t.Fatalf("effectiveModels() = %+v, want %d slugs", got, len(wants))
		}
		for i, w := range wants {
			if got[i].slug != w.slug {
				t.Errorf("slug %d = %q, want %q", i, got[i].slug, w.slug)
			}
			for _, f := range w.features {
				if !strings.Contains(got[i].features, f) {
					t.Errorf("slug %q affects %q — %q is missing", got[i].slug, got[i].features, f)
				}
			}
		}
	}
	shared := []string{"note formatting", "design idea drafts", "campaign auto-translation"}
	analysis := []string{"tech-card construction analysis", "EnhanceText"}

	t.Run("four distinct slugs, four labels", func(t *testing.T) {
		check(t, Config{APIKey: "k", Model: "shared/slug", ModelAnalysis: "escalated/slug"}, []want{
			{"shared/slug", shared},
			{"escalated/slug", analysis},
			{DefaultIdeasModel, []string{"playground Ideas suggestions"}},
			{IdeasFallbackModel, []string{"playground Ideas suggestions", "fallback"}},
		})
	})
	t.Run("no analysis override: the shared slug carries both labels", func(t *testing.T) {
		check(t, Config{APIKey: "k", Model: "shared/slug", ModelIdeas: IdeasModelOff}, []want{
			{"shared/slug", append(append([]string{}, shared...), analysis...)},
		})
	})
}

// TestIdeasModel pins the one place that decides what OPENROUTER_MODEL_IDEAS means.
func TestIdeasModel(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", DefaultIdeasModel},
		{"   ", DefaultIdeasModel},
		{"off", ""},
		{" OFF ", ""},
		{"x/y", "x/y"},
		{" x/y ", "x/y"},
	} {
		if got := New(Config{APIKey: "k", ModelIdeas: tc.in}).IdeasModel(); got != tc.want {
			t.Errorf("IdeasModel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := (*Client)(nil).IdeasModel(); got != "" {
		t.Errorf("nil client IdeasModel = %q", got)
	}
}

// TestEmptyAnswerIsSplitByFinishReason pins the classification that reached production as its
// opposite. The first live analysis run on prod came back with 2500 completion tokens spent, an
// empty message, and a panel that said «this one is weather: retry» — an invitation to spend the
// same $0.11 again, forever, on a fault that is perfectly deterministic.
//
// THE TWO HALVES ARE THE POINT. Empty AT THE CAP is a configuration fault (the budget was gone
// before the answer began); empty for any OTHER reason is a misbehaving provider and must NOT
// borrow the configuration verdict, or a genuinely odd reply would send somebody to edit a setting
// that is correct. A test with only the first half passes on `return ErrBudgetExhausted` for every
// empty message, which is the mistake next door.
func TestEmptyAnswerIsSplitByFinishReason(t *testing.T) {
	cases := map[string]struct {
		finishReason string
		wantSentinel bool
	}{
		"empty at the cap is a configuration fault": {"length", true},
		"empty after a normal stop is not":          {"stop", false},
		"empty with no reason at all is not":        {"", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"model":"stub-model","choices":[{"message":{"role":"assistant","content":"  "},`+
					`"finish_reason":"`+tc.finishReason+`"}],`+
					`"usage":{"prompt_tokens":10149,"completion_tokens":2500,"total_tokens":12649}}`)
			}))
			defer srv.Close()

			c := New(Config{APIKey: "k", Model: "shared/slug", BaseURL: srv.URL})
			_, finishReason, usage, err := c.CompleteWithMeta(context.Background(), "sys", "user", true, 2500)
			if err == nil {
				t.Fatal("an empty message must be an error whatever stopped it")
			}
			if got := errors.Is(err, ErrBudgetExhausted); got != tc.wantSentinel {
				t.Errorf("errors.Is(err, ErrBudgetExhausted) = %v, want %v — err was %v", got, tc.wantSentinel, err)
			}
			// The spend rides along on BOTH branches: an empty answer is not a free call, and the
			// log line that reports it is the only place the money would otherwise disappear from.
			if usage.Completion != 2500 {
				t.Errorf("usage.Completion = %d, want 2500 — a failed run still cost the full budget", usage.Completion)
			}
			if finishReason != tc.finishReason {
				t.Errorf("finishReason = %q, want %q", finishReason, tc.finishReason)
			}
			if tc.wantSentinel && !strings.Contains(err.Error(), "2500") {
				t.Errorf("the error does not name what was spent: %v", err)
			}
		})
	}
}

// TestAnalysisPassTurnsExtendedThinkingOff pins the fix for that same outage at its source.
//
// Reasoning tokens are billed and budgeted as OUTPUT tokens, so a reasoning slug pointed at a cap
// sized for a non-reasoning one spends the whole cap thinking and answers nothing. The analysis
// pass therefore says so explicitly instead of trusting the provider's default.
//
// AND IT SAYS IT NOWHERE ELSE. Complete backs note formatting and campaign translation, which never
// had a `reasoning` field and must keep the provider default: sending it from the shared path would
// change two features to fix one.
func TestAnalysisPassTurnsExtendedThinkingOff(t *testing.T) {
	body := func(t *testing.T, call func(*Client, context.Context) error) string {
		t.Helper()
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}))
		defer srv.Close()
		c := New(Config{APIKey: "k", Model: "shared/slug", BaseURL: srv.URL})
		if err := call(c, context.Background()); err != nil {
			t.Fatalf("call: %v", err)
		}
		return got
	}

	analysis := body(t, func(c *Client, ctx context.Context) error {
		_, _, _, err := c.CompleteWithMeta(ctx, "sys", "user", true, 2500)
		return err
	})
	if !strings.Contains(analysis, `"reasoning":{"effort":"none"}`) {
		t.Errorf("the analysis pass did not turn extended thinking off: %s", analysis)
	}

	shared := body(t, func(c *Client, ctx context.Context) error {
		_, err := c.Complete(ctx, "sys", "user", false)
		return err
	})
	if strings.Contains(shared, `"reasoning"`) {
		t.Errorf("the shared path must not carry a reasoning field — note formatting and campaign translation hang off it: %s", shared)
	}
}
