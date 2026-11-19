package app

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

type workerFakeDirectory struct {
	teams []tenant.Team
	err   error
}

func (d workerFakeDirectory) ByID(context.Context, int64) (tenant.Team, error) {
	return tenant.Team{}, tenant.ErrNotFound
}
func (d workerFakeDirectory) BySlug(context.Context, string) (tenant.Team, error) {
	return tenant.Team{}, tenant.ErrNotFound
}
func (d workerFakeDirectory) ListActive(context.Context) ([]tenant.Team, error) {
	return d.teams, d.err
}
func (workerFakeDirectory) Invalidate(int64) {}

func workerTeam(id int64, status tenant.Status) tenant.Team {
	return tenant.Team{ID: id, Slug: string(rune('a' + id - 1)), DBName: "teamdb", Status: status}
}

func TestTeamWorkerTickVisitsApplicableTeamsAndIsolatesFailure(t *testing.T) {
	dir := workerFakeDirectory{teams: []tenant.Team{
		workerTeam(1, tenant.StatusActive),
		workerTeam(2, tenant.StatusSuspended),
		workerTeam(3, tenant.StatusProvisioning),
		workerTeam(4, tenant.StatusActive),
	}}
	var mu sync.Mutex
	var seen []int64
	ok := runTeamWorkerTick(context.Background(), dir, "test", activeAndSuspendedTeams, 2,
		func(ctx context.Context) bool {
			team, bound := tenant.From(ctx)
			if !bound {
				t.Error("tick context has no team")
				return false
			}
			mu.Lock()
			seen = append(seen, team.ID)
			mu.Unlock()
			return team.ID != 2
		})
	if ok {
		t.Fatal("one team failed but the tick reported success")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("visited %v, want active and suspended teams", seen)
	}
	for _, want := range []int64{1, 2, 4} {
		found := false
		for _, got := range seen {
			found = found || got == want
		}
		if !found {
			t.Errorf("team %d was not visited: %v", want, seen)
		}
	}
}

func TestTeamWorkerTickActiveScopeSkipsSuspended(t *testing.T) {
	dir := workerFakeDirectory{teams: []tenant.Team{
		workerTeam(1, tenant.StatusActive), workerTeam(2, tenant.StatusSuspended),
	}}
	var seen []int64
	if ok := runTeamWorkerTick(context.Background(), dir, "test", activeTeams, 1, func(ctx context.Context) bool {
		team, _ := tenant.From(ctx)
		seen = append(seen, team.ID)
		return true
	}); !ok {
		t.Fatal("clean active-team tick failed")
	}
	if len(seen) != 1 || seen[0] != 1 {
		t.Fatalf("visited %v, want team 1 only", seen)
	}
}

func TestTeamWorkerTickBoundsConcurrency(t *testing.T) {
	const parallel = 2
	dir := workerFakeDirectory{teams: []tenant.Team{
		workerTeam(1, tenant.StatusActive), workerTeam(2, tenant.StatusActive),
		workerTeam(3, tenant.StatusActive), workerTeam(4, tenant.StatusActive),
	}}
	entered := make(chan struct{}, len(dir.teams))
	release := make(chan struct{})
	done := make(chan bool, 1)
	var current atomic.Int32
	var maximum atomic.Int32
	go func() {
		done <- runTeamWorkerTick(context.Background(), dir, "test", activeTeams, parallel,
			func(context.Context) bool {
				n := current.Add(1)
				for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
				}
				entered <- struct{}{}
				<-release
				current.Add(-1)
				return true
			})
	}()
	for i := 0; i < parallel; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("workers did not enter")
		}
	}
	select {
	case <-entered:
		t.Fatal("more than the concurrency bound entered")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if ok := <-done; !ok {
		t.Fatal("bounded tick failed")
	}
	if got := maximum.Load(); got != parallel {
		t.Fatalf("maximum concurrency = %d, want %d", got, parallel)
	}
}

func TestTeamWorkerTickLegacyStaticDirectoryRunsTeamOneOnce(t *testing.T) {
	dir, err := tenant.NewStaticDirectory(tenant.TeamOne("grbpwr"))
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	if ok := runTeamWorkerTick(context.Background(), dir, "test", activeTeams, 4, func(ctx context.Context) bool {
		calls++
		team, bound := tenant.From(ctx)
		return bound && team.ID == tenant.TeamOneID
	}); !ok {
		t.Fatal("legacy tick failed")
	}
	if calls != 1 {
		t.Fatalf("legacy calls = %d, want 1", calls)
	}
}

func TestTeamWorkerTickCancellation(t *testing.T) {
	dir := workerFakeDirectory{teams: []tenant.Team{workerTeam(1, tenant.StatusActive)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	if ok := runTeamWorkerTick(ctx, dir, "test", activeTeams, 1, func(context.Context) bool {
		calls.Add(1)
		return true
	}); ok || calls.Load() != 0 {
		t.Fatalf("pre-cancelled tick: ok=%v calls=%d", ok, calls.Load())
	}

	ctx, cancel = context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan bool, 1)
	go func() {
		done <- runTeamWorkerTick(ctx, dir, "test", activeTeams, 1, func(ctx context.Context) bool {
			close(entered)
			<-ctx.Done()
			return false
		})
	}()
	<-entered
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled in-flight tick reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled in-flight tick did not return")
	}
}

func TestTeamWorkerTickDirectoryFailure(t *testing.T) {
	boom := errors.New("boom")
	if runTeamWorkerTick(context.Background(), workerFakeDirectory{err: boom}, "test", activeTeams, 1,
		func(context.Context) bool { return true }) {
		t.Fatal("directory failure reported success")
	}
}
