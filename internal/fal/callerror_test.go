package fal

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ═══ B-14 — callJSON SPEAKS aiprov.CallError, AND ENGAGED IS A MONEY FACT OF THE SUBMIT ONLY ═══
//
// Every error leaving callJSON is a CallError around today's error (the sentinels and sentences are
// pinned by TestEveryStatusCodeGetsTheSentinelThatSaysWhatToDO and submit_unconfirmed_test.go, which
// pass untouched). What these probes pin is Engaged: on a SUBMIT it is true exactly when fal may have
// queued and billed the job — a post-write break, a 5xx other than a bare 503, a 2xx without a usable
// answer — and false for every refusal; on a GET (a status poll, a result fetch) it is NEVER true, and
// the failed lookup stays retryable, because the money moved at the submit and looking again is free.
//
// MUTATIONS (each measured red→green): the Do branch's `case wroteRequest():` → `case !wroteRequest():`
// → the post-write and refused-dial submit cases go red; the non-2xx `fail(code, status, false, …)` →
// `true` → every refusal row goes red; `broken`'s GET arm → engaged → the GET 2xx case goes red; the
// GET Do arm → `fail(code, 0, true, …)` → the GET timeout case goes red.

func falCallError(t *testing.T, err error, what string) *aiprov.CallError {
	t.Helper()
	ce, ok := aiprov.AsCallError(err)
	require.Truef(t, ok, "%s: err = %v (%T) must carry an *aiprov.CallError", what, err, err)
	require.Equalf(t, entity.AIProviderFal, ce.Provider, "%s: the billing key", what)
	return ce
}

// hangAfterRead reads the whole request, then holds the answer until the client gives up.
func hangAfterRead(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

func TestCallJSON_TheSubmitIsTheMoneyBoundary(t *testing.T) {
	t.Run("a deadline AFTER the submit was written is ENGAGED, unconfirmed, final", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(hangAfterRead))
		defer srv.Close()
		c := newGenericClient(srv.URL, Config{})
		c.cfg.HTTPTimeout = 150 * time.Millisecond
		for route, submit := range submitAll(c) {
			err := submit()
			ce := falCallError(t, err, route)
			require.Truef(t, ce.Engaged, "%s: a written submit may be queued and billed", route)
			require.Falsef(t, ce.Retryable, "%s", route)
			require.Equalf(t, aiprov.CodeTimeout, ce.Code, "%s", route)
			require.Zerof(t, ce.HTTPStatus, "%s: no answer arrived", route)
			require.ErrorIsf(t, err, ErrSubmitUnconfirmed, "%s: the existing sentinel rides inside", route)
		}
	})

	t.Run("a refused dial is NOT engaged and may be retried", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		deadURL := dead.URL
		dead.Close()
		for route, submit := range submitAll(newGenericClient(deadURL, Config{})) {
			err := submit()
			ce := falCallError(t, err, route)
			require.Falsef(t, ce.Engaged, "%s: nothing left the process", route)
			require.Truef(t, ce.Retryable, "%s", route)
			require.Equalf(t, aiprov.CodeTransport, ce.Code, "%s", route)
			require.NotErrorIsf(t, err, ErrSubmitUnconfirmed, "%s", route)
		}
	})

	t.Run("every 4xx and the bare 503 are NOT engaged and carry their status", func(t *testing.T) {
		for _, status := range []int{400, 401, 402, 403, 404, 408, 409, 410, 422, 429, 503} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"detail":"said no"}`)
			}))
			code, retryable := aiprov.ClassifyStatus(status)
			for route, submit := range submitAll(newGenericClient(srv.URL, Config{})) {
				err := submit()
				ce := falCallError(t, err, route)
				require.Falsef(t, ce.Engaged, "%s HTTP %d: a refusal at the gate moved no money", route, status)
				require.Equalf(t, status, ce.HTTPStatus, "%s", route)
				require.Equalf(t, code, ce.Code, "%s HTTP %d", route, status)
				require.Equalf(t, retryable, ce.Retryable, "%s HTTP %d", route, status)
				require.NotErrorIsf(t, err, ErrSubmitUnconfirmed, "%s HTTP %d", route, status)
			}
			srv.Close()
		}
	})

	t.Run("every other 5xx on a submit is ENGAGED — fal's gateway may have lost the queue's answer", func(t *testing.T) {
		for _, status := range []int{500, 502, 504} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			for route, submit := range submitAll(newGenericClient(srv.URL, Config{})) {
				err := submit()
				ce := falCallError(t, err, route)
				require.Truef(t, ce.Engaged, "%s HTTP %d", route, status)
				require.Falsef(t, ce.Retryable, "%s HTTP %d", route, status)
				require.Equalf(t, status, ce.HTTPStatus, "%s", route)
				require.Equalf(t, aiprov.CodeProviderError, ce.Code, "%s", route)
				require.ErrorIsf(t, err, ErrSubmitUnconfirmed, "%s HTTP %d", route, status)
			}
			srv.Close()
		}
	})

	t.Run("a 2xx that is not an answer is ENGAGED", func(t *testing.T) {
		for name, body := range map[string]string{"no request id": `{"status":"IN_QUEUE"}`, "not json": `<html>`} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			for route, submit := range submitAll(newGenericClient(srv.URL, Config{})) {
				err := submit()
				ce := falCallError(t, err, route+" / "+name)
				require.Truef(t, ce.Engaged, "%s / %s", route, name)
				require.Falsef(t, ce.Retryable, "%s / %s", route, name)
				require.ErrorIsf(t, err, ErrSubmitUnconfirmed, "%s / %s", route, name)
			}
			srv.Close()
		}
	})
}

func TestCallJSON_ALookupIsNEVER_ENGAGED(t *testing.T) {
	get := func(c *Client) error {
		var out statusResponse
		return c.callJSON(context.Background(), http.MethodGet, "/meshy/v7/requests/req-1/status", nil, &out, nil)
	}

	t.Run("a deadline on a status poll is not engaged and stays retryable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(hangAfterRead))
		defer srv.Close()
		c := newGenericClient(srv.URL, Config{})
		c.cfg.HTTPTimeout = 150 * time.Millisecond
		err := get(c)
		ce := falCallError(t, err, "GET timeout")
		require.False(t, ce.Engaged)
		require.True(t, ce.Retryable, "looking again is free")
		require.Equal(t, aiprov.CodeTimeout, ce.Code)
		require.NotErrorIs(t, err, ErrSubmitUnconfirmed)
	})

	t.Run("every non-2xx on a lookup is not engaged, 5xx included", func(t *testing.T) {
		for _, status := range []int{401, 404, 408, 410, 429, 500, 502, 503, 504} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			err := get(newGenericClient(srv.URL, Config{}))
			srv.Close()
			ce := falCallError(t, err, http.StatusText(status))
			code, retryable := aiprov.ClassifyStatus(status)
			require.Falsef(t, ce.Engaged, "GET %d", status)
			require.Equalf(t, status, ce.HTTPStatus, "GET %d", status)
			require.Equalf(t, code, ce.Code, "GET %d", status)
			require.Equalf(t, retryable, ce.Retryable, "GET %d", status)
		}
	})

	t.Run("a garbled 2xx on a lookup is not engaged and stays retryable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `<html>`)
		}))
		defer srv.Close()
		err := get(newGenericClient(srv.URL, Config{}))
		ce := falCallError(t, err, "GET garbled")
		require.False(t, ce.Engaged)
		require.True(t, ce.Retryable)
		require.Equal(t, http.StatusOK, ce.HTTPStatus)
		require.True(t, errors.Is(err, ErrUnexpectedResponse), "the sentinel rides inside: %v", err)
		require.NotErrorIs(t, err, ErrSubmitUnconfirmed)
	})
}
