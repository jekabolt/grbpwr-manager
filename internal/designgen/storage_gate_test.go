package designgen

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestStorageGate(t *testing.T) {
	w := &Worker{}
	if err := w.storageCheck(context.Background()); err != nil {
		t.Fatalf("no gate: %v", err)
	}
	w.SetStorageGate(func(context.Context) error { return nil })
	if err := w.storageCheck(context.Background()); err != nil {
		t.Fatalf("room left: %v", err)
	}
	w.SetStorageGate(func(context.Context) error { return errors.New("full") })
	err := w.storageCheck(context.Background())
	if !errors.Is(err, errTeamStorageFull) {
		t.Fatalf("full team: %v", err)
	}
	v := classify(err)
	if v.Retryable || v.Code != CodeStorageFull || v.State != entity.DesignAttemptFailed {
		t.Fatalf("classify: %+v", v)
	}
}
