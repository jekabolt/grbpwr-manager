// Package saferun provides a small helper to recover panics in background
// worker loops, so a panic in a single iteration is logged (with stack) and the
// loop continues instead of crashing the whole process.
package saferun

import (
	"context"
	"log/slog"
	"runtime/debug"
)

// Recover recovers a panic in a deferred call, logs it with stack via log/slog
// (tagged with the worker name), and allows the caller to continue.
//
// Use it as the first deferred call inside the per-iteration work function of a
// worker loop, e.g.:
//
//	func (w *Worker) runOnce(ctx context.Context) {
//	    defer saferun.Recover(ctx, "my-worker")
//	    // ... per-tick work ...
//	}
//
// Placed there, a panic unwinds to this defer, is recovered, the function
// returns, and the surrounding for/select loop proceeds to the next tick.
func Recover(ctx context.Context, name string) {
	if r := recover(); r != nil {
		slog.ErrorContext(ctx, "panic recovered",
			slog.String("worker", name),
			slog.Any("panic", r),
			slog.String("stack", string(debug.Stack())),
		)
	}
}
