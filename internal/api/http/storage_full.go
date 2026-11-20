package httpapi

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// storageFullUnary answers every call that hit the team's storage cap with one code (lane X11):
// RESOURCE_EXHAUSTED "storage_full", whatever status the handler wrapped the error in — the client
// shows one toast, never a 500.
func storageFullUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err != nil && bucket.IsStorageFull(err) {
			return nil, status.Error(codes.ResourceExhausted, "storage_full")
		}
		return resp, err
	}
}
