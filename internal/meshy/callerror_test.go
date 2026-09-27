package meshy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ B-14 — callJSON SPEAKS aiprov.CallError; ENGAGED IS A FACT OF THE CREATE-TASK POST ONLY ═══
//
// Every error leaving callJSON is a CallError around today's error (TestStatusErrorsAreClassifiedByCodeAlone
// and the money-fault probes pin the sentinels, untouched). Engaged: on the POST, true exactly when
// Meshy may have created — and will bill — the task: a break after the request was written, a 2xx
// that named no task; false for every non-2xx (D-09) and for a request that never left. On the status
// GET it is never true, and the lookup stays retryable.
//
// MUTATIONS (each measured red→green): `engaged := wroteRequest()` → `!wroteRequest()` → the
// post-write and refused-dial cases go red; the non-2xx `fail(code, status, false, …)` → `true` →
// every status row goes red; the GET Do arm → `fail(code, 0, true, …)` → the GET timeout case goes red.
//
// B-13/A1 — the create call's 5xx other than a bare 503 (and any 5xx naming a task) is ENGAGED and
// ErrSubmitUnconfirmed, as fal's is. MUTATION (measured red→green): the `fail(code, status, true,
// false, err)` of the unconfirmed arm → `false, retryable` → the 500/502/504 and the id-naming 503
// rows go red.
//
// B-13/A3 — a 408 on the create call is ENGAGED and unconfirmed too; a 408 on the status poll stays a
// free, retryable read. MUTATION (measured red→green): the `|| status == http.StatusRequestTimeout`
// dropped from the submit arm → the create-call 408 row goes red (the GET 408 row stays green).

func meshyCallError(t *testing.T, err error, what string) *aiprov.CallError {
	t.Helper()
	ce, ok := aiprov.AsCallError(err)
	if !ok {
		t.Fatalf("%s: err = %v (%T) must carry an *aiprov.CallError", what, err, err)
	}
	if ce.Provider != entity.AIProviderMeshy {
		t.Fatalf("%s: CallError.Provider = %q, want meshy", what, ce.Provider)
	}
	return ce
}

func hangAfterRead(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

func TestCallJSON_TheSubmitIsTheMoneyBoundary(t *testing.T) {
	submit := func(baseURL string, timeout time.Duration) error {
		_, err := New(Config{APIKey: "k", BaseURL: baseURL, HTTPTimeout: timeout}).Submit(context.Background(), sampleRequest())
		return err
	}

	t.Run("a deadline AFTER the create call was written is ENGAGED and final", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(hangAfterRead))
		defer srv.Close()
		ce := meshyCallError(t, submit(srv.URL, 150*time.Millisecond), "post-write timeout")
		if !ce.Engaged || ce.Retryable || ce.Code != aiprov.CodeTimeout || ce.HTTPStatus != 0 {
			t.Fatalf("CallError = {engaged %v, retryable %v, code %q, status %d}, want {true, false, timeout, 0}",
				ce.Engaged, ce.Retryable, ce.Code, ce.HTTPStatus)
		}
	})

	t.Run("a refused dial is NOT engaged and may be retried", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		deadURL := dead.URL
		dead.Close()
		ce := meshyCallError(t, submit(deadURL, time.Second), "refused dial")
		if ce.Engaged || !ce.Retryable || ce.Code != aiprov.CodeTransport {
			t.Fatalf("CallError = {engaged %v, retryable %v, code %q}, want {false, true, transport}", ce.Engaged, ce.Retryable, ce.Code)
		}
	})

	t.Run("every refusal is NOT engaged and carries its status", func(t *testing.T) {
		for _, status := range []int{400, 401, 402, 403, 404, 422, 429, 503} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeStatus(w, status, map[string]string{"message": "said no"})
			}))
			err := submit(srv.URL, time.Second)
			srv.Close()
			ce := meshyCallError(t, err, http.StatusText(status))
			code, retryable := aiprov.ClassifyStatus(status)
			if ce.Engaged || ce.HTTPStatus != status || ce.Code != code || ce.Retryable != retryable {
				t.Errorf("HTTP %d: CallError = {engaged %v, status %d, code %q, retryable %v}, want {false, %d, %q, %v}",
					status, ce.Engaged, ce.HTTPStatus, ce.Code, ce.Retryable, status, code, retryable)
			}
		}
	})

	t.Run("a 5xx other than a bare 503, or a 408, on the create call is ENGAGED and unconfirmed (B-13/A1, A3)", func(t *testing.T) {
		for _, c := range []struct {
			status int
			body   string
		}{
			{500, `{"message":"internal"}`},
			{502, `{"message":"bad gateway"}`},
			{504, `{"message":"gateway timeout"}`},
			{408, `{"message":"request timeout"}`},             // B-13/A3: the server gave up after the body
			{503, `{"result":"task-lost-1","message":"busy"}`}, // a 503 that names the task it created
		} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			}))
			err := submit(srv.URL, time.Second)
			srv.Close()
			ce := meshyCallError(t, err, http.StatusText(c.status))
			if !ce.Engaged || ce.Retryable || ce.HTTPStatus != c.status || !errors.Is(err, ErrSubmitUnconfirmed) {
				t.Errorf("HTTP %d %s: CallError = {engaged %v, retryable %v, status %d} %v, want {true, false, %d} + ErrSubmitUnconfirmed",
					c.status, c.body, ce.Engaged, ce.Retryable, ce.HTTPStatus, err, c.status)
			}
		}
		// The id rides the sentence: it is what a person reconciles by on Meshy's dashboard.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeStatus(w, http.StatusBadGateway, map[string]string{"result": "task-lost-2"})
		}))
		defer srv.Close()
		if err := submit(srv.URL, time.Second); err == nil || !strings.Contains(err.Error(), "task-lost-2") {
			t.Errorf("the named task must reach the error: %v", err)
		}
	})

	t.Run("a 2xx that names no task is ENGAGED", func(t *testing.T) {
		for name, body := range map[string]string{"no id": `{"result":""}`, "not json": `<html>`} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			err := submit(srv.URL, time.Second)
			srv.Close()
			ce := meshyCallError(t, err, name)
			if !ce.Engaged || ce.Retryable || !errors.Is(err, ErrUnexpectedResponse) {
				t.Errorf("%s: CallError = {engaged %v, retryable %v} %v, want {true, false} + ErrUnexpectedResponse",
					name, ce.Engaged, ce.Retryable, err)
			}
		}
	})
}

func TestCallJSON_ALookupIsNEVER_ENGAGED(t *testing.T) {
	get := func(baseURL string, timeout time.Duration) error {
		var out task
		return New(Config{APIKey: "k", BaseURL: baseURL, HTTPTimeout: timeout}).
			callJSON(context.Background(), http.MethodGet, multiImagePath+"/task-1", nil, &out)
	}

	t.Run("a deadline on the status poll is not engaged and stays retryable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(hangAfterRead))
		defer srv.Close()
		ce := meshyCallError(t, get(srv.URL, 150*time.Millisecond), "GET timeout")
		if ce.Engaged || !ce.Retryable || ce.Code != aiprov.CodeTimeout {
			t.Fatalf("CallError = {engaged %v, retryable %v, code %q}, want {false, true, timeout}", ce.Engaged, ce.Retryable, ce.Code)
		}
	})

	t.Run("every non-2xx and a garbled 2xx on the poll are not engaged", func(t *testing.T) {
		for _, status := range []int{401, 404, 408, 429, 500, 502, 504, 200} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `<html>`)
			}))
			err := get(srv.URL, time.Second)
			srv.Close()
			ce := meshyCallError(t, err, http.StatusText(status))
			if ce.Engaged || ce.HTTPStatus != status {
				t.Errorf("GET %d: CallError = {engaged %v, status %d}, want {false, %d}", status, ce.Engaged, ce.HTTPStatus, status)
			}
		}
	})
}
