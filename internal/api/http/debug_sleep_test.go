// TEMP(techcard-analysis Ф1а, задача T16): снять вместе с debug_sleep.go.
package httpapi

import (
	"net/http"
	"net/http/httptest"
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
