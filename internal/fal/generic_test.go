package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// genericStub serves one generic-route lifecycle and records every path it was asked for.
type genericStub struct {
	result    string // the result body; "%s" is replaced by the file url
	units     string // x-fal-billable-units on the RESULT fetch
	fileCode  int    // non-zero: the file download answers this status
	fileBytes []byte

	mu         sync.Mutex
	paths      []string
	submitPath string
	submitRaw  []byte
}

func (s *genericStub) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		switch {
		case r.URL.Path == "/file.png":
			if s.fileCode != 0 {
				w.WriteHeader(s.fileCode)
				return
			}
			_, _ = w.Write(s.fileBytes)
		case strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
		case strings.Contains(r.URL.Path, "/requests/"):
			if s.units != "" {
				w.Header().Set(billableUnitsHeader, s.units)
			}
			_, _ = w.Write([]byte(strings.ReplaceAll(s.result, "%s", "http://"+r.Host+"/file.png")))
		case r.Method == http.MethodPost:
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			s.mu.Lock()
			s.submitPath, s.submitRaw = r.URL.Path, raw
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "gen-1"})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}
}

func newGenericClient(base string, cfg Config) *Client {
	cfg.APIKey = "test-key-not-a-real-one"
	cfg.BaseURL = base
	cfg.HTTPTimeout = 2 * time.Second
	cfg.PollInterval = 5 * time.Millisecond
	cfg.PollTimeout = time.Second
	return New(cfg)
}

// TestTheFillSlugIsSUBMITTED_WHOLE_AND_POLLED_AT_ITS_APP — a four-segment slug. fal's own client
// (fal-js libs/client/src/queue.ts) builds status/result urls from `${owner}/${alias}` only, so the
// path segments are submitted and never polled; getting this backwards is a paid request whose
// result can never be collected.
func TestTheFillSlugIsSUBMITTED_WHOLE_AND_POLLED_AT_ITS_APP(t *testing.T) {
	require.Equal(t, "fal-ai/flux-pro", queuePath(DefaultModelFill))
	require.Equal(t, "fal-ai/flux-2-pro", queuePath(DefaultModelOutpaint))
	require.Equal(t, "fal-ai/bria", queuePath("fal-ai/bria/expand"))

	stub := &genericStub{result: `{"images":[{"url":"%s","content_type":"image/png"}],"seed":7}`, units: "2",
		fileBytes: []byte("FILLED")}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()
	c := newGenericClient(srv.URL, Config{})

	body := map[string]any{"prompt": "a brass button", "image_url": "https://cdn.example/a.png",
		"mask_url": "data:image/png;base64,AAAA", "num_images": 1, "output_format": "png", "safety_tolerance": "2"}
	id, err := c.SubmitJSON(context.Background(), c.ModelFor(RouteFill), body)
	require.NoError(t, err)
	require.Equal(t, "gen-1", id)
	require.Equal(t, "/fal-ai/flux-pro/v1/fill", stub.submitPath)
	want, _ := json.Marshal(body)
	require.JSONEq(t, string(want), string(stub.submitRaw), "the body leaves byte-for-byte as built")

	var dst bytes.Buffer
	res, err := c.CollectFile(context.Background(), c.ModelFor(RouteFill), id, PickImages0, &dst, 0)
	require.NoError(t, err)
	require.Equal(t, "FILLED", dst.String())
	require.Equal(t, 2.0, res.BillableUnits)
	require.Equal(t, "image/png", res.ContentType)
	require.Equal(t, DefaultModelFill, res.Model)
	require.Contains(t, stub.paths, "GET /fal-ai/flux-pro/requests/gen-1/status")
	require.Contains(t, stub.paths, "GET /fal-ai/flux-pro/requests/gen-1")
}

// TestPickReadsBOTH_ANSWER_SHAPES — `{"image":{…}}` (birefnet, bria) and `{"images":[{…}]}` (flux
// outpaint, flux fill). A picker reading the wrong key is the costliest mistake here: the file was
// built and billed, and the url is invisible.
func TestPickReadsBOTH_ANSWER_SHAPES(t *testing.T) {
	u, ct, err := PickImage(json.RawMessage(`{"image":{"url":"https://x/a.png","content_type":"image/png"},"seed":1}`))
	require.NoError(t, err)
	require.Equal(t, "https://x/a.png", u)
	require.Equal(t, "image/png", ct)
	u, _, err = PickImages0(json.RawMessage(`{"images":[{"url":"https://x/0.png"},{"url":"https://x/1.png"}]}`))
	require.NoError(t, err)
	require.Equal(t, "https://x/0.png", u, "the FIRST file, deterministically")

	for _, tc := range []struct {
		pick FilePicker
		body string
	}{
		{PickImage, `{"images":[{"url":"https://x/0.png"}]}`},
		{PickImages0, `{"image":{"url":"https://x/a.png"}}`},
		{PickImages0, `{"images":[]}`},
		{PickImage, `{"image":{"url":"  "}}`},
	} {
		_, _, err := tc.pick(json.RawMessage(tc.body))
		require.ErrorIs(t, err, ErrNoFile, tc.body)
		require.ErrorIs(t, err, ErrNoModel, "the classifier's «paid, nothing to show» — never weather")
	}
}

// TestACompletedFileWithNoUrlOrAFailedDownloadCARRIES_THE_CHARGE — the units are read on the result
// fetch, so every failure after it is billed and must say so.
func TestACompletedFileWithNoUrlOrAFailedDownloadCARRIES_THE_CHARGE(t *testing.T) {
	stub := &genericStub{result: `{"images":[]}`, units: "3"}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()
	c := newGenericClient(srv.URL, Config{})

	_, err := c.CollectFile(context.Background(), DefaultModelOutpaint, "gen-1", PickImages0, &bytes.Buffer{}, 0)
	require.ErrorIs(t, err, ErrNoFile)
	units, ok := Charge(err)
	require.True(t, ok)
	require.Equal(t, 3.0, units)
	require.Equal(t, DefaultModelOutpaint, ChargedModel(err))

	stub.result = `{"image":{"url":"%s"}}`
	stub.fileCode = http.StatusInternalServerError
	_, err = c.CollectFile(context.Background(), "fal-ai/bria/expand", "gen-1", PickImage, &bytes.Buffer{}, 0)
	require.Error(t, err)
	units, ok = Charge(err)
	require.True(t, ok, "a download that died after the result fetch was still billed")
	require.Equal(t, 3.0, units)

	// Over the caller's cap: refused, not truncated, and billed.
	stub.fileCode = 0
	stub.fileBytes = bytes.Repeat([]byte("x"), 64)
	_, err = c.CollectFile(context.Background(), "fal-ai/bria/expand", "gen-1", PickImage, &bytes.Buffer{}, 16)
	require.ErrorIs(t, err, ErrTooLarge)
	_, ok = Charge(err)
	require.True(t, ok)
}

// TestAnInlinePictureOverTheCeilingIsREFUSED_BEFORE_ANY_REQUEST — MaxDataURIBytes is ours (fal
// documents no body limit): a data URI one byte over it never leaves the process, for free.
// Mutation: raise MaxDataURIBytes → the literal assertion goes red; drop the check → the stand sees a
// request and the test fails.
func TestAnInlinePictureOverTheCeilingIsREFUSED_BEFORE_ANY_REQUEST(t *testing.T) {
	require.Equal(t, 8<<20, MaxDataURIBytes, "the inline ceiling is a memory budget, not a round number")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a request left the process for a body that could be refused locally, for free")
	}))
	defer srv.Close()
	c := newGenericClient(srv.URL, Config{})

	prefix := "data:image/png;base64,"
	over := prefix + strings.Repeat("A", MaxDataURIBytes-len(prefix)+1)
	_, err := c.SubmitJSON(context.Background(), DefaultModelFill, map[string]any{
		"prompt": "x", "image_url": "https://cdn.example/a.png", "mask_url": over})
	require.ErrorIs(t, err, ErrDataURITooLarge)
	require.ErrorIs(t, err, ErrBadRequest, "terminal and free for the classifier")

	// Nested too: a data URI inside a list is the same bytes in the same body.
	_, err = c.SubmitJSON(context.Background(), DefaultModelFill, map[string]any{
		"image_urls": []any{over}})
	require.ErrorIs(t, err, ErrDataURITooLarge)

	// An *_url that the provider cannot fetch is refused the same way.
	for _, bad := range []string{"", "s3://bucket/k.png", "not a url"} {
		_, err = c.SubmitJSON(context.Background(), DefaultModelFill, map[string]any{"image_url": bad})
		require.ErrorIs(t, err, ErrBadImageURL, "reference %q", bad)
	}
	_, err = c.SubmitJSON(context.Background(), "  / ", map[string]any{"image_url": "https://x/a.png"})
	require.ErrorIs(t, err, ErrBadRequest, "no slug, no submit")
}

// TestAnInlinePictureAtTheCeilingLEAVES — the positive control: exactly MaxDataURIBytes goes.
func TestAnInlinePictureAtTheCeilingLEAVES(t *testing.T) {
	stub := &genericStub{}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()
	c := newGenericClient(srv.URL, Config{})
	prefix := "data:image/png;base64,"
	at := prefix + strings.Repeat("A", MaxDataURIBytes-len(prefix))
	_, err := c.SubmitJSON(context.Background(), DefaultModelOutpaint, map[string]any{"image_url": at})
	require.NoError(t, err)
	require.Equal(t, "/"+DefaultModelOutpaint, stub.submitPath)
}

// TestRouteCeilingUSD_IS_THE_3D_SHAPE — no tariff → the code ceiling; tariff + ceiling → the product;
// tariff alone → no number (ok=false), and the door refuses the kind. Mutation: answer the estimate
// for «tariff alone» → red.
func TestRouteCeilingUSD_IS_THE_3D_SHAPE(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		r    Route
		want string
		ok   bool
	}{
		{"outpaint, no tariff", Config{}, RouteOutpaint, "0.12", true},
		{"fill, no tariff", Config{}, RouteFill, "0.15", true},
		{"outpaint, tariff + ceiling", Config{UnitUSDOutpaint: 0.015, UnitsCeilingOutpaint: 8}, RouteOutpaint, "0.12", true},
		{"fill, tariff + ceiling", Config{UnitUSDFill: 0.05, UnitsCeilingFill: 2}, RouteFill, "0.1", true},
		{"outpaint, tariff only", Config{UnitUSDOutpaint: 0.015}, RouteOutpaint, "0", false},
		{"fill, tariff only", Config{UnitUSDFill: 0.05}, RouteFill, "0", false},
		{"fill's ceiling does not bound outpaint", Config{UnitUSDOutpaint: 0.015, UnitsCeilingFill: 9}, RouteOutpaint, "0", false},
		{"a ceiling without a tariff changes nothing", Config{UnitsCeilingFill: 9}, RouteFill, "0.15", true},
	} {
		got, ok := New(tc.cfg).RouteCeilingUSD(tc.r)
		require.Equal(t, tc.ok, ok, tc.name)
		require.Equal(t, tc.want, got.String(), tc.name)
	}
	var nilC *Client
	got, ok := nilC.RouteCeilingUSD(RouteFill)
	require.True(t, ok)
	require.Equal(t, "0.15", got.String())

	// The booking side: tariff × units, or the estimate; zero units is zero.
	c := New(Config{UnitUSDFill: 0.05})
	require.Equal(t, "0.1", c.CostRouteUSD(RouteFill, 2).String())
	require.Equal(t, "0.12", c.CostRouteUSD(RouteOutpaint, 5).String(), "outpaint has no tariff here: the estimate")
	require.True(t, c.CostRouteUSD(RouteFill, 0).IsZero())
	require.True(t, nilC.CostRouteUSD(RouteFill, 3).IsZero())

	n, ok := New(Config{UnitUSDFill: 0.05, UnitsCeilingFill: 2}).RouteUnitsCeiling(RouteFill)
	require.True(t, ok)
	require.Equal(t, 2.0, n)
	_, ok = New(Config{UnitsCeilingFill: 2}).RouteUnitsCeiling(RouteFill)
	require.False(t, ok, "a ceiling only matters under a tariff")
}

// TestTheRouteSlugsAndSettingsARE_PRINTED — a config dump must answer «which model, at what rate».
func TestTheRouteSlugsAndSettingsARE_PRINTED(t *testing.T) {
	c := Config{APIKey: "sk-secret", ModelOutpaint: "fal-ai/bria/expand", UnitUSDFill: 0.05, UnitsCeilingFill: 2}
	s := c.String()
	require.NotContains(t, s, "sk-secret")
	require.Contains(t, s, "ModelOutpaint:fal-ai/bria/expand")
	require.Contains(t, s, "UnitUSDFill:0.05")
	require.Contains(t, s, "UnitsCeilingFill:2")

	require.Equal(t, DefaultModelOutpaint, New(Config{}).ModelFor(RouteOutpaint))
	require.Equal(t, "fal-ai/bria/expand", New(Config{ModelOutpaint: " /fal-ai/bria/expand/ "}).ModelFor(RouteOutpaint))
	var nilC *Client
	require.Equal(t, DefaultModelFill, nilC.ModelFor(RouteFill))
	require.Equal(t, "", nilC.ModelFor(Route("nope")))
	require.Equal(t, "FAL_UNITS_CEILING_OUTPAINT", RouteOutpaint.UnitsCeilingEnv())
	require.Equal(t, "FAL_UNIT_USD_FILL", RouteFill.UnitUSDEnv())

	// Negative settings are «unset», never a negative price.
	neg := New(Config{UnitUSDFill: -1, UnitsCeilingFill: -3})
	got, ok := neg.RouteCeilingUSD(RouteFill)
	require.True(t, ok)
	require.Equal(t, "0.15", got.String())
}

// TestTheCutoutRequestsAreUNCHANGED_BY_THE_REFACTOR — the cut-out now rides on awaitFile; the
// requests it makes (paths, order, methods) are the same four as before.
func TestTheCutoutRequestsAreUNCHANGED_BY_THE_REFACTOR(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	stub := &cutoutStub{units: "1", contentType: "image/png", payload: []byte("x")}
	inner := stub.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		inner(w, r)
	}))
	defer srv.Close()
	c := newCutoutClient(t, srv.URL)
	_, err := removeBackground(c, context.Background(), "https://cdn.example/a.png", &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, []string{
		"POST /fal-ai/birefnet/v2",
		"GET /fal-ai/birefnet/requests/cut-1/status",
		"GET /fal-ai/birefnet/requests/cut-1",
		"GET /cutout.png",
	}, paths)
}
