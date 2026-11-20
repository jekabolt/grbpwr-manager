package designgen

import (
	"context"
	"fmt"
)

// SetStorageGate hangs the team's storage cap on the worker (lane X11): gate answers non-nil when
// the team in ctx is at its cap (bucket.Bucket.StorageRoom). Asked in the pre-flight of a fresh run,
// before any attempt row and therefore before any money; a paid job being collected is never
// stopped (its bytes were bought). nil = no gate. Call before Start.
func (w *Worker) SetStorageGate(gate func(ctx context.Context) error) { w.storageGate = gate }

func (w *Worker) storageCheck(ctx context.Context) error {
	if w.storageGate == nil {
		return nil
	}
	if err := w.storageGate(ctx); err != nil {
		return fmt.Errorf("%w: %v", errTeamStorageFull, err)
	}
	return nil
}
