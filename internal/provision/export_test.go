package provision

import (
	"context"
	"time"
)

// SetSeedForTest replaces the seeder (unit tests without a database).
func (w *Worker) SetSeedForTest(f func(ctx context.Context, ownerHandle string) error) { w.seed = f }

// SetNowForTest replaces the clock.
func (w *Worker) SetNowForTest(now func() time.Time) { w.now = now }
