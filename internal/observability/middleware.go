package observability

import (
	"context"
	"net/http"

	"google.golang.org/grpc"
)

// HTTPMiddleware gives each HTTP request its own hub tagged with a request id. The id is the
// incoming X-Request-Id when it looks like one, otherwise a fresh one; it is written back onto the
// request headers (never the response) so the grpc-gateway forwards it to the gRPC call.
// Disabled, it returns next itself.
func HTTPMiddleware(next http.Handler) http.Handler {
	if !Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := sanitizeRequestID(r.Header.Get(RequestIDHeader))
		if rid == "" {
			rid = newRequestID()
			r.Header.Set(RequestIDHeader, rid)
		}
		ctx := withHub(r.Context(), map[string]string{
			"request_id":  rid,
			"http.method": r.Method,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UnaryServerInterceptor gives each unary gRPC call its own hub tagged with the method and the
// request id forwarded by the gateway (a pass-through when disabled). It must sit OUTSIDE the recovery interceptor, so the hub is
// in the context the recovery handler receives, and outside auth, so SetAdmin can reach it.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	if !Enabled() {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(callContext(ctx, info.FullMethod), req)
	}
}

// StreamServerInterceptor is the streaming twin of UnaryServerInterceptor.
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	if !Enabled() {
		return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, ss)
		}
	}
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &hubStream{ServerStream: ss, ctx: callContext(ss.Context(), info.FullMethod)})
	}
}

func callContext(ctx context.Context, method string) context.Context {
	return withHub(ctx, map[string]string{
		"route":      method,
		"request_id": requestIDFromMetadata(ctx),
	})
}

type hubStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *hubStream) Context() context.Context { return s.ctx }
