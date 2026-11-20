// Package storagemeter counts each team's file storage against its cap (teams wave, lane X11). The
// bucket asks Reserve before every write and Add after it (bucket/meter.go); a daily recount lists
// the bucket once and writes the true numbers, so a missed decrement or a crash heals by itself.
package storagemeter

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Store is the control-plane side (platformstore.Store).
type Store interface {
	ReserveStorage(ctx context.Context, teamID, n, defCap int64) (bool, error)
	AddStorage(ctx context.Context, teamID, delta int64) error
	StorageTeams(ctx context.Context) ([]platformstore.StorageTeam, error)
	SetRecountedStorage(ctx context.Context, used map[int64]int64, now time.Time) error
	LockStorageRecount(ctx context.Context) (release func(), ok bool, err error)
}

// Lister is the bucket side of the recount (bucket.Bucket.SumByTeam).
type Lister interface {
	SumByTeam(ctx context.Context) (bucket.StorageTotals, error)
}

// Meter implements bucket.StorageMeter over the control plane.
type Meter struct {
	st     Store
	defCap int64
}

// New: defCap = the cap of a team with no cap of its own (bytes, 0 = no cap).
func New(st Store, defCap int64) *Meter { return &Meter{st: st, defCap: defCap} }

// Reserve: ErrStorageFull when the team is at its cap.
func (m *Meter) Reserve(ctx context.Context, teamID, n int64, stream bool) error {
	if stream {
		n = 0
	}
	ok, err := m.st.ReserveStorage(ctx, teamID, n, m.defCap)
	if err != nil {
		return err
	}
	if !ok {
		return bucket.ErrStorageFull
	}
	return nil
}

// Add moves the count.
func (m *Meter) Add(ctx context.Context, teamID, delta int64) error {
	return m.st.AddStorage(ctx, teamID, delta)
}

// ErrRecountBusy: another app instance holds the recount lock; this one skips its turn.
var ErrRecountBusy = errors.New("storagemeter: another instance is recounting")

// Recount lists the bucket once and writes every team's true number: team #1 gets the legacy keys,
// every other team the bytes under its file key (0 when it has none). One instance at a time
// (LockStorageRecount). Uploads and deletes that land while the listing runs may be counted twice
// or lost; the next recount (24 h) settles them — the drift is bounded by one day of traffic.
func Recount(ctx context.Context, st Store, ls Lister, now time.Time) (map[int64]int64, error) {
	release, ok, err := st.LockStorageRecount(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrRecountBusy
	}
	defer release()
	totals, err := ls.SumByTeam(ctx)
	if err != nil {
		return nil, err
	}
	teams, err := st.StorageTeams(ctx)
	if err != nil {
		return nil, err
	}
	used := make(map[int64]int64, len(teams))
	for _, t := range teams {
		switch {
		case t.ID == tenant.TeamOneID:
			used[t.ID] = totals.TeamOne
		case t.FileKey.Valid:
			used[t.ID] = totals.ByFileKey[t.FileKey.String]
		default:
			used[t.ID] = 0
		}
	}
	if err := st.SetRecountedStorage(ctx, used, now); err != nil {
		return nil, err
	}
	return used, nil
}

// Worker runs Recount after a first delay and then every interval.
type Worker struct {
	st       Store
	ls       Lister
	first    time.Duration
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
	once     sync.Once
}

// NewWorker: the recount at boot+first, then every interval.
func NewWorker(st Store, ls Lister, first, interval time.Duration) *Worker {
	return &Worker{st: st, ls: ls, first: first, interval: interval, stop: make(chan struct{}), done: make(chan struct{})}
}

// Start runs the loop in the background; ctx carries team #1 (the app root).
func (w *Worker) Start(ctx context.Context) {
	go func() {
		defer close(w.done)
		t := time.NewTimer(w.first)
		defer t.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			w.tick(ctx)
			t.Reset(w.interval)
		}
	}()
}

func (w *Worker) tick(ctx context.Context) {
	tctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	start := time.Now()
	used, err := Recount(tctx, w.st, w.ls, start.UTC().Truncate(time.Millisecond))
	if errors.Is(err, ErrRecountBusy) {
		slog.Default().InfoContext(ctx, "storage recount skipped: another instance holds it")
		return
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "storage recount failed", slog.String("err", err.Error()))
		return
	}
	var total int64
	for _, n := range used {
		total += n
	}
	slog.Default().InfoContext(ctx, "storage recount done", slog.Int("teams", len(used)),
		slog.Int64("bytes", total), slog.Duration("took", time.Since(start)))
}

// Stop ends the loop and waits for a running recount.
func (w *Worker) Stop() {
	w.once.Do(func() { close(w.stop) })
	<-w.done
}
