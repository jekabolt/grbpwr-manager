// TEMP(techcard-analysis Ф1а, задача T16; при остановке фичи после Ф0 — снять отдельным коммитом):
// удалить вместе с внесением LLM-пути. Файл заведён отдельным ровно затем, чтобы снятие было
// удалением файла плюс одной строки вызова, а не археологией внутри http.go.
package httpapi

import (
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

// debugSleepHandler sleeps for the requested number of seconds, then answers 200 "slept N s".
func debugSleepHandler(w http.ResponseWriter, r *http.Request) {
	requested := 1
	if raw := r.URL.Query().Get("s"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			requested = n
		}
	}
	seconds := clampDebugSleepSeconds(requested)

	// Plain time.Sleep, deliberately: the point is a connection that produces no bytes at all for
	// the whole interval. It does not watch r.Context() either — a client that gave up has already
	// answered the question the probe is asking, and the goroutine ends on its own in ≤150 s.
	time.Sleep(time.Duration(seconds) * time.Second)

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprintf(w, "slept %d s", seconds); err != nil {
		slog.Default().Error("failed to write debug sleep response", slog.String("err", err.Error()))
	}
}
