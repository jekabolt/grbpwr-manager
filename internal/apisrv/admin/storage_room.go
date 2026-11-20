package admin

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetStorageRoom hangs the team's storage cap on the paid and import doors (lane X11): room answers
// non-nil when the team in ctx is at its cap (bucket.Bucket.StorageRoom). Call before serving.
func (s *Server) SetStorageRoom(room func(ctx context.Context) error) { s.storageRoom = room }

// storageRoomCheck: RESOURCE_EXHAUSTED storage_full when the team is at its cap; nil without a gate.
func (s *Server) storageRoomCheck(ctx context.Context) error {
	if s.storageRoom == nil {
		return nil
	}
	if s.storageRoom(ctx) != nil {
		return status.Error(codes.ResourceExhausted, "storage_full")
	}
	return nil
}
