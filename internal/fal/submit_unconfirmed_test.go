package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ═══ G-03 fix, Codex 1 (b) — AN AMBIGUOUS SUBMIT IS NAMED, NEVER READ AS WEATHER ═══
//
// fal documents no idempotency key for a model submit (https://fal.ai/docs/documentation/model-apis/
// inference/queue), so a submit that may have reached fal must not be sent a second time. The
// transport's job is to tell the two cases apart: headers written → ErrSubmitUnconfirmed; nothing
// written → the ordinary error.

// submitAll — the three submit verbs of this package, all over callJSON's POST.
func submitAll(c *Client) map[string]func() error {
	return map[string]func() error{
		"generic": func() error {
			_, err := c.SubmitJSON(context.Background(), DefaultModelFill, map[string]any{
				"prompt": "x", "image_url": "https://cdn.example/a.png", "mask_url": "https://cdn.example/m.png"})
			return err
		},
		"cutout": func() error {
			_, err := c.SubmitCutout(context.Background(), "https://cdn.example/a.png")
			return err
		},
		"3d": func() error {
			_, err := c.Submit(context.Background(), Request3D{FrontURL: "https://cdn.example/f.png"})
			return err
		},
	}
}

// TestAnAmbiguousSubmitIsUNCONFIRMED_ON_EVERY_ROUTE — a request fal read but answered with nothing
// usable: a hang past the HTTP timeout, a 502, a 2xx without a request id, a 2xx that is not JSON.
// The cut-out and the 3D submit ride the same callJSON, so the flaw — and the fix — are shared.
// MUTATIONS (measured red): drop `sent.Store(true)` in callJSON → the «hang» rows lose the sentinel;
// drop the `>= 500` wrap → the «502» rows; submitLost() back to a bare ErrUnexpectedResponse → the
// «no id» rows.
func TestAnAmbiguousSubmitIsUNCONFIRMED_ON_EVERY_ROUTE(t *testing.T) {
	for _, tc := range []struct {
		name  string
		serve http.HandlerFunc
	}{
		{"a hang after the request was read", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			time.Sleep(400 * time.Millisecond)
		}},
		{"a 502 from the gateway", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}},
		{"a 2xx without a request id", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "IN_QUEUE"})
		}},
		{"a 2xx that is not JSON", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "<html>")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.serve)
			defer srv.Close()
			c := newGenericClient(srv.URL, Config{})
			c.cfg.HTTPTimeout = 150 * time.Millisecond
			for route, submit := range submitAll(c) {
				err := submit()
				require.ErrorIsf(t, err, ErrSubmitUnconfirmed, "%s: %v", route, err)
			}
		})
	}
}

// TestADefiniteSubmitRefusalIsNOT_UNCONFIRMED — the positive controls: fal read the request and said
// no (4xx), or nothing ever left the process (a dead address). Neither may be dressed as «maybe
// charged»: the first keeps its terminal sentinel, the second stays retryable weather.
func TestADefiniteSubmitRefusalIsNOT_UNCONFIRMED(t *testing.T) {
	for _, code := range []int{http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusUnauthorized} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		c := newGenericClient(srv.URL, Config{})
		for route, submit := range submitAll(c) {
			err := submit()
			require.Errorf(t, err, "%s %d", route, code)
			require.NotErrorIsf(t, err, ErrSubmitUnconfirmed, "%s %d: %v", route, code, err)
		}
		srv.Close()
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	c := newGenericClient(url, Config{})
	for route, submit := range submitAll(c) {
		err := submit()
		require.Errorf(t, err, route)
		require.NotErrorIsf(t, err, ErrSubmitUnconfirmed, "%s: nothing was written, so nothing was bought: %v", route, err)
	}
}

// TestAStatusLookupIsNeverUNCONFIRMED — the sentinel is about PAYMENTS: a GET that hangs is a free
// lookup and keeps its old reading.
func TestAStatusLookupIsNeverUNCONFIRMED(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := newGenericClient(srv.URL, Config{})
	_, err := c.CollectFile(context.Background(), DefaultModelFill, "gen-1", PickImages0, &bytes.Buffer{}, 0)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrSubmitUnconfirmed)
}

// ═══ G-03 fix, Codex 3 — A BROKEN RESULT BODY KEEPS THE CHARGE ═══

// TestAMalformedOrOversizedResultCARRIES_THE_CHARGE — fal answered the result fetch with 2xx and a
// billing header, then a body that is not JSON or is over the envelope cap. The job was made and
// billed; the error must say so. Without the header the one assumed unit rides the error flagged
// Assumed (the pricing side books its conservative ceiling). MUTATION (measured red): collectFile
// back to `return nil, err` on a callJSON error → Charge(err) is not ok.
func TestAMalformedOrOversizedResultCARRIES_THE_CHARGE(t *testing.T) {
	for _, tc := range []struct {
		name, units, body string
		wantUnits         float64
		assumed           bool
	}{
		{"not JSON, units named", "3", "{not json", 3, false},
		{"over the envelope cap, units named", "2", `{"images":[{"url":"` + strings.Repeat("x", maxAPIResponseBytes) + `"}]}`, 2, false},
		{"not JSON, no header", "", "{not json", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &genericStub{result: tc.body, units: tc.units}
			srv := httptest.NewServer(stub.handler(t))
			defer srv.Close()
			c := newGenericClient(srv.URL, Config{})

			_, err := c.CollectFile(context.Background(), DefaultModelOutpaint, "gen-1", PickImages0, &bytes.Buffer{}, 0)
			require.Error(t, err)
			units, ok := Charge(err)
			require.Truef(t, ok, "a 2xx result was billed: %v", err)
			require.Equal(t, tc.wantUnits, units)
			var ce *ChargedError
			require.ErrorAs(t, err, &ce)
			require.Equal(t, tc.assumed, ce.Assumed)
			require.Equal(t, "gen-1", ce.RequestID)
			require.Equal(t, DefaultModelOutpaint, ce.Model)
		})
	}
}
