package observability

import (
	"context"
	"fmt"
	"time"

	"github.com/getsentry/sentry-go"
)

// Quota guard. The Sentry plan is small, and one hot failing path must not spend it: each panic
// route is sent at most once a minute, each distinct error log line at most once per 30 minutes,
// and all slog-originated events together at most logBudgetPerHour times an hour. Suppressed
// repeats are counted and travel with the next event of the same key.
const (
	panicEvery       = time.Minute
	logEvery         = 30 * time.Minute
	logBudgetPerHour = 60
)

var limits = newLimiter()

// CapturePanic sends a recovered panic as an exception event. route names what was running (gRPC
// full method or HTTP route pattern). The request's hub (request id, admin) is taken from ctx.
func CapturePanic(ctx context.Context, p any, route string) {
	if !Enabled() || p == nil {
		return
	}
	ok, suppressed := limits.allow("panic:"+route, panicEvery, 0, time.Now())
	if !ok {
		return
	}
	handled := true
	ev := sentry.NewEvent()
	ev.Level = sentry.LevelFatal
	ev.Exception = []sentry.Exception{{
		Type:       panicType(p),
		Value:      fmt.Sprint(p),
		Stacktrace: sentry.NewStacktrace(),
		Mechanism:  &sentry.Mechanism{Type: "go.panic", Handled: &handled},
	}}
	if suppressed > 0 {
		ev.Contexts["rate_limit"] = sentry.Context{"suppressed_repeats": suppressed}
	}
	hub := hubFrom(ctx)
	hub.WithScope(func(scope *sentry.Scope) {
		if route != "" {
			scope.SetTag("route", route)
		}
		hub.CaptureEvent(ev)
	})
}

func panicType(p any) string {
	if _, ok := p.(error); ok {
		return fmt.Sprintf("panic: %T", p)
	}
	return "panic"
}
