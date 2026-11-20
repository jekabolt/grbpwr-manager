package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/storagemeter"
)

// Storage cap (lane X11): the bucket counts every team's bytes against its cap through the control
// plane, and a daily recount lists the bucket once so the numbers heal. Teams mode only.
const (
	storageRecountFirst    = 10 * time.Minute
	storageRecountInterval = 24 * time.Hour
)

// startStorageMeter turns the cap on in the bucket and starts the recount; nil stop when off.
func (a *App) startStorageMeter(ctx context.Context) func() {
	bk, ok := a.b.(*bucket.Bucket)
	if a.platform == nil || !ok {
		return nil
	}
	def := a.c.Platform.StorageCapDefault()
	bk.SetStorageMeter(storagemeter.New(a.platform, def))
	w := storagemeter.NewWorker(a.platform, bk, storageRecountFirst, storageRecountInterval)
	w.Start(ctx)
	slog.Default().InfoContext(ctx, "teams: storage cap on", slog.Int64("default_cap_bytes", def))
	return w.Stop
}
