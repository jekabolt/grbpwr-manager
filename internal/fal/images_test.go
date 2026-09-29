package fal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/pricing"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// ═══ H3 — fal AS AN image.generate TRANSPORT ═══

// onePixelPNG — a real one-pixel PNG: the transport sniffs what it downloads.
var onePixelPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYPj/HwADAgH/pZzT0QAAAABJRU5ErkJggg==")

// imageQueue plays fal's queue for one image request: the submit (submitCode / submitBody), the status
// (status, repeated), the result envelope (result, "%s" = the file url; resultCode ≠ 0 refuses it) and
// the file. It records every path, the submit body and the Authorization of every request.
type imageQueue struct {
	submitCode int
	submitBody string
	status     string
	resultCode int
	result     string
	units      string
	file       []byte

	mu     sync.Mutex
	paths  []string
	auths  []string
	submit map[string]any
}

func newImageQueue(t *testing.T, q *imageQueue) (*httptest.Server, *imageQueue) {
	t.Helper()
	if q.status == "" {
		q.status = "COMPLETED"
	}
	if q.file == nil {
		q.file = onePixelPNG
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q.mu.Lock()
		q.paths = append(q.paths, r.Method+" "+r.URL.Path)
		q.auths = append(q.auths, r.Header.Get("Authorization"))
		q.mu.Unlock()
		switch {
		case r.URL.Path == "/file.png":
			_, _ = w.Write(q.file)
		case strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": q.status})
		case strings.Contains(r.URL.Path, "/requests/"):
			if q.units != "" {
				w.Header().Set(billableUnitsHeader, q.units)
			}
			if q.resultCode != 0 {
				w.WriteHeader(q.resultCode)
				_, _ = io.WriteString(w, `{"detail":"boom"}`)
				return
			}
			_, _ = io.WriteString(w, strings.ReplaceAll(q.result, "%s", "http://"+r.Host+"/file.png"))
		case r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			q.mu.Lock()
			q.submit = body
			q.mu.Unlock()
			if q.submitCode != 0 {
				w.WriteHeader(q.submitCode)
				_, _ = io.WriteString(w, q.submitBody)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "img-1"})
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, q
}

func (q *imageQueue) recorded() ([]string, []string, map[string]any) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.paths...), append([]string(nil), q.auths...), q.submit
}

const imagesJSON = `{"images":[{"url":"%s","content_type":"image/png","width":1,"height":1}],"seed":3}`

// unpricedLookup / pricedLookup — the catalogue seam: no row, or a per-call row at usd for slug.
func unpricedLookup(string, string) (pricing.Model, bool) { return pricing.Model{}, false }

func pricedLookup(slug, usd string) func(string, string) (pricing.Model, bool) {
	return func(provider, s string) (pricing.Model, bool) {
		if provider != entity.AIProviderFal || s != slug {
			return pricing.Model{}, false
		}
		return pricing.Model{Provider: provider, Slug: s, Kind: pricing.KindImage,
			PerCallUSD: decimal.NullDecimal{Decimal: decimal.RequireFromString(usd), Valid: true}}, true
	}
}

func testImages(base string, lookup func(string, string) (pricing.Model, bool)) *Images {
	t := NewImages(newGenericClient(base, Config{}))
	t.lookup = lookup
	return t
}

func captureImagesLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestFalImagesTEXT_TO_IMAGE — a run naming no model draws FAL_MODEL_IMAGE's default: the body is the
// prompt, num_images 1, the preset size and the format — the design route's quality / background dials
// are NOT sent (no shared fal counterpart) — the picture is downloaded without the key, and an
// unpriced slug books Cost 0 with no source.
//
// MUTATION (measured red→green): Quality passed through as "quality" → the JSONEq red.
func TestFalImagesTEXT_TO_IMAGE(t *testing.T) {
	srv, q := newImageQueue(t, &imageQueue{result: imagesJSON, units: "1"})
	tr := testImages(srv.URL, unpricedLookup)
	require.True(t, tr.Enabled())
	require.Equal(t, DefaultModelImage, tr.Model())

	res, err := tr.Generate(context.Background(), orimages.Request{Prompt: "a shirt", AspectRatio: "16:9",
		OutputFormat: "png", Quality: "high", Background: "opaque", N: 1})
	require.NoError(t, err)
	paths, auths, body := q.recorded()
	require.Equal(t, "POST /fal-ai/flux-pro/v1.1", paths[0])
	require.JSONEq(t, `{"prompt":"a shirt","num_images":1,"image_size":"landscape_16_9","output_format":"png"}`, mustJSON(t, body))
	require.Contains(t, paths, "GET /fal-ai/flux-pro/requests/img-1")
	require.Equal(t, "GET /file.png", paths[len(paths)-1])
	require.Empty(t, auths[len(auths)-1], "no key travels to the picture's host")
	require.Equal(t, "Key test-key-not-a-real-one", auths[0])

	require.Equal(t, DefaultModelImage, res.Model)
	require.Len(t, res.Images, 1)
	require.Equal(t, onePixelPNG, res.Images[0].Bytes)
	require.Equal(t, "image/png", res.Images[0].MediaType)
	require.Zero(t, res.Usage.Cost, "no catalogue row: unpriced, never a guess")
	require.Empty(t, res.Usage.CostSource)
}

// TestFalImagesEDIT_SLUG_SENDS_image_urls — an edit endpoint takes the list, every reference in order.
func TestFalImagesEDIT_SLUG_SENDS_image_urls(t *testing.T) {
	srv, q := newImageQueue(t, &imageQueue{result: imagesJSON})
	tr := testImages(srv.URL, unpricedLookup)
	_, err := tr.Generate(context.Background(), orimages.Request{Model: "fal-ai/nano-banana/edit", Prompt: "recolour",
		InputReferences: []string{"https://cdn.example/a.png", "data:image/png;base64,AAAA"}})
	require.NoError(t, err)
	paths, _, body := q.recorded()
	require.Equal(t, "POST /fal-ai/nano-banana/edit", paths[0])
	require.Equal(t, []any{"https://cdn.example/a.png", "data:image/png;base64,AAAA"}, body["image_urls"])
	require.NotContains(t, body, "image_url")
}

// TestFalImagesSINGLE_IMAGE_SLUG_SENDS_THE_FIRST_AND_WARNS — a non-edit endpoint takes `image_url`: the
// first reference goes, the rest are named in a warning rather than dropped in silence.
func TestFalImagesSINGLE_IMAGE_SLUG_SENDS_THE_FIRST_AND_WARNS(t *testing.T) {
	logs := captureImagesLog(t)
	srv, q := newImageQueue(t, &imageQueue{result: imagesJSON})
	tr := testImages(srv.URL, unpricedLookup)
	_, err := tr.Generate(context.Background(), orimages.Request{Model: "fal-ai/flux/dev/image-to-image", Prompt: "p",
		InputReferences: []string{"https://cdn.example/a.png", "https://cdn.example/b.png"}})
	require.NoError(t, err)
	_, _, body := q.recorded()
	require.Equal(t, "https://cdn.example/a.png", body["image_url"])
	require.NotContains(t, body, "image_urls")
	require.Contains(t, logs.String(), "takes one reference picture")
	require.Contains(t, logs.String(), "given=2")
}

// TestFalImagesASPECT_MAPPING — the five ratios with a preset, "auto" / "" omitted, an unknown ratio
// omitted and said once.
func TestFalImagesASPECT_MAPPING(t *testing.T) {
	logs := captureImagesLog(t)
	tr := NewImages(nil)
	for ar, want := range map[string]string{"1:1": "square_hd", "3:4": "portrait_4_3", "4:3": "landscape_4_3",
		"9:16": "portrait_16_9", "16:9": "landscape_16_9", "auto": "", "": "", "3:2": "", "21:9": ""} {
		body := tr.imageBody(context.Background(), "fal-ai/flux-pro/v1.1", "p", orimages.Request{AspectRatio: ar}, nil)
		if want == "" {
			require.NotContains(t, body, "image_size", ar)
			continue
		}
		require.Equal(t, want, body["image_size"], ar)
	}
	tr.imageBody(context.Background(), "fal-ai/flux-pro/v1.1", "p", orimages.Request{AspectRatio: "3:2"}, nil)
	require.Equal(t, 1, strings.Count(logs.String(), "aspect_ratio=3:2"), "an unknown ratio is said once")
	body := tr.imageBody(context.Background(), "x/y", "p", orimages.Request{OutputFormat: "svg"}, nil)
	require.NotContains(t, body, "output_format", "a format fal's rasters do not take is not sent")
}

// TestFalImagesPICKS_images_0_AND_image — both answer shapes deliver the picture; neither is a paid,
// empty answer: engaged, terminal, empty_answer.
func TestFalImagesPICKS_images_0_AND_image(t *testing.T) {
	for name, result := range map[string]string{
		"images[0]": imagesJSON,
		"image":     `{"image":{"url":"%s","content_type":"image/png"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := newImageQueue(t, &imageQueue{result: result})
			res, err := testImages(srv.URL, unpricedLookup).Generate(context.Background(), orimages.Request{Model: "fal-ai/bria/text-to-image/3.2", Prompt: "p"})
			require.NoError(t, err)
			require.Equal(t, onePixelPNG, res.Images[0].Bytes)
		})
	}
	t.Run("neither", func(t *testing.T) {
		srv, q := newImageQueue(t, &imageQueue{result: `{"images":[],"seed":1}`})
		res, err := testImages(srv.URL, unpricedLookup).Generate(context.Background(), orimages.Request{Prompt: "p"})
		require.Nil(t, res)
		ce, ok := aiprov.AsCallError(err)
		require.True(t, ok)
		require.True(t, ce.Engaged, "the request completed: bought")
		require.False(t, ce.Retryable)
		require.Equal(t, aiprov.CodeEmptyAnswer, ce.Code)
		require.Contains(t, err.Error(), "request img-1")
		paths, _, _ := q.recorded()
		require.NotContains(t, paths, "GET /file.png")
	})
}

// TestFalImagesCOST_IS_UNITS_TIMES_THE_TABLE — a catalogue row priced per call: Cost = units × price,
// marked a table number (the ledger books cost_source table, never provider). With no row it stays 0.
//
// MUTATION (measured red→green): CostSource left "" → the ledger would book fal's «own» dollar figure.
func TestFalImagesCOST_IS_UNITS_TIMES_THE_TABLE(t *testing.T) {
	srv, _ := newImageQueue(t, &imageQueue{result: imagesJSON, units: "2"})
	res, err := testImages(srv.URL, pricedLookup("fal-ai/flux-pro/v1.1", "0.04")).
		Generate(context.Background(), orimages.Request{Prompt: "p"})
	require.NoError(t, err)
	require.InDelta(t, 0.08, res.Usage.Cost, 1e-9)
	require.Equal(t, entity.AICostTable, res.Usage.CostSource)

	res, err = testImages(srv.URL, pricedLookup("fal-ai/other/model", "0.04")).
		Generate(context.Background(), orimages.Request{Prompt: "p"})
	require.NoError(t, err)
	require.Zero(t, res.Usage.Cost)
	require.Empty(t, res.Usage.CostSource)
}

// TestFalImagesA404AtSubmitIS_FREE — an unknown-but-well-formed endpoint id: fal's 404 at the gate is a
// free, terminal CallError (the chain advances; nothing was bought), and no poll follows.
func TestFalImagesA404AtSubmitIS_FREE(t *testing.T) {
	srv, q := newImageQueue(t, &imageQueue{submitCode: http.StatusNotFound, submitBody: `{"detail":"Application not found"}`})
	res, err := testImages(srv.URL, unpricedLookup).Generate(context.Background(), orimages.Request{Model: "acme/no-such-model", Prompt: "p"})
	require.Nil(t, res)
	ce, ok := aiprov.AsCallError(err)
	require.True(t, ok)
	require.False(t, ce.Engaged)
	require.False(t, ce.Retryable)
	require.Equal(t, http.StatusNotFound, ce.HTTPStatus)
	require.Equal(t, aiprov.CodeModelUnknown, ce.Code)
	require.Equal(t, entity.AIProviderFal, ce.Provider)
	paths, _, _ := q.recorded()
	require.Equal(t, []string{"POST /acme/no-such-model"}, paths)
}

// TestFalImagesAFTER_THE_SUBMIT_IS_BOUGHT — a wait that runs out and a result fal refuses to serve are
// both ENGAGED and terminal (the status read's own «free, retryable» must not leak out: the request id
// was accepted), naming the request id.
//
// MUTATION (measured red→green): Generate returning CollectFile's error as-is → the 500 on the result
// fetch reads free + retryable → the chain would buy the picture twice.
func TestFalImagesAFTER_THE_SUBMIT_IS_BOUGHT(t *testing.T) {
	t.Run("the ceiling", func(t *testing.T) {
		srv, _ := newImageQueue(t, &imageQueue{status: "IN_PROGRESS"})
		c := newGenericClient(srv.URL, Config{})
		c.cfg.PollTimeout = 50 * time.Millisecond
		tr := NewImages(c)
		tr.lookup = unpricedLookup
		_, err := tr.Generate(context.Background(), orimages.Request{Prompt: "p"})
		ce, ok := aiprov.AsCallError(err)
		require.True(t, ok)
		require.True(t, ce.Engaged)
		require.False(t, ce.Retryable)
		require.Equal(t, aiprov.CodeTimeout, ce.Code)
		require.ErrorIs(t, err, ErrTimedOut)
		require.Contains(t, err.Error(), "request img-1 (fal-ai/flux-pro/v1.1) is bought")
	})
	t.Run("the result refused", func(t *testing.T) {
		srv, _ := newImageQueue(t, &imageQueue{resultCode: http.StatusInternalServerError})
		_, err := testImages(srv.URL, unpricedLookup).Generate(context.Background(), orimages.Request{Prompt: "p"})
		ce, ok := aiprov.AsCallError(err)
		require.True(t, ok)
		require.True(t, ce.Engaged)
		require.False(t, ce.Retryable)
		require.True(t, aiprov.Engaged(err))
		require.Equal(t, http.StatusInternalServerError, ce.HTTPStatus)
	})
}

// TestFalImagesREFUSES_BEFORE_THE_WIRE — no key, an unfetchable reference, an empty prompt: free,
// nothing sent.
func TestFalImagesREFUSES_BEFORE_THE_WIRE(t *testing.T) {
	srv, q := newImageQueue(t, &imageQueue{result: imagesJSON})
	_, err := NewImages(New(Config{BaseURL: srv.URL})).Generate(context.Background(), orimages.Request{Prompt: "p"})
	ce, ok := aiprov.AsCallError(err)
	require.True(t, ok)
	require.Equal(t, aiprov.CodeNotConfigured, ce.Code)
	require.False(t, ce.Engaged)

	tr := testImages(srv.URL, unpricedLookup)
	for _, req := range []orimages.Request{
		{Prompt: "p", InputReferences: []string{"file:///etc/passwd"}},
		{Prompt: "  "},
		{Model: "../x", Prompt: "p"},
	} {
		_, err := tr.Generate(context.Background(), req)
		ce, ok := aiprov.AsCallError(err)
		require.True(t, ok, "%+v", req)
		require.Equal(t, aiprov.CodeBadRequest, ce.Code)
		require.False(t, ce.Engaged)
	}
	paths, _, _ := q.recorded()
	require.Empty(t, paths)
}

// TestFalImagesSERVES_ANY_WELL_FORMED_ENDPOINT — Serves is the path rule, not a whitelist.
func TestFalImagesSERVES_ANY_WELL_FORMED_ENDPOINT(t *testing.T) {
	tr := NewImages(newGenericClient("http://unused", Config{}))
	for _, ok := range []string{"fal-ai/flux-pro/v1.1", "fal-ai/nano-banana/edit", " /fal-ai/gpt-image-2/ ", "acme/model-v1.5_x"} {
		require.True(t, tr.Serves(ok), ok)
	}
	for _, bad := range []string{"../x", "fal-ai/../x", "", "acme", "Acme/Model", "openai/gpt-image-2?x=1", "acme//model"} {
		require.False(t, tr.Serves(bad), bad)
	}
	var nilT *Images
	require.False(t, nilT.Serves("fal-ai/flux-pro/v1.1"))
	require.False(t, nilT.Enabled())
	require.Equal(t, "acme/pic/v2", NewImages(New(Config{ModelImage: " /acme/pic/v2/ "})).Model(), "FAL_MODEL_IMAGE")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}
