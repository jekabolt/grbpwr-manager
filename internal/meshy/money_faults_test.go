package meshy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAnEmptyBalanceIsNamedRatherThanGuessed.
//
// 402 IS THE ONE FAULT WHERE «TRY AGAIN» IS PURE LOSS OF TIME AND OF FACE. Every other provider in
// this feature already names it, and the caller's classifier turns those sentinels into a terminal
// provider_out_of_credit. This client did not, so a drained account arrived as a plain error — and
// a plain error from a provider is weather: the whole attempt cap spent knocking on an empty till,
// and a history row that blames the provider's availability for an unpaid invoice.
//
// The distinctness assertions are the point. 402 must not fall into the generic 4xx bucket either:
// «we sent something wrong» and «there is no money» are read by different people.
func TestAnEmptyBalanceIsNamedRatherThanGuessed(t *testing.T) {
	for _, verb := range []string{"submit", "lookup"} {
		t.Run(verb, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeStatus(w, http.StatusPaymentRequired, map[string]string{"message": "insufficient credits"})
			}))
			defer srv.Close()
			c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: time.Second})

			var err error
			if verb == "submit" {
				_, err = c.Submit(context.Background(), sampleRequest())
			} else {
				var model bytes.Buffer
				_, err = c.Collect(context.Background(), fakeTaskID, Sink{Model: &model})
			}

			if !errors.Is(err, ErrOutOfCredit) {
				t.Fatalf("err = %v, want ErrOutOfCredit", err)
			}
			for name, other := range map[string]error{
				"ErrBadRequest":   ErrBadRequest,
				"ErrUnauthorized": ErrUnauthorized,
				"ErrRateLimited":  ErrRateLimited,
				"ErrTaskNotFound": ErrTaskNotFound,
			} {
				if errors.Is(err, other) {
					t.Errorf("an empty balance must not also read as %s: %v", name, err)
				}
			}
			if !strings.Contains(err.Error(), "insufficient credits") {
				t.Errorf("the provider's own sentence must survive into %q", err)
			}
		})
	}
}

// lagFake answers the retrieve endpoint with 404 for the first `missing` lookups and serves a
// finished task afterwards. It is the crude local model of a read path that has not yet caught up
// with the write that created the task.
type lagFake struct {
	mu      sync.Mutex
	missing int
	posts   int
	gets    int
	srv     *httptest.Server
}

func newLagFake(t *testing.T, missing int) *lagFake {
	t.Helper()
	f := &lagFake{missing: missing}
	mux := http.NewServeMux()
	mux.HandleFunc(multiImagePath, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.posts++
		f.mu.Unlock()
		writeJSON(w, map[string]string{"result": fakeTaskID})
	})
	mux.HandleFunc(multiImagePath+"/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.gets++
		still := f.gets <= f.missing
		f.mu.Unlock()
		if still {
			writeStatus(w, http.StatusNotFound, map[string]string{"message": "task not found"})
			return
		}
		writeJSON(w, map[string]any{
			"id":               fakeTaskID,
			"status":           string(StatusSucceeded),
			"progress":         100,
			"consumed_credits": 30,
			"model_urls":       map[string]string{"glb": f.srv.URL + "/assets/model.glb"},
		})
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fakeModelBody))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *lagFake) client(mut ...func(*Config)) *Client {
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

// TestAPaidTaskSurvivesALagOnTheFirstLookup.
//
// THE FIRST LOOKUP HAS NO PAUSE IN FRONT OF IT: Generate submits — which is where the money goes —
// and polls in the same breath. ErrTaskNotFound is terminal, so read at face value on that first
// poll, a read-after-write lag of a fraction of a second closes a task bought a fraction of a
// second earlier, and the only route back to a discarded id is a SECOND CHARGE.
func TestAPaidTaskSurvivesALagOnTheFirstLookup(t *testing.T) {
	f := newLagFake(t, 3)
	var model bytes.Buffer

	res, err := f.client().Generate(context.Background(), sampleRequest(), Sink{Model: &model})
	if err != nil {
		t.Fatalf("a lag on the retrieve path must not discard a paid task: %v", err)
	}
	if res.TaskID != fakeTaskID || model.String() != fakeModelBody {
		t.Fatalf("the model did not arrive: id=%q %d bytes", res.TaskID, model.Len())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 {
		t.Errorf("a lag must never be paid for twice: %d submits", f.posts)
	}
	if f.gets <= f.missing {
		t.Errorf("the grace must have kept polling, got %d lookups", f.gets)
	}
}

// TestAnUnknownTaskIsStillTerminalAfterTheGrace is the other half, and the half that makes the
// grace a delay rather than a repeal. An id the provider never knows must still come back as
// ErrTaskNotFound — NOT as ErrTimedOut, which tells a worker the opposite thing: that the task is
// probably alive and worth collecting later.
func TestAnUnknownTaskIsStillTerminalAfterTheGrace(t *testing.T) {
	f := newLagFake(t, 1<<30) // never found
	var model bytes.Buffer

	// A ceiling of 200ms caps the grace at 100ms: the grace may never eat more than half the wait,
	// or a genuinely unknown id would surface as a timeout instead of as itself.
	c := f.client(func(cfg *Config) { cfg.PollTimeout = 200 * time.Millisecond })
	_, err := c.Await(context.Background(), fakeTaskID, Sink{Model: &model})

	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("err = %v, want ErrTaskNotFound", err)
	}
	if errors.Is(err, ErrTimedOut) {
		t.Errorf("an unknown id is not a wait that ran out: %v", err)
	}
}

// TestOneLookupStillAnswersImmediately: the grace belongs to Await, which is a loop and can afford
// to wait. Collect performs ONE lookup by contract, and a caller that asked for one answer gets the
// answer, not a softened one.
func TestOneLookupStillAnswersImmediately(t *testing.T) {
	f := newLagFake(t, 1<<30)
	var model bytes.Buffer
	start := time.Now()
	if _, err := f.client().Collect(context.Background(), fakeTaskID, Sink{Model: &model}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("err = %v, want ErrTaskNotFound", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Collect must not poll: took %s", elapsed)
	}
}
