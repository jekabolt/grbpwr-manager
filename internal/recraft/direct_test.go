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
)

// directService wires the FALLBACK route at a test server, exercising the same path a deployment
// takes when RECRAFT_ROUTE=direct.
func directService(t *testing.T, srvURL string) *Client {
	t.Helper()
	return New(Config{
		Route:  "direct",
		Direct: DirectConfig{APIKey: "test-key", BaseURL: srvURL},
	}, nil)
}

func redrawRequest() ImageToImageRequest {
	return ImageToImageRequest{
		Prompt: "technical flat of an oversized hoodie, clean line art",
		Image:  ImageInput{URL: "https://media.grbpwr.com/flat.png"},
	}
}

func TestDirect_Success_InlineSVG(t *testing.T) {
	var seenPath, seenMethod, seenAuth, seenContentType string
	var body map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath, seenMethod = r.URL.Path, r.Method
		seenAuth = r.Header.Get("Authorization")
		seenContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"b64_json":%q,"image_id":"abc"}],"credits":80}`,
			base64.StdEncoding.EncodeToString([]byte(sampleSVG)))
	}))
	defer srv.Close()

	res, err := directService(t, srv.URL).ImageToImage(context.Background(), redrawRequest())
	if err != nil {
		t.Fatalf("ImageToImage: %v", err)
	}

	// THE ENDPOINT ASSERTION. This is the requirement, at the wire: the call must land on
	// imageToImage. Its sibling — the raster tracer — is what produces the many-node soup the owner
	// forbade, and a wire-level check is the only kind that cannot be argued with.
	if seenPath != "/images/imageToImage" {
		t.Fatalf("called %q, want /images/imageToImage", seenPath)
	}
	if seenMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", seenMethod)
	}
	if seenAuth != "Bearer test-key" {
		t.Errorf("auth header = %q", seenAuth)
	}
	if seenContentType != "application/json" {
		t.Errorf("content type = %q, want application/json for the url path", seenContentType)
	}
	if body["image_url"] != "https://media.grbpwr.com/flat.png" {
		t.Errorf("image_url = %v", body["image_url"])
	}
	if body["model"] != ModelDirectVector {
		t.Errorf("model = %v, want %s", body["model"], ModelDirectVector)
	}
	// strength is DIFFERENCE, not similarity: a low default keeps the approved garment.
	if s, ok := body["strength"].(float64); !ok || math.Abs(s-defaultStrength) > 1e-9 {
		t.Errorf("strength = %v, want %v", body["strength"], defaultStrength)
	}
	if n, ok := body["n"].(float64); !ok || n != 1 {
		t.Errorf("n = %v, want 1 (n>1 multiplies the price of one press)", body["n"])
	}
	if body["response_format"] != "b64_json" {
		t.Errorf("response_format = %v, want b64_json (inline delivery cannot expire)", body["response_format"])
	}

	if string(res.SVG) != sampleSVG {
		t.Error("the SVG must arrive byte-for-byte")
	}
	if res.Route != RouteDirect || res.Model != ModelDirectVector {
		t.Errorf("route/model = %q/%q", res.Route, res.Model)
	}
	if res.Credits != 80 {
		t.Errorf("credits = %v, want the raw 80 reported by the provider", res.Credits)
	}
	if math.Abs(res.CostUSD-0.08) > 1e-9 {
		t.Errorf("cost = %v, want 0.08 (80 units at $0.001)", res.CostUSD)
	}
	if res.SourceURL != "" {
		t.Errorf("an inlined picture has no source link, got %q", res.SourceURL)
	}
}

func TestDirect_Success_MultipartWhenBytesAreSupplied(t *testing.T) {
	var seenFile, seenPrompt, seenModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("expected a multipart body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f, _, err := r.FormFile("image")
		if err != nil {
			t.Errorf("no image part: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		raw, _ := io.ReadAll(f)
		seenFile = string(raw)
		seenPrompt, seenModel = r.FormValue("prompt"), r.FormValue("model")
		fmt.Fprintf(w, `{"data":[{"b64_json":%q}],"credits":80}`,
			base64.StdEncoding.EncodeToString([]byte(sampleSVG)))
	}))
	defer srv.Close()

	req := redrawRequest()
	req.Image = ImageInput{Bytes: []byte("PRETEND-PNG"), Filename: "flat.png", ContentType: "image/png"}
	if _, err := directService(t, srv.URL).ImageToImage(context.Background(), req); err != nil {
		t.Fatalf("ImageToImage: %v", err)
	}
	if seenFile != "PRETEND-PNG" {
		t.Errorf("uploaded bytes = %q", seenFile)
	}
	if seenModel != ModelDirectVector || seenPrompt == "" {
		t.Errorf("form fields lost: model=%q prompt=%q", seenModel, seenPrompt)
	}
}

func TestDirect_DownloadsAndRetriesTheFreeGET(t *testing.T) {
	var imageCalls int32
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/images/imageToImage", func(w http.ResponseWriter, r *http.Request) {
		// A provider that ignores response_format and hands back a link instead.
		fmt.Fprintf(w, `{"data":[{"url":%q}],"credits":300}`, base+"/out.svg")
	})
	mux.HandleFunc("/out.svg", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&imageCalls, 1) == 1 {
			// A hiccup on a picture we have ALREADY PAID FOR. Giving up here throws the money away,
			// and a repeat costs nothing: this GET is free and idempotent.
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", SVGContentType)
		io.WriteString(w, sampleSVG)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	res, err := directService(t, srv.URL).ImageToImage(context.Background(), redrawRequest())
	if err != nil {
		t.Fatalf("ImageToImage: %v", err)
	}
	if got := atomic.LoadInt32(&imageCalls); got != 2 {
		t.Fatalf("image fetched %d times, want 2 (one failure, one retry)", got)
	}
	if string(res.SVG) != sampleSVG {
		t.Error("the downloaded SVG must arrive intact")
	}
	if res.SourceURL != srv.URL+"/out.svg" {
		t.Errorf("source link = %q, want the provider url recorded for the log", res.SourceURL)
	}
	if math.Abs(res.CostUSD-0.30) > 1e-9 {
		t.Errorf("cost = %v, want 0.30 (300 units)", res.CostUSD)
	}
}

// TestDirect_PaidCallIsNeverRetried is the money guard. The provider does not promise idempotency on
// this route, so a retry hidden in the HTTP client could render and bill a second image OUTSIDE the
// attempt ledger, where nobody would ever see it. Retrying is the worker's decision, capped at two.
func TestDirect_PaidCallIsNeverRetried(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"message":"upstream exploded"}`)
	}))
	defer srv.Close()

	_, err := directService(t, srv.URL).ImageToImage(context.Background(), redrawRequest())
	if !errors.Is(err, ErrProviderFailure) {
		t.Fatalf("err = %v, want ErrProviderFailure (we do not know whether it was billed)", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("the paid endpoint was called %d times, want exactly 1", got)
	}
	if !strings.Contains(err.Error(), "upstream exploded") {
		t.Errorf("the provider's own sentence must survive into the message, got %q", err)
	}
}

func TestDirect_StatusClassification(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrUnauthorized},
		{http.StatusPaymentRequired, ErrInsufficientCredits},
		{http.StatusTooManyRequests, ErrRateLimited},
		// 404 means the model id or the route is not served. Classified BY STATUS ALONE, so a
		// reworded provider sentence cannot silently turn a dead slug into "weather".
		{http.StatusNotFound, ErrModelUnavailable},
		{http.StatusBadRequest, ErrBadRequest},
		{http.StatusUnprocessableEntity, ErrBadRequest},
		{http.StatusServiceUnavailable, ErrProviderFailure},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, `{"code":"provider_said_no","message":"nope"}`)
			}))
			defer srv.Close()
			_, err := directService(t, srv.URL).ImageToImage(context.Background(), redrawRequest())
			if !errors.Is(err, tc.want) {
				t.Fatalf("HTTP %d gave %v, want %v", tc.status, err, tc.want)
			}
		})
	}
}

func TestDirect_MalformedProviderBodies(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"not json", `<html>502 Bad Gateway</html>`, ErrInvalidResponse},
		{"no images", `{"data":[],"credits":80}`, ErrInvalidResponse},
		{"neither bytes nor link", `{"data":[{"image_id":"abc"}],"credits":80}`, ErrInvalidResponse},
		{"undecodable base64", `{"data":[{"b64_json":"!!!not base64!!!"}]}`, ErrInvalidResponse},
		{"a link that is not http", `{"data":[{"url":"file:///etc/passwd"}]}`, ErrInvalidResponse},
		// A raster arriving from a model configured under a vector name.
		{"a PNG in the envelope", `{"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUg=="}]}`, ErrNotVector},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := directService(t, srv.URL).ImageToImage(context.Background(), redrawRequest())
			if !errors.Is(err, tc.want) {
				t.Fatalf("body %s gave %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestDirect_OversizedResponseIsLoud pins the lesson from the text client: a silent read cap turns
// "the picture was too big" into an unexplained parse error and sends the reader hunting for a bug
// in the JSON.
func TestDirect_OversizedResponseIsLoud(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"b64_json":"`)
		chunk := strings.Repeat("A", 1<<20)
		for written := 0; written <= maxResponseBytes; written += len(chunk) {
			io.WriteString(w, chunk)
		}
		io.WriteString(w, `"}]}`)
	}))
	defer srv.Close()

	_, err := directService(t, srv.URL).ImageToImage(context.Background(), redrawRequest())
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("err = %v, want ErrInvalidResponse", err)
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("the message must say the response was too big, got %q", err)
	}
}

func TestDirect_NoKeyIsRefusedBeforeAnyRequest(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	c := New(Config{Route: "direct", Direct: DirectConfig{BaseURL: srv.URL}}, nil)
	if c.Enabled() {
		t.Fatal("a keyless direct route must read as disabled")
	}
	_, err := c.ImageToImage(context.Background(), redrawRequest())
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("the provider was contacted %d times without a key", got)
	}
}
