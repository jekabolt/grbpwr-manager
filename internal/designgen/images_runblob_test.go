package designgen

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov/endpoints"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/runblob"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ B-31 — runblob IS an image provider of the panel's image.generate route ═══
//
// The REAL runblob image transport (submit → poll → download) over the real registry and the real
// worker, against a stand that plays platform.runblob.io and its CDN. The stand is reached through
// runblob.Config.Transport — a RoundTripper that sends platform.runblob.io to the httptest server;
// every request is still built against endpoints.RunblobHost (no base URL anywhere, SSRF rule).
//
// FIXTURES: the wire shapes of runblob-specs/kling.json and the Nano Banana docs page (read
// 2026-09-28); every VALUE is UNVERIFIED (G-06) until the owner's key runs one live job on beta.

const runblobTaskID = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"

// runblobStand — a fake runblob for pictures: the submit, the status reads (answered in order, the
// last repeated) and GET /cdn/out.png. It records every request's path and Authorization header.
type runblobStand struct {
	srv          *httptest.Server
	submitStatus int
	submit       string
	statuses     []string
	png          []byte

	mu    sync.Mutex
	paths []string
	auths []string
	body  []string
	reads int
}

func newRunblobStand(t *testing.T) *runblobStand {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString(pngB64)
	require.NoError(t, err)
	s := &runblobStand{submitStatus: http.StatusCreated, png: png}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *runblobStand) cdnURL() string { return s.srv.URL + "/cdn/out.png" }

// gemini / kling — the stand's answers for one family: the 201 and a completed read naming the CDN.
func (s *runblobStand) gemini() *runblobStand {
	s.submit = `{"task_uuid":"` + runblobTaskID + `","status":"pending","price":"0.0210"}`
	s.statuses = []string{`{"task_uuid":"` + runblobTaskID + `","status":"completed","prompt":"p","result_image_url":"` + s.cdnURL() + `","message":null}`}
	return s
}

func (s *runblobStand) kling() *runblobStand {
	s.submit = `{"generation_id":"` + runblobTaskID + `","status":"pending","price":"0.0290"}`
	s.statuses = []string{`{"generation_id":"` + runblobTaskID + `","status":"completed","prompt":"p","image_url":"` + s.cdnURL() + `","model":"kling-o1-photo"}`}
	return s
}

func (s *runblobStand) handle(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.paths = append(s.paths, r.Method+" "+r.URL.Path)
	s.auths = append(s.auths, r.Header.Get("Authorization"))
	s.body = append(s.body, string(b))
	s.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/generate"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.submitStatus)
		_, _ = io.WriteString(w, s.submit)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/generations/"):
		s.mu.Lock()
		i := s.reads
		s.reads++
		s.mu.Unlock()
		if i >= len(s.statuses) {
			i = len(s.statuses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, s.statuses[i])
	case r.Method == http.MethodGet && r.URL.Path == "/cdn/out.png":
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(s.png)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
	}
}

func (s *runblobStand) recorded() (paths, auths, bodies []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...), append([]string(nil), s.auths...), append([]string(nil), s.body...)
}

// toStand sends every request built for platform.runblob.io to the stand instead — the seam
// runblob.Config.Transport exists for. Anything else (the CDN url the stand itself named) goes where
// it points.
type toStand struct{ stand *url.URL }

func (rt toStand) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == rt.hostOf(endpoints.RunblobHost) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme, clone.URL.Host = rt.stand.Scheme, rt.stand.Host
		clone.Host = rt.stand.Host
		req = clone
	}
	return http.DefaultTransport.RoundTrip(req)
}

func (toStand) hostOf(raw string) string {
	u, _ := url.Parse(raw)
	return u.Host
}

// runblobTransport — the real image transport over the rig's registry key, pointed at the stand.
func runblobTransport(t *testing.T, rg *imageRouteRig, s *runblobStand) ImageTransport {
	t.Helper()
	u, err := url.Parse(s.srv.URL)
	require.NoError(t, err)
	c := runblob.New(runblob.Config{
		KeyFunc: rg.reg.KeyFunc(entity.AIProviderRunblob), HTTPTimeout: 2 * time.Second, Transport: toStand{stand: u},
	})
	tr := runblob.NewImages(c)
	require.True(t, tr.Enabled(), "the rig sealed a runblob key: the registry hands it to the transport")
	return tr
}

// (a) TestARunblobPrimaryPAYS_ON_RUNBLOB — route runblob (no model): a flat run is paid on runblob —
// POST /v1/gemini/generate, then the free poll, then the download from the url the status named (no
// key on it) — the attempt row names `runblob_images`, its price is the submit's, the ledger row is
// booked to runblob with cost_source provider and the price, and the picture lands in the sink.
//
// MUTATIONS (measured red→green): the transport's Usage.Cost left 0 → the ledger row reads cost_source
// none and the attempt's price NULL → red; app-style map without the runblob transport → the route is
// «routed to runblob, and this build has no image transport» → red at the first pass.
func TestARunblobPrimaryPAYS_ON_RUNBLOB(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderRunblob, ""))
	s := newRunblobStand(t).gemini()
	st := &fakeStore{}
	sink := newFakeSink(ContentTypePNG)
	w := testWorker(st, nil, sink, Providers{
		Image: NewRoutedImageProvider(rg.reg, map[string]ImageTransport{
			entity.AIProviderRunblob: runblobTransport(t, rg, s)}, "openai/gpt-image-2"),
	})
	w.now = func() time.Time { return time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC) }
	ai := withLedger(w)
	run := testRun(90, entity.DesignRunKindFlat)
	run.Author = "im"

	require.NoError(t, w.PreflightKind(entity.DesignRunKindFlat), "the door: runblob with a key is a configured route")
	require.NoError(t, w.execute(context.Background(), run, "tok"))

	paths, auths, bodies := s.recorded()
	require.Equal(t, []string{"POST /v1/gemini/generate", "GET /v1/gemini/generations/" + runblobTaskID, "GET /cdn/out.png"}, paths)
	require.Equal(t, "Bearer sk-runblob-test-1234", auths[0], "the panel's key, through the registry")
	require.Equal(t, "Bearer sk-runblob-test-1234", auths[1])
	require.Empty(t, auths[2], "no key travels to the picture's host")
	require.Contains(t, bodies[0], `"model":"standard"`, "the transport's own default: Nano Banana standard")
	require.Contains(t, bodies[0], `"quality":"standard"`, "a flat run's dial is high, and standard has no 2k: the family's tier, never a 422")

	require.Equal(t, []string{"runblob_images"}, startedProviders(st), "the attempt row names the concrete candidate")
	require.Len(t, st.finished, 1)
	require.Equal(t, entity.DesignAttemptDelivered, st.finished[0].State)
	require.True(t, st.finished[0].Price.Valid)
	require.Equal(t, "0.021", st.finished[0].Price.Decimal.String(), "the submit's price is the attempt's price")
	require.Len(t, st.completed, 1)
	require.Equal(t, []string{ContentTypePNG}, sink.putTypes)

	rows := ai.Rows()
	require.Len(t, rows, 1, "one ledger row for the one paid call (the poll and the download are free)")
	require.Equal(t, entity.AIProviderRunblob, rows[0].Start.ProviderKey)
	require.Equal(t, "gemini/standard", rows[0].Start.Model)
	require.Equal(t, entity.AIPurposeImageGenerate, rows[0].Start.Purpose)
	require.Equal(t, entity.AICallOK, rows[0].Status)
	require.Equal(t, entity.AICostProvider, rows[0].End.CostSource, "«their number»: runblob's own price")
	require.True(t, rows[0].End.CostUSD.Valid)
	require.Equal(t, "0.021", rows[0].End.CostUSD.Decimal.String())
	require.Equal(t, "gemini/standard", rows[0].End.ModelActual)
	require.Nil(t, rows[0].End.PromptTokens, "a per-call price carries no tokens")
}

// (b) TestARunblobKlingRowGOES_TO_O1_PHOTO — a route row naming `kling/o1-photo` sends the run to the
// o1-photo path with `images_url` (the run's reference pictures) and `img_resolution`; the ledger and
// the attempt carry that slug.
//
// MUTATION (measured red→green): imageProvider.Execute sending `job.Model` alone (the route row's
// model dropped from the request) → the transport draws gemini/standard → the path red.
func TestARunblobKlingRowGOES_TO_O1_PHOTO(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderRunblob, "kling/o1-photo"))
	s := newRunblobStand(t).kling()
	st := &fakeStore{}
	w := testWorker(st, media(11), newFakeSink(ContentTypePNG), Providers{
		Image: NewRoutedImageProvider(rg.reg, map[string]ImageTransport{
			entity.AIProviderRunblob: runblobTransport(t, rg, s)}, "openai/gpt-image-2"),
	})
	ai := withLedger(w)
	run := testRun(91, entity.DesignRunKindFlat)
	run.Inputs = entity.RawJSON(`{"refs":[{"media_id":11}]}`)

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	paths, _, bodies := s.recorded()
	require.Equal(t, []string{"POST /v1/kling/o1-photo/generate", "GET /v1/kling/o1-photo/generations/" + runblobTaskID, "GET /cdn/out.png"}, paths)
	require.Contains(t, bodies[0], `"images_url":["https://cdn.example/m/11.png"]`)
	require.Contains(t, bodies[0], `"img_resolution":"2k"`, "a flat run's dial is high → 2k")
	require.NotContains(t, bodies[0], `"images"`+`:`, "the Kling key, not the gemini one")
	require.Equal(t, []string{"runblob_images"}, startedProviders(st))
	require.Equal(t, "0.029", st.finished[0].Price.Decimal.String())
	rows := ai.Rows()
	require.Len(t, rows, 1)
	require.Equal(t, "kling/o1-photo", rows[0].Start.Model)
	require.Equal(t, entity.AICostProvider, rows[0].End.CostSource)
	require.Equal(t, "0.029", rows[0].End.CostUSD.Decimal.String())
}

// (c) TestARunblobFailedStatusIS_FREE_AND_THE_CHAIN_ADVANCES — runblob's own `failed` is refunded: the
// attempt closes `failed`, the ledger row reads `free` with no cost (the refunded price is NOT booked),
// the run comes straight back and the next pass pays the fallback on its own attempt.
//
// MUTATION (measured red→green): the transport's failed branch marked Engaged → the attempt reads
// `unknown`, the run closes terminal, the fallback is never called → red.
func TestARunblobFailedStatusIS_FREE_AND_THE_CHAIN_ADVANCES(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderRunblob, ""), row(2, entity.AIProviderOpenRouter, ""))
	s := newRunblobStand(t).gemini()
	s.statuses = []string{`{"task_uuid":"` + runblobTaskID + `","status":"failed","prompt":"p","result_image_url":null,"message":"TASK_FAILED"}`}
	or := &fakeImageTransport{model: "openai/gpt-image-2"}
	st := &fakeStore{}
	w := routedWorker(st, rg.reg, map[string]ImageTransport{
		entity.AIProviderRunblob: runblobTransport(t, rg, s), entity.AIProviderOpenRouter: or})
	ai := withLedger(w)
	run := testRun(92, entity.DesignRunKindFlat)
	logs := captureSlog(t)

	// ─── pass 1: runblob fails and refunds — free, terminal for runblob, the chain advances.
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	paths, _, _ := s.recorded()
	require.Equal(t, []string{"POST /v1/gemini/generate", "GET /v1/gemini/generations/" + runblobTaskID}, paths, "no download of a failed job")
	require.Zero(t, or.n(), "the fallback is the NEXT pass's")
	require.Equal(t, []string{"runblob_images"}, startedProviders(st))
	require.Equal(t, entity.DesignAttemptFailed, st.finished[0].State, "refunded: failed, not unknown")
	require.False(t, st.finished[0].Price.Valid, "the refunded price is not booked on the attempt")
	require.Len(t, st.failed, 1)
	require.True(t, st.failed[0].Retryable)
	require.Equal(t, w.now(), st.failed[0].NextAttempt, "advanced at once")
	require.Equal(t, CodeProviderUnavailable, st.failed[0].ErrorCode)
	require.Contains(t, st.failed[0].LastError, "failed at the provider and was refunded: TASK_FAILED")
	require.Contains(t, logs.String(), "from=runblob_images")
	require.Contains(t, logs.String(), "to=openrouter_images")

	rows := ai.Rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AIProviderRunblob, rows[0].Start.ProviderKey)
	// THE LEDGER ROW OF A REFUNDED CALL: status `free`, cost 0.00 with cost_source `free` (the ledger's
	// own word for «nothing is owed», aiprov/ledger.go — never the submit's 0.021), error_code
	// provider_unavailable, no HTTP status (the refusal was the provider's word, not a status code).
	require.Equal(t, entity.AICallFree, rows[0].Status, "the ledger row of a refunded call: free")
	require.True(t, rows[0].End.CostUSD.Valid && rows[0].End.CostUSD.Decimal.IsZero(), "cost 0.00, not 0.021: %v", rows[0].End.CostUSD)
	require.Equal(t, entity.AICostFree, rows[0].End.CostSource)
	require.Equal(t, CodeProviderUnavailable, rows[0].End.ErrorCode)
	require.Nil(t, rows[0].End.HTTPStatus)

	// ─── pass 2: openrouter pays on a fresh attempt.
	st.getRun = historyOf(st, run)
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, 1, or.n())
	require.Equal(t, []string{"runblob_images", "openrouter_images"}, startedProviders(st))
	require.Len(t, st.completed, 1)
	require.Len(t, ai.Rows(), 2)
	require.Equal(t, entity.AICallOK, ai.Rows()[1].Status)
}

// (d) TestARunblobPollTimeoutIS_UNKNOWN_AND_NEVER_FALLS_BACK — the job was bought (a 201) and the pass
// ran out of time waiting for it: the attempt is `unknown`, the ledger row `unknown` with no cost, the
// run closes terminal, and the fallback is never called — it would buy the picture a second time (D-16).
//
// MUTATION (measured red→green): the transport's ceiling / deadline failure built Engaged:false →
// the chain advances, openrouter is called on pass 2 → red.
func TestARunblobPollTimeoutIS_UNKNOWN_AND_NEVER_FALLS_BACK(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderRunblob, ""), row(2, entity.AIProviderOpenRouter, ""))
	s := newRunblobStand(t).gemini()
	s.statuses = []string{`{"task_uuid":"` + runblobTaskID + `","status":"processing","prompt":"p","result_image_url":null,"message":null}`}
	or := &fakeImageTransport{model: "openai/gpt-image-2"}
	st := &fakeStore{}
	w := routedWorker(st, rg.reg, map[string]ImageTransport{
		entity.AIProviderRunblob: runblobTransport(t, rg, s), entity.AIProviderOpenRouter: or})
	ai := withLedger(w)
	run := testRun(93, entity.DesignRunKindFlat)

	// The pass's own deadline (worker.go: RunTimeout) — here a short one — cuts the poll.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.NoError(t, w.execute(ctx, run, "tok"))
	paths, _, _ := s.recorded()
	require.Equal(t, "POST /v1/gemini/generate", paths[0])
	require.NotContains(t, paths, "GET /cdn/out.png")
	require.Equal(t, entity.DesignAttemptUnknown, st.finished[0].State)
	require.Len(t, st.failed, 1)
	require.False(t, st.failed[0].Retryable, "money may have moved: terminal")
	require.True(t, st.failed[0].NextAttempt.IsZero())
	require.Contains(t, st.failed[0].LastError, "generation "+runblobTaskID+" (gemini) is bought")
	if st.failed[0].Retryable {
		st.getRun = historyOf(st, run)
		require.NoError(t, w.execute(context.Background(), run, "tok"))
	}
	require.Zero(t, or.n(), "candidate 2 is never called: it would buy the picture a second time")
	require.Equal(t, []string{"runblob_images"}, startedProviders(st))
	rows := ai.Rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AICallUnknown, rows[0].Status)
	require.Equal(t, entity.AICostNone, rows[0].End.CostSource)
	require.False(t, rows[0].End.CostUSD.Valid)
}

// (e) TestAnUnknownSlugOnRunblobIS_SKIPPED_BY_THE_CHOOSER — a runblob row naming a slug the transport
// does not draw (an OpenRouter slug) is passed over before any row is opened: with a second candidate
// that one pays; alone, the run closes `unknown_image_model` and the stand sees nothing.
//
// MUTATION (measured red→green): Images.Serves answering true for every non-empty slug → the runblob
// row is chosen, the transport refuses before the wire (bad_request, not engaged), the chain advances
// on an ATTEMPT ROW → the «no attempt row» assertions red.
func TestAnUnknownSlugOnRunblobIS_SKIPPED_BY_THE_CHOOSER(t *testing.T) {
	t.Run("alone: unknown_image_model, nothing sent", func(t *testing.T) {
		rg := newImageRouteRig(t, row(1, entity.AIProviderRunblob, "openai/gpt-image-2"))
		s := newRunblobStand(t).gemini()
		st := &fakeStore{}
		w := routedWorker(st, rg.reg, map[string]ImageTransport{entity.AIProviderRunblob: runblobTransport(t, rg, s)})
		require.NoError(t, w.execute(context.Background(), testRun(94, entity.DesignRunKindFlat), "tok"))
		paths, _, _ := s.recorded()
		require.Empty(t, paths, "nothing was sent")
		require.Empty(t, startedProviders(st), "no attempt row")
		require.Len(t, st.failed, 1)
		require.Equal(t, CodeUnknownImageModel, st.failed[0].ErrorCode)
		require.False(t, st.failed[0].Retryable)
	})
	t.Run("with a fallback: the fallback pays on attempt 1", func(t *testing.T) {
		rg := newImageRouteRig(t, row(1, entity.AIProviderRunblob, "openai/gpt-image-2"), row(2, entity.AIProviderOpenRouter, ""))
		s := newRunblobStand(t).gemini()
		or := &fakeImageTransport{model: "openai/gpt-image-2"}
		st := &fakeStore{}
		w := routedWorker(st, rg.reg, map[string]ImageTransport{
			entity.AIProviderRunblob: runblobTransport(t, rg, s), entity.AIProviderOpenRouter: or})
		require.NoError(t, w.execute(context.Background(), testRun(95, entity.DesignRunKindFlat), "tok"))
		paths, _, _ := s.recorded()
		require.Empty(t, paths)
		require.Equal(t, 1, or.n())
		require.Equal(t, []string{"openrouter_images"}, startedProviders(st), "runblob was skipped, not tried")
		require.Len(t, st.completed, 1)
	})
	t.Run("a frozen runblob slug is drawn by runblob and skipped by nobody else's transport", func(t *testing.T) {
		rg := newImageRouteRig(t, row(1, entity.AIProviderOpenRouter, ""), row(2, entity.AIProviderRunblob, ""))
		s := newRunblobStand(t).kling()
		or := &fakeImageTransport{model: "openai/gpt-image-2", only: map[string]bool{"openai/gpt-image-2": true}}
		st := &fakeStore{}
		w := routedWorker(st, rg.reg, map[string]ImageTransport{
			entity.AIProviderRunblob: runblobTransport(t, rg, s), entity.AIProviderOpenRouter: or})
		run := testRun(96, entity.DesignRunKindFlat)
		run.Params = entity.RawJSON(`{"image":{"model":"kling/o3-photo"}}`)
		require.NoError(t, w.execute(context.Background(), run, "tok"))
		require.Zero(t, or.n())
		paths, _, _ := s.recorded()
		require.Equal(t, "POST /v1/kling/o3-photo/generate", paths[0])
		require.Equal(t, []string{"runblob_images"}, startedProviders(st))
	})
}

// (f) TestA402OnRunblobIS_A_NEW_ATTEMPT_ON_THE_NEXT — runblob's 402 (INSUFFICIENT_CREDITS) buys
// nothing: the attempt closes `failed` / provider_out_of_credit, the ledger row `free` with the 402,
// and the next pass pays the fallback on a fresh attempt row.
//
// MUTATION (measured red→green): the transport marking a submit's refusal engaged → the run closes
// `unknown`, terminal → red.
func TestA402OnRunblobIS_A_NEW_ATTEMPT_ON_THE_NEXT(t *testing.T) {
	rg := newImageRouteRig(t, row(1, entity.AIProviderRunblob, ""), row(2, entity.AIProviderOpenRouter, ""))
	s := newRunblobStand(t).gemini()
	s.submitStatus, s.submit = http.StatusPaymentRequired, `{"detail":"INSUFFICIENT_CREDITS"}`
	or := &fakeImageTransport{model: "openai/gpt-image-2"}
	st := &fakeStore{}
	w := routedWorker(st, rg.reg, map[string]ImageTransport{
		entity.AIProviderRunblob: runblobTransport(t, rg, s), entity.AIProviderOpenRouter: or})
	ai := withLedger(w)
	run := testRun(97, entity.DesignRunKindFlat)

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	paths, _, _ := s.recorded()
	require.Equal(t, []string{"POST /v1/gemini/generate"}, paths, "refused at the gate: no poll")
	require.Equal(t, []string{"runblob_images"}, startedProviders(st))
	require.Equal(t, entity.DesignAttemptFailed, st.finished[0].State)
	require.Len(t, st.failed, 1)
	require.True(t, st.failed[0].Retryable)
	require.Equal(t, w.now(), st.failed[0].NextAttempt, "the re-queue is immediate")
	require.Equal(t, CodeOutOfCredit, st.failed[0].ErrorCode)
	rows := ai.Rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AICallFree, rows[0].Status)
	require.Equal(t, intp(http.StatusPaymentRequired), rows[0].End.HTTPStatus)

	st.getRun = historyOf(st, run)
	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Equal(t, 1, or.n())
	require.Equal(t, []string{"runblob_images", "openrouter_images"}, startedProviders(st))
	require.Len(t, st.completed, 1)
}
