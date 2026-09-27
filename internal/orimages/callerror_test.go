package orimages

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

// ═══ B-14 — THE WRITE IS THE MONEY BOUNDARY, AND THE TRANSPORT SAYS WHICH SIDE A FAILURE IS ON ═══
//
// Every error Generate returns is an *aiprov.CallError around today's error. What these probes pin is
// the one field the design worker's retry and the ledger now read instead of the sentinels: Engaged.
// A deadline AFTER the request was written is engaged (OpenRouter may be rendering and billing it); a
// refused dial is not; no non-2xx is (all-or-nothing billing: «fails and is not billed»); every 2xx that
// did not become pictures is. The sentinel and the sentence ride inside, unchanged.
//
// MUTATIONS (each measured red→green): `engaged := wroteRequest()` → `!wroteRequest()` in the Do
// branch → the post-write and the refused-dial cases go red; the non-2xx `fail(code, status, false,
// …)` → `true` → every status row goes red; the status check moved back below the body read → the
// unreadable-404 case reads ErrResponseTooLarge / too_large / engaged.
//
// B-13/A3 — a 408 on this paid POST is ENGAGED and final (the server may have rendered and billed the
// picture before it gave up). MUTATION (measured red→green): the 408 arm's `fail(code, status, true,
// false, …)` → `false, retryable` → the 408 case goes red.

func mustCallError(t *testing.T, err error) *aiprov.CallError {
	t.Helper()
	ce, ok := aiprov.AsCallError(err)
	if !ok {
		t.Fatalf("err = %v (%T): every failure of Generate must carry an *aiprov.CallError", err, err)
	}
	if ce.Provider != entity.AIProviderOpenRouter {
		t.Fatalf("CallError.Provider = %q, want %q — the account this client spends", ce.Provider, entity.AIProviderOpenRouter)
	}
	return ce
}

func TestGenerate_TheWriteIsTheMoneyBoundary(t *testing.T) {
	t.Run("a deadline AFTER the request was written is ENGAGED and never retryable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The provider has the whole request, and is still rendering when we hang up.
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}))
		defer srv.Close()

		c := New(Config{APIKey: "k", BaseURL: srv.URL, HTTPTimeout: 150 * time.Millisecond})
		res, err := c.Generate(context.Background(), Request{Prompt: "p"})
		ce := mustCallError(t, err)
		if !ce.Engaged {
			t.Fatalf("a post-write deadline must be ENGAGED — retrying it pays twice: %+v", ce)
		}
		if ce.Retryable || ce.Code != aiprov.CodeTimeout || ce.HTTPStatus != 0 {
			t.Fatalf("CallError = {code %q, status %d, retryable %v}, want {timeout, 0, false}", ce.Code, ce.HTTPStatus, ce.Retryable)
		}
		if res != nil {
			t.Errorf("nothing came back, so nothing is returned: %+v", res)
		}
		if !strings.HasPrefix(err.Error(), "orimages: request failed: ") {
			t.Errorf("the sentence must be today's, verbatim: %q", err)
		}
	})

	t.Run("a refused dial is NOT engaged and may be retried", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		deadURL := dead.URL
		dead.Close()

		_, err := New(Config{APIKey: "k", BaseURL: deadURL}).Generate(context.Background(), Request{Prompt: "p"})
		ce := mustCallError(t, err)
		if ce.Engaged || !ce.Retryable || ce.Code != aiprov.CodeTransport || ce.HTTPStatus != 0 {
			t.Fatalf("CallError = {engaged %v, retryable %v, code %q, status %d}, want {false, true, transport, 0}",
				ce.Engaged, ce.Retryable, ce.Code, ce.HTTPStatus)
		}
	})

	t.Run("a 408 is ENGAGED and never retried: the picture may exist on the far side (B-13/A3)", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusRequestTimeout)
			_, _ = io.WriteString(w, `{"error":{"message":"gave up"}}`)
		}))
		defer srv.Close()
		res, err := New(Config{APIKey: "k", BaseURL: srv.URL}).Generate(context.Background(), Request{Prompt: "p"})
		ce := mustCallError(t, err)
		code, _ := aiprov.ClassifyStatus(http.StatusRequestTimeout)
		if !ce.Engaged || ce.Retryable || ce.HTTPStatus != http.StatusRequestTimeout || ce.Code != code {
			t.Fatalf("CallError = {engaged %v, retryable %v, status %d, code %q}, want {true, false, 408, %q}",
				ce.Engaged, ce.Retryable, ce.HTTPStatus, ce.Code, code)
		}
		if res != nil || !errors.Is(err, ErrProviderFailure) || !strings.Contains(err.Error(), "gave up") {
			t.Errorf("result %+v, err %q — nil result, today's sentinel and the provider's own words", res, err)
		}
	})

	t.Run("every other non-2xx is NOT engaged, carries its status, and is classified by the matrix", func(t *testing.T) {
		for _, status := range []int{400, 401, 402, 403, 404, 409, 422, 429, 500, 502, 503, 504} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"error":{"message":"said no"}}`)
			}))
			res, err := New(Config{APIKey: "k", BaseURL: srv.URL}).Generate(context.Background(), Request{Prompt: "p"})
			srv.Close()
			ce := mustCallError(t, err)
			code, retryable := aiprov.ClassifyStatus(status)
			if ce.Engaged || ce.HTTPStatus != status || ce.Code != code || ce.Retryable != retryable {
				t.Errorf("HTTP %d: CallError = {engaged %v, status %d, code %q, retryable %v}, want {false, %d, %q, %v}",
					status, ce.Engaged, ce.HTTPStatus, ce.Code, ce.Retryable, status, code, retryable)
			}
			if res != nil || !strings.Contains(err.Error(), "said no") {
				t.Errorf("HTTP %d: result %+v, err %q — nil result and the provider's own words", status, res, err)
			}
		}
	})

	t.Run("a 404 whose body cannot be read is still a 404", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, strings.Repeat("x", 512)) // over the ceiling below
		}))
		defer srv.Close()

		_, err := New(Config{APIKey: "k", BaseURL: srv.URL, MaxResponseBytes: 64}).Generate(context.Background(), Request{Prompt: "p"})
		ce := mustCallError(t, err)
		if ce.Engaged || ce.HTTPStatus != http.StatusNotFound || ce.Code != aiprov.CodeModelUnknown {
			t.Fatalf("CallError = {engaged %v, status %d, code %q}, want {false, 404, model_unknown}", ce.Engaged, ce.HTTPStatus, ce.Code)
		}
		if !errors.Is(err, ErrModelUnavailable) || errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("the STATUS names the fault, not the body's fate: %v", err)
		}
		if !strings.Contains(err.Error(), "could not be read") {
			t.Errorf("the lost sentence must say it was lost: %q", err)
		}
	})

	t.Run("every 2xx that did not become pictures is ENGAGED and final", func(t *testing.T) {
		for _, c := range []struct {
			name, body string
			ceiling    int64
			code       string
			sentinel   error
		}{
			{"not json", `<html>gateway</html>`, 0, aiprov.CodeProviderError, nil},
			{"an error inside a 200", `{"error":{"message":"content policy"}}`, 0, aiprov.CodeProviderError, nil},
			{"no pictures", `{"data":[],"usage":{"cost":0.01}}`, 0, aiprov.CodeEmptyAnswer, ErrNoImages},
			{"junk base64", `{"data":[{"b64_json":"!!!"}],"usage":{"cost":0.01}}`, 0, aiprov.CodeProviderError, nil},
			{"over the ceiling", okResponse("image/png") + "  ", int64(len(okResponse("image/png"))), aiprov.CodeTooLarge, ErrResponseTooLarge},
		} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, c.body)
			}))
			_, err := New(Config{APIKey: "k", BaseURL: srv.URL, MaxResponseBytes: c.ceiling}).Generate(context.Background(), Request{Prompt: "p"})
			srv.Close()
			ce := mustCallError(t, err)
			if !ce.Engaged || ce.Retryable || ce.HTTPStatus != http.StatusOK || ce.Code != c.code {
				t.Errorf("%s: CallError = {engaged %v, retryable %v, status %d, code %q}, want {true, false, 200, %q}",
					c.name, ce.Engaged, ce.Retryable, ce.HTTPStatus, ce.Code, c.code)
			}
			if c.sentinel != nil && !errors.Is(err, c.sentinel) {
				t.Errorf("%s: the sentinel %v must survive the wrap: %v", c.name, c.sentinel, err)
			}
		}
	})

	t.Run("a refusal before the wire is NOT engaged and not retryable", func(t *testing.T) {
		for _, c := range []struct {
			name   string
			client *Client
			req    Request
			code   string
			want   error
		}{
			{"no key", New(Config{}), Request{Prompt: "p"}, aiprov.CodeNotConfigured, ErrNotConfigured},
			{"nil client", nil, Request{Prompt: "p"}, aiprov.CodeNotConfigured, ErrNotConfigured},
			{"no prompt", New(Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"}), Request{}, aiprov.CodeBadRequest, ErrBadRequest},
		} {
			_, err := c.client.Generate(context.Background(), c.req)
			ce := mustCallError(t, err)
			if ce.Engaged || ce.Retryable || ce.HTTPStatus != 0 || ce.Code != c.code || !errors.Is(err, c.want) {
				t.Errorf("%s: CallError = {engaged %v, retryable %v, status %d, code %q} %v, want {false, false, 0, %q} %v",
					c.name, ce.Engaged, ce.Retryable, ce.HTTPStatus, ce.Code, err, c.code, c.want)
			}
		}
	})
}
