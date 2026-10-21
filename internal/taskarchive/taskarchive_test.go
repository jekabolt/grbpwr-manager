package taskarchive

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

type fakeTasks struct {
	dependency.Tasks
	closedAt   map[int]time.Time
	listErr    error
	archiveErr map[int]error
	cutoffs    []time.Time
	archived   []int
}

func (f *fakeTasks) ListTaskIdsToAutoArchive(_ context.Context, cutoff time.Time) ([]int, error) {
	f.cutoffs = append(f.cutoffs, cutoff)
	if f.listErr != nil {
		return nil, f.listErr
	}
	var ids []int
	for id, closedAt := range f.closedAt {
		if closedAt.Before(cutoff) {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	return ids, nil
}

func (f *fakeTasks) ArchiveTask(_ context.Context, id int) error {
	f.archived = append(f.archived, id)
	return f.archiveErr[id]
}

func TestRunOnceUsesConfiguredRetentionAndArchiveTaskPath(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	retention := 14 * 24 * time.Hour
	fake := &fakeTasks{
		closedAt: map[int]time.Time{
			1: now.Add(-retention - time.Second),
			2: now.Add(-retention), // the boundary is half-open: exactly 14d is not older yet
			3: now.Add(-retention + time.Second),
		},
		archiveErr: map[int]error{},
	}
	w := New(&Config{RetentionPeriod: retention}, fake)
	w.now = func() time.Time { return now }

	if ok := w.runOnce(context.Background()); !ok {
		t.Fatal("clean auto-archive tick reported failure")
	}
	if len(fake.cutoffs) != 1 || !fake.cutoffs[0].Equal(now.Add(-retention)) {
		t.Fatalf("cutoffs = %v, want [%v]", fake.cutoffs, now.Add(-retention))
	}
	if len(fake.archived) != 1 || fake.archived[0] != 1 {
		t.Fatalf("ArchiveTask calls = %v, want [1]", fake.archived)
	}
}

func TestRunOnceTreatsAlreadyArchivedRaceAsSuccess(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	fake := &fakeTasks{
		closedAt:   map[int]time.Time{7: now.Add(-30 * 24 * time.Hour)},
		archiveErr: map[int]error{7: sql.ErrNoRows},
	}
	w := New(nil, fake)
	w.now = func() time.Time { return now }

	if ok := w.runOnce(context.Background()); !ok {
		t.Fatal("a competing worker's already-archived result must be a successful tick")
	}
}

func TestRunOnceContinuesAfterOneArchiveFailure(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	fake := &fakeTasks{
		closedAt: map[int]time.Time{
			4: now.Add(-30 * 24 * time.Hour),
			5: now.Add(-31 * 24 * time.Hour),
		},
		archiveErr: map[int]error{4: errors.New("write failed")},
	}
	w := New(nil, fake)
	w.now = func() time.Time { return now }

	if ok := w.runOnce(context.Background()); ok {
		t.Fatal("partial archive failure reported a successful tick")
	}
	if len(fake.archived) != 2 {
		t.Fatalf("ArchiveTask calls = %v, want both candidates despite one failure", fake.archived)
	}
}

func TestDefaultRetentionIsFourteenDays(t *testing.T) {
	if got := DefaultConfig().RetentionPeriod; got != 14*24*time.Hour {
		t.Fatalf("default retention = %s, want 14d", got)
	}
}
