package designgen

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// teamTrail records which team each queue verb ran for; failTeam's revive fails, hangTeam's revive
// waits for its ctx, claims[team] is what that team's ClaimRuns returns (once).
type teamTrail struct {
	runStore
	mu       sync.Mutex
	revived  []int64
	claimed  []int64
	failTeam int64
	hangTeam int64
	claims   map[int64][]entity.DesignRun
}

func teamOf(ctx context.Context) int64 {
	t, _ := tenant.From(ctx)
	return t.ID
}

func (s *teamTrail) ReviveExpiredRuns(ctx context.Context) (int, error) {
	s.mu.Lock()
	s.revived = append(s.revived, teamOf(ctx))
	fail, hang := s.failTeam, s.hangTeam
	s.mu.Unlock()
	if teamOf(ctx) == hang {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	if teamOf(ctx) == fail {
		return 0, errors.New("this team's database is down")
	}
	return 0, nil
}

func (s *teamTrail) CloseOverdueRuns(context.Context, entity.DesignOverdueSweep) (int, error) {
	return 0, nil
}

func (s *teamTrail) ClaimRuns(ctx context.Context, _ int, _ time.Duration, _ string) ([]entity.DesignRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := teamOf(ctx)
	s.claimed = append(s.claimed, id)
	runs := s.claims[id]
	delete(s.claims, id)
	return runs, nil
}

func (s *teamTrail) seen() (revived, claimed []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.revived...), append([]int64(nil), s.claimed...)
}

func loopOver(teams ...tenant.Team) TeamLoop {
	return func(ctx context.Context, fn func(ctx context.Context) error) error {
		for _, t := range teams {
			_ = fn(tenant.With(ctx, t))
		}
		return nil
	}
}

var (
	tOne   = tenant.TeamOne("team1db")
	tTwo   = tenant.Team{ID: 2, Slug: "ostra", Status: tenant.StatusActive}
	tSleep = tenant.Team{ID: 3, Slug: "sleepy", Status: tenant.StatusSuspended}
)

func teamsWorker(t *testing.T, st *teamTrail, cleanup, work TeamLoop, parallel int) (*Worker, *time.Time) {
	t.Helper()
	c := DefaultConfig()
	applyDefaults(&c)
	w := newWorker(&c, st, fakeMedia{}, newFakeSink(ContentTypePNG), Providers{})
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	w.now = func() time.Time { return now }
	w.SetTeamLoops(cleanup, work, parallel)
	return w, &now
}

// Cleanup runs for every team the cleanup loop lists — the suspended one included — while only the
// teams of the work loop claim.
func TestTeamsTickCleansUpSuspendedTeamsButClaimsOnlyWorkTeams(t *testing.T) {
	st := &teamTrail{runStore: &fakeStore{}}
	w, _ := teamsWorker(t, st, loopOver(tOne, tTwo, tSleep), loopOver(tTwo), 2)
	require.True(t, w.tick(context.Background()))
	revived, claimed := st.seen()
	require.Equal(t, []int64{1, 2, 3}, revived, "the suspended team's dead leases are revived")
	require.Equal(t, []int64{2}, claimed, "only the work team claims")
}

// A failing team backs off alone; the others are served every tick, and the failing one comes back
// once its backoff is over.
func TestTeamsTickFailingTeamBacksOffAlone(t *testing.T) {
	st := &teamTrail{runStore: &fakeStore{}, failTeam: 1}
	w, now := teamsWorker(t, st, loopOver(tOne, tTwo), loopOver(tOne, tTwo), 2)
	require.True(t, w.tick(context.Background()), "one team's failure never backs the worker off")
	revived, claimed := st.seen()
	require.Equal(t, []int64{1, 2}, revived)
	require.Equal(t, []int64{2}, claimed, "team #1 failed its cleanup: no claim this tick")

	require.True(t, w.tick(context.Background()))
	revived, _ = st.seen()
	require.Equal(t, []int64{1, 2, 2}, revived, "team #1 is backing off; team 2 goes on")

	st.mu.Lock()
	st.failTeam = 0
	st.mu.Unlock()
	*now = now.Add(backoffBase + time.Second)
	require.True(t, w.tick(context.Background()))
	revived, claimed = st.seen()
	require.Equal(t, []int64{1, 2, 2, 1, 2}, revived)
	require.Equal(t, []int64{2, 2, 1, 2}, claimed, "back after its backoff")
}

// The provider phase runs outside the walk: team #1's batch hangs, yet the tick returns and team 2
// claims; with one slot nobody else claims while it runs, and team #1 claims no second batch.
func TestTeamsTickProviderCallIsNotInTheWalk(t *testing.T) {
	st := &teamTrail{runStore: &fakeStore{}, claims: map[int64][]entity.DesignRun{1: {{Id: 7}}}}
	w, _ := teamsWorker(t, st, nil, loopOver(tOne, tTwo), 1)
	release, started := make(chan struct{}), make(chan int64, 1)
	w.runBatch = func(ctx context.Context, runs []entity.DesignRun, _ string) error {
		started <- teamOf(ctx)
		<-release
		return nil
	}
	done := make(chan bool, 1)
	go func() { done <- w.tick(context.Background()) }()
	select {
	case ok := <-done:
		require.True(t, ok)
	case <-time.After(2 * time.Second):
		t.Fatal("the tick waited for team #1's provider phase")
	}
	require.Equal(t, int64(1), <-started)
	_, claimed := st.seen()
	require.Equal(t, []int64{1}, claimed, "the only slot is busy: team 2 waits for the next tick")

	require.True(t, w.tick(context.Background()))
	_, claimed = st.seen()
	require.Equal(t, []int64{1}, claimed, "team #1 is busy and the slot is taken")

	close(release)
	w.wg.Wait()
	require.True(t, w.tick(context.Background()))
	_, claimed = st.seen()
	require.Equal(t, []int64{1, 1, 2}, claimed, "slot free again: both teams claim")
}

func TestWorkerKeepsALedgerSweepClockPerTeam(t *testing.T) {
	st := &teamTrail{runStore: &fakeStore{}}
	w, _ := teamsWorker(t, st, loopOver(tOne, tTwo), loopOver(tOne, tTwo), 2)
	l := &countingSweeper{}
	w.ledger = l
	w.tick(context.Background())
	w.tick(context.Background())
	require.Equal(t, map[int64]int{1: 1, 2: 1}, l.byTeam(), "each team swept once per ledgerSweepEvery, not one sweep for all")
}

func TestWorkerTickBacksOffWhenTheTeamListFails(t *testing.T) {
	st := &teamTrail{runStore: &fakeStore{}}
	broken := TeamLoop(func(context.Context, func(context.Context) error) error { return errors.New("no list") })
	w, _ := teamsWorker(t, st, broken, broken, 2)
	require.False(t, w.tick(context.Background()), "an unreadable team list backs off")
}

func TestSweeperTickSweepsEveryTeamAndBoundsEach(t *testing.T) {
	st := &teamTrail{hangTeam: 1}
	s := newSweeper(st, time.Hour)
	s.teamTimeout = 50 * time.Millisecond
	s.SetTeamLoop(loopOver(tOne, tTwo, tSleep))
	start := time.Now()
	s.tick(context.Background())
	require.Less(t, time.Since(start), 2*time.Second, "a hanging team costs its timeout, not forever")
	revived, _ := st.seen()
	require.Equal(t, []int64{1, 2, 3}, revived, "teams after the hanging one are swept")

	s.tick(context.Background())
	revived, _ = st.seen()
	require.Equal(t, []int64{1, 2, 3, 2, 3}, revived, "the hanging team backs off alone")
}

// countingSweeper is a ledger whose only used verb is Sweep, counted per team.
type countingSweeper struct {
	callLedger
	mu sync.Mutex
	n  map[int64]int
}

func (c *countingSweeper) Sweep(ctx context.Context, _ time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[int64]int{}
	}
	c.n[teamOf(ctx)]++
	return 0, nil
}

func (c *countingSweeper) byTeam() map[int64]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
