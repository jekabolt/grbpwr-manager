package designgen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/aiprovtest"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/jekabolt/grbpwr-manager/internal/meshy"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/jekabolt/grbpwr-manager/internal/recraft"
)

// ═══ B-07: THE AI LEDGER FROM THE WORKER'S SIDE ═══
//
// Every probe runs the REAL routes against httptest stands and the in-memory ledger store
// (aiprovtest — the store's WHERE clauses and COALESCE merge, no database). The attempt rows are
// read off the same fakeStore every other probe here uses: the ledger is proven by what it wrote AND
// by what it left alone.

// withLedger gives a test worker the ledger over a fresh in-memory store.
func withLedger(w *Worker) *aiprovtest.Store {
	ai := &aiprovtest.Store{}
	w.ledger = aiprov.NewLedger(ai, func() string { return entity.DefaultBudgetTimezone })
	return ai
}

// pngB64 — a real one-pixel PNG, base64: the image route decodes what it is sent.
const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYPj/HwADAgH/pZzT0QAAAABJRU5ErkJggg=="

// imageStand answers every POST /images with one picture, the i-th call costing costs[i] and
// reporting tokens. It counts the calls it received.
type imageStand struct {
	srv   *httptest.Server
	calls atomic.Int32
}

func newImageStand(t *testing.T, costs ...float64) *imageStand {
	t.Helper()
	st := &imageStand{}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := int(st.calls.Add(1)) - 1
		cost := costs[len(costs)-1]
		if i < len(costs) {
			cost = costs[i]
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"b64_json":%q,"media_type":"image/png"}],`+
			`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"cost":%v}}`, pngB64, 100+i, 200+i, cost)
	}))
	t.Cleanup(st.srv.Close)
	return st
}

func imageRoute(baseURL string) Provider {
	return NewImageProvider(orimages.New(orimages.Config{APIKey: "k", BaseURL: baseURL, HTTPTimeout: 2 * time.Second}))
}

func perViewFlat(id int) entity.DesignRun {
	r := testRun(id, entity.DesignRunKindFlat)
	r.Author = "im"
	r.Params = entity.RawJSON(`{"views":["front","back"],"layout":"per_view"}`)
	return r
}

// (1) TestAPerViewRunIsONE_ROW_PER_PAID_CALL — two views on the per_view route are two paid calls:
// two ledger rows, call_no 1 and 2, each `ok` with ITS OWN provider price, under the one attempt; and
// the attempt row is priced with their sum exactly as before the ledger existed.
//
// MUTATIONS (each measured red→green): (a) `job.Recorder = w.recorderFor(...)` removed from execute →
// the ledger is empty (the attempt assertions stay green: prices identical, ledger empty); (b)
// `beginCall(..., i+1)` → `(..., 1)` → the second Begin collides on uq_ai_usage_call (store refuses,
// row lost) and the call_no assertion fails.
func TestAPerViewRunIsONE_ROW_PER_PAID_CALL(t *testing.T) {
	run := func(withLedgerToo bool) (*fakeStore, *aiprovtest.Store, int32) {
		stand := newImageStand(t, 0.04, 0.05)
		st := &fakeStore{}
		w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: imageRoute(stand.srv.URL)})
		var ai *aiprovtest.Store
		if withLedgerToo {
			ai = withLedger(w)
		}
		require.NoError(t, w.execute(context.Background(), perViewFlat(50), "tok"))
		return st, ai, stand.calls.Load()
	}

	st, ai, calls := run(true)
	require.EqualValues(t, 2, calls, "per_view × 2 views = two paid calls")

	// The attempt: one row, delivered, priced with the SUM — the money truth, untouched.
	require.Len(t, st.finished, 1)
	require.Equal(t, entity.DesignAttemptDelivered, st.finished[0].State)
	require.True(t, st.finished[0].Price.Valid)
	require.Equal(t, "0.09", st.finished[0].Price.Decimal.String())

	// The ledger: one row per physical call.
	rows := ai.Rows()
	require.Len(t, rows, 2)
	for i, r := range rows {
		require.Equal(t, i+1, r.Start.CallNo)
		require.Equal(t, 50, *r.Start.RunID)
		require.Equal(t, 1, *r.Start.AttemptNo, "both calls belong to the one attempt")
		require.Equal(t, entity.AIProviderOpenRouter, r.Start.ProviderKey, "the billing transport")
		require.Equal(t, orimages.DefaultModel, r.Start.Model)
		require.Equal(t, entity.AIPurposeImageGenerate, r.Start.Purpose)
		require.Equal(t, "im", r.Start.Actor, "design_run.author")
		require.Nil(t, r.Start.ActorAdminID, "the cached admin-id lookup is B-05's")
		require.Equal(t, entity.AICallOK, r.Status)
		require.Equal(t, entity.AICostProvider, r.End.CostSource)
		require.NotNil(t, r.End.Engaged)
		require.True(t, *r.End.Engaged)
		require.Equal(t, orimages.DefaultModel, r.End.ModelActual)
		require.NotNil(t, r.End.PromptTokens)
		require.Equal(t, 100+i, *r.End.PromptTokens)
		require.Equal(t, 200+i, *r.End.CompletionTokens)
		require.NotNil(t, r.End.LatencyMs)
		require.Empty(t, r.End.ErrorCode)
	}
	require.Equal(t, "0.04", rows[0].End.CostUSD.Decimal.String())
	require.Equal(t, "0.05", rows[1].End.CostUSD.Decimal.String())

	// BYTE-IDENTICAL ATTEMPTS: the same pass without a ledger writes exactly the same attempt rows.
	stNo, _, callsNo := run(false)
	require.Equal(t, calls, callsNo)
	require.Equal(t, stNo.finished, st.finished, "the ledger must not change one field of an attempt")
	require.Equal(t, stNo.started, st.started)
	require.Equal(t, stNo.failed, st.failed)
}

// (2) TestAChargedImageFailureIsCHARGED_FAILED_AND_STILL_BOOKED — a 200 with no picture and a cost
// (the billed-and-empty shape orimages returns WITH its error): the ledger row is `charged_failed`
// at the provider's price, and the attempt still books that price.
//
// MUTATION (measured red→green): imageCallEnd's `case end.CostUSD.Valid` removed → the row reads
// `failed` with the price kept but the status wrong (the report would not count it as a paid failure).
func TestAChargedImageFailureIsCHARGED_FAILED_AND_STILL_BOOKED(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"usage":{"prompt_tokens":10,"completion_tokens":0,"cost":0.03}}`))
	}))
	t.Cleanup(srv.Close)
	st := &fakeStore{}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: imageRoute(srv.URL)})
	ai := withLedger(w)
	run := testRun(51, entity.DesignRunKindFlat)
	run.Author = "im"

	require.NoError(t, w.execute(context.Background(), run, "tok"))

	require.Len(t, st.finished, 1)
	require.True(t, st.finished[0].Price.Valid, "the attempt still books the charge")
	require.Equal(t, "0.03", st.finished[0].Price.Decimal.String())

	rows := ai.Rows()
	require.Len(t, rows, 1)
	r := rows[0]
	require.Equal(t, entity.AICallChargedFailed, r.Status)
	require.Equal(t, "0.03", r.End.CostUSD.Decimal.String())
	require.Equal(t, entity.AICostProvider, r.End.CostSource)
	require.Equal(t, CodeEmptyResponse, r.End.ErrorCode, "the attempt's own machine word")
	require.True(t, *r.End.Engaged)
}

// TestTheImageCallOutcomeIsTHE_TRANSPORTS_OWN_FACT — each shape orimages returns maps to one ledger
// status, read off the real client against a stand (no engaged observer until B-14): a status the
// client classified is `free` (OpenRouter bills image generation all-or-nothing), a bare transport
// error is `unknown`, a 2xx with usage and no cost is `failed`.
//
// MUTATION (measured red→green): drop orimages.ErrProviderFailure from the free list → the 503 row
// reads `unknown`.
func TestTheImageCallOutcomeIsTHE_TRANSPORTS_OWN_FACT(t *testing.T) {
	stand := func(status int, body string) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	for _, c := range []struct {
		name     string
		baseURL  string
		status   string
		http     *int
		costZero bool // free: 0, not NULL
	}{
		{"402 out of credit", stand(402, `{"error":{"message":"no credit"}}`), entity.AICallFree, intp(402), true},
		{"503 unbilled by the provider's rule", stand(503, `{}`), entity.AICallFree, intp(503), true},
		{"401 key rejected", stand(401, `{}`), entity.AICallFree, intp(401), true},
		{"a transport error: nobody knows", deadURL, entity.AICallUnknown, nil, false},
		{"200, usage, no cost, no picture", stand(200, `{"data":[],"usage":{"prompt_tokens":5}}`), entity.AICallFailed, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ai := &aiprovtest.Store{}
			job := Job{RunID: 9, Kind: entity.DesignRunKindFlat, Prompt: "a flat", Layout: "one",
				Recorder: runRecorder{ledger: aiprov.NewLedger(ai, nil), runID: 9, attemptNo: 1,
					purpose: entity.AIPurposeImageGenerate, actor: "im"}}
			_, err := imageRoute(c.baseURL).Execute(context.Background(), job)
			require.Error(t, err)
			rows := ai.Rows()
			require.Len(t, rows, 1)
			require.Equal(t, c.status, rows[0].Status)
			require.Equal(t, c.http, rows[0].End.HTTPStatus)
			require.Equal(t, classify(err).Code, rows[0].End.ErrorCode)
			if c.costZero {
				require.True(t, rows[0].End.CostUSD.Valid)
				require.True(t, rows[0].End.CostUSD.Decimal.IsZero())
				require.Equal(t, entity.AICostFree, rows[0].End.CostSource)
			} else {
				require.False(t, rows[0].End.CostUSD.Valid, "unknown is NULL, never 0")
			}
		})
	}
}

func intp(v int) *int { return &v }

// falBuildStand — fal's queue for one 3D build: the submit answers req-1 (or `submitStatus`), the
// status is COMPLETED, the result names one billable unit and the model file.
type falBuildStand struct {
	srv          *httptest.Server
	submitStatus int
	posts        atomic.Int32
}

func newFalBuildStand(t *testing.T) *falBuildStand {
	t.Helper()
	st := &falBuildStand{submitStatus: http.StatusOK}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			st.posts.Add(1)
			if st.submitStatus != http.StatusOK {
				w.WriteHeader(st.submitStatus)
				_, _ = w.Write([]byte(`{"detail":"bad gateway"}`))
				return
			}
			_, _ = w.Write([]byte(`{"request_id":"req-1"}`))
		case strings.HasSuffix(r.URL.Path, "/model.glb"):
			_, _ = w.Write([]byte("glTF-bytes"))
		case strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
		default:
			w.Header().Set("x-fal-billable-units", "1")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model_glb": map[string]any{"url": "http://" + r.Host + "/model.glb"},
			})
		}
	}))
	t.Cleanup(st.srv.Close)
	return st
}

const falMeshySlug = "meshy/v7/multi-image-to-3d"

// (3) TestAFalBuildIsACCEPTED_THEN_PRICED_ON_ITS_OWN_ROW — the submit opens ONE row and closes it
// `accepted` with the locator; the collect (its own, free attempt) prices THAT row — (run, the
// SUBMIT's attempt, call 1) — `ok` at units × tariff, the same number the collect's attempt books.
//
// MUTATION (measured red→green): the collect's recorder scoped to `att.AttemptNo` (the collect's own
// attempt) instead of pendingAttempt → PriceAccepted looks for (run, 2, 1), finds nothing, and the
// row stays `accepted`.
func TestAFalBuildIsACCEPTED_THEN_PRICED_ON_ITS_OWN_ROW(t *testing.T) {
	stand := newFalBuildStand(t)
	st := &fakeStore{}
	w := steerWorker(t, st, falRoute(t, stand.srv.URL, falMeshySlug))
	ai := withLedger(w)
	run := steerRun(60)
	run.Author = "im"

	require.NoError(t, w.execute(context.Background(), run, "tok"))

	// The attempts, as before: #1 accepted with the locator and no price, #2 the collect, priced.
	require.Len(t, st.finished, 2)
	require.Equal(t, entity.DesignAttemptAccepted, st.finished[0].State)
	require.False(t, st.finished[0].Price.Valid)
	require.Equal(t, entity.DesignAttemptDelivered, st.finished[1].State)
	require.Equal(t, "1.2", st.finished[1].Price.Decimal.String())

	rows := ai.Rows()
	require.Len(t, rows, 1, "ONE row: the collect is a lookup and opens none")
	r := rows[0]
	require.Equal(t, 60, *r.Start.RunID)
	require.Equal(t, 1, *r.Start.AttemptNo, "the SUBMIT's attempt")
	require.Equal(t, 1, r.Start.CallNo)
	require.Equal(t, entity.AIProviderFal, r.Start.ProviderKey)
	require.Equal(t, falMeshySlug, r.Start.Model)
	require.Equal(t, entity.AIPurposeThreed, r.Start.Purpose)
	require.Equal(t, entity.AICallOK, r.Status, "priced by the collect that delivered")
	require.Equal(t, falMeshySlug+"#req-1", r.End.RequestID, "the submit's locator, kept")
	require.Equal(t, "1.2", r.End.CostUSD.Decimal.String(), "the number the attempt books")
	require.Equal(t, entity.AICostUnits, r.End.CostSource)
	require.NotNil(t, r.End.Units)
	require.Equal(t, "1", r.End.Units.String())
	require.Equal(t, "unit", r.End.Unit)

	verbs := []string{}
	for _, wr := range ai.Writes() {
		verbs = append(verbs, wr.Verb)
	}
	require.Equal(t, []string{"begin", "finish", "price"}, verbs[len(verbs)-3:])
}

// TestAResumedCollectPRICES_THE_ROW_ITS_SUBMIT_OPENED — the submit was accepted on an EARLIER pass
// (the history says attempt 1); this pass only collects, on attempt 2, and prices the row of attempt
// 1. A second collect of the same job changes nothing (the store moves only `accepted` rows).
//
// MUTATION (measured red→green): pendingAttempt left 0 on the resume branch → no recorder, row stays
// `accepted`.
func TestAResumedCollectPRICES_THE_ROW_ITS_SUBMIT_OPENED(t *testing.T) {
	stand := newFalBuildStand(t)
	run := steerRun(61)
	run.Author = "im"
	full := run
	full.Attempts = []entity.DesignRunAttempt{{RunId: 61, AttemptNo: 1, Provider: ThreedProviderFal,
		State: entity.DesignAttemptAccepted, ProviderRequestId: sql.NullString{String: falMeshySlug + "#req-1", Valid: true}}}
	st := &fakeStore{getRun: &full, nextNo: 1}
	w := steerWorker(t, st, falRoute(t, stand.srv.URL, falMeshySlug))
	ai := withLedger(w)
	runID, attemptNo := 61, 1
	ai.Seed(aiprovtest.Row{Status: entity.AICallAccepted, Start: entity.AICallStart{
		OccurredAt: time.Now(), ProviderKey: entity.AIProviderFal, Purpose: entity.AIPurposeThreed,
		RunID: &runID, AttemptNo: &attemptNo, CallNo: 1}})

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.Zero(t, stand.posts.Load(), "a resume never submits")
	require.Len(t, st.finished, 1)
	require.Equal(t, 2, st.finished[0].AttemptNo, "the collect's own attempt")

	r := ai.Rows()[0]
	require.Equal(t, entity.AICallOK, r.Status)
	require.Equal(t, "1.2", r.End.CostUSD.Decimal.String())

	// The same job collected again: nothing moves.
	st2 := &fakeStore{getRun: &full, nextNo: 2}
	w2 := steerWorker(t, st2, falRoute(t, stand.srv.URL, falMeshySlug))
	w2.ledger = w.ledger
	require.NoError(t, w2.execute(context.Background(), run, "tok"))
	require.Len(t, ai.Rows(), 1)
	require.Equal(t, "1.2", ai.Rows()[0].End.CostUSD.Decimal.String())
}

// (4) TestAnUnconfirmedSubmitIsUNKNOWN — fal answers the submit with a 502: the request left whole
// and nothing usable came back (ErrSubmitUnconfirmed). The row is `unknown`, engaged, NULL-priced,
// with the status and the attempt's own word.
//
// MUTATION (measured red→green): falSubmitEnd without the ErrSubmitUnconfirmed branch → `free`, cost
// 0 — a possibly-bought build booked as nothing.
func TestAnUnconfirmedSubmitIsUNKNOWN(t *testing.T) {
	stand := newFalBuildStand(t)
	stand.submitStatus = http.StatusBadGateway
	st := &fakeStore{}
	w := steerWorker(t, st, falRoute(t, stand.srv.URL, falMeshySlug))
	ai := withLedger(w)
	run := steerRun(62)
	run.Author = "im"

	require.NoError(t, w.execute(context.Background(), run, "tok"))
	require.EqualValues(t, 1, stand.posts.Load())
	require.Len(t, st.finished, 1)
	require.Equal(t, entity.DesignAttemptUnknown, st.finished[0].State)

	rows := ai.Rows()
	require.Len(t, rows, 1)
	r := rows[0]
	require.Equal(t, entity.AICallUnknown, r.Status)
	require.True(t, *r.End.Engaged, "the whole request left: money may have moved")
	require.False(t, r.End.CostUSD.Valid, "unknown is NULL, never 0")
	require.Equal(t, CodeSubmitUnconfirmed, r.End.ErrorCode)
	require.Equal(t, intp(502), r.End.HTTPStatus)

	// And the refusals that carried no money are `free`: a 422 from fal's validator.
	stand422 := newFalBuildStand(t)
	stand422.submitStatus = http.StatusUnprocessableEntity
	w422 := steerWorker(t, &fakeStore{}, falRoute(t, stand422.srv.URL, falMeshySlug))
	ai422 := withLedger(w422)
	require.NoError(t, w422.execute(context.Background(), steerRun(63), "tok"))
	require.Equal(t, entity.AICallFree, ai422.Rows()[0].Status)
	require.True(t, ai422.Rows()[0].End.CostUSD.Decimal.IsZero())
	require.Equal(t, intp(422), ai422.Rows()[0].End.HTTPStatus)
}

// (5) TestALedgerOutageNEVER_STOPS_THE_CALL — the store refuses every INSERT: the paid call still
// happens, the attempt is priced exactly as ever, the row is lost and the log says so at ERROR with
// its fields — and the outcome too, because that line is now its only record.
//
// MUTATION (measured red→green): Ledger.Begin returning nil on a store error → the Finish's ERROR line
// (with the price) disappears; making runRecorder panic on a nil handle would fail the pass.
func TestALedgerOutageNEVER_STOPS_THE_CALL(t *testing.T) {
	logs := captureSlog(t)
	stand := newImageStand(t, 0.04)
	st := &fakeStore{}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Image: imageRoute(stand.srv.URL)})
	ai := withLedger(w)
	ai.BeginErr = errors.New("ledger table is gone")
	run := testRun(52, entity.DesignRunKindFlat)
	run.Author = "im"

	require.NoError(t, w.execute(context.Background(), run, "tok"))

	require.EqualValues(t, 1, stand.calls.Load(), "the call happened")
	require.Len(t, st.finished, 1)
	require.Equal(t, "0.04", st.finished[0].Price.Decimal.String(), "and the attempt is priced as ever")
	require.Len(t, st.completed, 1, "and the run delivered")
	require.Empty(t, ai.Rows())
	out := logs.String()
	require.Contains(t, out, "level=ERROR")
	require.Contains(t, out, "its row is LOST")
	require.Contains(t, out, "outcome exists only in this line")
	require.Contains(t, out, "run_id=52")
	require.Contains(t, out, "cost_usd=0.04")
	require.Contains(t, out, `err="ledger table is gone"`)
}

// fakeReviver is the reserve sweeper's store.
type fakeReviver struct{ calls atomic.Int32 }

func (f *fakeReviver) ReviveExpiredRuns(context.Context) (int, error) { f.calls.Add(1); return 0, nil }

func seedLedgerRows(ai *aiprovtest.Store, now time.Time) (stale, fresh, done int64) {
	start := func(age time.Duration) entity.AICallStart {
		return entity.AICallStart{OccurredAt: now.Add(-age), ProviderKey: entity.AIProviderOpenRouter,
			Purpose: entity.AIPurposeImageGenerate, Actor: "im"}
	}
	stale = ai.Seed(aiprovtest.Row{Status: entity.AICallDispatching, Start: start(20 * time.Minute)})
	fresh = ai.Seed(aiprovtest.Row{Status: entity.AICallDispatching, Start: start(time.Minute)})
	done = ai.Seed(aiprovtest.Row{Status: entity.AICallOK, Start: start(20 * time.Minute)})
	return
}

func ledgerStatus(ai *aiprovtest.Store, id int64) string {
	for _, r := range ai.Rows() {
		if r.ID == id {
			return r.Status
		}
	}
	return ""
}

// (6) TestTheSweeperCALLS_A_STALE_DISPATCHING_ROW_UNKNOWN — a row still `dispatching` long after it
// was opened belongs to a process that died holding it: the tick turns it `unknown` (error_code
// sweeper) and leaves a fresh one and a finished one alone. On the reserve sweeper (generation off)
// and on the worker's own tick (generation on — the reserve sweeper is not built then), where it
// runs at most once a minute.
//
// MUTATIONS (measured red→green): sweepLedger call removed from Sweeper.sweepOnce → the stale row
// stays `dispatching`; maybeSweepLedger's rate limit removed → the second tick sweeps the new row.
func TestTheSweeperCALLS_A_STALE_DISPATCHING_ROW_UNKNOWN(t *testing.T) {
	t.Run("reserve sweeper (generation off)", func(t *testing.T) {
		ai := &aiprovtest.Store{}
		stale, fresh, done := seedLedgerRows(ai, time.Now())
		rv := &fakeReviver{}
		s := newSweeper(rv, time.Minute)
		s.ledger = aiprov.NewLedger(ai, nil)

		s.sweepOnce(context.Background())

		require.Equal(t, entity.AICallUnknown, ledgerStatus(ai, stale))
		require.Equal(t, entity.AICallDispatching, ledgerStatus(ai, fresh))
		require.Equal(t, entity.AICallOK, ledgerStatus(ai, done))
		for _, r := range ai.Rows() {
			if r.ID == stale {
				require.Equal(t, entity.AICallErrorSweeper, r.End.ErrorCode)
			}
		}
		require.EqualValues(t, 1, rv.calls.Load(), "the reserve sweep still ran")

		// A failing ledger sweep neither skips the reserve sweep nor marks the organ unhealthy.
		ai.SweepErr = errors.New("db down")
		s.sweepOnce(context.Background())
		require.EqualValues(t, 2, rv.calls.Load())
		require.Empty(t, s.tracker.LastError())
	})

	t.Run("worker tick (generation on)", func(t *testing.T) {
		st := &fakeStore{}
		w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{})
		ai := withLedger(w)
		now := time.Now()
		w.now = func() time.Time { return now }
		stale, fresh, done := seedLedgerRows(ai, now)

		require.True(t, w.runOnce(context.Background()))
		require.Equal(t, entity.AICallUnknown, ledgerStatus(ai, stale))
		require.Equal(t, entity.AICallDispatching, ledgerStatus(ai, fresh))
		require.Equal(t, entity.AICallOK, ledgerStatus(ai, done))

		// Another stale row, and the next tick five seconds later: not swept yet (once a minute).
		stale2, _, _ := seedLedgerRows(ai, now)
		now = now.Add(5 * time.Second)
		require.True(t, w.runOnce(context.Background()))
		require.Equal(t, entity.AICallDispatching, ledgerStatus(ai, stale2))
		now = now.Add(time.Minute)
		require.True(t, w.runOnce(context.Background()))
		require.Equal(t, entity.AICallUnknown, ledgerStatus(ai, stale2))

		// A failing sweep never fails the tick.
		ai.SweepErr = errors.New("db down")
		now = now.Add(2 * time.Minute)
		require.True(t, w.runOnce(context.Background()), "bookkeeping is not a reason to back off")
	})

	t.Run("the worker never sweeps a row its own pass may still hold", func(t *testing.T) {
		w := testWorker(&fakeStore{}, nil, newFakeSink(ContentTypePNG), Providers{})
		require.Equal(t, w.c.RunTimeout+ledgerFinishSlack, w.workerLedgerSweepAge(),
			"with the default 15 min RunTimeout, the pass bound wins over the plain 15 min")
		w.c.RunTimeout = time.Minute
		require.Equal(t, ledgerSweepAge, w.workerLedgerSweepAge())
	})
}

// TestTheAttemptWriteBOOKS_NO_LEDGER_ROW — FinishAttempt (the worker's recordAttempt, and the
// draft-idea handler's own call of the store verb) never touches the ledger: the ledger is written
// only around physical calls. A de-duplicated attempt can therefore never double a ledger row.
func TestTheAttemptWriteBOOKS_NO_LEDGER_ROW(t *testing.T) {
	st := &fakeStore{}
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{})
	ai := withLedger(w)
	out := &Outcome{Price: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.5"), Valid: true}, RequestID: "x"}
	require.NoError(t, w.recordAttempt(context.Background(), testRun(1, entity.DesignRunKindFlat), 1, out, nil,
		entity.DesignAttemptDelivered))
	require.Len(t, st.finished, 1)
	require.Empty(t, ai.Writes())
}

// TestAMeshyBuildIsAcceptedThenPricedINCREDITS — the direct Meshy route: the submit's row is
// `accepted`; a collect that reports credits and no model is `charged_failed` at credits × rate, on
// the SAME row, the number the attempt books.
func TestAMeshyBuildIsAcceptedThenPricedINCREDITS(t *testing.T) {
	prior := acceptedThreedRun()
	st := &fakeStore{getRun: &prior, nextNo: 1}
	w := chargedThreedWorker(t, st, 30)
	ai := withLedger(w)
	runID, attemptNo := 8, 1
	ai.Seed(aiprovtest.Row{Status: entity.AICallAccepted, Start: entity.AICallStart{
		OccurredAt: time.Now(), ProviderKey: entity.AIProviderMeshy, Purpose: entity.AIPurposeThreed,
		RunID: &runID, AttemptNo: &attemptNo, CallNo: 1}})

	require.NoError(t, w.execute(context.Background(), acceptedThreedRun(), "tok"))

	require.Equal(t, "0.6", st.finished[0].Price.Decimal.String())
	r := ai.Rows()[0]
	require.Equal(t, entity.AICallChargedFailed, r.Status)
	require.Equal(t, "0.6", r.End.CostUSD.Decimal.String())
	require.Equal(t, "30", r.End.Units.String())
	require.Equal(t, "credit", r.End.Unit)
	require.Equal(t, CodeEmptyResponse, r.End.ErrorCode)

	// And the submit half: a fresh Meshy submit opens its row and closes it `accepted` with the id.
	stand := newThreedSteerStand(t)
	ai2 := &aiprovtest.Store{}
	job := Job{RunID: 64, Kind: entity.DesignRunKindThreed, References: []string{"https://cdn.example/f.png"},
		Recorder: runRecorder{ledger: aiprov.NewLedger(ai2, nil), runID: 64, attemptNo: 1,
			purpose: entity.AIPurposeThreed, actor: "im"}}
	sub, err := newThreedSteerProvider(t, stand.srv.URL).Execute(context.Background(), job)
	require.NoError(t, err)
	require.True(t, sub.Pending)
	require.Equal(t, entity.AIProviderMeshy, sub.Provider)
	rows := ai2.Rows()
	require.Len(t, rows, 1)
	require.Equal(t, entity.AIProviderMeshy, rows[0].Start.ProviderKey)
	require.Equal(t, entity.AICallAccepted, rows[0].Status)
	require.Equal(t, "task-777", rows[0].End.RequestID)
	require.False(t, rows[0].End.CostUSD.Valid, "accepted is unpriced until the collect")
}

// fakeVectorGen is recraft's transport seam: one SVG at a stated price.
type fakeVectorGen struct {
	usd, credits float64
	err          error
}

func (g fakeVectorGen) GenerateImage(_ context.Context, req recraft.GenerateRequest) (*recraft.GenerateResponse, error) {
	if g.err != nil {
		return nil, g.err
	}
	return &recraft.GenerateResponse{
		Bytes:       []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path d="M1 1L9 9"/></svg>`),
		ContentType: "image/svg+xml", Model: req.Model, CostUSD: g.usd, Credits: g.credits,
	}, nil
}

// TestAVectorCallIsBOOKED_TO_THE_ACCOUNT_THAT_PAYS — recraft through OpenRouter is an OpenRouter row
// priced by OpenRouter's USD; RECRAFT_ROUTE=direct is a recraft row priced in credits (02-PLAN rev.1
// Opus #7). The route NAME on the attempt is unchanged either way.
func TestAVectorCallIsBOOKED_TO_THE_ACCOUNT_THAT_PAYS(t *testing.T) {
	models := map[recraft.Tier]string{recraft.TierVector: "recraft/recraft-v4-vector"}
	for _, c := range []struct {
		route        recraft.Route
		gen          fakeVectorGen
		provider     string
		source       string
		cost, units  string
		wantStatus   string
		wantCostNull bool
	}{
		{recraft.RouteOpenRouter, fakeVectorGen{usd: 0.08}, entity.AIProviderOpenRouter, entity.AICostProvider, "0.08", "", entity.AICallOK, false},
		{recraft.RouteDirect, fakeVectorGen{usd: 0.04, credits: 40}, entity.AIProviderRecraft, entity.AICostUnits, "0.04", "40", entity.AICallOK, false},
		{recraft.RouteOpenRouter, fakeVectorGen{err: fmt.Errorf("%w (HTTP 402): broke", recraft.ErrInsufficientCredits)},
			entity.AIProviderOpenRouter, entity.AICostFree, "0", "", entity.AICallFree, false},
		{recraft.RouteOpenRouter, fakeVectorGen{err: fmt.Errorf("%w: reset", recraft.ErrProviderFailure)},
			entity.AIProviderOpenRouter, entity.AICostNone, "", "", entity.AICallUnknown, true},
	} {
		t.Run(string(c.route)+"/"+c.wantStatus, func(t *testing.T) {
			ai := &aiprovtest.Store{}
			p := NewVectorProvider(recraft.NewWithGenerator(c.route, c.gen, models))
			job := Job{RunID: 3, Kind: entity.DesignRunKindVector, Prompt: "a flat", References: []string{"https://cdn.example/a.png"},
				Recorder: runRecorder{ledger: aiprov.NewLedger(ai, nil), runID: 3, attemptNo: 1,
					purpose: entity.AIPurposeVector, actor: "im"}}
			out, _ := p.Execute(context.Background(), job)
			require.Equal(t, "recraft_vector", p.Name(), "the attempt still names the ROUTE")
			r := ai.Rows()[0]
			require.Equal(t, c.provider, r.Start.ProviderKey)
			require.Equal(t, "recraft/recraft-v4-vector", r.Start.Model)
			require.Equal(t, entity.AIPurposeVector, r.Start.Purpose)
			require.Equal(t, c.wantStatus, r.Status)
			require.Equal(t, c.source, r.End.CostSource)
			if c.wantCostNull {
				require.False(t, r.End.CostUSD.Valid)
			} else {
				require.True(t, r.End.CostUSD.Decimal.Equal(decimal.RequireFromString(c.cost)))
			}
			if c.units != "" {
				require.Equal(t, c.units, r.End.Units.String())
				require.Equal(t, "credit", r.End.Unit)
			}
			if out != nil && c.wantStatus == entity.AICallOK {
				require.Equal(t, c.provider, out.Provider)
			}
		})
	}
}

// TestWithLedgerNilIsNoLedger — a typed nil must not become a non-nil interface the worker would
// call into.
func TestWithLedgerNilIsNoLedger(t *testing.T) {
	// `== nil`, NOT require.Nil: testify calls a typed-nil pointer inside an interface «nil», which is
	// exactly the defect this guards against (the worker's `w.ledger == nil` would be false).
	require.True(t, applyOptions([]Option{WithLedger(nil), nil}).ledger == nil)
	require.NotNil(t, applyOptions([]Option{WithLedger(aiprov.NewLedger(&aiprovtest.Store{}, nil))}).ledger)
	w := testWorker(&fakeStore{}, nil, newFakeSink(ContentTypePNG), Providers{})
	require.Nil(t, w.recorderFor(testRun(1, entity.DesignRunKindFlat), 1), "no ledger, no recorder")
	withLedger(w)
	require.Nil(t, w.recorderFor(testRun(1, entity.DesignRunKindFlat), 0), "no attempt, no recorder")
	rec := w.recorderFor(testRun(1, entity.DesignRunKindFlat), 3)
	require.NotNil(t, rec)
	require.Equal(t, aiprov.ActorUnknown, rec.(runRecorder).actor, "a run with no author is booked to unknown")
}

// recorded — a Job wired to a fresh in-memory ledger as (run 70, attempt 1), and that ledger.
func recorded(job Job, purpose string) (Job, *aiprovtest.Store) {
	ai := &aiprovtest.Store{}
	job.Recorder = runRecorder{ledger: aiprov.NewLedger(ai, nil), runID: 70, attemptNo: 1, purpose: purpose, actor: "im"}
	return job, ai
}

// TestTheFalPictureRoutesBOOK_THE_SUBMIT_AND_PRICE_IT_ON_COLLECT — cut-out, extend and fill share the
// submit/collect shape of the 3D route: the submit opens the row (`accepted`, the locator), the
// delivering collect prices it with the number the attempt books (tariff × fal's own units), and a
// charged failure is `charged_failed` — its units written only when fal NAMED them (an assumed unit
// is not a unit anybody reported).
//
// MUTATIONS (measured red→green): recordCollect removed from collectRouteFile's success path → the
// extend row stays `accepted`; reportedUnits ignoring `assumed` → the fill row's units read "1".
func TestTheFalPictureRoutesBOOK_THE_SUBMIT_AND_PRICE_IT_ON_COLLECT(t *testing.T) {
	t.Run("extend: submit accepted, collect ok at tariff × units", func(t *testing.T) {
		stand := &outpaintStand{canvas: solidPNG(t, 700, 300, colorOpaque), units: "4"}
		srv := httptest.NewServer(stand.handler(t))
		t.Cleanup(srv.Close)
		c := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second,
			UnitUSDOutpaint: 0.015, UnitsCeilingOutpaint: 8})
		prov := NewFalOutpaintProvider(c)
		r, _ := entity.DesignExtendRatioValue("21:9")
		plan, err := planExtend(200, 300, r)
		require.NoError(t, err)
		job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindExtend,
			References: []string{"https://cdn.example/m/11.png"}, Extend: &plan}, entity.AIPurposeImageExtend)

		out, err := prov.Execute(context.Background(), job)
		require.NoError(t, err)
		row := ai.Rows()[0]
		require.Equal(t, entity.AICallAccepted, row.Status)
		require.Equal(t, entity.AIProviderFal, row.Start.ProviderKey)
		require.Equal(t, fal.DefaultModelOutpaint, row.Start.Model)
		require.Equal(t, out.RequestID, row.End.RequestID)

		got, err := prov.(Collector).Collect(context.Background(), job, out.RequestID)
		require.NoError(t, err)
		row = ai.Rows()[0]
		require.Equal(t, entity.AICallOK, row.Status)
		require.Equal(t, "0.06", got.Price.Decimal.String())
		require.Equal(t, got.Price.Decimal.String(), row.End.CostUSD.Decimal.String(), "one number, two rows")
		require.Equal(t, "4", row.End.Units.String())
		require.Equal(t, entity.AICostUnits, row.End.CostSource)
	})

	t.Run("fill: a charged collect is charged_failed; an assumed unit is not written as one", func(t *testing.T) {
		units := ""
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/status"):
				_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
			case strings.Contains(r.URL.Path, "/requests/"):
				if units != "" {
					w.Header().Set("x-fal-billable-units", units)
				}
				_, _ = w.Write([]byte(`{not json`))
			}
		}))
		t.Cleanup(srv.Close)
		c := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second,
			UnitUSDFill: 0.01, UnitsCeilingFill: 10})
		for _, c2 := range []struct{ units, cost, wantUnits string }{{"", "0.1", ""}, {"3", "0.03", "3"}} {
			units = c2.units
			job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindInpaint}, entity.AIPurposeImageInpaint)
			runID, attemptNo := 70, 1
			ai.Seed(aiprovtest.Row{Status: entity.AICallAccepted, Start: entity.AICallStart{OccurredAt: time.Now(),
				ProviderKey: entity.AIProviderFal, Purpose: entity.AIPurposeImageInpaint, RunID: &runID, AttemptNo: &attemptNo}})
			out, err := NewFalFillProvider(c).(Collector).Collect(context.Background(), job, fal.DefaultModelFill+"#f-1")
			require.Error(t, err)
			row := ai.Rows()[0]
			require.Equal(t, entity.AICallChargedFailed, row.Status)
			require.Equal(t, c2.cost, row.End.CostUSD.Decimal.String())
			require.Equal(t, out.Price.Decimal.String(), row.End.CostUSD.Decimal.String())
			if c2.wantUnits == "" {
				require.Nil(t, row.End.Units, "fal named no units: the ceiling is booked, no unit count is claimed")
			} else {
				require.Equal(t, c2.wantUnits, row.End.Units.String())
			}
		}
	})

	t.Run("cutout: submit accepted, collect ok before the alpha verdict", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost:
				_, _ = w.Write([]byte(`{"request_id":"c-1"}`))
			case r.URL.Path == "/cut.png":
				_, _ = w.Write(solidPNG(t, 8, 8, colorOpaque)) // no alpha: a complaint, still a paid answer
			case strings.HasSuffix(r.URL.Path, "/status"):
				_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
			default:
				w.Header().Set("x-fal-billable-units", "2")
				_, _ = w.Write([]byte(`{"image":{"url":"http://` + r.Host + `/cut.png"}}`))
			}
		}))
		t.Cleanup(srv.Close)
		c := fal.New(fal.Config{APIKey: "k", BaseURL: srv.URL, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second,
			UnitUSDCutout: 0.005})
		prov := NewFalCutoutProvider(c)
		job, ai := recorded(Job{RunID: 70, Kind: entity.DesignRunKindCutout,
			References: []string{"https://cdn.example/m/5.png"}}, entity.AIPurposeImageCutout)
		out, err := prov.Execute(context.Background(), job)
		require.NoError(t, err)
		require.Equal(t, entity.AICallAccepted, ai.Rows()[0].Status)

		got, err := prov.(Collector).Collect(context.Background(), job, out.RequestID)
		require.ErrorIs(t, err, errCutoutNoAlpha, "the complaint is the attempt's")
		row := ai.Rows()[0]
		require.Equal(t, entity.AICallOK, row.Status, "the provider answered and billed: the call is ok")
		require.Empty(t, row.End.ErrorCode)
		require.Equal(t, got.Price.Decimal.String(), row.End.CostUSD.Decimal.String())
		require.Equal(t, "0.01", row.End.CostUSD.Decimal.String())
		require.Equal(t, "2", row.End.Units.String())
	})
}

var colorOpaque = color.NRGBA{R: 1, G: 2, B: 3, A: 255}

// TestATerminalCollectIsUNKNOWN_UNLESS_THE_PROVIDER_REFUNDED — an accepted job is billed when it
// finishes, whether or not we look: a lookup that fails for good on OUR side (a key rejected on the
// status GET, a request id the queue forgot) is `unknown`, never `failed`; only Meshy's FAILED task
// (credits returned) is `failed`; a job still running writes nothing and the row stays `accepted`.
//
// MUTATION (measured red→green): collectEnd mapping through classify(err).State → the 401 row reads
// `failed` (classify's word for the ATTEMPT, which paid nothing) and drops out of «unpriced».
func TestATerminalCollectIsUNKNOWN_UNLESS_THE_PROVIDER_REFUNDED(t *testing.T) {
	_, write := collectEnd(nil, fal.ErrNotReady, nil, "", false)
	require.False(t, write, "still running: the next collect decides")
	_, write = collectEnd(nil, fmt.Errorf("%w: job 1", meshy.ErrTimedOut), nil, "", false)
	require.False(t, write)

	for _, c := range []struct {
		name string
		err  error
		want string
		http *int
	}{
		{"fal rejects the key on the status lookup", fmt.Errorf("%w (HTTP 401): nope", fal.ErrUnauthorized), entity.AICallUnknown, intp(401)},
		{"fal forgot the request", fmt.Errorf("%w (HTTP 404): gone", fal.ErrRequestNotFound), entity.AICallUnknown, intp(404)},
		{"fal failed the task (it may have billed)", fal.ErrTaskFailed, entity.AICallUnknown, nil},
		{"Meshy failed the task (Meshy refunds)", meshy.ErrTaskFailed, entity.AICallFailed, nil},
	} {
		end, write := collectEnd(nil, c.err, nil, "", false)
		require.True(t, write, c.name)
		require.Equal(t, c.want, end.Status, c.name)
		require.Equal(t, c.http, end.HTTPStatus, c.name)
		require.False(t, end.CostUSD.Valid, "%s: no number is invented", c.name)
		require.Equal(t, classify(c.err).Code, end.ErrorCode, c.name)
	}
}
