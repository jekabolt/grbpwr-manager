package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/observability"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Recover recovers panics in non-gRPC HTTP handlers, logs the stack via slog, and returns 500
// instead of letting net/http drop the connection silently. With Sentry on, the panic is also
// sent there (once: the log line below is marked as already reported).
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				observability.CapturePanic(r.Context(), rec, httpRoute(r))
				slog.Default().ErrorContext(observability.MarkReported(r.Context()), "recovered panic in http handler",
					slog.Any("panic", rec),
					slog.String("path", r.URL.Path),
					slog.String("stack", string(debug.Stack())),
				)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// httpRoute is the chi route pattern that matched (low cardinality), else the bare path.
func httpRoute(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return r.Method + " " + p
		}
	}
	return r.Method + " " + r.URL.Path
}

// GRPCPanicHandler logs a recovered panic with its stack trace and returns a generic Internal
// error, so a single malformed request cannot crash the process and panic internals are never
// leaked to the caller. With Sentry on, the panic is also sent there.
func GRPCPanicHandler(ctx context.Context, p any) error {
	method, _ := grpc.Method(ctx)
	observability.CapturePanic(ctx, p, method)
	slog.Default().ErrorContext(observability.MarkReported(ctx), "recovered from panic in gRPC handler",
		slog.Any("panic", p),
		slog.String("stack", string(debug.Stack())),
	)
	return status.Error(codes.Internal, "internal server error")
}
