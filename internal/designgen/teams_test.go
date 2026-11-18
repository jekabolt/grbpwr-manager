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

// teamTrail records which team each queue verb ran for; failTeam's revive fails.
type teamTrail struct {
	runStore
	mu       sync.Mutex
	revived  []int64
	claimed  []int64
	failTeam int64
}

func teamOf(ctx context.Context) int64 {
	t, _ := tenant.From(ctx)
	return t.ID
}

func (s *teamTrail) ReviveExpiredRuns(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revived = append(s.revived, teamOf(ctx))
	if teamOf(ctx) == s.failTeam {
		return 0, errors.New("this team's database is down")
	}
	return 0, nil
}

func (s *teamTrail) ClaimRuns(ctx context.Context, _ int, _ time.Duration, _ string) ([]entity.DesignRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed = append(s.claimed, teamOf(ctx))
	return nil, nil
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
	tOne = tenant.TeamOne("team1db")
	tTwo = tenant.Team{ID: 2, Slug: "ostra", Status: tenant.StatusActive}
)

func TestWorkerTickRunsEveryTeamAndSurvivesOneFailure(t *testing.T) {
	st := &teamTrail{runStore: &fakeStore{}, failTeam: 1}
	c := DefaultConfig()
	applyDefaults(&c)
	w := newWorker(&c, st, fakeMedia{}, newFakeSink(ContentTypePNG), Providers{})
	w.SetTeamLoop(loopOver(tOne, tTwo))

	require.True(t, w.tick(context.Background()), "one healthy team is enough not to back off")
	require.Equal(t, []int64{1, 2}, st.revived)
	require.Equal(t, []int64{2}, st.claimed, "team 2 is claimed although team #1 failed before it")

	st.failTeam = 0
	require.True(t, w.tick(context.Background()))
	require.Equal(t, []int64{2, 1, 2}, st.claimed)
}

func TestWorkerTickBacksOffWhenEveryTeamFails(t *testing.T) {
	st := &teamTrail{runStore: &fakeStore{}, failTeam: 1}
	c := DefaultConfig()
	applyDefaults(&c)
	w := newWorker(&c, st, fakeMedia{}, newFakeSink(ContentTypePNG), Providers{})
	w.SetTeamLoop(loopOver(tOne))
	require.False(t, w.tick(context.Background()), "teams mode off = one team: today's rule")

	w.SetTeamLoop(func(context.Context, func(context.Context) error) error { return errors.New("no list") })
	require.False(t, w.tick(context.Background()), "an unreadable team list backs off")
}

func TestWorkerKeepsALedgerSweepClockPerTeam(t *testing.T) {
	st := &teamTrail{runStore: &fakeStore{}}
	c := DefaultConfig()
	applyDefaults(&c)
	w := newWorker(&c, st, fakeMedia{}, newFakeSink(ContentTypePNG), Providers{})
	l := &countingSweeper{}
	w.ledger = l
	w.SetTeamLoop(loopOver(tOne, tTwo))
	w.tick(context.Background())
	w.tick(context.Background())
	require.Equal(t, map[int64]int{1: 1, 2: 1}, l.byTeam(), "each team swept once per ledgerSweepEvery, not one sweep for all")
}

func TestSweeperTickSweepsEveryTeam(t *testing.T) {
	st := &teamTrail{failTeam: 1}
	s := newSweeper(st, time.Hour)
	s.SetTeamLoop(loopOver(tOne, tTwo))
	s.tick(context.Background())
	require.Equal(t, []int64{1, 2}, st.revived, "team 2 is swept although team #1 failed")
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
