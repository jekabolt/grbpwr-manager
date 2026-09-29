package designgen

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/pricing"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// ═══ H3 — fal IS AN image provider of the panel's image.generate route ═══
//
// The REAL fal image transport (fal.NewImages over a fal client keyed by the registry) behind the real
// chooser and worker, against a stand that plays queue.fal.run and its CDN.

// falImageStand — fal's queue for one image request; submitCode ≠ 0 refuses the submit.
type falImageStand struct {
	srv        *httptest.Server
	submitCode int
	png        []byte

	mu     sync.Mutex
	paths  []string
	auths  []string
	submit map[string]any
}

func newFalImageStand(t *testing.T) *falImageStand {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString(pngB64)
	require.NoError(t, err)
	s := &falImageStand{png: png}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.mu.Unlock()
		switch {
		case r.URL.Path == "/cdn/out.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(s.png)
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = io.WriteString(w, `{"status":"COMPLETED"}`)
		case strings.Contains(r.URL.Path, "/requests/"):
			w.Header().Set("x-fal-billable-units", "1")
			_, _ = io.WriteString(w, `{"images":[{"url":"http://`+r.Host+`/cdn/out.png","content_type":"image/png"}]}`)
		case r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			s.mu.Lock()
			s.submit = body
			s.mu.Unlock()
			if s.submitCode != 0 {
				w.WriteHeader(s.submitCode)
				_, _ = io.WriteString(w, `{"detail":"Application not found"}`)
				return
			}
			_, _ = io.WriteString(w, `{"request_id":"fal-img-1"}`)
		default:
			http.Error(w, "unexpected", http.StatusTeapot)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *falImageStand) recorded() ([]string, []string, map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...), append([]string(nil), s.auths...), s.submit
}

// falImageTransport — the real fal image transport over the rig's registry key, pointed at the stand.
func falImageTransport(t *testing.T, rg *imageRouteRig, s *falImageStand) ImageTransport {
	t.Helper()
	c := fal.New(fal.Config{KeyFunc: rg.reg.KeyFunc(entity.AIProviderFal), BaseURL: s.srv.URL,
		HTTPTimeout: 2 * time.Second, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second})
	tr := fal.NewImages(c)
	require.True(t, tr.Enabled(), "the rig sealed a fal key: the registry hands it to the transport")
	return tr
}

// TestAFalRouteHeadPAYS_ON_FAL — route `image.generate → fal / fal-ai/nano-banana/edit`: a flat run
// with a reference is paid on fal — POST at the row's endpoint id with `image_urls`, the free wait, the
// download (no key on it) — the attempt row names `fal_images`, the ledger row is booked to fal at that
// slug, priced by the catalogue when it has a per-call row (cost_source table + its version) and
// unpriced otherwise.
//
// MUTATION (measured red→green): app-style map without the fal transport → «routed to fal, and this
// build has no image transport» → the pass fails before any request.
func TestAFalRouteHeadPAYS_ON_FAL(t *testing.T) {
	const slug = "fal-ai/nano-banana/edit"
	rg := newImageRouteRig(t, row(1, entity.AIProviderFal, slug))
	s := newFalImageStand(t)
	st := &fakeStore{}
	sink := newFakeSink(ContentTypePNG)
	w := testWorker(st, media(11), sink, Providers{
		Image: NewRoutedImageProvider(rg.reg, map[string]ImageTransport{
			entity.AIProviderFal: falImageTransport(t, rg, s)}, orimages.DefaultModel),
	})
	ai := withLedger(w)
	run := testRun(98, entity.DesignRunKindFlat)
	run.Inputs = entity.RawJSON(`{"refs":[{"media_id":11}]}`)

	require.NoError(t, w.PreflightKind(entity.DesignRunKindFlat), "the door: fal with a key is a configured route")
	require.NoError(t, w.execute(context.Background(), run, "tok"))

	paths, auths, body := s.recorded()
	require.Equal(t, "POST /"+slug, paths[0], "the row's endpoint id is the queue the run is sent to")
	require.Equal(t, "GET /cdn/out.png", paths[len(paths)-1])
	require.Equal(t, "Key sk-fal-test-1234", auths[0], "the panel's key, through the registry")
	require.Empty(t, auths[len(auths)-1], "no key travels to the picture's host")
	require.Equal(t, []any{"https://cdn.example/m/11.png"}, body["image_urls"])
	require.EqualValues(t, 1, body["num_images"])

	require.Equal(t, []string{"fal_images"}, startedProviders(st), "the attempt row names the concrete candidate")
	require.Len(t, st.completed, 1)
	require.Equal(t, []string{ContentTypePNG}, sink.putTypes)

	rows := ai.Rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AIProviderFal, rows[0].Start.ProviderKey)
	require.Equal(t, slug, rows[0].Start.Model)
	require.Equal(t, entity.AICallOK, rows[0].Status)
	require.Equal(t, slug, rows[0].End.ModelActual)
	if m, ok := pricing.Lookup(entity.AIProviderFal, slug); ok && m.PerCallUSD.Valid {
		require.Equal(t, entity.AICostTable, rows[0].End.CostSource, "the table's number, never «fal said»")
		require.Equal(t, pricing.Version, rows[0].End.PriceVersion)
	} else {
		require.Equal(t, entity.AICostNone, rows[0].End.CostSource, "no catalogue row: unpriced")
		require.False(t, rows[0].End.CostUSD.Valid)
	}
}

// TestAFal404AtSubmitIS_A_NEW_ATTEMPT_ON_THE_NEXT — a well-formed endpoint id fal does not serve: its
// 404 at the gate buys nothing, the ledger row reads `free`, and the next pass pays the fallback on a
// fresh attempt row.
func TestAFal404AtSubmitIS_A_NEW_ATTEMPT_ON_THE_NEXT(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderFal, "acme/no-such-model"), row(2, entity.AIProviderOpenRouter, ""))
	s := newFalImageStand(t)
	s.submitCode = http.StatusNotFound
	or := &fakeImageTransport{model: "openai/gpt-image-2"}
	st := &fakeStore{}
	w := routedWorker(st, rg.reg, map[string]ImageTransport{
		entity.AIProviderFal: falImageTransport(t, rg, s), entity.AIProviderOpenRouter: or})
	ai := withLedger(w)
	run := testRun(99, entity.DesignRunKindFlat)

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	paths, _, _ := s.recorded()
	require.Equal(t, []string{"POST /acme/no-such-model"}, paths, "refused at the gate: no poll")
	require.Len(t, st.failed, 1)
	require.True(t, st.failed[0].Retryable)
	require.Equal(t, entity.AICallFree, ai.Rows()[0].Status)

	st.getRun = historyOf(st, run)
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, 1, or.n())
	require.Equal(t, []string{"fal_images", "openrouter_images"}, startedProviders(st))
	require.Len(t, st.completed, 1)
}

// TestABadFalSlugIS_SKIPPED_BY_THE_CHOOSER — a fal row naming "../x" never reaches the wire: the chooser
// asks Serves (fal.ValidSlug) and the fallback pays on attempt 1.
func TestABadFalSlugIS_SKIPPED_BY_THE_CHOOSER(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderFal, "../x"), row(2, entity.AIProviderOpenRouter, ""))
	s := newFalImageStand(t)
	or := &fakeImageTransport{model: "openai/gpt-image-2"}
	st := &fakeStore{}
	w := routedWorker(st, rg.reg, map[string]ImageTransport{
		entity.AIProviderFal: falImageTransport(t, rg, s), entity.AIProviderOpenRouter: or})
	require.NoError(t, w.execute(context.Background(), testRun(100, entity.DesignRunKindFlat), "tok"))
	paths, _, _ := s.recorded()
	require.Empty(t, paths)
	require.Equal(t, []string{"openrouter_images"}, startedProviders(st))
}

// TestImageCallEndBOOKS_A_TABLE_COST_AS_TABLE — H3: a transport that computed its cost from the table
// (Usage.CostSource = table) is booked cost_source table + pricing.Version; one that passed the
// provider's own number ("" source) stays `provider` with no version — on success and on a charged
// failure alike.
//
// MUTATION (measured red→green): the CostSource branch removed → the table cost reads `provider`.
func TestImageCallEndBOOKS_A_TABLE_COST_AS_TABLE(t *testing.T) {
	table := &orimages.Result{Model: "fal-ai/flux-pro/v1.1", Usage: orimages.Usage{Cost: 0.08, CostSource: entity.AICostTable}}
	end := imageCallEnd(entity.AIProviderFal, table, nil)
	require.Equal(t, entity.AICallOK, end.Status)
	require.Equal(t, entity.AICostTable, end.CostSource)
	require.Equal(t, pricing.Version, end.PriceVersion)
	require.Equal(t, "0.08", end.CostUSD.Decimal.String())

	own := &orimages.Result{Model: "gemini/standard", Usage: orimages.Usage{Cost: 0.021}}
	end = imageCallEnd(entity.AIProviderFal, own, nil)
	require.Equal(t, entity.AICostProvider, end.CostSource)
	require.Empty(t, end.PriceVersion)

	bought := &aiprov.CallError{Provider: entity.AIProviderFal, Code: aiprov.CodeProviderError, Engaged: true,
		Err: errors.New("fal: request x is bought and delivered no picture")}
	end = imageCallEnd(entity.AIProviderFal, table, bought)
	require.Equal(t, entity.AICallChargedFailed, end.Status)
	require.Equal(t, entity.AICostTable, end.CostSource)
	require.Equal(t, pricing.Version, end.PriceVersion)
}

// TestImageCallEndPRICES_A_SILENT_PROVIDER_BY_THE_TABLE — H4: openai and apibost state no price on
// /images, so their Result carries Cost 0; a DELIVERED picture is booked at the catalogue's per-call
// row for (provider, slug) as cost_source table + pricing.Version. A slug the table has no per-call row
// for stays unpriced (`none`); a 2xx that delivered nothing stays `failed` and unpriced (the transport's
// own fact, TestTheImageCallOutcomeIsTHE_TRANSPORTS_OWN_FACT); a refusal with no Result books no money.
//
// MUTATION (measured red→green): the pricing.Price branch removed → openai gpt-image-2 reads `none`.
func TestImageCallEndPRICES_A_SILENT_PROVIDER_BY_THE_TABLE(t *testing.T) {
	row, ok := pricing.Lookup(entity.AIProviderOpenAI, "gpt-image-2")
	require.True(t, ok && row.OutputUSDPer1M.Valid, "the openai catalogue prices gpt-image-2 by token (OpenAI's own tariff)")

	// 12 text-input + 4160 image-output tokens (a high 1024² on gpt-image-1's scale): $5/M + $30/M.
	silent := &orimages.Result{Model: "gpt-image-2", Usage: orimages.Usage{Prompt: 12, Completion: 4160}}
	end := imageCallEnd(entity.AIProviderOpenAI, silent, nil)
	require.Equal(t, entity.AICallOK, end.Status)
	require.Equal(t, entity.AICostTable, end.CostSource)
	require.Equal(t, pricing.Version, end.PriceVersion)
	require.True(t, end.CostUSD.Valid, "priced")
	require.Equal(t, "0.12486", end.CostUSD.Decimal.String(), "12×$5/M + 4160×$30/M — the tokens, not one flat picture")
	require.NotNil(t, end.PromptTokens)

	// The same picture on apibost: a per-call row ($0.16 whatever the quality — apibost's own tariff).
	end = imageCallEnd(entity.AIProviderApibost, &orimages.Result{Model: "gpt-image-2"}, nil)
	require.Equal(t, entity.AICostTable, end.CostSource)
	require.Equal(t, "0.16", end.CostUSD.Decimal.String())

	// A token row with no tokens reported: nothing is invented.
	end = imageCallEnd(entity.AIProviderOpenAI, &orimages.Result{Model: "gpt-image-2"}, nil)
	require.False(t, end.CostUSD.Valid)

	unknown := &orimages.Result{Model: "gpt-image-9-nightly"}
	end = imageCallEnd(entity.AIProviderOpenAI, unknown, nil)
	require.Equal(t, entity.AICostNone, end.CostSource)
	require.False(t, end.CostUSD.Valid)

	// A chat row is not an image price: nothing is invented for it either.
	chatRow := &orimages.Result{Model: "gpt-5-mini"}
	end = imageCallEnd(entity.AIProviderOpenAI, chatRow, nil)
	require.False(t, end.CostUSD.Valid)

	// A 2xx with no picture and no number: failed, unpriced — the table does not guess a charge.
	empty := &aiprov.CallError{Provider: entity.AIProviderOpenAI, Code: aiprov.CodeEmptyAnswer, Engaged: true,
		Err: errors.New("openai: the provider returned no image")}
	end = imageCallEnd(entity.AIProviderOpenAI, silent, empty)
	require.Equal(t, entity.AICallFailed, end.Status)
	require.False(t, end.CostUSD.Valid)
}
