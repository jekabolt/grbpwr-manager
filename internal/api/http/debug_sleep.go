// TEMP(techcard-analysis Ф1а, задача T16; при остановке фичи после Ф0 — снять отдельным коммитом):
// удалить вместе с внесением LLM-пути. Файл заведён отдельным ровно затем, чтобы снятие было
// удалением файла плюс одной строки вызова, а не археологией внутри http.go.
package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	chi "github.com/go-chi/chi/v5"
)

// debugSleepEnabledEnv is the ONLY switch that brings the endpoint into existence. It is set in the
// BETA app spec and nowhere else.
//
// WHY A FLAG AND NOT JUST A ROUTE. The endpoint is unauthenticated (it has to be: it measures the
// edge, and an auth round trip is not what is being timed) and it holds a connection open for up to
// two and a half minutes. Registering it unconditionally would mean that a master promotion, or
// this feature being abandoned after Ф0, could leave an anonymous connection-holder alive on prod
// with nobody remembering it was there. Behind the flag the route does not exist at all on any
// deployment that does not name it — which is every deployment except beta.
//
// The flag does NOT replace deleting the code (T16). It only makes forgetting survivable.
const debugSleepEnabledEnv = "DEBUG_SLEEP_ENABLED"

// debugSleepMaxSeconds caps the nap. 150 s is above every edge timeout worth measuring (Cloudflare
// cuts a silent origin at ~100 s on non-Enterprise plans), so the ceiling never becomes the thing
// the measurement finds.
const debugSleepMaxSeconds = 150

// registerDebugSleep mounts GET /debug/sleep?s=N — and ONLY when debugSleepEnabledEnv is exactly
// "1". Without it the route is never registered, so the mux answers 404 like any other path that
// does not exist.
//
// WHAT IT MEASURES. Cloudflare's 524 fires on TIME TO FIRST BYTE, not on total duration, so the
// handler must send NOTHING until the sleep is over: no early WriteHeader, no flush, no header that
// commits the response. A ticker or a progress trickle would keep the connection alive and the
// probe would report a ceiling that does not exist for a real, silent request.
func registerDebugSleep(r chi.Router) {
	if os.Getenv(debugSleepEnabledEnv) != "1" {
		return
	}
	slog.Default().Warn("DEBUG /debug/sleep endpoint is REGISTERED (temporary edge-timeout probe)",
		slog.String("env", debugSleepEnabledEnv))
	r.HandleFunc("/debug/sleep", debugSleepHandler)
}

// clampDebugSleepSeconds folds any requested nap into [1, debugSleepMaxSeconds]. Separate from the
// handler so the ceiling can be asserted without a test that actually waits 150 seconds.
func clampDebugSleepSeconds(requested int) int {
	if requested < 1 {
		return 1
	}
	if requested > debugSleepMaxSeconds {
		return debugSleepMaxSeconds
	}
	return requested
}

// debugSleepFor is the nap itself, and it is a variable for ONE reason: it is the only way a test
// can drive the whole HTTP path — including the clamp that connects the parsed ?s to the sleep —
// without actually waiting. Before it existed, both HTTP cases of the clamp test asked for the
// floor, so deleting the clamp call from the handler left every test green while ?s=9000 became a
// two-and-a-half-hour nap.
//
// IT WATCHES THE CLIENT, AND THAT IS THE WHOLE POINT OF THE select. A plain time.Sleep does not end
// when the caller hangs up: net/http cancels the request context on the client's FIN but does not
// reclaim the CONNECTION until the handler returns. An earlier comment here waved this away with
// "the goroutine ends on its own in ≤150 s" — the goroutine does; the file descriptor does not.
// Measured on this endpoint: 20 clients that abort after 100 ms still held 20 server connections
// and 20 goroutines for the entire nap. Unauthenticated, unthrottled, on the root router, with no
// ReadTimeout/WriteTimeout on the server and h2c carrying up to 250 streams per TCP connection,
// that is ~1500x amplification from 100 ms of caller time — a denial of service wearing the mask of
// a measurement tool.
//
// Watching the context costs the probe NOTHING: the question it asks is "when does the edge give
// up on a silent origin", and by the time the edge gives up it has already answered — the 524
// reached the caller. Returning then is what lets the server reclaim the socket.
var debugSleepFor = func(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// debugSleepHandler sleeps for the requested number of seconds, then answers 200 "slept N s".
//
// It writes NOTHING before the nap is over — no early WriteHeader, no flush, no committing header.
// Cloudflare's 524 fires on time to FIRST BYTE, not on total duration, so a progress trickle would
// keep the connection alive and make the probe report a ceiling that no silent request ever meets.
func debugSleepHandler(w http.ResponseWriter, r *http.Request) {
	requested := 1
	if raw := r.URL.Query().Get("s"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			requested = n
		}
	}
	seconds := clampDebugSleepSeconds(requested)

	if !debugSleepFor(r.Context(), time.Duration(seconds)*time.Second) {
		// The caller gave up first, which already answers the question the probe asks. Return at
		// once so the server can close the connection; writing to a socket nobody is reading would
		// only keep this goroutine and its descriptor alive for the rest of the nap.
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprintf(w, "slept %d s", seconds); err != nil {
		slog.Default().Error("failed to write debug sleep response", slog.String("err", err.Error()))
	}
}
