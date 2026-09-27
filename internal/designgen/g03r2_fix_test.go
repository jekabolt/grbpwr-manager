package designgen

import (
	"context"
	"encoding/json"
	"image/color"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	"github.com/stretchr/testify/require"
)

// ═══ G-03 r2 fix pass — Codex 3, 4 and 8, the worker side ═══

// TestAYoungOpenSubmitIsLEFT_TO_SETTLE — Codex 3 (b). The pickup holds the claim because the previous
// pass's LEASE expired, which does not prove its paid call has stopped. An open attempt younger than
// the settle grace is neither closed `unknown` nor resubmitted: the run goes back to the queue until
// the grace is over (NextAttempt = started + grace), spending nothing. Past the grace the old guard
// applies (TestAnOpenSubmitAtPickupIsNOT_BOUGHT_AGAIN).
// MUTATION (measured red): drop the `w.clock().Before(settleBy)` branch in execute → the young row is
// closed `unknown` and the run fails terminally.
func TestAYoungOpenSubmitIsLEFT_TO_SETTLE(t *testing.T) {
	started := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	open := entity.DesignRunAttempt{RunId: 4, AttemptNo: 1, Provider: providerNameFalFill,
		State: entity.DesignAttemptDispatching, StartedAt: started}
	st := &fakeStore{getRun: &entity.DesignRun{Id: 4, Attempts: []entity.DesignRunAttempt{open}}}
	prov := asyncFill(&Outcome{RequestID: "x#y", Pending: true}, nil)
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Threed: prov})
	w.now = func() time.Time { return started.Add(time.Minute) }

	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindThreed), "tok"))
	require.Empty(t, prov.calls, "no second submit")
	require.Empty(t, st.started, "no attempt opened")
	require.Empty(t, st.finished, "the possibly live row is NOT closed under its owner")
	require.Equal(t, []string{CodeSubmitSettling + " retry=true"}, failedCodes(st))
	require.Equal(t, started.Add(w.submitSettleGrace()), st.failed[0].NextAttempt,
		"the run comes back exactly when the grace is over, not on the store's short first back-off")
	require.Greater(t, w.submitSettleGrace(), w.c.RunTimeout+settleTimeout,
		"the grace covers the longest a cooperative pass can still be inside the call and its write")
}

// TestALongAcceptedLocatorIsSTORED_WITHIN_THE_COLUMN — Codex 8. The slug is checked before the submit;
// the id is fal's and is typed as a string. A locator over provider_request_id's 128 must not fail
// the ACCEPTED write after the charge: it degrades to the namespace form, then to the bare id, each
// still resumable.
// MUTATION (measured red): drop the fitRequestLocator call in execute → the accepted row is handed a
// 131-character locator.
func TestALongAcceptedLocatorIsSTORED_WITHIN_THE_COLUMN(t *testing.T) {
	slug := "fal-ai/" + strings.Repeat("s", 65) + "/v1/fill" // 80 characters: falLocatorFits lets it submit
	require.NoError(t, falLocatorFits(slug))
	id := strings.Repeat("r", 50)
	long := falLocator(slug, id)
	require.Greater(t, len(long), providerRequestIDMax)

	st := &fakeStore{}
	prov := asyncFill(&Outcome{RequestID: long, Pending: true}, nil)
	w := testWorker(st, nil, newFakeSink(ContentTypePNG), Providers{Threed: prov})
	require.NoError(t, w.execute(context.Background(), testRun(4, entity.DesignRunKindThreed), "tok"))

	require.NotEmpty(t, st.finished)
	acc := st.finished[0]
	require.Equal(t, entity.DesignAttemptAccepted, acc.State)
	require.LessOrEqual(t, len(acc.ProviderRequestId), providerRequestIDMax)
	require.Equal(t, fal.QueueNamespace(slug)+falLocatorSep+id, acc.ProviderRequestId,
		"the namespace is all a poll needs")
	require.Equal(t, []string{acc.ProviderRequestId}, prov.collectFor, "the collect polls what was stored")

	// The later steps of the ladder, directly.
	hugeID := strings.Repeat("q", 120)
	require.Equal(t, hugeID, fitRequestLocator(context.Background(), 4, falLocator(slug, hugeID)),
		"namespace + id does not fit either: the bare id, polled against the route's namespaces")
	tooLong := strings.Repeat("z", 130)
	require.Equal(t, falLocator(slug, tooLong), fitRequestLocator(context.Background(), 4, falLocator(slug, tooLong)),
		"nothing fits: returned whole, the write refuses it and the run fails closed with the id")
	require.Equal(t, "a/b#c", fitRequestLocator(context.Background(), 4, "a/b#c"))
}

// fal3DStub is a fal queue that knows ONE request, under ONE namespace, and answers 404 everywhere
// else — the shape of «the build was bought under slug A and the deployment now says B».
type fal3DStub struct {
	knownNS string

	mu     sync.Mutex
	paths  []string
	posted string
}

func (s *fal3DStub) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		switch {
		case r.URL.Path == "/m.glb":
			_, _ = w.Write([]byte("glTF"))
		case r.Method == http.MethodPost:
			s.mu.Lock()
			s.posted = r.URL.Path
			s.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "gen-1"})
		case !strings.HasPrefix(r.URL.Path, "/"+s.knownNS+"/requests/gen-1"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"not found"}`))
		case strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
		default:
			w.Header().Set("x-fal-billable-units", "1")
			_ = json.NewEncoder(w).Encode(map[string]any{"model_glb": map[string]any{"url": "http://" + r.Host + "/m.glb"}})
		}
	}
}

func fal3DClient(base, model string) *fal.Client {
	return fal.New(fal.Config{APIKey: "test-key-not-a-real-one", BaseURL: base, Model3D: model,
		HTTPTimeout: 2 * time.Second, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second})
}

// TestThe3DLocatorCARRIES_ITS_SLUG_ACROSS_A_CUSTOM_MOVE — Codex 4 (a). A build bought under a custom
// FAL_MODEL_3D=A is resumed after the deployment moved to another custom B, which is neither the
// default nor retired3D — the one move the namespace search could never recover. The accepted row now
// stores "<slug>#<id>", and the collect polls A.
// MUTATION (measured red): Execute back to `RequestID: id` (bare) → the collect polls B, then the
// default/retired namespaces, and ends 404: a paid build lost.
func TestThe3DLocatorCARRIES_ITS_SLUG_ACROSS_A_CUSTOM_MOVE(t *testing.T) {
	stub := &fal3DStub{knownNS: "acme/custom-a"}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	job := Job{RunID: 4, References: []string{"https://cdn.example/f.png"}, ReferenceViews: []string{entity.DesignViewFront}}
	out, err := falThreedProvider{c: fal3DClient(srv.URL, "acme/custom-a/v1")}.execute(context.Background(), job, threedOptions{})
	require.NoError(t, err)
	require.Equal(t, "/acme/custom-a/v1", stub.posted)
	require.Equal(t, "acme/custom-a/v1#gen-1", out.RequestID, "the slug travels with the id")

	moved := falThreedProvider{c: fal3DClient(srv.URL, "acme/custom-b/v1")}
	got, err := moved.Collect(context.Background(), job, out.RequestID)
	require.NoError(t, err)
	require.Equal(t, out.RequestID, got.RequestID, "the collect row keys the charge by the same locator")
	require.Equal(t, "acme/custom-a/v1", got.Model, "priced as what it was bought as")
	for _, p := range stub.paths {
		require.NotContains(t, p, "custom-b", "the configured slug is never asked about a build it did not sell")
	}
}

// TestABareLegacyIdIsSEARCHED_IN_THE_ROUTES_NAMESPACES — Codex 4 (b). A generic-route row written
// before the locator holds a bare id. The deployment now points FAL_MODEL_OUTPAINT at bria; the
// request was queued under the flux default. Today's slug 404s past the grace; the route's known
// namespaces are asked, and the paid file is collected where it lives.
// MUTATION (measured red): collectRouteFile back to `models := []string{c.ModelFor(route)}` for a bare
// id → ErrRequestNotFound.
func TestABareLegacyIdIsSEARCHED_IN_THE_ROUTES_NAMESPACES(t *testing.T) {
	var fileURL string
	knownNS := fal.QueueNamespace(fal.DefaultModelOutpaint)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/f.png":
			_, _ = w.Write(solidPNG(t, 4, 4, color.NRGBA{R: 200, A: 255}))
		case !strings.HasPrefix(r.URL.Path, "/"+knownNS+"/requests/legacy-1"):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/status"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
		default:
			w.Header().Set("x-fal-billable-units", "1")
			_ = json.NewEncoder(w).Encode(map[string]any{"images": []map[string]any{{"url": fileURL, "content_type": "image/png"}}})
		}
	}))
	defer srv.Close()
	fileURL = srv.URL + "/f.png"

	c := fal.New(fal.Config{APIKey: "test-key-not-a-real-one", BaseURL: srv.URL, ModelOutpaint: "fal-ai/bria/expand",
		HTTPTimeout: 2 * time.Second, PollInterval: 5 * time.Millisecond, PollTimeout: time.Second})
	out, err := collectRouteFile(context.Background(), c, fal.RouteOutpaint, Job{RunID: 4}, "legacy-1")
	require.NoError(t, err)
	require.Equal(t, "legacy-1", out.RequestID)
	require.Len(t, out.Artifacts, 1)
}
