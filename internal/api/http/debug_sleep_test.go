// TEMP(techcard-analysis Ф1а, задача T16): снять вместе с debug_sleep.go.
package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	chi "github.com/go-chi/chi/v5"
)

// TestDebugSleepExistsOnlyBehindTheFlag is the whole safety argument of the endpoint, stated as a
// test: WITHOUT DEBUG_SLEEP_ENABLED=1 the route is not registered, so an unauthenticated
// connection-holder cannot survive on a deployment that never asked for it — which is every
// deployment except beta.
//
// Both halves matter. "404 when unset" alone would also pass against a handler that quietly answers
// 200 on a router nobody mounted; "200 when set" alone would pass against a route registered
// unconditionally. The pair pins the flag itself.
func TestDebugSleepExistsOnlyBehindTheFlag(t *testing.T) {
	t.Run("unset: the route does not exist", func(t *testing.T) {
		t.Setenv(debugSleepEnabledEnv, "")
		r := chi.NewRouter()
		registerDebugSleep(r)

		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/sleep?s=1", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("with the flag unset /debug/sleep answered %d, want 404 (the route must not be registered at all)", rec.Code)
		}
	})

	t.Run("a value that is not 1 is not the flag", func(t *testing.T) {
		// "true" reads like an on switch and is not one: the spec key is DEBUG_SLEEP_ENABLED=1, and
		// a near-miss must fail CLOSED rather than half-enable an anonymous endpoint.
		t.Setenv(debugSleepEnabledEnv, "true")
		r := chi.NewRouter()
		registerDebugSleep(r)

		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/sleep?s=1", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("DEBUG_SLEEP_ENABLED=true registered the route (%d); only the exact value \"1\" may", rec.Code)
		}
	})

	t.Run("set: it sleeps, then answers", func(t *testing.T) {
		t.Setenv(debugSleepEnabledEnv, "1")
		r := chi.NewRouter()
		registerDebugSleep(r)

		rec := httptest.NewRecorder()
		start := time.Now()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/sleep?s=1", nil))
		took := time.Since(start)

		if rec.Code != http.StatusOK {
			t.Fatalf("with the flag set /debug/sleep answered %d, want 200", rec.Code)
		}
		if got, want := rec.Body.String(), "slept 1 s"; got != want {
			t.Errorf("body = %q, want %q", got, want)
		}
		// It has to actually sleep — an endpoint that returns instantly measures nothing.
		if took < time.Second {
			t.Errorf("returned after %s; ?s=1 must hold the connection for a whole second", took)
		}
	})
}

// TestDebugSleepClampsTheRequestedNap pins the [1,150] clamp WITHOUT sleeping for 150 seconds: the
// clamp is read off the answer text, which names the duration the handler settled on.
//
// The ceiling exists so the probe never measures its own limit instead of the edge's; the floor
// exists so a missing/garbage ?s cannot turn the endpoint into an instant 200 that looks like a
// measurement.
func TestDebugSleepClampsTheRequestedNap(t *testing.T) {
	// Only the two PARSE paths go through HTTP (each costs a real second): an absent ?s and a ?s
	// that is not a number must both land on the floor rather than on zero. The clamp arithmetic
	// itself is asserted below, on the function, at no cost.
	for _, tc := range []struct {
		query string
		want  string
	}{
		{"", "slept 1 s"},
		{"?s=oops", "slept 1 s"},
	} {
		t.Setenv(debugSleepEnabledEnv, "1")
		r := chi.NewRouter()
		registerDebugSleep(r)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/sleep"+tc.query, nil))
		if got := rec.Body.String(); got != tc.want {
			t.Errorf("GET /debug/sleep%s → %q, want %q", tc.query, got, tc.want)
		}
	}

	// The upper clamp is asserted on the ARITHMETIC rather than by waiting: 150 real seconds in a
	// unit test would be its own defect. The handler and this test read the same constant, so the
	// assertion is that a request above the ceiling is reduced TO the ceiling.
	if debugSleepMaxSeconds != 150 {
		t.Errorf("the documented ceiling is 150 s, the code says %d", debugSleepMaxSeconds)
	}
	if got := clampDebugSleepSeconds(9000); got != debugSleepMaxSeconds {
		t.Errorf("?s=9000 clamps to %d, want %d", got, debugSleepMaxSeconds)
	}
	if got := clampDebugSleepSeconds(-1); got != 1 {
		t.Errorf("?s=-1 clamps to %d, want 1", got)
	}
	if got := clampDebugSleepSeconds(42); got != 42 {
		t.Errorf("?s=42 clamps to %d, want 42 (a value inside the range must pass through)", got)
	}
}

// TestDebugSleepClampConnectsToTheEndpoint closes the hole the two HTTP cases above cannot: both of
// them ask for the FLOOR, so removing clampDebugSleepSeconds from the handler entirely left this
// package green while ?s=9000 turned into a two-and-a-half-hour nap. Here the nap is stubbed, so the
// full HTTP path — parse, clamp, sleep — is exercised at the CEILING for free.
func TestDebugSleepClampConnectsToTheEndpoint(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  time.Duration
	}{
		{"?s=9000", time.Duration(debugSleepMaxSeconds) * time.Second},
		{"?s=-5", 1 * time.Second},
		{"?s=42", 42 * time.Second},
	} {
		t.Run(tc.query, func(t *testing.T) {
			var asked time.Duration
			restore := debugSleepFor
			debugSleepFor = func(_ context.Context, d time.Duration) bool { asked = d; return true }
			t.Cleanup(func() { debugSleepFor = restore })

			t.Setenv(debugSleepEnabledEnv, "1")
			r := chi.NewRouter()
			registerDebugSleep(r)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/sleep"+tc.query, nil))

			if asked != tc.want {
				t.Errorf("GET /debug/sleep%s slept %v, want %v — the handler is not reading the clamp",
					tc.query, asked, tc.want)
			}
			if got, want := rec.Body.String(), "slept "+strconv.Itoa(int(tc.want/time.Second))+" s"; got != want {
				t.Errorf("answer %q, want %q", got, want)
			}
		})
	}
}

// TestDebugSleepReleasesTheConnectionWhenTheCallerHangsUp is the availability half. A plain
// time.Sleep does not end when the caller goes away: net/http cancels the request context on the
// client's FIN but does not reclaim the connection until the handler RETURNS. Unauthenticated and
// unthrottled on the root router, that turns 100 ms of caller time into 150 s of held descriptor.
//
// The probe here is the handler's own return, not a goroutine count: if the handler returns while
// the nap is still notionally running, the server is free to close the socket — which is exactly
// the property that was missing.
func TestDebugSleepReleasesTheConnectionWhenTheCallerHangsUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/debug/sleep?s=150", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { defer close(done); debugSleepHandler(rec, req) }()

	cancel() // the caller hangs up
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler was still asleep 3 s after the caller hung up: a client that aborts " +
			"after 100 ms would hold a server connection for the full 150 s nap")
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("handler wrote %q to a caller that had already gone; writing to a dead socket keeps "+
			"the goroutine and its descriptor alive for the rest of the nap", body)
	}
}

// TestDebugSleepStillSleepsForACallerThatWaits is the negative control for the test above: the
// early return must depend on the CALLER leaving, not fire unconditionally. Without this, a handler
// that returned immediately in every case would pass the hang-up test and silently measure nothing.
func TestDebugSleepStillSleepsForACallerThatWaits(t *testing.T) {
	start := time.Now()
	rec := httptest.NewRecorder()
	debugSleepHandler(rec, httptest.NewRequest(http.MethodGet, "/debug/sleep?s=1", nil))
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("a live caller got its answer after %v; the endpoint measures nothing if it does "+
			"not actually stay silent", elapsed)
	}
	if got := rec.Body.String(); got != "slept 1 s" {
		t.Errorf("answer %q, want %q", got, "slept 1 s")
	}
}
