package admin

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStorageRoomCheck(t *testing.T) {
	s := &Server{}
	if err := s.storageRoomCheck(context.Background()); err != nil {
		t.Fatalf("no check: %v", err)
	}
	s.SetStorageRoom(func(context.Context) error { return nil })
	if err := s.storageRoomCheck(context.Background()); err != nil {
		t.Fatalf("room left: %v", err)
	}
	s.SetStorageRoom(func(context.Context) error { return errors.New("full") })
	err := s.storageRoomCheck(context.Background())
	if status.Code(err) != codes.ResourceExhausted || status.Convert(err).Message() != "storage_full" {
		t.Fatalf("full team: %v", err)
	}
}
