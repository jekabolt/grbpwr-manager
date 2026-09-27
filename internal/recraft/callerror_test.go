package recraft

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

// ═══ B-14 — THE DIRECT ROUTE SPEAKS aiprov.CallError, AND THE WRITE IS ITS MONEY BOUNDARY ═══
//
// Every error of DirectClient.GenerateImage is a CallError (Provider recraft, this route's billing key)
// around today's error — TestDirect_StatusClassification and its neighbours pin the sentinels,
// untouched. What these probes pin is Engaged: a transport failure keeps ErrProviderFailure as its
// sentinel («we do not know whether it was billed» is still the sentence), but the CallError now knows
// the side of the write; every non-2xx is not engaged; every 2xx that did not become a picture is.
//
// MUTATIONS (each measured red→green): `engaged := wroteRequest()` → `!wroteRequest()` → the
// post-write and refused-dial cases go red; the non-2xx `fail(code, status, false, …)` → `true` →
// every status row goes red; the status check moved back below the `truncated` refusal → the oversized
// 404 reads ErrInvalidResponse / engaged.
//
// B-13/A3 — a 408 on this paid POST is ENGAGED and final (the vector may have been generated and
// billed before the server gave up). MUTATION (measured red→green): the 408 arm's `fail(code, status,
// true, false, …)` → `false, retryable` → the 408 case goes red.

func directCallError(t *testing.T, err error, what string) *aiprov.CallError {
	t.Helper()
	ce, ok := aiprov.AsCallError(err)
	if !ok {
		t.Fatalf("%s: err = %v (%T) must carry an *aiprov.CallError", what, err, err)
	}
	if ce.Provider != entity.AIProviderRecraft {
		t.Fatalf("%s: CallError.Provider = %q, want recraft — the direct route bills Recraft's account", what, ce.Provider)
	}
	return ce
}

func directCall(baseURL string, timeout time.Duration) error {
	_, err := NewDirect(DirectConfig{APIKey: "k", BaseURL: baseURL, HTTPTimeout: timeout}).GenerateImage(context.Background(),
		GenerateRequest{Model: "recraftv4_vector", Prompt: "p", Image: ImageInput{URL: "https://media.grbpwr.com/flat.png"}})
	return err
}

func TestDirect_TheWriteIsTheMoneyBoundary(t *testing.T) {
	t.Run("a deadline AFTER the request was written is ENGAGED and final", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}))
		defer srv.Close()
		err := directCall(srv.URL, 150*time.Millisecond)
		ce := directCallError(t, err, "post-write timeout")
		if !ce.Engaged || ce.Retryable || ce.Code != aiprov.CodeTimeout || ce.HTTPStatus != 0 {
			t.Fatalf("CallError = {engaged %v, retryable %v, code %q, status %d}, want {true, false, timeout, 0}",
				ce.Engaged, ce.Retryable, ce.Code, ce.HTTPStatus)
		}
		if !errors.Is(err, ErrProviderFailure) {
			t.Fatalf("the sentinel stays ErrProviderFailure: %v", err)
		}
	})

	t.Run("a refused dial is NOT engaged and may be retried", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		deadURL := dead.URL
		dead.Close()
		err := directCall(deadURL, time.Second)
		ce := directCallError(t, err, "refused dial")
		if ce.Engaged || !ce.Retryable || ce.Code != aiprov.CodeTransport || !errors.Is(err, ErrProviderFailure) {
			t.Fatalf("CallError = {engaged %v, retryable %v, code %q} %v, want {false, true, transport} + ErrProviderFailure",
				ce.Engaged, ce.Retryable, ce.Code, err)
		}
	})

	t.Run("a 408 is ENGAGED and never retried: the vector may exist on the far side (B-13/A3)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusRequestTimeout)
			_, _ = io.WriteString(w, `{"message":"gave up"}`)
		}))
		defer srv.Close()
		err := directCall(srv.URL, time.Second)
		ce := directCallError(t, err, "408")
		code, _ := aiprov.ClassifyStatus(http.StatusRequestTimeout)
		if !ce.Engaged || ce.Retryable || ce.HTTPStatus != http.StatusRequestTimeout || ce.Code != code {
			t.Fatalf("CallError = {engaged %v, retryable %v, status %d, code %q}, want {true, false, 408, %q}",
				ce.Engaged, ce.Retryable, ce.HTTPStatus, ce.Code, code)
		}
		if !errors.Is(err, ErrBadRequest) || !strings.Contains(err.Error(), "gave up") {
			t.Errorf("today's sentinel and the provider's words ride inside: %v", err)
		}
	})

	t.Run("every other non-2xx is NOT engaged and carries its status", func(t *testing.T) {
		for _, status := range []int{400, 401, 402, 403, 404, 422, 429, 500, 502, 503, 504} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"said no"}`)
			}))
			err := directCall(srv.URL, time.Second)
			srv.Close()
			ce := directCallError(t, err, http.StatusText(status))
			code, retryable := aiprov.ClassifyStatus(status)
			if ce.Engaged || ce.HTTPStatus != status || ce.Code != code || ce.Retryable != retryable {
				t.Errorf("HTTP %d: CallError = {engaged %v, status %d, code %q, retryable %v}, want {false, %d, %q, %v}",
					status, ce.Engaged, ce.HTTPStatus, ce.Code, ce.Retryable, status, code, retryable)
			}
		}
	})

	t.Run("a 404 whose body is over the ceiling is still a 404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+16))
		}))
		defer srv.Close()
		err := directCall(srv.URL, 5*time.Second)
		ce := directCallError(t, err, "oversized 404")
		if ce.Engaged || ce.HTTPStatus != http.StatusNotFound || !errors.Is(err, ErrModelUnavailable) || errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("CallError = {engaged %v, status %d} %v, want {false, 404} + ErrModelUnavailable only", ce.Engaged, ce.HTTPStatus, err)
		}
	})

	t.Run("every 2xx that did not become a picture is ENGAGED and final", func(t *testing.T) {
		for name, body := range map[string]string{
			"not json":   `<html>`,
			"no image":   `{"data":[],"credits":40}`,
			"junk b64":   `{"data":[{"b64_json":"!!!"}],"credits":40}`,
			"no payload": `{"data":[{}]}`,
		} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			err := directCall(srv.URL, time.Second)
			srv.Close()
			ce := directCallError(t, err, name)
			if !ce.Engaged || ce.Retryable || ce.HTTPStatus != http.StatusOK || !errors.Is(err, ErrInvalidResponse) {
				t.Errorf("%s: CallError = {engaged %v, retryable %v, status %d} %v, want {true, false, 200} + ErrInvalidResponse",
					name, ce.Engaged, ce.Retryable, ce.HTTPStatus, err)
			}
		}
	})

	t.Run("a billed failure still carries its charge, around the CallError", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"data":[],"credits":40}`)
		}))
		defer srv.Close()
		err := directCall(srv.URL, time.Second)
		if _, credits, ok := Charge(err); !ok || credits != 40 {
			t.Fatalf("Charge(err) = %v, %v — the spend must survive the CallError", credits, ok)
		}
		if ce := directCallError(t, err, "charged"); !ce.Engaged || ce.Code != aiprov.CodeEmptyAnswer {
			t.Fatalf("CallError = {engaged %v, code %q}, want {true, empty_answer}", ce.Engaged, ce.Code)
		}
	})
}
