package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"

	"github.com/getsentry/sentry-go"
	"google.golang.org/grpc/metadata"
)

// RequestIDHeader carries the request id from the HTTP edge through the grpc-gateway into the
// gRPC call (as metadata key RequestIDMetadataKey), so both halves of one request share an id.
const (
	RequestIDHeader      = "X-Request-Id"
	RequestIDMetadataKey = "x-request-id"
)

type reportedKey struct{}

// MarkReported returns a context whose slog records are not forwarded to Sentry again: the caller
// has already captured the failure itself (a recovered panic and its log line are one event).
func MarkReported(ctx context.Context) context.Context {
	if !Enabled() {
		return ctx
	}
	return context.WithValue(ctx, reportedKey{}, true)
}

func isReported(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(reportedKey{}).(bool)
	return v
}

// withHub gives one request (or one gRPC call) its own hub, so tags and the user set while it runs
// never leak into another request's events.
func withHub(ctx context.Context, tags map[string]string) context.Context {
	hub := sentry.CurrentHub().Clone()
	for k, v := range tags {
		if v != "" {
			hub.Scope().SetTag(k, v)
		}
	}
	return sentry.SetHubOnContext(ctx, hub)
}

func hubFrom(ctx context.Context) *sentry.Hub {
	if ctx != nil {
		if hub := sentry.GetHubFromContext(ctx); hub != nil {
			return hub
		}
	}
	return sentry.CurrentHub()
}

// SetAdmin records the authenticated admin on the current request's hub as admins.id alone: no
// username, no email. Without a known id (nil) nothing is recorded. A no-op when disabled or
// outside a request hub.
func SetAdmin(ctx context.Context, id *int) {
	if !Enabled() || ctx == nil || id == nil {
		return
	}
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		return
	}
	adminID := strconv.Itoa(*id)
	hub.Scope().SetTag("admin_id", adminID)
	hub.Scope().SetUser(sentry.User{ID: adminID})
}

// requestIDFromMetadata reads the id the gateway forwarded from the HTTP request.
func requestIDFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if v := md.Get(RequestIDMetadataKey); len(v) > 0 {
		return sanitizeRequestID(v[0])
	}
	return ""
}

// sanitizeRequestID keeps an incoming id only when it is short and made of id-like characters.
func sanitizeRequestID(s string) string {
	if s == "" || len(s) > 64 {
		return ""
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			return ""
		}
	}
	return s
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
