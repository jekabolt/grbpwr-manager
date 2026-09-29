package oaiimages

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// pngB64 — a real one-pixel PNG, so the sniffer calls what comes back a picture.
const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYPj/HwADAgH/pZzT0QAAAABJRU5ErkJggg=="

const testKey = "sk-test-KEY-0123456789"

func pngBytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(pngB64)
	require.NoError(t, err)
	return b
}

// stand is a fake OpenAI-shaped images API plus a CDN: it records every request and answers the API
// POSTs with (status, body) and GET /cdn/out.png with a PNG.
type stand struct {
	status int
	body   string

	mu    sync.Mutex
	reqs  []recorded
	srv   *httptest.Server
	delay time.Duration
}

type recorded struct {
	method, path, contentType, auth string
	body                            []byte
}

func newStand(t *testing.T) *stand {
	t.Helper()
	s := &stand{status: http.StatusOK, body: `{"data":[{"b64_json":"` + pngB64 + `"}]}`}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recorded{r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), b})
		s.mu.Unlock()
		if r.Method == http.MethodGet && r.URL.Path == "/cdn/out.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes(t))
			return
		}
		if s.delay > 0 {
			select {
			case <-time.After(s.delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stand) client(provider string) *Client {
	return New(Config{
		Provider: provider, BaseURL: s.srv.URL + "/v1", KeyFunc: func() string { return testKey },
		HTTP: s.srv.Client(), DefaultSlug: "gpt-image-2",
	})
}

func (s *stand) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.reqs...)
}

// only is the one API request the call made.
func (s *stand) only(t *testing.T) recorded {
	t.Helper()
	rs := s.requests()
	require.Len(t, rs, 1)
	return rs[0]
}

func jsonBody(t *testing.T, r recorded) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(r.body, &m))
	return m
}

func callErr(t *testing.T, err error) *aiprov.CallError {
	t.Helper()
	ce, ok := aiprov.AsCallError(err)
	require.True(t, ok, "want a CallError, got %v", err)
	return ce
}

func TestGenerations_gptImage2_bodyHasNoResponseFormat_qualityPassthrough_sizeMap(t *testing.T) {
	s := newStand(t)
	res, err := s.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{
		Model: "openai/gpt-image-2", Prompt: " a coat ", Quality: "medium", AspectRatio: "16:9",
		Background: "opaque", OutputFormat: "png", N: 1,
	})
	require.NoError(t, err)
	r := s.only(t)
	require.Equal(t, http.MethodPost, r.method)
	require.Equal(t, "/v1/images/generations", r.path)
	require.Equal(t, "application/json", r.contentType)
	require.Equal(t, "Bearer "+testKey, r.auth)
	require.Equal(t, map[string]any{
		"model": "gpt-image-2", "prompt": "a coat", "n": float64(1), "size": "1536x1024",
		"quality": "medium", "background": "opaque", "output_format": "png",
	}, jsonBody(t, r))
	require.Equal(t, "gpt-image-2", res.Model)
	require.Len(t, res.Images, 1)
	require.Equal(t, "image/png", res.Images[0].MediaType)
	require.Zero(t, res.Usage.Cost, "neither provider states a price; the ledger prices by table")

	for aspect, want := range map[string]any{"1:1": "1024x1024", "3:4": "1024x1536", "auto": nil, "": nil, "21:9": nil} {
		s2 := newStand(t)
		_, err := s2.client(entity.AIProviderOpenAI).Generate(context.Background(),
			orimages.Request{Prompt: "p", AspectRatio: aspect})
		require.NoError(t, err)
		require.Equal(t, want, jsonBody(t, s2.only(t))["size"], "aspect %q", aspect)
	}
}

func TestGenerations_dallE3_responseFormatB64_qualityHD_size1792(t *testing.T) {
	s := newStand(t)
	_, err := s.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{
		Model: "dall-e-3", Prompt: "a coat", Quality: "high", AspectRatio: "3:2",
		Background: "opaque", OutputFormat: "png",
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"model": "dall-e-3", "prompt": "a coat", "n": float64(1), "size": "1792x1024",
		"quality": "hd", "response_format": "b64_json",
	}, jsonBody(t, s.only(t)))

	s2 := newStand(t)
	_, err = s2.client(entity.AIProviderOpenAI).Generate(context.Background(),
		orimages.Request{Model: "dall-e-3", Prompt: "p", Quality: "medium", AspectRatio: "9:16"})
	require.NoError(t, err)
	b := jsonBody(t, s2.only(t))
	require.NotContains(t, b, "quality", "dall-e-3 takes standard|hd: anything but high is left to its default")
	require.Equal(t, "1024x1792", b["size"])
}

func TestGenerations_apibostRelaySlug_minimalBody(t *testing.T) {
	s := newStand(t)
	_, err := s.client(entity.AIProviderApibost).Generate(context.Background(), orimages.Request{
		Model: "flux-kontext-pro", Prompt: "a coat", Quality: "high", AspectRatio: "1:1",
		Background: "opaque", OutputFormat: "png",
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"model": "flux-kontext-pro", "prompt": "a coat", "n": float64(1), "size": "1024x1024",
	}, jsonBody(t, s.only(t)))
}

func TestGenerations_urlAnswerIsDownloadedWithoutTheKey(t *testing.T) {
	s := newStand(t)
	s.body = `{"data":[{"url":"` + s.srv.URL + `/cdn/out.png"}]}`
	res, err := s.client(entity.AIProviderApibost).Generate(context.Background(),
		orimages.Request{Model: "dall-e-3", Prompt: "p"})
	require.NoError(t, err)
	rs := s.requests()
	require.Len(t, rs, 2)
	require.Equal(t, http.MethodGet, rs[1].method)
	require.Equal(t, "/cdn/out.png", rs[1].path)
	require.Empty(t, rs[1].auth, "no key travels to the CDN")
	require.Equal(t, pngBytes(t), res.Images[0].Bytes)
	require.Equal(t, "image/png", res.Images[0].MediaType)
}

func TestGenerations_urlDownloadFailureIsBought(t *testing.T) {
	s := newStand(t)
	s.body = `{"data":[{"url":"` + s.srv.URL + `/cdn/missing.png"}],"usage":{"input_tokens":3,"output_tokens":4}}`
	res, err := s.client(entity.AIProviderApibost).Generate(context.Background(),
		orimages.Request{Model: "flux-kontext-pro", Prompt: "p"})
	ce := callErr(t, err)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.NotNil(t, res, "the billed usage rides along")
	require.Equal(t, 4, res.Usage.Completion)
}

func TestGenerations_b64AnswerDecoded(t *testing.T) {
	s := newStand(t)
	res, err := s.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{Prompt: "p"})
	require.NoError(t, err)
	require.Equal(t, pngBytes(t), res.Images[0].Bytes)
	require.Equal(t, "gpt-image-2", jsonBody(t, s.only(t))["model"], "an empty Model is the DefaultSlug")
}

func TestGenerations_usageTokensCopied(t *testing.T) {
	s := newStand(t)
	s.body = `{"data":[{"b64_json":"` + pngB64 + `"}],"usage":{"input_tokens":50,"output_tokens":4160,` +
		`"total_tokens":4210,"input_tokens_details":{"image_tokens":40,"text_tokens":10}}}`
	res, err := s.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{Prompt: "p"})
	require.NoError(t, err)
	require.Equal(t, orimages.Usage{Prompt: 50, Completion: 4160, Total: 4210}, res.Usage)
}

func TestEdits_multipartWithOneDataURIReference(t *testing.T) {
	s := newStand(t)
	_, err := s.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{
		Model: "gpt-image-2", Prompt: "recolour it", Quality: "low", AspectRatio: "2:3",
		InputReferences: []string{"data:image/png;base64," + pngB64},
	})
	require.NoError(t, err)
	r := s.only(t)
	require.Equal(t, "/v1/images/edits", r.path)
	mt, params, err := mime.ParseMediaType(r.contentType)
	require.NoError(t, err)
	require.Equal(t, "multipart/form-data", mt)
	mr := multipart.NewReader(bytes.NewReader(r.body), params["boundary"])
	fields := map[string]string{}
	var files []*multipart.Part
	var fileBytes [][]byte
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		b, _ := io.ReadAll(p)
		if p.FileName() != "" {
			files, fileBytes = append(files, p), append(fileBytes, b)
			continue
		}
		fields[p.FormName()] = string(b)
	}
	require.Equal(t, map[string]string{
		"model": "gpt-image-2", "prompt": "recolour it", "n": "1", "size": "1024x1536", "quality": "low",
	}, fields)
	require.Len(t, files, 1)
	require.Equal(t, "image[]", files[0].FormName())
	require.Equal(t, "image/png", files[0].Header.Get("Content-Type"))
	require.Equal(t, pngBytes(t), fileBytes[0])
}

// fakeObjects is the bucket: one object by key.
type fakeObjects map[string][]byte

func (f fakeObjects) GetManagedObject(_ context.Context, key string) (io.ReadCloser, int64, error) {
	b, ok := f[key]
	if !ok {
		return nil, 0, errors.New("no such object")
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

func TestEdits_httpsReferenceIsReadThroughTheBucketOnly(t *testing.T) {
	s := newStand(t)
	c := s.client(entity.AIProviderApibost)
	c.cfg.Objects = fakeObjects{"2026/a.png": pngBytes(t)}
	c.cfg.KeyFromURL = func(u string) (string, error) {
		if !strings.HasPrefix(u, "https://files.grbpwr.com/") {
			return "", errors.New("foreign host")
		}
		return strings.TrimPrefix(u, "https://files.grbpwr.com/"), nil
	}
	_, err := c.Generate(context.Background(), orimages.Request{Model: "gpt-image-2", Prompt: "p",
		InputReferences: []string{"https://files.grbpwr.com/2026/a.png"}})
	require.NoError(t, err)
	require.Equal(t, "/v1/images/edits", s.only(t).path)

	for _, ref := range []string{"https://evil.example/a.png", "http://files.grbpwr.com/2026/a.png", "file:///etc/passwd"} {
		_, err := c.Generate(context.Background(), orimages.Request{Model: "gpt-image-2", Prompt: "p", InputReferences: []string{ref}})
		ce := callErr(t, err)
		require.Equal(t, aiprov.CodeBadRequest, ce.Code, ref)
		require.False(t, ce.Engaged, ref)
	}
	require.Len(t, s.requests(), 1, "a refused reference sends nothing")
}

func TestEdits_dallE3WithReferencesIsRefusedFree(t *testing.T) {
	s := newStand(t)
	_, err := s.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{
		Model: "dall-e-3", Prompt: "p", InputReferences: []string{"data:image/png;base64," + pngB64},
	})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeBadRequest, ce.Code)
	require.False(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.Equal(t, entity.AIProviderOpenAI, ce.Provider)
	require.Empty(t, s.requests())
}

func TestServes(t *testing.T) {
	openai := New(Config{Provider: entity.AIProviderOpenAI})
	apibost := New(Config{Provider: entity.AIProviderApibost})
	cases := []struct {
		slug            string
		openai, apibost bool
	}{
		{"gpt-image-2", true, true},
		{"openai/gpt-image-2", true, false},
		{"gpt-image-1", true, true},
		{"gpt-image-1-mini", true, true},
		{"gpt-image-1.5", true, true},
		{"gpt-image-2.5-sunburst", true, true},
		{"gpt-image-3", true, true},
		{"dall-e-2", true, true},
		{"dall-e-3", true, true},
		{" dall-e-3 ", true, true},
		{"gpt-image-", false, true},
		{"flux-kontext-pro", false, true},
		{"gemini-2.5-flash-image", false, true},
		{"google/gemini-2.5-flash-image", false, false},
		{"gpt-5", false, true},
		{"", false, false},
		{"gpt image", false, false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.openai, openai.Serves(tc.slug), "openai %q", tc.slug)
		require.Equal(t, tc.apibost, apibost.Serves(tc.slug), "apibost %q", tc.slug)
	}
	var nilClient *Client
	require.False(t, nilClient.Serves("gpt-image-2"))
	require.False(t, nilClient.Enabled())
}

func TestStatus401IsFreeKeyRejected_andTheKeyIsScrubbed(t *testing.T) {
	s := newStand(t)
	s.status = http.StatusUnauthorized
	s.body = `{"error":{"message":"Incorrect API key provided: ` + testKey + `"}}`
	_, err := s.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{Prompt: "p"})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeKeyRejected, ce.Code)
	require.Equal(t, http.StatusUnauthorized, ce.HTTPStatus)
	require.False(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.NotContains(t, err.Error(), testKey)
	require.Contains(t, err.Error(), "Incorrect API key provided: [key]")
}

func TestStatus5xxIsFreeRetryable(t *testing.T) {
	s := newStand(t)
	s.status = http.StatusBadGateway
	s.body = `{"error":{"message":"upstream"}}`
	_, err := s.client(entity.AIProviderApibost).Generate(context.Background(), orimages.Request{Model: "gpt-image-2", Prompt: "p"})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeProviderError, ce.Code)
	require.False(t, ce.Engaged)
	require.True(t, ce.Retryable)
	require.Equal(t, entity.AIProviderApibost, ce.Provider)
}

func TestStatus400CarriesTheProviderMessageBounded(t *testing.T) {
	s := newStand(t)
	s.status = http.StatusBadRequest
	s.body = `{"error":{"message":"` + strings.Repeat("x", 500) + `"}}`
	_, err := s.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{Prompt: "p"})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeBadRequest, ce.Code)
	require.False(t, ce.Engaged)
	require.Less(t, len(err.Error()), 300)
}

func TestMalformed200IsEngagedTerminal(t *testing.T) {
	s := newStand(t)
	s.body = `{"data":`
	res, err := s.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{Prompt: "p"})
	require.Nil(t, res)
	ce := callErr(t, err)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable)

	s2 := newStand(t)
	s2.body = `{"data":[],"usage":{"input_tokens":1,"output_tokens":2}}`
	res, err = s2.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{Prompt: "p"})
	ce = callErr(t, err)
	require.Equal(t, aiprov.CodeEmptyAnswer, ce.Code)
	require.True(t, ce.Engaged)
	require.Equal(t, 2, res.Usage.Completion)
}

func TestTimeoutAfterTheWriteIsEngaged(t *testing.T) {
	s := newStand(t)
	s.delay = 2 * time.Second
	c := s.client(entity.AIProviderOpenAI)
	c.cfg.Timeout = 100 * time.Millisecond
	_, err := c.Generate(context.Background(), orimages.Request{Prompt: "p"})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeTimeout, ce.Code)
	require.True(t, ce.Engaged)
	require.False(t, ce.Retryable)
}

func TestNoKeyIsNotConfiguredFree(t *testing.T) {
	c := New(Config{Provider: entity.AIProviderOpenAI, KeyFunc: func() string { return " " }})
	require.False(t, c.Enabled())
	_, err := c.Generate(context.Background(), orimages.Request{Prompt: "p"})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeNotConfigured, ce.Code)
	require.False(t, ce.Engaged)
}

func TestUnservedSlugIsRefusedBeforeTheWire(t *testing.T) {
	s := newStand(t)
	_, err := s.client(entity.AIProviderOpenAI).Generate(context.Background(), orimages.Request{Model: "flux-kontext-pro", Prompt: "p"})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeBadRequest, ce.Code)
	require.Empty(t, s.requests())
}
