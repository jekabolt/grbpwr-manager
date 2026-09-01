package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestClient points a client at a stub and shortens every wait, so a poll loop in a test is
// milliseconds rather than minutes.
func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	return New(Config{
		APIKey:       "test-key-not-a-real-one",
		BaseURL:      base,
		HTTPTimeout:  2 * time.Second,
		PollInterval: 5 * time.Millisecond,
		PollTimeout:  2 * time.Second,
		UnitUSD:      0.5,
	})
}

// TestARouteWithNoKeyREFUSES_AND_NAMES_THE_VARIABLE.
//
// ⚠ THE ASSERTION IS ON THE WORDS, AND IT IS THE POINT OF THE TEST. This sentence is what a person
// reads on the screen when they press GENERATE. «not configured» is a fact about the process;
// «FAL_KEY is not set» is a fact the person can act on, and the owner who has just typed a key into
// a dashboard needs to be able to tell, from the button alone, whether that was the missing piece.
func TestARouteWithNoKeyREFUSES_AND_NAMES_THE_VARIABLE(t *testing.T) {
	require.Contains(t, ErrNotConfigured.Error(), "FAL_KEY is not set")

	c := New(Config{}) // no key
	require.False(t, c.Enabled())

	_, err := c.Submit(context.Background(), Request3D{FrontURL: "https://cdn.example/f.png"})
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = c.Collect(context.Background(), "req-1", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = c.Await(context.Background(), "req-1", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrNotConfigured)

	// A NIL CLIENT IS A DISABLED CLIENT, so a caller need not nil-check before asking.
	var nilC *Client
	require.False(t, nilC.Enabled())
}

// TestTheKeyIsNeverPRINTED. A config printed with %v, %+v or %s must not carry the secret; all
// three route through Stringer.
func TestTheKeyIsNeverPRINTED(t *testing.T) {
	c := Config{APIKey: "sk-super-secret-value", BaseURL: "https://queue.fal.run"}
	for _, s := range []string{c.String(), strings.TrimSpace(strings.Join([]string{c.String()}, ""))} {
		require.NotContains(t, s, "sk-super-secret-value")
		require.Contains(t, s, "REDACTED")
	}
	// An UNSET key stays visibly unset: whether the provider is configured at all is diagnostic,
	// and hiding that would turn a redaction into a second mystery.
	require.NotContains(t, Config{}.String(), "REDACTED")
}

// TestThePollingPathDropsTheSubPathButTheSubmitKeepsIt.
//
// fal submits a model id WHOLE and polls it at its BASE. Getting this backwards produces a paid
// build whose result can never be collected — the most expensive shape of mistake this package can
// make, because the money is already gone when the mistake is discovered.
func TestThePollingPathDropsTheSubPathButTheSubmitKeepsIt(t *testing.T) {
	require.Equal(t, "hitem3d/hi3d", queuePath("hitem3d/hi3d/v3.0/multi-view-to-3d"))
	require.Equal(t, "fal-ai/flux", queuePath("fal-ai/flux/dev"))
	// A two-segment id has no sub-path to drop.
	require.Equal(t, "fal-ai/fast-sdxl", queuePath("fal-ai/fast-sdxl"))
	// A malformed id is returned as it is: inventing a namespace here would produce a silently
	// mangled URL instead of a readable provider answer.
	require.Equal(t, "nonsense", queuePath("nonsense"))
}

// TestSubmitSendsTheViewsBY_NAME. This is the whole reason this provider was asked for: the bench
// knows which plate is the front, and this route is the first one that can be told.
func TestSubmitSendsTheViewsBY_NAME(t *testing.T) {
	var gotPath, gotAuth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id": "req-42",
			"status_url": srvStatusURL(r.Host, "hitem3d/hi3d", "req-42"),
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	id, err := c.Submit(context.Background(), Request3D{
		FrontURL: "https://cdn.example/front.png",
		BackURL:  "https://cdn.example/back.png",
		LeftURL:  "https://cdn.example/left.png",
		RightURL: "https://cdn.example/right.png",
	})
	require.NoError(t, err)
	require.Equal(t, "req-42", id)

	require.Equal(t, "/"+DefaultModel3D, gotPath, "the submit keeps the model's whole sub-path")
	// fal's own scheme is `Key`, not `Bearer`. A Bearer prefix here is a 401 on every call.
	require.Equal(t, "Key test-key-not-a-real-one", gotAuth)

	require.Equal(t, "https://cdn.example/front.png", body["front_image_url"])
	require.Equal(t, "https://cdn.example/back.png", body["back_image_url"])
	require.Equal(t, "https://cdn.example/left.png", body["left_image_url"])
	require.Equal(t, "https://cdn.example/right.png", body["right_image_url"])
	require.Equal(t, "glb", body["export_format"], "the band shows GLB and only GLB")
	require.Equal(t, true, body["enable_texture"], "an untextured mesh answers a different question")
	// STATED, NOT OMITTED: the provider's own default for enable_pbr is TRUE, and PBR maps
	// quadruple the download for lighting nuance a product tile does not show.
	require.Equal(t, false, body["enable_pbr"])
}

func srvStatusURL(host, base, id string) string {
	return "http://" + host + "/" + base + "/requests/" + id + "/status"
}

// TestABuildWithNoFrontIsREFUSED_LOCALLY. A build without a front is not a cheaper build, it is a
// wrong one: hitem3d reads front_image_url as the face of the object.
func TestABuildWithNoFrontIsREFUSED_LOCALLY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a request left the process for a job that could be refused locally, for free")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Submit(context.Background(), Request3D{BackURL: "https://cdn.example/back.png"})
	require.ErrorIs(t, err, ErrNoFrontView)
}

// TestARETIRED_MODEL_DOES_NOT_READ_AS_A_BUSY_SERVICE.
//
// ⚠ THIS IS THE DEFECT THAT ONCE TOOK DOWN BOTH AI FEATURES AT ONCE: a slug the provider had
// removed surfaced as an ordinary error, was classified as weather, and was retried to the attempt
// cap while the history row blamed the provider's availability. The two 404s of this API mean
// opposite things and are told apart BY PATH, never by the provider's English sentence.
func TestARETIRED_MODEL_DOES_NOT_READ_AS_A_BUSY_SERVICE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"detail": "Not Found"})
	}))
	defer srv.Close()
	c := newTestClient(t, srv.URL)

	_, err := c.Submit(context.Background(), Request3D{FrontURL: "https://cdn.example/f.png"})
	require.ErrorIs(t, err, ErrModelUnavailable, "404 on the submit path means the slug is gone")
	require.NotErrorIs(t, err, ErrRequestNotFound)
	require.Contains(t, err.Error(), DefaultModel3D, "the message has to name the slug to fix")

	_, err = c.Collect(context.Background(), "req-1", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrRequestNotFound, "404 on the request path means the id is worthless")
	require.NotErrorIs(t, err, ErrModelUnavailable)
}

// TestEveryStatusCodeGetsTheSentinelThatSaysWhatToDO. Classification is BY STATUS, never by the
// provider's wording, so a reworded message cannot silently reclassify a fault.
func TestEveryStatusCodeGetsTheSentinelThatSaysWhatToDO(t *testing.T) {
	for _, tc := range []struct {
		code int
		want error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrUnauthorized},
		{http.StatusPaymentRequired, ErrOutOfCredit},
		{http.StatusTooManyRequests, ErrRateLimited},
		{http.StatusUnprocessableEntity, ErrBadRequest},
		{http.StatusBadRequest, ErrBadRequest},
		{http.StatusGone, ErrTaskFailed},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte(`{"detail":[{"msg":"field required","type":"missing"}]}`))
		}))
		c := newTestClient(t, srv.URL)
		_, err := c.Submit(context.Background(), Request3D{FrontURL: "https://cdn.example/f.png"})
		require.ErrorIsf(t, err, tc.want, "HTTP %d", tc.code)
		// The provider's own sentence is quoted for a human, even from the list-shaped 422 body.
		require.Contains(t, err.Error(), "field required")
		srv.Close()
	}
}

// falStub serves one queue lifecycle: status, then the result, then the artifacts.
type falStub struct {
	statusAfter int    // how many status calls answer IN_PROGRESS before COMPLETED
	units       string // x-fal-billable-units on the result fetch; "" omits the header
	noModelURL  bool
	calls       int
}

func (s *falStub) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/status"):
			s.calls++
			st := StatusCompleted
			if s.calls <= s.statusAfter {
				st = StatusInProgress
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": string(st), "queue_position": 0})
		case strings.HasSuffix(r.URL.Path, "/model.glb"):
			_, _ = w.Write([]byte("glTF-bytes"))
		case strings.HasSuffix(r.URL.Path, "/thumb.png"):
			_, _ = w.Write([]byte("png-bytes"))
		case strings.Contains(r.URL.Path, "/requests/"):
			if s.units != "" {
				w.Header().Set(billableUnitsHeader, s.units)
			}
			out := map[string]any{"thumbnail": map[string]any{"url": "http://" + r.Host + "/thumb.png"}}
			if !s.noModelURL {
				out["model_mesh"] = map[string]any{
					"url": "http://" + r.Host + "/model.glb", "content_type": "model/gltf-binary",
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}
}

// TestAwaitBringsBackTHE_BYTES_AND_THE_PROVIDER_S_OWN_CHARGE.
//
// The two halves are one test because they arrive on one response: fal reports what a request cost
// in a HEADER on the result fetch, and that is the only number in the whole exchange that comes
// from the provider rather than from our configuration.
func TestAwaitBringsBackTHE_BYTES_AND_THE_PROVIDER_S_OWN_CHARGE(t *testing.T) {
	stub := &falStub{statusAfter: 2, units: "3"}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var model, thumb bytes.Buffer
	res, err := c.Await(context.Background(), "req-7", Sink{Model: &model, Thumbnail: &thumb})
	require.NoError(t, err)

	require.Equal(t, "glTF-bytes", model.String())
	require.Equal(t, "png-bytes", thumb.String())
	require.Equal(t, int64(10), res.ModelBytes)
	require.NotEmpty(t, res.ModelSHA256)
	require.Equal(t, formatGLB, res.Format)

	require.Equal(t, 3.0, res.BillableUnits)
	require.False(t, res.UnitsAssumed, "the provider named the number; nothing was assumed")
	require.True(t, c.CostUSD(res.BillableUnits).Equal(c.CostUSD(3)))
	require.Equal(t, "1.5", c.CostUSD(res.BillableUnits).String(), "3 units at FAL_UNIT_USD=0.5")

	// AND NO URL CROSSES THE PACKAGE BOUNDARY. fal's artifact links expire; a stored link is a
	// model that quietly stops existing, so the Result has nowhere to put one.
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "http", "a Result must carry bytes and money, never a link")
}

// TestAMissingBillingHeaderIsASSUMED_AND_SAYS_SO.
//
// ⚠ RECORDING NOTHING WOULD MAKE A PAID 3D BUILD READ AS FREE, which is the failure the whole
// ledger exists to prevent; recording an assumption as though it were the provider's own figure
// would be a different lie. So the number is produced AND the guess is flagged, at the one place
// the decision is made.
func TestAMissingBillingHeaderIsASSUMED_AND_SAYS_SO(t *testing.T) {
	stub := &falStub{units: ""}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	res, err := c.Await(context.Background(), "req-8", Sink{Model: &bytes.Buffer{}})
	require.NoError(t, err)
	require.Equal(t, 1.0, res.BillableUnits, "one unit per request is fal's marketplace default")
	require.True(t, res.UnitsAssumed, "the flag is what stops a guess hardening into a measurement")
	require.True(t, c.CostUSD(res.BillableUnits).IsPositive(), "«free» is the worse lie")
}

// TestA_COMPLETED_REQUEST_WITH_NO_MODEL_STILL_CARRIES_ITS_CHARGE.
//
// The most expensive line in the package: the build succeeded, the units are spent, and there is
// simply no file url to fetch it with. Dropping the charge here is exactly how paid failures came
// to be invisible to the ledger.
func TestA_COMPLETED_REQUEST_WITH_NO_MODEL_STILL_CARRIES_ITS_CHARGE(t *testing.T) {
	stub := &falStub{units: "2", noModelURL: true}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Await(context.Background(), "req-9", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrNoModel)

	units, ok := Charge(err)
	require.True(t, ok, "the provider named a charge and it must reach the ledger")
	require.Equal(t, 2.0, units)

	// AND AN UNBILLED FAILURE IS NOT DRESSED AS A BILLED ONE: zero units means «nobody could say»,
	// which is a different claim from «it was free».
	_, ok = Charge(ErrNoModel)
	require.False(t, ok)
	require.Equal(t, ErrNoModel, chargedWith(ErrNoModel, 0, "req-9"),
		"zero units means «the provider did not say» and must not be wrapped as a charge of zero")
}

// TestAnUnfinishedRequestIsNOT_READY_AND_IS_NOT_CHARGED. Await loops on this rather than ending on
// it, and a charge attached here would be re-read on every poll of the same unfinished job.
func TestAnUnfinishedRequestIsNOT_READY_AND_IS_NOT_CHARGED(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path, "/status"),
			"an unfinished request must not have its result fetched")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "IN_QUEUE", "queue_position": 4})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Collect(context.Background(), "req-10", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrNotReady)
	_, ok := Charge(err)
	require.False(t, ok)
}

// TestThePollCeilingReadsAsACeiling. «The wait ran out, look again later» and «the request failed»
// point a worker in opposite directions, and only the first is true here — the id is still worth
// something.
func TestThePollCeilingReadsAsACeiling(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "IN_PROGRESS"})
	}))
	defer srv.Close()

	c := New(Config{APIKey: "k", BaseURL: srv.URL, PollInterval: time.Millisecond, PollTimeout: 30 * time.Millisecond})
	_, err := c.Await(context.Background(), "req-11", Sink{Model: &bytes.Buffer{}})
	require.ErrorIs(t, err, ErrTimedOut)
	require.Contains(t, err.Error(), "req-11", "the id is the only thing that can find a paid job again")
}

// TestA_404_IN_THE_FIRST_SECONDS_IS_A_LAG_NOT_AN_ANSWER.
//
// The submit IS the payment, and the first lookup has no pause in front of it. Taken at face value,
// a read-after-write lag of one second would throw away a build bought a second earlier, and the
// only road back from a discarded id is a second charge.
func TestA_404_IN_THE_FIRST_SECONDS_IS_A_LAG_NOT_AN_ANSWER(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			n++
			if n < 3 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"detail":"not found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/model.glb") {
			_, _ = w.Write([]byte("glb"))
			return
		}
		w.Header().Set(billableUnitsHeader, "1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model_mesh": map[string]any{"url": "http://" + r.Host + "/model.glb"},
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	res, err := c.Await(context.Background(), "req-12", Sink{Model: &bytes.Buffer{}})
	require.NoError(t, err, "a 404 inside the grace must not discard a paid build")
	require.Equal(t, "req-12", res.RequestID)
}

// TestAnOversizedArtifactIsREFUSED_NOT_TRUNCATED. A GLB cut at the boundary is a file that opens in
// nothing and looks like a provider defect for as long as it takes somebody to compare byte counts.
func TestAnOversizedArtifactIsREFUSED_NOT_TRUNCATED(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 64)
	_, err := readCapped(bytes.NewReader(big), 16)
	require.ErrorIs(t, err, ErrTooLarge)

	var sink bytes.Buffer
	r := newCapReader(bytes.NewReader(big), 16)
	_, err = sink.ReadFrom(r)
	require.ErrorIs(t, err, ErrTooLarge)
}

// TestAReferenceTheProviderCannotFetchIsREFUSED. The provider downloads these itself, so a bucket
// key or a file:// url is a mistake worth catching here rather than as a provider failure later.
func TestAReferenceTheProviderCannotFetchIsREFUSED(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1")
	for _, bad := range []string{"design/plate.png", "file:///etc/passwd", "ftp://x/y.png", ""} {
		_, err := c.Submit(context.Background(), Request3D{FrontURL: "https://ok/f.png", BackURL: bad})
		if bad == "" {
			continue // an empty optional view is simply omitted, not refused
		}
		require.ErrorIsf(t, err, ErrBadImageURL, "reference %q", bad)
	}
}
