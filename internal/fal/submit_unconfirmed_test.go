package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
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
// usable: a hang past the HTTP timeout, a 500, a 503 that nevertheless names a request id, a 2xx
// without a request id, a 2xx that is not JSON. The cut-out and the 3D submit ride the same
// callJSON, so the flaw — and the fix — are shared.
// MUTATIONS (measured red): drop `sent.Store(true)` in callJSON → the «hang» rows lose the sentinel;
// drop the `>= 500` branch → the «500» and «503 with an id» rows; submitLost() back to a bare
// ErrUnexpectedResponse → the «no id» rows.
func TestAnAmbiguousSubmitIsUNCONFIRMED_ON_EVERY_ROUTE(t *testing.T) {
	for _, tc := range []struct {
		name  string
		serve http.HandlerFunc
	}{
		{"a hang after the request was read", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			time.Sleep(400 * time.Millisecond)
		}},
		{"a 500 from the queue itself", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}},
		{"a 503 that names a request id", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"request_id":"req-accepted-anyway","detail":"busy"}`)
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
// no (4xx), the gateway/queue said it did not take it (502/503/504 without an id — fal's own queue
// client retries exactly these on a submit), or nothing ever left the process (a dead address). None
// may be dressed as «maybe charged»: the 4xx keep their terminal sentinels, the rest stay retryable
// weather.
// MUTATION (measured red): submitRetryableStatus → always false → the 502/503/504 rows turn
// unconfirmed (the G-03 r2 regression: every pre-enqueue 503 closed a cut-out or 3D run for good).
func TestADefiniteSubmitRefusalIsNOT_UNCONFIRMED(t *testing.T) {
	for _, code := range []int{http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusUnauthorized,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
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

// TestASubmitCutMidBodyIsNOT_UNCONFIRMED — G-03 r2, Codex 2: the headers went out, the JSON body did
// not (the peer resets the connection while the body is still being written). fal holds no complete
// request and cannot have queued or charged it, so this must stay the ordinary retryable error.
// A raw listener that RSTs every connection on accept (SetLinger(0)) and an 8 MiB body — larger than
// the transport's buffer and the kernel's send buffer — force the reset mid-write, the technique
// internal/openrouter's TestARequestCutMidWriteIsNotCharged measured.
// MUTATION (measured red): mark `sent` on WroteHeaders (the previous code) instead of WroteRequest
// with a nil Err → ErrSubmitUnconfirmed.
func TestASubmitCutMidBodyIsNOT_UNCONFIRMED(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	accepted := make(chan struct{}, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case accepted <- struct{}{}:
			default:
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0)
			}
			_ = conn.Close()
		}
	}()

	c := newGenericClient("http://"+ln.Addr().String(), Config{})
	c.cfg.HTTPTimeout = 20 * time.Second
	var out submitResponse
	err = c.callJSON(context.Background(), http.MethodPost, "/fal-ai/flux-pro/v1/fill",
		map[string]string{"prompt": strings.Repeat("x", 8<<20)}, &out, nil)
	require.Error(t, err)
	select {
	case <-accepted:
	default:
		t.Fatal("the listener accepted nothing — the probe measures a dead address, not a cut body")
	}
	require.NotErrorIs(t, err, context.DeadlineExceeded, "the cut must arrive as a write error, not a timeout")
	require.NotErrorIsf(t, err, ErrSubmitUnconfirmed, "the body never reached fal in full: %v", err)
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
