package recraft

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// orService wires the PRIMARY route — the shared OpenRouter image client — at a test server. It is
// the real internal/orimages client, not a stand-in: the seam between the two packages is exactly
// what a fake would stop testing.
func orService(t *testing.T, srvURL string) *Client {
	t.Helper()
	gen := NewOpenRouterGenerator(orimages.New(orimages.Config{APIKey: "test-key", BaseURL: srvURL}))
	return NewWithGenerator(RouteOpenRouter, gen, map[Tier]string{
		TierVector:    ModelORVector,
		TierProVector: ModelORVectorPro,
	})
}

// refURL digs the address out of one input_references element. Since 2026-08-30 the wire shape is
// the object {"type":"image_url","image_url":{"url":…}} — the endpoint's validator refuses a bare
// string with `invalid_type: expected object, received string`.
func refURL(v any) string {
	m, _ := v.(map[string]any)
	iu, _ := m["image_url"].(map[string]any)
	u, _ := iu["url"].(string)
	return u
}

func TestOpenRouter_Success(t *testing.T) {
	var seenPath string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		fmt.Fprintf(w, `{"data":[{"b64_json":%q,"media_type":"image/svg+xml"}],"usage":{"cost":0.08}}`,
			base64.StdEncoding.EncodeToString([]byte(sampleSVG)))
	}))
	defer srv.Close()

	req := redrawRequest()
	req.NegativePrompt = "photorealism, shadows"
	res, err := orService(t, srv.URL).ImageToImage(context.Background(), req)
	if err != nil {
		t.Fatalf("ImageToImage: %v", err)
	}

	// The SECOND catalogue: pictures live at /images, not at /chat/completions.
	if seenPath != "/images" {
		t.Fatalf("called %q, want /images", seenPath)
	}
	if body["model"] != ModelORVector {
		t.Errorf("model = %v, want %s", body["model"], ModelORVector)
	}
	if n, ok := body["n"].(float64); !ok || n != 1 {
		t.Errorf("n = %v, want 1 (n means n variants of one prompt, each billed)", body["n"])
	}
	refs, _ := body["input_references"].([]any)
	if len(refs) != 1 || refURL(refs[0]) != "https://media.grbpwr.com/flat.png" {
		t.Errorf("input_references = %v, want our own media url crossing as a url", body["input_references"])
	}
	// This endpoint has no negative-prompt field, so the instruction is SAID OUT LOUD in the prompt
	// rather than dropped on the floor.
	if p, _ := body["prompt"].(string); !strings.Contains(p, "Avoid: photorealism, shadows") {
		t.Errorf("prompt = %q, want the negative prompt folded in", p)
	}

	if string(res.SVG) != sampleSVG || res.Route != RouteOpenRouter {
		t.Errorf("result route/bytes wrong: %q, %d bytes", res.Route, len(res.SVG))
	}
	if math.Abs(res.CostUSD-0.08) > 1e-9 {
		t.Errorf("cost = %v, want the provider's own 0.08", res.CostUSD)
	}
	if res.Stats.CubicSegments != 2 {
		t.Errorf("stats not measured: %+v", res.Stats)
	}
}

func TestOpenRouter_BytesBecomeADataURI(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		fmt.Fprintf(w, `{"data":[{"b64_json":%q}],"usage":{"cost":0.08}}`,
			base64.StdEncoding.EncodeToString([]byte(sampleSVG)))
	}))
	defer srv.Close()

	req := redrawRequest()
	req.Image = ImageInput{Bytes: []byte("PRETEND-PNG"), ContentType: "image/png"}
	if _, err := orService(t, srv.URL).ImageToImage(context.Background(), req); err != nil {
		t.Fatalf("ImageToImage: %v", err)
	}
	refs, _ := body["input_references"].([]any)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("PRETEND-PNG"))
	if len(refs) != 1 || refURL(refs[0]) != want {
		t.Fatalf("input_references = %v, want a data uri", body["input_references"])
	}
}

// TestOpenRouter_StrengthIsRefusedNotIgnored: this route has no `strength` dial. Serving the call
// anyway would make the knob look broken instead of absent — and "the redraw wandered too far from
// the flat" is the exact complaint that dial answers.
func TestOpenRouter_StrengthIsRefusedNotIgnored(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	s := 0.3
	req := redrawRequest()
	req.Strength = &s
	_, err := orService(t, srv.URL).ImageToImage(context.Background(), req)
	if !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
	if !strings.Contains(err.Error(), "RECRAFT_ROUTE=direct") {
		t.Errorf("the refusal must name the way to get the dial, got %q", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("a refused request must not be paid for; %d calls made", got)
	}
}

// TestOpenRouter_BilledFailureKeepsTheCharge is «деньги списаны, картинок нет». The run fails and
// publishes nothing, but the money is real and must reach the ledger — otherwise the daily budget
// silently stops matching the invoice.
func TestOpenRouter_BilledFailureKeepsTheCharge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[],"usage":{"cost":0.08}}`)
	}))
	defer srv.Close()

	_, err := orService(t, srv.URL).ImageToImage(context.Background(), redrawRequest())
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("err = %v, want ErrInvalidResponse", err)
	}
	cost, _, ok := Charge(err)
	if !ok || math.Abs(cost-0.08) > 1e-9 {
		t.Fatalf("Charge(err) = %v, %v — the spend must survive the failure", cost, ok)
	}

	// An UNBILLED failure must not read as a billed one: ok is false, which is not the same as $0.
	if _, _, ok := Charge(fmt.Errorf("%w: nothing reached the provider", ErrProviderFailure)); ok {
		t.Error("an unbilled failure must not report a charge")
	}
}

func TestOpenRouter_ErrorTranslation(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		// The vector slug is gone or was never in the IMAGE catalogue: read RECRAFT_MODEL_*.
		{http.StatusNotFound, ErrModelUnavailable},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusBadGateway, ErrProviderFailure},
		// THIS LINE USED TO EXPECT ErrProviderFailure, and the comment beside it explained that the
		// shared client could not tell a rejected key from weather. That stopped being true in the
		// same wave — orimages classifies 401/403 and 402 now — but the test kept demanding the old
		// answer, so the package was green while every vector run against a revoked key burned four
		// retries and was filed as «the provider is unavailable».
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrUnauthorized},
		{http.StatusPaymentRequired, ErrInsufficientCredits},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, `{"error":{"message":"provider said no"}}`)
			}))
			defer srv.Close()
			_, err := orService(t, srv.URL).ImageToImage(context.Background(), redrawRequest())
			if !errors.Is(err, tc.want) {
				t.Fatalf("HTTP %d gave %v, want %v", tc.status, err, tc.want)
			}
		})
	}
}

// TestOpenRouter_RasterFromAVectorSlugIsRefused: the endpoint serves every picture model, so a
// mistyped RECRAFT_MODEL_VECTOR answers 200 with a PNG. Storing it would leave the band showing a
// "vector" that is a bitmap — the requirement defeated silently, which is the only way it could be.
func TestOpenRouter_RasterFromAVectorSlugIsRefused(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"b64_json":%q,"media_type":"image/png"}],"usage":{"cost":0.04}}`,
			base64.StdEncoding.EncodeToString(png))
	}))
	defer srv.Close()

	_, err := orService(t, srv.URL).ImageToImage(context.Background(), redrawRequest())
	if !errors.Is(err, ErrNotVector) {
		t.Fatalf("err = %v, want ErrNotVector", err)
	}
}

func TestOpenRouter_NotConfigured(t *testing.T) {
	gen := NewOpenRouterGenerator(orimages.New(orimages.Config{})) // no key
	c := NewWithGenerator(RouteOpenRouter, gen, map[Tier]string{TierVector: ModelORVector})
	if c.Enabled() {
		t.Fatal("a keyless OpenRouter client must read as disabled")
	}
	if _, err := c.ImageToImage(context.Background(), redrawRequest()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}

	nilGen := NewOpenRouterGenerator(nil)
	if nilGen.(interface{ Enabled() bool }).Enabled() {
		t.Fatal("a nil shared client must read as disabled, not panic")
	}
}
