package meshy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// A fake Meshy, close enough to the real one to be worth trusting.
//
// It logs every request in order (calls), so a test can assert not only THAT the artifact was
// fetched but WHEN — inside the same Collect that learned its url. And it serves each artifact
// exactly once, answering 410 Gone afterwards: the crude local model of a link that expires, and
// the reason a caller that stored the url instead of the bytes would fail here.
// ---------------------------------------------------------------------------

const (
	fakeTaskID    = "0198c0de-0000-7000-8000-000000000001"
	fakeModelBody = "glTF\x02\x00\x00\x00 pretend this is a binary gltf payload"
	fakeThumbBody = "\x89PNG\r\n\x1a\n pretend this is a thumbnail"
)

type fakeProvider struct {
	t *testing.T

	mu       sync.Mutex
	calls    []string          // ordered log: "POST /openapi/v1/multi-image-to-3d", "GET /assets/model.glb", ...
	assetGET map[string]int    // artifact path -> times fetched
	authSeen map[string]string // request path -> Authorization header as received

	submitted submitBody // the decoded create-task payload

	statuses        []Status // consumed one per lookup; the last one repeats forever
	lookups         int
	consumedCredits int
	taskErrMessage  string
	withoutGLB      bool
	withoutThumb    bool
	assetDelay      time.Duration
	assetStatus     int // non-zero => the artifact host answers with this instead of the bytes

	srv *httptest.Server
}

func newFake(t *testing.T, statuses ...Status) *fakeProvider {
	t.Helper()
	f := &fakeProvider{
		t:               t,
		assetGET:        map[string]int{},
		authSeen:        map[string]string{},
		statuses:        statuses,
		consumedCredits: 30,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(multiImagePath, f.handleSubmit)
	mux.HandleFunc(multiImagePath+"/", f.handleLookup)
	mux.HandleFunc("/assets/", f.handleAsset)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) note(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.authSeen[r.URL.Path] = r.Header.Get("Authorization")
}

func (f *fakeProvider) handleSubmit(w http.ResponseWriter, r *http.Request) {
	f.note(r)
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	f.mu.Lock()
	if err := json.NewDecoder(r.Body).Decode(&f.submitted); err != nil {
		f.mu.Unlock()
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	f.mu.Unlock()
	writeJSON(w, map[string]string{"result": fakeTaskID})
}

func (f *fakeProvider) handleLookup(w http.ResponseWriter, r *http.Request) {
	f.note(r)
	if id := strings.TrimPrefix(r.URL.Path, multiImagePath+"/"); id != fakeTaskID {
		writeStatus(w, http.StatusNotFound, map[string]string{"message": "task not found"})
		return
	}

	f.mu.Lock()
	status := StatusSucceeded
	if len(f.statuses) > 0 {
		idx := f.lookups
		if idx >= len(f.statuses) {
			idx = len(f.statuses) - 1
		}
		status = f.statuses[idx]
	}
	f.lookups++
	body := map[string]any{
		"id":               fakeTaskID,
		"status":           string(status),
		"progress":         50,
		"consumed_credits": f.consumedCredits,
	}
	switch status {
	case StatusSucceeded:
		urls := map[string]string{}
		if !f.withoutGLB {
			urls["glb"] = f.srv.URL + "/assets/model.glb"
		}
		body["model_urls"] = urls
		body["progress"] = 100
		if !f.withoutThumb {
			body["thumbnail_url"] = f.srv.URL + "/assets/thumb.png"
		}
	case StatusFailed, StatusCanceled:
		body["task_error"] = map[string]string{"message": f.taskErrMessage}
	}
	f.mu.Unlock()
	writeJSON(w, body)
}

func (f *fakeProvider) handleAsset(w http.ResponseWriter, r *http.Request) {
	f.note(r)
	f.mu.Lock()
	f.assetGET[r.URL.Path]++
	served := f.assetGET[r.URL.Path]
	delay, status := f.assetDelay, f.assetStatus
	f.mu.Unlock()

	// Serve once. A second fetch of the same artifact is what a caller who kept the url instead of
	// the bytes would eventually attempt, and in three days that is exactly what it gets.
	if served > 1 {
		http.Error(w, "gone", http.StatusGone)
		return
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	if status != 0 {
		writeStatus(w, status, map[string]string{"message": "the artifact host is unwell"})
		return
	}
	switch r.URL.Path {
	case "/assets/model.glb":
		_, _ = io.WriteString(w, fakeModelBody)
	case "/assets/thumb.png":
		_, _ = io.WriteString(w, fakeThumbBody)
	default:
		http.Error(w, "no such asset", http.StatusNotFound)
	}
}

func (f *fakeProvider) snapshotCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func writeJSON(w http.ResponseWriter, v any) { writeStatus(w, http.StatusOK, v) }

func writeStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// client builds a client pointed at the fake, with a polling shape sized for a test rather than
// for a provider that takes minutes.
func (f *fakeProvider) client(mut ...func(*Config)) *Client {
	cfg := Config{
		APIKey:       "test-key",
		BaseURL:      f.srv.URL,
		HTTPTimeout:  2 * time.Second,
		PollInterval: 5 * time.Millisecond,
		PollTimeout:  2 * time.Second,
	}
	for _, m := range mut {
		m(&cfg)
	}
	return New(cfg)
}

func sampleRequest() Request {
	return Request{ImageURLs: []string{
		"https://files.grbpwr.com/design/front.png",
		"https://files.grbpwr.com/design/back.png",
		"https://files.grbpwr.com/design/side_l.png",
	}}
}

// ---------------------------------------------------------------------------
// The happy cycle
// ---------------------------------------------------------------------------

// TestGenerateSubmitsPollsAndDownloads walks the whole contract in one pass: the task is created,
// polled while it runs, and its bytes arrive in the sink — with the ORDER of the provider's
// requests asserted, because the order is the proof that the download happened inside the call
// that learned the url.
func TestGenerateSubmitsPollsAndDownloads(t *testing.T) {
	f := newFake(t, StatusPending, StatusInProgress, StatusSucceeded)
	var model, thumb bytes.Buffer

	res, err := f.client().Generate(context.Background(), sampleRequest(), Sink{Model: &model, Thumbnail: &thumb})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if res.TaskID != fakeTaskID {
		t.Errorf("task id = %q, want %q", res.TaskID, fakeTaskID)
	}
	if res.Format != formatGLB {
		t.Errorf("format = %q, want %q", res.Format, formatGLB)
	}
	if got := model.String(); got != fakeModelBody {
		t.Errorf("model bytes = %q, want %q", got, fakeModelBody)
	}
	if got := thumb.String(); got != fakeThumbBody {
		t.Errorf("thumbnail bytes = %q, want %q", got, fakeThumbBody)
	}
	if res.ModelBytes != int64(len(fakeModelBody)) {
		t.Errorf("ModelBytes = %d, want %d", res.ModelBytes, len(fakeModelBody))
	}
	if res.ThumbnailBytes != int64(len(fakeThumbBody)) {
		t.Errorf("ThumbnailBytes = %d, want %d", res.ThumbnailBytes, len(fakeThumbBody))
	}
	sum := sha256.Sum256([]byte(fakeModelBody))
	if res.ModelSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("ModelSHA256 = %q, want %q", res.ModelSHA256, hex.EncodeToString(sum[:]))
	}
	if res.ConsumedCredits != 30 {
		t.Errorf("ConsumedCredits = %d, want 30", res.ConsumedCredits)
	}

	// The order IS the assertion: three lookups, and the artifact fetches immediately after the
	// third — inside the same Collect, with no return to the caller in between.
	want := []string{
		"POST " + multiImagePath,
		"GET " + multiImagePath + "/" + fakeTaskID,
		"GET " + multiImagePath + "/" + fakeTaskID,
		"GET " + multiImagePath + "/" + fakeTaskID,
		"GET /assets/model.glb",
		"GET /assets/thumb.png",
	}
	if got := f.snapshotCalls(); !reflect.DeepEqual(got, want) {
		t.Errorf("provider call sequence\n got: %v\nwant: %v", got, want)
	}
}

// TestSubmitAsksForGLBAndKeepsTheViewOrder pins the two things about the request that are product
// decisions rather than defaults: the browser needs GLB and nothing else, and image_urls[0] is the
// front view — a reordering here would silently model the garment back-to-front.
func TestSubmitAsksForGLBAndKeepsTheViewOrder(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	req := sampleRequest()

	if _, err := f.client().Submit(context.Background(), req); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	f.mu.Lock()
	got := f.submitted
	auth := f.authSeen[multiImagePath]
	f.mu.Unlock()

	if !reflect.DeepEqual(got.TargetFormats, []string{"glb"}) {
		t.Errorf("target_formats = %v, want [glb] — the band renders glTF-binary and nothing else", got.TargetFormats)
	}
	if !reflect.DeepEqual(got.ImageURLs, req.ImageURLs) {
		t.Errorf("image_urls = %v, want %v (order is meaning: [0] is the front view)", got.ImageURLs, req.ImageURLs)
	}
	if !got.ShouldTexture {
		t.Error("should_texture must be true: an untextured mesh answers a different question")
	}
	if got.EnablePBR {
		t.Error("enable_pbr must stay false: PBR maps quadruple the download for nuance a tile does not show")
	}
	if got.AIModel != "" {
		t.Errorf("ai_model = %q, want empty: no baked-in provider slug (see doc.go)", got.AIModel)
	}
	if auth != "Bearer test-key" {
		t.Errorf("Authorization on the control plane = %q, want %q", auth, "Bearer test-key")
	}
}

// ---------------------------------------------------------------------------
// The trap this package exists for: expiring links
// ---------------------------------------------------------------------------

// TestResultCarriesNoExpiringURL is a guard on the TYPE, not on a code path. Meshy's artifact links
// die after three days, so the safest design is one where a caller cannot store one because it is
// never handed one. This test walks the fields Result exposes and fails if a url ever appears among
// them — which is the shape the next well-meaning "let's also return the link for debugging" change
// would take.
func TestResultCarriesNoExpiringURL(t *testing.T) {
	rt := reflect.TypeOf(Result{})
	for i := range rt.NumField() {
		name := strings.ToLower(rt.Field(i).Name)
		if strings.Contains(name, "url") || strings.Contains(name, "uri") || strings.Contains(name, "link") {
			t.Errorf("Result.%s: this package must not hand back a provider link — it expires in "+
				"three days, and a stored one looks delivered until the week is out", rt.Field(i).Name)
		}
	}
}

// TestCollectFetchesTheBytesBeforeItReturns is the behavioural half of the same claim. The fake
// serves each artifact exactly once and 410s afterwards, so a caller that had merely been handed a
// url would have nothing. Here the bytes are already in the sink when Collect returns, and the
// second fetch — the one a link-storing caller would eventually make — is proven to be gone.
func TestCollectFetchesTheBytesBeforeItReturns(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	c := f.client()

	var model bytes.Buffer
	res, err := c.Collect(context.Background(), fakeTaskID, Sink{Model: &model})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if model.String() != fakeModelBody {
		t.Fatalf("the sink holds %q, want the model bytes", model.String())
	}
	if res.ModelBytes == 0 {
		t.Fatal("ModelBytes = 0: nothing was actually transferred")
	}

	// Second look at the same finished task: the status still says SUCCEEDED and the url is still
	// in the JSON, but the artifact behind it is gone. That is precisely the state a persisted link
	// reaches on day four.
	var again bytes.Buffer
	if _, err := c.Collect(context.Background(), fakeTaskID, Sink{Model: &again}); err == nil {
		t.Fatal("a re-fetch of an expired artifact must fail, otherwise this test proves nothing")
	}
}

// TestAwaitCeilingDoesNotCutTheDownload guards the most expensive edge in the package. The poll
// ceiling here (80 ms) elapses long before the artifact finishes arriving (300 ms). If the download
// ran under the ceiling's deadline, this would fail — the credits spent, the model built, and
// nothing to show for it but a link with a three-day fuse.
func TestAwaitCeilingDoesNotCutTheDownload(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	f.assetDelay = 300 * time.Millisecond

	var model bytes.Buffer
	res, err := f.client(func(cfg *Config) {
		cfg.PollTimeout = 80 * time.Millisecond
		cfg.PollInterval = 5 * time.Millisecond
	}).Await(context.Background(), fakeTaskID, Sink{Model: &model})
	if err != nil {
		t.Fatalf("Await: the poll ceiling must bound the WAIT, never the fetch: %v", err)
	}
	if res.ModelBytes != int64(len(fakeModelBody)) {
		t.Errorf("ModelBytes = %d, want %d", res.ModelBytes, len(fakeModelBody))
	}
}

// TestFetchSendsNoAPIKeyToTheArtifactHost: the artifact url comes out of the provider's JSON and
// names whatever host the provider likes. Attaching our key to a request at an address we did not
// choose would hand the key to that host.
func TestFetchSendsNoAPIKeyToTheArtifactHost(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	var model bytes.Buffer
	if _, err := f.client().Collect(context.Background(), fakeTaskID, Sink{Model: &model}); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	f.mu.Lock()
	auth := f.authSeen["/assets/model.glb"]
	f.mu.Unlock()
	if auth != "" {
		t.Errorf("Authorization sent to the artifact host = %q, want none", auth)
	}
}

// ---------------------------------------------------------------------------
// The ways it goes wrong
// ---------------------------------------------------------------------------

// TestGenerateProviderFailedTask: the provider ends the task itself. Terminal, with the provider's
// own sentence carried through so an operator does not have to open a dashboard to learn why.
func TestGenerateProviderFailedTask(t *testing.T) {
	f := newFake(t, StatusInProgress, StatusFailed)
	f.taskErrMessage = "input images are inconsistent"

	var model bytes.Buffer
	_, err := f.client().Generate(context.Background(), sampleRequest(), Sink{Model: &model})
	if !errors.Is(err, ErrTaskFailed) {
		t.Fatalf("err = %v, want ErrTaskFailed", err)
	}
	for _, want := range []string{fakeTaskID, "input images are inconsistent"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must mention %q", err, want)
		}
	}
	if model.Len() != 0 {
		t.Error("nothing must be written to the sink for a failed task")
	}
}

// TestGenerateCanceledTask — CANCELED is the same shape of fact as FAILED and must not read as
// "still running", which would keep a worker polling a task that will never move.
func TestGenerateCanceledTask(t *testing.T) {
	f := newFake(t, StatusCanceled)
	var model bytes.Buffer
	_, err := f.client().Generate(context.Background(), sampleRequest(), Sink{Model: &model})
	if !errors.Is(err, ErrTaskFailed) {
		t.Fatalf("err = %v, want ErrTaskFailed", err)
	}
}

// TestAwaitCeilingExpires: the task never finishes. The answer must be ErrTimedOut and must carry
// the task id — the task is probably still alive at the provider, and that id is the only way back
// to it while the links live.
func TestAwaitCeilingExpires(t *testing.T) {
	f := newFake(t, StatusInProgress)

	start := time.Now()
	var model bytes.Buffer
	_, err := f.client(func(cfg *Config) {
		cfg.PollInterval = 5 * time.Millisecond
		cfg.PollTimeout = 60 * time.Millisecond
	}).Await(context.Background(), fakeTaskID, Sink{Model: &model})

	if !errors.Is(err, ErrTimedOut) {
		t.Fatalf("err = %v, want ErrTimedOut", err)
	}
	if !strings.Contains(err.Error(), fakeTaskID) {
		t.Errorf("error %q must name the task, or a paid task becomes unfindable", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("waited %s: the ceiling did not bound the wait", elapsed)
	}
	if n := len(f.snapshotCalls()); n < 2 {
		t.Errorf("only %d lookups before the ceiling: polling did not actually poll", n)
	}
}

// TestAwaitHonoursContextCancellation: a cancelled run must stop waiting at once, and the error
// must be the caller's cancellation rather than our ceiling — the two mean different things to a
// worker deciding whether to requeue.
func TestAwaitHonoursContextCancellation(t *testing.T) {
	f := newFake(t, StatusInProgress)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)

	start := time.Now()
	var model bytes.Buffer
	_, err := f.client(func(cfg *Config) {
		cfg.PollInterval = 5 * time.Millisecond
		cfg.PollTimeout = time.Hour // the ceiling must not be what stops this
	}).Await(ctx, fakeTaskID, Sink{Model: &model})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrTimedOut) {
		t.Error("a cancelled wait must not be reported as our poll ceiling")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("returned after %s: cancellation was not observed promptly", elapsed)
	}
}

// TestGenerateWithoutAPIKey: the whole surface refuses before it opens a socket. That refusal is
// what lets StartRun answer "3D is not configured" instead of queuing a run nobody can execute.
func TestGenerateWithoutAPIKey(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	c := New(Config{BaseURL: f.srv.URL}) // no APIKey

	if c.Enabled() {
		t.Fatal("a client with no key must not report itself enabled")
	}
	var model bytes.Buffer
	if _, err := c.Generate(context.Background(), sampleRequest(), Sink{Model: &model}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Generate err = %v, want ErrNotConfigured", err)
	}
	if _, err := c.Submit(context.Background(), sampleRequest()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Submit err = %v, want ErrNotConfigured", err)
	}
	if _, err := c.Collect(context.Background(), fakeTaskID, Sink{Model: &model}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Collect err = %v, want ErrNotConfigured", err)
	}
	if _, err := c.Await(context.Background(), fakeTaskID, Sink{Model: &model}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Await err = %v, want ErrNotConfigured", err)
	}
	if calls := f.snapshotCalls(); len(calls) != 0 {
		t.Errorf("an unconfigured client must not call the provider, got %v", calls)
	}
	// Nil is a valid, permanently disabled client: callers need not nil-check.
	var nilClient *Client
	if nilClient.Enabled() {
		t.Error("a nil *Client must report itself disabled")
	}
}

// TestCollectNotReady: while the task runs, Collect says so with a distinguishable sentinel — the
// signal a worker turns into "look again later", never into "submit again" (which is a second
// charge).
func TestCollectNotReady(t *testing.T) {
	f := newFake(t, StatusInProgress)
	var model bytes.Buffer
	_, err := f.client().Collect(context.Background(), fakeTaskID, Sink{Model: &model})
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if model.Len() != 0 {
		t.Error("nothing may be written for an unfinished task")
	}
}

// TestCollectSucceededWithoutGLB: we ask for one format, so a finished task without it is a broken
// answer. Falling back to fbx would put a file in the bucket that the band cannot render.
func TestCollectSucceededWithoutGLB(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	f.withoutGLB = true
	var model bytes.Buffer
	if _, err := f.client().Collect(context.Background(), fakeTaskID, Sink{Model: &model}); !errors.Is(err, ErrNoGLB) {
		t.Fatalf("err = %v, want ErrNoGLB", err)
	}
}

// TestCollectSurvivesAMissingThumbnail: the model is what was paid for. A courtesy image that does
// not arrive costs a tile, not a run.
func TestCollectSurvivesAMissingThumbnail(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	f.withoutThumb = true
	var model, thumb bytes.Buffer
	res, err := f.client().Collect(context.Background(), fakeTaskID, Sink{Model: &model, Thumbnail: &thumb})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.ModelBytes == 0 || res.ThumbnailBytes != 0 {
		t.Errorf("ModelBytes=%d ThumbnailBytes=%d, want the model delivered and the thumbnail simply absent",
			res.ModelBytes, res.ThumbnailBytes)
	}
}

// TestStatusErrorsAreClassifiedByCodeAlone: a rejected key and a rate limit are opposite
// instructions — fix a setting versus wait a minute — so they must not both arrive as "provider
// error". Classification is by status code, never by the provider's English sentence, so a
// reworded message cannot silently reclassify a fault.
func TestStatusErrorsAreClassifiedByCodeAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		want error
	}{
		{"unauthorized", http.StatusUnauthorized, ErrUnauthorized},
		{"forbidden", http.StatusForbidden, ErrUnauthorized},
		{"rate limited", http.StatusTooManyRequests, ErrRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeStatus(w, tc.code, map[string]string{"message": "provider says something"})
			}))
			defer srv.Close()

			c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second})
			_, err := c.Submit(context.Background(), sampleRequest())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), "provider says something") {
				t.Errorf("the provider's own sentence must survive into %q", err)
			}
		})
	}
}

// TestLookupOfAnUnknownTask: 404 on a lookup means the id in our attempt row buys nothing. It is
// terminal and distinguishable, because the only way forward from it is a new — paid — submit.
func TestLookupOfAnUnknownTask(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	var model bytes.Buffer
	_, err := f.client().Collect(context.Background(), "no-such-task", Sink{Model: &model})
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("err = %v, want ErrTaskNotFound", err)
	}
}

// TestSubmitRejectsImpossibleRequestsLocally: the image count and the shape of a reference are
// things we can be certain about without paying for a round trip — and a bucket key or a relative
// path here would surface minutes later as a provider-side failure with a worse message.
func TestSubmitRejectsImpossibleRequestsLocally(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	c := f.client()

	for _, tc := range []struct {
		name string
		req  Request
		want error
	}{
		{"no images", Request{}, ErrImageCount},
		{"five images", Request{ImageURLs: []string{"https://a/1", "https://a/2", "https://a/3", "https://a/4", "https://a/5"}}, ErrImageCount},
		{"bucket key", Request{ImageURLs: []string{"design/front.png"}}, ErrBadImageURL},
		{"local file", Request{ImageURLs: []string{"file:///etc/passwd"}}, ErrBadImageURL},
		{"blank", Request{ImageURLs: []string{"   "}}, ErrBadImageURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Submit(context.Background(), tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// A data: uri is a legitimate reference — the provider accepts inline images.
	if _, err := c.Submit(context.Background(), Request{ImageURLs: []string{"data:image/png;base64,iVBORw0KGgo="}}); err != nil {
		t.Errorf("a data: uri must be accepted: %v", err)
	}
	if calls := f.snapshotCalls(); len(calls) != 1 {
		t.Errorf("only the valid submit may reach the provider, got %v", calls)
	}
}

// TestCollectRefusesASinklessCall: without somewhere to put the model there is no point learning a
// url we are forbidden to keep.
func TestCollectRefusesASinklessCall(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	if _, err := f.client().Collect(context.Background(), fakeTaskID, Sink{}); err == nil {
		t.Fatal("Collect with no Sink.Model must fail")
	}
	if calls := f.snapshotCalls(); len(calls) != 0 {
		t.Errorf("the check must happen before the lookup, got %v", calls)
	}
}

// TestUnreadableAnswersAreLoud: a 200 with a body this client cannot read must not pass for an
// empty-but-fine answer. Silence there would read as "still pending" forever.
func TestUnreadableAnswersAreLoud(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"not json", "<html>maintenance</html>"},
		{"no task id", `{"result":""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second})
			if _, err := c.Submit(context.Background(), sampleRequest()); !errors.Is(err, ErrUnexpectedResponse) {
				t.Fatalf("err = %v, want ErrUnexpectedResponse", err)
			}
		})
	}

	// A finished-looking task with no status at all is the same class of fault on the read path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": fakeTaskID})
	}))
	defer srv.Close()
	var model bytes.Buffer
	c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second})
	if _, err := c.Collect(context.Background(), fakeTaskID, Sink{Model: &model}); !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("err = %v, want ErrUnexpectedResponse", err)
	}
}

// TestCapReaderRefusesInsteadOfTruncating: io.LimitReader would hand back a clean EOF at the
// boundary, and a GLB truncated "successfully" is a file that opens in nothing while every counter
// says the run went fine.
func TestCapReaderRefusesInsteadOfTruncating(t *testing.T) {
	_, err := io.Copy(io.Discard, newCapReader(strings.NewReader("0123456789"), 4))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if _, err := readCapped(strings.NewReader("0123456789"), 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("readCapped err = %v, want ErrTooLarge", err)
	}
	// Exactly at the limit is fine — the refusal starts one byte later.
	if _, err := readCapped(strings.NewReader("0123"), 4); err != nil {
		t.Fatalf("a body exactly at the limit must be accepted: %v", err)
	}
}

// TestDefaultsAreApplied pins the polling shape a worker sizes its lease against, and the fallback
// credit rate. An unset MESHY_CREDIT_USD must produce a plausible cost rather than zero: "this run
// was free" is a worse lie than an estimate.
func TestDefaultsAreApplied(t *testing.T) {
	c := New(Config{APIKey: "k"})
	if c.PollInterval() != defaultPollInterval || c.PollTimeout() != defaultPollTimeout {
		t.Errorf("poll shape = %s/%s, want %s/%s", c.PollInterval(), c.PollTimeout(), defaultPollInterval, defaultPollTimeout)
	}
	if got := c.CostUSD(30).String(); got != "0.6" {
		t.Errorf("CostUSD(30) = %s, want 0.6 at the default rate", got)
	}
	if got := New(Config{APIKey: "k", CreditUSD: 0.03}).CostUSD(10).String(); got != "0.3" {
		t.Errorf("CostUSD(10) at 0.03/credit = %s, want 0.3", got)
	}
	if got := c.CostUSD(0); !got.IsZero() {
		t.Errorf("CostUSD(0) = %s, want zero", got)
	}
	// A trailing slash on the base url must not produce a doubled path separator.
	if got := New(Config{APIKey: "k", BaseURL: "https://api.example.com/"}).cfg.BaseURL; got != "https://api.example.com" {
		t.Errorf("BaseURL = %q, want the trailing slash trimmed", got)
	}
}

// TestAwaitDoesNotRelabelADownloadFailureAsATimeout is the sharp edge of the split budget. The
// download outlives the poll ceiling by design, so by the time a fetch fails the ceiling has
// usually passed — and reporting that as ErrTimedOut would tell a worker "the task is still
// running, look again later" about a task that is finished and whose artifact would not come down.
// The two verdicts send it in opposite directions; only the specific one is true.
func TestAwaitDoesNotRelabelADownloadFailureAsATimeout(t *testing.T) {
	f := newFake(t, StatusSucceeded)
	f.assetDelay = 120 * time.Millisecond // the ceiling passes while the fetch is in flight
	f.assetStatus = http.StatusInternalServerError

	var model bytes.Buffer
	_, err := f.client(func(cfg *Config) {
		cfg.PollTimeout = 40 * time.Millisecond
		cfg.PollInterval = 5 * time.Millisecond
	}).Await(context.Background(), fakeTaskID, Sink{Model: &model})

	if err == nil {
		t.Fatal("a failing artifact host must be an error")
	}
	if errors.Is(err, ErrTimedOut) {
		t.Errorf("a download failure was relabelled as the poll ceiling: %v", err)
	}
	if !strings.Contains(err.Error(), "downloading the model") || !strings.Contains(err.Error(), "500") {
		t.Errorf("the error must say what actually failed, got %q", err)
	}
}

// TestConfigRedactsTheAPIKey: the config is a struct that gets printed — into a log line, into an
// error, into a test failure. Whether the provider is configured at all stays visible, because
// hiding that turns a redaction into a second mystery.
func TestConfigRedactsTheAPIKey(t *testing.T) {
	printed := fmt.Sprintf("%v %+v %s",
		Config{APIKey: "msy-super-secret"},
		Config{APIKey: "msy-super-secret"},
		Config{APIKey: "msy-super-secret"})
	if strings.Contains(printed, "msy-super-secret") {
		t.Fatalf("the api key survived printing: %s", printed)
	}
	if !strings.Contains(printed, "REDACTED") {
		t.Errorf("a configured key must still be visible AS configured: %s", printed)
	}
	if got := (Config{BaseURL: "https://x"}).String(); strings.Contains(got, "REDACTED") {
		t.Errorf("an unset key must read as unset, not as redacted: %s", got)
	}
}

// TestNilClientIsUsableAsADisabledOne: callers hold a *Client that may never have been built,
// and asking it about its polling shape must not be a panic.
func TestNilClientIsUsableAsADisabledOne(t *testing.T) {
	var c *Client
	if c.PollInterval() != defaultPollInterval || c.PollTimeout() != defaultPollTimeout {
		t.Error("a nil client must answer with the defaults rather than panic")
	}
	if !c.CostUSD(10).IsZero() {
		t.Error("a nil client prices nothing")
	}
}
