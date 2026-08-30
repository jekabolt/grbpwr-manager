package orimages

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pngBytes is a minimal but genuine PNG header + a little payload. It is a real signature on
// purpose: the media-type fallback sniffs bytes, and sniffing "hello" would prove nothing.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), []byte("\x00\x00\x00\rIHDR-not-a-real-image")...)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// okResponse is a well-formed provider reply carrying one PNG and a real cost.
func okResponse(mediaType string) string {
	mt := ""
	if mediaType != "" {
		mt = fmt.Sprintf(`,"media_type":%q`, mediaType)
	}
	return fmt.Sprintf(
		`{"created":1748372400,"data":[{"b64_json":%q%s}],`+
			`"usage":{"prompt_tokens":31,"completion_tokens":4175,"total_tokens":4206,"cost":0.042}}`,
		b64(pngBytes), mt)
}

// TestGenerate_RoundTrip is the whole happy path against a stubbed provider: the endpoint we hit,
// the auth header, every request field, and the decode of the reply.
//
// THE ENDPOINT ASSERTION IS THE POINT OF THIS PACKAGE. /images is a different route from
// /chat/completions, reached from a different catalogue; if this test ever passes with the chat
// path it means the package has quietly become a second way to do the same thing.
func TestGenerate_RoundTrip(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, okResponse("image/png"))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "secret-key", BaseURL: srv.URL})
	compression := 90
	res, err := c.Generate(context.Background(), Request{
		Prompt:            "technical flat, front view",
		N:                 1,
		AspectRatio:       "16:9",
		Quality:           "high",
		Background:        "opaque",
		OutputFormat:      "png",
		OutputCompression: &compression,
		InputReferences:   []string{"https://media.grbpwr.com/moodboard/1.png"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if gotPath != "/images" {
		t.Errorf("path = %q, want %q — the image catalogue is NOT reachable from /chat/completions", gotPath, "/images")
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("auth header = %q", gotAuth)
	}
	for _, want := range []string{
		`"model":"openai/gpt-image-2"`,
		`"prompt":"technical flat, front view"`,
		`"n":1`,
		// 16:9 is a ratio ONLY gpt-image-2 takes (gpt-image-1 stops at 1:1/3:2/2:3/auto), so this
		// line also says the default really is the newer slug.
		`"aspect_ratio":"16:9"`,
		`"quality":"high"`,
		`"background":"opaque"`,
		`"output_format":"png"`,
		`"output_compression":90`,
		`"input_references":["https://media.grbpwr.com/moodboard/1.png"]`,
	} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("request body missing %s\nbody: %s", want, gotBody)
		}
	}

	if len(res.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(res.Images))
	}
	if string(res.Images[0].Bytes) != string(pngBytes) {
		t.Errorf("decoded bytes do not match what the provider sent")
	}
	if res.Images[0].MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", res.Images[0].MediaType)
	}
	if res.Model != DefaultModel {
		t.Errorf("result model = %q, want the slug actually sent (%q)", res.Model, DefaultModel)
	}
	// Usage is asserted as NUMBERS, not as "no error": a misspelled json tag leaves every field at
	// zero, the call still succeeds, and every generation reads as free for ever.
	if res.Usage.Cost != 0.042 {
		t.Errorf("usage.Cost = %v, want 0.042 — zero means `json:\"cost\"` never decoded and the run ledger records free pictures", res.Usage.Cost)
	}
	if res.Usage.Completion != 4175 || res.Usage.Total != 4206 || res.Usage.Prompt != 31 {
		t.Errorf("usage tokens = %+v, want 31/4175/4206", res.Usage)
	}
}

// TestGenerate_OmitsUnsetFields proves the zero value of every optional knob leaves the provider's
// own default in force instead of sending an explicit zero, which several of these enums reject.
func TestGenerate_OmitsUnsetFields(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, okResponse("image/png"))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	if _, err := c.Generate(context.Background(), Request{Prompt: "p"}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, absent := range []string{"aspect_ratio", "quality", "background", "output_format", "output_compression", "input_references", `"n"`} {
		if strings.Contains(gotBody, absent) {
			t.Errorf("request body carries %s although the caller never set it: %s", absent, gotBody)
		}
	}
}

// TestGenerate_BackgroundIsNeverRewritten is the guard on the ONE parameter the move to
// gpt-image-2 narrowed.
//
// The default model takes `background: [auto, opaque]`; gpt-image-1 also took `transparent`
// (measured against GET /api/v1/images/models, 2026-08-30). The tempting "helpful" fix is to drop
// or remap a transparent request so nothing 400s. THIS PACKAGE MUST NOT DO THAT: a silently
// rewritten background is a picture that differs from the one that was ordered, and the difference
// surfaces hours later in a sheet nobody can lay over anything. A 400 naming the parameter is the
// cheap, loud outcome — and TestGenerate_UnclassifiedStatusStaysGeneric already proves that 400
// arrives as our own bad request rather than as retryable weather.
//
// So: whatever the caller puts in Background reaches the wire verbatim, supported or not.
func TestGenerate_BackgroundIsNeverRewritten(t *testing.T) {
	for _, bg := range []string{"opaque", "auto", "transparent"} {
		t.Run(bg, func(t *testing.T) {
			var gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				io.WriteString(w, okResponse("image/png"))
			}))
			defer srv.Close()

			c := New(Config{APIKey: "k", BaseURL: srv.URL})
			if _, err := c.Generate(context.Background(), Request{Prompt: "p", Background: bg}); err != nil {
				t.Fatalf("Generate: %v", err)
			}
			want := fmt.Sprintf(`"background":%q`, bg)
			if !strings.Contains(gotBody, want) {
				t.Errorf("body missing %s — the package rewrote or dropped a background the caller chose: %s",
					want, gotBody)
			}
		})
	}
}

// TestGenerate_ModelOverride proves one client can address a second slug on the same endpoint —
// the seam the vector model will come through, since /images is genuinely the same route for it.
func TestGenerate_ModelOverride(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, okResponse("image/svg+xml"))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	res, err := c.Generate(context.Background(), Request{Prompt: "p", Model: "recraft/recraft-v4-vector"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(gotBody, `"model":"recraft/recraft-v4-vector"`) {
		t.Errorf("override did not reach the wire: %s", gotBody)
	}
	if res.Model != "recraft/recraft-v4-vector" {
		t.Errorf("result model = %q — provenance must name the slug that was CALLED, not the configured one", res.Model)
	}
	if res.Images[0].MediaType != "image/svg+xml" {
		t.Errorf("media type = %q, want the provider's own label", res.Images[0].MediaType)
	}
}

// TestGenerate_ProviderErrorsAreClassifiedByStatus pins the split a caller needs in order to decide
// whether trying again is free, expensive, or pointless — WITHOUT reading the provider's English.
func TestGenerate_ProviderErrorsAreClassifiedByStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
		// msg is the provider's own sentence, which must survive classification. It is stated per
		// case rather than checked against a list of known phrases: a list silently accepts any new
		// case whose wording it does not happen to contain, which is how a message-dropping bug
		// would pass unnoticed the day someone adds a status.
		msg string
	}{
		{"404 is a settings fault", http.StatusNotFound,
			`{"error":{"message":"No image model found for \"anthropic/claude-sonnet-5\"","code":404}}`, ErrModelUnavailable, "No image model found"},
		{"429 is weather", http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`, ErrRateLimited, "rate limited"},
		// 401/403 and 402 used to fall through to the unclassified default, which a caller reads as
		// "we cannot tell whether this was billed". Both are provably unbilled and both name their
		// own remedy, so saying "unknown" was both false and expensive.
		{"401 is a rejected key, not weather", http.StatusUnauthorized,
			`{"error":{"message":"No auth credentials found","code":401}}`, ErrUnauthorized, "No auth credentials found"},
		{"403 is a rejected key too", http.StatusForbidden,
			`{"error":{"message":"forbidden"}}`, ErrUnauthorized, "forbidden"},
		{"402 is an empty balance, and waiting will not fill it", http.StatusPaymentRequired,
			`{"error":{"message":"Insufficient credits","code":402}}`, ErrOutOfCredit, "Insufficient credits"},
		{"502 is an unbilled provider failure", http.StatusBadGateway, `{"error":{"message":"upstream failed"}}`, ErrProviderFailure, "upstream failed"},
		{"503 is an unbilled provider failure", http.StatusServiceUnavailable, `{"error":{"message":"down"}}`, ErrProviderFailure, "down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			c := New(Config{APIKey: "k", BaseURL: srv.URL})
			res, err := c.Generate(context.Background(), Request{Prompt: "p"})
			if res != nil {
				t.Errorf("result must be nil when nothing was produced, got %+v", res)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want it to wrap %v", err, tc.want)
			}
			// The provider's own sentence must survive classification, or the log loses the only
			// description of what actually went wrong.
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("classified error dropped the provider's message %q: %v", tc.msg, err)
			}
		})
	}
}

// TestGenerate_UnclassifiedStatusStaysGeneric: a 400 is our own malformed request, not one of the
// named faults, and must NOT masquerade as retryable weather.
func TestGenerate_UnclassifiedStatusStaysGeneric(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"background transparent is not supported"}}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	_, err := c.Generate(context.Background(), Request{Prompt: "p"})
	if err == nil {
		t.Fatal("a 400 must be an error")
	}
	for _, sentinel := range []error{ErrRateLimited, ErrProviderFailure, ErrModelUnavailable} {
		if errors.Is(err, sentinel) {
			t.Errorf("a 400 was classified as %v — a caller would retry our own bad request", sentinel)
		}
	}
	if !strings.Contains(err.Error(), "background transparent is not supported") {
		t.Errorf("the provider's explanation was dropped: %v", err)
	}
}

// TestGenerate_ResponseCeilingIsLoud is the regression this task exists for.
//
// The pair matters: a body EXACTLY at the ceiling must succeed, and a body ONE BYTE over must fail
// with a named error. Without the first half, a ceiling that refuses everything would pass; without
// the second, the old silent-truncation behaviour would pass. Truncation is the dangerous half for
// pictures specifically — a cut base64 string still decodes, into half an image.
func TestGenerate_ResponseCeilingIsLoud(t *testing.T) {
	// Build a real reply, then pad it with JSON whitespace so the ONLY difference between the two
	// cases is length. Whitespace keeps the over-limit body valid JSON: this proves the refusal is
	// the ceiling talking, not a decode failure that happens to look like one.
	base := okResponse("image/png")

	t.Run("exactly at the ceiling is fine", func(t *testing.T) {
		body := base
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, body)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", BaseURL: srv.URL, MaxResponseBytes: int64(len(body))})
		if _, err := c.Generate(context.Background(), Request{Prompt: "p"}); err != nil {
			t.Fatalf("a body exactly at the ceiling must be accepted, got %v", err)
		}
	})

	t.Run("one byte over is refused by name", func(t *testing.T) {
		body := base + " " // still valid JSON, one byte longer
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, body)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", BaseURL: srv.URL, MaxResponseBytes: int64(len(body) - 1)})
		res, err := c.Generate(context.Background(), Request{Prompt: "p"})
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("err = %v, want ErrResponseTooLarge — a silently truncated base64 string decodes into HALF AN IMAGE", err)
		}
		if res != nil {
			t.Errorf("a refused body must not produce a result: %+v", res)
		}
		if !strings.Contains(err.Error(), "OPENROUTER_IMAGES_MAX_RESPONSE_BYTES") {
			t.Errorf("the error must name the knob to turn: %v", err)
		}
	})
}

// TestGenerate_NotConfigured: no key means the client refuses BEFORE the network, so a
// misconfigured deployment cannot even reach the provider, let alone be billed by it.
func TestGenerate_NotConfigured(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	for _, c := range []*Client{
		nil,
		New(Config{BaseURL: srv.URL}),
		New(Config{APIKey: "   ", BaseURL: srv.URL}),
	} {
		if c.Enabled() {
			t.Errorf("client with no usable key reports Enabled()")
		}
		if _, err := c.Generate(context.Background(), Request{Prompt: "p"}); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("err = %v, want ErrNotConfigured", err)
		}
	}
	if called {
		t.Error("an unconfigured client reached the provider — the refusal must be local")
	}
	if err := (*Client)(nil).CheckModel(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("CheckModel on a nil client = %v, want ErrNotConfigured", err)
	}
}

// TestGenerate_NoImagesStillReportsTheMoney: a 200 with an empty data array WAS BILLED. The error
// says no picture arrived; the result carries what it cost, because a ledger that records only
// successes under-reports exactly the spend that was wasted.
func TestGenerate_NoImagesStillReportsTheMoney(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"created":1,"data":[],"usage":{"prompt_tokens":10,"completion_tokens":0,"total_tokens":10,"cost":0.017}}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	res, err := c.Generate(context.Background(), Request{Prompt: "p"})
	if !errors.Is(err, ErrNoImages) {
		t.Fatalf("err = %v, want ErrNoImages", err)
	}
	if res == nil {
		t.Fatal("a billed failure must still hand back the usage")
	}
	if res.Usage.Cost != 0.017 {
		t.Errorf("usage.Cost = %v, want 0.017 — the money was spent whether or not a picture arrived", res.Usage.Cost)
	}
}

// TestGenerate_ErrorInsideA200 covers a body that says 200 but carries an error object.
func TestGenerate_ErrorInsideA200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"error":{"message":"content policy refused this prompt"}}`)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	_, err := c.Generate(context.Background(), Request{Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "content policy refused this prompt") {
		t.Fatalf("err = %v, want the provider's own explanation", err)
	}
	if errors.Is(err, ErrNoImages) {
		t.Error("an explicit provider error must not be reported as an empty result — it blames the wrong thing")
	}
}

// TestGenerate_MalformedBodies covers a reply that is not JSON at all and one whose base64 is junk.
func TestGenerate_MalformedBodies(t *testing.T) {
	t.Run("not json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "<html>gateway</html>")
		}))
		defer srv.Close()
		c := New(Config{APIKey: "k", BaseURL: srv.URL})
		if _, err := c.Generate(context.Background(), Request{Prompt: "p"}); err == nil ||
			!strings.Contains(err.Error(), "decode") {
			t.Fatalf("err = %v, want a decode failure", err)
		}
	})
	t.Run("b64 is junk, and the money still comes back", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"data":[{"b64_json":"!!!! not base64 !!!!","media_type":"image/png"}],`+
				`"usage":{"total_tokens":9,"cost":0.031}}`)
		}))
		defer srv.Close()
		c := New(Config{APIKey: "k", BaseURL: srv.URL})
		res, err := c.Generate(context.Background(), Request{Prompt: "p"})
		if err == nil || !strings.Contains(err.Error(), "base64") {
			t.Fatalf("err = %v, want a base64 failure naming the field", err)
		}
		// Broken bytes were still a paid generation: the run fails, the price is recorded anyway.
		if res == nil || res.Usage.Cost != 0.031 {
			t.Errorf("usage after a broken payload = %+v, want cost 0.031 carried back", res)
		}
		if res != nil && len(res.Images) != 0 {
			t.Errorf("a half-decoded set must not escape: %d images", len(res.Images))
		}
	})
	t.Run("b64 is empty", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"data":[{"b64_json":"","media_type":"image/png"}]}`)
		}))
		defer srv.Close()
		c := New(Config{APIKey: "k", BaseURL: srv.URL})
		if _, err := c.Generate(context.Background(), Request{Prompt: "p"}); err == nil {
			t.Fatal("an empty payload must not pass as a zero-byte image")
		}
	})
}

// TestGenerate_MediaTypeFallsBackToSniffing: the provider omits media_type "only when it could not
// be determined", and a bucket upload with no content type serves a file browsers will not render.
func TestGenerate_MediaTypeFallsBackToSniffing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okResponse("")) // no media_type key at all
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	res, err := c.Generate(context.Background(), Request{Prompt: "p"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.Images[0].MediaType != "image/png" {
		t.Errorf("sniffed media type = %q, want image/png", res.Images[0].MediaType)
	}
}

// TestGenerate_RefusesBadInputBeforeTheNetwork: every one of these is OUR mistake, and finding it
// at the provider would cost a round trip and read in the log as a provider fault.
func TestGenerate_RefusesBadInputBeforeTheNetwork(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++ }))
	defer srv.Close()
	c := New(Config{APIKey: "k", BaseURL: srv.URL})

	tooMany := make([]string, maxInputReferences+1)
	for i := range tooMany {
		tooMany[i] = "https://media.grbpwr.com/x.png"
	}
	bad := 101

	cases := []struct {
		name string
		req  Request
		want string
	}{
		{"no prompt", Request{Prompt: "   "}, "needs a prompt"},
		{"n too large", Request{Prompt: "p", N: maxImagesPerRequest + 1}, "outside the supported range"},
		{"n negative", Request{Prompt: "p", N: -1}, "outside the supported range"},
		{"too many references", Request{Prompt: "p", InputReferences: tooMany}, "exceeds the 16"},
		{"reference is not a url", Request{Prompt: "p", InputReferences: []string{"file:///etc/passwd"}}, "must be http(s)"},
		{"reference is empty", Request{Prompt: "p", InputReferences: []string{"  "}}, "empty reference address"},
		{"data uri without payload", Request{Prompt: "p", InputReferences: []string{"data:image/png"}}, "no payload"},
		{"compression out of range", Request{Prompt: "p", OutputCompression: &bad}, "outside 0..100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Generate(context.Background(), tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	if called != 0 {
		t.Errorf("%d bad requests reached the provider; all of them are ours to catch", called)
	}
}

// TestGenerate_AcceptsADataURIReference proves the second admitted form actually goes through, so
// the validation above is a filter and not a wall.
func TestGenerate_AcceptsADataURIReference(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, okResponse("image/png"))
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL})
	ref := "data:image/png;base64," + b64(pngBytes)
	if _, err := c.Generate(context.Background(), Request{Prompt: "p", InputReferences: []string{ref}}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(gotBody, "data:image/png;base64,") {
		t.Errorf("data URI reference did not reach the wire: %s", gotBody)
	}
}

// TestDefaults documents the values a deployment gets for free — including WHICH slug.
//
// THE SLUG IS SPELLED OUT rather than compared to DefaultModel, because a test that reads the
// constant it is guarding agrees with any typo the constant acquires. gpt-image-2 is the owner's
// choice for the raster half of "good raster, then a good vector"; the earlier gpt-image-1 was held
// only for `background: transparent`, which the band's prompts do not ask for (they order a white
// background in words) and the vectoriser does not carry.
func TestDefaults(t *testing.T) {
	c := New(Config{APIKey: "k"})
	if c.Model() != "openai/gpt-image-2" {
		t.Errorf("default model = %q, want openai/gpt-image-2 — the slug the live image catalogue serves", c.Model())
	}
	if c.BaseURL() != defaultBaseURL {
		t.Errorf("default base url = %q", c.BaseURL())
	}
	if c.MaxResponseBytes() != defaultMaxResponseBytes {
		t.Errorf("default ceiling = %d, want %d", c.MaxResponseBytes(), defaultMaxResponseBytes)
	}
	if got := New(Config{APIKey: "k", HTTPTimeout: 0}).http.Timeout; got != defaultTimeout {
		t.Errorf("default timeout = %v, want %v", got, defaultTimeout)
	}
	if got := New(Config{APIKey: "k", HTTPTimeout: 5 * time.Second}).http.Timeout; got != 5*time.Second {
		t.Errorf("configured timeout = %v, want it honoured", got)
	}
	if (*Client)(nil).Model() != "" || (*Client)(nil).BaseURL() != "" || (*Client)(nil).MaxResponseBytes() != 0 {
		t.Error("a nil client must be nil-safe on every accessor")
	}
}

// TestCheckModel_ReadsTheIMAGEProbeShape is the trap this package could most easily have fallen
// into: the chat probe answers {"data":{"endpoints":[…]}} while the image probe answers
// {"id":…,"endpoints":[…]} — endpoints at the TOP level. A struct copied from the chat client would
// decode every image response to "no endpoints field", land in the silent branch, and never warn
// about anything at all.
func TestCheckModel_ReadsTheIMAGEProbeShape(t *testing.T) {
	t.Run("live slug", func(t *testing.T) {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			io.WriteString(w, `{"id":"openai/gpt-image-2","endpoints":[{"provider_name":"OpenAI"}]}`)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", BaseURL: srv.URL})
		if err := c.CheckModel(context.Background()); err != nil {
			t.Fatalf("CheckModel: %v", err)
		}
		if gotPath != "/images/models/openai/gpt-image-2/endpoints" {
			t.Errorf("probe path = %q — the image catalogue lives under /images/models", gotPath)
		}
	})

	t.Run("chat-shaped body is not read as live", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The CHAT route's shape. If this passes as "live", the probe is reading the wrong
			// struct and would be equally blind to a real answer.
			io.WriteString(w, `{"data":{"endpoints":[{"provider_name":"OpenAI"}]}}`)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", BaseURL: srv.URL})
		err := c.CheckModel(context.Background())
		if err == nil {
			t.Fatal("a chat-shaped probe body must not be accepted as a live image endpoint")
		}
		if !strings.Contains(err.Error(), "no endpoints field") {
			t.Errorf("err = %v, want the 'could not find out' branch (silence), not a verdict", err)
		}
		if errors.Is(err, ErrModelUnavailable) {
			t.Error("an unreadable body must NOT be reported as a retired model — that alarm has to stay trustworthy")
		}
	})

	t.Run("retired slug: 200 with an empty array", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"id":"openai/gone","endpoints":[]}`)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "openai/gone"})
		if err := c.CheckModel(context.Background()); !errors.Is(err, ErrModelUnavailable) {
			t.Fatalf("err = %v, want ErrModelUnavailable — an empty array IS the alarm", err)
		}
	})

	t.Run("a chat slug answers 404 here", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"No image model found for \"anthropic/claude-sonnet-5\"","code":404}}`)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", BaseURL: srv.URL, Model: "anthropic/claude-sonnet-5"})
		err := c.CheckModel(context.Background())
		if !errors.Is(err, ErrModelUnavailable) {
			t.Fatalf("err = %v, want ErrModelUnavailable", err)
		}
		if !strings.Contains(err.Error(), "CHAT slug") {
			t.Errorf("the message should name the likely cause: %v", err)
		}
	})

	t.Run("unreachable provider is silence, not a verdict", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", BaseURL: srv.URL})
		err := c.CheckModel(context.Background())
		if err == nil {
			t.Fatal("a 500 must still be an error")
		}
		if errors.Is(err, ErrModelUnavailable) {
			t.Error("a 500 is not evidence that the model is gone")
		}
	})
}

// TestWarnIfModelRetired_IsSilentWithoutAKey: no key means nothing is calling the provider, so
// there is nothing to warn about — and no probe traffic from a deployment that has the feature off.
func TestWarnIfModelRetired_IsSilentWithoutAKey(t *testing.T) {
	called := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case called <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	New(Config{BaseURL: srv.URL}).WarnIfModelRetired()
	(*Client)(nil).WarnIfModelRetired() // must not panic
	select {
	case <-called:
		t.Error("a keyless client probed the provider at boot")
	case <-time.After(150 * time.Millisecond):
	}
}

// TestWarnIfModelRetired_ProbesOnce proves the boot warning actually reaches the image route.
func TestWarnIfModelRetired_ProbesOnce(t *testing.T) {
	paths := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		io.WriteString(w, `{"id":"openai/gpt-image-2","endpoints":[{"provider_name":"OpenAI"}]}`)
	}))
	defer srv.Close()

	New(Config{APIKey: "k", BaseURL: srv.URL}).WarnIfModelRetired()
	select {
	case p := <-paths:
		if p != "/images/models/openai/gpt-image-2/endpoints" {
			t.Errorf("probe path = %q", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the boot probe never fired")
	}
	select {
	case p := <-paths:
		t.Errorf("the probe fired twice (second path %q) — one slug, one request", p)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestRequestWireIsValidJSON is a cheap guard that the body we build is the object the API expects,
// decoded back rather than eyeballed as a substring.
func TestRequestWireIsValidJSON(t *testing.T) {
	c := New(Config{APIKey: "k"})
	wire, err := c.buildRequest(Request{Prompt: "p", Quality: " high ", InputReferences: []string{" https://x/y.png "}})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back["quality"] != "high" {
		t.Errorf("quality = %v, want the trimmed value", back["quality"])
	}
	refs, _ := back["input_references"].([]any)
	if len(refs) != 1 || refs[0] != "https://x/y.png" {
		t.Errorf("input_references = %v, want the trimmed address", back["input_references"])
	}
}
