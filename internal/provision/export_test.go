package provision

import (
	"context"
	"time"
)

// SetSeedForTest replaces the seeder (unit tests without a database).
// The worker still checks its lease guard before and after it, as it does around Seed.
func (w *Worker) SetSeedForTest(f func(ctx context.Context, ownerHandle string) error) {
	w.seed = func(ctx context.Context, owner string, _ func(context.Context) error) error { return f(ctx, owner) }
}

// SetNowForTest replaces the clock.
func (w *Worker) SetNowForTest(now func() time.Time) { w.now = now }
